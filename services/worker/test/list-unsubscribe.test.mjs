// Phase 5 S4 — THE ONE-CLICK UNSUBSCRIBE PAIR (push_job.send.headers["List-Unsubscribe"] and ["List-Unsubscribe-Post"], built by the Control from
// send_byo.jobs.unsubscribe_url). The worker RELAYS Control-supplied headers; these tests pin the properties that make the relay safe for a pair that
// mailbox providers parse strictly, from the renderer up to the bytes a real SMTP server receives.
//
//   1  EVERY VARIATION PROFILE (all 19 golden cases, with and without a plain-text part): the pair appears in the header section EXACTLY ONCE each,
//      with the exact value the Control sent, and it is the ONLY difference: removing those two lines from the with-pair render gives, byte for byte,
//      the render of the same case without the pair (so header re-ordering, encoding and boundary rotation, the User-Agent and Precedence stages and
//      every content stage leave the two headers untouched and add nothing of their own).
//      <- the paired control is the same case WITHOUT the pair: it carries neither header, and the with-pair render differs from it (so the
//         comparison can fail)
//   2  THE WIRE: through the REAL worker (WorkerStream -> runSmtpSession) to a REAL SMTP server, the message the server accepts holds both headers exactly
//      once, exactly as sent, and is exactly the assembled bytes; the twin without the pair holds neither. The frame is also sent in the key order Go's
//      encoding/json gives a map (alphabetical, so the pair sits BETWEEN the §8 headers in the object), because that is the order the live Control writes.
//
// WHICH PATH THESE TESTS DRIVE. Group 1 calls assembleDataBytes, the function stream.js's #runJob calls on the push_job it received (`assembleDataBytes(pj.send, pj.variation,
// jobId)`); group 2 does not call it directly at all: a JSON push_job frame goes over a socket into the real WorkerStream, which decodes it, runs #runJob, assembles, and speaks SMTP to
// a real server. That is the path a live message takes, with only the mTLS wrap substituted (proven elsewhere). Nothing between the frame's `send.headers` and the DATA bytes
// filters a Control-supplied header: the pair survives every variation stage. (The first live run of SUITE 16 reported the pair missing; the cause was the harness fetching the
// wrong message from Mailpit, whose /api/v1/messages ignores a `query` parameter and returns the newest message of the whole sink, not anything in this worker.)
//
// Run: node --test services/worker/test/list-unsubscribe.test.mjs   (Node 24)

import { test } from "node:test";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { assembleDataBytes } from "../src/assemble.js";
import { WorkerStream } from "../src/stream.js";
import { FIXED_JOB_ID, GOLDEN_CASES, goldenSend } from "./golden-cases.mjs";
import { startSink } from "./sink.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const CERTS = path.resolve(here, "../../../secrets/dev/certs");
const LOOPBACK_ALLOW = ["127.0.0.0/8"]; // the test sink listens on loopback: the destination guard (C4) must be told so

const LINK = "https://unsubscribe.example.test/u/AbC-123_xyz";
const UNSUB = `<${LINK}>`;
const POST = "List-Unsubscribe=One-Click";
const TEXT = "Hello,\nthis is the plain-text alternative.\n";

// The pair exactly as the Control builds it (dispatch/headers.go listUnsubscribeHeaders), added to the Control-supplied header map.
const withPair = (send) => ({ ...send, headers: { ...send.headers, "List-Unsubscribe": UNSUB, "List-Unsubscribe-Post": POST } });

// splitHeaderSection returns the header lines and the rest (starting at the blank line) of one rendered message.
function splitHeaderSection(raw) {
  const i = raw.indexOf("\r\n\r\n");
  assert.ok(i > 0, "no header/body separator in the rendered message");
  return { lines: raw.slice(0, i).split("\r\n"), rest: raw.slice(i) };
}
const isPairLine = (l) => /^list-unsubscribe(-post)?:/i.test(l);
const pairLines = (raw) => splitHeaderSection(raw).lines.filter(isPairLine);

// ===== 1. every variation profile ==================================================================================

for (const withText of [false, true]) {
  test(`list-unsubscribe: in every golden case (${withText ? "with" : "without"} a plain-text part) the pair is in the header section exactly once each, exact, and the ONLY difference`, () => {
    assert.ok(GOLDEN_CASES.length >= 19, "the golden set shrank: this test would cover less than it claims");
    for (const c of GOLDEN_CASES) {
      const base = withText ? { ...goldenSend(c.jobId), text: TEXT } : goldenSend(c.jobId);
      const plain = assembleDataBytes(base, c.variation, c.jobId);
      const paired = assembleDataBytes(withPair(base), c.variation, c.jobId);

      // the control: without the pair NEITHER header exists, anywhere in the message
      assert.equal(pairLines(plain).length, 0, `${c.name}: a message WITHOUT the pair carries one of its headers`);
      assert.ok(!/list-unsubscribe/i.test(plain), `${c.name}: a message WITHOUT the pair mentions List-Unsubscribe`);

      // with the pair: each header exactly once, with the exact value, in the HEADER section
      const lines = pairLines(paired);
      assert.deepEqual(lines.slice().sort(), [`List-Unsubscribe-Post: ${POST}`, `List-Unsubscribe: ${UNSUB}`].sort(), `${c.name}: the pair is not exactly the two headers, once each, as sent: ${JSON.stringify(lines)}`);
      assert.equal((paired.match(/^list-unsubscribe(-post)?:/gim) || []).length, 2, `${c.name}: a List-Unsubscribe header line appears somewhere other than the header section (a body part, a duplicate)`);

      // the pair is the ONLY difference: take the two lines out and the message is the one rendered without them
      const { lines: all, rest } = splitHeaderSection(paired);
      const stripped = all.filter((l) => !isPairLine(l)).join("\r\n") + rest;
      assert.equal(stripped, plain, `${c.name}: removing the two header lines does not give the render without them — a stage moved, rewrote or added something because of the pair`);
      assert.notEqual(paired, plain, `${c.name}: the with-pair render equals the without-pair render (the comparison could not fail)`);
    }
  });
}

test("list-unsubscribe: a message built without the pair is byte-identical to the golden files (the pair is additive; nothing else moved)", () => {
  // the golden files themselves are asserted in plaintext.test.mjs; here the pair's absence is what is pinned, over every case and both shapes
  for (const c of GOLDEN_CASES) {
    for (const text of [undefined, TEXT]) {
      const send = text === undefined ? goldenSend(c.jobId) : { ...goldenSend(c.jobId), text };
      assert.ok(!/list-unsubscribe/i.test(assembleDataBytes(send, c.variation, c.jobId)), `${c.name}: List-Unsubscribe appeared in a render with no pair`);
    }
  }
});

test("list-unsubscribe: the headers land among the Control-supplied ones after the §8 set, and the §8 set keeps its order", () => {
  const send = withPair(goldenSend(FIXED_JOB_ID));
  const out = assembleDataBytes(send, undefined, FIXED_JOB_ID);
  const names = splitHeaderSection(out).lines.map((l) => l.slice(0, l.indexOf(":")));
  assert.deepEqual(names.slice(0, 5), ["From", "To", "Subject", "Reply-To", "Message-ID"], "the §8 minimum set lost its order");
  assert.ok(names.indexOf("List-Unsubscribe") > names.indexOf("Date") && names.indexOf("List-Unsubscribe-Post") > names.indexOf("Date"), `the pair is not after the §8 set: ${names}`);
  assert.equal(names.filter((n) => n === "List-Unsubscribe").length, 1);
  assert.equal(names.filter((n) => n === "List-Unsubscribe-Post").length, 1);
});

// ===== 2. the wire =================================================================================================

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
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return { server, port: server.address().port, close: () => new Promise((r) => server.close(r)) };
}

function makeStream(port) {
  return new WorkerStream({
    host: "127.0.0.1", port, workerId: "worker-unsub-test", token: "tok",
    certPath: path.join(CERTS, "harness-client.crt"), keyPath: path.join(CERTS, "harness-client.key"), caPath: path.join(CERTS, "ca.crt"),
    serverName: "control", heartbeatSec: 3600,
    dial: (o, _tls, onSecure) => net.connect({ host: o.host, port: o.port }, onSecure),
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

const crlf = (s) => s.replace(/\r\n/g, "\n").replace(/\n/g, "\r\n"); // what smtp.js's DATA normalisation does to bare LFs

// sendThroughWorker runs ONE job through the real worker against a real SMTP server and returns what that server accepted and the worker's report.
async function sendThroughWorker({ pair, text, variation, jobId = FIXED_JOB_ID, goKeyOrder = false }) {
  const sink = await startSink({ authResult: "ok" });
  const reports = [];
  const base = goldenSend(jobId);
  let headers = pair ? { ...base.headers, "List-Unsubscribe": UNSUB, "List-Unsubscribe-Post": POST } : base.headers;
  // Go's encoding/json writes a map[string]string in sorted key order; the live Control's frame therefore carries Date, From, List-Unsubscribe, List-Unsubscribe-Post, Message-ID, ...
  if (goKeyOrder) headers = Object.fromEntries(Object.entries(headers).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
  const send = { recipient: base.recipient, from_address: base.from_address, subject: base.subject, body: base.body, headers, ...(text ? { text } : {}) };
  const frame = {
    type: "push_job", request_id: "req-unsub", job_id: jobId, attempt_token: 1, send,
    smtp: { host: "127.0.0.1", port: sink.port, username: "u", secret: "S-" + crypto.randomBytes(8).toString("hex"), security: "starttls", operation_timeout_sec: 3, dest_allow_cidrs: LOOPBACK_ALLOW },
    lease_expires_at: new Date(Date.now() + 60_000).toISOString(),
    ...(variation ? { variation } : {}),
  };
  const control = await startFakeControl({
    onFrame: (msg, conn) => {
      if (msg.type === "register") { conn.reply({ type: "register_ack", request_id: msg.request_id, accepted: true, outcome_buffer_ttl_sec: 900 }); setImmediate(() => conn.reply(frame)); }
      if (msg.type === "report") { reports.push(msg); conn.reply({ type: "report_ack", request_id: msg.request_id, job_id: msg.job_id, accepted: true }); }
    },
  });
  const stream = makeStream(control.port);
  try {
    await stream.start();
    assert.ok(await waitFor(() => reports.length === 1, 15000), "the worker never reported on the job");
    return { report: reports[0], accepted: sink.messages.slice(), send };
  } finally {
    stream.stop();
    await control.close();
    await sink.close();
  }
}

test("list-unsubscribe wire: the SMTP server receives both headers exactly once, as sent, and exactly the assembled bytes", async () => {
  for (const text of [undefined, TEXT]) {
    const { report, accepted, send } = await sendThroughWorker({ pair: true, text });
    assert.equal(report.outcome, "accepted", `the worker's report was ${JSON.stringify(report)}`);
    assert.equal(accepted.length, 1, "the server must accept exactly one message");
    assert.deepEqual(pairLines(accepted[0]).sort(), [`List-Unsubscribe-Post: ${POST}`, `List-Unsubscribe: ${UNSUB}`].sort());
    assert.equal((accepted[0].match(/^list-unsubscribe(-post)?:/gim) || []).length, 2, "a List-Unsubscribe header line appears outside the header section, or twice");
    assert.equal(accepted[0], crlf(assembleDataBytes(send, undefined, FIXED_JOB_ID)), "the bytes on the wire are not the assembled message");
  }
});

test("list-unsubscribe wire — paired control: the twin WITHOUT the pair arrives with neither header and is not the message above", async () => {
  const { report, accepted } = await sendThroughWorker({ pair: false });
  assert.equal(report.outcome, "accepted", `the worker's report was ${JSON.stringify(report)}`);
  assert.equal(accepted.length, 1);
  assert.ok(!/list-unsubscribe/i.test(accepted[0]), "a job without a link was sent with a List-Unsubscribe header");
  const paired = await sendThroughWorker({ pair: true });
  assert.notEqual(accepted[0], paired.accepted[0]);
});

test("list-unsubscribe wire: under the all-on variation profile the pair still arrives exactly once, and a retry puts identical bytes on the wire", async () => {
  const kitchen = GOLDEN_CASES.find((c) => c.name === "kitchen-sink");
  const one = await sendThroughWorker({ pair: true, text: TEXT, variation: kitchen.variation, jobId: kitchen.jobId });
  const two = await sendThroughWorker({ pair: true, text: TEXT, variation: kitchen.variation, jobId: kitchen.jobId });
  assert.equal(one.report.outcome, "accepted");
  assert.deepEqual(pairLines(one.accepted[0]).sort(), [`List-Unsubscribe-Post: ${POST}`, `List-Unsubscribe: ${UNSUB}`].sort());
  assert.equal(one.accepted[0], two.accepted[0], "a retry of the same item sent different bytes");
});

test("list-unsubscribe wire: a frame whose headers arrive in the Go Control's sorted key order still puts the pair once each in the header block, and the §8 headers keep their order", async () => {
  for (const variation of [undefined, GOLDEN_CASES.find((c) => c.name === "header-stages").variation]) {
    const { report, accepted } = await sendThroughWorker({ pair: true, text: TEXT, goKeyOrder: true, variation, jobId: variation ? "golden-job-headers" : FIXED_JOB_ID });
    assert.equal(report.outcome, "accepted", `the worker's report was ${JSON.stringify(report)}`);
    assert.equal(accepted.length, 1);
    const { lines } = splitHeaderSection(accepted[0]);
    assert.deepEqual(lines.filter(isPairLine).sort(), [`List-Unsubscribe-Post: ${POST}`, `List-Unsubscribe: ${UNSUB}`].sort(), "the pair is not exactly the two headers, once each, in the header block");
    if (!variation) {
      const names = lines.map((l) => l.slice(0, l.indexOf(":")));
      assert.deepEqual(names.slice(0, 5), ["From", "To", "Subject", "Reply-To", "Message-ID"], "the §8 set lost its order because the frame's keys were sorted");
    }
  }
  // the paired control: the same sorted frame WITHOUT the pair has neither header
  const none = await sendThroughWorker({ pair: false, goKeyOrder: true });
  assert.equal(none.report.outcome, "accepted");
  assert.ok(!/list-unsubscribe/i.test(none.accepted[0]), "a sorted frame without the pair was sent with a List-Unsubscribe header");
});
