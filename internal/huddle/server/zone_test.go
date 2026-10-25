package server

// The hub's one display time zone: a fresh home has none, the first owner request that carries
// the browser's zone header seeds it, a later browser's zone never overwrites it, a PUT on
// /api/settings changes it and marks the owner as its origin, and a name Go cannot load is
// refused with 400 and seeds nothing.

import (
	"context"
	"net/http"
	"testing"
)

// zoneOf reads the zone and its origin from GET /api/settings, sending the browser's zone header
// when header is not empty. An answer to a request that sent one names the hub's zone back in the
// same header, so the browser stops sending it; an answer to one that sent none never does.
func zoneOf(t *testing.T, srv *childServer, header string) (string, string) {
	t.Helper()
	headers := map[string]string{}
	if header != "" {
		headers[ZoneHeader] = header
	}
	code, answer, out := srv.get(t, "/api/settings", headers)
	if code != 200 {
		t.Fatalf("GET /api/settings = %d %s", code, out)
	}
	body := bodyMap(t, out)
	zone, _ := body["zone"].(string)
	origin, _ := body["zone_origin"].(string)
	heard, sent := answer.Get(ZoneHeader), len(answer.Values(ZoneHeader)) > 0
	if header == "" && sent {
		t.Fatalf("an answer to a request without the zone header carries %s: %q", ZoneHeader, heard)
	}
	if header != "" && (!sent || heard != zone) {
		t.Fatalf("the answer's %s = %q (sent %v), want the hub's zone %q", ZoneHeader, heard, sent, zone)
	}
	return zone, origin
}

// putZone sends PUT /api/settings {zone} as the owner.
func putZone(t *testing.T, srv *childServer, body string) (int, string) {
	t.Helper()
	code, _, out, err := srv.request(context.Background(), http.MethodPut, "/api/settings", body,
		map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"})
	if err != nil {
		t.Fatalf("PUT /api/settings: %v", err)
	}
	return code, out
}

func TestHubTimeZone(t *testing.T) {
	srv := startServer(t, nil)

	if zone, origin := zoneOf(t, srv, ""); zone != "" || origin != "" {
		t.Fatalf("a fresh home's zone = %q (%q), want none", zone, origin)
	}

	// a name Go cannot load seeds nothing
	if zone, _ := zoneOf(t, srv, "Mars/Olympus_Mons"); zone != "" {
		t.Fatalf("an unknown header zone seeded %q, want none", zone)
	}

	if zone, origin := zoneOf(t, srv, "Europe/Istanbul"); zone != "Europe/Istanbul" || origin != "first-sign-in" {
		t.Fatalf("the first sign-in's zone = %q (%q), want Europe/Istanbul (first-sign-in)", zone, origin)
	}
	if zone, origin := zoneOf(t, srv, "America/New_York"); zone != "Europe/Istanbul" || origin != "first-sign-in" {
		t.Fatalf("a second browser's zone overwrote it: %q (%q)", zone, origin)
	}

	for _, refused := range []string{`{"zone": "Mars/Olympus_Mons"}`, `{"zone": ""}`, `{"zone": "Local"}`, `{"zone": 3}`, `{}`} {
		if code, out := putZone(t, srv, refused); code != 400 {
			t.Fatalf("PUT %s = %d %s, want 400", refused, code, out)
		}
	}
	if zone, _ := zoneOf(t, srv, ""); zone != "Europe/Istanbul" {
		t.Fatalf("a refused PUT changed the zone to %q", zone)
	}

	code, out := putZone(t, srv, `{"zone": "Asia/Tokyo"}`)
	if code != 200 {
		t.Fatalf("PUT Asia/Tokyo = %d %s", code, out)
	}
	if body := bodyMap(t, out); body["zone"] != "Asia/Tokyo" || body["zone_origin"] != "owner" {
		t.Fatalf("the PUT's answer = %s, want Asia/Tokyo (owner)", out)
	}
	if zone, origin := zoneOf(t, srv, "Europe/Istanbul"); zone != "Asia/Tokyo" || origin != "owner" {
		t.Fatalf("after the owner's change = %q (%q), want Asia/Tokyo (owner)", zone, origin)
	}
	if code, out := putZone(t, srv, `{"zone": "UTC"}`); code != 200 || bodyMap(t, out)["zone"] != "UTC" {
		t.Fatalf("PUT UTC = %d %s", code, out)
	}

	// the zone survives a restart: it lives in the settings file, beside the notify switch
	srv.kill()
	again := startServer(t, map[string]string{"HUDDLE_DATA": srv.dataDir, "HUDDLE_HOME": srv.home})
	if zone, origin := zoneOf(t, again, ""); zone != "UTC" || origin != "owner" {
		t.Fatalf("after a restart = %q (%q), want UTC (owner)", zone, origin)
	}
}
