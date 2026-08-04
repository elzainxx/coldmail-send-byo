# deploy/dns-test/ — Phase 4 "From gate" test DNS fixtures

**DEV/TEST-ONLY. NOT real prod DNS data.** Mirrors the disclaimer convention in
`deploy/pg-init/00-platform-fixtures.sql`: everything under this directory is a fixture this
repo owns outright, built solely so the SPF/DMARC/DKIM alignment gate's behavioral tests
(`Docs/history/phase-4-from-gate.md` §7) can exercise a **real** DNS resolver, real protocol, real
`net.Resolver` code path — the production evaluator, unmodified — against controllable failure
modes (SERVFAIL, a genuine timeout, NXDOMAIN) that a public registrar cannot be made to produce
on demand, and against edge cases (multiple records, malformed SPF, >10-lookup chains, macros)
that would require rare and fragile real-world domains to find.

None of this is a real domain, a real provider, or a real credential. The `.test` TLD is
reserved by RFC 2606 for exactly this use.

## What's here

- `Corefile` — CoreDNS config. Three zones: `servfail.test` (manufactured SERVFAIL for any
  query), `blackhole.test` (forwarded to the `dns-blackhole` compose service, which never
  replies — a genuine hang), and `test` (the real fixture zone, served from `zones/db.zone`).
  A catch-all block forwards anything NOT under `.test` to real upstream resolvers (8.8.8.8 /
  1.1.1.1), so a real public domain referenced by a fixture (e.g. `_spf.google.com`) or a
  separately-marked M3-P4-OD-6 canary test still resolves for real.
- `zones/db.zone` — the BIND-style zone file with every `.test` fixture record.

## Running it

Part of the base `docker-compose.yml` (repo root), as the `coredns` and `dns-blackhole`
services. Additive only — nothing else depends on them and they gate nothing else's startup
(same posture as `smtp-sink`):

```
docker compose up -d coredns dns-blackhole
```

- From the **host** (e.g. a `go test` run outside Docker): point `from_dns_resolver` at
  `localhost:${HOST_PORT_COREDNS_UDP:-9053}` (default port 9053, both UDP and TCP).
- From **inside the compose network** (the Brain, once later chunks wire the real gate to it):
  `coredns:53`.

## Zone-to-fixture mapping

All names below are under the `test.` TLD.

| Zone / name | Fixture purpose |
|---|---|
| `aligned.test` | SPF `v=spf1 include:_spf.google.com ~all` — contains the real Google Workspace SPF token. Resolves via the Corefile's real-upstream forward. T1 positive: aligned From → `aligned`. |
| `misaligned-reject-with-spf.test` | SPF present (`include:_spf.otherprovider.test`, no matching token) + DMARC `p=reject`. The common WARN case added by M3-P4-OD-1's ruling — T2's paired control, T2b's `p=reject` role model, T3c's "enforcing policy" warn variant. |
| `misaligned-reject-nospf-nodkim.test` | NO SPF record at all + DMARC `p=reject` + no DKIM at any common selector. The **only** remaining BLOCK case (T2 positive). |
| `misaligned-quarantine.test` | SPF present, no matching token, DMARC `p=quarantine` → WARN. |
| `misaligned-none.test` | SPF present, no matching token, DMARC `p=none` → WARN (T3's ordinary-warning case). |
| `no-dmarc.test` | SPF present, no matching token, NO `_dmarc` record at all → WARN. |
| `two-dmarc.test` | TWO `_dmarc` TXT records (`p=reject` and `p=none`) → the ambiguous "can't pick one" non-detection case. |
| `two-spf.test` | TWO apex TXT records, each `v=spf1...` → RFC 7208 §4.5 PermError (multiple records). |
| `malformed-spf.test` | `v=spf1 frobnicate:nonsense ~all` — unparseable mechanism token → PermError. |
| `spf-lookup-limit.test` (+ `l1`..`l11.spf-lookup-limit.test`) | 11-deep `include:` chain → exceeds RFC 7208 §4.6.4's 10-lookup limit → PermError. |
| `spf-void-limit.test` | SPF record with 3 `include:` targets that don't exist (NXDOMAIN/NODATA) → 3 void lookups, exceeds the RFC 7208 §11.1 recommended limit of 2. |
| `macro-spf.test` | SPF record containing macros (`%{ir}`, `%{l1r+-}`) → must be recognized and refused at setup time, not blindly expanded. |
| `alias-selfhosted.test` | §1.7 "send-as via the alias domain's own SMTP": bare `a`/`mx` mechanisms + a real A record for itself → genuinely resolvable, aligns trivially. T4 positive: must be `aligned`, not collateral damage. |
| `alias-viaprovider.test` | §1.7 "send-as via the provider's servers": SPF DOES contain the fake provider's token (the trap — token match looks like alignment) but represents a different authenticating mailbox. T4 negative control: Go-side alias-detection must override the token match; DNS alone can't express this, this zone just supplies the bait. |
| `servfail.test` | CoreDNS `template` plugin returns SERVFAIL for any query under this zone. T5: DNS failure at setup → fails closed, retryable — verdict/`from_checked_at` untouched. |
| `blackhole.test` | Forwarded (via CoreDNS `forward`) to the `dns-blackhole` service, which never replies. T5: proves SERVFAIL and a genuine timeout are handled identically. Point a test's own `context.WithTimeout` at ~2-3s; do not rely on CoreDNS's own forward-plugin patience to time out first. |
| `_spf.fakeprovider.test` | Fake provider's own SPF include target, `v=spf1 ip4:203.0.113.0/24 ~all`. The "good" token most self-contained fixtures above use so they need no real internet access. |
| `_spf.otherprovider.test` | A DIFFERENT fake provider's SPF include target, `v=spf1 ip4:198.51.100.0/24 ~all`. Used by the misaligned zones as "some SPF record, just not the right one." |
| `t2b-nospf-dkim.test` | T2b (i): `p=reject` + no SPF record + DKIM **present** at `google._domainkey` → missing "no DKIM" → WARN, not BLOCK. |
| `t2b-spf-nodkim.test` | T2b (ii): `p=reject` + SPF record present (no matching token) + no DKIM at any common selector → missing "no SPF record" → WARN, not BLOCK. |
| `t2b-nospf-nodkim-quarantine.test` | T2b (iii): no SPF record + no DKIM + DMARC `p=quarantine` (not `reject`) → missing "p=reject" → WARN, not BLOCK. Distinct from `misaligned-quarantine.test`, which has an SPF record present — this one has SPF absent specifically. |

Canary tests (M3-P4-OD-6 — allowed to fail loudly against real internet state) use real public
names directly: `smtp.gmail.com` / `_spf.google.com` (§1.1's facts). They do not need any zone
here; they resolve through this same server's catch-all upstream forward.

## Verification performed

See the delivery report for the exact commands run (`docker compose config`, bringing the two
services up, and `dig`/raw-socket checks against `aligned.test`, `servfail.test`, and
`blackhole.test`) and their output.
