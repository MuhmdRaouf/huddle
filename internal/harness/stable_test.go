package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// writeBin writes one executable file, the way an install lands the binary.
func writeBin(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// resolvedHome answers the home as StableBin sees it: symlinks resolved, so a macOS temp dir's
// /var spelling never disagrees with the /private/var one the binary resolves to.
func resolvedHome(t *testing.T) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	bin = filepath.Join(home, "0.0.1", "huddle")
	writeBin(t, bin)
	return home, bin
}

// TestStableBinPrefersTheLaunchersCurrentLink pins the binary path an entry names: the launcher's
// current link when it sits beside the versioned binary — the path that survives an upgrade —
// else the binary's own resolved path.
func TestStableBinPrefersTheLaunchersCurrentLink(t *testing.T) {
	home, bin := resolvedHome(t)
	if got := StableBin(bin); got != bin {
		t.Fatalf("StableBin with no current link = %q, want the resolved path %q", got, bin)
	}

	// the launcher's link: a verified install re-points it at every new version
	if err := os.Symlink(filepath.Join("0.0.1", "huddle"), filepath.Join(home, "current")); err != nil {
		t.Fatal(err)
	}
	if got := StableBin(bin); got != filepath.Join(home, "current") {
		t.Fatalf("StableBin beside a current link = %q, want %q", got, filepath.Join(home, "current"))
	}

	// invoked through the link itself: the same answer
	if got := StableBin(filepath.Join(home, "current")); got != filepath.Join(home, "current") {
		t.Fatalf("StableBin through the link = %q, want %q", got, filepath.Join(home, "current"))
	}
}

// TestStableBinFallsBackWhenCurrentIsNotABinary keeps the fallback honest: a link that resolves to
// nothing (a version just replaced) and a stray directory of that name are not the binary, and the
// resolved path is written instead.
func TestStableBinFallsBackWhenCurrentIsNotABinary(t *testing.T) {
	home, bin := resolvedHome(t)

	if err := os.Symlink(filepath.Join("0.0.2", "huddle"), filepath.Join(home, "current")); err != nil {
		t.Fatal(err)
	}
	if got := StableBin(bin); got != bin {
		t.Fatalf("StableBin beside a dangling link = %q, want %q", got, bin)
	}

	if err := os.Remove(filepath.Join(home, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "current"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := StableBin(bin); got != bin {
		t.Fatalf("StableBin beside a directory named current = %q, want %q", got, bin)
	}
}
