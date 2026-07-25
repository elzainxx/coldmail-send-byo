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

// brainPushAttempt pushes the SAME job with a DIFFERENT push_job.request_id — i.e. the Brain
// reclaimed a first attempt and re-dispatched the job to this same box. request_id is minted per
// attempt by the Brain and echoed in the box's report, so it is the box's only attempt identity.
function brainPushAttempt(brain, jobId, requestID) {
  const conn = brain.server._lastConn;
  if (conn) conn.reply({ ...expiredPushJob(jobId), request_id: requestID });
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

// §2.3 — the DB-down case: the stream stays LIVE, the Brain receives the report but CANNOT persist it
// so it sends NO ack, and the spec requires the box to "keep replaying until it receives a post-commit
// ack". This drives the PRODUCTION trigger only — the real heartbeat interval timer inside the real
// BoxStream. It never calls replayNow() and never reconnects; if replay were reconnect-only (as it was
// before this fix) the report count would stay at 1 forever and Brain-side reclaim (lease 120s ≪ TTL
// 900s) would re-send the mail: a silent duplicate.
test("P7-4/§2.3: box KEEPS REPLAYING an unacked outcome on a LIVE stream (no reconnect, no test seam)", async () => {
  const reports = [];
  const acked = [];
  let ackNext = false; // Brain is "DB down" until we flip this
  const conns = [];
  const brain = await startFakeBrain({
    onConnect: (c) => conns.push(c),
    onFrame: (msg, conn) => {
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
      if (msg.type === "report") {
        reports.push(msg.job_id);
        // Persist FAILED (DB down) → deliberately send NO report_ack. This is the §2.3 condition.
        if (ackNext) conn.reply({ type: "report_ack", request_id: msg.request_id, job_id: msg.job_id, accepted: true });
      }
    },
  });
  // heartbeatSec 0.15 → the real setInterval fires the production replay pass several times/second.
  const box = makeBox(brain.port, { heartbeatSec: 0.15, onAck: (j) => acked.push(j) });
  try {
    await box.start();
    brainPush(brain, "job-L");
    assert.ok(await waitFor(() => reports.length >= 1), "Brain never received the first report");
    // THE ASSERTION: with no ack, the SAME outcome arrives again — driven only by the box's own timer.
    assert.ok(await waitFor(() => reports.filter((j) => j === "job-L").length >= 3, 4000),
      `unacked outcome was NOT re-delivered on a live stream (saw ${reports.filter((j) => j === "job-L").length} deliveries) — §2.3 "keeps replaying until it receives a post-commit ack" unsatisfied`);
    assert.equal(conns.length, 1, "the replay must come from the LIVE stream, not a reconnect");
    assert.equal(box.outcomeBuffer.has("job-L"), true, "outcome must stay buffered while unacked");
    assert.equal(acked.length, 0, "no ack was sent, yet the box reported one");

    // NEGATIVE CONTROL (anti-vacuity): once the DB is back and the Brain acks post-commit, replaying
    // STOPS. Without this, a test asserting "reports keep growing" would also pass a box that replays
    // forever and ignores acks — which would hammer the Brain and never converge.
    ackNext = true;
    assert.ok(await waitFor(() => acked.includes("job-L"), 4000), "box never processed the post-commit ack");
    assert.equal(box.outcomeBuffer.has("job-L"), false, "acked outcome still buffered");
    const settled = reports.length;
    await new Promise((r) => setTimeout(r, 700)); // several more heartbeat ticks
    assert.equal(reports.length, settled, "box kept replaying AFTER a post-commit ack (never converges)");
  } finally {
    box.stop();
    await brain.close();
  }
});

// §1.3 requires the buffer be bounded by the TTL *and a max entry count*. Proves the cardinality bound
// exists, evicts the OLDEST, and always retains the NEWEST outcome (the one least likely to have been
// reclaimed already, so keeping it is what minimizes duplicates).
test("P7-4/§1.3: buffer is bounded by a MAX ENTRY COUNT — overflow evicts the oldest, keeps the newest", async () => {
  const brain = await startFakeBrain({
    onFrame: (msg, conn) => {
      // Never ack: retention is governed purely by the bounds under test.
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
    },
  });
  const box = makeBox(brain.port, { outcomeBufferMaxEntries: 2 });
  try {
    await box.start();
    for (const id of ["job-1", "job-2"]) {
      brainPush(brain, id);
      assert.ok(await waitFor(() => box.outcomeBuffer.has(id)), `${id} never buffered`);
    }
    assert.equal(box.outcomeBuffer.size, 2, "buffer should be exactly at capacity");
    // Positive control: at capacity, nothing has been dropped yet.
    assert.equal(box.overflowDropped, 0, "dropped an entry before the buffer was over capacity");

    brainPush(brain, "job-3");
    assert.ok(await waitFor(() => box.outcomeBuffer.has("job-3")), "the NEWEST outcome was not buffered on overflow");
    assert.equal(box.outcomeBuffer.size, 2, `buffer grew past its max entry count (size=${box.outcomeBuffer.size}) — unbounded, §1.3 violated`);
    assert.equal(box.outcomeBuffer.has("job-1"), false, "overflow evicted something other than the OLDEST entry");
    assert.equal(box.outcomeBuffer.has("job-2"), true, "overflow evicted more than it needed to");
    assert.equal(box.overflowDropped, 1, "overflow drop was not counted");
  } finally {
    box.stop();
    await brain.close();
  }
});

// Review gate R2 findings 1–2. The buffer is keyed by job_id, but a job can be pushed to this SAME box
// TWICE: attempt 1's ack is lost, the Brain reclaims the lease and re-dispatches, and the box gets the
// same job_id again with a NEW push_job.request_id. Two distinct hazards follow, both proved here.
test("P7-4/R2: a job pushed AGAIN buffers the LIVE attempt's outcome (the superseded one is discarded)", async () => {
  const reports = [];
  const brain = await startFakeBrain({
    onFrame: (msg, conn) => {
      // Never ack: the buffer's contents are the whole subject of this test.
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
      if (msg.type === "report") reports.push(msg);
    },
  });
  const box = makeBox(brain.port);
  try {
    await box.start();

    // Attempt 1: pushed, reported, never acked → buffered.
    brainPushAttempt(brain, "job-R", "req-attempt-1");
    assert.ok(await waitFor(() => box.outcomeBuffer.has("job-R")), "attempt 1's outcome never buffered");
    assert.equal(box.outcomeBuffer.get("job-R").report.request_id, "req-attempt-1");
    // Positive control: with only one attempt seen, nothing was superseded.
    assert.equal(box.supersededDropped, 0, "counted a superseded entry before any second push");

    // Attempt 2: the Brain reclaimed and re-dispatched the SAME job to this box.
    brainPushAttempt(brain, "job-R", "req-attempt-2");
    assert.ok(
      await waitFor(() => box.outcomeBuffer.get("job-R")?.report.request_id === "req-attempt-2"),
      "the LIVE attempt's outcome was NOT buffered — a stale entry for the same job_id blocked it, so a lost send would replay the dead attempt's result forever and lose the real one",
    );
    assert.equal(box.outcomeBuffer.size, 1, "both attempts are buffered under one job_id — only the live attempt may be retained");
    assert.equal(box.supersededDropped, 1, "the superseded entry was not counted as dropped");

    // And a replay now carries the LIVE attempt only.
    reports.length = 0;
    box.replayNow();
    assert.ok(await waitFor(() => reports.length > 0), "replay sent nothing");
    assert.equal(reports[0].request_id, "req-attempt-2", "replay re-sent the SUPERSEDED attempt's outcome");
  } finally {
    box.stop();
    await brain.close();
  }
});

test("P7-4/R2: a report_ack for a SUPERSEDED attempt does not drop the live attempt's outcome", async () => {
  const acked = [];
  let lastConn = null;
  const brain = await startFakeBrain({
    onFrame: (msg, conn) => {
      lastConn = conn;
      if (msg.type === "register") conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 });
    },
  });
  const box = makeBox(brain.port, { onAck: (j) => acked.push(j) });
  try {
    await box.start();
    brainPushAttempt(brain, "job-S", "req-attempt-1");
    assert.ok(await waitFor(() => box.outcomeBuffer.has("job-S")), "attempt 1 never buffered");
    brainPushAttempt(brain, "job-S", "req-attempt-2");
    assert.ok(await waitFor(() => box.outcomeBuffer.get("job-S")?.report.request_id === "req-attempt-2"), "attempt 2 never buffered");

    // A LATE ack for attempt 1 arrives (the Brain committed the first attempt's outcome, or an ack was
    // merely delayed in flight). It must NOT drop attempt 2's still-unacked outcome.
    lastConn.reply({ type: "report_ack", request_id: "req-attempt-1", job_id: "job-S", accepted: true });
    await new Promise((r) => setTimeout(r, 200));
    assert.equal(
      box.outcomeBuffer.has("job-S"), true,
      "an ack for the SUPERSEDED attempt dropped the live attempt's outcome — the box would stop replaying an outcome the Brain never committed, losing it silently",
    );
    assert.equal(acked.length, 0, "onAck fired for an attempt whose outcome is not the buffered one");

    // NEGATIVE CONTROL: the matching ack DOES drop it (so the assertion above is about the mismatch,
    // not about an ack path that never works).
    lastConn.reply({ type: "report_ack", request_id: "req-attempt-2", job_id: "job-S", accepted: true });
    assert.ok(await waitFor(() => acked.includes("job-S")), "the MATCHING report_ack did not drop the buffered outcome");
    assert.equal(box.outcomeBuffer.has("job-S"), false, "buffered outcome survived its own ack (would replay forever)");
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
