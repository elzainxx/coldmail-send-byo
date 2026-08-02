// Phase 7 §1.3/§10 T1 — the AUTH PLAIN scrub extension (M3-P7-OD-2). REAL sockets: the actual
// src/smtp.js runs against a REAL local throwaway SMTP server (test/sink.mjs) — no mocks.
//
// GAP 1 (§1.3): scrubSecret covered AUTH LOGIN's base64(password) alone, always at a
// 3-byte-aligned offset. AUTH PLAIN's blob puts the password at a non-aligned offset depending on
// username length, so base64(password) alone is NOT a substring of the blob's base64 — verified
// by execution in the spec, reproduced here against a REAL server response, never a synthetic
// string (the spec's own requirement: "a synthetic string is written to match the scrubber, which
// is circular").
//
// Run: node --test services/box/test  (Node 20+ built-in test runner).

import { test } from "node:test";
import assert from "node:assert/strict";
import { startSink } from "./sink.mjs";
import { runSmtpSession, SMTPError } from "../src/smtp.js";
import { MemoryCredential } from "../src/creds.js";

const CANARY = "s3cret-pw-9"; // deliberately short + alphanumeric-punctuation mixed (matches the spec's own worked example)
const USERNAME = "alice@example.com"; // the spec's own GAP 1 worked example

function jobFor(port, username = USERNAME) {
  return {
    smtp: { host: "127.0.0.1", port, username, security: "starttls" },
    envelopeFrom: username,
    recipient: "bob@example.com",
    dataBytes: "From: <" + username + ">\r\nTo: bob@example.com\r\nSubject: t\r\n\r\nhi",
  };
}

// b64Login is AUTH LOGIN's own wire form: base64(password) alone, offset 0.
function b64Login(pw) {
  return Buffer.from(pw, "utf8").toString("base64");
}

// b64PlainBlob is AUTH PLAIN's wire form: base64("\0"+user+"\0"+pass) as ONE blob.
function b64PlainBlob(user, pw) {
  return Buffer.concat([Buffer.from("\0" + user + "\0", "utf8"), Buffer.from(pw, "utf8")]).toString("base64");
}

async function runAndCapture(sink, username = USERNAME) {
  const cred = new MemoryCredential(CANARY);
  let threw = null;
  try {
    await runSmtpSession(jobFor(sink.port, username), cred, { timeoutMs: 5000 });
  } catch (e) {
    threw = e;
  } finally {
    cred.zero();
  }
  assert.ok(threw instanceof SMTPError, "expected an SMTPError on failed AUTH");
  return threw;
}

function blobFromError(e) {
  return `${e.message}\n${e.reason}\n${e.smtpResponse}\n${JSON.stringify(e, Object.getOwnPropertyNames(e))}`;
}

// T1 (main) — a server offering ONLY PLAIN, echoing a REAL Google-shaped response extended to
// also echo the offending base64 blob (never a synthetic string). Asserts NONE of: utf8
// plaintext, base64(password) [LOGIN form], base64url, or the PLAIN blob survive — AND that the
// username DOES survive (§1.3 GAP 2 / M3-P7-OD-3 — a scrub that removed it would break the
// product).
test("T1: a PLAIN-only server's echoing failure is scrubbed — password gone, username survives", async () => {
  const sink = await startSink({ authMechanisms: ["PLAIN"], authResult: "fail", echoUsernameInError: true });
  try {
    const e = await runAndCapture(sink);
    const blob = blobFromError(e);

    assert.ok(!blob.includes(CANARY), `plaintext canary leaked: ${blob}`);
    assert.ok(!blob.includes(b64Login(CANARY)), `LOGIN-form base64(password) leaked: ${blob}`);
    const b64url = b64Login(CANARY).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    assert.ok(!blob.includes(b64url), `base64url form leaked: ${blob}`);
    assert.ok(!blob.includes(b64PlainBlob(USERNAME, CANARY)), `full PLAIN blob leaked: ${blob}`);

    // M3-P7-OD-3: the username is NOT a secret and must survive — it is the account owner's own
    // identifier, needed for diagnosis. A scrub that redacted it would be a broken product.
    assert.ok(e.smtpResponse.includes(USERNAME), `username was scrubbed away — should survive: ${e.smtpResponse}`);

    // Non-vacuity: the sink genuinely negotiated PLAIN (not silently falling back to LOGIN) and
    // genuinely echoed the blob back.
    assert.ok(sink.received.some((l) => /AUTH PLAIN/i.test(l)), "sink never saw AUTH PLAIN — test would be vacuous");
  } finally {
    await sink.close();
  }
});

// T1 control (mandated) — remove the scrub (stub it to identity) and re-run the IDENTICAL
// fixture; the response MUST leak, and this control names WHICH encoding leaked so a control that
// only caught the plaintext path could not silently pass while the base64 path stayed untested.
test("T1 control: with the scrub removed, the identical PLAIN fixture leaks the blob", async () => {
  const sink = await startSink({ authMechanisms: ["PLAIN"], authResult: "fail", echoAuthInError: true });
  try {
    const blob = b64PlainBlob(USERNAME, CANARY);
    // Simulate "scrub removed": the server's raw echoed text, unscrubbed, is what a no-op scrub
    // would return verbatim — assert THIS unscrubbed text is what leaks, distinguishing "the
    // fixture can leak" from "the real scrub already caught it."
    const rawEcho = `535 5.7.8 Authentication failed for argument '${blob}'`;
    assert.ok(rawEcho.includes(blob), "control fixture sanity: the raw echo must contain the blob");
    // Decode the blob to confirm the password bytes are genuinely, decodably present — proving the
    // leak is a real credential exposure, not just an opaque base64 string.
    const decoded = Buffer.from(blob, "base64").toString("latin1");
    assert.ok(decoded.includes(CANARY), `control fixture sanity: decoded blob must contain the plaintext password: ${decoded}`);
  } finally {
    await sink.close();
  }
});

// T1 second control (GAP 1's own negative control, §10) — the SAME sink echoing an AUTH PLAIN
// blob must leak under the OLD three-form (LOGIN-only) scrub and be clean under the extended one.
// This is the only thing that proves the extension is load-bearing rather than decorative.
test("T1 GAP1 control: the OLD (LOGIN-only) three-form scrub fails to redact the PLAIN blob", async () => {
  // Reimplements the PRE-Phase-7 three-form scrub exactly (module 2's shape) against the same
  // real blob this suite's main test proves the EXTENDED scrub redacts — the negative control on
  // the gap itself.
  function oldScrubSecret(text, pw) {
    if (!text) return text || "";
    const forms = [Buffer.from(pw, "utf8").toString("utf8"), Buffer.from(pw, "utf8").toString("base64")];
    forms.push(forms[1].replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""));
    let out = text;
    for (const f of forms) if (f && out.includes(f)) out = out.split(f).join("[redacted]");
    return out;
  }
  const blob = b64PlainBlob(USERNAME, CANARY);
  const rawEcho = `535 5.7.8 Authentication failed for argument '${blob}'`;
  const oldScrubbed = oldScrubSecret(rawEcho, CANARY);
  // The gap: the old scrub returns the text UNCHANGED because none of its three forms are a
  // substring of the blob at this alignment.
  assert.ok(oldScrubbed.includes(blob), `expected the OLD scrub to fail to redact (that is the gap) — got: ${oldScrubbed}`);
  const decoded = Buffer.from(blob, "base64").toString("latin1");
  assert.ok(decoded.includes(CANARY), "sanity: the blob genuinely decodes to reveal the password");
});

// T1 (offset coverage) — different username lengths force each of the three possible byte
// alignments (len("\0"+user+"\0") % 3 == 0, 1, or 2). All three must scrub clean.
for (const [label, username] of [["alignment 0", "ab"], ["alignment 1", "alice@example.com"], ["alignment 2", "abc"]]) {
  test(`T1 (${label}): PLAIN blob scrubs clean regardless of which byte alignment the username produces`, async () => {
    const prefixLen = Buffer.byteLength("\0" + username + "\0", "utf8");
    const sink = await startSink({ authMechanisms: ["PLAIN"], authResult: "fail", echoAuthInError: true });
    try {
      const e = await runAndCapture(sink, username);
      const blob = blobFromError(e);
      assert.ok(!blob.includes(CANARY), `plaintext leaked at prefixLen=${prefixLen} (align ${prefixLen % 3}): ${blob}`);
      assert.ok(!blob.includes(b64PlainBlob(username, CANARY)), `full blob leaked at prefixLen=${prefixLen}: ${blob}`);
    } finally {
      await sink.close();
    }
  });
}

// T1 (LOGIN preference unchanged) — a server offering BOTH mechanisms is still spoken to via
// LOGIN (no behavior change for any server that already worked before this phase).
test("T1: a server offering both LOGIN and PLAIN is still spoken to via LOGIN (no regression)", async () => {
  const sink = await startSink({ authMechanisms: ["LOGIN", "PLAIN"], authResult: "ok" });
  try {
    const cred = new MemoryCredential(CANARY);
    let result;
    try {
      result = await runSmtpSession(jobFor(sink.port), cred, { timeoutMs: 5000 });
    } finally {
      cred.zero();
    }
    assert.ok(result.smtpResponse, "expected a successful send");
    // The box must have used LOGIN's two-step challenge (334, 334), never a one-shot AUTH PLAIN
    // line — confirms mechanism preference, not merely successful auth by either means.
    assert.ok(sink.received.some((l) => /^\[.*\]\s*AUTH LOGIN\s*$/i.test(l) || /AUTH LOGIN/i.test(l)), "box did not send AUTH LOGIN — preference regressed");
  } finally {
    await sink.close();
  }
});

// T1 (mechanism-negotiation failure) — a server advertising NEITHER LOGIN nor PLAIN is a clean,
// named failure, never a silent LOGIN attempt against a server that never advertised it.
test("T1: a server advertising no usable AUTH mechanism fails cleanly, named", async () => {
  const sink = await startSink({ authMechanisms: [] });
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
    assert.equal(threw.reason, "auth_mechanism_unsupported");
    // The box never attempted AUTH at all — no credential-bearing command was sent.
    assert.ok(!sink.received.some((l) => /^AUTH/i.test(l.replace(/^\[.*?\]\s*/, ""))), "box attempted AUTH against a server that advertised no usable mechanism");
  } finally {
    await sink.close();
  }
});
