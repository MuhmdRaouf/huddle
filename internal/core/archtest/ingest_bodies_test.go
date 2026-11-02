package archtest

// The ingest guard: the ingester reads transcripts, spool lines and harness files into the
// ledger; the request bodies are the router daemon's store, reached only through its tap.
// No ingest file may name the body store or its opener, so request content never enters the
// ingest path.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bodySymbols are the ledger package's body-store identifiers no ingest file may use: the
// store's type and the constructor that opens it.
var bodySymbols = []string{"BodyStore", "OpenBodies"}

func TestIngestNeverOpensBodies(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "..", "radar", "ingest")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if imp.Name != nil && imp.Name.Name == "." {
				t.Errorf("%s: a dot import hides which package a name comes from", entry.Name())
			}
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			for _, symbol := range bodySymbols {
				if sel.Sel.Name == symbol {
					pos := fset.Position(sel.Pos())
					t.Errorf("%s:%d: ingest must not use ledger.%s", entry.Name(), pos.Line, symbol)
				}
			}
			return true
		})
	}
}

// TestIngestGuardWalksRealFiles proves the guard's directory holds Go files, so the scan
// cannot pass vacuously over an empty or moved package.
func TestIngestGuardWalksRealFiles(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "..", "radar", "ingest")
	found := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			found++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no Go files under internal/radar/ingest; the guard has nothing to scan")
	}
}
