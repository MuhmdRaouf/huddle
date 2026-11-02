package server

// The artifacts route (artifacts.go): the artifacts one session's main transcript published,
// one row per artifact id at its latest publish.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// artifactLines are a session's lines publishing a report twice and a board once, so the route
// answers the report at its second version ahead of the board.
func artifactLines() []string {
	call := func(uuid, id string) string {
		return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Artifact","input":{"file_path":"/srv/page.html"}}]}}`
	}
	result := func(uuid, id, at, artifact, title, version string) string {
		return `{"type":"user","uuid":"` + uuid + `","timestamp":"` + at + `","toolUseResult":{"url":"https://claude.ai/artifact/` + artifact + `","artifact_id":"` + artifact + `","title":"` + title + `","version":"` + version + `"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"Published"}]}}`
	}
	return []string{
		call("a1", "toolu_1"), result("u1", "toolu_1", "2026-10-26T10:00:02.000Z", "report", "Report", "1"),
		call("a2", "toolu_2"), result("u2", "toolu_2", "2026-10-26T10:00:04.000Z", "board", "Board", "1"),
		call("a3", "toolu_3"), result("u3", "toolu_3", "2026-10-26T10:00:06.000Z", "report", "Report v2", "2"),
	}
}

func TestTheArtifactsRouteListsWhatASessionPublished(t *testing.T) {
	srv := startServer(t, nil)
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(transcript, []byte(joinLines(artifactLines())), 0o600); err != nil {
		t.Fatal(err)
	}
	reportConvoSession(t, srv, "s-1", transcript)

	code, _, body := srv.get(t, "/api/sessions/s-1/artifacts", nil)
	if code != 200 {
		t.Fatalf("artifacts: %d %s", code, body)
	}
	raw, _ := bodyMap(t, body)["artifacts"].([]any)
	got := make([]map[string]any, 0, len(raw))
	for _, one := range raw {
		got = append(got, one.(map[string]any))
	}
	want := []map[string]any{
		{"artifact_id": "report", "url": "https://claude.ai/artifact/report", "title": "Report v2", "version": "2", "at": "2026-10-26T10:00:06.000Z"},
		{"artifact_id": "board", "url": "https://claude.ai/artifact/board", "title": "Board", "version": "1", "at": "2026-10-26T10:00:04.000Z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("artifacts = %v, want %v", got, want)
	}

	// a session whose reports never named a transcript answers none, as does one whose
	// transcript the disk lost
	reportConvoSession(t, srv, "s-bare", "")
	reportConvoSession(t, srv, "s-gone", filepath.Join(t.TempDir(), "missing.jsonl"))
	for _, id := range []string{"s-bare", "s-gone"} {
		code, _, body = srv.get(t, "/api/sessions/"+id+"/artifacts", nil)
		if code != 200 || !strings.Contains(body, `"artifacts":[]`) {
			t.Errorf("%s: %d %s, want an empty list", id, code, body)
		}
	}

	// a session the registry does not know
	code, _, body = srv.get(t, "/api/sessions/s-none/artifacts", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("unknown session: %d %s, want 404 naming it", code, body)
	}
}
