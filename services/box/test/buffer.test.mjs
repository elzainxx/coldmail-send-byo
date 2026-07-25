// Box outcome-buffering + persist-ack behavioral tests (Phase 7 §1.3, P7-4). These exercise the
// ACTUAL src/stream.js BoxStream — its real read loop, real outcome buffer, real replay-on-connect,
// real report_ack drop, and real TTL prune — over a REAL OS socket to a REAL local "fake Brain"
// (a net.Server). Only the TLS WRAP is substituted (via the box's injectable `dial` seam): mutual
// TLS is proven separately by the live-stack mTLS suite + the S4 handshake test, and — critically —
// the local host runs Norton "Web/Mail Shield" which MITM-intercepts localhost TLS, so a real-TLS
// loopback test here would assert Norton's behavior, not ours. The end-to-end no-duplicate proof over
// genuine mTLS is the live-stack harness (T1, Docker, Norton-immune). This is the same "stand in only
// the already-proven wire" philosophy the Go dispatch harness uses for the SMTP wire.
//
// Run: node --test services/box/test/buffer.test.mjs  (Node 20+).

import { test } from "node:test";
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { BoxStream } from "../src/stream.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CERTS = path.resolve(__dirname, "../../../secrets/dev/certs"); // read for realism; not used by the plain-socket dial

// startFakeBrain: a plain-TCP server speaking the newline-JSON box protocol. onFrame(frame, conn)
// handles each frame; conn.reply(obj) sends one back. Records connections so a test can drop one.
async function startFakeBrain(opts = {}) {
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
    conn.on("error", () => { /* ignore peer resets */ });
    server._lastConn = conn;
    if (opts.onConnect) opts.onConnect(conn);
  });
  server._lastConn = null;
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return { server, port: server.address().port, close: () => new Promise((r) => server.close(r)) };
}

// plainDial substitutes the TLS wrap with a plain TCP socket (see the file header for why). It
// preserves the exact BoxStream contract: return a socket, fire onSecure once connected.
function plainDial(o, _tlsMaterial, onSecure) {
  const sock = net.connect({ host: o.host, port: o.port }, onSecure);
  return sock;
}

function makeBox(port, extra = {}) {
  return new BoxStream({
    host: "127.0.0.1",
    port,
    boxId: "box-test",
    token: "tok",
    // certPath/keyPath/caPath are read at construction (fs.readFileSync) even with a custom dial, so
    // point them at the real dev certs; the plain dial ignores the material.
    certPath: path.join(CERTS, "box.crt"),
    keyPath: path.join(CERTS, "box.key"),
    caPath: path.join(CERTS, "ca.crt"),
    serverName: "brain",
    heartbeatSec: 3600, // no heartbeat noise during the short test
    dial: plainDial,
    ...extra,
  });
}

const waitFor = async (pred, ms = 3000) => {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (pred()) return true;
    await new Promise((r) => setTimeout(r, 15));
  }
  return false;
};

// A push_job whose lease is already expired → the box reports 'error' IMMEDIATELY without any SMTP
// session, exercising the true #bufferAndSend path end to end (the SMTP wire is covered elsewhere).
function expiredPushJob(jobId) {
  return {
    type: "push_job",
    request_id: "req-" + jobId,
    job_id: jobId,
    send: { recipient: "x@example.com", from_address: "u@ourdomain.com", subject: "s", body: "b", headers: {} },
    smtp: { host: "127.0.0.1", port: 1, username: "u", secret: "SECRET", security: "starttls" },
    lease_expires_at: new Date(Date.now() - 60_000).toISOString(),
  };
}

function brainPush(brain, jobId) {
  const conn = brain.server._lastConn;
  if (conn) conn.reply(expiredPushJob(jobId));
}

test("P7-4: box buffers an outcome and DROPS it only on report_ack", async () => {
  const reports = [];
  const acked = [];
  let ackNext = false; // the Brain withholds the ack until we flip this
  const brain = await startFakeBrain({
    onFrame: (msg, conn) => {
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
      if (msg.type === "report") {
        reports.push(msg.job_id);
        if (ackNext) conn.reply({ type: "report_ack", request_id: msg.request_id, job_id: msg.job_id, accepted: true });
      }
    },
  });
  const box = makeBox(brain.port, { onAck: (j) => acked.push(j) });
  try {
    await box.start();
    brainPush(brain, "job-A");
    assert.ok(await waitFor(() => reports.includes("job-A")), "Brain never received the outcome report");
    // No ack yet → the outcome MUST still be buffered (unacked outcomes are retained).
    assert.equal(box.outcomeBuffer.has("job-A"), true, "outcome dropped before any ack — must be retained until acked");
    assert.equal(acked.length, 0, "onAck fired with no report_ack sent");

    // Now the Brain acks → the box drops the buffered copy.
    ackNext = true;
    box.replayNow();
    assert.ok(await waitFor(() => acked.includes("job-A")), "box never processed the report_ack");
    assert.equal(box.outcomeBuffer.has("job-A"), false, "outcome NOT dropped after report_ack (would replay forever)");
  } finally {
    box.stop();
    await brain.close();
  }
});

test("P7-4: buffered outcome REPLAYS on reconnect (stream drop → the no-duplicate mechanism)", async () => {
  const reportsPerConn = [];
  let ackAll = false;
  const acked = [];
  const brain = await startFakeBrain({
    onConnect: () => { reportsPerConn.push([]); },
    onFrame: (msg, conn) => {
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
      if (msg.type === "report") {
        reportsPerConn[reportsPerConn.length - 1].push(msg.job_id);
        if (ackAll) conn.reply({ type: "report_ack", request_id: msg.request_id, job_id: msg.job_id, accepted: true });
      }
    },
  });
  const box = makeBox(brain.port, { onAck: (j) => acked.push(j) });
  try {
    await box.start();
    brainPush(brain, "job-R");
    assert.ok(await waitFor(() => reportsPerConn[0]?.includes("job-R")), "first connection never got the report");
    // The Brain did NOT ack → the outcome is still buffered. Drop the connection.
    assert.equal(box.outcomeBuffer.has("job-R"), true);
    ackAll = true; // the reconnect WILL ack
    box.sock.destroy(); // simulate a stream drop

    // Model the in-process reconnect (index.js supervise loop): re-run start() on the SAME box
    // instance — its buffer is intact — and assert the buffered outcome is REPLAYED on register_ack.
    await box.start();
    assert.ok(await waitFor(() => reportsPerConn.length >= 2), "Brain never saw the reconnect");
    assert.ok(await waitFor(() => reportsPerConn[1]?.includes("job-R")), "buffered outcome was NOT replayed on reconnect (duplicate/loss risk)");
    assert.ok(await waitFor(() => acked.includes("job-R")), "reconnect replay was never acked");
    assert.equal(box.outcomeBuffer.has("job-R"), false, "outcome still buffered after the reconnect ack");
  } finally {
    box.stop();
    await brain.close();
  }
});

test("P7-4: an outcome older than the TTL is DROPPED (bounded buffer → falls back to reclaim)", async () => {
  let clock = 1_000_000;
  const brain = await startFakeBrain({
    onFrame: (msg, conn) => {
      // TTL = 10s; never ack, so retention is governed purely by the TTL.
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 10 });
    },
  });
  const box = makeBox(brain.port, { now: () => clock });
  try {
    await box.start();
    brainPush(brain, "job-T");
    assert.ok(await waitFor(() => box.outcomeBuffer.has("job-T")), "outcome never buffered");
    // Advance the clock past the TTL and trigger a prune (a replay pass prunes first).
    clock += 11_000;
    box.replayNow();
    assert.equal(box.outcomeBuffer.has("job-T"), false, "outcome past TTL was NOT dropped (buffer unbounded — P7-4 violated)");
  } finally {
    box.stop();
    await brain.close();
  }
});
