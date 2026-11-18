package platformapi

// response_leak_gate_test.go — THE GATE of the Phase 3 close-out's C3 (internal text must not cross the
// API on the test button and bulk-add).
//
// WHAT IT ENFORCES. It parses the two route files (account_test_button.go, account_bulk_add.go) and,
// for every place a response field is set — a key of a `map[string]any{...}` literal, an assignment
// `m["key"] = v`, a field of a response struct literal (`bulkAddRowResult{Code: v}`, `fieldDetail{...}`),
// an assignment `res.Field = v` — it classifies the VALUE expression and fails the build if the field
// can carry anything but its closed set:
//
//   - raw error text      any `.Error()` call in the value (err.Error(), a wrapped error's text);
//   - formatted text      fmt.Sprintf / Sprint / Errorf, errors.New — formatting is how a `%v` or `%w` of an
//     error reaches a string;
//   - concatenation       a `+` with a non-constant operand (`"unique_violation:" + constraint`);
//   - the internal reason `.internalReason` read into a response (the exact pre-C3 defect);
//   - anything else       a literal outside the field's closed set, an identifier or selector or call that
//     is not one of the field's named producers, an expression form the table does not know. The default
//     is NO: a new response field has no rule and is red until a reviewer writes its closed set here.
//
// Containers (`verification`, `from_alignment`, `results`) are checked at the sites that build them,
// which this same scan covers; raw writers (`w.Write`, `json.NewEncoder`, `fmt.Fprint`, `http.Error`)
// and a `writeJSON` whose body is not a literal are red, because they would put fields past the scan.
//
// WHAT IT IS NOT. A static scan cannot see a leak that arrives through a producer function the table
// names (`userVerifyCode`, `bulkRowCode`, ...): those are proven closed by their own tests, and the
// DB-backed test (response_closed_vocabulary_test.go) forces every internal failure path and asserts
// every string in the body against the closed sets. The scan and the dynamic test are the two halves;
// neither stands for the other. `message` (rendered copy, ledgered) and `smtp_response` (the provider's
// own reply) are the two documented non-code strings, each with an exact producer.
//
// ANTI-VACUITY. The real-tree test fails unless it found at least leakGateSinkFloor string sinks across
// both files, every rule in the table matched a real site (a stale rule widens what may be added
// silently), and the nine fixture controls below each produce a finding with their own reason while the
// clean fixture produces none. A one-hunk mutation of the real source (the fault proof) turns the
// real-tree test red.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// leakRule is what one response field may be set from.
type leakRule struct {
	literals   map[string]bool // string literals (and package constants) whose value is in the closed set
	anyLiteral bool            // any string literal (rendered copy: constants can never be error text)
	calls      map[string]bool // functions whose call result is allowed, matched on the final identifier
	selectors  map[string]bool // `x.Sel` allowed by Sel
	idents     map[string]bool // plain identifiers allowed by name (ids and containers built elsewhere in the scan)
	nonString  bool            // the field is not a string; the value is not classified
}

type leakFinding struct {
	file, key, reason string
	line              int
}

func (f leakFinding) String() string {
	return fmt.Sprintf("%s:%d field %q: %s", f.file, f.line, f.key, f.reason)
}

type leakScan struct {
	findings []leakFinding
	sinks    int            // response fields examined (string-typed or not)
	strSinks int            // of which classified as strings
	funcs    int            // function bodies scanned
	used     map[string]int // rule key -> how many sites matched it
}

// leakGateSinkFloor is the number of string sinks the real tree must yield (counted when the gate was
// built; a refactor that moves the fields out of these files makes the scan find nothing, and a scan
// that finds nothing proves nothing).
const leakGateSinkFloor = 40

func union(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			out[k] = true
		}
	}
	return out
}

// realLeakRules is the reviewed table: every response field on the two routes, with the closed set it
// is drawn from and the named producers allowed to supply it. The sets are the SAME maps the mappers in
// user_codes.go consult, so widening a set widens both and changing a mapper without the table is red.
func realLeakRules() map[string]leakRule {
	return map[string]leakRule{
		// --- map keys: the test button's two objects, and bulk-add's envelope --------------------------
		"state": {
			literals: union(verificationStateSet, alignmentStateSet),
			calls:    setOf("userVerificationState", "userAlignmentState"),
		},
		"code": {
			literals: union(verifyCodeSet, alignmentCodeSet),
			calls:    setOf("userVerifyCode", "userAlignmentCode"),
		},
		"error":          {literals: alignmentErrorSet},
		"message":        {anyLiteral: true, calls: setOf("RenderCopy")},
		"smtp_response":  {selectors: setOf("SMTPResponse")}, // the provider's own reply, scrubbed by the worker
		"verification":   {idents: setOf("verification")},    // container: its literals are scanned too
		"from_alignment": {idents: setOf("alignment")},       // container
		"request_id":     {idents: setOf("requestID")},
		"results":        {idents: setOf("results")},
		// --- the bulk-add row struct (bulkAddRowResult) --------------------------------------------------
		"Row":    {nonString: true},
		"Status": {literals: bulkRowStatusSet},
		// AccountID is a UUID the create path returned, never text
		"AccountID":    {idents: setOf("accIDStr")},
		"Verification": {literals: verificationStateSet, calls: setOf("userVerificationState")},
		"Code": {
			literals: union(bulkRejectCodeSet, verifyCodeSet),
			calls:    setOf("bulkRowCode", "userVerifyCode"),
		},
		"SMTPResponse":     {selectors: setOf("SMTPResponse")},
		"AlignmentState":   {literals: alignmentStateSet, calls: setOf("userAlignmentState")},
		"AlignmentMessage": {anyLiteral: true, calls: setOf("RenderCopy")},
		"Details":          {idents: setOf("details"), selectors: setOf("details")},
		// --- fieldDetail (the details[] of a validation rejection) -----------------------------------
		"Field": {literals: setOf("smtp_secret", "user_id", "accounts")},
		"Issue": {idents: setOf("issueMalformed", "issueRequired", "issueOutOfRange", "issueNotFound")},
	}
}

// realLeakStructs are the response struct types whose composite literals and field assignments are sinks.
func realLeakStructs() map[string]bool {
	return map[string]bool{"bulkAddRowResult": true, "fieldDetail": true}
}

func tailName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

// scanLeakSource scans one Go source file.
func scanLeakSource(filename string, src any, rules map[string]leakRule, structs map[string]bool) (*leakScan, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	scan := &leakScan{used: map[string]int{}}

	// package-level string constants (bulkAddStatusCreated, ...), resolvable when used as a value
	consts := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i < len(vs.Values) {
					if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if v, err := strconv.Unquote(bl.Value); err == nil {
							consts[n.Name] = v
						}
					}
				}
			}
		}
	}

	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		scan.funcs++
		defs := map[string][]ast.Expr{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, l := range as.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok {
					continue
				}
				switch {
				case len(as.Lhs) == len(as.Rhs):
					defs[id.Name] = append(defs[id.Name], as.Rhs[i])
				case len(as.Rhs) == 1:
					defs[id.Name] = append(defs[id.Name], as.Rhs[0])
				}
			}
			return true
		})
		for _, vs := range collectVarSpecs(fd.Body) {
			for i, n := range vs.Names {
				if i < len(vs.Values) {
					defs[n.Name] = append(defs[n.Name], vs.Values[i])
				}
			}
		}

		sink := func(pos token.Pos, key string, val ast.Expr) {
			line := fset.Position(pos).Line
			scan.sinks++
			rule, ok := rules[key]
			if !ok {
				scan.findings = append(scan.findings, leakFinding{filename, key, "has no rule — a new response field on a leak-gated route needs its closed set written into the gate's table by a reviewer", line})
				return
			}
			scan.used[key]++
			if !rule.nonString {
				scan.strSinks++
			}
			if reason := classifyLeakValue(val, rule, defs, consts, 0); reason != "" {
				scan.findings = append(scan.findings, leakFinding{filename, key, reason, line})
			}
		}

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				switch t := x.Type.(type) {
				case *ast.MapType:
					if kid, ok := t.Key.(*ast.Ident); ok && kid.Name == "string" {
						for _, el := range x.Elts {
							kv, ok := el.(*ast.KeyValueExpr)
							if !ok {
								continue
							}
							if bl, ok := kv.Key.(*ast.BasicLit); ok && bl.Kind == token.STRING {
								key, _ := strconv.Unquote(bl.Value)
								sink(kv.Pos(), key, kv.Value)
							}
						}
					}
				case *ast.Ident:
					if structs[t.Name] {
						for _, el := range x.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if kid, ok := kv.Key.(*ast.Ident); ok {
									sink(kv.Pos(), kid.Name, kv.Value)
								}
							}
						}
					}
				}
			case *ast.AssignStmt:
				if len(x.Lhs) != len(x.Rhs) {
					break
				}
				for i, l := range x.Lhs {
					switch lx := l.(type) {
					case *ast.IndexExpr:
						if bl, ok := lx.Index.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							key, _ := strconv.Unquote(bl.Value)
							sink(x.Pos(), key, x.Rhs[i])
						}
					case *ast.SelectorExpr:
						// `res.Code = v`: a sink when the selected name is a field some rule names AND the
						// receiver is a plain identifier (res, ...). Selector assignments to anything the
						// table does not name (e.g. `a.b = c` on a local) are not response fields.
						if _, isRule := rules[lx.Sel.Name]; isRule {
							if _, isIdent := lx.X.(*ast.Ident); isIdent {
								sink(x.Pos(), lx.Sel.Name, x.Rhs[i])
							}
						}
					}
				}
			case *ast.CallExpr:
				if reason := rawWriterReason(x); reason != "" {
					scan.findings = append(scan.findings, leakFinding{filename, "(writer)", reason, fset.Position(x.Pos()).Line})
				}
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "writeJSON" {
					if len(x.Args) == 3 {
						if _, isLit := x.Args[2].(*ast.CompositeLit); !isLit {
							scan.findings = append(scan.findings, leakFinding{filename, "(writeJSON)", "writeJSON is given a body that is not a literal map: its fields would escape the scan", fset.Position(x.Pos()).Line})
						}
					}
				}
			}
			return true
		})
	}
	return scan, nil
}

func collectVarSpecs(body *ast.BlockStmt) []*ast.ValueSpec {
	var out []*ast.ValueSpec
	ast.Inspect(body, func(n ast.Node) bool {
		if ds, ok := n.(*ast.DeclStmt); ok {
			if gd, ok := ds.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, sp := range gd.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok {
						out = append(out, vs)
					}
				}
			}
		}
		return true
	})
	return out
}

// rawWriterReason flags a call that would write a response without going through writeJSON's literal.
func rawWriterReason(c *ast.CallExpr) string {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if x, ok := sel.X.(*ast.Ident); ok {
		switch {
		case x.Name == "w" && (sel.Sel.Name == "Write" || sel.Sel.Name == "WriteString"):
			return "a raw ResponseWriter write: it bypasses the scan"
		case x.Name == "http" && sel.Sel.Name == "Error":
			return "http.Error writes a plain-text body: it bypasses the scan"
		case x.Name == "json" && sel.Sel.Name == "NewEncoder":
			return "json encoding to a writer outside writeJSON: it bypasses the scan"
		case x.Name == "fmt" && strings.HasPrefix(sel.Sel.Name, "Fprint"):
			return "fmt.Fprint writes outside writeJSON: it bypasses the scan"
		}
	}
	return ""
}

// classifyLeakValue returns "" when val may be assigned to a field governed by rule, else the reason.
func classifyLeakValue(val ast.Expr, rule leakRule, defs map[string][]ast.Expr, consts map[string]string, depth int) string {
	if depth > 6 {
		return "the value's definition chain is too deep to classify"
	}
	// 1. the forbidden forms, wherever they sit in the expression
	var bad string
	ast.Inspect(val, func(n ast.Node) bool {
		if bad != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "Error" && len(x.Args) == 0 {
					bad = "raw error text: `" + types.ExprString(x) + "` puts an error's own text into the response"
					return false
				}
				if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "fmt" || id.Name == "errors") {
					bad = "formatted text: `" + types.ExprString(x) + "` can carry an error through %v or %w into the response"
					return false
				}
			}
		case *ast.BinaryExpr:
			if x.Op == token.ADD && !(isConstOperand(x.X, consts) && isConstOperand(x.Y, consts)) {
				bad = "string concatenation with a non-constant operand: `" + types.ExprString(x) + "`"
				return false
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "internalReason" {
				bad = "the internal reason (`" + types.ExprString(x) + "`) is read into the response — it belongs in the log"
				return false
			}
		}
		return true
	})
	if bad != "" {
		return bad
	}
	if rule.nonString {
		return ""
	}
	// 2. the allowed forms
	switch e := val.(type) {
	case *ast.ParenExpr:
		return classifyLeakValue(e.X, rule, defs, consts, depth+1)
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return ""
		}
		v, _ := strconv.Unquote(e.Value)
		if rule.anyLiteral || rule.literals[v] {
			return ""
		}
		return fmt.Sprintf("the literal %q is not in the closed set for this field", v)
	case *ast.Ident:
		if rule.idents[e.Name] {
			return ""
		}
		if v, ok := consts[e.Name]; ok {
			if rule.anyLiteral || rule.literals[v] {
				return ""
			}
			return fmt.Sprintf("the constant %s = %q is not in the closed set for this field", e.Name, v)
		}
		if ds, ok := defs[e.Name]; ok {
			for _, d := range ds {
				if r := classifyLeakValue(d, rule, defs, consts, depth+1); r != "" {
					return fmt.Sprintf("identifier %q is defined from a value that is not allowed: %s", e.Name, r)
				}
			}
			return ""
		}
		return fmt.Sprintf("identifier %q is not a closed value or a named producer for this field (a parameter or an unresolved name)", e.Name)
	case *ast.CallExpr:
		if rule.calls[tailName(e.Fun)] {
			return ""
		}
		return fmt.Sprintf("a call to %s is not one of this field's producers", types.ExprString(e.Fun))
	case *ast.SelectorExpr:
		if rule.selectors[e.Sel.Name] {
			return ""
		}
		return fmt.Sprintf("`%s` is not an allowed source for this field", types.ExprString(e))
	}
	return fmt.Sprintf("an expression of kind %T is not an allowed form", val)
}

func isConstOperand(e ast.Expr, consts map[string]string) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		_, ok := consts[x.Name]
		return ok
	case *ast.BinaryExpr:
		return isConstOperand(x.X, consts) && isConstOperand(x.Y, consts)
	case *ast.ParenExpr:
		return isConstOperand(x.X, consts)
	}
	return false
}

// ---------------------------------------------------------------------------------------------
// The gate on the real tree
// ---------------------------------------------------------------------------------------------

var leakGateFiles = []string{"account_test_button.go", "account_bulk_add.go"}

func TestResponseLeakGate_RealTreeIsClean(t *testing.T) {
	rules := realLeakRules()
	structs := realLeakStructs()
	total := &leakScan{used: map[string]int{}}
	for _, name := range leakGateFiles {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("the gate cannot read %s: %v — a gate whose subject is missing is red, not skipped", name, err)
		}
		sc, err := scanLeakSource(name, src, rules, structs)
		if err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		total.findings = append(total.findings, sc.findings...)
		total.sinks += sc.sinks
		total.strSinks += sc.strSinks
		total.funcs += sc.funcs
		for k, n := range sc.used {
			total.used[k] += n
		}
	}
	for _, f := range total.findings {
		t.Errorf("LEAK GATE: %s", f)
	}
	if total.strSinks < leakGateSinkFloor {
		t.Fatalf("LEAK GATE VACUOUS: found %d string sinks across %v, the floor is %d — the scan is not seeing the response fields", total.strSinks, leakGateFiles, leakGateSinkFloor)
	}
	if total.funcs < 6 {
		t.Fatalf("LEAK GATE VACUOUS: scanned %d functions, want at least 6", total.funcs)
	}
	var stale []string
	for k := range rules {
		if total.used[k] == 0 {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("LEAK GATE: the table has rules that match no site (a stale rule widens what may be added without review): %v", stale)
	}
	t.Logf("leak gate: %d response fields examined (%d string-typed) in %d functions of %d files", total.sinks, total.strSinks, total.funcs, len(leakGateFiles))
}

// ---------------------------------------------------------------------------------------------
// The paired controls: each leak form, in a fixture, must be caught for ITS OWN reason; the clean
// fixture must pass. Without the clean one, "red" could mean "the scanner rejects everything".
// ---------------------------------------------------------------------------------------------

const leakFixtureHeader = `package fixture

import (
	"fmt"
	"net/http"
)

const stCreated = "created"

type bulkAddRowResult struct{ Code, Status string }

func writeJSON(w http.ResponseWriter, s int, v any) {}
`

func scanFixture(t *testing.T, body string) *leakScan {
	t.Helper()
	sc, err := scanLeakSource("fixture.go", leakFixtureHeader+body, realLeakRules(), realLeakStructs())
	if err != nil {
		t.Fatalf("fixture does not parse: %v\n%s", err, body)
	}
	return sc
}

func TestResponseLeakGate_CleanFixturePasses(t *testing.T) {
	sc := scanFixture(t, `
func ok(w http.ResponseWriter, cerr *apiError) {
	code := bulkRowCode(cerr)
	res := bulkAddRowResult{Status: stCreated, Code: code}
	res.Code = "validation_failed"
	verification := map[string]any{"state": userVerificationState("x"), "code": "verify_timeout"}
	verification["error"] = "from_check_not_saved"
	writeJSON(w, 200, map[string]any{"verification": verification})
}`)
	if len(sc.findings) != 0 {
		t.Fatalf("a clean fixture was reported: %v", sc.findings)
	}
	if sc.strSinks < 6 {
		t.Fatalf("the clean fixture yielded only %d string sinks — the scanner is not seeing them", sc.strSinks)
	}
}

func TestResponseLeakGate_EachLeakFormIsCaughtForItsOwnReason(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"err.Error() in a map value", `func f(err error) { _ = map[string]any{"error": err.Error()} }`, "raw error text"},
		{"err.Error() through an index assignment", `func f(err error) { m := map[string]any{}; m["code"] = err.Error() }`, "raw error text"},
		{"err.Error() reached through a local", `func f(err error) { s := err.Error(); _ = map[string]any{"code": s} }`, "raw error text"},
		{"fmt.Sprintf of an error", `func f(err error) { _ = map[string]any{"code": fmt.Sprintf("x: %v", err)} }`, "formatted text"},
		{"fmt.Errorf with %w", `func f(err error) { _ = map[string]any{"code": fmt.Errorf("x: %w", err)} }`, "formatted text"},
		{"concatenation with a constraint name", `func f(c string) { _ = bulkAddRowResult{Code: "unique_violation:" + c} }`, "string concatenation"},
		{"the internal reason", `func f(e *apiError) { _ = bulkAddRowResult{Code: e.internalReason} }`, "the internal reason"},
		{"a literal outside the closed set", `func f() { _ = bulkAddRowResult{Code: "tx_begin_error"} }`, "not in the closed set"},
		{"a parameter", `func f(s string) { _ = map[string]any{"code": s} }`, "not a closed value"},
		{"a call that is not a producer", `func f() { _ = bulkAddRowResult{Code: somethingElse()} }`, "is not one of this field's producers"},
		{"a new field with no rule", `func f() { _ = map[string]any{"detail": "x"} }`, "has no rule"},
		{"a raw writer", `func f(w http.ResponseWriter, err error) { w.Write([]byte(err.Error())) }`, "raw ResponseWriter write"},
		{"writeJSON with a non-literal body", `func f(w http.ResponseWriter, b map[string]any) { writeJSON(w, 200, b) }`, "not a literal map"},
	}
	if len(cases) < 13 {
		t.Fatal("fewer than 13 leak-form controls")
	}
	for _, c := range cases {
		sc := scanFixture(t, c.body)
		hit := false
		for _, f := range sc.findings {
			if strings.Contains(f.reason, c.want) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("CONTROL FAILED (%s): the gate did not report %q; findings: %v", c.name, c.want, sc.findings)
		}
	}
}

// The scanner is also red on the exact pre-C3 construct: the row code assigned from the create
// path's internal reason.
func TestResponseLeakGate_PreC3BulkRowCodeIsRed(t *testing.T) {
	sc := scanFixture(t, `func f(cerr *apiError) bulkAddRowResult { return bulkAddRowResult{Status: stCreated, Code: cerr.internalReason} }`)
	if len(sc.findings) == 0 {
		t.Fatal("the pre-C3 `Code: cerr.internalReason` was not reported")
	}
}

// ---------------------------------------------------------------------------------------------
// The mappers are closed: unknown or hostile input never comes back out.
// ---------------------------------------------------------------------------------------------

const leakCanary = "CANARY-hunter2-198.51.100.7-db.internal.example.test-pg:5432"

func TestUserCodeMappers_AreClosed(t *testing.T) {
	hostile := []string{"", leakCanary, "unique_violation:" + leakCanary, "tx_begin_error: " + leakCanary,
		"verify_auth_failed ", "VERIFY_AUTH_FAILED", "from_gate_write_error", "\x00", strings.Repeat("a", 5000)}
	for _, in := range hostile {
		if c := userVerifyCode(in); !verifyCodeSet[c] {
			t.Errorf("userVerifyCode(%q) = %q, not in the closed set", in, c)
		}
		if c := userVerificationState(in); !verificationStateSet[c] {
			t.Errorf("userVerificationState(%q) = %q, not in the closed set", in, c)
		}
		if c := userAlignmentState(in); !alignmentStateSet[c] {
			t.Errorf("userAlignmentState(%q) = %q, not in the closed set", in, c)
		}
		if c := userAlignmentCode(in); !alignmentCodeSet[c] {
			t.Errorf("userAlignmentCode(%q) = %q, not in the closed set", in, c)
		}
		for _, code := range []string{codeValidationFailed, codeConflict, codeUnprocessable, codeNotFound, codeInternalError, codeRateLimited, ""} {
			if c := bulkRowCode(newErr(code, in)); !bulkRejectCodeSet[c] {
				t.Errorf("bulkRowCode(code=%q, reason=%q) = %q, not in the closed set", code, in, c)
			}
		}
	}
	// Positive controls: the values the contract has always documented are kept as-is.
	for _, keep := range []string{"account_cap_reached", "duplicate_account_for_user", "from_alignment_warning_unacknowledged"} {
		if c := bulkRowCode(newErr(codeConflict, keep)); c != keep {
			t.Errorf("the documented row code %q was changed to %q", keep, c)
		}
	}
	for _, keep := range []string{"verify_connect_failed", "verify_timeout", "verify_tls_unavailable", "verify_tls_failed",
		"verify_auth_failed", "verify_protocol_error", "verify_connection_lost"} {
		if c := userVerifyCode(keep); c != keep {
			t.Errorf("the documented verify code %q was changed to %q", keep, c)
		}
	}
	// the raw slugs the leak used to carry are NOT in any closed set
	for _, raw := range []string{"tx_begin_error", "account_write_error", "unique_violation", "check_violation", "fk_violation",
		"encrypt_error", "uuid_generate_error", "from_gate_write_error", "from_gate_acknowledgment_write_error", "advisory_lock_failed"} {
		for name, set := range map[string]map[string]bool{"verify": verifyCodeSet, "bulk": bulkRejectCodeSet, "alignment error": alignmentErrorSet} {
			if set[raw] {
				t.Errorf("the internal slug %q is in the closed %s set", raw, name)
			}
		}
	}
	// A rejected row's mapping by external code (the four that name a class), and the generic fallback.
	cases := map[string]string{codeValidationFailed: "validation_failed", codeConflict: "conflict",
		codeUnprocessable: "unprocessable", codeNotFound: "not_found", codeInternalError: "internal_error"}
	for in, want := range cases {
		if got := bulkRowCode(newErr(in, "unique_violation:"+leakCanary)); got != want {
			t.Errorf("bulkRowCode(external %q) = %q, want %q", in, got, want)
		}
	}
}

// The verify_* codes in the closed set are exactly the literals dispatch.externalCodeFor returns (plus
// the two this surface adds), so a code added to the worker mapping cannot be silently dropped to
// verify_error here, and one removed from it cannot linger.
func TestUserCodeMappers_VerifyCodesMatchDispatch(t *testing.T) {
	src, err := os.ReadFile("../dispatch/verify.go")
	if err != nil {
		t.Fatalf("cannot read dispatch/verify.go: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "verify.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "externalCodeFor" {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if rs, ok := n.(*ast.ReturnStmt); ok && len(rs.Results) > 0 {
				if bl, ok := rs.Results[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					v, _ := strconv.Unquote(bl.Value)
					got[v] = true
				}
			}
			return true
		})
	}
	if len(got) < 7 {
		t.Fatalf("found only %d codes in dispatch.externalCodeFor (%v) — the parse is wrong", len(got), got)
	}
	want := map[string]bool{}
	for c := range verifyCodeSet {
		if c != codeVerifyError && c != codeVerificationUnavail {
			want[c] = true
		}
	}
	for c := range got {
		if !want[c] {
			t.Errorf("dispatch.externalCodeFor returns %q, which is not in verifyCodeSet — it would be reported as verify_error", c)
		}
	}
	for c := range want {
		if !got[c] {
			t.Errorf("verifyCodeSet lists %q, which dispatch.externalCodeFor never returns", c)
		}
	}
}
