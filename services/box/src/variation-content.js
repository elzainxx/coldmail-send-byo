// variation-content.js — Phase 8 CHUNK 3: the body/content STAGES (toggles 1-5 of §5). Ported
// from the reference (`Docs/mailflow-worker (Sender Inspiration)/src/features/content-variation.ts`
// B1-B6), with ONE deliberate adaptation: every random draw is replaced by the job_id+stream-seeded
// PRNG from variation.js (P8-5) so the same job always produces the same variation — the reference
// used bare Math.random(), which this module never calls.
//
// Each stage operates on msg.body (an HTML string) and returns a new message with only `body`
// changed — headers/transferEncoding/boundary pass through untouched. Registered into STAGES by
// variation.js in the locked pipeline order (§1.1): minify → comments → css → url params →
// zero-width. (Dynamic preheader, toggle 8, is body/content too but ships in Chunk 4 alongside the
// other still-open toggles; its stage will be inserted BEFORE minify to match §1.1's order when it
// lands — order is documented there, not re-derived here.)

import { prngFor, hashFor } from "./variation-seed.js";

function withBody(msg, body) {
  return { ...msg, body };
}

// --- Toggle 1: HTML minification (variation_html_minify, ref B1) --------------------------------

// htmlMinify: strip HTML comments (PRESERVE Outlook conditional comments `<!--[if ...` and the
// matching `<!--<![endif]-->` close form), collapse inter-tag whitespace, collapse runs of spaces,
// trim line-trailing whitespace, collapse empty lines. Deterministic — no randomness involved, so
// no seed is needed; ported verbatim from the reference's minifyEmailHtml.
export function htmlMinify(body) {
  let result = body;
  result = result.replace(/<!--(?!\[if)(?!<!\[endif\])[\s\S]*?-->/g, "");
  result = result.replace(/>\s+</g, "><");
  result = result.replace(/\s{2,}/g, " ");
  result = result.replace(/\s+\n/g, "\n");
  result = result.replace(/\n{2,}/g, "\n");
  return result.trim();
}

// --- Toggle 2: invisible comment injection (variation_comment_injection + _intensity, ref B2) ----

const COMMENT_RATE = { low: 0.05, medium: 0.15, high: 0.30 };

// injectComments: after each closing tag, seeded-decide (per occurrence, in document order) whether
// to append an invisible `<!--{8 seeded hex chars}-->`. Two draws per closing tag from the SAME
// job+"comments"-seeded stream — one for the rate decision, four (one per byte) for the hex payload
// when it fires — so the whole injection is reproducible for a given job_id and unaffected by any
// OTHER toggle's own stream (§7/P8-6 no hidden coupling).
export function injectComments(body, intensity, jobId) {
  const rate = COMMENT_RATE[intensity] ?? COMMENT_RATE.low;
  const rng = prngFor(jobId, "comments");
  return body.replace(/(<\/\w+>)/g, (match) => {
    if (rng() < rate) {
      let hex = "";
      for (let i = 0; i < 4; i++) hex += Math.floor(rng() * 256).toString(16).padStart(2, "0");
      return `${match}<!--${hex}-->`;
    }
    return match;
  });
}

// --- Toggle 3: CSS micro-variation (variation_css_micro, ref B3) --------------------------------

// applyCssVariation: vary integer `padding`/`margin` px values by a seeded {-1, 0, +1}. Skips
// values <3px and decimal values (preserve precision, matching the reference); never lets the
// result go below 0. One draw per matched declaration from the job+"css" stream.
export function applyCssVariation(body, jobId) {
  const rng = prngFor(jobId, "css");
  return body.replace(/(padding|margin):\s*(\d+(?:\.\d+)?)px/g, (match, prop, px) => {
    const original = parseFloat(px);
    if (original < 3) return match;
    if (px.includes(".")) return match;
    const delta = Math.floor(rng() * 3) - 1;
    const varied = Math.round(original) + delta;
    return `${prop}: ${Math.max(0, varied)}px`;
  });
}

// --- Toggle 4: unique link/image URL parameters (variation_unique_url_params, ref B4+B5) --------
// NOT tracking (§9 P8-10, permanent forward boundary): purely an inert cache-buster appended to the
// AUTHOR'S OWN destination URL. No new endpoint, no redirect, no tracking domain is ever introduced
// here — see variation.js's module header and the spec §5 toggle 4 for the forward-boundary text
// this stage must never be repurposed against.

const IMG_EXT = /\.(png|jpg|jpeg|gif|webp|svg)$/i;

// addUniqueURLParams: appends `?_ref=`/`&_ref=` to every `href="http(s)://..."` and `?_v=`/`&_v=`
// to every image `src="http(s)://....(png|jpg|jpeg|gif|webp|svg)..."`, where the hash is derived
// from job_id (§5 toggle 4: "per-recipient, since one job = one recipient") — NOT a per-URL random
// draw, so every link/image in the SAME message shares one stable per-recipient value, matching the
// reference's per-contact (not per-link) hash. Skips a URL that already carries the param.
export function addUniqueURLParams(body, jobId) {
  const refHash = hashFor(jobId, "url_ref", 8);
  const vHash = hashFor(jobId, "url_img", 6);

  let out = body.replace(/href="(https?:\/\/[^"]+)"/g, (match, url) => {
    if (url.includes("_ref=")) return match;
    return `href="${appendParam(url, "_ref", refHash)}"`;
  });
  out = out.replace(/src="(https?:\/\/[^"]+)"/g, (match, url) => {
    const withoutFragment = url.split("#")[0];
    if (!IMG_EXT.test(withoutFragment.split("?")[0])) return match;
    if (url.includes("_v=")) return match;
    return `src="${appendParam(url, "_v", vHash)}"`;
  });
  return out;
}

// appendParam inserts `name=value` before any URL fragment, using `&` if the URL already has a
// query string, `?` otherwise.
function appendParam(url, name, value) {
  const fragmentIndex = url.indexOf("#");
  const urlPart = fragmentIndex === -1 ? url : url.slice(0, fragmentIndex);
  const fragment = fragmentIndex === -1 ? "" : url.slice(fragmentIndex);
  const separator = urlPart.includes("?") ? "&" : "?";
  return `${urlPart}${separator}${name}=${value}${fragment}`;
}

// --- Toggle 5: zero-width insertion (variation_zero_width, ref B6) — DEFAULT OFF -----------------

const ZWJ = "‍";
const ZWJ_RATE = 0.05; // §5 toggle 5: "~5% of word-boundary spaces" — a fixed toggle behavior, not
                       // a further sender.config dial (the spec names no intensity parameter for
                       // this toggle, unlike toggle 2's comment_intensity).

// insertZeroWidth: within visible text nodes only (`>text<`, never inside a tag/attribute/URL),
// insert a ZWJ before a seeded ~5% of spaces. Skips text nodes under 5 chars and skips a space
// immediately after an HTML entity (`&...`) so an entity is never split. One draw per space from
// the job+"zwj" stream, in document order — reproducible per job_id.
export function insertZeroWidth(body, jobId) {
  const rng = prngFor(jobId, "zwj");
  return body.replace(/>([^<]+)</g, (match, textContent) => {
    if (textContent.trim().length < 5) return match;
    const varied = textContent.replace(/ /g, (space, offset) => {
      const before = textContent.substring(Math.max(0, offset - 6), offset);
      if (/&\w*$/.test(before)) return space;
      return rng() < ZWJ_RATE ? `${ZWJ}${space}` : space;
    });
    return `>${varied}<`;
  });
}

// --- stage registrations, in the §1.1 body/content pipeline order -------------------------------

export const contentStages = [
  {
    name: "html_minify",
    active: (v) => v.html_minify,
    apply: (msg) => withBody(msg, htmlMinify(msg.body)),
  },
  {
    name: "comment_injection",
    active: (v) => v.comment_injection,
    apply: (msg, v, jobId) => withBody(msg, injectComments(msg.body, v.comment_intensity, jobId)),
  },
  {
    name: "css_micro",
    active: (v) => v.css_micro,
    apply: (msg, v, jobId) => withBody(msg, applyCssVariation(msg.body, jobId)),
  },
  {
    name: "unique_url_params",
    active: (v) => v.unique_url_params,
    apply: (msg, v, jobId) => withBody(msg, addUniqueURLParams(msg.body, jobId)),
  },
  {
    name: "zero_width",
    active: (v) => v.zero_width,
    apply: (msg, v, jobId) => withBody(msg, insertZeroWidth(msg.body, jobId)),
  },
];
