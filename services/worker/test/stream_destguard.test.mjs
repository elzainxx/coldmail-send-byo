// stream_destguard.test.mjs — the destination guard as it reaches the worker over the wire (Phase 3 close-out C4).
// A REAL WorkerStream, driven by a REAL fake-Control socket server (same arrangement as buffer.test.mjs and
// variation-security.test.mjs: only the mTLS wrap is substituted), proves that
//   - the Control's `dest_allow_cidrs` field on a push_job / push_verify SMTP block is what relaxes the guard (a
//     frame WITHOUT it is refused: destination_blocked, an EMPTY smtp_response, and the sink is never spoken to);
//   - a non-empty list is announced by ONE loud warning, again when the list changes, never for an empty or
//     absent list.

import { test } from "node:test";
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { WorkerStream } from "../src/stream.js";
import { startSink } from "./sink.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CERTS = path.resolve(__dirname, "../../../secrets/dev/certs");
const CANARY = "canary-stream-destguard-9Z";
const WORKER_ID = "worker-destguard-test";
const LOOPBACK_ALLOW = ["127.0.0.0/8"];

async function startFakeControl(opts = {}) {
  const server = net.createServer((conn) => {
    conn.setEncoding("utf8");
    conn.reply = (obj) => { try { conn.write(JSON.stringify(obj) + "\n"); } catch { /* gone */ } };
    let buf = "";
    conn.on("data", (d) => {
      buf += d;
      let i;
      while ((i = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, i);
        buf = buf.slice(i + 1);
        if (!line.trim()) continue;
        let msg;
        try { msg = JSON.parse(line); } catch { continue; }
        if (opts.onFrame) opts.onFrame(msg, conn);
      }
    });
    conn.on("error", () => {});
    server._lastConn = conn;
  });
  server._lastConn = null;
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return { server, port: server.address().port, close: () => new Promise((r) => server.close(r)) };
}

function plainDial(o, _tlsMaterial, onSecure) {
  return net.connect({ host: o.host, port: o.port }, onSecure);
}

function makeWorker(port) {
  return new WorkerStream({
    host: "127.0.0.1",
    port,
    workerId: WORKER_ID,
    token: "tok",
    certPath: path.join(CERTS, "harness-client.crt"),
    keyPath: path.join(CERTS, "harness-client.key"),
    caPath: path.join(CERTS, "ca.crt"),
    serverName: "control",
    heartbeatSec: 3600,
    dial: plainDial,
    smtpTimeoutMs: 5000,
  });
}

const waitFor = async (pred, ms = 8000) => {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (pred()) return true;
    await new Promise((r) => setTimeout(r, 15));
  }
  return false;
};

// harness starts a fake Control + worker; `frames` collects every report / verify_report the worker sends.
async function harness() {
  const frames = [];
  const control = await startFakeControl({
    onFrame: (msg, conn) => {
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
      if (msg.type === "report" || msg.type === "verify_report") {
        frames.push(msg);
        if (msg.type === "report") conn.reply({ type: "report_ack", request_id: msg.request_id, job_id: msg.job_id, accepted: true });
      }
    },
  });
  const worker = makeWorker(control.port);
  await worker.start();
  return {
    frames,
    push: (frame) => control.server._lastConn.reply(frame),
    stop: async () => { worker.stop(); await control.close(); },
  };
}

function smtpBlock(sinkPort, extra = {}) {
  return { host: "127.0.0.1", port: sinkPort, username: "sender@ourdomain.com", secret: CANARY, security: "starttls", operation_timeout_sec: 5, ...extra };
}

function pushJob(id, sinkPort, extra = {}, leaseMs = 60_000) {
  return {
    type: "push_job",
    request_id: "req-" + id,
    job_id: id,
    send: {
      recipient: "alice@example.com",
      from_address: "sender@ourdomain.com",
      subject: "destguard",
      body: "<p>hello</p>",
      headers: { From: "sender@ourdomain.com", To: "alice@example.com", Subject: "destguard", "Message-ID": `<${id}@ourdomain.com>`, Date: new Date().toUTCString() },
    },
    smtp: smtpBlock(sinkPort, extra),
    lease_expires_at: new Date(Date.now() + leaseMs).toISOString(),
  };
}

function pushVerify(id, sinkPort, extra = {}) {
  return { type: "push_verify", request_id: "req-" + id, account_id: "acct-" + id, attempt_token: 1, verify_domain: "ourdomain.com", smtp: smtpBlock(sinkPort, extra) };
}

// capture runs fn with console.warn recorded; returns the recorder (stop() restores console.warn).
function captureWarn() {
  const orig = console.warn;
  const lines = [];
  console.warn = (...a) => lines.push(a.join(" "));
  return { lines, stop: () => { console.warn = orig; } };
}

test("a push_job / push_verify WITHOUT dest_allow_cidrs is refused (destination_blocked, empty smtp_response, sink untouched); WITH it, it runs", async () => {
  const sink = await startSink({ authResult: "ok" });
  const warn = captureWarn();
  const h = await harness();
  try {
    // --- the guard refuses a loopback sink the frame does not name
    h.push(pushJob("job-blocked", sink.port));
    h.push(pushVerify("ver-blocked", sink.port));
    assert.ok(await waitFor(() => h.frames.length >= 2), "the worker reported neither refusal");
    const job = h.frames.find((f) => f.type === "report" && f.job_id === "job-blocked");
    const ver = h.frames.find((f) => f.type === "verify_report" && f.request_id === "req-ver-blocked");
    for (const f of [job, ver]) {
      assert.ok(f, "a refusal was not reported");
      assert.equal(f.outcome, "error");
      assert.equal(f.error_reason, "destination_blocked");
      assert.equal(f.smtp_response, "", "nothing from inside the network may reach the user");
      assert.ok(!JSON.stringify(f).includes(CANARY), "the credential is in the report");
      assert.ok(!JSON.stringify(f).includes("loopback"), "the refused range is in the report");
    }
    assert.equal(sink.received.length, 0, "the sink was spoken to by a refused session");

    // --- CONTROL: the same two frames carrying dest_allow_cidrs run to completion
    h.push(pushJob("job-allowed", sink.port, { dest_allow_cidrs: LOOPBACK_ALLOW }));
    h.push(pushVerify("ver-allowed", sink.port, { dest_allow_cidrs: LOOPBACK_ALLOW }));
    assert.ok(await waitFor(() => h.frames.length >= 4), "the allowlisted frames never reported");
    const job2 = h.frames.find((f) => f.type === "report" && f.job_id === "job-allowed");
    const ver2 = h.frames.find((f) => f.type === "verify_report" && f.request_id === "req-ver-allowed");
    assert.equal(job2.outcome, "accepted", `the allowlisted push_job failed: ${job2.error_reason}`);
    assert.equal(ver2.outcome, "accepted", `the allowlisted push_verify failed: ${ver2.error_reason}`);
    assert.ok(sink.received.some((l) => /AUTH/i.test(l)), "the allowlisted session never reached AUTH");
    assert.ok(sink.received.some((l) => /MAIL FROM/i.test(l)), "the allowlisted send never reached MAIL FROM");
  } finally {
    warn.stop();
    await h.stop();
    await sink.close();
  }
});

test("a non-empty dest_allow_cidrs is announced by ONE loud warning, again when it changes, never for an empty or absent list", async () => {
  // expired leases: no SMTP is attempted, so this isolates the announcement from the session itself
  const expired = (id, list) => {
    const f = pushJob(id, 1, {}, -60_000);
    if (list !== undefined) f.smtp.dest_allow_cidrs = list;
    return f;
  };
  const warn = captureWarn();
  const h = await harness();
  const announcements = () => warn.lines.filter((l) => l.includes("WARNING smtp_host_allow_cidrs is non-empty"));
  let sent = 0;
  const send = async (list) => {
    h.push(expired("job-ann-" + ++sent, list));
    assert.ok(await waitFor(() => h.frames.filter((f) => f.type === "report").length >= sent), `frame ${sent} was not processed`);
  };
  try {
    const A = LOOPBACK_ALLOW;
    const B = ["127.0.0.0/8", "10.1.0.0/16"];

    await send(undefined); // CONTROL: an absent list says nothing
    await send([]); //        and neither does an empty one
    assert.equal(announcements().length, 0, "an absent or empty list was announced");

    await send(A);
    assert.equal(announcements().length, 1, "the first non-empty list was not announced exactly once");
    assert.equal(
      announcements()[0],
      `worker ${WORKER_ID}: WARNING smtp_host_allow_cidrs is non-empty (127.0.0.0/8): the SMTP destination guard is relaxed for these ranges; this is a test-harness setting and production leaves it empty`,
    );
    assert.ok(!announcements()[0].includes("\n"), "the announcement must be one line");

    await send(A); // the same list again: no second line
    await send(A);
    assert.equal(announcements().length, 1, "an unchanged list was announced again");

    await send(B); // changed: announced again, naming the new list
    assert.equal(announcements().length, 2);
    assert.match(announcements()[1], /non-empty \(127\.0\.0\.0\/8,10\.1\.0\.0\/16\)/);

    await send(undefined); // dropped: nothing is said ...
    assert.equal(announcements().length, 2);
    await send(A); // ... and the list coming back IS a change
    assert.equal(announcements().length, 3);

    // nothing but the announcements was warned (no credential, no frame content)
    assert.ok(!warn.lines.join("\n").includes(CANARY));
  } finally {
    warn.stop();
    await h.stop();
  }
});

test("a push_verify announces the list too", async () => {
  const sink = await startSink({ authResult: "ok" });
  const warn = captureWarn();
  const h = await harness();
  try {
    h.push(pushVerify("ver-ann", sink.port, { dest_allow_cidrs: LOOPBACK_ALLOW }));
    assert.ok(await waitFor(() => h.frames.length >= 1));
    assert.equal(warn.lines.filter((l) => l.includes("WARNING smtp_host_allow_cidrs is non-empty (127.0.0.0/8)")).length, 1);
    assert.equal(h.frames[0].outcome, "accepted");
  } finally {
    warn.stop();
    await h.stop();
    await sink.close();
  }
});
