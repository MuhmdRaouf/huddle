package server

// The owner key: the run's root credential, left in the home's run directory for the CLI of the
// same machine (cli's creds.go) when no one handed the root over — a service manager's serve, a
// terminal's — written 0600 inside the 0700 run directory through a temp file and a rename,
// never said in the log, and taken away by a clean exit.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// serveUntilPort runs Serve in-process until it says its port, both outputs in log; the answer
// ends the server cleanly.
func serveUntilPort(t *testing.T, o Options, log *syncBuffer) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, o) }()
	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the server never stopped")
		}
	}
	said := regexp.MustCompile(`on http://[\d.]+:(\d+)`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if said.MatchString(log.joined()) {
			return stop
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	t.Fatalf("the server never said its port: %s", log.joined())
	return func() {}
}

func TestServeLeavesTheOwnerKeyAndACleanExitTakesIt(t *testing.T) {
	homeDir := t.TempDir()
	log := &syncBuffer{}
	o := Options{
		Version: "t", Host: "127.0.0.1", Port: 0, PortNamed: true,
		Home: homeDir, Data: homeDir, Token: "hcred_the_owners_root",
		Stdout: log, Stderr: log,
	}
	stop := serveUntilPort(t, o, log)
	key := filepath.Join(homeDir, "run", "owner.key")
	var body string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(key); err == nil {
			body = strings.TrimSpace(string(raw))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body != "hcred_the_owners_root" {
		stop()
		t.Fatalf("run/owner.key = %q, want the run's root credential", body)
	}
	info, err := os.Stat(key)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("owner.key mode %o, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(key))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("the run directory mode %o, want 0700", dir.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(key))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Errorf("the run directory keeps %s: the write was not atomic", entry.Name())
		}
	}
	if strings.Contains(log.joined(), "hcred_the_owners_root") {
		t.Errorf("the server's output says the root credential")
	}
	stop()
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Errorf("owner.key after a clean exit: %v, want it gone", err)
	}
}

func TestAServeThatCannotWriteTheOwnerKeySaysSoAndServesOn(t *testing.T) {
	homeDir := t.TempDir()
	// a run that is a file: the key cannot be laid down
	if err := os.WriteFile(filepath.Join(homeDir, "run"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	o := Options{
		Version: "t", Host: "127.0.0.1", Port: 0, PortNamed: true,
		Home: homeDir, Data: homeDir, Token: "hcred_the_owners_root",
		Stdout: log, Stderr: log,
	}
	stop := serveUntilPort(t, o, log)
	defer stop()
	if !strings.Contains(log.joined(), "owner key") {
		t.Errorf("the log says %q, want the refused owner key line", log.joined())
	}
	if strings.Contains(log.joined(), "hcred_the_owners_root") {
		t.Errorf("the refusal line says the root credential")
	}
}
