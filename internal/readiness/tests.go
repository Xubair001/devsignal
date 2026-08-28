package readiness

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// TestIndex is every test function in the repository, by package.name.
//
// Built so a "covered by X" claim is FALSIFIABLE. Without it the gate's
// test-backed lines are an assertion in a comment: they would keep reporting
// covered after the test was renamed or deleted, which is the exact failure mode
// a launch checklist must not have. With it, deleting the test fails the gate.
type TestIndex map[string]bool

// IndexTests walks the repository for Go test functions.
//
// A source parse rather than running anything: this command must be safe to run
// against production, and `go test` is not. What it proves is narrower than "the
// tests pass" and is stated as such — the test exists and CI runs the suite.
func IndexTests(root string) (TestIndex, error) {
	idx := TestIndex{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// node_modules holds an npm dependency that ships Go, and vendored
			// trees are not ours.
			switch d.Name() {
			case "node_modules", ".git", "dist", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// Deliberately swallowed. A file that does not parse is not evidence
			// about any readiness line, and failing the whole gate because an
			// unrelated test file has a syntax error would make the gate the
			// thing that broke. The consequence is bounded and safe in the right
			// direction: a test that cannot be parsed is not indexed, so its line
			// reports as NOT covered rather than as covered.
			//nolint:nilerr // see above: an unparseable file must not fail the gate
			return nil
		}
		pkg := strings.TrimSuffix(file.Name.Name, "_test")
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			idx[pkg+"."+fn.Name.Name] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("readiness: indexing tests: %w", err)
	}
	return idx, nil
}

// checkCoverage verifies every test named on a line exists.
func (idx TestIndex) checkCoverage(l Line) Result {
	names := strings.Split(l.CoveredBy, ",")
	var missing []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !idx[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return Result{Line: l, Status: StatusFail,
			Detail: fmt.Sprintf("the test named as covering this does not exist: %v. "+
				"Either it was renamed — update the gate — or the coverage was lost.",
				missing)}
	}
	return Result{Line: l, Status: StatusPass,
		Detail: "covered by " + l.CoveredBy + ", which CI runs on every push. " +
			"This checks the test EXISTS, not that it passed just now — `make test` " +
			"and the CI run are what settle that."}
}
