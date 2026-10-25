package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// A `go` statement inside internal/huddle is a goroutine that can end the hub on a panic;
// safego.Go is the one way to start one.
func TestHuddleGoroutinesStartThroughSafego(t *testing.T) {
	var sites []string
	err := filepath.WalkDir(filepath.Join("..", "..", "huddle"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		slash := filepath.ToSlash(p)
		if strings.Contains(slash, "/web/") || strings.Contains(slash, "/testdata/") {
			return nil
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel := "internal/huddle/" + strings.TrimPrefix(slash, "../../huddle/")
		ast.Inspect(parsed, func(n ast.Node) bool {
			if _, ok := n.(*ast.GoStmt); ok {
				sites = append(sites, fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) > 0 {
		t.Errorf("%d go statements under internal/huddle start a goroutine outside safego.Go:\n%s",
			len(sites), strings.Join(sites, "\n"))
	}
}
