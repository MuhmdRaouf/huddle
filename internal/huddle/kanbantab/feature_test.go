package kanbantab

// The tab's feature route: close and reopen one feature under
// /b/<key>/feature, the owner's writes alone, one audit callback per write
// like the flow route's, a refusal as 409 and any other failure as 500.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// writeFeatureCard lands one card carrying a feature as its cluster.
func writeFeatureCard(t *testing.T, repo, col, id string) {
	t.Helper()
	text := "---\nid: " + id + "\ntitle: \"" + id + " title\"\ntype: docs\ncluster: swr\n" +
		"summary: \"one\"\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(boardRoot(repo), col, id+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// featureBody runs one POST against the tab's feature route and answers status
// and body.
func featureBody(t *testing.T, h http.Handler, key, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, Prefix+"/b/"+key+"/feature", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// hashBoard answers one digest of every file under the board root, so a test
// proves a refusal left the board byte for byte unchanged.
func hashBoard(t *testing.T, root string) string {
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

// closedFeatures reads the board's FEATURES through the board itself.
func closedFeatures(t *testing.T, repo string) []board.ClosedFeature {
	t.Helper()
	return board.Board{Root: boardRoot(repo), Now: pinnedNow}.ClosedFeatures()
}

func TestFeatureRouteClosesAndReopensForTheOwner(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeFeatureCard(t, repo, "ready", "SWR-1")
	writeFeatureCard(t, repo, "in-progress", "SWR-2")
	audits := &auditLog{}
	owner := false
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Audit: audits.record,
		Owner: func(*http.Request) bool { return owner },
	})
	key := server.KeyOf(boardRoot(repo))

	// a member's close is refused with the board untouched
	before := hashBoard(t, boardRoot(repo))
	status, out := featureBody(t, tab, key, `{"action":"close","feature":"swr"}`)
	if status != http.StatusForbidden || !strings.Contains(out, "only the owner closes or reopens a feature") {
		t.Fatalf("the member's close = %d %s, want 403 naming the owner", status, out)
	}
	if after := hashBoard(t, boardRoot(repo)); after != before {
		t.Fatal("the refused close changed the board")
	}

	// the owner's close answers the line naming the cards and lands in FEATURES
	owner = true
	status, out = featureBody(t, tab, key, `{"action":"close","feature":"swr"}`)
	if status != http.StatusOK || !strings.Contains(out, "feature swr closed: 2 cards archived") {
		t.Fatalf("the owner's close = %d %s, want 200 with the archived line", status, out)
	}
	if closed := closedFeatures(t, repo); len(closed) != 1 || closed[0].Name != "swr" || closed[0].Actor != "owner" {
		t.Fatalf("the closed features = %v, want swr closed by the owner", closed)
	}

	// the owner's reopen clears the feature again
	status, out = featureBody(t, tab, key, `{"action":"reopen","feature":"swr"}`)
	if status != http.StatusOK || !strings.Contains(out, "feature swr reopened: 2 cards back") {
		t.Fatalf("the owner's reopen = %d %s, want 200 with the back line", status, out)
	}
	if closed := closedFeatures(t, repo); len(closed) != 0 {
		t.Fatalf("the closed features after the reopen = %v, want none", closed)
	}

	// one audit call per write: the close and the reopen, the feature in the id
	// slot, and never a call for the member's refused request
	audits.mu.Lock()
	defer audits.mu.Unlock()
	want := []auditCall{
		{command: "feature.close", key: key, id: "swr", ok: true},
		{command: "feature.reopen", key: key, id: "swr", ok: true},
	}
	if len(audits.calls) != len(want) {
		t.Fatalf("the audit saw %v, want one call per write %v", audits.calls, want)
	}
	for i, call := range audits.calls {
		if call != want[i] {
			t.Errorf("audit call %d = %+v, want %+v", i, call, want[i])
		}
	}
}

func TestFeatureRouteWithoutAnOwnerRefusesEveryone(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeFeatureCard(t, repo, "ready", "SWR-1")
	writeFeatureCard(t, repo, "ready", "SWR-2")
	// no Owner option: a tab mounted without the answer closes nothing
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow,
	})
	key := server.KeyOf(boardRoot(repo))
	before := hashBoard(t, boardRoot(repo))
	status, out := featureBody(t, tab, key, `{"action":"close","feature":"swr"}`)
	if status != http.StatusForbidden || !strings.Contains(out, "only the owner closes or reopens a feature") {
		t.Fatalf("the close with no Owner = %d %s, want 403", status, out)
	}
	if after := hashBoard(t, boardRoot(repo)); after != before {
		t.Fatal("the refused close changed the board")
	}
}

func TestFeatureRouteRefusesWhatItDoesNotTake(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeFeatureCard(t, repo, "ready", "SWR-1")
	writeFeatureCard(t, repo, "ready", "SWR-2")
	audits := &auditLog{}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Audit: audits.record,
		Owner: func(*http.Request) bool { return true },
	})
	key := server.KeyOf(boardRoot(repo))

	// a feature no card carries is the board's own refusal
	status, out := featureBody(t, tab, key, `{"action":"close","feature":"nope"}`)
	if status != http.StatusConflict || !strings.Contains(out, "no feature nope on this board") {
		t.Fatalf("the unknown feature = %d %s, want 409 naming it", status, out)
	}

	// an action that is neither close nor reopen, an empty feature and a torn
	// body are refused before the board is read
	for _, refused := range []struct{ body, want string }{
		{`{"action":"delete","feature":"swr"}`, "action"},
		{`{"action":"close","feature":""}`, "feature"},
		{`{`, "feature"},
	} {
		if status, out := featureBody(t, tab, key, refused.body); status != http.StatusBadRequest || !strings.Contains(out, refused.want) {
			t.Fatalf("POST %s = %d %s, want 400 naming %q", refused.body, status, out, refused.want)
		}
	}

	// a body over 64 KiB is refused before it is read
	big := `{"action":"close","feature":"` + strings.Repeat("x", 70*1024) + `"}`
	if status, out := featureBody(t, tab, key, big); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("the oversized body = %d %s, want 413", status, out)
	}

	// a read is not the route's method
	if status, out := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/feature"); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /feature = %d %s, want 405", status, out)
	}

	// a key no board answers is the tab's own 404
	if status, out := featureBody(t, tab, "000000000000", `{"action":"close","feature":"swr"}`); status != http.StatusNotFound {
		t.Fatalf("the unknown board = %d %s, want 404", status, out)
	}

	// the refused writes left the cards where they were
	for _, id := range []string{"SWR-1", "SWR-2"} {
		if col := columnOf(t, tab, key, id); col != "ready" {
			t.Fatalf("%s sits in %s after the refusals, want ready", id, col)
		}
	}

	// one audit call for the one write that reached the board, refused by it
	audits.mu.Lock()
	defer audits.mu.Unlock()
	if len(audits.calls) != 1 || audits.calls[0] != (auditCall{command: "feature.close", key: key, id: "nope", ok: false}) {
		t.Fatalf("the audit saw %v, want the one refused close", audits.calls)
	}
}
