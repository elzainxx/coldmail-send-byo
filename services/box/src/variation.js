// variation.js — the Phase 8 content-variation layer (box-side, §1.1 / P8-1).
//
// WHERE THIS RUNS. Between "the box received push_job" and "the box hands the assembled bytes to
// DATA" (§1.1). It is the ONLY place variation happens, and it operates on a structured intermediate
// representation of the message (an ordered header list + body + MIME framing), never on raw bytes,
// so a transform cannot accidentally corrupt framing it did not mean to touch.
//
// WHAT IT MAY TOUCH. Presentation and encoding ONLY: the assembled representation's whitespace,
// comment noise, CSS px values, inert URL params, zero-width characters, transfer-encoding, MIME
// boundary, header ORDER, and a small set of additive headers (User-Agent, Precedence). It decides
// NO content: recipient, subject, body text, From, Reply-To, Message-ID and Date are the Brain's
// (§1.2). It also touches NOTHING in the credential/TLS/AUTH path — it never sees a credential; the
// SMTP session and its MemoryCredential live in smtp.js and are entirely disjoint from this file
// (§8 S1). Nothing here reads config, holds state across jobs, or performs I/O.
//
// THE HEADLINE SAFETY PROPERTY (§4, P8-4). With every toggle off/neutral the layer is the IDENTITY
// FUNCTION: `applyVariation` returns the message it was handed, unchanged, and the assembler's output
// is byte-for-byte the Phase 3 output. This is guaranteed structurally, not by convention:
//
//   * every transform is registered as a STAGE with its own `active(v)` predicate, and the pipeline
//     calls a stage ONLY when that predicate is true;
//   * the neutral defaults are chosen so that "default" literally MEANS Phase 3 — `header_style:
//     "none"` = preserve the Phase 3 order, `precedence: "auto"` = omit the header (Phase 3 emits
//     none), `encoding_rotation`/`mime_boundary_rotation` off = the Phase 3 defaults, and every
//     content toggle off = zero injected bytes;
//   * an ABSENT `variation` block resolves to exactly those defaults, so a Phase-3-era Brain that
//     never sends the block behaves identically to one that sends an all-default block.
//
// DETERMINISM (§7, P8-5). Every stage draws from a deterministic PRNG seeded from `job_id`, so the
// same job always produces the same bytes: a duplicate from the accepted crash-after-250 window is
// byte-identical (modulo the send-time Date) rather than a second, different-looking email. Each
// stage draws from its OWN independently-seeded stream (`prngFor(jobId, "css")` etc.), which is what
// makes the toggles genuinely independent — enabling one cannot shift another's draws (§8 T2).
//
// STAGES lands across Phase 8's chunks: this file ships the pipeline + the resolver; the
// determinism primitives live in variation-seed.js (re-exported below for existing importers); the
// body/content transforms (Chunk 3, variation-content.js) and the header/MIME transforms (Chunk 4)
// register themselves into STAGES here, each from their own module so a chunk's transforms stay
// reviewable independently of this file's scaffolding.

import { prngFor, hashFor, pickSeeded, DEFAULT_PREHEADER_TEMPLATES } from "./variation-seed.js";
import { contentStages } from "./variation-content.js";
import { headerMimeStages } from "./variation-headers.js";

export { prngFor, hashFor, pickSeeded };

// VARIATION_DEFAULTS is the resolved shape of an ABSENT variation block, and the value of every key
// at its seeded default (§6.1). This set IS the Phase 3 identity — see the header. It is not a tuning
// constant: the operator-tunable source of truth is sender.config, which the Brain resolves and
// stamps onto push_job; this object exists so that a missing/partial block degrades to Phase 3
// rather than to something invented. preheader_templates defaults to the spec's own token-free pool
// (§6.1) — NOT an empty list — so that enabling dynamic_preheader without an explicit override still
// has candidates to pick from; it stays INERT (per isNeutral) while dynamic_preheader is off.
export const VARIATION_DEFAULTS = Object.freeze({
  html_minify: false,
  comment_injection: false,
  comment_intensity: "low",
  css_micro: false,
  unique_url_params: false,
  zero_width: false,
  encoding_rotation: false,
  mime_boundary_rotation: false,
  dynamic_preheader: false,
  preheader_templates: DEFAULT_PREHEADER_TEMPLATES,
  header_style: "none",
  user_agent_injection: false,
  precedence: "auto",
  thread_index: false,
});

const HEADER_STYLES = ["none", "gmail", "outlook", "apple", "thunderbird", "random"];
const PRECEDENCE_MODES = ["auto", "marketing", "personal"];
const COMMENT_INTENSITIES = ["low", "medium", "high"];

const bool = (x, dflt) => (typeof x === "boolean" ? x : dflt);
const oneOf = (x, allowed, dflt) => (typeof x === "string" && allowed.includes(x) ? x : dflt);
const strArray = (x, dflt) =>
  Array.isArray(x) && x.every((s) => typeof s === "string") ? x.slice() : dflt;

// resolveVariation normalizes whatever arrived on push_job into a complete, valid toggle set.
//
// It is deliberately TOTAL and never throws: the box applies exactly the booleans it is handed, and
// an unrecognized/malformed field falls back to its Phase-3-neutral default rather than failing the
// send. That is the safe direction — a malformed toggle degrades to Phase 3, never to an arbitrary
// transform. It also means the box cannot enable a toggle the Brain did not send.
export function resolveVariation(block) {
  const b = block && typeof block === "object" ? block : {};
  const d = VARIATION_DEFAULTS;
  return {
    html_minify: bool(b.html_minify, d.html_minify),
    comment_injection: bool(b.comment_injection, d.comment_injection),
    comment_intensity: oneOf(b.comment_intensity, COMMENT_INTENSITIES, d.comment_intensity),
    css_micro: bool(b.css_micro, d.css_micro),
    unique_url_params: bool(b.unique_url_params, d.unique_url_params),
    zero_width: bool(b.zero_width, d.zero_width),
    encoding_rotation: bool(b.encoding_rotation, d.encoding_rotation),
    mime_boundary_rotation: bool(b.mime_boundary_rotation, d.mime_boundary_rotation),
    dynamic_preheader: bool(b.dynamic_preheader, d.dynamic_preheader),
    preheader_templates: strArray(b.preheader_templates, d.preheader_templates),
    header_style: oneOf(b.header_style, HEADER_STYLES, d.header_style),
    user_agent_injection: bool(b.user_agent_injection, d.user_agent_injection),
    precedence: oneOf(b.precedence, PRECEDENCE_MODES, d.precedence),
    thread_index: bool(b.thread_index, d.thread_index),
  };
}

// isNeutral reports whether a resolved toggle set is the Phase 3 identity — i.e. no stage would run.
// Exported so a test can assert the §4 property directly, and so the assembler can prove (rather
// than assume) that it took the identity path. Toggle PARAMETERS (comment_intensity,
// preheader_templates) are deliberately NOT considered: they are inert while their parent toggle is
// off, so changing one must not make the set non-neutral.
export function isNeutral(v) {
  return !STAGES.some((s) => s.active(v));
}

// --- the pipeline -------------------------------------------------------------------------------

// STAGES is the ordered transform list of §4.2. Each entry is
//   { name, active(v) -> bool, apply(msg, v, jobId) -> msg }
// and the pipeline runs a stage ONLY when `active` is true. `apply` receives and returns a MESSAGE:
//
//   { headers: [[name, value], ...],   // ordered; Phase 3 order as built by the assembler
//     body: string,                    // the HTML body
//     transferEncoding: string,        // "8bit" in Phase 3
//     boundary: string|null }          // null in Phase 3 (single-part: no boundary exists)
//
// Order matters and follows the reference: body/content first (preheader → minify → comments → css →
// url params → zero-width), then encoding/MIME/header engineering.
//
// Chunk 3 registered the five body/content stages (toggles 1-5). Chunk 4 completes the pipeline:
// dynamic preheader (toggle 8, body/content but ordered FIRST per §1.1: preheader → minify →
// comments → css → url params → zero-width — its own stage lives at the front of contentStages,
// not here) and the header/MIME stages (toggles 6, 7, 9-12: encoding rotation, MIME boundary
// rotation, header reordering, User-Agent, precedence, Thread-Index-inert), applied AFTER every
// body/content stage per §1.1's two-layer ordering. A STAGES entry runs only when its own `active`
// predicate is true, so an empty/undefined toggle set still yields the identity function (§4)
// regardless of what's registered.
export const STAGES = [...contentStages, ...headerMimeStages];

// applyVariation runs the pipeline. With an all-default toggle set NO stage is active, so the
// message object is returned UNCHANGED (identity, §4.2) — the assembler then serializes exactly the
// Phase 3 bytes.
export function applyVariation(msg, variation, jobId) {
  let out = msg;
  for (const stage of STAGES) {
    if (!stage.active(variation)) continue;
    out = stage.apply(out, variation, jobId);
  }
  return out;
}
