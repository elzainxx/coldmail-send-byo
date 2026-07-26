// variation.test.mjs — Phase 8 Chunk 2: the headline safety property (§4, P8-4) and the
// resolver/pipeline scaffolding it rests on. Real code paths, no mocks: this drives the ACTUAL
// assemble.js (toMessage/serializeMessage/assembleDataBytes) and variation.js
// (resolveVariation/isNeutral/applyVariation) against the ACTUAL frozen Phase 3 reference
// (stream.js's exported buildDataBytes) — the same function #runJob calls in production.
//
// Run: node --test services/box/test/*.test.mjs  (Node 20+).

import { test } from "node:test";
import assert from "node:assert/strict";
import { buildDataBytes } from "../src/stream.js";
import { assembleDataBytes } from "../src/assemble.js";
import { resolveVariation, isNeutral, VARIATION_DEFAULTS, prngFor, pickSeeded } from "../src/variation.js";

// A representative Phase 3 send — the three legitimately-random inputs (Message-ID, Date, and any
// future MIME boundary) are PINNED to fixed literal values here, exactly as §4.2/§8 T1 requires,
// so the test isolates "does the variation layer alter anything when off" from "Phase 3 itself is
// non-deterministic". There is no boundary field yet (Phase 3 is single-part — no MIME boundary
// exists today), so pinning Message-ID + Date is the whole requirement until Chunk 4 introduces one.
function pinnedSend() {
  return {
    recipient: "alice@example.com",
    from_address: "sender@ourdomain.com",
    subject: "Pinned Subject",
    body: "<p>Hello <b>world</b></p>",
    headers: {
      From: '"P3 Suite" <sender@ourdomain.com>',
      To: "alice@example.com",
      Subject: "Pinned Subject",
      "Reply-To": "reply@ourdomain.com",
      "Message-ID": "<pinned-job-id@ourdomain.com>",
      Date: "Mon, 21 Jul 2026 14:05:00 +0000",
    },
  };
}

const JOB_ID = "pinned-job-id";

test("T1: variation ABSENT ⇒ assembleDataBytes is byte-for-byte the Phase 3 reference", () => {
  const send = pinnedSend();
  const phase3 = buildDataBytes(send);
  const phase8 = assembleDataBytes(send, undefined, JOB_ID);
  assert.equal(phase8, phase3, "an absent variation block must produce byte-identical output to Phase 3");
});

test("T1: variation ALL-DEFAULT ⇒ assembleDataBytes is byte-for-byte the Phase 3 reference", () => {
  const send = pinnedSend();
  const phase3 = buildDataBytes(send);
  const phase8 = assembleDataBytes(send, { ...VARIATION_DEFAULTS }, JOB_ID);
  assert.equal(phase8, phase3, "an all-default variation block must produce byte-identical output to Phase 3");
});

// Anti-vacuity for the two tests above: prove the reference itself is non-trivial (contains the
// pinned headers, the body, the MIME framing) — otherwise an empty-string equality would pass for
// the wrong reason.
test("T1 anti-vacuity: the Phase 3 reference is genuinely non-trivial", () => {
  const phase3 = buildDataBytes(pinnedSend());
  assert.ok(phase3.includes("Message-ID: <pinned-job-id@ourdomain.com>"), "reference missing pinned Message-ID");
  assert.ok(phase3.includes("Date: Mon, 21 Jul 2026 14:05:00 +0000"), "reference missing pinned Date");
  assert.ok(phase3.includes("Hello <b>world</b>"), "reference missing body");
  assert.ok(phase3.includes("MIME-Version: 1.0"), "reference missing MIME framing");
  assert.ok(phase3.length > 100, "reference suspiciously short — equality tests would be vacuous");
});

test("resolveVariation: absent/malformed block degrades to VARIATION_DEFAULTS, never throws", () => {
  assert.deepEqual(resolveVariation(undefined), VARIATION_DEFAULTS);
  assert.deepEqual(resolveVariation(null), VARIATION_DEFAULTS);
  assert.deepEqual(resolveVariation({}), VARIATION_DEFAULTS);
  // Unrecognized enum values fall back to the neutral default rather than being accepted verbatim.
  const resolved = resolveVariation({ header_style: "not-a-real-style", precedence: "bogus", comment_intensity: "extreme" });
  assert.equal(resolved.header_style, "none");
  assert.equal(resolved.precedence, "auto");
  assert.equal(resolved.comment_intensity, "low");
});

test("resolveVariation: a legitimate non-default value passes through unchanged", () => {
  const resolved = resolveVariation({ html_minify: true, header_style: "gmail", comment_intensity: "high" });
  assert.equal(resolved.html_minify, true);
  assert.equal(resolved.header_style, "gmail");
  assert.equal(resolved.comment_intensity, "high");
  // Untouched fields still resolve to their own defaults.
  assert.equal(resolved.css_micro, false);
  assert.equal(resolved.precedence, "auto");
});

test("isNeutral: true for defaults/absent, and toggle PARAMETERS alone never flip it", () => {
  assert.equal(isNeutral(resolveVariation(undefined)), true);
  assert.equal(isNeutral(resolveVariation({})), true);
  // Anti-vacuity for the "parameters are inert while off" claim: changing comment_intensity /
  // preheader_templates with their parent toggle OFF must not make the set non-neutral.
  assert.equal(isNeutral(resolveVariation({ comment_intensity: "high" })), true);
  assert.equal(isNeutral(resolveVariation({ preheader_templates: ["custom"] })), true);
});

test("isNeutral: false once STAGES contains an active entry (forward-looking, no live stages yet)", () => {
  // Chunk 2 ships STAGES empty (later chunks register real transforms) — assert that honestly
  // rather than asserting isNeutral(true-toggle)===false, which would be vacuous until a stage
  // exists to consult. This documents the current state so a later chunk's registration is the
  // thing that flips this test's premise, not a silent behavior change.
  const anyToggleOn = resolveVariation({ html_minify: true });
  assert.equal(isNeutral(anyToggleOn), true, "no stages registered yet — isNeutral is unconditionally true until Chunk 3 adds the first one");
});

test("determinism: prngFor/pickSeeded are stable for the same (jobId, stream) and diverge across streams/jobs", () => {
  const a1 = prngFor("job-A", "css")();
  const a2 = prngFor("job-A", "css")();
  assert.equal(a1, a2, "same (jobId, stream) must draw the same first value");
  const b = prngFor("job-B", "css")();
  assert.notEqual(a1, b, "different jobId must diverge (extremely unlikely collision otherwise)");
  const aOther = prngFor("job-A", "comments")();
  assert.notEqual(a1, aOther, "different stream namespace for the SAME job must diverge — toggles must not share a draw");

  const list = ["x", "y", "z", "w"];
  const p1 = pickSeeded(list, "job-A", "preheader");
  const p2 = pickSeeded(list, "job-A", "preheader");
  assert.equal(p1, p2, "pickSeeded must be stable for the same (jobId, stream)");
  assert.equal(pickSeeded([], "job-A", "preheader"), undefined, "empty candidate list yields undefined, not a throw");
});
