package report

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenTimeNames are the time-package members that make a goroutine wake on its own.
var forbiddenTimeNames = map[string]bool{
	"Ticker":    true,
	"NewTicker": true,
	"Tick":      true,
	"AfterFunc": true,
	"NewTimer":  true,
}

// TestPackageHasNoNetworkAndNoTimer pins the rule for the whole insights/report tree: it builds
// and stores reports on demand, never listens, never polls and never wakes on a timer.
func TestPackageHasNoNetworkAndNoTimer(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "net/http" || strings.HasPrefix(p, "net/http/") {
				t.Errorf("%s imports %s", path, p)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && forbiddenTimeNames[sel.Sel.Name] {
				t.Errorf("%s uses time.%s", path, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
