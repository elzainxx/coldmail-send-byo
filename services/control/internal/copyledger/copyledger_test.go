package copyledger

// The user-copy ledger gate (FW-M3-21; ruled by M3-P8-OD-3, built here).
//
// M3-P8-OD-3 ruled that the copy ledger is a BLOCKING gate, "scoped to what it can prove": every user-facing string is
// enumerated, and every ledger row names the evidence it asserts. Until this package existed the ledger
// (Docs/user-copy-strings.txt) was a reviewed table, not a checked one — a string added to code without a row was not
// caught, and nothing noticed when a row outlived its string.
//
// WHAT IS CHECKED, in both directions, as a three-way set equality on the string_id:
//
//	the CODE        every `// copy-ledger: <id>` marker in services/control/internal (non-test Go): the sites that
//	                render, or pass through, a string a user reads
//	the LEDGER      Docs/user-copy-strings.txt, one `string_id|surface|evidence_it_asserts` row per string
//	the AUDIT DOC   the id column of the tables in Docs/user-facing-copy.md, where each string carries its verdict
//
// plus COMPLETENESS, the direction a marker convention alone cannot give: in the files that render copy, every string
// literal that reads as prose (four or more words, not SQL) must sit in a statement marked either with a ledger id or with
// `// copy-ledger: not-copy: <reason>`. A new sentence added to copy.go therefore fails this test until someone gives it a
// ledger row and an audit verdict; it cannot be added silently.
//
// WHAT IS NOT CHECKED, said plainly, because the ruling says so: whether the evidence a row names is TRUE, and whether a
// string's wording is honest about it. That is the audit doc's verdict column and a human's judgment. This gate proves the
// ledger is complete and coherent; it does not prove it is right.
//
// COPYLEDGER_ROOT overrides the repository root; tests/harness/checks/user_copy_ledger_selftest.sh points it at a scratch
// copy it has deliberately broken, and requires this test to go red for each break.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	ledgerPath = "Docs/user-copy-strings.txt"
	auditPath  = "Docs/user-facing-copy.md"
	codeRoot   = "services/control/internal"
)

// copySites are the files that render user-facing copy, and — where only part of a file does — the functions that do.
// Every prose literal inside them must be marked. A listed file that no longer exists, or that yields no prose literal at
// all, fails: a scan that scans nothing is not a gate.
var copySites = []struct {
	file  string
	funcs []string
}{
	{"services/control/internal/fromgate/copy.go", nil},
	{"services/control/internal/dispatch/verify.go", []string{"externalCodeFor"}},
	{"services/control/internal/platformapi/account_update.go", nil},
	{"services/control/internal/platformapi/account_bulk_add.go", nil},
	{"services/control/internal/adminapi/account_update.go", nil},
}

var validSurfaces = map[string]bool{"fromgate": true, "smtp_error_frame": true, "notifications": true, "account_events": true}

var (
	idPattern     = regexp.MustCompile(`^[a-z][a-z_]*\.[a-z][a-z0-9_]*$`)
	markerPattern = regexp.MustCompile(`^//\s*copy-ledger:\s*(.+?)\s*$`)
	auditPattern  = regexp.MustCompile("(?m)^\\|\\s*`([a-z][a-z_]*\\.[a-z][a-z0-9_]*)`\\s*\\|")
	sqlPattern    = regexp.MustCompile(`(?i)^\s*(select|insert|update|delete|with|create|alter|drop)\b`)
	sqlFragment   = regexp.MustCompile(`^\s*(AND|OR|WHERE|FROM|JOIN|LEFT|SET|VALUES|RETURNING|ORDER|GROUP|LIMIT|ON)\b`)
)

func root(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("COPYLEDGER_ROOT"); r != "" {
		return r
	}
	r, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return r
}

type ledgerRow struct {
	id       string
	surface  string
	evidence string
	line     int
}

func readLedger(t *testing.T, base string) []ledgerRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(base, ledgerPath))
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	var rows []ledgerRow
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		f := strings.SplitN(line, "|", 3)
		if len(f) != 3 {
			t.Errorf("MALFORMED LEDGER ROW %s:%d: want exactly string_id|surface|evidence_it_asserts, got %q", ledgerPath, i+1, line)
			continue
		}
		rows = append(rows, ledgerRow{id: strings.TrimSpace(f[0]), surface: strings.TrimSpace(f[1]), evidence: strings.TrimSpace(f[2]), line: i + 1})
	}
	return rows
}

// marker is one `// copy-ledger:` comment: the ids it claims, or a not-copy exemption with its reason.
type marker struct {
	file    string
	line    int
	ids     []string
	notCopy bool
	reason  string
}

func parseMarker(file string, c *ast.Comment, fset *token.FileSet) (marker, bool) {
	m := markerPattern.FindStringSubmatch(c.Text)
	if m == nil {
		return marker{}, false
	}
	mk := marker{file: file, line: fset.Position(c.End()).Line}
	body := m[1]
	if strings.HasPrefix(body, "not-copy:") {
		mk.notCopy = true
		mk.reason = strings.TrimSpace(strings.TrimPrefix(body, "not-copy:"))
		return mk, true
	}
	for _, id := range strings.Split(body, ",") {
		mk.ids = append(mk.ids, strings.TrimSpace(id))
	}
	return mk, true
}

// collectMarkers parses every non-test Go file under services/control/internal and returns the markers in it.
func collectMarkers(t *testing.T, base string) []marker {
	t.Helper()
	var out []marker
	dir := filepath.Join(base, codeRoot)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if mk, ok := parseMarker(filepath.ToSlash(rel), c, fset); ok {
					out = append(out, mk)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestLedgerIsWellFormed: every row has the three fields, a known surface, a unique well-formed id and a real evidence
// claim, and every surface is represented.
func TestLedgerIsWellFormed(t *testing.T) {
	rows := readLedger(t, root(t))
	if len(rows) == 0 {
		t.Fatalf("the ledger %s has no rows — an empty ledger would make every other check vacuous", ledgerPath)
	}
	seen := map[string]int{}
	surfaces := map[string]bool{}
	for _, r := range rows {
		if !idPattern.MatchString(r.id) {
			t.Errorf("BAD STRING_ID %s:%d: %q is not a stable slug of the form surface_or_area.name", ledgerPath, r.line, r.id)
		}
		if prev, dup := seen[r.id]; dup {
			t.Errorf("DUPLICATE STRING_ID %s:%d: %q is already a row at line %d", ledgerPath, r.line, r.id, prev)
		}
		seen[r.id] = r.line
		if !validSurfaces[r.surface] {
			t.Errorf("BAD SURFACE %s:%d: %q is not one of fromgate | smtp_error_frame | notifications | account_events", ledgerPath, r.line, r.surface)
		}
		surfaces[r.surface] = true
		// The evidence column is the ruling's other half: "every ledger row naming the evidence it asserts". A placeholder is
		// the defect the ledger exists to surface ("a string whose evidence column would have to say 'nothing'").
		if len(r.evidence) < 30 || regexp.MustCompile(`(?i)^(todo|tbd|n/?a|none|nothing)\b`).MatchString(r.evidence) {
			t.Errorf("NO EVIDENCE %s:%d: %q names no specific fact the module holds at render time (%q)", ledgerPath, r.line, r.id, r.evidence)
		}
	}
	for s := range validSurfaces {
		if !surfaces[s] {
			t.Errorf("SURFACE WITH NO ROWS: %q — the audit covers four surfaces and this one has no string in the ledger", s)
		}
	}
}

// TestLedgerCodeAndAuditDocAgree is the three-way set equality, with each direction of each difference named.
func TestLedgerCodeAndAuditDocAgree(t *testing.T) {
	base := root(t)
	rows := readLedger(t, base)
	ledger := map[string]bool{}
	for _, r := range rows {
		ledger[r.id] = true
	}

	code := map[string][]string{} // id -> sites
	for _, mk := range collectMarkers(t, base) {
		for _, id := range mk.ids {
			code[id] = append(code[id], mk.file+":"+strconv.Itoa(mk.line))
			if !ledger[id] {
				t.Errorf("CODE SITE NOT IN THE LEDGER: %s:%d marks %q, which is not a string_id in %s — a user-facing string the ledger does not know", mk.file, mk.line, id, ledgerPath)
			}
		}
	}
	for _, r := range rows {
		if len(code[r.id]) == 0 {
			t.Errorf("LEDGER ROW WITH NO CODE SITE: %s:%d %q has no `// copy-ledger: %s` marker under %s — a row that nothing renders", ledgerPath, r.line, r.id, r.id, codeRoot)
		}
	}

	b, err := os.ReadFile(filepath.Join(base, auditPath))
	if err != nil {
		t.Fatalf("read the audit doc: %v", err)
	}
	audit := map[string]bool{}
	for _, m := range auditPattern.FindAllStringSubmatch(string(b), -1) {
		audit[m[1]] = true
	}
	if len(audit) == 0 {
		t.Fatalf("the audit doc %s names no string ids in its tables — the doc leg of the equality would be vacuous", auditPath)
	}
	for _, id := range sortedKeys(audit) {
		if !ledger[id] {
			t.Errorf("AUDIT DOC ROW NOT IN THE LEDGER: %s has a verdict for %q, which is not a row in %s", auditPath, id, ledgerPath)
		}
	}
	for _, r := range rows {
		if !audit[r.id] {
			t.Errorf("LEDGER ROW NOT IN THE AUDIT DOC: %s:%d %q has no verdict row in %s — a string nobody audited", ledgerPath, r.line, r.id, auditPath)
		}
	}
}

// isProse reports whether a string literal reads as a sentence a user could see: four or more words, at least one of them
// alphabetic, and not a SQL statement.
func isProse(s string) bool {
	if sqlPattern.MatchString(s) || sqlFragment.MatchString(s) {
		return false
	}
	words := 0
	for _, w := range strings.Fields(s) {
		if strings.ContainsAny(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			words++
		}
	}
	return words >= 4
}

// statementLine returns the first line of the nearest simple statement (or top-level declaration) enclosing the node at the
// top of the stack: the line a marker must sit on, or directly above.
func statementLine(stack []ast.Node, fset *token.FileSet) int {
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i].(type) {
		case *ast.ReturnStmt, *ast.AssignStmt, *ast.ExprStmt, *ast.DeclStmt, *ast.GoStmt, *ast.DeferStmt, *ast.SendStmt, *ast.IncDecStmt, *ast.GenDecl:
			return fset.Position(stack[i].Pos()).Line
		}
	}
	return fset.Position(stack[len(stack)-1].Pos()).Line
}

// TestEveryProseLiteralInACopyFileIsLedgered is COMPLETENESS: a sentence added to a file that renders copy cannot be added
// without a ledger id (or an explicit, reasoned not-copy exemption) on its statement.
func TestEveryProseLiteralInACopyFileIsLedgered(t *testing.T) {
	base := root(t)
	ledger := map[string]bool{}
	for _, r := range readLedger(t, base) {
		ledger[r.id] = true
	}
	for _, site := range copySites {
		path := filepath.Join(base, filepath.FromSlash(site.file))
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Errorf("DESIGNATED COPY FILE MISSING OR UNPARSEABLE: %s: %v — the completeness scan would scan nothing", site.file, err)
			continue
		}
		// markers by the line they end on
		marks := map[int]marker{}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if mk, ok := parseMarker(site.file, c, fset); ok {
					marks[mk.line] = mk
				}
			}
		}
		inScope := func(p token.Pos) bool {
			if len(site.funcs) == 0 {
				return true
			}
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				for _, name := range site.funcs {
					if fd.Name.Name == name && p >= fd.Pos() && p < fd.End() {
						return true
					}
				}
			}
			return false
		}
		scanned := 0
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !inScope(lit.Pos()) {
				return true
			}
			if len(stack) >= 2 {
				if _, isTag := stack[len(stack)-2].(*ast.Field); isTag {
					return true
				}
			}
			val, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !isProse(val) {
				return true
			}
			scanned++
			line := statementLine(stack, fset)
			mk, found := marks[line-1]
			if !found {
				mk, found = marks[line]
			}
			switch {
			case !found:
				t.Errorf("UNLEDGERED STRING: %s:%d reads as user-facing prose (%q) and has no `// copy-ledger: <id>` marker on or directly above its statement — give it a row in %s and a verdict in %s",
					site.file, fset.Position(lit.Pos()).Line, shorten(val), ledgerPath, auditPath)
			case mk.notCopy && mk.reason == "":
				t.Errorf("NOT-COPY WITHOUT A REASON: %s:%d is exempted from the ledger with no reason recorded", site.file, mk.line)
			case !mk.notCopy:
				for _, id := range mk.ids {
					if !ledger[id] {
						t.Errorf("CODE SITE NOT IN THE LEDGER: %s:%d marks %q, which is not a string_id in %s", site.file, mk.line, id, ledgerPath)
					}
				}
			}
			return true
		})
		if scanned == 0 {
			t.Errorf("EMPTY SCAN: %s yielded no prose literal in scope %v — either the file stopped rendering copy or the scope is wrong; a scan that finds nothing proves nothing", site.file, site.funcs)
		}
	}
}

func shorten(s string) string {
	if len(s) > 70 {
		return s[:70] + "…"
	}
	return s
}
