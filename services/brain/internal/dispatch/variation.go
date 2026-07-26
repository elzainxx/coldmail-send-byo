package dispatch

import "github.com/sender-module/brain/internal/boxproto"

// variation.go — Phase 8 (P8-2/P8-3/P8-4): the Brain's half of the content-variation wire
// contract. The Brain reads the GLOBAL sender.config toggle set (Config, populated by LoadConfig
// from the seventeen sender_0020 keys — amended from fourteen post-Chunk-5, P8-OD-1 RULED-WIDEN)
// and stamps a RESOLVED snapshot onto every push_job (§2.2); the box never reads config
// (services/box/src/variation.js's resolveVariation/STAGES is the box-side identity-function
// guarantee that makes an absent/all-default snapshot safe either way).
//
// variationSnapshot performs NO decision logic beyond copying cfg's already-loaded values into the
// wire shape — every toggle's semantics live box-side. GLOBAL scope (§3): the same snapshot is
// stamped on every push_job in a given LoadConfig lifetime; there is no per-pool/per-campaign
// resolution (flagged future refinement, §9 P8-3 — not built).
func variationSnapshot(cfg Config) *boxproto.Variation {
	return &boxproto.Variation{
		HTMLMinify:           cfg.VariationHTMLMinify,
		CommentInjection:     cfg.VariationCommentInjection,
		CommentIntensity:     cfg.VariationCommentIntensity,
		CSSMicro:             cfg.VariationCSSMicro,
		CSSMagnitudePx:       cfg.VariationCSSMagnitudePx,
		UniqueURLParams:      cfg.VariationUniqueURLParams,
		ZeroWidth:            cfg.VariationZeroWidth,
		ZeroWidthRate:        cfg.VariationZeroWidthRate,
		EncodingRotation:     cfg.VariationEncodingRotation,
		MIMEBoundaryRotation: cfg.VariationMIMEBoundaryRotation,
		DynamicPreheader:     cfg.VariationDynamicPreheader,
		PreheaderTemplates:   cfg.VariationPreheaderTemplates,
		HeaderStyle:          cfg.VariationHeaderStyle,
		UserAgentInjection:   cfg.VariationUserAgentInjection,
		UserAgentRate:        cfg.VariationUserAgentRate,
		Precedence:           cfg.VariationPrecedence,
		ThreadIndex:          cfg.VariationThreadIndex,
	}
}
