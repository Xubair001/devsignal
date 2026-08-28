package telemetry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// allowedLabels is hard rule 12's list, verbatim.
//
// Every value in each of these has a small fixed set: a chi route TEMPLATE
// (never the raw path), a status CLASS (never the exact code), a stage name, a
// status, a source id — hundreds, and per-source health is the entire point of
// having it — and a model id.
var allowedLabels = map[string]string{
	"http.route":                 "the chi route template, never the raw path",
	"http.request.method":        "one of eight verbs",
	"http.response.status_class": "2xx/4xx/5xx, never the exact code",
	"pipeline.stage":             "a fixed set of stage names",
	"status":                     "a fixed set of outcomes",
	"source_id":                  "hundreds; per-source health is the point",
	"model_id":                   "a handful",
	"lane":                       "hot or cold",
}

// forbiddenLabels are the ones that take a metrics backend down.
//
// One time series per opportunity is unbounded cardinality, and it fails long
// after the code that caused it shipped — by which time the dashboard is the
// thing that broke rather than the deploy. These belong in trace attributes and
// log fields, which are sampled and indexed for exactly that purpose.
var forbiddenLabels = []string{
	"user_id", "opportunity_id", "email", "session", "token",
	"http.target", "url.path", "http.url", "resume_id", "profile_id",
}

// TestMetricLabelsAreBounded parses this package and checks every label key
// passed to attribute.String against hard rule 12.
//
// A source-level check rather than a runtime one, because the failure it guards
// is a NEW call site added later — a runtime assertion only sees the code paths a
// test happens to exercise, and the one that gets this wrong is always the one
// nobody wrote a test for.
func TestMetricLabelsAreBounded(t *testing.T) {
	fset := token.NewFileSet()
	// Explicit walk rather than parser.ParseDir, which is deprecated because it
	// ignores build tags. Every non-test .go file in this package is checked, so
	// a label added behind a tag is checked too.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	{
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
				strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, perr := parser.ParseFile(fset, name, nil, 0)
			if perr != nil {
				t.Fatalf("%s: %v", name, perr)
			}

			// Resource attributes are not metric labels and carry no cardinality
			// risk: they are attached ONCE to the whole SDK, so every series
			// carries them identically. Their positions are collected first and
			// their subtrees skipped, rather than being allow-listed by name —
			// a name-based exception would also excuse a genuinely unbounded
			// label that happened to share the name.
			inResource := map[token.Pos]bool{}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok || pkgIdent.Name != "resource" {
					return true
				}
				ast.Inspect(call, func(inner ast.Node) bool {
					if c, ok := inner.(*ast.CallExpr); ok {
						inResource[c.Pos()] = true
					}
					return true
				})
				return true
			})

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "attribute" {
					return true
				}
				if inResource[call.Pos()] {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					// A non-literal label key cannot be checked here, and a
					// computed one is exactly how an id gets in.
					t.Errorf("%s: metric label key is not a string literal, so its "+
						"cardinality cannot be checked", fset.Position(call.Pos()))
					return true
				}
				key, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				found[key] = true

				if _, allowed := allowedLabels[key]; !allowed {
					t.Errorf("%s: metric label %q is not on hard rule 12's list.\n"+
						"  Allowed: %v\n"+
						"  If this is genuinely bounded, add it to allowedLabels with "+
						"a note saying how many values it can take. If it identifies a "+
						"user or a posting it belongs in a trace attribute, not a label.",
						fset.Position(call.Pos()), key, keysOf(allowedLabels))
				}
				for _, bad := range forbiddenLabels {
					if strings.Contains(strings.ToLower(key), bad) {
						t.Errorf("%s: metric label %q contains %q — one series per "+
							"entity is unbounded cardinality and takes the metrics "+
							"backend down long after this shipped",
							fset.Position(call.Pos()), key, bad)
					}
				}
				return true
			})
		}
	}

	if len(found) == 0 {
		t.Fatal("no metric labels found; this test is not checking anything")
	}
	t.Logf("checked %d distinct metric label keys", len(found))
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
