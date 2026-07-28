# Install and deploy

Standing this module up on real servers, from a bare VPS to a verified first send — then keeping it
running through box changes, redeploys, and rollbacks.

**Target:** a generic Ubuntu-style VPS. Nothing here is cloud-provider-specific. Where a managed
service is a reasonable substitute (Postgres, KMS), that is called out.

**This document does not restate config keys or endpoint shapes.** Every config key's meaning and
default is in [`config-reference.md`](./config-reference.md); every request/response shape is in
[`api-contract.md`](./api-contract.md). When a step here says "set X", the reference is where you
look up what X means.

**Before a first LIVE deployment**, also work through [`go-live-checklist.md`](./go-live-checklist.md)
— it covers the platform-coordination sign-offs (KMS/IAM separation, advisory-lock namespace,
platform table shapes) that this guide assumes are settled.

---

## Part 1 — Prerequisites

### What you need before starting

| Requirement | Notes |
|---|---|
| **A VPS for the Brain** | 2 vCPU / 2 GB RAM is comfortable for V1 volumes. The Brain is a single process and is not memory-hungry; see [`capacity-planning.md`](./capacity-planning.md) before sizing for scale. |
| **One or more VPSes for boxes** | 1 vCPU / 512 MB each is sufficient — a box is a thin SMTP relay. Start with one. |
| **A private network between them** | Essential. See Part 4 — the box↔Brain channel must not traverse the public internet, and no box port is ever publicly reachable. |
| **PostgreSQL 16+** | Managed (Supabase, RDS, etc.) or self-hosted. The module owns the `sender` schema; the platform owns `public`. |
| **A 32-byte master encryption key** | Wraps SMTP credentials. In production this should be a real KMS key. See Part 3 and the key-backup section of [`disaster-recovery.md`](./disaster-recovery.md) **before you create it** — losing this key is unrecoverable. |
| **SMTP account credentials** | At least one working account (host, port, username, password). |
| **`public.users` and `public.job_items`** | Platform-owned tables the module reads. They must exist before migrating; the preflight fails loudly if they do not. |

### Software on each host

```bash
# Both Brain and box hosts:
sudo apt-get update
sudo apt-get install -y ca-certificates curl gnupg git jq
# jq is not optional in practice: the admin/health API returns compact single-line JSON, and every
# diagnostic in operator-runbook.md pipes through it. Install it before you need it at 3am.

# Docker (the supported deployment shape — same images CI tests):
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"   # log out and back in for this to take effect
```

Verify: `docker --version && docker compose version`.

> **Running without Docker** is possible — the Brain is a static Go binary, the box is Node.js —
> but the containers are what CI exercises, so they are what this guide covers.

### Decide your topology first

The smallest sane production shape:

```
              ┌───────────────────────────────┐
  platform ──►│  BRAIN VPS                    │
  (Domain A)  │   :8080  HTTP  API + health    │◄── operator (Domain B)
              │   :8443  mTLS  register/hb     │
              │   :8444  mTLS  send stream     │
              └───────┬───────────────────────┘
                      │  private network only
        ┌─────────────┼─────────────┐
        ▼             ▼             ▼
   ┌─────────┐  ┌─────────┐  ┌─────────┐
   │ BOX VPS │  │ BOX VPS │  │ BOX VPS │   boxes open OUTBOUND connections only.
   │  (no    │  │  (no    │  │  (no    │   No inbound port. Not publicly routable.
   │ inbound)│  │ inbound)│  │ inbound)│
   └────┬────┘  └────┬────┘  └────┬────┘
        └────────────┴────────────┘
                     ▼
              SMTP providers (:587 / :465)

        Postgres: reachable from the BRAIN ONLY. Boxes have no DB access, ever.
```

---

## Part 2 — First-time setup, in order

Each step depends on the one before it. Do not skip ahead; the preflight and healthchecks exist to
catch out-of-order setup, but some ordering (KMS key before any credential is stored) cannot be
recovered from afterward.

### Step 1 — Get the code onto the Brain host

```bash
git clone <your-repo-url> sender-module
cd sender-module
```

### Step 2 — Confirm the database connection

You need two distinct Postgres roles. This separation is load-bearing, not a style preference:

| Role | Used by | Capability |
|---|---|---|
| **migration admin** | `tools/sender-migrate` only | admin/owner-capable: must `CREATE ROLE`, `CREATE SCHEMA`, and assign ownership to `sender_owner` |
| **`sender_module`** | the Brain, at runtime | login-only, non-superuser, holds only enumerated grants. Never runs DDL. |

The migration admin credential comes from your secret store and is used only during migration runs.
Confirm which real role it is before going live (go-live checklist item 3).

Verify the platform tables exist and you can reach them:

```bash
psql "$MIGRATE_DATABASE_URL" -c "\d public.users" -c "\d public.job_items"
```

If either is missing, stop — deploy the platform schema first. The preflight will refuse to migrate
without them.

> **Also confirm the column shapes**, not just existence. The preflight deliberately checks
> existence only (a column-level check would false-fail every time the platform adds an unrelated
> column). The shapes the module assumes are in go-live checklist item 2. A mismatch found after
> migrating is much more expensive than one found now.

### Step 3 — Create the master encryption key

This key wraps every SMTP credential. **Read the key-backup section of
[`disaster-recovery.md`](./disaster-recovery.md) before you create it** — if it is lost, every
stored credential is permanently unrecoverable and must be re-entered by hand.

**Production (recommended): a real KMS key.** Create a key whose IAM policy grants `Decrypt` to the
Brain's runtime identity **and to nothing else** — not to operators, not to the platform, not to
your CI. That separation is what makes "the platform cannot read credentials" true, and it is not
checkable from code (go-live checklist item 4).

**Self-hosted / file-backed key** (what the dev stack uses, viable for a small deployment where you
control the host):

```bash
sudo mkdir -p /etc/sender/kms
sudo openssl rand -out /etc/sender/kms/master.key 32
sudo chmod 400 /etc/sender/kms/master.key
sudo chown <brain-user>:<brain-user> /etc/sender/kms/master.key
```

Then **back it up off-host immediately**, before storing any credential. See
[`disaster-recovery.md`](./disaster-recovery.md).

Set `CREDENTIAL_KEY_ID` to a version label for this key (e.g. `cred-dek-v1`). It is stamped into
every encrypted row and is what makes rotation possible later — see the rotation procedure in
[`disaster-recovery.md`](./disaster-recovery.md).

### Step 4 — Generate mTLS material

The box↔Brain channel is mutual TLS in both directions. You need a CA, a server leaf for the Brain,
and a client leaf per box.

```bash
bash tools/gen-certs.sh
# writes secrets/dev/certs/: ca.crt ca.key brain.crt brain.key box.crt box.key sink.crt sink.key
```

> **`tools/gen-certs.sh` is explicitly DEV-ONLY** — it says so in its own header. It self-signs a
> CA that never leaves the machine and issues 10-year leaves with no revocation path. For
> production, issue the leaves from a CA you actually manage (your internal PKI, `step-ca`, Vault,
> or your cloud's private CA). A managed cert PKI with rotation and revocation is deferred work,
> not built — see [`future-work.md`](./future-work.md).

Whatever issues them, the requirements are fixed:

- The Brain needs its server cert + key + the CA cert.
- Each box needs its own client cert + key + the CA cert.
- The Brain's cert must carry a SAN matching the hostname boxes connect to, and the box must be
  configured with that same name (`BRAIN_SERVER_NAME`).
- **A box host never receives `ca.key`, `brain.key`, or the KMS key.** Distribute only that box's
  own leaf. This is enforced in the compose mounts and worth preserving in any other deployment
  shape.

Remember: **the cert is transport, not authority.** A valid cert alone gets a box nothing — the
registration token is what authorizes it (Step 7).

### Step 5 — Run the migrations

Migrations are centrally owned, contiguous, self-recording, and applied in ascending order. The
runner takes an advisory lock so two migrators cannot race.

```bash
export MIGRATE_DATABASE_URL='postgres://<admin-role>:<pw>@<host>:5432/<db>'
export SENDER_MODULE_PASSWORD='<a strong password for the app role>'

# Inspect first — shows applied vs pending, changes nothing:
docker compose run --rm --no-deps migrate status

# Apply the chain, then set the app role's password out-of-band:
docker compose run --rm --no-deps migrate up-and-set-password
```

> **`--no-deps` is not optional, and omitting it is destructive.** The `migrate` service
> `depends_on` the dev `postgres` service, so without `--no-deps`, `docker compose run` will start
> — and can **recreate** — that container. Against a production external database that is merely
> pointless; against a local stack it silently destroys your data volume. This was found by running
> the command as first written while drafting this guide: it recreated the postgres container and
> wiped the dev database. `--no-deps` runs the one-shot alone, which is what you want in every
> environment where Postgres is not managed by this compose file.
>
> Verify what you are about to touch before running `up` anywhere real: `status` is read-only, so
> run it first, always.

What that does: runs the preflight (platform tables exist?), applies every pending
`sender_NNNN_*.up.sql` in order, records each in `sender.schema_migrations`, creates the
non-login `sender_owner` (schema owner, so `FORCE ROW LEVEL SECURITY` actually enforces) and the
login-only `sender_module` app role, and seeds all 53 config keys with their documented defaults.

The role password is set via `set-app-password` rather than in SQL specifically so it never lands
in a migration file or the DB's statement logs.

**Migrations are forward-only in production.** `down` refuses to run unless you explicitly set
`SENDER_MIGRATE_ALLOW_DOWN=true`. Do not casually override that — see the rollback section (Part 5).

Confirm: `docker compose run --rm --no-deps migrate status` shows every version applied and none pending.

### Step 6 — Start the Brain

Set the Brain's environment (Part 4 has a complete systemd unit; the full variable list is in
`.env.example`). The essentials:

```bash
DATABASE_URL=postgres://sender_module:<pw>@<host>:5432/<db>   # the app role, NOT the admin role
KMS_DEV_KEY_FILE=/etc/sender/kms/master.key                   # or your KMS wiring
CREDENTIAL_KEY_ID=cred-dek-v1
MTLS_SERVER_CERT=/etc/sender/certs/brain.crt
MTLS_SERVER_KEY=/etc/sender/certs/brain.key
MTLS_CA_CERT=/etc/sender/certs/ca.crt
BRAIN_HEALTH_ADDR=:8080
BRAIN_BOXLINK_ADDR=:8443
BRAIN_BOXLINK_STREAM_ADDR=:8444
ADMIN_API_KEY_HASHES=<bcrypt hash(es), comma-separated>        # Domain B; empty = admin API OFF
PLATFORM_TOKEN_HASHES=<bcrypt hash(es), comma-separated>       # Domain A; empty = platform API OFF
DEV_BOX_TOKEN_HASH=<bcrypt hash>                               # legacy register/heartbeat route
```

**Both API surfaces fail closed**: leave a hash list empty and that surface stays off entirely.
That is deliberate — a misconfigured deploy serves nothing rather than serving unauthenticated.

The two auth domains never share a credential. Domain A is the platform's service token
(`quote`/`run`/`report`/`describe`); Domain B is the operator's admin key (`/v1/admin/*`). Mint each
separately:

```bash
cd tools/provision-box && go build -o bin/provision-box .
./bin/provision-box admin-key    # prints ADMIN_API_KEY (keep) + ADMIN_API_KEY_HASH (deploy)
./bin/provision-box admin-key    # run AGAIN for the platform token — a distinct credential
```

The Brain stores only hashes and compares in constant time. Record the plaintexts in your secret
store; they are not recoverable from the hash.

Start it, then verify:

```bash
curl -fsS http://localhost:8080/readyz     # expect db:true, singleton_lock:true
```

`singleton_lock:true` means this Brain holds the advisory lock and is the active one. **Exactly one
Brain is ever active** — a second instance waits for the lock (bounded by
`brain_singleton_acquire_timeout_sec`) and then exits. Never run two, and never add a second
dispatch path.

### Step 7 — Provision the first box

Box lifecycle is an out-of-band operator procedure; there is deliberately no `/v1/admin/boxes` REST
surface in V1 (see [`future-work.md`](./future-work.md)).

**On the Brain host** (where the tool and DB access live):

```bash
cd tools/provision-box && go build -o bin/provision-box .

# 1. Mint an id + token + bcrypt hash:
./bin/provision-box new --id <box-id>
#    -> prints BOX_TOKEN=... and DEV_BOX_TOKEN_HASH=...
```

**Record `BOX_TOKEN` now.** It is stored nowhere — only its bcrypt hash goes to the database — and
cannot be recovered after this step. It goes into that box's own secret config.

```bash
# 2. Emit the SQL that seeds the sender.boxes row, and run it as the migration-admin role:
./bin/provision-box seed --id <box-uuid> --token-hash '<hash>'
#    -> pipe/paste into psql "$MIGRATE_DATABASE_URL"
```

Note `sender.boxes.id` is a **UUID**. The token is the authority (verified against `token_hash`);
the cert CN is transport only.

### Step 8 — Start the box

On the box host, with its own leaf certs and its token:

```bash
BRAIN_HOST=<brain-private-hostname>
BRAIN_BOXLINK_PORT=8443
BRAIN_STREAM_PORT=8444
BRAIN_SERVER_NAME=<the name in the Brain cert's SAN>
BOX_ID=<the box uuid you seeded>
BOX_TOKEN=<the plaintext token from step 7>
MTLS_CLIENT_CERT=/etc/sender/certs/box.crt
MTLS_CLIENT_KEY=/etc/sender/certs/box.key
MTLS_CA_CERT=/etc/sender/certs/ca.crt
```

**There is no `DATABASE_URL` and no KMS variable here, and there never will be.** The box ships no
DB driver and no KMS SDK — its `package.json` dependency set is empty. That is structural, not
configuration.

Confirm registration in the Brain's log: `box registered id="<id>"`.

If the box exits instead: it has a **bounded** register-retry budget and deliberately exits for the
supervisor to restart rather than hammering a Brain that is not ready. A box that exits during
first-time setup usually means the Brain was not up yet, or the `sender.boxes` row was not seeded.
See the runbook.

### Step 9 — Create a pool, add an account, set the default pool

Nothing sends until a pool exists, has an eligible account, and is designated the default.
Request/response shapes are in [`api-contract.md`](./api-contract.md).

```bash
ADMIN=http://localhost:8080
KEY='<the plaintext ADMIN_API_KEY>'

# 1. A shared pool:
curl -fsS -X POST $ADMIN/v1/admin/pools -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"kind":"shared","name":"primary"}'

# 2. An SMTP account in it (the credential is encrypted on arrival; the Brain never returns it):
curl -fsS -X POST $ADMIN/v1/admin/accounts -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{ ... see api-contract.md ... }'

# 3. Designate the default shared pool — base sending does NOT work until this is set:
curl -fsS -X PUT $ADMIN/v1/admin/config/default_shared_pool_id -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"value":"<pool-uuid>"}'
```

`default_shared_pool_id` seeds `NULL` and stays `NULL` until you set it. Until then, base-campaign
jobs stay cleanly `queued` — they are not lost and not failed, but nothing sends. This is the single
most common "why is nothing happening" cause; the runbook's flagship tree starts there.

### Step 10 — Verify a real send end to end

```bash
curl -fsS $ADMIN/v1/admin/health -H "Authorization: Bearer $KEY" \
  | jq '{dispatch, pools, boxes, accounts}'
```

Expect: your pool present with headroom that is **not** `exhausted`, `dispatch_paused:false`, and at
least one connected box. What each field means and what warrants action is in the runbook.

Then run one real campaign through `quote` → `run` → `report` (Domain A, platform service token) with
a single recipient you control, and confirm the job reaches `sent`.

> **Before pointing this at a real SMTP provider for the first time**, use
> [`smoke-test-real-provider.md`](./smoke-test-real-provider.md). Everything up to this point has
> been exercised against a local SMTP sink; first contact with a real provider surfaces
> provider-specific quirks (auth mechanisms, TLS behavior, rate-limit responses, sender identity
> verification) and it touches real sending reputation.

---

## Part 3 — Environment variables by service

Full annotated list: `.env.example`. Config *keys* (runtime dials, changeable without a redeploy)
are a different thing entirely and live in the database — see
[`config-reference.md`](./config-reference.md).

### Brain

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | The **`sender_module`** app role. Not the migration admin role. |
| `KMS_DEV_KEY_FILE` | Path to the master key file (or your KMS wiring). **Brain only.** |
| `CREDENTIAL_KEY_ID` | Write-key version label, stamped into `credential_key_id`. Rotation depends on this. |
| `MTLS_SERVER_CERT` / `MTLS_SERVER_KEY` / `MTLS_CA_CERT` | The Brain's mTLS server identity + trust root. |
| `BRAIN_HEALTH_ADDR` | HTTP: health, readiness, Domain A, Domain B. Default `:8080`. |
| `BRAIN_BOXLINK_ADDR` | mTLS register/heartbeat. Default `:8443`. |
| `BRAIN_BOXLINK_STREAM_ADDR` | mTLS persistent send stream (carries credentials). Default `:8444`. |
| `BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC` | Bounded wait for the singleton lock, then exit. |
| `ADMIN_API_KEY_HASHES` | Domain B operator key hashes, comma-separated. **Empty = admin API off.** |
| `PLATFORM_TOKEN_HASHES` | Domain A platform service token hashes. **Empty = platform API off.** |
| `DEV_BOX_TOKEN_HASH` | Token hash for the register/heartbeat route. |
| `SENDER_ALERT_WEBHOOK_URL` | Optional. Empty = alerting off. See [`monitoring.md`](./monitoring.md). |

### Box

| Variable | Purpose |
|---|---|
| `BRAIN_HOST`, `BRAIN_BOXLINK_PORT`, `BRAIN_STREAM_PORT` | Where to connect (private network). |
| `BRAIN_SERVER_NAME` | Must match the Brain cert's SAN, or TLS verification fails. |
| `BOX_ID` | The UUID seeded into `sender.boxes`. |
| `BOX_TOKEN` | The plaintext registration token — **the box's actual authority**. |
| `MTLS_CLIENT_CERT` / `MTLS_CLIENT_KEY` / `MTLS_CA_CERT` | This box's own leaf + trust root. Never the CA key. |
| `BOX_HEARTBEAT_INTERVAL_SEC`, `BOX_SMTP_TIMEOUT_MS` | Liveness cadence; SMTP socket timeout. |

**Never set on a box:** `DATABASE_URL`, any KMS variable, `ca.key`, `brain.key`. If a box host has
any of these, that is a security incident, not a misconfiguration —
[`security-posture.md`](./security-posture.md) explains what it costs.

### Migration runner

`MIGRATE_DATABASE_URL` (admin role), `MIGRATIONS_DIR`, `PREFLIGHT_SQL`, `SENDER_MODULE_PASSWORD`,
and optionally `MIGRATE_LOCK_TIMEOUT` / `MIGRATE_STATEMENT_TIMEOUT` (the one documented exception to
config-not-code: `sender.config` does not exist yet when the bootstrap migration runs) and
`SENDER_MIGRATE_ALLOW_DOWN`.

---

## Part 4 — Production deployment

### Network layout and why no box endpoint is public

**Boxes are outbound-only.** A box opens the connection to the Brain and holds it; the Brain never
dials a box. So a box needs **no inbound port at all** — no listener, no public IP, nothing to
firewall correctly. That is the design, and it is worth not undermining:

- The Brain↔box channel carries **decrypted SMTP credentials**, one job at a time. It is mutual TLS
  in both directions, but the correct posture is that this traffic never touches the public
  internet — put it on a private network / VPC / WireGuard mesh.
- A box that accepted inbound connections would be an attack surface holding live credentials in
  memory. There is no reason to give it one.
- **Postgres is reachable from the Brain only.** Boxes have no DB access by construction; make the
  network agree with the code.

Minimum firewall shape on the Brain host:

```bash
# The box-facing mTLS ports: private network only.
sudo ufw allow from <private-subnet> to any port 8443 proto tcp
sudo ufw allow from <private-subnet> to any port 8444 proto tcp

# :8080 serves the platform API, the operator admin API, AND health.
# Expose it only to the platform and your operator network — never 0.0.0.0.
sudo ufw allow from <platform-ip> to any port 8080 proto tcp
sudo ufw allow from <operator-ip> to any port 8080 proto tcp

sudo ufw default deny incoming
sudo ufw enable
```

On box hosts: **deny all inbound.** Outbound needs the Brain's private address plus your SMTP
providers on 587/465.

> `:8080` is plain HTTP and serves both authenticated API domains. Terminate TLS in front of it
> (nginx/Caddy) if it crosses any untrusted network. The bearer credentials on that port deserve
> transport encryption.

### A production compose file is not shipped — you write it

> **FLAGGED GAP, read before using the units below.** This repo ships `docker-compose.yml` (the dev
> stack) and `tests/harness/compose.test.yml` (the test override). It does **not** ship a
> `docker-compose.prod.yml`. The units below reference one because a production deployment needs
> one, and it is deliberately not committed: it is site-specific (your hostnames, your cert paths,
> your secret store) and an untested committed one would be worse than none. Registered in
> [`future-work.md`](./future-work.md).
>
> Derive it from `docker-compose.yml`, changing exactly these things:
>
> - **Drop the `postgres` and `smtp-sink` services.** Production Postgres is external; the sink is a
>   dev-only local mail trap. Leaving either in a prod file is how you accidentally send real mail
>   into a black hole.
> - **Drop the `migrate` service's automatic run** (or keep it as a `profiles:`-gated one-shot).
>   Migrations must run as a deliberate step before the Brain deploys, not implicitly on boot — see
>   Part 6.
> - **Split brain and box into separate files**, since they run on separate hosts.
> - **Point volume mounts at real paths** (`/etc/sender/certs/...`, `/etc/sender/kms/...`) instead of
>   `./secrets/dev/...`.
> - **Keep the least-privilege mounts exactly as they are.** The box mounts only its own leaf + the
>   CA cert; the Brain alone mounts the KMS key. Do not "simplify" by mounting a shared secrets
>   directory into both.
> - **Remove the box's host port mapping** (there is none in the dev file either — keep it that way)
>   and keep the Brain's ports bound to interfaces your firewall covers.
>
> Adjust the unit file paths to whatever you name yours.

### systemd units

**Brain** (`/etc/systemd/system/sender-brain.service`):

```ini
[Unit]
Description=Sender Module — Brain
After=network-online.target docker.service
Requires=docker.service

[Service]
Type=simple
WorkingDirectory=/opt/sender-module
EnvironmentFile=/etc/sender/brain.env
ExecStartPre=-/usr/bin/docker compose -f docker-compose.prod.yml rm -f brain
ExecStart=/usr/bin/docker compose -f docker-compose.prod.yml up brain
ExecStop=/usr/bin/docker compose -f docker-compose.prod.yml stop -t 90 brain
Restart=on-failure
RestartSec=5

# The Brain drains in-flight jobs on SIGTERM. Give it MORE time than
# shutdown_drain_timeout_sec (see config-reference.md) or systemd will
# SIGKILL a Brain that was shutting down cleanly.
TimeoutStopSec=120
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
```

`TimeoutStopSec` is the one value here that is easy to get wrong and quietly harmful. On SIGTERM the
Brain stops claiming new work, keeps the box stream up so in-flight jobs can report their outcomes,
and only then exits. Cut that short and you turn a clean shutdown into reclaim work — recoverable,
but pointless. Keep it above `shutdown_drain_timeout_sec`.

A SIGKILL is still safe: the singleton advisory lock is session-level, so Postgres releases it
automatically. There is no stale-lock recovery step.

**Box** (`/etc/systemd/system/sender-box.service`):

```ini
[Unit]
Description=Sender Module — Sender Box
After=network-online.target docker.service
Requires=docker.service

[Service]
Type=simple
WorkingDirectory=/opt/sender-module
EnvironmentFile=/etc/sender/box.env
ExecStart=/usr/bin/docker compose -f docker-compose.prod.yml up box
ExecStop=/usr/bin/docker compose -f docker-compose.prod.yml stop -t 60 box
# A box EXITS on purpose when it cannot register within its retry budget.
# Restart=always is correct: the supervisor is the retry mechanism.
Restart=always
RestartSec=10
TimeoutStopSec=90

[Install]
WantedBy=multi-user.target
```

Secure the env files: `sudo chmod 600 /etc/sender/*.env`. They hold the DB password, the token, and
the key path.

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sender-brain    # on the Brain host
sudo systemctl enable --now sender-box      # on each box host
```

---

## Part 5 — Operating procedures

### Adding a box

Boxes are additive and need no Brain restart.

1. On the Brain host: `provision-box new --id <id>` → record the token.
2. `provision-box seed --id <uuid> --token-hash '<hash>'` → run the SQL as the migration admin.
3. Bring up the box with its own leaf certs and its token.
4. Confirm `box registered id="<id>"` in the Brain log, and that
   `GET /v1/admin/health` counts the new box.

A box coming online kicks the dispatcher, so queued work starts flowing to it immediately.

### Removing a box

```bash
./bin/provision-box revoke --id <id>    # emits SQL: clears token_hash, status='disabled'
```

Run that SQL, then stop the box (`sudo systemctl disable --now sender-box`).

On its next connect attempt the box is refused **regardless of its still-valid mTLS certificate** —
the token is the authority. Any job it held in flight is picked up by the reclaim loop and
re-dispatched to a live box. No manual requeue.

**Rotating** a box's token is revoke + re-provision: revoke the old, mint and seed a new one, update
the box's secret, restart it.

### Redeploying

Ordinary code change, no migration:

```bash
cd /opt/sender-module && git pull
docker compose -f docker-compose.prod.yml build brain
sudo systemctl restart sender-brain      # drains, then restarts
curl -fsS http://localhost:8080/readyz
```

Restarting the Brain is safe by design: it drains in-flight work, buffered outcomes replay on
reconnect, and anything still in flight is reclaimed. Boxes reconnect on their own.

**Config changes are not deploys.** All 53 dials live in `sender.config` and take effect on the next
tick — `PUT /v1/admin/config/{key}` and you are done. Restarting the Brain to change a timeout means
something is wrong with your mental model, not with the Brain.

### Rolling back

**Code rollback** is a normal redeploy of the previous image. Safe, because the schema is
additive-only: an older Brain against a newer schema does not see columns it does not know about.

**Schema rollback is not the tool you want.** `down` is refused unless
`SENDER_MIGRATE_ALLOW_DOWN=true`, deliberately: contracts here are additive-only, so a forward fix
is nearly always available and nearly always cheaper. Reverting a migration that has been live can
destroy data an older binary cannot see.

If you genuinely must:

1. Take a database backup first. Not optional.
2. Stop the Brain (do not leave it running against a schema being changed underneath it).
3. `SENDER_MIGRATE_ALLOW_DOWN=true docker compose run --rm --no-deps migrate down <version>` — one version at
   a time, verifying between.
4. Deploy the matching older Brain **before** starting anything.

---

## Part 6 — Version skew during a rolling deploy

This bites on the *second* deploy, not the first, so it is worth reading before you get there.

### Migrations run BEFORE the Brain deploys. Always.

The order is:

```
1. migrate up          (schema + any new config keys are now present)
2. deploy the Brain    (the new binary finds everything it expects)
3. deploy boxes        (independently, whenever — see below)
```

**Why this direction and not the other:** the Brain reads its config keys from `sender.config` at
startup and again on every tick, and a key it expects but cannot find is a **hard error** — startup
fails fatally (`FATAL send-path config load`), and a tick that cannot load config does not dispatch.
So a new Brain against an old schema does not degrade gracefully; it does not run. Migrating first
makes that impossible.

The reverse skew is the safe one. **An old Brain against a new schema is fine**: the schema is
additive-only (columns, enum values, and config keys are only ever added, never renamed or removed),
so the old binary simply ignores what it does not know about. That asymmetry is exactly why
migrations lead.

Practical consequences:

- **Never deploy the Brain and the migration concurrently.** Let `migrate up` finish and verify with
  `migrate status` before the new binary starts.
- The window between step 1 and step 2 is safe to sit in. The old Brain keeps working.
- If step 2 fails, you are in that safe window, not a broken state. Roll the binary back and leave
  the schema alone.
- Two migrators cannot race — the runner holds an advisory lock — but two *deploy pipelines* both
  restarting the Brain can fight over the singleton lock. Serialize your deploys.

### Old boxes talking to a new Brain

**This works, and it is designed to.** Boxes and the Brain are independently deployable and you do
not need to restart them together.

The reason it works: the wire protocol is newline-delimited JSON with a `type` discriminator, and
both sides are **additive-tolerant by construction**:

- The box's frame dispatch has a `default: // Unknown frame: ignore` arm. A new Brain sending a
  frame type an old box has never heard of does not crash it — the box ignores it.
- New *fields* on an existing frame type are ignored by an older peer's decoder. (Note the
  deliberate contrast with the client-facing HTTP APIs, which use `DisallowUnknownFields` to reject
  typos in operator input. The internal box channel is lenient precisely so version skew is a
  non-event; the client boundary is strict precisely so a typo is not silently accepted.)
- `Register` carries an optional `box_version`, which the Brain records for observability. It is
  **not** a compatibility gate: the Brain does not refuse an old box, and there is no version
  negotiation to get wrong.

What an old box will do is simply not honor a *behavior* introduced with the new Brain — a new
variation toggle, for example, is stamped onto the pushed job but a box that predates it ignores it.
The job still sends correctly. So during a rolling box deploy expect **mixed behavior, not
failures**: some sends get the new treatment, some don't, until the roll completes.

**Rolling boxes safely**, one at a time:

1. Revoke nothing. Just stop the box: `sudo systemctl stop sender-box`.
2. In-flight jobs on that box are reclaimed automatically (dead-box detection + lease expiry) and
   re-dispatched to the remaining boxes. Nothing is lost and the account is not penalized.
3. Deploy and start the new box; confirm it registers.
4. Move to the next. Keep at least one box up throughout, or work simply queues until one returns —
   which is also safe, just idle.

### Summary

| Skew | Safe? | Why |
|---|---|---|
| New schema, old Brain | **Yes** | Additive-only; the old binary ignores what it doesn't know. This is the window you deploy through. |
| Old schema, new Brain | **No** | A missing config key is a fatal startup error. Migrate first — this is why the order is fixed. |
| New Brain, old boxes | **Yes** | Unknown frames ignored, unknown fields ignored, `box_version` is observability not a gate. Expect mixed behavior mid-roll, not failures. |
| Old Brain, new boxes | **Yes** | Same tolerance in the other direction. |
| Two Brains at once | **Never** | The singleton advisory lock prevents it; the second exits after a bounded wait. Serialize deploys so they don't fight over it. |
| Two migrators at once | **Prevented** | The runner holds an advisory lock; the second waits. |

---

## Where to go next

- Something is broken → [`operator-runbook.md`](./operator-runbook.md)
- First live deployment → [`go-live-checklist.md`](./go-live-checklist.md)
- Back up the key you just created → [`disaster-recovery.md`](./disaster-recovery.md)
- Hook up monitoring → [`monitoring.md`](./monitoring.md)
- Size the deployment → [`capacity-planning.md`](./capacity-planning.md)
