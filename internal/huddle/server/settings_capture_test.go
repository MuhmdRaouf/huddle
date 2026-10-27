package server

// The hub's capture switch over the HTTP API: the owner's POST /api/settings turns body capture
// off, the answer and GET /api/settings carry it, a POST naming both switches or neither is a
// 400 naming both shapes, and a restart reads the switch back from the settings file.

import (
	"net/http"
	"strings"
	"testing"
)

// getSettings is GET /api/settings as the owner, parsed.
func getSettings(t *testing.T, srv *childServer) map[string]any {
	t.Helper()
	code, _, body := srv.get(t, "/api/settings", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d %s", code, body)
	}
	return bodyMap(t, body)
}

// postSettings is POST /api/settings as the owner.
func postSettings(t *testing.T, srv *childServer, body string) (int, string) {
	t.Helper()
	return srv.post(t, "/api/settings", body, nil)
}

func TestTheHubServesTheCaptureSetting(t *testing.T) {
	srv := startServer(t, nil)

	if got := getSettings(t, srv)["capture"]; got != true {
		t.Errorf("a fresh home's capture = %v, want true", got)
	}

	code, body := postSettings(t, srv, `{"notify": true, "capture": true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("both switches = %d %s, want 400", code, body)
	}
	if !strings.Contains(body, "settings: {notify: true|false} or {capture: true|false}") {
		t.Errorf("the 400 says %s, want both shapes", body)
	}
	if code, _ := postSettings(t, srv, `{}`); code != http.StatusBadRequest {
		t.Errorf("no switch = %d, want 400", code)
	}

	code, body = postSettings(t, srv, `{"capture": false}`)
	if code != http.StatusOK {
		t.Fatalf("POST capture false = %d %s", code, body)
	}
	if got := bodyMap(t, body)["capture"]; got != false {
		t.Errorf("the POST's answer = %s, want capture false", body)
	}
	if got := getSettings(t, srv)["capture"]; got != false {
		t.Errorf("the GET after the switch = %v, want false", got)
	}

	srv.kill()
	srv = startServer(t, map[string]string{"HUDDLE_DATA": srv.dataDir, "HUDDLE_HOME": srv.home})
	if got := getSettings(t, srv)["capture"]; got != false {
		t.Errorf("the GET after a restart = %v, want the switch read back from the settings file", got)
	}
}
