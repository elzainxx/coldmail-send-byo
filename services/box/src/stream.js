// stream.js — the box side of the Phase-3 persistent box↔Brain control channel (§2.1). The box
// DIALS the Brain's mTLS stream port and holds ONE long-lived connection: it sends `register`
// first, then `heartbeat`s on an interval, receives `push_job` frames, runs the SMTP session
// (src/smtp.js), and sends `report` back — all as newline-delimited JSON frames.
//
// mTLS both ways (§3.3, S4): the box presents its client cert AND verifies the Brain's server
// cert against the CA (rejectUnauthorized:true). The box will NOT send `register` to a Brain it
// cannot verify, so no credential-bearing push is ever accepted from a forged Brain.
//
// The plaintext credential from push_job is held ONLY in a MemoryCredential for the one session
// and zeroed on EVERY exit path — accept, error, exception (§4.4, S1/S3). The box has NO DB and
// NO KMS: it receives an already-decrypted secret and returns only an outcome (§3.4).
//
// Phase 7 §1.3 (P7-4) — OUTCOME BUFFERING + PERSIST-ACK. A report the box cannot confirm was
// durably stored (stream down, Brain restarting, DB outage — the box can't tell which) is RETAINED
// in an in-memory buffer keyed by job_id and REPLAYED on every (re)connect until the Brain returns
// a `report_ack` for that job — an ack the Brain sends ONLY AFTER the outcome is committed. On ack
// the entry is dropped. The buffer is bounded by box_outcome_buffer_ttl_sec (delivered on
// register_ack): an entry older than the TTL is dropped and the job falls back to Brain reclaim +
// the accepted duplicate. The CREDENTIAL is never buffered — only the outcome (§4.4 unchanged).
// Idempotency (provider_message_id UNIQUE + the Brain's pre-persist check) makes a replay a clean
// no-op, never a second send. This one mechanism covers stream-drop, Brain-restart, and DB-outage.

import tls from "node:tls";
import fs from "node:fs";
import { MemoryCredential } from "./creds.js";
import { runSmtpSession, SMTPError } from "./smtp.js";

// defaultDial is the PRODUCTION transport: a mutual-TLS connection to the Brain (§3.3). It is the
// only transport the box ever uses in production. A test may inject an alternative dial (o.dial) to
// drive the REAL read loop / outcome buffer / replay over a plain OS socket — substituting ONLY the
// TLS wrap, which is proven separately by the live-stack mTLS suite + the S4 handshake test. This is
// the same "stand in only the already-proven wire" philosophy the Go dispatch harness uses for SMTP.
function defaultDial(o, tlsMaterial, onSecure) {
  const sock = tls.connect({
    host: o.host,
    port: o.port,
    cert: tlsMaterial.cert,
    key: tlsMaterial.key,
    ca: tlsMaterial.ca,
    servername: o.serverName || "brain",
    rejectUnauthorized: true, // verify the Brain (§3.3) — refuse a Brain we can't authenticate
  });
  sock.once("secureConnect", onSecure);
  return sock;
}

export class BoxStream {
  /**
   * @param {object} o
   * @param {string} o.host  Brain host
   * @param {number} o.port  Brain stream mTLS port
   * @param {string} o.boxId
   * @param {string} o.token registration token (authority)
   * @param {string} o.certPath box client cert
   * @param {string} o.keyPath  box client key
   * @param {string} o.caPath   CA that signed the Brain server cert
   * @param {string} o.serverName SAN to verify ("brain")
   * @param {number} o.heartbeatSec
   * @param {number} o.smtpTimeoutMs per-operation SMTP timeout (from push_job/config)
   * @param {object} o.metadata optional {box_version, hostname, started_at}
   * @param {function} o.onZeroed optional test hook: called (jobId) after the credential is zeroed
   * @param {number} o.outcomeBufferTtlMs bound on the outcome buffer (P7-4); overridden by the
   *        Brain's register_ack (box_outcome_buffer_ttl_sec). Default 900_000 (15 min).
   * @param {function} o.onAck optional test hook: called (jobId) after a report_ack drops a buffered entry
   * @param {function} o.now optional clock seam (returns epoch ms) for TTL tests
   */
  constructor(o) {
    this.host = o.host;
    this.port = o.port;
    this.boxId = o.boxId;
    this.token = o.token;
    this.serverName = o.serverName || "brain";
    this.heartbeatSec = o.heartbeatSec || 120;
    this.smtpTimeoutMs = o.smtpTimeoutMs || 30000;
    this.metadata = o.metadata || {};
    this.onZeroed = o.onZeroed || null;
    this.onAck = o.onAck || null;
    this.now = o.now || (() => Date.now());
    // dial is the transport factory: production uses defaultDial (mutual-TLS). Tests may inject one
    // to exercise the real read loop/buffer/replay over a plain socket (TLS proven elsewhere).
    this.dial = o.dial || defaultDial;
    this.tls = {
      cert: fs.readFileSync(o.certPath),
      key: fs.readFileSync(o.keyPath),
      ca: fs.readFileSync(o.caPath),
    };
    this.sock = null;
    this.buf = "";
    this.hbTimer = null;
    this.registered = false;
    this.stopped = false;

    // Phase 7 (§1.3, P7-4): the outcome buffer. Map<job_id, {report, firstSeenMs}>. An outcome lives
    // here from the moment it is produced until the Brain acks it (dropped) or it ages past the TTL
    // (dropped → falls back to Brain reclaim). NEVER holds a credential — only the report object.
    this.outcomeBuffer = new Map();
    this.outcomeBufferTtlMs = o.outcomeBufferTtlMs || 900_000; // overridden by register_ack
  }

  // start dials the Brain, completes the register handshake, and begins the read + heartbeat
  // loops. Resolves once registered (or rejects on a rejected register / TLS failure).
  start() {
    // Reset per-connection state so a reconnect (P7-4) starts clean. The outcome buffer is NOT reset
    // — it deliberately survives the reconnect so unacked outcomes replay on the new register_ack.
    this.registered = false;
    this.buf = "";
    return new Promise((resolve, reject) => {
      const onSecure = () => {
        // First frame MUST be register (§3.1).
        this.#send({
          type: "register",
          request_id: reqID(),
          box_id: this.boxId,
          token: this.token,
          box_version: this.metadata.box_version,
          hostname: this.metadata.hostname,
          started_at: this.metadata.started_at,
        });
      };
      const sock = this.dial(this, this.tls, onSecure);
      this.sock = sock;
      sock.setEncoding("utf8");

      sock.on("data", (d) => this.#onData(d, resolve, reject));
      sock.once("error", (e) => { if (!this.registered) reject(e); });
      sock.once("close", () => {
        this.#stopHeartbeat();
        if (!this.registered) reject(new Error("stream closed before register"));
      });
    });
  }

  stop() {
    this.stopped = true;
    this.#stopHeartbeat();
    if (this.sock) { try { this.sock.destroy(); } catch { /* ignore */ } }
  }

  #onData(chunk, resolve, reject) {
    this.buf += chunk;
    let idx;
    while ((idx = this.buf.indexOf("\n")) >= 0) {
      const line = this.buf.slice(0, idx);
      this.buf = this.buf.slice(idx + 1);
      if (!line.trim()) continue;
      let msg;
      try { msg = JSON.parse(line); } catch { continue; }
      this.#handle(msg, resolve, reject);
    }
  }

  #handle(msg, resolve, reject) {
    switch (msg.type) {
      case "register_ack":
        if (msg.accepted) {
          this.registered = true;
          // Adopt the Brain-delivered outcome-buffer TTL (config-not-code; P7-4). Falls back to the
          // bootstrap default when the frame omits it (older Brain / test).
          if (msg.outcome_buffer_ttl_sec > 0) {
            this.outcomeBufferTtlMs = msg.outcome_buffer_ttl_sec * 1000;
          }
          this.#startHeartbeat();
          // REPLAY any outcomes still unacked from before this (re)connect (§1.3): a stream that just
          // came back must re-deliver what the Brain never acked. Prune expired entries first.
          this.#replayBufferedOutcomes();
          if (resolve) resolve(true);
        } else {
          // Clean reject code only (two-vocabulary); close and let start() reject.
          if (reject) reject(new Error(`register rejected: ${msg.code || "unauthenticated"}`));
          this.stop();
        }
        break;
      case "push_job":
        // Do not await here — process the job then report; keep the read loop responsive.
        this.#runJob(msg).catch(() => { /* #runJob never throws credential detail; already handled */ });
        break;
      case "report_ack":
        // The Brain durably persisted (or cleanly no-op'd) this job's outcome — drop the buffered copy
        // so we stop replaying it (P7-4). Keyed by job_id so it matches across a reconnect.
        if (msg.job_id && this.outcomeBuffer.delete(msg.job_id) && this.onAck) {
          try { this.onAck(msg.job_id); } catch { /* test hook only */ }
        }
        break;
      default:
        // Unknown frame: ignore.
    }
  }

  // #bufferAndSend records the outcome in the replay buffer (keyed by job_id) and then attempts to
  // send it. The buffer entry persists until a report_ack drops it or the TTL expires it — so if this
  // send is lost to a stream drop, the outcome is replayed on reconnect (P7-4). It is a no-op to
  // re-buffer a job already present (a replay), so firstSeenMs (the TTL anchor) is not reset.
  #bufferAndSend(report) {
    if (!this.outcomeBuffer.has(report.job_id)) {
      this.outcomeBuffer.set(report.job_id, { report, firstSeenMs: this.now() });
    }
    this.#send(report);
  }

  // #replayBufferedOutcomes re-sends every still-unacked buffered outcome (oldest first) after a
  // (re)connect, dropping any that have aged past the TTL (they fall back to Brain reclaim). Bounded
  // work: at most one in-flight-per-box means the buffer is small in practice, but the TTL is the
  // hard bound regardless.
  #replayBufferedOutcomes() {
    this.#pruneExpiredOutcomes();
    for (const { report } of this.outcomeBuffer.values()) {
      this.#send(report);
    }
  }

  // #pruneExpiredOutcomes drops buffer entries older than the TTL (P7-4 "bounded" teeth): past the
  // TTL the Brain-side reclaim loop owns the job, so retaining/replaying would only risk a late,
  // now-meaningless duplicate. Returns the number dropped (for tests/logs).
  #pruneExpiredOutcomes() {
    const cutoff = this.now() - this.outcomeBufferTtlMs;
    let dropped = 0;
    for (const [jobId, entry] of this.outcomeBuffer) {
      if (entry.firstSeenMs <= cutoff) {
        this.outcomeBuffer.delete(jobId);
        dropped++;
      }
    }
    return dropped;
  }

  // #runJob is the credential lifecycle boundary (§4.4). It wraps the plaintext in a
  // MemoryCredential and zeroes it in `finally` on EVERY path — accept, SMTP error, or thrown
  // exception. The plaintext is never logged and never placed in the report.
  async #runJob(pj) {
    const credential = new MemoryCredential(pj.smtp.secret);
    // Drop our own reference to the plaintext string ASAP; the durable copy is the buffer.
    pj.smtp.secret = null;

    const jobId = pj.job_id;
    let report;
    try {
      // Lease check (§2.3): decline without SMTP if the lease already passed on receipt.
      if (leaseExpired(pj.lease_expires_at)) {
        report = errorReport(pj.request_id, jobId, "lease_expired");
      } else {
        const dataBytes = buildDataBytes(pj.send);
        // Per-operation SMTP timeout (Phase 6 §5, P6-11): prefer the seeded smtp_operation_timeout_sec
        // delivered on this push_job (the Brain enforces it < the job lease at config-load), falling
        // back to the box's bootstrap env default only when the frame omits it (older Brain / test).
        const opTimeoutMs = pj.smtp.operation_timeout_sec > 0
          ? pj.smtp.operation_timeout_sec * 1000
          : this.smtpTimeoutMs;
        const result = await runSmtpSession(
          {
            smtp: { host: pj.smtp.host, port: pj.smtp.port, username: pj.smtp.username, security: pj.smtp.security },
            envelopeFrom: pj.send.from_address,
            recipient: pj.send.recipient,
            dataBytes,
          },
          credential,
          { timeoutMs: opTimeoutMs },
        );
        // Provider id: parse from the 250, else fall back to the Brain-generated Message-ID (§4.3).
        const provID = parseProviderID(result.smtpResponse) || pj.send.headers["Message-ID"] || "";
        if (!provID) {
          report = errorReport(pj.request_id, jobId, "accepted_without_id"); // §4.3 defensive backstop
        } else {
          report = {
            type: "report",
            request_id: pj.request_id,
            job_id: jobId,
            outcome: "accepted",
            provider_message_id: provID,
            smtp_response: result.smtpResponse,
            error_reason: null,
          };
        }
      }
    } catch (e) {
      if (e instanceof SMTPError && e.reason === "bounced") {
        report = {
          type: "report", request_id: pj.request_id, job_id: jobId,
          outcome: "bounced", smtp_response: e.smtpResponse || "", error_reason: null,
        };
      } else {
        const reason = e instanceof SMTPError ? e.reason : "timeout";
        const resp = e instanceof SMTPError ? (e.smtpResponse || "") : "";
        report = { type: "report", request_id: pj.request_id, job_id: jobId, outcome: "error", smtp_response: resp, error_reason: reason };
      }
    } finally {
      // LOCKED: zero the plaintext on EVERY exit path (§4.4, S3).
      credential.zero();
      if (this.onZeroed) { try { this.onZeroed(jobId); } catch { /* test hook only */ } }
    }
    // Buffer-then-send (P7-4): retain the outcome (never the credential — already zeroed above) so a
    // lost send is replayed on reconnect, until the Brain acks it.
    this.#bufferAndSend(report);
  }

  // replayNow re-sends all still-unacked buffered outcomes (pruning expired ones first). The box does
  // this automatically on every register_ack; this is a thin public seam so tests can trigger a
  // replay/prune deterministically without a full reconnect. Safe to call anytime.
  replayNow() {
    this.#replayBufferedOutcomes();
  }

  #send(obj) {
    if (!this.sock) return;
    try { this.sock.write(JSON.stringify(obj) + "\n", "utf8"); } catch { /* stream gone */ }
  }

  #startHeartbeat() {
    this.#stopHeartbeat();
    this.hbTimer = setInterval(() => {
      this.#send({ type: "heartbeat", request_id: reqID(), box_id: this.boxId });
    }, this.heartbeatSec * 1000);
    if (this.hbTimer.unref) this.hbTimer.unref();
  }

  #stopHeartbeat() {
    if (this.hbTimer) { clearInterval(this.hbTimer); this.hbTimer = null; }
  }
}

// --- helpers ---------------------------------------------------------------------------------

function reqID() {
  return "req_" + Math.floor(Date.now() / 1000).toString(16) + Math.random().toString(16).slice(2, 10);
}

function leaseExpired(iso) {
  if (!iso) return false;
  const t = Date.parse(iso);
  return Number.isFinite(t) && t <= Date.now();
}

function errorReport(requestId, jobId, reason) {
  return { type: "report", request_id: requestId, job_id: jobId, outcome: "error", smtp_response: "", error_reason: reason };
}

// buildDataBytes assembles the RFC 5322 message from the Brain-supplied headers (§8) + body. The
// box sends EXACTLY the headers it was given (it does not invent or reorder in Phase 3, §8.1),
// plus the transport-mandatory MIME framing.
function buildDataBytes(send) {
  const h = send.headers || {};
  const order = ["From", "To", "Subject", "Reply-To", "Message-ID", "Date"];
  const lines = [];
  for (const k of order) {
    if (h[k] != null && h[k] !== "") lines.push(`${k}: ${h[k]}`);
  }
  // Any additional Brain-supplied headers (none in Phase 3 baseline, but forward-compatible).
  for (const k of Object.keys(h)) {
    if (!order.includes(k) && h[k] != null && h[k] !== "") lines.push(`${k}: ${h[k]}`);
  }
  lines.push("MIME-Version: 1.0");
  lines.push('Content-Type: text/html; charset="utf-8"');
  lines.push("Content-Transfer-Encoding: 8bit");
  lines.push(""); // header/body separator
  lines.push(String(send.body || ""));
  return lines.join("\r\n");
}

// parseProviderID extracts a provider message id from a 250 response where present (§4.3). It is
// intentionally conservative: common forms are "250 OK queued as ABCD1234" and Google's
// "250 2.0.0 OK <ts> <id> - gsmtp". Returns "" if nothing parseable (caller falls back to
// Message-ID).
export function parseProviderID(resp) {
  if (!resp) return "";
  let m = resp.match(/queued as ([A-Za-z0-9._-]+)/i);
  if (m) return m[1];
  m = resp.match(/\b2\.0\.0 OK\s+\S+\s+([A-Za-z0-9._-]+)/i);
  if (m) return m[1];
  m = resp.match(/\bid=([A-Za-z0-9._@-]+)/i);
  if (m) return m[1];
  return "";
}
