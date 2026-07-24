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

import tls from "node:tls";
import fs from "node:fs";
import { MemoryCredential } from "./creds.js";
import { runSmtpSession, SMTPError } from "./smtp.js";

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
  }

  // start dials the Brain, completes the register handshake, and begins the read + heartbeat
  // loops. Resolves once registered (or rejects on a rejected register / TLS failure).
  start() {
    return new Promise((resolve, reject) => {
      const sock = tls.connect({
        host: this.host,
        port: this.port,
        cert: this.tls.cert,
        key: this.tls.key,
        ca: this.tls.ca,
        servername: this.serverName,
        rejectUnauthorized: true, // verify the Brain (§3.3) — refuse a Brain we can't authenticate
      });
      this.sock = sock;
      sock.setEncoding("utf8");

      sock.once("secureConnect", () => {
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
      });

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
          this.#startHeartbeat();
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
      default:
        // Unknown frame: ignore.
    }
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
    this.#send(report);
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
