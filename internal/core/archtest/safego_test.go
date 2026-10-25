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

// daemonRoots are the packages the two daemons run: the hub, huddle-router and the hub's
// in-process Radar and Kanban servers. dir is the walk root relative to this package, path is
// its module path.
var daemonRoots = []struct {
	dir  string
	path string
}{
	{"../../huddle", "internal/huddle"},
	{"../../routerd", "internal/routerd"},
	{"../router", "internal/core/router"},
	{"../../radar/server", "internal/radar/server"},
	{"../../kanban/server", "internal/kanban/server"},
}

// A `go` statement in a daemon's package is a goroutine that can end the daemon on a panic;
// safego.Go is the one way to start one.
func TestDaemonGoroutinesStartThroughSafego(t *testing.T) {
	var sites []string
	for _, root := range daemonRoots {
		err := filepath.WalkDir(root.dir, func(p string, d fs.DirEntry, err error) error {
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
			rel := root.path + "/" + strings.TrimPrefix(slash, root.dir+"/")
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
	}
	if len(sites) > 0 {
		t.Errorf("%d go statements under the daemons' packages start a goroutine outside safego.Go:\n%s",
			len(sites), strings.Join(sites, "\n"))
	}
}
