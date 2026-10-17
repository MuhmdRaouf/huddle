package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// FS() serves the built board page with dist/ as its root, so index.html and
// every asset sit at the names the server hands to browsers.
func TestEmbedsTheBuiltBoardPage(t *testing.T) {
	t.Run("index.html is the built page, not the placeholder", func(t *testing.T) {
		data, err := fs.ReadFile(FS(), "index.html")
		if err != nil {
			t.Fatalf("read index.html: %v", err)
		}
		page := string(data)
		if !strings.Contains(page, `<div id="root">`) {
			t.Error("index.html lacks the mount point div #root")
		}
		if !strings.Contains(page, `<script type="module"`) {
			t.Error("index.html lacks the module script that boots the page")
		}
	})

	t.Run("every asset index.html references is in the FS", func(t *testing.T) {
		page, err := fs.ReadFile(FS(), "index.html")
		if err != nil {
			t.Fatalf("read index.html: %v", err)
		}
		referenced := regexp.MustCompile(`(?:src|href)="([^"]+)"`)
		matches := referenced.FindAllStringSubmatch(string(page), -1)
		if len(matches) == 0 {
			t.Fatal("index.html references no asset")
		}
		for _, match := range matches {
			name := strings.TrimPrefix(match[1], "./")
			if strings.Contains(name, ":") || !strings.Contains(name, "/") {
				continue // an absolute URL or an inline value, not a built asset
			}
			asset, err := fs.ReadFile(FS(), name)
			if err != nil {
				t.Errorf("read %s: %v", name, err)
				continue
			}
			if len(asset) == 0 {
				t.Errorf("%s is empty", name)
			}
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
