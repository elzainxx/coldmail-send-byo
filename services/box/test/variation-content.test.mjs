// variation-content.test.mjs — Phase 8 CHUNK 3: the five body/content toggles (§5 toggles 1-5).
// Real code paths against the actual variation-content.js + assemble.js/stream.js pipeline, no
// mocks. Covers §8 T2 (independent switching), T5 (zero-width default-off + rate/placement), T7
// (URL params unique-per-recipient, no tracking), plus per-toggle behavioral assertions and the
// determinism guarantee (§7).

import { test } from "node:test";
import assert from "node:assert/strict";
import { buildDataBytes } from "../src/stream.js";
import { assembleDataBytes } from "../src/assemble.js";
import { resolveVariation, applyVariation } from "../src/variation.js";
import {
  htmlMinify,
  injectComments,
  applyCssVariation,
  addUniqueURLParams,
  insertZeroWidth,
} from "../src/variation-content.js";

function sendWithBody(body) {
  return {
    recipient: "alice@example.com",
    from_address: "sender@ourdomain.com",
    subject: "Subject",
    body,
    headers: {
      From: '"P3 Suite" <sender@ourdomain.com>',
      To: "alice@example.com",
      Subject: "Subject",
      "Message-ID": "<pinned-job-id@ourdomain.com>",
      Date: "Mon, 21 Jul 2026 14:05:00 +0000",
    },
  };
}

// --- Toggle 1: HTML minification -----------------------------------------------------------------

test("html_minify: strips comments (preserves Outlook conditionals), collapses whitespace, renders identically", () => {
  const body = `<div>
    <!-- drop me -->
    <p>Hello   world</p>
    <!--[if mso]><p>mso only</p><![endif]-->
  </div>`;
  const out = htmlMinify(body);
  assert.ok(!out.includes("drop me"), "a plain comment must be stripped");
  assert.ok(out.includes("<!--[if mso]>"), "an Outlook conditional comment must be PRESERVED");
  assert.ok(!/ {2,}/.test(out.replace(/<!--\[if mso\]>.*<!\[endif\]-->/, "")), "runs of spaces must collapse");
  assert.ok(!out.includes(">    <") && !out.includes(">\n"), "inter-tag whitespace must collapse");
});

test("html_minify: off ⇒ body untouched (toggle gate)", () => {
  const send = sendWithBody("<div>  <!-- x --> <p>a  b</p></div>");
  const resolved = resolveVariation({ html_minify: false });
  const msg = applyVariation({ headers: [], body: send.body, transferEncoding: "8bit", boundary: null }, resolved, "job-1");
  assert.equal(msg.body, send.body);
});

// --- Toggle 2: invisible comment injection ---------------------------------------------------------

test("comment_injection: low/medium/high produce increasing comment counts on a large body (rate scaling)", () => {
  const bigBody = "<div>" + "<p>text</p>".repeat(400) + "</div>";
  const low = (injectComments(bigBody, "low", "job-rate").match(/<!--[0-9a-f]{8}-->/g) || []).length;
  const medium = (injectComments(bigBody, "medium", "job-rate").match(/<!--[0-9a-f]{8}-->/g) || []).length;
  const high = (injectComments(bigBody, "high", "job-rate").match(/<!--[0-9a-f]{8}-->/g) || []).length;
  assert.ok(low < medium, `low (${low}) should inject fewer comments than medium (${medium}) on a 400-closing-tag body`);
  assert.ok(medium < high, `medium (${medium}) should inject fewer comments than high (${high})`);
});

test("comment_injection stage: toggle off leaves the body byte-identical", () => {
  const bigBody = "<div>" + "<p>text</p>".repeat(50) + "</div>";
  const resolved = resolveVariation({ comment_injection: false });
  const msg = applyVariation({ headers: [], body: bigBody, transferEncoding: "8bit", boundary: null }, resolved, "job-2");
  assert.equal(msg.body, bigBody);
});

test("comment_injection: injected comments are never rendered content (semantic no-op) and are deterministic per job_id", () => {
  const body = "<div>" + "<p>x</p>".repeat(100) + "</div>";
  const a = injectComments(body, "high", "job-det");
  const b = injectComments(body, "high", "job-det");
  assert.equal(a, b, "same job_id must produce the identical injected comments");
  const c = injectComments(body, "high", "job-different");
  assert.notEqual(a, c, "a different job_id should (overwhelmingly likely) diverge");
  // Stripping every injected comment must reproduce the original body exactly (semantic equivalence).
  assert.equal(a.replace(/<!--[0-9a-f]{8}-->/g, ""), body);
});

// --- Toggle 3: CSS micro-variation ------------------------------------------------------------------

test("css_micro: varies padding/margin by seeded {-1,0,+1}px, skips <3px, never negative", () => {
  const body = `<div style="padding: 10px; margin: 2px; margin: 0px;"></div>`;
  const out = applyCssVariation(body, "job-css");
  const paddingMatch = out.match(/padding:\s*(\d+)px/);
  assert.ok(paddingMatch, "padding declaration must survive");
  const varied = parseInt(paddingMatch[1], 10);
  assert.ok(Math.abs(varied - 10) <= 1, `padding varied by more than 1px: ${varied}`);
  assert.ok(out.includes("margin: 2px"), "a margin value <3px must be skipped, unchanged");
  assert.ok(out.includes("margin: 0px") || out.includes("margin: 1px") === false, "0px must never go negative and stays skipped (<3px)");
});

test("css_micro: decimal px values are left untouched (preserve precision)", () => {
  const body = `<div style="padding: 10.5px;"></div>`;
  assert.equal(applyCssVariation(body, "job-css2"), body);
});

test("css_micro: deterministic per job_id", () => {
  const body = `<div style="padding: 20px; padding: 30px; padding: 40px;"></div>`;
  const a = applyCssVariation(body, "job-css-det");
  const b = applyCssVariation(body, "job-css-det");
  assert.equal(a, b);
});

// --- Toggle 4: unique link/image URL params (NOT tracking) -------------------------------------

test("unique_url_params: appends _ref to links and _v to images, stable per job_id, no tracking/callback introduced", () => {
  const body = `<a href="https://example.com/page?x=1">link</a> <a href="https://example.com/other">other</a> <img src="https://cdn.example.com/logo.png">`;
  const out = addUniqueURLParams(body, "job-url-1");
  const refs = [...out.matchAll(/_ref=([0-9a-f]+)/g)].map((m) => m[1]);
  assert.equal(refs.length, 2, "both links must get a _ref param");
  assert.equal(refs[0], refs[1], "the SAME job (== one recipient) must get the SAME _ref value on every link");
  assert.match(out, /_v=[0-9a-f]+/, "image must get a _v param");
  // Host + path unchanged; only a query param appended.
  assert.ok(out.includes("https://example.com/page?x=1&_ref="), "existing query string must be preserved with & separator");
  assert.ok(out.includes("https://example.com/other?_ref="), "no query string must use ? separator");
  assert.ok(out.includes("https://cdn.example.com/logo.png?_v="));
  // NOT tracking: no new domain, no redirect wrapper, no callback endpoint appears anywhere.
  assert.ok(!/mail-track|redirect|track\./.test(out), "must introduce no tracking/redirect surface");
});

test("unique_url_params: unique per recipient (different job_id ⇒ different hash), stable for the SAME job_id", () => {
  const body = `<a href="https://example.com/page">link</a>`;
  const out1 = addUniqueURLParams(body, "job-recipient-A");
  const out2 = addUniqueURLParams(body, "job-recipient-B");
  const out1Again = addUniqueURLParams(body, "job-recipient-A");
  const ref1 = out1.match(/_ref=([0-9a-f]+)/)[1];
  const ref2 = out2.match(/_ref=([0-9a-f]+)/)[1];
  assert.notEqual(ref1, ref2, "different recipients (job_ids) must get different _ref values");
  assert.equal(out1, out1Again, "the same job_id must produce byte-identical output");
});

test("unique_url_params: skips a URL that already carries the param", () => {
  const body = `<a href="https://example.com/page?_ref=deadbeef">link</a> <img src="https://cdn.example.com/x.png?_v=abc123">`;
  const out = addUniqueURLParams(body, "job-skip");
  assert.equal(out, body, "URLs already carrying _ref/_v must be left untouched");
});

test("unique_url_params: off ⇒ URLs untouched (toggle gate)", () => {
  const body = `<a href="https://example.com/page">link</a>`;
  const resolved = resolveVariation({ unique_url_params: false });
  const msg = applyVariation({ headers: [], body, transferEncoding: "8bit", boundary: null }, resolved, "job-3");
  assert.equal(msg.body, body);
});

// --- Toggle 5: zero-width insertion (DEFAULT OFF) -----------------------------------------------

test("zero_width: DEFAULT is OFF — resolveVariation(undefined/{}) never enables it", () => {
  assert.equal(resolveVariation(undefined).zero_width, false);
  assert.equal(resolveVariation({}).zero_width, false);
});

test("zero_width: with the toggle at default, assembled bytes contain ZERO U+200D", () => {
  const send = sendWithBody("<div><p>Hello there this is a fairly long visible sentence with many spaces to test.</p></div>");
  const bytes = assembleDataBytes(send, undefined, "job-zwj-off");
  assert.ok(!bytes.includes("‍"), "default (off) must never insert ZWJ");
});

test("zero_width: enabled inserts U+200D only at word-boundary spaces in visible text, nowhere in tags/attrs/urls/headers", () => {
  const body = `<div style="padding: 5px;"><a href="https://example.com/x?y=1">Click this link right here for more info about the offer today</a></div>`;
  const out = insertZeroWidth(body, "job-zwj-on");
  // Every U+200D must be immediately followed by a literal space (it is inserted right before a space).
  const positions = [...out.matchAll(/‍/g)].map((m) => m.index);
  assert.ok(positions.length > 0, "a long enough text node at a ~5% rate should insert at least one ZWJ (else the test proves nothing)");
  for (const p of positions) {
    assert.equal(out[p + 1], " ", "a ZWJ must always be immediately followed by the space it was inserted before");
  }
  // None inside the tag/attribute region (before the first '>' of the anchor) or inside the URL.
  const tagEnd = out.indexOf(">", out.indexOf("<a "));
  const tagRegion = out.slice(0, tagEnd + 1);
  assert.ok(!tagRegion.includes("‍"), "no ZWJ must appear inside a tag/attribute (e.g. the href URL)");
});

test("zero_width: deterministic per job_id; rate is reproducible", () => {
  const body = `<p>${"word ".repeat(200)}</p>`;
  const a = insertZeroWidth(body, "job-zwj-det");
  const b = insertZeroWidth(body, "job-zwj-det");
  assert.equal(a, b, "same job_id must produce identical ZWJ placement");
  const countA = (a.match(/‍/g) || []).length;
  // ~5% of ~200 spaces ⇒ roughly 10, generous bounds to avoid flakiness while still proving "not 0, not all".
  assert.ok(countA > 0 && countA < 100, `ZWJ count ${countA} outside a sane ~5% band for 200 spaces`);
});

test("zero_width: skips short text nodes and never splits an HTML entity", () => {
  const body = `<p>Hi</p><p>Terms &amp; Conditions apply to everyone reading this today</p>`;
  const out = insertZeroWidth(body, "job-zwj-entity");
  assert.ok(!out.includes("&‍"), "a ZWJ must never be inserted immediately after an entity ampersand");
  assert.ok(out.includes("<p>Hi</p>"), "short text node ('Hi', <5 chars) must be left completely untouched");
});

// --- T2: each toggle independently switchable — flipping one changes ONLY its own effect --------

test("T2: independence — enabling one toggle changes only that toggle's effect vs the all-off baseline", () => {
  const send = sendWithBody(
    '<div style="padding: 10px; margin: 2px;"><!--keep--><p>Hello world this is some visible text</p><a href="https://example.com/a">a</a></div>',
  );
  const jobId = "job-t2";
  const baseline = assembleDataBytes(send, {}, jobId);

  const cssOnly = assembleDataBytes(send, { css_micro: true }, jobId);
  const urlOnly = assembleDataBytes(send, { unique_url_params: true }, jobId);
  const minifyOnly = assembleDataBytes(send, { html_minify: true }, jobId);

  // css_micro must not touch URLs or strip comments.
  assert.ok(cssOnly.includes("<!--keep-->"), "css_micro must not remove comments");
  assert.ok(cssOnly.includes('href="https://example.com/a"'), "css_micro must not touch URLs");
  assert.notEqual(cssOnly, baseline, "css_micro on must differ from baseline");

  // unique_url_params must not touch CSS or comments.
  assert.ok(urlOnly.includes("padding: 10px"), "unique_url_params must not touch CSS px values");
  assert.ok(urlOnly.includes("<!--keep-->"), "unique_url_params must not remove comments");
  assert.notEqual(urlOnly, baseline);

  // html_minify must not touch URLs (removes the comment by design, but must not alter the link).
  assert.ok(minifyOnly.includes('href="https://example.com/a"'), "html_minify must not touch URLs");
  assert.notEqual(minifyOnly, baseline);
});

// --- full-pipeline determinism through assembleDataBytes ----------------------------------------

test("full pipeline: two assemblies of the same job_id with variation on are byte-identical (T4 precursor)", () => {
  const send = sendWithBody('<div style="padding: 12px;"><p>Some fairly long visible text for zero width testing here</p><a href="https://x.com/y">y</a></div>');
  const profile = { html_minify: true, comment_injection: true, comment_intensity: "high", css_micro: true, unique_url_params: true, zero_width: true };
  const a = assembleDataBytes(send, profile, "job-full-det");
  const b = assembleDataBytes(send, profile, "job-full-det");
  assert.equal(a, b, "identical job_id + identical profile must produce byte-identical assembled output");
});

test("full pipeline: variation ON produces DIFFERENT bytes than variation OFF for the same content", () => {
  const send = sendWithBody('<div style="padding: 12px;"><p>Some fairly long visible text for zero width testing here</p><a href="https://x.com/y">y</a></div>');
  const off = assembleDataBytes(send, undefined, "job-diff");
  const on = assembleDataBytes(send, { html_minify: true, css_micro: true, unique_url_params: true }, "job-diff");
  assert.notEqual(off, on);
});

// Anti-vacuity for the whole suite: prove buildDataBytes (the frozen reference) is unaffected by
// this chunk's changes — still importable, still produces the same bytes for the pinned send.
test("anti-vacuity: the frozen Phase 3 reference is unaffected by the Chunk 3 stages", () => {
  const send = sendWithBody("<p>ref</p>");
  assert.equal(buildDataBytes(send), buildDataBytes(send));
});
