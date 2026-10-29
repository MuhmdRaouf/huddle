package server

// The health answer names the serving process's executable, symlinks resolved: up from a newer
// binary reads it to give an older hub way (the router's start applies the same rule to its
// daemon), so a hub that cannot name its binary counts as an old one. The stores the hub opens
// report into the same answer: a store that cannot open shows failing, and healthy turns false.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/health"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

func TestRouterPartsArePrefixed(t *testing.T) {
	parts := routerParts([]routerd.HealthPart{
		{Name: "reload", Kind: "loop", State: "failing", LastOKAt: 100, LastErrAt: 200, LastError: "is a directory", Errors: 3},
		{Name: "bodies.db", Kind: "store", State: "ok", LastOKAt: 300},
	})
	if len(parts) != 2 {
		t.Fatalf("routerParts = %+v, want two parts", parts)
	}
	if part := parts[0]; part.Name != "router.reload" || part.Kind != "loop" || part.State != "failing" ||
		part.LastOKAt != 100 || part.LastErrAt != 200 || part.LastError != "is a directory" || part.Errors != 3 {
		t.Errorf("part = %+v, want reload's own record under router.reload", part)
	}
	if part := parts[1]; part.Name != "router.bodies.db" || part.Kind != "store" || part.State != "ok" ||
		part.LastOKAt != 300 || part.Errors != 0 {
		t.Errorf("part = %+v, want bodies.db's own record under router.bodies.db", part)
	}
	if empty := routerParts(nil); empty == nil || len(empty) != 0 {
		t.Errorf("routerParts(nil) = %+v, want an empty non-nil slice", empty)
	}
}

func TestAFailingRouterPartMakesTheHubUnhealthy(t *testing.T) {
	t.Run("a failing router part makes a healthy hub unhealthy", func(t *testing.T) {
		if healthyWith(true, []routerd.HealthPart{{Name: "reload", State: "failing"}}) {
			t.Error("healthy = true, want false while a router part fails")
		}
	})
	t.Run("a stale router part makes a healthy hub unhealthy", func(t *testing.T) {
		if healthyWith(true, []routerd.HealthPart{{Name: "reload", State: "stale"}}) {
			t.Error("healthy = true, want false while a router part is stale")
		}
	})
	t.Run("ok and unknown parts keep a healthy hub healthy, and none at all does", func(t *testing.T) {
		if !healthyWith(true, nil) {
			t.Error("healthy = false with no router parts, want the hub's own verdict kept")
		}
		if !healthyWith(true, []routerd.HealthPart{{Name: "reload", State: "ok"}, {Name: "rss", State: "unknown"}}) {
			t.Error("healthy = false, want true while every part is ok or unknown")
		}
	})
	t.Run("an unhealthy hub stays unhealthy whatever the parts say", func(t *testing.T) {
		if healthyWith(false, nil) {
			t.Error("healthy = true, want the hub's own false kept")
		}
	})
}

func TestTheHealthAnswerListsTheComponents(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/health", nil)
	if code != 200 {
		t.Fatalf("/health = %d %s", code, body)
	}
	var answer struct {
		Healthy    bool          `json:"healthy"`
		Components []health.Part `json:"components"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the health answer %s: %v", body, err)
	}
	if !answer.Healthy {
		t.Errorf("healthy = false, want true on a server with nothing failing")
	}
	// other parts may list their own components beside the stores
	hubDB := findComponent(answer.Components, "hub.db")
	if hubDB == nil {
		t.Fatalf("components = %+v, want hub.db listed", answer.Components)
	}
	if hubDB.Kind != "store" || hubDB.State != "ok" || hubDB.Errors != 0 {
		t.Errorf("the hub.db component = %+v, want a store, ok, no errors", hubDB)
	}
	if hubDB.LastOKAt <= 0 {
		t.Errorf("lastOkAt = %d, want the open's moment", hubDB.LastOKAt)
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

// findComponent answers the named component of a health answer, nil when it is missing.
func findComponent(parts []health.Part, name string) *health.Part {
	for i := range parts {
		if parts[i].Name == name {
			return &parts[i]
		}
	}
	return nil
}

// A hub.db that cannot open shows failing in the health answer, and healthy turns false: the
// server keeps serving every channel, and /health says what cannot open.
func TestAHubDBThatCannotOpenShowsFailing(t *testing.T) {
	home := tempDir("huddle-no-hub-db-")
	// a directory where hub.db would open: the registry cannot start, and the server runs
	// without it
	if err := os.MkdirAll(filepath.Join(home, "hub.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_HOME": home})
	code, _, body := srv.get(t, "/health", nil)
	if code != 200 {
		t.Fatalf("/health = %d %s", code, body)
	}
	var answer struct {
		OK         bool          `json:"ok"`
		Healthy    bool          `json:"healthy"`
		Components []health.Part `json:"components"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the health answer %s: %v", body, err)
	}
	if !answer.OK {
		t.Errorf("ok = false, want the server still serving without its registry")
	}
	if answer.Healthy {
		t.Errorf("healthy = true, want false while hub.db cannot open")
	}
	part := findComponent(answer.Components, "hub.db")
	if part == nil {
		t.Fatalf("components = %+v, want hub.db listed", answer.Components)
	}
	if part.State != "failing" {
		t.Errorf("state = %q, want failing", part.State)
	}
	if part.Errors != 1 {
		t.Errorf("errors = %d, want the one open failure", part.Errors)
	}
	if part.LastError == "" {
		t.Errorf("lastError = empty, want the open's error")
	}
}

// A router body store that cannot open shows failing once a read has tried it: the read
// itself answers off as before, and /health names the store failing.
func TestARouterDBThatCannotOpenShowsFailing(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	// bytes no body store opens: the read's open fails, and the read answers off
	file := paths.RouterBodiesPath(context.Background(), domain.EnvLookup{"RADAR_HOME": rig.radar})
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/b-1", "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	if answer := bodyMap(t, out); answer["state"] != "off" {
		t.Errorf("the state = %v, want off: the unreadable store stays off", answer["state"])
	}
	code, out = bodiesCall(t, rig, http.MethodGet, "/health", "", rig.token)
	if code != 200 {
		t.Fatalf("/health = %d %s", code, out)
	}
	var answer struct {
		Healthy    bool          `json:"healthy"`
		Components []health.Part `json:"components"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatalf("the health answer %s: %v", out, err)
	}
	part := findComponent(answer.Components, "router.db")
	if part == nil {
		t.Fatalf("components = %+v, want router.db listed", answer.Components)
	}
	if part.State != "failing" {
		t.Errorf("state = %q, want failing", part.State)
	}
	if part.Errors != 1 {
		t.Errorf("errors = %d, want the one open failure", part.Errors)
	}
	if answer.Healthy {
		t.Errorf("healthy = true, want false while router.db cannot open")
	}
}
