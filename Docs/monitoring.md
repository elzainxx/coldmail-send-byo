# Monitoring hookup

Where to point an uptime monitor, where to point the alert webhook, and what each signal means. Two
endpoints and one config key; the whole surface is small on purpose.

Triage — what to *do* when something fires — is [`operator-runbook.md`](./operator-runbook.md).
This page is only about wiring the signals up.

---

## 1. The three signals

| Signal | Auth | Use it for |
|---|---|---|
| `GET /healthz` | **none** | Liveness. "The process is up." |
| `GET /readyz` | **none** | Readiness. DB reachable **and** singleton lock genuinely held. `200` = ready, `503` = not. |
| `GET /v1/admin/health` | **Domain B operator key** | The operational snapshot: dispatch state, queue counts, box liveness, account/pool summaries, error counts over a rolling window, shadow-mode summary, and active alert conditions. |

`/healthz` and `/readyz` are deliberately unauthenticated and deliberately thin — they carry no
operational detail, which is what makes exposing them to a monitoring service safe.

**Do not point an unauthenticated uptime check at `/v1/admin/health`.** It requires the operator
bearer key and exposes real operational state; it is a Domain B route, not a probe.

---

## 2. Uptime monitoring — point it at `/readyz`

`/readyz` is the right probe because it fails for the two reasons that actually matter:

```
GET http://<brain-host>:8080/readyz
expect: HTTP 200 and body {"status":"ready","db":true,"singleton_lock":true,...}
alert on: any non-200, or db:false, or singleton_lock:false
```

- `db:false` — the Brain cannot reach Postgres. Dispatch has halted on bounded backoff; the Brain
  stays alive and recovers on its own when the DB returns, needing no reconciliation.
- `singleton_lock:false` — the Brain is running but does **not** hold the dispatch lock, so **nothing
  is dispatching**. This is the failure a naive `/healthz` check misses entirely, and it is the
  reason to probe `/readyz` instead.

A 30–60s interval is plenty. Alert on two consecutive failures rather than one, so a restart during a
deploy does not page anyone.

Use `/healthz` only where you need a pure "is the process alive" check that stays green during a DB
outage — e.g. a container orchestrator's liveness probe, where restarting on a DB outage would be
exactly the wrong response.

Boxes expose **no** listener at all, so there is nothing to probe on a box host. Box liveness is
observed from the Brain, in `boxes` on the admin health endpoint. That is by design (see
[`security-posture.md`](./security-posture.md) §4).

---

## 3. Alert webhook

The Brain POSTs a JSON payload on the **rising edge** of each alert condition. Set the destination
with the `alert_webhook_url` config key — empty (the default) means alerting is off and conditions
remain visible only via the health endpoint.

### Wiring it up

```bash
# Domain B operator key. Exact request shape: api-contract.md
curl -fsS -X PUT "$ADMIN/v1/admin/config/alert_webhook_url" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"value":"https://hooks.example.com/services/T000/B000/XXXX"}'
```

It takes effect **immediately, with no redeploy** — the URL is re-read on each health evaluation.
Only `http://` or `https://` values are accepted; anything else is rejected at write time.

> **`SENDER_ALERT_WEBHOOK_URL` in `.env.example` is inert.** No code reads that variable — the
> webhook destination comes *only* from the `alert_webhook_url` config row. Setting the env var alone
> does nothing. Recorded in [`future-work.md`](./future-work.md).

Prefer `https://`. The payload carries no secrets (§3.3), but the URL itself is usually a bearer
capability for the receiving system.

### The payload

```json
{
  "condition": "box_death",
  "severity": "critical",
  "subject": { "type": "box", "id": "<uuid>" },
  "detail":  { "...": "condition-specific" },
  "fired_at": "2026-07-28T14:03:11Z",
  "module": "sender"
}
```

All three conditions today carry `severity: "critical"`. The three conditions and how to triage each
are in the runbook's "Alert conditions → triage" section.

### Delivery semantics — read these before relying on it

- **Rising edge only, deduped.** One POST per `(condition, subject)` when the condition appears. It
  will not re-fire while the same condition persists for the same subject. There is **no "cleared"
  notification** — a fired alert is never followed by an all-clear POST, so build your receiver
  around "check the health endpoint to see current state," not around pairing up/down events.
- **Conditions are evaluated when the health endpoint is called.** They are derived from live state at
  request time, not by a background scanner. **If nothing ever calls `GET /v1/admin/health`, no
  webhook ever fires.** This is the single most important operational fact on this page — the webhook
  is a *push on evaluation*, not an independent monitor.
- **Delivery is best-effort:** 5s timeout, 3 bounded attempts with a short backoff, then logged and
  dropped. It never blocks or slows the health response, and never touches job or account state.
- **The payload is two-vocabulary clean by design** — condition codes, subject ids, timestamps, and
  condition detail only. Never content, credentials, internal reasons, SQL, or internal hostnames.
  The webhook is treated as untrusted outbound egress across a trust boundary
  ([`security-posture.md`](./security-posture.md)).

### The consequence you must act on: poll the health endpoint

Because conditions are evaluated on request, wire a scheduled poll. Without it, alerting is silent.

```bash
# /etc/cron.d/sender-health  — evaluate conditions every 5 minutes so the webhook can fire.
# The response is discarded; the POINT is the evaluation. Keep the key out of the crontab itself.
*/5 * * * * brain curl -fsS -m 10 -o /dev/null \
  -H "Authorization: Bearer $(cat /etc/sender/admin.key)" \
  http://127.0.0.1:8080/v1/admin/health || true
```

Notes on that snippet:

- `127.0.0.1` — the admin surface should not be reachable off-host unproxied.
- The key is read from a `0400` file owned by the cron user, not embedded in the crontab (which is
  world-readable on many systems).
- `|| true` so a failed poll does not generate cron mail noise; the uptime monitor on `/readyz` is
  what tells you the Brain is down.
- **No `%` anywhere in a crontab command.** Cron treats `%` as a newline, so adding something like
  `-w '%{http_code}'` to that line silently truncates the command. Put anything that needs `%` in a
  script and call the script.
- 5 minutes is a reasonable default. Note the interaction with `health_metrics_window_sec`: the
  rate/error/shadow counts are windowed, so a poll interval much longer than that window leaves gaps
  in what you would have seen.

---

## 4. A concrete end-to-end example

A minimal, complete hookup for a single-Brain deployment:

1. **Uptime monitor** (any external service, or a second host): `GET http://<brain>:8080/readyz` every
   60s, alert after two consecutive non-200s. Covers "Brain down", "DB unreachable", "not holding the
   dispatch lock".
2. **Health poll**: the cron entry above, every 5 minutes. This is what makes the webhook work.
3. **Webhook destination**: set `alert_webhook_url` to your incident channel's inbound URL. You now
   get a message the first time a pool exhausts, an account is quarantined, or a box dies.
4. **On receiving an alert**: open the runbook at "Alert conditions → triage", and run
   `curl -fsS "$ADMIN/v1/admin/health" -H "Authorization: Bearer $KEY" | jq` to see current state —
   the alert tells you *what*, the endpoint tells you *now*.
5. **Log shipping** (optional but recommended): the Brain logs internal failure reasons keyed by
   `request_id`, which is the *only* place the detail behind a client error envelope exists. Without
   log retention, a reported error is a code and a request id with nothing behind them.

`jq` is required for step 4 — the health endpoint returns compact single-line JSON. It is installed by
the prerequisites in [`install-and-deploy.md`](./install-and-deploy.md).

---

## 5. What is deliberately not here

- **No metrics endpoint, no Prometheus exposition, no tracing exporter.** `GET /v1/admin/health` is
  a JSON snapshot, not a time series. If you want history, poll it and store the response — several
  fields (`send_rate_per_min`, `errors_last_window`) are already windowed and graph cleanly.
- **No "cleared" webhook** (§3), and one asymmetry worth knowing: `pool_exhausted` and `box_death`
  derive from live state, while `account_quarantined` is scoped to `health_metrics_window_sec` because
  a quarantine is a point-in-time event. **That condition clearing does not mean the account
  recovered** — use the standing `accounts.quarantined` count instead. The runbook covers this.
- **No bounce or complaint feedback.** There is none in V1: a provider `250` is recorded as `sent` and
  nothing downstream is observed. Deliverability monitoring has to come from the provider side. See
  the runbook's deliverability section and [`future-work.md`](./future-work.md).

## 6. Related

| Doc | For |
|---|---|
| [`operator-runbook.md`](./operator-runbook.md) | What to do when a signal fires; reading the health endpoint field by field. |
| [`api-contract.md`](./api-contract.md) | Exact request/response shapes for every route above. |
| [`config-reference.md`](./config-reference.md) | `alert_webhook_url`, `health_metrics_window_sec`, and every other key. |
| [`security-posture.md`](./security-posture.md) | Why the webhook payload is minimal; what should be network-reachable. |
