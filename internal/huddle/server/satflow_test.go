package server

// The satellite's board writes (KBN-SHARED): a satellite's credential passes the kanban
// prefix's gate and its flow commands run on the hub's own board, audited as the satellite.
// The satellite's tab relays a command for a hub-hosted board onto the hub under the link's
// credential, so a claim on either machine is one claim on one kanban.db; a hub that cannot be
// reached answers one plain line, and a board the hub does not host stays local.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/satellite"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanban "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// gitRepo answers one git repository carrying an origin remote, in its own temp tree.
func gitRepo(t *testing.T, name, origin string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", name, err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("git remote add %s: %v\n%s", name, err, out)
	}
	return repo
}

// boardWith migrates a repo's fresh board to kanban.db holding one card in ready and answers
// the board root.
func boardWith(t *testing.T, repo, id string) string {
	t.Helper()
	root := filepath.Join(repo, ".agents", "kanban")
	if _, err := board.Init(context.Background(), root, "", time.Now()); err != nil {
		t.Fatalf("init the board: %v", err)
	}
	card := "---\nid: " + id + "\ntitle: \"" + id + " title\"\ntype: docs\ncluster: kanban\n" +
		"summary: \"one\"\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(root, "ready", id+".md"), []byte(card), 0o644); err != nil {
		t.Fatalf("write the card: %v", err)
	}
	if _, err := board.MigrateDB(context.Background(), board.Board{Root: root}); err != nil {
		t.Fatalf("migrate the board to kanban.db: %v", err)
	}
	return root
}

// columnOf opens a board's kanban.db and answers the column one card sits in.
func columnOf(t *testing.T, root, id string) string {
	t.Helper()
	opened, closeBoard, err := board.Open(context.Background(), board.Board{Root: root})
	if err != nil {
		t.Fatalf("open the board at %s: %v", root, err)
	}
	defer func() { _ = closeBoard() }()
	_, column, ok := opened.Find(id)
	if !ok {
		t.Fatalf("card %s is not on the board at %s", id, root)
	}
	return column
}

// joinSatellite mints and spends one invite the way join-hub does, answering the machine's
// credential.
func joinSatellite(t *testing.T, hubHome, hubBase, machine string) string {
	t.Helper()
	minter, err := hub.Open(context.Background(), home.Home{Root: hubHome}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's database: %v", err)
	}
	invite, err := minter.MintSatelliteInvite(context.Background(), time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body := call(t, hubBase, http.MethodPost, "/api/sat/join", "", "",
		`{"invite":"`+invite+`","name":"`+machine+`"}`)
	if status != 200 {
		t.Fatalf("the join answered %d: %s", status, body)
	}
	var joined struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(body), &joined); err != nil || joined.Credential == "" {
		t.Fatalf("the join = %s (%v)", body, err)
	}
	if err := minter.Close(); err != nil {
		t.Fatalf("close the hub's database: %v", err)
	}
	return joined.Credential
}

// reportCwd lands one session report naming a working dir.
func reportCwd(t *testing.T, base, token, session, cwd, at string) {
	t.Helper()
	status, body := call(t, base, http.MethodPost, "/api/registry/report", token, "",
		`{"session_id":"`+session+`","cwd":"`+cwd+`","last_event":"UserPromptSubmit","last_event_at":"`+at+`"}`)
	if status != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("report %s = %d %s", session, status, body)
	}
}

// TestTheKanbanPrefixTakesASatellitesCredential proves the hub side: a satellite's bearer
// credential passes the prefix's gate, its flow write lands on the hub's board and the audit
// names the satellite. A wrong bearer is refused on both routes.
func TestTheKanbanPrefixTakesASatellitesCredential(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hubHome := t.TempDir()
	hubLog := &runLog{}
	hubBase, stopHub, hubDone := startHuddleRun(t, hubHome, "hub-token", hubLog)
	t.Cleanup(func() {
		stopHub()
		select {
		case <-hubDone:
		case <-time.After(10 * time.Second):
		}
	})

	const origin = "git@github.com:MuhmdRaouf/gate-board.git"
	repo := gitRepo(t, "hub-side", origin)
	root := boardWith(t, repo, "SAT-1")
	reportCwd(t, hubBase, "hub-token", "s-hub", repo, "2026-10-18T09:00:05.000Z")
	key := board.RepoKey(origin, kanban.KeyOf(root))
	credential := joinSatellite(t, hubHome, hubBase, "mbp")

	// the boards list answers the satellite's credential
	status, body := call(t, hubBase, http.MethodGet, "/api/kanban/boards", "", credential, "")
	if status != 200 || !strings.Contains(body, key) {
		t.Fatalf("the boards with the satellite's credential = %d %s, want the board %s", status, body, key)
	}

	// the flow write runs on the hub's board under the satellite's name
	status, body = call(t, hubBase, http.MethodPost, "/api/kanban/b/"+key+"/flow", "", credential,
		`{"command":"pull","actor":"w1","id":"SAT-1"}`)
	if status != 200 || !strings.Contains(body, "in-progress") {
		t.Fatalf("the satellite's pull = %d %s, want 200 with the card's new column", status, body)
	}
	if column := columnOf(t, root, "SAT-1"); column != "in-progress" {
		t.Fatalf("SAT-1 sits in %s on the hub's board, want in-progress", column)
	}
	_, body = call(t, hubBase, http.MethodGet, "/api/audit", "hub-token", "", "")
	var audit struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
			Target string `json:"target"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &audit); err != nil {
		t.Fatalf("decode the audit %s: %v", body, err)
	}
	named := false
	for _, row := range audit.Entries {
		if row.Action == "kanban.pull" && row.Target == key+"/SAT-1" && row.Actor == "satellite:mbp" {
			named = true
		}
	}
	if !named {
		t.Fatalf("the audit holds no kanban.pull row naming satellite:mbp: %s", body)
	}

	// a credential the hub does not know is refused on both routes
	const wrong = "hsat_wrongmachinecredential000001"
	if status, _ = call(t, hubBase, http.MethodGet, "/api/kanban/boards", "", wrong, ""); status != 401 {
		t.Fatalf("the wrong bearer's boards = %d, want 401", status)
	}
	if status, _ = call(t, hubBase, http.MethodPost, "/api/kanban/b/"+key+"/flow", "", wrong,
		`{"command":"pull","actor":"w1","id":"SAT-1"}`); status != 401 {
		t.Fatalf("the wrong bearer's flow = %d, want 401", status)
	}
}

// TestRelayFlowToHubAnswersTheBranches pins the tab's relay wiring: no link and a board the hub
// does not list stay local, a hub 404 falls back to the local board, any other hub verdict
// passes through, and an unreachable hub answers the one plain line.
func TestRelayFlowToHubAnswersTheBranches(t *testing.T) {
	var mu sync.Mutex
	flows := map[string]int{}
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/kanban/boards":
			_, _ = w.Write([]byte(`{"boards":[{"key":"held12345678"},{"key":"gone87654321"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/kanban/b/"):
			key, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/kanban/b/"), "/")
			mu.Lock()
			flows[key]++
			mu.Unlock()
			if key == "gone87654321" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no board gone87654321"}`))
				return
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"held by w2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(hubSrv.Close)
	db, err := hub.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	link := satellite.New(satellite.Options{
		Reg: db, Home: home.Home{Root: t.TempDir()},
		HubURL: hubSrv.URL, Credential: "hsat_test-credential-0001",
	})
	wired := &server{}
	relay := func(key string) (int, []byte, bool) {
		return wired.relayFlowToHub(context.Background(), key, []byte(`{"command":"pull","actor":"w1"}`))
	}

	// no link yet: nothing leaves the machine
	if status, _, relayed := relay("held12345678"); relayed || status != 0 {
		t.Fatalf("a server with no link relayed %v (%d), want nothing", relayed, status)
	}
	wired.satMu.Lock()
	wired.satLink, wired.satHubURL = link, hubSrv.URL
	wired.satMu.Unlock()

	// a board the hub does not list stays local, without a flow call
	if _, _, relayed := relay("nothere000000"); relayed {
		t.Fatal("a board the hub does not list was relayed")
	}
	mu.Lock()
	calls := len(flows)
	mu.Unlock()
	if calls != 0 {
		t.Fatalf("the hub took %d flow calls, want none", calls)
	}

	// a hub 404 falls back to the local board
	if status, _, relayed := relay("gone87654321"); relayed || status != 0 {
		t.Fatalf("a hub 404 relayed %v (%d), want the local board", relayed, status)
	}

	// any other hub verdict passes through
	status, answer, relayed := relay("held12345678")
	if !relayed || status != http.StatusConflict || !strings.Contains(string(answer), "held by w2") {
		t.Fatalf("the relayed verdict = %d %s (%v), want the hub's 409 through", status, answer, relayed)
	}

	// an unreachable hub answers the one plain line, naming the hub
	hubSrv.Close()
	status, answer, relayed = relay("held12345678")
	if !relayed || status != http.StatusServiceUnavailable {
		t.Fatalf("the unreachable hub = %d %s (%v), want a relayed 503", status, answer, relayed)
	}
	var line struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(answer, &line); err != nil {
		t.Fatalf("the 503 body = %s: %v", answer, err)
	}
	want := fmt.Sprintf("the hub at %s is unreachable; a claim needs its answer, try again when it is back", hubSrv.URL)
	if line.Error != want {
		t.Fatalf("the 503 line = %q, want %q", line.Error, want)
	}
}

// TestASatellitesFlowCommandRunsOnTheHubsBoard is the card's end to end: both machines hold a
// clone of one repository, each with its own kanban.db; the satellite's pull lands on the hub's
// board and is audited there as the satellite; a second pull through the hub's own tab is one
// claim too many; the hub down, the satellite answers one plain 503 line.
func TestASatellitesFlowCommandRunsOnTheHubsBoard(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hubHome, satHome := t.TempDir(), t.TempDir()
	hubLog, satLog := &runLog{}, &runLog{}
	hubBase, stopHub, hubDone := startHuddleRun(t, hubHome, "hub-token", hubLog)
	t.Cleanup(func() {
		stopHub()
		select {
		case <-hubDone:
		case <-time.After(10 * time.Second):
		}
	})

	// one repository cloned in two places: both boards answer the origin's one key
	const origin = "git@github.com:MuhmdRaouf/shared-board.git"
	hubRepo := gitRepo(t, "hub-side", origin)
	satRepo := gitRepo(t, "sat-side", origin)
	hubRoot := boardWith(t, hubRepo, "S-1")
	satRoot := boardWith(t, satRepo, "S-1")
	// the hub's session is the newer report, so the hub's tab serves its own copy of the key
	reportCwd(t, hubBase, "hub-token", "s-hub", hubRepo, "2026-10-18T09:00:05.000Z")

	// the satellite joins and starts into its role, the way a real machine links
	credential := joinSatellite(t, hubHome, hubBase, "mbp")
	h := home.Home{Root: satHome}
	if err := home.SaveConfig(h, home.Config{
		Role: "satellite", Machine: "mbp", Hub: home.HubConfig{URL: hubBase},
		Remote: home.RemoteConfig{Permissions: true, Messages: true, Allow: []string{"*"}},
	}); err != nil {
		t.Fatalf("the satellite's config: %v", err)
	}
	if err := home.SaveSatelliteSecret(context.Background(), home.KeyFiles{Dir: h.Keys()}, hubBase, credential); err != nil {
		t.Fatalf("the satellite's credential: %v", err)
	}
	satBase, stopSat, satDone := startHuddleRun(t, satHome, "sat-token", satLog)
	t.Cleanup(func() {
		stopSat()
		select {
		case <-satDone:
		case <-time.After(10 * time.Second):
		}
	})
	if !until(5*time.Second, func() bool {
		return strings.Contains(satLog.joined(), "satellite link to "+hubBase)
	}) {
		t.Fatalf("the link never came up: %s / %s", satLog.joined(), hubLog.joined())
	}
	reportCwd(t, satBase, "sat-token", "s-sat", satRepo, "2026-10-18T09:00:00.000Z")
	key := board.RepoKey(origin, kanban.KeyOf(hubRoot))

	// the satellite's pull lands on the hub's board: one claim on one kanban.db
	status, body := call(t, satBase, http.MethodPost, "/api/kanban/b/"+key+"/flow", "sat-token", "",
		`{"command":"pull","actor":"w1","id":"S-1"}`)
	if status != 200 || !strings.Contains(body, "in-progress") {
		t.Fatalf("the satellite's pull = %d %s, want 200 with the card's new column", status, body)
	}
	if column := columnOf(t, hubRoot, "S-1"); column != "in-progress" {
		t.Fatalf("S-1 sits in %s on the hub's board, want in-progress", column)
	}
	if column := columnOf(t, satRoot, "S-1"); column != "ready" {
		t.Fatalf("S-1 sits in %s on the satellite's board, want ready: the relay never writes locally", column)
	}

	// the hub's audit names the satellite's write
	if !until(5*time.Second, func() bool {
		_, body := call(t, hubBase, http.MethodGet, "/api/audit", "hub-token", "", "")
		return strings.Contains(body, `"satellite:mbp"`) && strings.Contains(body, key+"/S-1")
	}) {
		_, body := call(t, hubBase, http.MethodGet, "/api/audit", "hub-token", "", "")
		t.Fatalf("the audit never named the satellite's pull: %s / %s", body, satLog.joined())
	}

	// a second pull through the hub's own tab is one claim too many
	status, _ = call(t, hubBase, http.MethodPost, "/api/kanban/b/"+key+"/flow", "hub-token", "",
		`{"command":"pull","actor":"w2","id":"S-1"}`)
	if status != 409 {
		t.Fatalf("the hub's own second pull = %d, want 409", status)
	}

	// the hub down: the satellite answers one plain line, and nothing is queued
	stopHub()
	select {
	case <-hubDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the hub never stopped")
	}
	status, body = call(t, satBase, http.MethodPost, "/api/kanban/b/"+key+"/flow", "sat-token", "",
		`{"command":"pull","actor":"w1","id":"S-1"}`)
	if status != 503 {
		t.Fatalf("the pull at the dead hub = %d %s, want 503", status, body)
	}
	want := "the hub at " + hubBase + " is unreachable; a claim needs its answer, try again when it is back"
	if !strings.Contains(body, want) {
		t.Fatalf("the 503 body = %s, want the line %q", body, want)
	}
	// the claim never queued: the satellite's own board still holds the card ready
	if column := columnOf(t, satRoot, "S-1"); column != "ready" {
		t.Fatalf("S-1 sits in %s on the satellite's board after the refusal, want ready", column)
	}
}
