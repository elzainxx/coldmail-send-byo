package dispatch

// consolidation_test.go — M3-P4-OD-3's own acceptance criterion, made mechanically checkable
// rather than merely asserted: "after commit 1, a grep for the predicate's distinguishing clause
// returns ONE production site." This test is that grep, run at test time against the package's
// own non-test source, so a future hand-copy of the base eligibility filter (instead of reusing
// accountBaseEligibilityFilter) fails a real test rather than silently re-introducing the drift
// class M3-P4-OD-11 found.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConsolidation_BaseEligibilityFilterHasOneDefinitionSite asserts that the literal fragment
// "a.quarantined = FALSE" — accountBaseEligibilityFilter's own distinguishing clause — appears in
// exactly ONE non-test .go file in this package (eligibility.go, where the constant is defined),
// and every OTHER file that participates in eligibility/exhaustion classification (exhaustion.go)
// consumes the constant by name rather than a hand-copied literal.
func TestConsolidation_BaseEligibilityFilterHasOneDefinitionSite(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	const needle = "a.quarantined = FALSE"
	filesWithNeedle := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if n := strings.Count(string(body), needle); n > 0 {
			filesWithNeedle[name] = n
		}
	}

	if len(filesWithNeedle) != 1 {
		t.Fatalf("%q appears in %d non-test files %v, want exactly 1 (accountBaseEligibilityFilter's own definition) — a hand-copied literal has re-introduced the drift class M3-P4-OD-3 consolidated and M3-P4-OD-11 found", needle, len(filesWithNeedle), filesWithNeedle)
	}
	if n := filesWithNeedle["eligibility.go"]; n != 1 {
		t.Fatalf("%q appears %d time(s) in eligibility.go, want exactly 1 (the accountBaseEligibilityFilter constant definition itself)", needle, n)
	}

	// Positive control: exhaustion.go must consume the constant BY NAME, not by re-copying the
	// literal — proving this test would actually catch a regression, not just an absence.
	exhaustionBody, err := os.ReadFile(filepath.Join(dir, "exhaustion.go"))
	if err != nil {
		t.Fatalf("read exhaustion.go: %v", err)
	}
	if !strings.Contains(string(exhaustionBody), "accountBaseEligibilityFilter") {
		t.Fatalf("exhaustion.go does not reference accountBaseEligibilityFilter by name — userHasSelfRecovering may have reverted to a hand-copied literal")
	}
}
