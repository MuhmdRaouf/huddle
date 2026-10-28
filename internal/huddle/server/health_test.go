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

func TestTheHealthAnswerListsTheComponents(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/health", nil)
	if code != 200 {
		t.Fatalf("/health = %d %s", code, body)
	}
	var answer struct {
		Healthy    bool              `json:"healthy"`
		Components []json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the health answer %s: %v", body, err)
	}
	if !answer.Healthy {
		t.Errorf("healthy = false, want true on a server with nothing failing")
	}
	if answer.Components == nil {
		t.Errorf("components = null, want an empty array")
	}
	if len(answer.Components) != 0 {
		t.Errorf("components = %s, want none before any part reports", answer.Components)
	}
}

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
