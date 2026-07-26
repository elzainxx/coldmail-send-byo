package dispatch

import (
	"context"
	"testing"
)

// variation_test.go — Phase 8 CHUNK 5: proves the Brain reads the LIVE sender.config content-
// variation keys (sender_0020, §6.1) — not a hardcoded snapshot — and stamps the resolved values
// onto push_job.variation. Real Postgres, real Dispatcher, no mocks (CLAUDE.md Rule 3).
//
// This is the config-not-code closing proof for Phase 8: Chunk 2 shipped a wire-only literal
// snapshot (defaultVariationSnapshot, since no seeded row existed yet); this test asserts the
// REPLACEMENT (variationSnapshot(cfg), config.go's fourteen readers) genuinely reads what an
// operator writes, exactly the same freshness contract every other dispatch dial already has
// (TryDispatch calls LoadConfig fresh — §0 OD-14, "a config change takes effect on the very next
// attempt, no redeploy").

// TestVariation_DefaultConfigYieldsNeutralSnapshot — the §4 identity baseline at the LIVE-config
// layer (not just the wire-shape layer Chunk 2 already covers): with sender_0020's seeded
// defaults untouched, every field of the stamped push_job.variation must equal the Phase-3-neutral
// value the box's own VARIATION_DEFAULTS expects.
func TestVariation_DefaultConfigYieldsNeutralSnapshot(t *testing.T) {
	e := newDispatchEnv(t, "variationdflt")
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")

	job := e.seedQueuedJob(t, "variation-default@example.com")
	e.dispatchToSending(t, job)

	pj, ok := e.px.lastPush()
	if !ok {
		t.Fatal("no push recorded")
	}
	v := pj.Variation
	if v == nil {
		t.Fatal("push_job.variation is nil — the Brain must always stamp a snapshot (Chunk 2/5), even at all-default")
	}
	switch {
	case v.HTMLMinify, v.CommentInjection, v.CSSMicro, v.UniqueURLParams, v.ZeroWidth,
		v.EncodingRotation, v.MIMEBoundaryRotation, v.DynamicPreheader, v.UserAgentInjection, v.ThreadIndex:
		t.Fatalf("a boolean toggle is true at the seeded default: %+v", v)
	}
	if v.CommentIntensity != "low" {
		t.Errorf("comment_intensity = %q, want the seeded default %q", v.CommentIntensity, "low")
	}
	if v.HeaderStyle != "none" {
		t.Errorf("header_style = %q, want the seeded default %q (Phase 3 identity)", v.HeaderStyle, "none")
	}
	if v.Precedence != "auto" {
		t.Errorf("precedence = %q, want the seeded default %q (Phase 3 identity)", v.Precedence, "auto")
	}
	if len(v.PreheaderTemplates) != 7 {
		t.Errorf("preheader_templates has %d entries, want the seeded 7 token-free defaults", len(v.PreheaderTemplates))
	}
}

// TestVariation_LiveConfigChangeReachesNextPushJob — the config-not-code proof itself: flip a
// boolean toggle + a string toggle via a direct sender.config write (the same write shape the
// admin API's PUT /v1/admin/config/{key} produces), then dispatch a SECOND job with NO Brain
// restart. Its push_job.variation MUST reflect the new values — proving the read path is live
// config, not a cached/hardcoded snapshot.
func TestVariation_LiveConfigChangeReachesNextPushJob(t *testing.T) {
	e := newDispatchEnv(t, "variationlive")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")

	// First job, still at the seeded defaults — the baseline this test's change must differ from.
	jobBefore := e.seedQueuedJob(t, "before@example.com")
	e.dispatchToSending(t, jobBefore)
	pjBefore, _ := e.px.lastPush()
	if pjBefore.Variation == nil || pjBefore.Variation.HTMLMinify {
		t.Fatalf("precondition: html_minify already true before the config change — the test would be vacuous")
	}

	// Live operator write: flip variation_html_minify on and variation_header_style to "gmail".
	e.setConfigBool(t, "variation_html_minify", true)
	e.setConfigStr(t, "variation_header_style", "gmail")

	e.px.freeAllBoxes()
	jobAfter := e.seedQueuedJob(t, "after@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch after config change: %v", err)
	}
	if st, _, _, _, _ := e.jobState(t, jobAfter); st != "sending" {
		t.Fatalf("precondition: state=%s, want sending", st)
	}
	pjAfter, ok := e.px.lastPush()
	if !ok || pjAfter.JobID != jobAfter {
		t.Fatalf("push after config change did not target the expected job (got %+v)", pjAfter)
	}
	if pjAfter.Variation == nil {
		t.Fatal("push_job.variation is nil after the config change")
	}
	if !pjAfter.Variation.HTMLMinify {
		t.Error("html_minify was set to true in sender.config but the pushed snapshot still says false — the Brain is not reading live config")
	}
	if pjAfter.Variation.HeaderStyle != "gmail" {
		t.Errorf("header_style = %q after the config change, want %q", pjAfter.Variation.HeaderStyle, "gmail")
	}
	// The FIRST job's already-pushed snapshot must be untouched by the later config write — proving
	// this is a per-dispatch-fresh stamp (§2.1), not a shared mutable struct both pushes alias.
	if pjBefore.Variation.HTMLMinify {
		t.Error("the EARLIER push's recorded variation snapshot mutated after a later config change — snapshots must be independent per push")
	}
}

// TestVariation_PreheaderTemplatesOverrideReachesPushJob — the array<string> toggle-parameter
// reads correctly too (not just booleans/strings), and an operator-supplied override replaces the
// seeded default list rather than merging with it.
func TestVariation_PreheaderTemplatesOverrideReachesPushJob(t *testing.T) {
	e := newDispatchEnv(t, "variationpreheader")
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")

	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.config SET value = jsonb_build_object('v', $1::jsonb), updated_at = NOW() WHERE key = 'variation_preheader_templates'`,
		`["Custom filler one", "Custom filler two"]`); err != nil {
		t.Fatalf("set preheader templates: %v", err)
	}

	job := e.seedQueuedJob(t, "preheader-override@example.com")
	e.dispatchToSending(t, job)
	pj, ok := e.px.lastPush()
	if !ok {
		t.Fatal("no push recorded")
	}
	if got := pj.Variation.PreheaderTemplates; len(got) != 2 || got[0] != "Custom filler one" || got[1] != "Custom filler two" {
		t.Fatalf("preheader_templates = %v, want the operator override (2 custom entries)", got)
	}
}
