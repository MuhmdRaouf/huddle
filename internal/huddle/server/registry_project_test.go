package server

import (
	"testing"
	"time"
)

func TestTheRegistryListRouteServesEachSessionsProjectKeyAndName(t *testing.T) {
	srv := startServer(t, nil)
	body := `{"session_id":"s-1","machine":"box","project":"/shop/wt","project_key":"/shop","project_name":"homelab","last_event":"SessionStart","last_event_at":"` + isoAgo(5*time.Second) + `"}`
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("post a report: %d %s", code, out)
	}
	code, _, out := srv.get(t, "/api/registry/sessions", nil)
	if code != 200 {
		t.Fatalf("the list = %d %s", code, out)
	}
	rows, _ := bodyMap(t, out)["sessions"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one", rows)
	}
	row, _ := rows[0].(map[string]any)
	if row["project_key"] != "/shop" || row["project_name"] != "homelab" {
		t.Errorf("the row = %v, want project_key /shop and project_name homelab", row)
	}
}
