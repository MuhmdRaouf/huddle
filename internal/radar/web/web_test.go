package web

import (
	"io/fs"
	"testing"
)

// FS() serves the built dashboard with dist/ as its root, so index.html and every
// asset sit at the names the server hands to browsers.
func TestEmbedsTheBuiltDashboard(t *testing.T) {
	t.Run("index.html is reachable and non-empty", func(t *testing.T) {
		data, err := fs.ReadFile(FS(), "index.html")
		if err != nil {
			t.Fatalf("read index.html: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("index.html is empty")
		}
	})

	t.Run("every file under dist is reachable and non-empty", func(t *testing.T) {
		seen := 0
		err := fs.WalkDir(FS(), ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			seen++
			data, err := fs.ReadFile(FS(), name)
			if err != nil {
				t.Errorf("read %s: %v", name, err)
				return nil
			}
			if len(data) == 0 {
				t.Errorf("%s is empty", name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen == 0 {
			t.Fatal("dist holds no files")
		}
	})
}
