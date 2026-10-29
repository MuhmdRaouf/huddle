package main

// The end-to-end path a store failure takes: a start whose hub.db cannot open keeps serving,
// and /health names hub.db failing with healthy false — the answer an owner's browser reads,
// from the built binary.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// brokenHomeServer starts the built huddle serve the way argvServer does, over a home with a
// directory where hub.db would open, and answers the base URL it announced.
func brokenHomeServer(t *testing.T, base map[string]string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "hub.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	child := argvLayer(base, map[string]string{
		"PORT":         "0",
		"HUDDLE_DATA":  filepath.Join(t.TempDir(), "data"),
		"HUDDLE_HOME":  home,
		"HUDDLE_TOKEN": "fake-root-token",
	})
	cmd := exec.Command(huddleBin, "serve")
	cmd.Env = argvEntries(child)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the server: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	lines := make(chan string, 16)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	port := 0
	for line := range lines {
		if match := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(line); match != nil {
			port, _ = strconv.Atoi(match[1])
			break
		}
	}
	if port == 0 {
		t.Fatal("the server never said its port")
	}
	go func() {
		for range lines {
		}
	}() // the rest of the log goes nowhere
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if argvHealthy(url) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !argvHealthy(url) {
		t.Fatal("the server never answered /health")
	}
	return url
}

func TestE2EAHubDBThatCannotOpenShowsInHealth(t *testing.T) {
	url := brokenHomeServer(t, argvBase(t))
	resp, err := http.Get(url + "/health")
	if err != nil {
		t.Fatalf("get /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/health = %d, want 200: the server serves without its registry", resp.StatusCode)
	}
	var answer struct {
		OK         bool `json:"ok"`
		Healthy    bool `json:"healthy"`
		Components []struct {
			Name      string `json:"name"`
			State     string `json:"state"`
			Errors    int64  `json:"errors"`
			LastError string `json:"lastError"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("decode the health answer: %v", err)
	}
	if !answer.OK {
		t.Errorf("ok = false, want the server still serving")
	}
	if answer.Healthy {
		t.Errorf("healthy = true, want false while hub.db cannot open")
	}
	var hubDB *struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		Errors    int64  `json:"errors"`
		LastError string `json:"lastError"`
	}
	for i := range answer.Components {
		if answer.Components[i].Name == "hub.db" {
			hubDB = &answer.Components[i]
			break
		}
	}
	if hubDB == nil {
		t.Fatalf("components = %+v, want hub.db listed", answer.Components)
	}
	if hubDB.State != "failing" {
		t.Errorf("state = %q, want failing", hubDB.State)
	}
	if hubDB.Errors != 1 {
		t.Errorf("errors = %d, want the one open failure", hubDB.Errors)
	}
	if hubDB.LastError == "" {
		t.Errorf("lastError = empty, want the open's error")
	}
}
