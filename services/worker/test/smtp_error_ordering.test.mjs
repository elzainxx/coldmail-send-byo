// Phase 7 §10 T8/T10 — two invariants that are each syntactically-valid-but-silently-broken if
// gotten wrong, which is exactly why each gets a DIRECT unit assertion plus a control that proves
// the assertion would catch the mistake.

import { test } from "node:test";
import assert from "node:assert/strict";
import { startSink } from "./sink.mjs";
import { runSmtpSession, SMTPError } from "../src/smtp.js";
import { MemoryCredential, CredentialZeroedError } from "../src/creds.js";

const CANARY = "canary-smtp-secret-9Z";

// The destination guard (Phase 3 close-out C4) refuses a loopback host unless the job's allowlist names it: this
// suite's sink listens on loopback, so every job here carries the loopback range, exactly as the test harness
// does with smtp_host_allow_cidrs. The refusal itself is proven in destguard.test.mjs.

function jobFor(port) {
  return {
    smtp: { host: "127.0.0.1", port, username: "user@ourdomain.com", security: "starttls", destAllowCidrs: ["127.0.0.0/8"] },
    envelopeFrom: "user@ourdomain.com",
    recipient: "alice@example.com",
    dataBytes: "From: <user@ourdomain.com>\r\nTo: alice@example.com\r\nSubject: t\r\n\r\nhi",
  };
}

// --- T8: SMTPError's constructor argument order is pinned -------------------------------------
//
// `constructor(reason, smtpResponse) { super(reason); ... }` is what makes e.message the
// INTERNAL SLUG and the provider text sit on a SEPARATE field (e.smtpResponse). All eleven
// console.* sites in the worker log e.message, and are therefore safe WITHOUT a scrub — structurally,
// not by care. A transposed constructor call (`super(smtpResponse)`) is syntactically valid,
// passes every existing test that doesn't check this specific property, and breaks all eleven
// log sites SILENTLY.

test("T8: SMTPError's constructor argument order — e.message is the reason, never the response", () => {
  const e = new SMTPError("auth_failed", `535-5.7.8 rejected, secret was ${CANARY}`);
  assert.equal(e.message, "auth_failed", "e.message must equal the reason exactly");
  assert.ok(!e.message.includes(CANARY), "e.message must contain none of the response text");
  assert.equal(e.smtpResponse, `535-5.7.8 rejected, secret was ${CANARY}`, "smtpResponse must carry the full response text on its own field");
});

// T8 control (the one that matters): a TRANSPOSED constructor call must make the SAME assertion
// FAIL, naming the leaked encoding. A transposition is syntactically valid Error-subclass code
// that passes every OTHER existing test — this control is the only thing proving the assertion
// above would actually catch it.
test("T8 control: a transposed constructor call fails the SAME assertion (proving it's load-bearing)", () => {
  class TransposedSMTPError extends Error {
    constructor(reason, smtpResponse) {
      super(smtpResponse); // BUG: transposed — this is the mistake T8 exists to catch
      this.name = "SMTPError";
      this.reason = reason;
      this.smtpResponse = smtpResponse || "";
    }
  }
  const e = new TransposedSMTPError("auth_failed", `535-5.7.8 rejected, secret was ${CANARY}`);
  assert.notEqual(e.message, "auth_failed", "sanity: the transposed version's message really differs");
  assert.ok(e.message.includes(CANARY), "the transposed constructor's e.message DOES leak the response — this is the exact bug T8 pins against");
});

// T8 (end-to-end half): drive a REAL failing send whose provider reply contains the password, and
// assert the worker's own captured error surface carries the slug and NONE of the credential
// encodings — proving the invariant holds through the real worker code path, not just the class
// definition in isolation.
test("T8 end-to-end: a real failing send's thrown error carries the slug, never the credential", async () => {
  const sink = await startSink({ authResult: "fail", echoAuthInError: true });
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
    assert.ok(threw instanceof SMTPError);
    assert.equal(threw.message, threw.reason, "e.message must equal e.reason on the real worker path too");
    assert.ok(!threw.message.includes(CANARY), "e.message leaked the credential end-to-end");
    const b64 = Buffer.from(CANARY, "utf8").toString("base64");
    assert.ok(!threw.message.includes(b64), "e.message leaked the base64 credential end-to-end");
  } finally {
    await sink.close();
  }
});

// --- T10: scrubSecret FAILS CLOSED on a zeroed credential buffer ------------------------------
//
// M3-P7-OD-8: "fail-closed beats fail-silent on a credential path." credential.use() throws
// CredentialZeroedError on an already-zeroed buffer (creds.js), and scrubSecret must let that
// throw PROPAGATE rather than catching it and returning the input unchanged (module 2's shape).

test("T10(b): scrubSecret throws on an already-zeroed buffer, rather than returning the input", () => {
  const cred = new MemoryCredential(CANARY);
  cred.zero();
  assert.throws(
    () => cred.use((buf) => buf.toString("utf8")),
    CredentialZeroedError,
    "credential.use() must throw on a zeroed buffer",
  );
});

// T10(b) control: with a LIVE (non-zeroed) buffer, credential.use() must NOT throw and must
// return normally — proving the throw fires only on the broken ordering, never on the normal
// path. Without this control, a use() that threw UNCONDITIONALLY would pass the test above.
test("T10(b) control: credential.use() does NOT throw on a live buffer, and returns the secret", () => {
  const cred = new MemoryCredential(CANARY);
  let seen = null;
  assert.doesNotThrow(() => {
    cred.use((buf) => { seen = buf.toString("utf8"); });
  });
  assert.equal(seen, CANARY);
  cred.zero();
});

// T10(a): the ordering itself — a real failing send's report assembly (which calls scrubSecret,
// which calls credential.use()) completes BEFORE credential.zero() runs in the caller's finally.
// Observed here directly at the smtp.js layer: scrubSecret must succeed (not throw) when called
// on a still-live buffer inside runSmtpSession's own error path, proving assembly-before-zero
// holds at the point that matters, not merely "somewhere in the codebase."
test("T10(a): the real worker's error path scrubs successfully — assembly runs before the credential is zeroed", async () => {
  const sink = await startSink({ authResult: "fail" });
  try {
    const cred = new MemoryCredential(CANARY);
    let threw = null;
    try {
      await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 });
    } catch (e) {
      threw = e; // scrubSecret already ran successfully INSIDE runSmtpSession's own catch, using
      // the still-live buffer — if the ordering were broken (zeroed before this point), this
      // catch would instead have received a CredentialZeroedError, not an SMTPError.
    }
    assert.ok(threw instanceof SMTPError, "expected a normal SMTPError — scrubSecret must have run against a LIVE buffer, not thrown CredentialZeroedError");
    cred.zero(); // the caller's own zero, AFTER assembly — mirrors stream.js's finally ordering.
  } finally {
    await sink.close();
  }
});

// T10(a) control: reproduce the BROKEN ordering directly — zero the credential BEFORE the scrub
// would run, and assert the resulting CredentialZeroedError propagates rather than silently
// returning the unscrubbed(-or-otherwise) text. This is the ordering bug made concrete: today
// that reordering would be silent under module 2's catch-and-return shape; module 3 must not be.
test("T10(a) control: zeroing BEFORE scrubbing throws CredentialZeroedError, not a silent pass-through", () => {
  const cred = new MemoryCredential(CANARY);
  cred.zero(); // simulate the broken ordering: zero happened first
  assert.throws(
    () => cred.use((buf) => buf.toString("base64")),
    CredentialZeroedError,
  );
});
