package tsql

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// queryHead is the start of a string literal that is a query, or the first
// piece of one: a statement the Query Store or the plan cache could capture.
// A fragment appended to a statement (" WITH (...)") starts with a space and
// is not one.
var queryHead = regexp.MustCompile(`^[\r\n\t]*(SELECT|WITH|INSERT|UPDATE|DELETE|MERGE|DECLARE)\s`)

// TestEveryQueryCarriesMarker reads every string literal of goDTA's code
// (the godta packages and the command, tests and test helpers left out) and
// fails on a query that does not carry Marker: collect would read it back
// from the Query Store as part of the workload. The replay package is left
// out: what it builds is the workload's own text, sent under SHOWPLAN_XML or
// AUTOPILOT, which compiles it without running it.
func TestEveryQueryCarriesMarker(t *testing.T) {
	var tagged int
	for _, root := range []string{"..", filepath.Join("..", "..", "..", "cmd", "godta")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "ittest" || d.Name() == "replay" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !queryHead.MatchString(s) {
					return true
				}
				if !strings.Contains(s, Marker) {
					t.Errorf("%s: query without %q: %.80q", fset.Position(lit.Pos()), Marker, s)
					return true
				}
				tagged++
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The scan reads the tree it expects: collect alone has more than 20
	// queries. Fewer means the roots are wrong and the test checks nothing.
	if tagged < 30 {
		t.Errorf("%d tagged queries found, want at least 30: the scan does not read goDTA's code", tagged)
	}
}
