package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// liveHome makes a home whose hub runs on port: the run directory carries the pid of this test
// process (alive) and the port the hub answers on.
func liveHome(t *testing.T, port int) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.pid"), strconv.Itoa(os.Getpid()))
	writeFile(t, filepath.Join(root, "run", "huddle.port"), strconv.Itoa(port))
	return root
}

// identifyWith answers the identity of a session in a project whose settings file holds
// settings, with env on top, after it joined with cred (the zero Cred joins nothing).
func identifyWith(t *testing.T, env map[string]string, settings string, cred Cred) Identity {
	t.Helper()
	ctx := context.Background()
	project := t.TempDir()
	if settings != "" {
		if err := os.MkdirAll(filepath.Join(project, ".agents", "huddle"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(project, ".agents", "huddle", "huddle.json"), settings)
	}
	vars := map[string]string{"HUDDLE_SESSIONS_DIR": filepath.Join(t.TempDir(), "sessions")}
	for key, value := range env {
		vars[key] = value
	}
	w := world(t, realDir(t, project), vars)
	if cred.URL != "" {
		if err := SaveCred(ctx, w, cred, ""); err != nil {
			t.Fatal(err)
		}
	}
	return Identify(ctx, w, "")
}

// TestIdentifyFollowsTheLiveHubOnLoopback pins the one-hub rule: a loopback address from the
// credential or the project's settings gives way to the port the home's running hub answers on.
func TestIdentifyFollowsTheLiveHubOnLoopback(t *testing.T) {
	hubPort := freePort(t)
	staleURL := Local(freePort(t))
	liveURL := Local(hubPort)
	member := Cred{URL: staleURL, Channel: "homelab", As: "proxbeam", Credential: "c"}

	t.Run("a credential on the old port", func(t *testing.T) {
		id := identifyWith(t, map[string]string{"HUDDLE_HOME": liveHome(t, hubPort)}, "", member)
		if id.URL != liveURL || id.Source != "credential" || id.Channel != "homelab" || id.As != "proxbeam" {
			t.Errorf("Identify = %+v, want the live hub %s from the credential, still homelab as proxbeam", id, liveURL)
		}
	})
	t.Run("a project url on the old port", func(t *testing.T) {
		settings := `{"channel": "homelab", "as": "proxbeam", "url": "` + staleURL + `"}`
		id := identifyWith(t, map[string]string{"HUDDLE_HOME": liveHome(t, hubPort)}, settings, Cred{})
		if id.URL != liveURL || id.Source != "config" {
			t.Errorf("Identify = %+v, want the live hub %s", id, liveURL)
		}
	})
	t.Run("a remote hub stays where it is", func(t *testing.T) {
		remote := Cred{URL: "https://hub.example.com", Channel: "homelab", As: "proxbeam", Credential: "c"}
		id := identifyWith(t, map[string]string{"HUDDLE_HOME": liveHome(t, hubPort)}, "", remote)
		if id.URL != "https://hub.example.com" {
			t.Errorf("Identify URL = %q, want the remote hub untouched", id.URL)
		}
	})
	t.Run("no hub running keeps the credential's address", func(t *testing.T) {
		id := identifyWith(t, map[string]string{"HUDDLE_HOME": t.TempDir()}, "", member)
		if id.URL != staleURL {
			t.Errorf("Identify URL = %q, want %q", id.URL, staleURL)
		}
	})
	t.Run("a dead pid is no running hub", func(t *testing.T) {
		root := liveHome(t, hubPort)
		writeFile(t, filepath.Join(root, "run", "huddle.pid"), "2147483646")
		id := identifyWith(t, map[string]string{"HUDDLE_HOME": root}, "", member)
		if id.URL != staleURL {
			t.Errorf("Identify URL = %q, want %q", id.URL, staleURL)
		}
	})
	t.Run("an address from the environment stays", func(t *testing.T) {
		env := map[string]string{"HUDDLE_HOME": liveHome(t, hubPort), "HUDDLE_URL": staleURL}
		if id := identifyWith(t, env, "", Cred{}); id.URL != staleURL || id.Source != "env" {
			t.Errorf("Identify = %+v, want HUDDLE_URL %s untouched", id, staleURL)
		}
		env = map[string]string{"HUDDLE_HOME": liveHome(t, hubPort), "HUDDLE_PORT": strconv.Itoa(freePort(t))}
		if id := identifyWith(t, env, "", Cred{}); id.Source != "port" || id.URL == liveURL {
			t.Errorf("Identify = %+v, want HUDDLE_PORT untouched", id)
		}
	})
	t.Run("a hub with no port file is read from its process", func(t *testing.T) {
		root := liveHome(t, hubPort)
		if err := os.Remove(filepath.Join(root, "run", "huddle.port")); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": root})
		w.Proc = Proc{
			Command:  func(int) (string, bool) { return "huddle serve", true },
			ListenOf: func(int) (int, bool) { return hubPort, true },
		}
		if url, ok := liveHubURL(ctx, w, root); !ok || url != liveURL {
			t.Errorf("liveHubURL = (%q, %v), want %q", url, ok, liveURL)
		}
	})
}

// TestFollowReadsTheSessionAgainBeforeAReconnect pins the bridge's reconnect: the address moves
// with the hub, and a session now in another channel starts that channel's stream from its own
// cursor.
func TestFollowReadsTheSessionAgainBeforeAReconnect(t *testing.T) {
	hubPort := freePort(t)
	live := Local(hubPort)
	cred := Cred{URL: Local(freePort(t)), Channel: "homelab", As: "proxbeam", Credential: "c"}
	w := world(t, t.TempDir(), map[string]string{
		"HUDDLE_HOME":         liveHome(t, hubPort),
		"HUDDLE_SESSIONS_DIR": filepath.Join(t.TempDir(), "sessions"),
	})
	if err := SaveCred(context.Background(), w, cred, ""); err != nil {
		t.Fatal(err)
	}
	b := &mcpBridge{w: w, url: cred.URL, channelName: "homelab", me: "proxbeam", lastSeq: 7,
		pushed: map[int64]bool{7: true}, subBase: 5, subBaseSet: true, stderr: &strings.Builder{}}

	b.follow(context.Background())
	if s := b.state(); s.url != live || s.channelName != "homelab" || s.me != "proxbeam" {
		t.Errorf("after a hub move the bridge reads %+v, want %s in homelab as proxbeam", s, live)
	}
	if b.lastSeq != 7 || !b.pushed[7] || !b.subBaseSet {
		t.Errorf("the same channel keeps its cursor: lastSeq %d, pushed %v, subBaseSet %v", b.lastSeq, b.pushed, b.subBaseSet)
	}

	if err := SaveCred(context.Background(), w, Cred{URL: cred.URL, Channel: "shop", As: "proxbeam", Credential: "c"}, ""); err != nil {
		t.Fatal(err)
	}
	b.follow(context.Background())
	if s := b.state(); s.channelName != "shop" {
		t.Fatalf("the bridge reads channel %q, want shop", s.channelName)
	}
	if b.lastSeq != 0 || len(b.pushed) != 0 || b.subBaseSet || b.subBase != 0 {
		t.Errorf("another channel starts from its own cursor: lastSeq %d, pushed %v, subBase %d (%v)", b.lastSeq, b.pushed, b.subBase, b.subBaseSet)
	}
}
