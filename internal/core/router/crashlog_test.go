// The router log: direct tests of every case.
package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantPrivateFile pins a file's 0600.
func wantPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %o, want 600", filepath.Base(path), info.Mode().Perm())
	}
}

// failingLogFs is a LogFs whose every operation fails, an ENOSPC stand-in.
type failingLogFs struct{}

func (failingLogFs) Append(string, string) error { return errors.New("no space") }
func (failingLogFs) Size(string) (int64, error)  { return 0, errors.New("no stat") }
func (failingLogFs) Read(string) ([]byte, error) { return nil, errors.New("no read") }
func (failingLogFs) Write(string, []byte) error  { return errors.New("no write") }
func (failingLogFs) Rename(string, string) error { return errors.New("no rename") }

func TestRotatingLog(t *testing.T) {
	t.Parallel()
	t.Run("rotates to .1 past its limit and never throws, not even on a full disk", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "router.log")
		log := RotatingLog(path, nil, 200)
		// A line with an astral pair and one with an invalid byte: the length the rotation counts is
		// the text's UTF-16 code units.
		log("\U0001F600 boots \xff")
		for i := 0; i < 10; i++ {
			log(fmt.Sprintf("request %d %s", i, strings.Repeat("x", 20)))
		}
		if _, err := os.Stat(path + ".1"); err != nil {
			t.Fatalf("the log did not rotate: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 200 {
			t.Fatalf("the log grew to %d bytes, want at most 200", info.Size())
		}
		RotatingLog(path, failingLogFs{}, 10)("line")
	})

	t.Run("does not bring back a directory that was removed", func(t *testing.T) {
		root := t.TempDir()
		RotatingLog(filepath.Join(root, "gone", "router.log"), nil, 0)("line")
		if _, err := os.Stat(filepath.Join(root, "gone")); !os.IsNotExist(err) {
			t.Fatalf("logging brought the directory back: %v", err)
		}
	})
}
