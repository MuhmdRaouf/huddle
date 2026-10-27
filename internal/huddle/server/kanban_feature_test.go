package server

// The feature route as the served server runs it: the owner's close and reopen
// alone, behind the same write guards every /api write passes, with one audit
// row per write.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanban "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// featureServer starts a server whose session's working dir holds a board with
// two swr cards, and answers the board root.
func featureServer(t *testing.T) (*childServer, string) {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "alpha")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".agents", "kanban")
	if _, err := board.Init(context.Background(), root, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"SWR-1", "SWR-2"} {
		card := "---\nid: " + id + "\ntitle: \"" + id + " title\"\ntype: docs\ncluster: swr\n" +
			"summary: \"one\"\n---\n\n# " + id + "\n\nBody text.\n"
		if err := os.WriteFile(filepath.Join(root, "ready", id+".md"), []byte(card), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := startServer(t, nil)
	report := `{"session_id":"s-swr","cwd":"` + repo +
		`","last_event":"UserPromptSubmit","last_event_at":"2026-10-17T09:00:00.000Z"}`
	if code, out := srv.post(t, "/api/registry/report", report, nil); code != 200 {
		t.Fatalf("report the session: %d %s", code, out)
	}
	return srv, root
}

// hashBoardDir answers one digest of every file under the board root, so a test
// proves a refusal left the board byte for byte unchanged.
func hashBoardDir(t *testing.T, root string) string {
	t.Helper()
	digest := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(digest, "%s\x00%x\x00", rel, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func TestOnlyTheOwnerClosesAFeatureOverHTTP(t *testing.T) {
	srv, root := featureServer(t)
	key := kanban.KeyOf(root)
	path := "/api/kanban/b/" + key + "/feature"

	// a joined member's close is refused, the board untouched
	member := permitMember(t, srv)
	before := hashBoardDir(t, root)
	if code, out := srv.post(t, path, `{"action":"close","feature":"swr"}`,
		map[string]string{"x-huddle-token": member}); code != http.StatusForbidden {
		t.Fatalf("the member's close = %d %s, want 403", code, out)
	}
	if after := hashBoardDir(t, root); after != before {
		t.Fatal("the member's refused close changed the board")
	}

	// the owner's close runs and lands the feature in FEATURES
	if code, out := srv.post(t, path, `{"action":"close","feature":"swr"}`, nil); code != http.StatusOK ||
		!strings.Contains(out, "feature swr closed: 2 cards archived") {
		t.Fatalf("the owner's close = %d %s, want 200 with the archived line", code, out)
	}
	data, err := os.ReadFile(filepath.Join(root, "FEATURES"))
	if err != nil || !strings.Contains(string(data), `"name":"swr"`) {
		t.Fatalf("FEATURES after the close = %s (%v), want swr in it", data, err)
	}

	// one audit row names the write: the owner, the command, the feature
	_, _, out := srv.get(t, "/api/audit", nil)
	var row map[string]any
	for _, entry := range bodyMap(t, out)["entries"].([]any) {
		if candidate, _ := entry.(map[string]any); candidate["action"] == "kanban.feature.close" {
			row = candidate
		}
	}
	if row == nil {
		t.Fatalf("no kanban.feature.close row in the audit %s", out)
	}
	if row["actor"] != "owner" || row["target"] != key+"/swr" {
		t.Errorf("the kanban.feature.close row = %v, want the owner closing swr on %s", row, key)
	}
	if detail, _ := row["detail"].(map[string]any); detail["ok"] != true {
		t.Errorf("the kanban.feature.close detail = %v, want ok true", row["detail"])
	}
}

func TestTheFeatureRouteSitsBehindTheWriteGuards(t *testing.T) {
	srv, root := featureServer(t)
	key := kanban.KeyOf(root)
	path := "/api/kanban/b/" + key + "/feature"
	headers := map[string]string{
		"x-huddle-token": srv.token,
		"content-type":   "application/json",
		ActionHeader:     path,
		"Origin":         "https://evil.example",
	}
	if code, _, out := guardDo(t, srv, http.MethodPost, path, `{"action":"close","feature":"swr"}`, headers); code != http.StatusForbidden {
		t.Fatalf("the foreign origin = %d %s, want the guard's 403", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "FEATURES")); !os.IsNotExist(err) {
		t.Fatalf("the refused close left FEATURES behind: %v", err)
	}
}
