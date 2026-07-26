package dispatch

import "github.com/sender-module/brain/internal/boxproto"

// variation.go — Phase 8 CHUNK 2 (P8-2/P8-3/P8-4): the Brain's half of the content-variation
// wire contract. The Brain reads (eventually) the GLOBAL sender.config toggle set and stamps a
// RESOLVED snapshot onto every push_job (§2.2); the box never reads config (services/box/src/
// variation.js resolveVariation/applyVariation is the box-side identity-function guarantee).
//
// CHUNK 2 SCOPE (deliberate, flagged): sender_0020's FOURTEEN toggle/intensity keys are seeded by
// Chunk 5, not this chunk — Chunk 1 already spent migration 0020 on sender.jobs.attempt_token (the
// P7-OD-6 scope addition), so the variation config seeds land in that SAME version file to keep the
// chain contiguous (flagged to the user; see the Chunk 1/2 handoff). Until Chunk 5 lands, there is
// no sender.config row to read for any of the fourteen keys. defaultVariationSnapshot below is
// therefore a LITERAL Phase-3-neutral snapshot — not a hardcoded tuning constant (CLAUDE.md Rule 1
// governs *tunables*; every value here is a fixed structural default, identical in shape and value
// to services/box/src/variation.js's VARIATION_DEFAULTS, until there is a config row to read at all).
// Chunk 5 replaces the BODY of this function with real sender.config reads through Config/LoadConfig
// — the call site in resolveAndPush does not change.
func defaultVariationSnapshot() *boxproto.Variation {
	return &boxproto.Variation{
		// Every boolean toggle defaults to its Go zero value (false) = off, matching VARIATION_DEFAULTS.
		CommentIntensity: "low",  // parameter of comment_injection; inert while that toggle is off
		HeaderStyle:      "none", // "none" = preserve Phase 3 header order (the §4 identity)
		Precedence:       "auto", // "auto" = omit the Precedence header (Phase 3 emits none)
	}
}
