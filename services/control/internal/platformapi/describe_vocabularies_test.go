package platformapi

// describe_vocabularies_test.go — fix-plan items 6 and 8. Real Postgres, real HTTP against the
// actual production Server (CLAUDE.md Rule 3).
//
// WHAT MAKES THESE TESTS RATHER THAN SHAPE CHECKS. Asserting `vocabularies` is non-empty, or that it
// contains the eleven keys this file happens to know about, would be exactly the vacuity Rule 3
// forbids: it would pass just as happily against a hardcoded list, which is the implementation item 8
// exists to REMOVE. So every test here compares the PUBLISHED declaration against an INDEPENDENT
// source of truth — the live catalog, the CHECK constraint, the codeStatus map, the JSON tags in the
// production files — and each has a paired control that names the divergence it catches.
//
// The strongest of them is TestDescribeVocabularies_LiveEnumValueIsPublishedWithoutACodeChange: it
// runs a real ALTER TYPE ... ADD VALUE against the clone and asserts the new label appears in the
// very next describe response. A hardcoded list cannot pass that test, and no amount of
// list-maintenance discipline can make it pass. That is the property item 8 is actually about.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- item 8: vocabularies{} ---

// TestDescribeVocabularies_LiveEnumValueIsPublishedWithoutACodeChange is THE test for item 8's
// central claim. It adds a real value to a real enum type in the clone and asserts describe publishes
// it immediately, with no rebuild and no code edit.
//
// This is the failure mode the audit named: a module adds an enum value, every client's hardcoded
// list is now silently incomplete, and additive-only versioning did not help because the addition
// was never published. A declaration that is read from the schema per request cannot have that bug.
// A hardcoded one cannot pass this test — which is precisely why the test is written this way rather
// than as an assertion about the eleven values that exist today.
//
// pause_reason is the subject because it is small, user-facing (account_object.status.pause_reason),
// and the added label is inert: nothing reads or writes 'zzz_test_probe', and the clone is dropped at
// cleanup, so the mutation cannot leak into another test or the template.
func TestDescribeVocabularies_LiveEnumValueIsPublishedWithoutACodeChange(t *testing.T) {
	e := newTestEnv(t, "descvocablive")

	before := e.describeVocabulary(t, "pause_reason")
	for _, v := range before {
		if v == "zzz_test_probe" {
			t.Fatalf("probe label already present before the ALTER — this test would be vacuous: %v", before)
		}
	}

	// ALTER TYPE ... ADD VALUE, committed. Postgres 16 permits it inside a transaction (verified), but
	// it must be COMMITTED for the server's own pooled connections to observe it.
	if _, err := e.admin.Exec(context.Background(),
		`ALTER TYPE send_byo.pause_reason ADD VALUE 'zzz_test_probe'`); err != nil {
		t.Fatalf("ALTER TYPE ADD VALUE: %v", err)
	}

	after := e.describeVocabulary(t, "pause_reason")
	if len(after) != len(before)+1 {
		t.Fatalf("describe published %d pause_reason values after the ALTER, want %d (before=%v after=%v) — "+
			"the vocabulary is not being read from the schema",
			len(after), len(before)+1, before, after)
	}
	found := false
	for _, v := range after {
		if v == "zzz_test_probe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a value added to send_byo.pause_reason is MISSING from describe.vocabularies "+
			"(got %v) — the declaration is not derived from the live schema, so every client's "+
			"hardcoded list is silently incomplete the moment an enum grows", after)
	}
}

// TestDescribeVocabularies_EveryPublishedValueSetMatchesTheLiveType — the direct control on the
// db_enum half: for every declared type, the published values must equal the live pg_enum labels
// exactly, in order. Catches a stale, reordered, or partially-typed list.
func TestDescribeVocabularies_EveryPublishedValueSetMatchesTheLiveType(t *testing.T) {
	e := newTestEnv(t, "descvocabmatch")
	vocabs := e.describeVocabularies(t)

	live := e.liveEnumLabels(t)
	checked := 0
	for _, de := range exposedDBEnums {
		entry, ok := vocabs[de.Type].(map[string]any)
		if !ok {
			t.Fatalf("describe.vocabularies is missing declared enum %q", de.Type)
		}
		if src, _ := entry["source"].(string); src != "db_enum" {
			t.Fatalf("%s: source=%q, want db_enum", de.Type, src)
		}
		want := live[de.Type]
		if len(want) == 0 {
			t.Fatalf("%s is declared in exposedDBEnums but does not exist in the send_byo schema", de.Type)
		}
		got := stringSliceOf(t, entry["values"])
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s published %v, live type has %v (order is enumsortorder, and it is part of the "+
				"contract — a client rendering a legend sees it)", de.Type, got, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no db_enum vocabularies checked — this control would be vacuous")
	}
}

// TestDescribeVocabularies_EverySchemaEnumIsAccountedFor — the coverage control, and the one that
// keeps the hand-maintained half honest. Every enum type in the send_byo schema must appear EITHER in
// exposedDBEnums (published) OR in notExposedDBEnums (with a written reason). A new enum type that is
// user-facing and simply forgotten fails here rather than shipping undeclared — otherwise "absent
// from exposedDBEnums" would be indistinguishable from "nobody looked", and item 8's whole point
// would be defeated one level up in the mechanism meant to enforce it.
func TestDescribeVocabularies_EverySchemaEnumIsAccountedFor(t *testing.T) {
	e := newTestEnv(t, "descvocabcover")
	live := e.liveEnumLabels(t)
	if len(live) == 0 {
		t.Fatal("no send_byo enum types found — this control would be vacuous")
	}

	exposed := map[string]bool{}
	for _, de := range exposedDBEnums {
		exposed[de.Type] = true
	}
	for typname := range live {
		_, isExcluded := notExposedDBEnums[typname]
		if !exposed[typname] && !isExcluded {
			t.Fatalf("send_byo.%s exists in the schema but is in NEITHER exposedDBEnums nor "+
				"notExposedDBEnums. If a Domain-A response can carry it, declare it; if not, add it to "+
				"notExposedDBEnums with the reason. Silence is the one option that is not allowed — it is "+
				"how an undeclared user-facing vocabulary ships.", typname)
		}
		if exposed[typname] && isExcluded {
			t.Fatalf("send_byo.%s is in BOTH exposedDBEnums and notExposedDBEnums — the accounting must "+
				"be unambiguous", typname)
		}
	}
	// The exclusion list must not accumulate names that no longer exist: a stale entry would silently
	// re-open the coverage hole for a type later re-added under the same name.
	for typname := range notExposedDBEnums {
		if len(live[typname]) == 0 {
			t.Fatalf("notExposedDBEnums names send_byo.%s, which does not exist in the schema — a stale "+
				"exclusion silently exempts a future type of the same name", typname)
		}
	}
	// And an excluded type must genuinely be absent from the published set.
	vocabs := e.describeVocabularies(t)
	for typname := range notExposedDBEnums {
		if _, present := vocabs[typname]; present {
			t.Fatalf("send_byo.%s is declared NOT exposed but appears in describe.vocabularies", typname)
		}
	}
}

// TestDescribeVocabularies_StaticSetsMatchTheirSourceOfTruth — the control on the module_constant
// half. Each static vocabulary is checked against something OTHER than a copy of itself:
//
//	detail_issue              -> the issue* constants, which the same file's Values already reference;
//	                             checked here for completeness against the wire (all five present).
//	quote_reason_code         -> every code the quote switch can emit, greppped from the production
//	                             source, so a sixth reason added to the switch fails this test.
//	notification_subject_type -> the live notifications_subject_type_check CHECK definition.
//	notification_condition    -> the live INSERT sites (the same pinning notifications_read_test.go does).
func TestDescribeVocabularies_StaticSetsMatchTheirSourceOfTruth(t *testing.T) {
	e := newTestEnv(t, "descvocabstatic")
	vocabs := e.describeVocabularies(t)

	for _, sv := range staticVocabularies {
		entry, ok := vocabs[sv.Name].(map[string]any)
		if !ok {
			t.Fatalf("describe.vocabularies is missing static vocabulary %q", sv.Name)
		}
		if src, _ := entry["source"].(string); src != "module_constant" {
			t.Fatalf("%s: source=%q, want module_constant", sv.Name, src)
		}
		got := stringSliceOf(t, entry["values"])
		if strings.Join(got, ",") != strings.Join(sv.Values, ",") {
			t.Fatalf("%s published %v, constants say %v", sv.Name, got, sv.Values)
		}
		if len(got) == 0 {
			t.Fatalf("%s published an EMPTY value set — a published empty vocabulary is worse than an "+
				"absent one: a client reads it as 'this field has no values'", sv.Name)
		}
	}

	// quote_reason_code / quote_warning_code vs the production emission sites. Every distinct
	// `Code: quoteReasonX` / `Code: quoteWarningX` in the non-test source must be declared, and every
	// declared constant must really be emitted. This is what catches a sixth reason being added to
	// computeQuoteFeasibility without touching the declaration.
	//
	// The pattern anchors on `Code: ` and NOT on a preceding brace: the emission sites use the elided
	// composite-literal form `[]quoteReason{{Code: ...}}`, so a `quoteReason\{Code:` pattern matches
	// nothing. (It matched nothing on the first run — the len==0 guard below is what caught it, which
	// is the entire reason a control gets an anti-vacuity check rather than a silent pass.)
	for _, pair := range []struct {
		vocab  string
		prefix string
	}{
		{"quote_reason_code", "quoteReason"},
		{"quote_warning_code", "quoteWarning"},
	} {
		emitted := map[string]bool{}
		pat := regexp.MustCompile(`Code:\s+(` + pair.prefix + `[A-Za-z]+)\b`)
		for _, m := range grepProductionSubmatches(t, pat) {
			emitted[m] = true
		}
		if len(emitted) == 0 {
			t.Fatalf("found no `Code: %sX` emission sites in production source — the grep is broken "+
				"and this control is vacuous", pair.prefix)
		}
		declared := map[string]bool{}
		for _, c := range constNamesFor(t, pair.vocab, pair.prefix) {
			declared[c] = true
		}
		if len(declared) == 0 {
			t.Fatalf("read no %s* constants out of %s's declaration — vacuous", pair.prefix, pair.vocab)
		}
		for name := range emitted {
			if !declared[name] {
				t.Fatalf("quote emits %s but staticVocabularies' %s does not declare it — a client "+
					"switching on that field would meet an undeclared value", name, pair.vocab)
			}
		}
		// Reverse direction: a declared code no emission site can produce is dead handling a client
		// writes and never exercises. (Unlike pricing_model's deliberately forward-declared `free`,
		// these codes describe THIS module's own behaviour, so an unreachable one is a stale leftover.)
		for name := range declared {
			if !emitted[name] {
				t.Fatalf("%s declares %s but no production emission site uses it — describe advertises a "+
					"code this module cannot produce", pair.vocab, name)
			}
		}
	}

	// notification_subject_type vs the live CHECK constraint.
	var checkDef string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
		   JOIN pg_class r ON r.oid = c.conrelid
		   JOIN pg_namespace n ON n.oid = r.relnamespace
		  WHERE n.nspname='send_byo' AND r.relname='notifications'
		    AND c.conname='notifications_subject_type_check'`).Scan(&checkDef); err != nil {
		t.Fatalf("read subject_type CHECK: %v", err)
	}
	subjectTypes := vocabs["notification_subject_type"].(map[string]any)
	for _, v := range stringSliceOf(t, subjectTypes["values"]) {
		if !strings.Contains(checkDef, "'"+v+"'") {
			t.Fatalf("declared subject_type %q is NOT permitted by notifications_subject_type_check (%s) — "+
				"describe would advertise a value the database rejects", v, checkDef)
		}
	}
	// And the reverse direction: a value the CHECK allows must be declared, or a real row could carry
	// a value the published vocabulary does not list.
	for _, m := range regexp.MustCompile(`'([a-z_]+)'::text`).FindAllStringSubmatch(checkDef, -1) {
		found := false
		for _, v := range stringSliceOf(t, subjectTypes["values"]) {
			if v == m[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("the CHECK permits subject_type %q but describe does not declare it — a row carrying "+
				"it would be an undeclared value on the wire", m[1])
		}
	}
}

// TestDescribeVocabularies_EveryDeclaredFieldPathExists — the control on the `fields` documentation.
// A path like "account_object.status.pause_reason" is prose, but its LEAF must be a real JSON tag in
// this package's production files, so a renamed or deleted response field cannot leave a dangling
// path pointing a client at something that no longer exists.
func TestDescribeVocabularies_EveryDeclaredFieldPathExists(t *testing.T) {
	e := newTestEnv(t, "descvocabfields")
	vocabs := e.describeVocabularies(t)

	tags := productionJSONTags(t)
	if len(tags) < 20 {
		t.Fatalf("collected only %d JSON tags from production source — the scan is broken and this "+
			"control would be vacuous", len(tags))
	}
	// Keys that appear only as map literal keys (not struct tags) in production code, plus the two
	// describe-level paths that name describe's own output rather than a response struct.
	alsoValid := map[string]bool{
		"estimate_basis": true, // quoteFeasibilityJSON's map key
		"model":          true, // handleDescribe's pricing map key
		"code":           true, // quoteReason/quoteWarning JSON tag AND map keys
	}
	checked := 0
	for name, raw := range vocabs {
		entry := raw.(map[string]any)
		fields := stringSliceOf(t, entry["fields"])
		if len(fields) == 0 {
			t.Fatalf("vocabulary %q declares no fields — a value set nobody can locate in a response is "+
				"not discovery", name)
		}
		for _, path := range fields {
			parts := strings.Split(path, ".")
			leaf := parts[len(parts)-1]
			if !tags[leaf] && !alsoValid[leaf] {
				t.Fatalf("vocabulary %q declares field path %q, but %q is not a JSON field name anywhere "+
					"in this package's production source — the path is dangling", name, path, leaf)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no field paths checked — vacuous")
	}
}

// --- item 8: error_codes[] ---

// TestDescribeErrorCodes_MatchesCodeStatusExactly — error_codes[] must be the codeStatus map, whole.
// Both directions: a code in the map must be published (else a client's exhaustive switch is missing
// a code the module really returns), and a published code must be in the map (else describe advertises
// a code the module cannot emit, and a client writes dead handling for it).
func TestDescribeErrorCodes_MatchesCodeStatusExactly(t *testing.T) {
	e := newTestEnv(t, "descerrcodes")
	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	raw, ok := desc.body["error_codes"].([]any)
	if !ok {
		t.Fatalf("describe missing error_codes[]: %s", desc.raw)
	}
	if len(raw) == 0 {
		t.Fatal("error_codes[] is empty — vacuous, and a client would read it as 'this module cannot fail'")
	}
	published := map[string]int{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("error_codes entry is not an object: %v", item)
		}
		code, _ := m["code"].(string)
		status, sok := m["http_status"].(float64)
		if code == "" || !sok {
			t.Fatalf("error_codes entry missing code/http_status: %v", m)
		}
		if _, dup := published[code]; dup {
			t.Fatalf("error_codes lists %q twice", code)
		}
		published[code] = int(status)
		// The fixed MESSAGE must never be published: it is operator-facing English, not a branchable
		// value, and publishing it invites clients to match on message text — the coupling the coded
		// envelope exists to remove.
		if _, leaked := m["message"]; leaked {
			t.Fatalf("error_codes entry for %q publishes a message: %v", code, m)
		}
	}
	if len(published) != len(codeStatus) {
		t.Fatalf("describe publishes %d error codes, codeStatus has %d: published=%v", len(published), len(codeStatus), published)
	}
	for code, wantStatus := range codeStatus {
		got, ok := published[code]
		if !ok {
			t.Fatalf("code %q is emittable (it is in codeStatus, which writeError resolves a status from) "+
				"but is NOT published — a client building an exhaustive switch off describe would meet it "+
				"unhandled at runtime", code)
		}
		if got != wantStatus {
			t.Fatalf("code %q published http_status=%d, writeError sends %d", code, got, wantStatus)
		}
	}
	// fixedMessages and codeStatus must agree on the code set, since writeError reads one for the
	// status and the other for the default message: a code in only one produces an envelope with an
	// empty message or a 500-by-accident status.
	if len(fixedMessages) != len(codeStatus) {
		t.Fatalf("fixedMessages has %d entries, codeStatus %d — writeError reads both", len(fixedMessages), len(codeStatus))
	}
	for code := range codeStatus {
		if fixedMessages[code] == "" {
			t.Fatalf("code %q has a status but no fixed message — its envelope would carry an empty message", code)
		}
	}
}

// TestDescribeErrorCodes_APublishedCodeIsReallyEmitted — the behavioral half. A published list is
// only worth anything if the codes really appear on the wire with the published status, so this
// provokes real errors over real HTTP and checks each against the declaration.
//
// Three of the seven are provoked here (validation_failed, unauthenticated, not_found). The other
// four are provoked by their own dedicated tests elsewhere in this package (conflict by the suspend
// gate, unprocessable by batch_too_large, rate_limited by the test-button limiter, internal_error by
// the failure-path tests); duplicating those setups here would add fixtures without adding coverage.
func TestDescribeErrorCodes_APublishedCodeIsReallyEmitted(t *testing.T) {
	e := newTestEnv(t, "descerrlive")
	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	published := map[string]int{}
	for _, item := range desc.body["error_codes"].([]any) {
		m := item.(map[string]any)
		published[m["code"].(string)] = int(m["http_status"].(float64))
	}

	// validation_failed
	r := e.do(t, "GET", "/v1/send-byo/notifications?user_id=not-a-uuid", nil)
	assertPublishedCode(t, published, r, codeValidationFailed)

	// not_found
	r = e.do(t, "GET", "/v1/report?user_id="+e.seedUser(t, "descerrlive@example.com")+
		"&job_id=00000000-0000-0000-0000-000000000000", nil)
	assertPublishedCode(t, published, r, codeNotFound)

	// unauthenticated — no token at all.
	r = e.doRaw(t, "GET", "/v1/send-byo/accounts?user_id=00000000-0000-0000-0000-000000000000", nil, "")
	assertPublishedCode(t, published, r, codeUnauthenticated)
}

// --- item 6: pricing.model's declared set ---

// TestDescribePricing_ModelIsAMemberOfTheDeclaredSet — item 6. The reported model must appear in the
// published set; a module cannot report a value it did not declare. Also asserts the set contains the
// forward-compatible third value, since declaring only what exists today is the defect item 6 names
// (a third module's launch would otherwise be a client-side code change).
func TestDescribePricing_ModelIsAMemberOfTheDeclaredSet(t *testing.T) {
	e := newTestEnv(t, "descpricing")
	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	pricing, ok := desc.body["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("describe missing pricing: %s", desc.raw)
	}
	model, _ := pricing["model"].(string)
	if model == "" {
		t.Fatalf("pricing.model is empty: %s", desc.raw)
	}
	models := stringSliceOf(t, pricing["models"])
	if len(models) == 0 {
		t.Fatalf("pricing.models is absent or empty — a client must switch on model without knowing "+
			"every module's answer in advance: %s", desc.raw)
	}
	found := false
	for _, m := range models {
		if m == model {
			found = true
		}
	}
	if !found {
		t.Fatalf("pricing.model=%q is NOT a member of the declared set %v — the module reports a value it "+
			"did not declare, which is worse than not declaring the set at all", model, models)
	}
	// This module's own answer, pinned: a silent flip to flat_per_recipient would otherwise still pass
	// the membership check above while changing what the platform bills.
	if model != pricingModelFlatSubscription {
		t.Fatalf("pricing.model=%q, want %q (M3-P7-OD-11: flat monthly subscription, no per-send charge)",
			model, pricingModelFlatSubscription)
	}
	for _, want := range []string{pricingModelFlatPerRecipient, pricingModelFlatSubscription, pricingModelFree} {
		present := false
		for _, m := range models {
			if m == want {
				present = true
			}
		}
		if !present {
			t.Fatalf("declared set %v omits %q — the set is the platform's, not this module's, and a value "+
				"only this module uses is not the point", models, want)
		}
	}
}

// TestDescribeVocabularies_FailsClosedRatherThanPublishingAPartialSet — the deliberate contrast with
// readUnitPriceCredits, which falls back to a seeded default. A vocabulary has no honest default, so
// a declared-but-missing enum type must produce an error rather than a set the client believes is
// complete. Provoked by DROPping a declared type's exposure: renaming the type out from under the
// declaration is the realistic form of this (a migration renames an enum, the Go list does not
// follow), and it must not silently publish ten of eleven vocabularies.
func TestDescribeVocabularies_FailsClosedRatherThanPublishingAPartialSet(t *testing.T) {
	e := newTestEnv(t, "descvocabclosed")
	// Sanity: describe works before the mutation, so a failure below is attributable to it.
	e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)

	// routing_strategy is declared in exposedDBEnums. Renaming it makes the declared name absent.
	// Nothing else in this test's lifetime reads the type, and the clone is dropped at cleanup.
	if _, err := e.admin.Exec(context.Background(),
		`ALTER TYPE send_byo.routing_strategy RENAME TO routing_strategy_renamed`); err != nil {
		t.Fatalf("rename enum type: %v", err)
	}

	r := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 500)
	if r.errorCode() != codeInternalError {
		t.Fatalf("code=%q, want %q: %s", r.errorCode(), codeInternalError, r.raw)
	}
	// The two-vocabulary rule: the internal reason names the type, the WIRE must not.
	if strings.Contains(r.raw, "routing_strategy") {
		t.Fatalf("the 500 envelope leaks the internal type name: %s", r.raw)
	}
	if strings.Contains(r.raw, "vocabularies") {
		t.Fatalf("the error envelope must be the module's fixed shape, not a partial describe body: %s", r.raw)
	}
}

// --- helpers ---

// describeVocabularies fetches describe and returns its vocabularies object.
func (e *testEnv) describeVocabularies(t *testing.T) map[string]any {
	t.Helper()
	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	v, ok := desc.body["vocabularies"].(map[string]any)
	if !ok {
		t.Fatalf("describe missing vocabularies{}: %s", desc.raw)
	}
	return v
}

// describeVocabulary returns one vocabulary's published values.
func (e *testEnv) describeVocabulary(t *testing.T, name string) []string {
	t.Helper()
	entry, ok := e.describeVocabularies(t)[name].(map[string]any)
	if !ok {
		t.Fatalf("describe.vocabularies has no %q", name)
	}
	return stringSliceOf(t, entry["values"])
}

// liveEnumLabels reads every send_byo enum type's labels straight from the catalog, in enumsortorder
// — the same query the production code runs, issued independently through the ADMIN connection so
// the assertion does not depend on the code under test.
func (e *testEnv) liveEnumLabels(t *testing.T) map[string][]string {
	t.Helper()
	rows, err := e.admin.Query(context.Background(), enumValuesQuery)
	if err != nil {
		t.Fatalf("query pg_enum: %v", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var typname, label string
		if err := rows.Scan(&typname, &label); err != nil {
			t.Fatalf("scan pg_enum: %v", err)
		}
		out[typname] = append(out[typname], label)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pg_enum rows: %v", err)
	}
	return out
}

func stringSliceOf(t *testing.T, v any) []string {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		// Also accept a []string, for the in-process constant slices.
		if ss, ok2 := v.([]string); ok2 {
			return ss
		}
		t.Fatalf("value is not an array: %#v", v)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("array element is not a string: %#v", item)
		}
		out = append(out, s)
	}
	return out
}

// assertPublishedCode checks a real response's code+status against the published declaration.
func assertPublishedCode(t *testing.T, published map[string]int, r resp, wantCode string) {
	t.Helper()
	if r.errorCode() != wantCode {
		t.Fatalf("got code=%q, want %q: %s", r.errorCode(), wantCode, r.raw)
	}
	wantStatus, ok := published[wantCode]
	if !ok {
		t.Fatalf("the module emitted %q but describe does not publish it", wantCode)
	}
	if r.status != wantStatus {
		t.Fatalf("%q was emitted with HTTP %d but describe publishes %d — a client degrading by status "+
			"would mis-handle it", wantCode, r.status, wantStatus)
	}
}

// constNamesFor returns the Go constant NAMES (not values) that staticVocabularies uses for a given
// vocabulary, read out of describe_vocabularies.go's own source. Needed because the test must compare
// the DECLARATION against the emission sites by identifier — comparing values would pass even if the
// declaration referenced a different constant that happened to hold the same string.
func constNamesFor(t *testing.T, vocabName, prefix string) []string {
	t.Helper()
	src := readProductionFile(t, "internal/platformapi/describe_vocabularies.go")
	// `Name:` alignment inside staticVocabularies varies (gofmt aligns per-literal), so match the
	// quoted vocabulary name rather than a fixed run of spaces.
	i := strings.Index(src, `"`+vocabName+`"`)
	if i < 0 {
		t.Fatalf("could not locate the %q entry in describe_vocabularies.go", vocabName)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n\t},"); j > 0 {
		rest = rest[:j]
	}
	return regexp.MustCompile(prefix+`[A-Za-z]+`).FindAllString(rest, -1)
}

// grepProductionSubmatches returns the first submatch of every match of pat across non-test .go files
// under services/control.
func grepProductionSubmatches(t *testing.T, pat *regexp.Regexp) []string {
	t.Helper()
	var out []string
	walkProductionGo(t, func(_ string, data []byte) {
		for _, m := range pat.FindAllStringSubmatch(string(data), -1) {
			out = append(out, m[1])
		}
	})
	return out
}

// productionJSONTags collects every `json:"name"` tag name in THIS package's production files.
func productionJSONTags(t *testing.T) map[string]bool {
	t.Helper()
	tags := map[string]bool{}
	pat := regexp.MustCompile(`json:"([a-z0-9_]+)`)
	root := servicesControlRoot(t)
	dir := filepath.Join(root, "internal", "platformapi")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read platformapi dir: %v", err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", ent.Name(), err)
		}
		for _, m := range pat.FindAllStringSubmatch(string(data), -1) {
			tags[m[1]] = true
		}
	}
	return tags
}

// walkProductionGo calls fn for every non-test .go file under services/control.
func walkProductionGo(t *testing.T, fn func(path string, data []byte)) {
	t.Helper()
	root := servicesControlRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fn(path, data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk services/control: %v", err)
	}
}

func readProductionFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(servicesControlRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}
