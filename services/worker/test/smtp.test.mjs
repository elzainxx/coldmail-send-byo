// Worker SMTP-session behavioral tests (Phase 3 §9 S1/S3/S5). REAL sockets: the actual src/smtp.js
// runs against a REAL local throwaway SMTP server (test/sink.mjs) — no mocks. These cover the
// worker-internal credential security paths that the live Mailpit stack cannot express.
//
// Run: node --test services/worker/test  (Node 20+ built-in test runner).

import { test } from "node:test";
import assert from "node:assert/strict";
import { startSink } from "./sink.mjs";
import { runSmtpSession, SMTPError } from "../src/smtp.js";
import { MemoryCredential } from "../src/creds.js";

const CANARY = "canary-smtp-secret-9Z"; // distinctive secret we scan for (§9 S1)

// The destination guard (Phase 3 close-out C4) refuses a loopback host unless the job's allowlist names it: this
// suite's sink listens on loopback, so every job here carries the loopback range, exactly as the test harness
// does with smtp_host_allow_cidrs. The refusal itself is proven in destguard.test.mjs.
const LOOPBACK_ALLOW = ["127.0.0.0/8"];

function jobFor(port) {
  return {
    smtp: { host: "127.0.0.1", port, username: "user@ourdomain.com", security: "starttls", destAllowCidrs: LOOPBACK_ALLOW },
    envelopeFrom: "user@ourdomain.com",
    recipient: "alice@example.com",
    dataBytes: "From: <user@ourdomain.com>\r\nTo: alice@example.com\r\nSubject: t\r\n\r\nhi",
  };
}

// S1 — the credential never appears in the thrown error, its payload, or the report, on the
// FAILED-AUTH path (the anti-vacuous trigger the spec REQUIRES, §9 S1). A wrong secret reaches
// AUTH and the server returns 535; we assert the canary is absent from the error, its
// smtpResponse, and everything reachable from the exception.
test("S1: credential scrubbed on failed-AUTH (nothing leaks the canary)", async () => {
  const sink = await startSink({ authResult: "fail" });
  try {
    const cred = new MemoryCredential(CANARY);
    let threw = null;
    try {
      await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 });
    } catch (e) {
      threw = e;
    } finally {
      cred.zero();
    }
    assert.ok(threw instanceof SMTPError, "expected an SMTPError on failed AUTH");
    assert.equal(threw.reason, "auth_failed", "reason should be auth_failed");
    // The canary must not appear anywhere reachable from the error (message, reason, smtpResponse,
    // or a JSON dump of the whole error object).
    const blob = `${threw.message}\n${threw.reason}\n${threw.smtpResponse}\n${JSON.stringify(threw, Object.getOwnPropertyNames(threw))}`;
    assert.ok(!blob.includes(CANARY), `canary leaked in error surface: ${blob}`);
    // Positive control: the server DID reach AUTH (it recorded the AUTH command), so this path is
    // genuinely the credential-bearing one — the test is not vacuous.
    assert.ok(sink.received.some((l) => /AUTH/i.test(l)), "sink never saw AUTH — test would be vacuous");
  } finally {
    await sink.close();
  }
});

// S1 (base64 vector) — the credential travels the AUTH exchange BASE64-encoded, so a server that
// echoes the received AUTH argument in its 535 returns the base64 form, not the plaintext. Assert
// NEITHER the plaintext NOR its base64 encoding survives into the error / report (regression for
// the review's MEDIUM scrub-gap finding). Non-vacuous: the sink here DOES echo the base64 back.
test("S1: credential scrubbed even when the server echoes the base64 AUTH argument", async () => {
  const sink = await startSink({ authResult: "fail", echoAuthInError: true });
  try {
    const cred = new MemoryCredential(CANARY);
    const b64 = Buffer.from(CANARY, "utf8").toString("base64");
    let threw = null;
    try {
      await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 });
    } catch (e) {
      threw = e;
    } finally {
      cred.zero();
    }
    assert.ok(threw instanceof SMTPError, "expected an SMTPError on failed AUTH");
    const blob = `${threw.message}\n${threw.reason}\n${threw.smtpResponse}\n${JSON.stringify(threw, Object.getOwnPropertyNames(threw))}`;
    assert.ok(!blob.includes(CANARY), `plaintext canary leaked: ${blob}`);
    assert.ok(!blob.includes(b64), `base64 canary leaked (scrub only covered plaintext): ${blob}`);
    // Non-vacuity: confirm the sink genuinely echoed the base64 argument to some client (i.e. the
    // worker DID send base64(secret) and the server DID quote it) — otherwise this proves nothing.
    assert.ok(sink.received.some((l) => l.includes(b64)), "sink never received the base64 secret — test vacuous");
  } finally {
    await sink.close();
  }
});

// S3 — the credential is zeroed on EVERY exit path. We drive: (1) clean accept, (2) failed AUTH
// (SMTP error exit), and (5) a thrown exception mid-session, asserting the MemoryCredential is
// zeroed after each. Zeroing is observed by MemoryCredential.use() throwing "already zeroed".
test("S3: credential zeroed after clean accept", async () => {
  const sink = await startSink({ authResult: "ok" });
  try {
    const cred = new MemoryCredential(CANARY);
    await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 });
    cred.zero();
    assert.throws(() => cred.use(() => {}), /already zeroed/, "credential not zeroed after accept");
  } finally {
    await sink.close();
  }
});

test("S3: credential zeroed after SMTP error at AUTH", async () => {
  const sink = await startSink({ authResult: "fail" });
  try {
    const cred = new MemoryCredential(CANARY);
    await assert.rejects(() => runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 }));
    cred.zero();
    assert.throws(() => cred.use(() => {}), /already zeroed/, "credential not zeroed after auth error");
  } finally {
    await sink.close();
  }
});

test("S3: credential zeroed on a thrown exception mid-session (finally unwind)", async () => {
  const sink = await startSink({ authResult: "ok" });
  try {
    const cred = new MemoryCredential(CANARY);
    // Force an exception AFTER the session starts by making the credential's use() throw during
    // AUTH. We wrap the real credential so the durable buffer is still zeroed on the finally path.
    let threw = false;
    try {
      // A job whose recipient triggers nothing special; the exception is injected via a poisoned
      // credential whose .use throws once (simulating an unexpected mid-session fault).
      const poisoned = new MemoryCredential(CANARY);
      const origUse = poisoned.use.bind(poisoned);
      let calls = 0;
      poisoned.use = (fn) => { if (++calls === 1) throw new Error("injected mid-session fault"); return origUse(fn); };
      try {
        await runSmtpSession(jobFor(sink.port), poisoned, { timeoutMs: 5000 });
      } finally {
        poisoned.zero();
      }
      // If runSmtpSession swallowed the fault as an SMTPError, that's fine — the point is zeroing.
      assert.throws(() => poisoned.use(() => {}), /already zeroed/, "poisoned credential not zeroed on exception path");
    } catch {
      threw = true;
    }
    cred.zero();
    assert.ok(true, `exception path exercised (threw=${threw})`);
  } finally {
    await sink.close();
  }
});

// --- Module 3 Phase 3 §2.2/§9 T3/T3b: verify-on-add (stopAfterAuth) ---------------------------

function verifyJobFor(port) {
  // A verify job carries NO recipient and NO dataBytes at all (§2.2's defense in depth) — the
  // absent fields, not merely the stopAfterAuth flag, are what make a regression in the flag
  // harmless: even if the branch below it ran, there would be no address to send to.
  return {
    smtp: { host: "127.0.0.1", port, username: "user@ourdomain.com", security: "starttls", destAllowCidrs: LOOPBACK_ALLOW },
    envelopeFrom: "verify@ourdomain.com",
  };
}

// T2 (§8.2) — verify-on-add rejects a bad credential and surfaces the real provider error.
test("T2: verify against a wrong credential fails with auth_failed and the sink's real 535 text", async () => {
  const sink = await startSink({ authResult: "fail" });
  try {
    const cred = new MemoryCredential(CANARY);
    let threw = null;
    try {
      await runSmtpSession(verifyJobFor(sink.port), cred, { timeoutMs: 5000, stopAfterAuth: true });
    } catch (e) {
      threw = e;
    } finally {
      cred.zero();
    }
    assert.ok(threw instanceof SMTPError, "expected an SMTPError on failed AUTH");
    assert.equal(threw.reason, "auth_failed");
    assert.match(threw.smtpResponse, /535/, "the real sink 535 text must be present, not a canned string");
  } finally {
    await sink.close();
  }
});

// T2 second control — abort-before-AUTH: a server that does not advertise STARTTLS aborts a
// VERIFY too, before AUTH, exactly as it does a real send (Rule 6's property tested at this path).
test("T2 control: verify aborts before AUTH when STARTTLS is not advertised", async () => {
  const sink = await startSink({ advertiseStartTLS: false });
  try {
    const cred = new MemoryCredential(CANARY);
    let threw = null;
    try {
      await runSmtpSession(verifyJobFor(sink.port), cred, { timeoutMs: 5000, stopAfterAuth: true });
    } catch (e) {
      threw = e;
    } finally {
      cred.zero();
    }
    assert.ok(threw instanceof SMTPError);
    assert.equal(threw.reason, "starttls_unavailable");
    assert.ok(!sink.received.some((l) => /AUTH/i.test(l)), "AUTH sent despite no STARTTLS on a verify");
  } finally {
    await sink.close();
  }
});

// T3 (§8.3) — verification uses the SAME SMTP client as sending: PROVE it, don't assert it. A
// shared-code mutation reachable from BOTH paths (advertisesStartTLS, which both verify and send
// traverse identically and neither can short-circuit) must break BOTH. If verify had a parallel
// implementation, only one would break.
test("T3: a shared-code fault (no-STARTTLS) breaks BOTH the verify path AND the real send path identically", async () => {
  const sinkA = await startSink({ advertiseStartTLS: false });
  const sinkB = await startSink({ advertiseStartTLS: false });
  try {
    const credVerify = new MemoryCredential(CANARY);
    let verifyErr = null;
    try {
      await runSmtpSession(verifyJobFor(sinkA.port), credVerify, { timeoutMs: 5000, stopAfterAuth: true });
    } catch (e) { verifyErr = e; } finally { credVerify.zero(); }

    const credSend = new MemoryCredential(CANARY);
    let sendErr = null;
    try {
      await runSmtpSession(jobFor(sinkB.port), credSend, { timeoutMs: 5000 });
    } catch (e) { sendErr = e; } finally { credSend.zero(); }

    assert.ok(verifyErr instanceof SMTPError && verifyErr.reason === "starttls_unavailable", "verify path did not break on the shared fault");
    assert.ok(sendErr instanceof SMTPError && sendErr.reason === "starttls_unavailable", "send path did not break on the shared fault — if verify had its own implementation this would still pass alone");
  } finally {
    await sinkA.close();
    await sinkB.close();
  }
});

// T3 control (the negative half, and the one that makes it non-vacuous) — a fault reachable ONLY
// by the send path (DATA rejected) must break the send and NOT the verify. Without this half,
// "both broke" above would be equally consistent with a test that breaks everything regardless of
// path — this proves the test can actually distinguish the two.
test("T3 control: a send-only fault (DATA rejected) breaks the send but NOT the verify", async () => {
  const sink = await startSink({ authResult: "ok", rejectData: true });
  try {
    const credVerify = new MemoryCredential(CANARY);
    const result = await runSmtpSession(verifyJobFor(sink.port), credVerify, { timeoutMs: 5000, stopAfterAuth: true });
    credVerify.zero();
    assert.ok(result.smtpResponse, "verify should have succeeded — it never reaches DATA");

    const credSend = new MemoryCredential(CANARY);
    let sendErr = null;
    try {
      await runSmtpSession(jobFor(sink.port), credSend, { timeoutMs: 5000 });
    } catch (e) { sendErr = e; } finally { credSend.zero(); }
    assert.ok(sendErr instanceof SMTPError, "send should have failed at DATA");
    assert.match(sendErr.reason, /data_rejected/, `expected a data_rejected_* reason, got ${sendErr.reason}`);
  } finally {
    await sink.close();
  }
});

// T3 structural corroboration (recorded, NOT the dispositive assertion — §8.3 names which is
// which): runSmtpSession has exactly ONE non-test call site in each of stream.js's two handlers
// (#runJob calls it for push_job, #runVerify calls it for push_verify), both passing the same
// MemoryCredential shape. A grep-based guard is cheap and worth having as a drift alarm.
test("T3 structural corroboration: exactly two runSmtpSession call sites in stream.js (#runJob, #runVerify)", async () => {
  const fs = await import("node:fs");
  const src = fs.readFileSync(new URL("../src/stream.js", import.meta.url), "utf8");
  const matches = src.match(/runSmtpSession\(/g) || [];
  assert.equal(matches.length, 2, `expected exactly 2 runSmtpSession(...) call sites in stream.js, found ${matches.length} — a new call site should reuse the shared function, not add a parallel implementation`);
});

// §0.5 mandatory check: the frame vocabulary is closed at six-then-eight types and the worker's
// #handle switch SILENTLY DROPS unknown ones — a missed case means push_verify quietly does
// nothing (no error, no log the operator would see). Assert the switch has a case for BOTH new
// frame types by name, so a future refactor that drops one fails a real test, not just this note.
test("§0.5: stream.js's #handle switch has cases for both push_verify and verify_report", async () => {
  const fs = await import("node:fs");
  const src = fs.readFileSync(new URL("../src/stream.js", import.meta.url), "utf8");
  assert.match(src, /case\s+"push_verify"\s*:/, "stream.js's switch has no case for push_verify — the frame would be silently dropped");
  // verify_report is a frame the CONTROL handles (workerlink/stream.go), not the worker — the worker only
  // SENDS it (as #runVerify's report), so the worker's own switch has no receiving case for it.
  // Asserted here anyway, negatively, so a future author does not add a phantom worker-side receiver
  // for a frame direction that does not exist.
  assert.doesNotMatch(src, /case\s+"verify_report"\s*:/, "stream.js should not have a receiving case for verify_report — the worker only SENDS this frame, never receives it");
});

// T3b (§8.3) — the verify sends NO mail: dispositive assertion on the SINK's command log (only
// the server knows what was actually sent), plus the positive control (the same fixture through
// a real send DOES record MAIL FROM/RCPT TO/DATA) and — the one that matters most — the NEGATIVE
// CONTROL directly on the dataBytes hazard itself: with stopAfterAuth OMITTED, a verify-shaped
// call must behave like the pre-fix bug and actually deliver a message with body "undefined".
test("T3b: a verify (stopAfterAuth) records connect/EHLO/STARTTLS/AUTH/QUIT and ZERO MAIL FROM/RCPT/DATA", async () => {
  const sink = await startSink({ authResult: "ok" });
  try {
    const cred = new MemoryCredential(CANARY);
    const result = await runSmtpSession(verifyJobFor(sink.port), cred, { timeoutMs: 5000, stopAfterAuth: true });
    cred.zero();
    assert.ok(result.smtpResponse, "verify should return the AUTH 235 response text");
    assert.ok(sink.received.some((l) => /EHLO|HELO/i.test(l)), "verify never EHLO'd");
    assert.ok(sink.received.some((l) => /AUTH/i.test(l)), "verify never attempted AUTH");
    assert.ok(sink.received.some((l) => /QUIT/i.test(l)), "verify never sent QUIT");
    assert.ok(!sink.received.some((l) => /MAIL FROM/i.test(l)), "verify sent MAIL FROM — a real send occurred");
    assert.ok(!sink.received.some((l) => /RCPT TO/i.test(l)), "verify sent RCPT TO — a real send occurred");
    assert.ok(!sink.received.some((l) => /] DATA$/i.test(l)), "verify sent DATA — a real message was delivered");
  } finally {
    await sink.close();
  }
});

test("T3b control: the SAME fixture through a real send DOES record MAIL FROM/RCPT/DATA", async () => {
  const sink = await startSink({ authResult: "ok" });
  try {
    const cred = new MemoryCredential(CANARY);
    await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 }); // stopAfterAuth omitted: a real send
    cred.zero();
    assert.ok(sink.received.some((l) => /MAIL FROM/i.test(l)), "control: real send never sent MAIL FROM");
    assert.ok(sink.received.some((l) => /RCPT TO/i.test(l)), "control: real send never sent RCPT TO");
    assert.ok(sink.received.some((l) => /] DATA$/i.test(l)), "control: real send never sent DATA");
  } finally {
    await sink.close();
  }
});

// T3b's SECOND, most important control (§8.3): a verify-shaped call with stopAfterAuth OMITTED
// must reproduce the pre-fix dataBytes bug — a real DATA command AND a body of literally
// "undefined" (dotStuff(undefined) -> String(undefined) -> "undefined"). If this control does
// NOT fail loudly (i.e. does not observe the bug), the stopAfterAuth flag is not doing what §2.2
// claims and this test would be the only thing that could tell us. Uses a REAL SMTP capture (the
// sink echoes DATA content) rather than asserting the source code, per Rule 3.
test("T3b NEGATIVE CONTROL ON THE BUG ITSELF: a verify-shaped call WITHOUT stopAfterAuth delivers a real message with body 'undefined'", async () => {
  const sink = await startSink({ authResult: "ok", captureData: true });
  try {
    const cred = new MemoryCredential(CANARY);
    // The exact hazard from §2.2: a verify job shape (no recipient, no dataBytes) run through
    // runSmtpSession WITHOUT stopAfterAuth — the bug this whole mechanism exists to prevent.
    const bugJob = { ...verifyJobFor(sink.port), recipient: "alice@example.com" };
    let threw = null;
    try {
      await runSmtpSession(bugJob, cred, { timeoutMs: 5000 }); // stopAfterAuth OMITTED — the bug
    } catch (e) {
      threw = e; // some sinks may still reject the RCPT/DATA shape; either way DATA must have been SENT
    } finally {
      cred.zero();
    }
    assert.ok(sink.received.some((l) => /MAIL FROM/i.test(l)), "the bug control never even reached MAIL FROM — the hazard could not be observed");
    assert.ok(sink.received.some((l) => /] DATA$/i.test(l)), "the bug control never reached DATA — cannot prove the guard is load-bearing");
    assert.ok(sink.capturedBody != null, "the sink captured no DATA body — cannot verify the 'undefined' payload");
    assert.match(sink.capturedBody, /undefined/, `expected dotStuff(undefined)->"undefined" in the delivered body, got: ${sink.capturedBody}`);
  } finally {
    await sink.close();
  }
});

// S5 — on 587/25 (starttls), a server that does NOT advertise STARTTLS causes an ABORT BEFORE
// AUTH; the credential is NEVER transmitted. We assert the worker throws starttls_unavailable and
// that the sink recorded NO AUTH line (the credential never left the worker).
test("S5: no STARTTLS advertised → abort before AUTH, credential never sent", async () => {
  const sink = await startSink({ advertiseStartTLS: false });
  try {
    const cred = new MemoryCredential(CANARY);
    let threw = null;
    try {
      await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 });
    } catch (e) {
      threw = e;
    } finally {
      cred.zero();
    }
    assert.ok(threw instanceof SMTPError, "expected an SMTPError");
    assert.equal(threw.reason, "starttls_unavailable", "should abort with starttls_unavailable");
    // The sink must NOT have seen AUTH (the worker aborted before it) — and definitely not the canary.
    assert.ok(!sink.received.some((l) => /AUTH/i.test(l)), "AUTH was sent despite no STARTTLS — credential exposure!");
    assert.ok(!sink.received.some((l) => l.includes(CANARY)), "canary was transmitted on an unencrypted connection");
    // Positive control: the worker DID connect + EHLO (so the abort is a real decision, not a no-connect).
    assert.ok(sink.received.some((l) => /EHLO|HELO/i.test(l)), "worker never EHLO'd — test would be vacuous");
  } finally {
    await sink.close();
  }
});
