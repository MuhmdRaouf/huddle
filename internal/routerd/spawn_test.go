package routerd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestSpawnServeRefusesATestBinary(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "start-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	// The marker proves nothing ran: the stub would create it if it were started.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	stub := filepath.Join(dir, "routerd.test")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = spawnServeArgs(stub, map[string]string{"HOME": dir}, log)
	if err == nil || !strings.Contains(err.Error(), "test binary") {
		t.Fatalf("spawn of a .test binary = %v, want the test-binary refusal", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the test binary was started")
	}
}

func TestEnvironDropsRouterProxies(t *testing.T) {
	env := domain.EnvLookup{
		"HUDDLE_ROUTER_PORT": "20999",
		"HTTPS_PROXY":        "http://127.0.0.1:20999",
		"https_proxy":        "127.0.0.1:" + strconv.Itoa(domain.HuddleRouterPort),
		"HTTP_PROXY":         "http://proxy.corp:3128",
		"NO_PROXY":           "corp",
		"ZAI_API_KEY":        "k",
	}
	got := environ(env)
	joined := strings.Join(got, "\n")
	for _, dropped := range []string{"HTTPS_PROXY=http://127.0.0.1:20999", "https_proxy=127.0.0.1:" + strconv.Itoa(domain.HuddleRouterPort)} {
		if strings.Contains(joined, dropped) {
			t.Errorf("environ kept %s, want the router proxy dropped", dropped)
		}
	}
	for _, kept := range []string{"HTTP_PROXY=http://proxy.corp:3128", "NO_PROXY=corp", "ZAI_API_KEY=k", "HUDDLE_ROUTER_PORT=20999"} {
		if !strings.Contains(joined, kept) {
			t.Errorf("environ lost %s, want every non-router variable kept", kept)
		}
	}
}

func TestEnvironWithoutAPortOverrideFallsBackToTheCanonicalPort(t *testing.T) {
	env := domain.EnvLookup{
		"HUDDLE_ROUTER_PORT": "not-a-port",
		"HTTPS_PROXY":        "localhost:" + strconv.Itoa(domain.HuddleRouterPort),
	}
	got := strings.Join(environ(env), "\n")
	if strings.Contains(got, "HTTPS_PROXY") {
		t.Errorf("environ kept %q, want the canonical port's router proxy dropped", env["HTTPS_PROXY"])
	}
}
