// assemble.js — the Phase 8 box-side MIME assembly entry point (§1.1). Bridges the Phase 3
// header/body shape (push_job.send) to the variation pipeline's intermediate representation
// (variation.js's STAGES operate on `{ headers, body, transferEncoding, boundary }`, never on raw
// bytes) and serializes the result back to RFC 5322 DATA bytes.
//
// This module is what makes the §4 identity property checkable on both sides of the same seam:
// `toMessage` + `serializeMessage` alone (no variation applied) MUST reproduce stream.js's
// `buildDataBytes` byte-for-byte, and `assembleDataBytes` with an absent/all-default variation
// block MUST reproduce it too — that equality (asserted by the T1 test) is what proves the
// pipeline is genuinely the identity function when off, not merely "produces similar output."

import { resolveVariation, applyVariation } from "./variation.js";

// HEADER_ORDER mirrors stream.js's buildDataBytes exactly: the §8 minimum guaranteed set in the
// Brain's fixed order, then any additional Brain-supplied headers in their map iteration order.
const HEADER_ORDER = ["From", "To", "Subject", "Reply-To", "Message-ID", "Date"];

// toMessage converts push_job.send into the pipeline's ordered-header intermediate
// representation. Single-part in Phase 3 — boundary is null because no MIME boundary exists
// until a variation stage (toggle 7, later chunk) introduces multipart structure.
export function toMessage(send) {
  const h = send.headers || {};
  const headers = [];
  for (const k of HEADER_ORDER) {
    if (h[k] != null && h[k] !== "") headers.push([k, h[k]]);
  }
  for (const k of Object.keys(h)) {
    if (!HEADER_ORDER.includes(k) && h[k] != null && h[k] !== "") headers.push([k, h[k]]);
  }
  return { headers, body: String(send.body || ""), transferEncoding: "8bit", boundary: null };
}

// serializeMessage renders the intermediate representation to RFC 5322 DATA bytes. The
// boundary===null (single-part) branch matches stream.js's buildDataBytes exactly, field for
// field — this is what T1 pins down.
export function serializeMessage(msg) {
  const lines = msg.headers.map(([k, v]) => `${k}: ${v}`);
  lines.push("MIME-Version: 1.0");
  lines.push('Content-Type: text/html; charset="utf-8"');
  lines.push(`Content-Transfer-Encoding: ${msg.transferEncoding}`);
  lines.push(""); // header/body separator
  lines.push(msg.body);
  return lines.join("\r\n");
}

// assembleDataBytes is the box's actual send-path call (§1.1): build the Phase 3 representation,
// run it through the variation pipeline (identity when variationBlock is absent/all-default,
// §4), and serialize. jobId seeds every stage's determinism (§7) — the same job always produces
// the same variation.
export function assembleDataBytes(send, variationBlock, jobId) {
  const resolved = resolveVariation(variationBlock);
  const msg = applyVariation(toMessage(send), resolved, jobId);
  return serializeMessage(msg);
}
