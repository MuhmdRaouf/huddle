package server

// The port a serve with no PORT in its environment takes: the same choice `huddle up` makes —
// ChoosePort over the home's config.toml — saved back, so a service restart comes back on the
// same address. A PORT in the environment (the plugin's own start, a test) still wins exactly.

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// serveOnce runs Serve in-process until it says its port, and answers that port with the stop
// that ends the server cleanly.
func serveOnce(t *testing.T, o Options) (int, func()) {
	t.Helper()
	log := &syncBuffer{}
	o.Stdout = log
	if o.Version == "" {
		o.Version = "0.0.0"
	}
	if o.Data == "" {
		o.Data = o.Home
	}
	ctx, cancel := context.WithCancel(context.Background())
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(log.joined()); m != nil {
			port, _ := strconv.Atoi(m[1])
			return port, stop
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	t.Fatalf("the server never said its port: %s", log.joined())
	return 0, func() {}
}

// TestServeWithNoPortChoosesAndSavesIt pins the service start's port: with no PORT named, serve
// takes the range's first free port and writes it to the home's config.toml, and the next serve
// of that home takes the saved port again.
func TestServeWithNoPortChoosesAndSavesIt(t *testing.T) {
	homeDir := t.TempDir()
	if err := (home.Home{Root: homeDir}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	_, firstFree := rangeState()
	if firstFree == 0 {
		t.Skip("every port in 18860-18869 is busy on this machine")
	}
	noPort := func(string) string { return "" }
	o := OptionsFromEnv("t", noPort, nil, nil, nil)
	o.Home = homeDir
	port, stop := serveOnce(t, o)
	if port != firstFree {
		stop()
		t.Fatalf("serve took port %d, want the range's first free %d", port, firstFree)
	}
	cfg, err := home.LoadConfig(home.Home{Root: homeDir})
	if err != nil {
		stop()
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Port != firstFree || !cfg.PortAuto {
		stop()
		t.Fatalf("the saved config = port %d auto %v, want %d auto true", cfg.Port, cfg.PortAuto, firstFree)
	}
	stop()

	// the second serve of the same home takes the saved port again
	again := OptionsFromEnv("t", noPort, nil, nil, nil)
	again.Home = homeDir
	port2, stop2 := serveOnce(t, again)
	defer stop2()
	if port2 != port {
		t.Fatalf("the second serve took port %d, want the saved %d", port2, port)
	}
}

// TestServeWithAPortInTheEnvironmentKeepsIt pins the other half: a PORT in the environment (the
// plugin's own start, a test) is bound exactly and never written to the home's config.
func TestServeWithAPortInTheEnvironmentKeepsIt(t *testing.T) {
	homeDir := t.TempDir()
	if err := (home.Home{Root: homeDir}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	own := freePort(t)
	env := func(name string) string {
		switch name {
		case "PORT":
			return strconv.Itoa(own)
		case "HUDDLE_HOME":
			return homeDir
		}
		return ""
	}
	port, stop := serveOnce(t, OptionsFromEnv("t", env, nil, nil, nil))
	defer stop()
	if port != own {
		t.Fatalf("serve took port %d, want the named %d", port, own)
	}
	cfg, err := home.LoadConfig(home.Home{Root: homeDir})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Port != 0 {
		t.Fatalf("a named port was saved anyway: config = %+v", cfg)
	}
}
