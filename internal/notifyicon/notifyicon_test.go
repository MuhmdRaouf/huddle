package notifyicon

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/web"
)

// embeddedPNG is the dashboard's icon as built, the bytes EnsurePNG writes.
func embeddedPNG(t *testing.T) []byte {
	t.Helper()
	data, err := fs.ReadFile(web.FS(), "icons/icon-192.png")
	if err != nil {
		t.Fatalf("read icons/icon-192.png from the built dashboard: %v", err)
	}
	return data
}

func TestEnsurePNG(t *testing.T) {
	t.Run("writes the dashboard's icon under the huddle home and answers its path", func(t *testing.T) {
		home := t.TempDir()
		want := filepath.Join(home, ".agents", "huddle", "huddle.png")
		path, err := EnsurePNG(home)
		if err != nil {
			t.Fatalf("EnsurePNG: %v", err)
		}
		if path != want || PNGPath(home) != want || Dir(home) != filepath.Dir(want) {
			t.Errorf("EnsurePNG = %q, Dir = %q, PNGPath = %q, want %q", path, Dir(home), PNGPath(home), want)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the icon: %v", err)
		}
		if !bytes.Equal(got, embeddedPNG(t)) {
			t.Error("the written icon is not icons/icon-192.png's bytes")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	})

	t.Run("a second call leaves the file alone", func(t *testing.T) {
		home := t.TempDir()
		path, err := EnsurePNG(home)
		if err != nil {
			t.Fatal(err)
		}
		when := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsurePNG(home); err != nil {
			t.Fatalf("second EnsurePNG: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(when) {
			t.Errorf("the second call rewrote the file: mtime %v, want %v", info.ModTime(), when)
		}
	})

	t.Run("a file holding other bytes is replaced", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(Dir(home), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(PNGPath(home), []byte("not the icon"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsurePNG(home); err != nil {
			t.Fatalf("EnsurePNG: %v", err)
		}
		got, err := os.ReadFile(PNGPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, embeddedPNG(t)) {
			t.Error("the icon was not replaced with the dashboard's bytes")
		}
	})

	t.Run("an empty home is an error and writes nothing", func(t *testing.T) {
		t.Chdir(t.TempDir())
		path, err := EnsurePNG("")
		if err == nil {
			t.Fatalf(`EnsurePNG("") = %q, want an error`, path)
		}
		if path != "" {
			t.Errorf(`EnsurePNG("") = %q, want an empty path`, path)
		}
		if _, err := os.Stat(".agents"); !os.IsNotExist(err) {
			t.Errorf(".agents exists after EnsurePNG(%q): %v", "", err)
		}
	})

	t.Run("no temp file is left behind", func(t *testing.T) {
		home := t.TempDir()
		if _, err := EnsurePNG(home); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(Dir(home))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "huddle.png" {
			names := make([]string, len(entries))
			for i, entry := range entries {
				names[i] = entry.Name()
			}
			t.Errorf("the icon dir holds %v, want only huddle.png", names)
		}
	})

	t.Run("a home whose huddle folder cannot be made is an error", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, ".agents"), []byte("a file, not a folder"), 0o600); err != nil {
			t.Fatal(err)
		}
		if path, err := EnsurePNG(home); err == nil {
			t.Fatalf("EnsurePNG = %q, want an error", path)
		}
	})

	t.Run("a temp file that cannot be written is an error and the icon is not touched", func(t *testing.T) {
		home := t.TempDir()
		// A folder where this process's temp file belongs.
		tmp := fmt.Sprintf("%s.%d.tmp", PNGPath(home), os.Getpid())
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		if path, err := EnsurePNG(home); err == nil {
			t.Fatalf("EnsurePNG = %q, want an error", path)
		}
		if _, err := os.Stat(PNGPath(home)); !os.IsNotExist(err) {
			t.Errorf("the icon after a failed write: %v, want it missing", err)
		}
	})

	t.Run("an icon path that cannot be replaced is an error and no temp file is left", func(t *testing.T) {
		home := t.TempDir()
		// A folder holding a file where the icon belongs: a file cannot be renamed over it.
		if err := os.MkdirAll(filepath.Join(PNGPath(home), "in-the-way"), 0o700); err != nil {
			t.Fatal(err)
		}
		if path, err := EnsurePNG(home); err == nil {
			t.Fatalf("EnsurePNG = %q, want an error", path)
		}
		entries, err := os.ReadDir(Dir(home))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != "huddle.png" {
				t.Errorf("the icon dir holds %q after a failed replace, want only huddle.png", entry.Name())
			}
		}
	})
}
