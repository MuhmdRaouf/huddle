package server

// The health answer names the serving process's executable, symlinks resolved: up from a newer
// binary reads it to give an older hub way (the router's start applies the same rule to its
// daemon), so a hub that cannot name its binary counts as an old one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTheHealthAnswerNamesItsExecutable(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/health", nil)
	if code != 200 {
		t.Fatalf("/health = %d %s", code, body)
	}
	var answer struct {
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the health answer %s: %v", body, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Executable != resolved {
		t.Errorf("health names executable %q, want %q", answer.Executable, resolved)
	}
}
