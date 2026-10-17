package outside

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startRadar stands in for the radar plugin: a loopback server on a free port, handed the port
// so it can answer only the host 127.0.0.1:<port>, as the real one's DNS-rebinding guard does.
func startRadar(t *testing.T, handler func(port int) http.HandlerFunc) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: handler(port)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return port
}

// radarWith points a Radar at a temp state dir; a port of 0 writes no port file, so radar is
// not there.
func radarWith(t *testing.T, port int) (*Radar, string) {
	t.Helper()
	home := t.TempDir()
	if port != 0 {
		if err := os.WriteFile(filepath.Join(home, "port"), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := NewRadar()
	env := map[string]string{"RADAR_HOME": home}
	r.Env = func(key string) string { return env[key] }
	return r, home
}

// radarOn serves one body on one status and points a Radar at it.
func radarOn(t *testing.T, status int, body string) *Radar {
	t.Helper()
	port := startRadar(t, func(int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}
	})
	r, _ := radarWith(t, port)
	return r
}

// the bodies the dashboard's fake radar answers with
const radarAlertsBody = `{"alerts":[
 {"id":"a1","kind":"loop","sessionId":"claude-api","agentId":null,"project":"shop","since":1770000000000,"detail":"same tool call 12 times","costUsd":0.4},
 {"id":"a2","kind":"stuck","sessionId":"claude-elsewhere","agentId":null,"project":"other","since":1770000060000,"detail":"not ours","costUsd":null}]}`

const radarAttributionBody = `{"rows":[
 {"key":"claude-api","label":"api","requests":10,"tokens":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0},"costUsd":1.25},
 {"key":"claude-web","label":"web","requests":3,"tokens":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0},"costUsd":0.5},
 {"key":"claude-elsewhere","label":"x","requests":3,"tokens":{},"costUsd":9}]}`

// TestPortTrimsJavaScriptWhitespace: the port file is read with Number(text.trim()), which
// drops the BOM and keeps NEL.
func TestPortTrimsJavaScriptWhitespace(t *testing.T) {
	r, home := radarWith(t, 0)
	if err := os.WriteFile(filepath.Join(home, "port"), []byte("\ufeff3900\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.Port(); got != 3900 {
		t.Errorf(`Port() of "\ufeff3900\n" = %d, want 3900`, got)
	}
	if err := os.WriteFile(filepath.Join(home, "port"), []byte("\u00853900"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := r.Port(); got != 0 {
		t.Errorf(`Port() of "\u00853900" = %d, want 0`, got)
	}
}

func TestRadarAbsent(t *testing.T) {
	t.Run("Radar absent: its parts are simply missing, no error", func(t *testing.T) {
		r, _ := radarWith(t, 0)
		if alerts, ok := r.Alerts(context.Background()); ok || alerts != nil {
			t.Errorf("Alerts() = %v, %t with no port file, want nil, false", alerts, ok)
		}
		if costs, ok := r.CostBySession(context.Background(), RangeDay); ok || costs != nil {
			t.Errorf("CostBySession() = %v, %t with no port file, want nil, false", costs, ok)
		}
		// the port file goes away after it was there: quiet again, nothing cached
		port := startRadar(t, func(int) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, radarAlertsBody) }
		})
		r, home := radarWith(t, port)
		if _, ok := r.Alerts(context.Background()); !ok {
			t.Fatal("Alerts() with radar running: want true")
		}
		if err := os.Remove(filepath.Join(home, "port")); err != nil {
			t.Fatal(err)
		}
		r.ResetCache()
		if alerts, ok := r.Alerts(context.Background()); ok || alerts != nil {
			t.Errorf("Alerts() = %v, %t after the port file left, want nil, false", alerts, ok)
		}
		if costs, ok := r.CostBySession(context.Background(), RangeWeek); ok || costs != nil {
			t.Errorf("CostBySession() = %v, %t after the port file left, want nil, false", costs, ok)
		}
	})
}

func TestRadarRunning(t *testing.T) {
	t.Run("Radar running: its alerts for this channel's sessions, the cost per session, in the digest too", func(t *testing.T) {
		port := startRadar(t, func(port int) http.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) {
				if req.Host != fmt.Sprintf("127.0.0.1:%d", port) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				switch req.URL.Path {
				case "/api/alerts":
					_, _ = io.WriteString(w, radarAlertsBody)
				case "/api/attribution":
					if req.URL.RawQuery != "by=session&range=day" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_, _ = io.WriteString(w, radarAttributionBody)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}
		})
		r, _ := radarWith(t, port)
		alerts, ok := r.Alerts(context.Background())
		if !ok {
			t.Fatal("Alerts() ok = false with radar running, want true")
		}
		if len(alerts) != 2 {
			t.Fatalf("Alerts() = %d alerts, want 2", len(alerts))
		}
		a1 := alerts[0]
		if a1.ID != "a1" || a1.Kind != "loop" || a1.SessionID != "claude-api" || a1.AgentID != nil ||
			a1.Project != "shop" || a1.Since != 1770000000000 || a1.Detail != "same tool call 12 times" ||
			a1.CostUSD == nil || *a1.CostUSD != 0.4 {
			t.Errorf("alert a1 = %+v", a1)
		}
		if a2 := alerts[1]; a2.ID != "a2" || a2.CostUSD != nil || a2.AgentID != nil {
			t.Errorf("alert a2 = %+v, want no cost and no agent", a2)
		}
		costs, ok := r.CostBySession(context.Background(), RangeDay)
		if !ok {
			t.Fatal("CostBySession() ok = false with radar running, want true")
		}
		want := map[string]float64{"claude-api": 1.25, "claude-web": 0.5, "claude-elsewhere": 9}
		if costs.Len() != len(want) {
			t.Fatalf("CostBySession() = %v, want %v", costs.Keys(), want)
		}
		for key, cost := range want {
			if costs.USD(key) != cost {
				t.Errorf("CostBySession()[%s] = %v, want %v", key, costs.USD(key), cost)
			}
		}
		// the rows' order is the answer's: a caller that writes it out quotes it as it came
		if got, want := fmt.Sprintf("%v", costs.Keys()), "[claude-api claude-web claude-elsewhere]"; got != want {
			t.Errorf("CostBySession() keys = %s, want radar's row order %s", got, want)
		}
	})

	t.Run("answers are cached for five seconds, failures too", func(t *testing.T) {
		var hits atomic.Int32
		port := startRadar(t, func(int) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, radarAlertsBody)
			}
		})
		r, _ := radarWith(t, port)
		clock := &fakeClock{now: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
		r.Now = clock.Now
		if _, ok := r.Alerts(context.Background()); !ok {
			t.Fatal("first Alerts(): want true")
		}
		if hits.Load() != 1 {
			t.Fatalf("radar answered %d requests, want 1", hits.Load())
		}
		if _, ok := r.Alerts(context.Background()); !ok {
			t.Fatal("second Alerts() inside the TTL: want the cached true")
		}
		if hits.Load() != 1 {
			t.Fatalf("radar answered %d requests inside the TTL, want 1", hits.Load())
		}
		clock.advance(5001 * time.Millisecond)
		if _, ok := r.Alerts(context.Background()); !ok {
			t.Fatal("Alerts() after the TTL: want true")
		}
		if hits.Load() != 2 {
			t.Fatalf("radar answered %d requests after the TTL, want 2", hits.Load())
		}
		r.ResetCache()
		if _, ok := r.Alerts(context.Background()); !ok {
			t.Fatal("Alerts() after ResetCache: want true")
		}
		if hits.Load() != 3 {
			t.Fatalf("radar answered %d requests after ResetCache, want 3", hits.Load())
		}
	})

	t.Run("a dead or misbehaving radar is quiet", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		deadPort := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		r, _ := radarWith(t, deadPort)
		if alerts, ok := r.Alerts(context.Background()); ok || alerts != nil {
			t.Errorf("Alerts() = %v, %t against a dead port, want nil, false", alerts, ok)
		}
		if _, ok := radarOn(t, http.StatusOK, "not json").Alerts(context.Background()); ok {
			t.Error("Alerts() ok = true against garbage, want false")
		}
		if _, ok := radarOn(t, http.StatusInternalServerError, "{}").Alerts(context.Background()); ok {
			t.Error("Alerts() ok = true against a 500, want false")
		}
		if _, ok := radarOn(t, http.StatusOK, `{"alerts": 5}`).Alerts(context.Background()); ok {
			t.Error("Alerts() ok = true when alerts is not a list, want false")
		}
	})

	t.Run("the port file decides whether radar is there", func(t *testing.T) {
		r, home := radarWith(t, 0)
		portFile := filepath.Join(home, "port")
		for _, body := range []string{"bogus", "", "0", "-1", "70000", "80.5"} {
			if err := os.WriteFile(portFile, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := r.Port(); got != 0 {
				t.Errorf("Port() = %d for port file %q, want 0", got, body)
			}
			if alerts, ok := r.Alerts(context.Background()); ok || alerts != nil {
				t.Errorf("Alerts() = %v, %t for port file %q, want nil, false", alerts, ok, body)
			}
		}
		if err := os.WriteFile(portFile, []byte(" 8080\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := r.Port(); got != 8080 {
			t.Errorf("Port() = %d, want 8080", got)
		}
	})

	t.Run("the state dir is $RADAR_HOME, else ~/.agents/radar", func(t *testing.T) {
		r := NewRadar()
		env := map[string]string{"RADAR_HOME": "/tmp/radar-here"}
		r.Env = func(key string) string { return env[key] }
		if got := r.StateDir(); got != "/tmp/radar-here" {
			t.Errorf("StateDir() = %q, want /tmp/radar-here", got)
		}
		env = map[string]string{"HOME": "/tmp/home"}
		if got := r.StateDir(); got != filepath.Join("/tmp/home", ".agents", "radar") {
			t.Errorf("StateDir() = %q, want /tmp/home/.agents/radar", got)
		}
	})

	t.Run("alert fields fall back the way the dashboard expects", func(t *testing.T) {
		r := radarOn(t, http.StatusOK, `{"alerts":[{"sessionId":"s"},{"sessionId":5},{"nope":1}]}`)
		alerts, ok := r.Alerts(context.Background())
		if !ok || len(alerts) != 1 {
			t.Fatalf("Alerts() = %v, %t, want the one alert with a string sessionId", alerts, ok)
		}
		a := alerts[0]
		if a.ID != "" || a.Kind != "alert" || a.AgentID != nil || a.Project != "" ||
			a.Since != 0 || a.Detail != "" || a.CostUSD != nil || a.SessionID != "s" {
			t.Errorf("alert = %+v, want every field at its default but sessionId", a)
		}
		for _, body := range []string{`{"alerts":{}}`, `{"nope":1}`} {
			if _, ok := radarOn(t, http.StatusOK, body).Alerts(context.Background()); ok {
				t.Errorf("Alerts() ok = true for %s, want false", body)
			}
		}
		if alerts, ok := radarOn(t, http.StatusOK, `{"alerts":[]}`).Alerts(context.Background()); !ok || alerts != nil {
			t.Errorf("Alerts() = %v, %t for an empty list, want nil, true", alerts, ok)
		}
	})

	t.Run("attribution keeps only rows with a string key and a finite number", func(t *testing.T) {
		body := `{"rows":[
 {"key":"a","costUsd":"1.25"},
 {"key":"b","costUsd":2},
 {"costUsd":3},
 {"key":"d"},
 {"key":"e","costUsd":1e999},
 {"key":"f","costUsd":-1},
 {"key":"b","costUsd":1}]}`
		costs, ok := radarOn(t, http.StatusOK, body).CostBySession(context.Background(), RangeMonth)
		if !ok {
			t.Fatal("CostBySession() ok = false, want true")
		}
		want := map[string]float64{"b": 3, "f": -1}
		if costs.Len() != len(want) {
			t.Fatalf("CostBySession() = %v, want %v", costs.Keys(), want)
		}
		for key, cost := range want {
			if costs.USD(key) != cost {
				t.Errorf("CostBySession()[%s] = %v, want %v", key, costs.USD(key), cost)
			}
		}
		for _, body := range []string{`{"rows": null}`, `{}`} {
			if _, ok := radarOn(t, http.StatusOK, body).CostBySession(context.Background(), RangeDay); ok {
				t.Errorf("CostBySession() ok = true for %s, want false", body)
			}
		}
		if costs, ok := radarOn(t, http.StatusOK, `{"rows":[]}`).CostBySession(context.Background(), RangeWeek); !ok || costs == nil || costs.Len() != 0 {
			t.Errorf("CostBySession() = %v, %t for an empty list, want empty, true", costs.Keys(), ok)
		}
	})
}

func TestJSFieldValues(t *testing.T) {
	t.Run("values become their JavaScript form", func(t *testing.T) {
		for _, tc := range []struct {
			raw  string
			want string
		}{
			{`"x"`, "x"},
			{"5", "5"},
			{"1.25", "1.25"},
			{"true", "true"},
			{"false", "false"},
			{`{"a":1}`, "d"},
			{`[1]`, "d"},
		} {
			if got := jsText(json.RawMessage(tc.raw), "d"); got != tc.want {
				t.Errorf("jsText(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		}
		for _, tc := range []struct {
			raw  string
			want float64
		}{
			{"", 0},
			{"null", 0},
			{"5", 5},
			{`"30"`, 30},
			{`"abc"`, 0},
			{"true", 1},
			{"false", 0},
			{`{"a":1}`, 0},
		} {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			if got := jsNumber(raw); got != tc.want {
				t.Errorf("jsNumber(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		}
		if got := jsStringPtr(nil); got != nil {
			t.Errorf("jsStringPtr(missing) = %v, want nil", *got)
		}
		if got := jsStringPtr(json.RawMessage("null")); got != nil {
			t.Errorf("jsStringPtr(null) = %v, want nil", *got)
		}
		for raw, want := range map[string]string{`"a"`: "a", "7": "7", "true": "true"} {
			if got := jsStringPtr(json.RawMessage(raw)); got == nil || *got != want {
				t.Errorf("jsStringPtr(%s) = %v, want %q", raw, got, want)
			}
		}
		for _, raw := range []string{"", "null", `"1.25"`, "true", `{"a":1}`} {
			var value json.RawMessage
			if raw != "" {
				value = json.RawMessage(raw)
			}
			if got := jsNumberPtr(value); got != nil {
				t.Errorf("jsNumberPtr(%s) = %v, want nil", raw, *got)
			}
		}
		if got := jsNumberPtr(json.RawMessage("1.5")); got == nil || *got != 1.5 {
			t.Errorf("jsNumberPtr(1.5) = %v, want 1.5", got)
		}
	})

	t.Run("alerts that are not objects are skipped", func(t *testing.T) {
		r := radarOn(t, http.StatusOK, `{"alerts":[5, "x", null, {"sessionId":"s"}]}`)
		alerts, ok := r.Alerts(context.Background())
		if !ok || len(alerts) != 1 || alerts[0].SessionID != "s" {
			t.Fatalf("Alerts() = %v, %t, want the one alert with a sessionId", alerts, ok)
		}
	})

	t.Run("the home fallback when no home is named", func(t *testing.T) {
		// an empty value names no home
		t.Setenv("HOME", "")
		r := NewRadar()
		r.Env = func(string) string { return "" }
		if got, want := r.StateDir(), ".agents/radar"; got != want {
			t.Errorf("StateDir() = %q, want %q", got, want)
		}
	})
}

// fakeSource stands in for a mounted radar tab: the paths it saw, the bodies it answers.
type fakeSource struct {
	seen  []string
	alert string
	cost  string
	ok    bool
}

func (f *fakeSource) RadarAnswer(_ context.Context, path string) ([]byte, bool) {
	f.seen = append(f.seen, path)
	switch {
	case strings.HasPrefix(path, "/api/alerts"):
		return []byte(f.alert), f.ok
	case strings.HasPrefix(path, "/api/attribution"):
		return []byte(f.cost), f.ok
	}
	return nil, false
}

// TestRadarSourceRead: an in-process source answers instead of the port file — the mounted tab's
// bytes, its failures quiet, and the port file unread while it stands.
func TestRadarSourceRead(t *testing.T) {
	// the port file names a radar that would answer differently: it must never be read
	port := startRadar(t, func(int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	r, _ := radarWith(t, port)
	source := &fakeSource{alert: radarAlertsBody, cost: radarAttributionBody, ok: true}
	r.Source = source
	alerts, ok := r.Alerts(context.Background())
	if !ok || len(alerts) != 2 || alerts[0].ID != "a1" {
		t.Fatalf("Alerts() = %v, %t, want the source's two alerts", alerts, ok)
	}
	costs, ok := r.CostBySession(context.Background(), RangeDay)
	if !ok || costs.Len() != 3 || costs.USD("claude-api") != 1.25 {
		t.Fatalf("CostBySession() = %v, %t, want the source's rows", costs, ok)
	}
	for _, want := range []string{"/api/alerts", "/api/attribution?by=session&range=day"} {
		if !slices.Contains(source.seen, want) {
			t.Errorf("the source saw %v, want %s", source.seen, want)
		}
	}

	t.Run("a failing source reads as radar absent, quietly", func(t *testing.T) {
		r.Source = &fakeSource{ok: false}
		r.ResetCache()
		if alerts, ok := r.Alerts(context.Background()); ok || alerts != nil {
			t.Errorf("Alerts() = %v, %t with a failing source, want nil, false", alerts, ok)
		}
		if costs, ok := r.CostBySession(context.Background(), RangeDay); ok || costs != nil {
			t.Errorf("CostBySession() = %v, %t with a failing source, want nil, false", costs, ok)
		}
	})
}
