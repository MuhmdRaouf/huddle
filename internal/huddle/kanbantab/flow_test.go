package kanbantab

// The tab's flow route: one board's write commands answered under
// /b/<key>/flow through the same board.Board the cli's commands run on, with
// one audit callback per write, a refusal as 409 and any other failure as 500.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// auditCall is one write the tab reported to its Audit callback.
type auditCall struct {
	command, key, id string
	ok               bool
}

// auditLog records every Audit callback, from any goroutine.
type auditLog struct {
	mu    sync.Mutex
	calls []auditCall
}

func (l *auditLog) record(_ context.Context, _ *http.Request, command, key, id string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, auditCall{command: command, key: key, id: id, ok: ok})
}

// flowBody runs one POST against the tab's flow route and answers status and body.
func flowBody(t *testing.T, h http.Handler, key, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, Prefix+"/b/"+key+"/flow", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// flow runs one flow command by its JSON fields and demands 200 with the wanted lines.
func flow(t *testing.T, h http.Handler, key, body string, wantLine string) {
	t.Helper()
	status, out := flowBody(t, h, key, body)
	if status != http.StatusOK {
		t.Fatalf("POST %s/flow %s = %d %s, want 200", key, body, status, out)
	}
	var done struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(out), &done); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	for _, line := range done.Lines {
		if strings.Contains(line, wantLine) {
			return
		}
	}
	t.Fatalf("POST %s/flow %s = %s, want a line naming %s", key, body, out, wantLine)
}

// columnOf answers the column one card sits in through the board's own API.
func columnOf(t *testing.T, h http.Handler, key, id string) string {
	t.Helper()
	_, body := answer(t, h, http.MethodGet, Prefix+"/b/"+key+"/api/board")
	var snapshot struct {
		Cards []struct {
			ID     string `json:"id"`
			Column string `json:"column"`
		} `json:"cards"`
	}
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatalf("decode the board %s: %v", body, err)
	}
	for _, card := range snapshot.Cards {
		if card.ID == id {
			return card.Column
		}
	}
	t.Fatalf("card %s never showed on the board %s", id, body)
	return ""
}

func TestFlowPullMovesTheCardAndAuditsEveryWrite(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "FLW-1")
	audits := &auditLog{}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Audit: audits.record,
	})
	key := server.KeyOf(boardRoot(repo))
	flow(t, tab, key,
		`{"command":"pull","actor":"w1","id":"FLW-1","branch":"card/FLW-1","worktree":"wt/FLW-1"}`,
		filepath.Join("in-progress", "FLW-1.md"))

	// the write reached the board the tab serves: the read API sees the card moved
	if col := columnOf(t, tab, key, "FLW-1"); col != "in-progress" {
		t.Fatalf("FLW-1 sits in %s after the pull, want in-progress", col)
	}

	// a second pull of the claimed card is the board's own refusal
	status, out := flowBody(t, tab, key, `{"command":"pull","actor":"w2","id":"FLW-1"}`)
	if status != http.StatusConflict || !strings.Contains(out, "already claimed by w1") {
		t.Fatalf("the second pull = %d %s, want 409 naming the holder", status, out)
	}

	// one audit call per write: the pull that moved the card and the one refused
	audits.mu.Lock()
	defer audits.mu.Unlock()
	want := []auditCall{
		{command: "pull", key: key, id: "FLW-1", ok: true},
		{command: "pull", key: key, id: "FLW-1", ok: false},
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

func TestFlowPullWritesADatabaseBoardThroughItsStore(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "FLW-DB")
	if _, err := board.MigrateDB(context.Background(), board.Board{Root: boardRoot(repo), Now: pinnedNow}); err != nil {
		t.Fatalf("migrate the board to kanban.db: %v", err)
	}
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Version: "test", Now: pinnedNow})
	key := server.KeyOf(boardRoot(repo))
	status, out := flowBody(t, tab, key, `{"command":"pull","actor":"w1","id":"FLW-DB"}`)
	if status != http.StatusOK {
		t.Fatalf("pull on a database board = %d %s, want 200", status, out)
	}
	// the claim is in kanban.db, where the cli reads it: a second pull is refused
	b, closeBoard, err := board.Open(context.Background(), board.Board{Root: boardRoot(repo), Now: pinnedNow})
	if err != nil {
		t.Fatalf("open the database board: %v", err)
	}
	defer func() { _ = closeBoard() }()
	if _, err := b.Pull(context.Background(), board.Args{Actor: "w2", ID: "FLW-DB"}); err == nil {
		t.Fatal("a second pull straight on kanban.db succeeded, want the tab's claim to hold")
	}
}

func TestTheTabServesADatabaseBoardsReadsFromItsRows(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "FLW-DB")
	if _, err := board.MigrateDB(context.Background(), board.Board{Root: boardRoot(repo), Now: pinnedNow}); err != nil {
		t.Fatalf("migrate the board to kanban.db: %v", err)
	}
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Version: "test", Now: pinnedNow})
	key := server.KeyOf(boardRoot(repo))

	// the read API answers the rows kanban.db holds, not the folder the migration archived
	if col := columnOf(t, tab, key, "FLW-DB"); col != "ready" {
		t.Fatalf("FLW-DB sits in %s on the database board, want ready", col)
	}
	flow(t, tab, key, `{"command":"pull","actor":"w1","id":"FLW-DB"}`,
		filepath.Join("in-progress", "FLW-DB.md"))
	// the watch answers on its own 300 ms cadence, so the moved row reaches the
	// read API within a few passes
	column := ""
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if column = columnOf(t, tab, key, "FLW-DB"); column == "in-progress" {
			break
		}
	}
	if column != "in-progress" {
		t.Fatalf("FLW-DB sits in %s after the pull, want in-progress", column)
	}
}

func TestFlowRunsEveryCommand(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "backlog", "FLW-A")
	writeTicket(t, repo, "ready", "FLW-B")
	// no Audit option: a nil Audit writes nothing and breaks no request
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow,
	})
	key := server.KeyOf(boardRoot(repo))
	steps := []struct{ body, line string }{
		{`{"command":"promote","actor":"lead","id":"FLW-A","force":true}`, filepath.Join("ready", "FLW-A.md")},
		{`{"command":"pull","actor":"w1","id":"FLW-A"}`, filepath.Join("in-progress", "FLW-A.md")},
		{`{"command":"log","actor":"w1","id":"FLW-A","note":"halfway through"}`, filepath.Join("in-progress", "FLW-A.md")},
		{`{"command":"set","actor":"w1","id":"FLW-A","pairs":["priority=P1"]}`, filepath.Join("in-progress", "FLW-A.md")},
		{`{"command":"review","actor":"w1","id":"FLW-A"}`, filepath.Join("review", "FLW-A.md")},
		{`{"command":"reject","actor":"w2","id":"FLW-A","note":"the body is thin"}`, filepath.Join("in-progress", "FLW-A.md")},
		{`{"command":"block","actor":"w1","id":"FLW-A","note":"waiting on infra","until":"2026-10-20","on":["FLW-B"]}`, filepath.Join("blocked", "FLW-A.md")},
		{`{"command":"unblock","actor":"w1","id":"FLW-A"}`, filepath.Join("ready", "FLW-A.md")},
		{`{"command":"pull","actor":"w1","id":"FLW-A"}`, filepath.Join("in-progress", "FLW-A.md")},
		{`{"command":"review","actor":"w1","id":"FLW-A"}`, filepath.Join("review", "FLW-A.md")},
		{`{"command":"test","actor":"w2","id":"FLW-A","note":"go test ./... exit 0"}`, filepath.Join("testing", "FLW-A.md")},
		{`{"command":"done","actor":"w2","id":"FLW-A","note":"shipped","checks":"npm run check","merge":"abc123"}`, filepath.Join("done", "FLW-A.md")},
		{`{"command":"move","actor":"lead","id":"FLW-B","column":"backlog"}`, filepath.Join("backlog", "FLW-B.md")},
	}
	for _, step := range steps {
		flow(t, tab, key, step.body, step.line)
	}
	if col := columnOf(t, tab, key, "FLW-A"); col != "done" {
		t.Fatalf("FLW-A ended in %s, want done", col)
	}
}

func TestFlowRefusesWhatItDoesNotTake(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "FLW-1")
	tab := Mount(context.Background(), Options{Roots: staticRoots(repo), Version: "test", Now: pinnedNow})
	key := server.KeyOf(boardRoot(repo))

	// a read of the flow route is not its method
	if status, out := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/flow"); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /flow = %d %s, want 405", status, out)
	}
	for _, refused := range []struct{ body, want string }{
		// new stays the cli's: the tab writes no cards into being
		{`{"command":"new","actor":"w1","id":"FLW-2"}`, "is not a flow command"},
		{`{"command":"nope","actor":"w1"}`, "is not a flow command"},
		{`{`, "is not one command"},
	} {
		if status, out := flowBody(t, tab, key, refused.body); status != http.StatusBadRequest || !strings.Contains(out, refused.want) {
			t.Fatalf("POST %s = %d %s, want 400 naming %q", refused.body, status, out, refused.want)
		}
	}
	// a body over 64 KiB is refused before it is read
	big := `{"command":"log","note":"` + strings.Repeat("x", 70*1024) + `"}`
	if status, out := flowBody(t, tab, key, big); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("the oversized body = %d %s, want 413", status, out)
	}
	// a key no board answers is the tab's own 404
	if status, out := flowBody(t, tab, "000000000000", `{"command":"pull","actor":"w1"}`); status != http.StatusNotFound {
		t.Fatalf("the unknown board = %d %s, want 404", status, out)
	}
	// nothing above wrote: the card never moved
	if col := columnOf(t, tab, key, "FLW-1"); col != "ready" {
		t.Fatalf("FLW-1 sits in %s after the refusals, want ready", col)
	}
}

func TestFlowAnswers500WhenTheBoardFails(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	// a directory where the card should be reads as a failure, not a refusal
	card := filepath.Join(boardRoot(repo), "ready", "ERR-1.md")
	if err := os.Mkdir(card, 0o755); err != nil {
		t.Fatal(err)
	}
	audits := &auditLog{}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Audit: audits.record,
	})
	key := server.KeyOf(boardRoot(repo))
	status, out := flowBody(t, tab, key, `{"command":"pull","actor":"w1","id":"ERR-1"}`)
	if status != http.StatusInternalServerError || !strings.Contains(out, `"error"`) {
		t.Fatalf("the failed write = %d %s, want 500 with the error", status, out)
	}
	audits.mu.Lock()
	defer audits.mu.Unlock()
	if len(audits.calls) != 1 || audits.calls[0].ok {
		t.Fatalf("the audit saw %v, want one call with ok false", audits.calls)
	}
}

func TestTwoConcurrentPullsAnswerOne200AndOne409(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "RACE-1")
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow,
	})
	key := server.KeyOf(boardRoot(repo))
	codes := make(chan int, 2)
	for _, actor := range []string{"w1", "w2"} {
		go func() {
			request := httptest.NewRequest(http.MethodPost,
				Prefix+"/b/"+key+"/flow",
				strings.NewReader(`{"command":"pull","actor":"`+actor+`","id":"RACE-1"}`))
			recorder := httptest.NewRecorder()
			tab.ServeHTTP(recorder, request)
			codes <- recorder.Code
		}()
	}
	got := []int{<-codes, <-codes}
	sort.Ints(got)
	if got[0] != http.StatusOK || got[1] != http.StatusConflict {
		t.Fatalf("two pulls of one card = %v, want one 200 and one 409", got)
	}
	if col := columnOf(t, tab, key, "RACE-1"); col != "in-progress" {
		t.Fatalf("RACE-1 sits in %s after the race, want in-progress", col)
	}
}
