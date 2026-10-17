package server

// The shared board's own proof (KBN-SHARED): one card, two machines, one holder. Twenty pulls
// of the same card at one moment — ten at the hub's own tab, ten at the satellite's local tab,
// where they relay onto the hub — leave exactly one claim on the hub's kanban.db, and the hub's
// live event stream carries the move within a second of the satellite's answer.

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanban "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// sharedBoard is one hub and one satellite serving clones of one repository: each copy holds
// the same card, both answer the origin's one key, and the satellite's link to the hub is up.
type sharedBoard struct {
	hubBase, satBase string
	hubRoot, satRoot string
	key              string
}

// startSharedBoard builds both machines the way two real machines link: one origin cloned in two
// temp repos, the card in ready on each copy, the hub's session the newer report so the hub's
// tab serves its own copy of the key, and the satellite joined over an invite and linked.
func startSharedBoard(t *testing.T, id string) *sharedBoard {
	t.Helper()
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

	const origin = "git@github.com:MuhmdRaouf/shared-board.git"
	hubRepo := gitRepo(t, "hub-side", origin)
	satRepo := gitRepo(t, "sat-side", origin)
	hubRoot := boardWith(t, hubRepo, id)
	satRoot := boardWith(t, satRepo, id)
	reportCwd(t, hubBase, "hub-token", "s-hub", hubRepo, "2026-10-18T09:00:05.000Z")

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
	return &sharedBoard{
		hubBase: hubBase, satBase: satBase,
		hubRoot: hubRoot, satRoot: satRoot,
		key: board.RepoKey(origin, kanban.KeyOf(hubRoot)),
	}
}

// postFlow posts one pull to one machine's tab without the test's own failure paths, so it is
// safe inside the racing goroutines: a transport problem answers a negative status with the
// error as the body, for the test goroutine to fail on.
func postFlow(base, token, key, actor, id string) (int, string, error) {
	path := "/api/kanban/b/" + key + "/flow"
	request, err := http.NewRequest(http.MethodPost, base+path,
		strings.NewReader(`{"command":"pull","actor":"`+actor+`","id":"`+id+`"}`))
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(ActionHeader, path)
	request.Header.Set("x-huddle-token", token)
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw), nil
}

// sseEvent is one event a board's stream delivers: its name and its data payload.
type sseEvent struct {
	name string
	data string
}

// openBoardStream opens one board's event stream through a machine's tab and answers its events
// on a channel until the context ends. The context alone bounds the read: a stream that never
// answers leaves the test waiting on its deadline, not forever.
func openBoardStream(ctx context.Context, base, token, key string) (<-chan sseEvent, <-chan error) {
	events, failed := make(chan sseEvent, 16), make(chan error, 1)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/kanban/b/"+key+"/api/events", nil)
	if err != nil {
		failed <- err
		return events, failed
	}
	request.Header.Set("x-huddle-token", token)
	go func() {
		defer close(events)
		response, err := (&http.Client{}).Do(request)
		if err != nil {
			failed <- err
			return
		}
		defer func() { _ = response.Body.Close() }()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		name := ""
		var data strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if data.Len() > 0 {
					select {
					case events <- sseEvent{name: name, data: data.String()}:
					case <-ctx.Done():
						return
					}
				}
				name = ""
				data.Reset()
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data.WriteString(strings.TrimPrefix(line, "data: "))
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			failed <- err
		}
	}()
	return events, failed
}

// waitSnapshot reads one stream until its first snapshot lands: the stream is live before the
// test writes anything through it.
func waitSnapshot(t *testing.T, events <-chan sseEvent, failed <-chan error, whose string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, open := <-events:
			if !open {
				t.Fatalf("%s closed before its snapshot", whose)
			}
			if event.name == "snapshot" {
				return
			}
		case err := <-failed:
			t.Fatalf("%s failed: %v", whose, err)
		case <-deadline:
			t.Fatalf("%s never delivered its snapshot within 5 s", whose)
		}
	}
}

// untilEvent reads one stream until an event matches and answers it; the bound ends the wait.
func untilEvent(t *testing.T, events <-chan sseEvent, failed <-chan error, bound time.Duration,
	what string, match func(name, data string) bool) sseEvent {
	t.Helper()
	deadline := time.After(bound)
	for {
		select {
		case event, open := <-events:
			if !open {
				t.Fatalf("the stream closed before %s", what)
			}
			if match(event.name, event.data) {
				return event
			}
		case err := <-failed:
			t.Fatalf("%s: the stream failed: %v", what, err)
		case <-deadline:
			t.Fatalf("%s never arrived within %s", what, bound)
		}
	}
}

// during reads one stream for the whole bound and fails the test when an event matches: the
// assertion that something never crossed.
func during(t *testing.T, events <-chan sseEvent, failed <-chan error, bound time.Duration,
	what string, match func(name, data string) bool) {
	t.Helper()
	deadline := time.After(bound)
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			if match(event.name, event.data) {
				t.Fatalf("%s crossed: %s", what, event.data)
			}
		case err := <-failed:
			t.Fatalf("%s: the stream failed: %v", what, err)
		case <-deadline:
			return
		}
	}
}

// TestOneCardPulledFromBothMachinesHasExactlyOneHolder is the race the shared board promises:
// twenty pulls of one card at one moment, half through the hub's own tab and half through the
// satellite's local tab (each relaying onto the hub), and exactly one claim stands — on the
// hub's kanban.db, held by the one actor whose pull answered 200.
func TestOneCardPulledFromBothMachinesHasExactlyOneHolder(t *testing.T) {
	rig := startSharedBoard(t, "SH-1")

	type pull struct {
		actor  string
		status int
		body   string
	}
	pulls := make(chan pull, 20)
	release := make(chan struct{})
	fight := func(base, token, prefix string) {
		for i := 0; i < 10; i++ {
			actor := prefix + strconv.Itoa(i)
			go func() {
				<-release
				status, body, err := postFlow(base, token, rig.key, actor, "SH-1")
				if err != nil {
					status, body = -1, err.Error()
				}
				pulls <- pull{actor: actor, status: status, body: body}
			}()
		}
	}
	fight(rig.hubBase, "hub-token", "h")
	fight(rig.satBase, "sat-token", "s")
	close(release)

	winner, wins, refusals := "", 0, 0
	for answered := 0; answered < 20; answered++ {
		select {
		case one := <-pulls:
			switch one.status {
			case http.StatusOK:
				if !strings.Contains(one.body, "in-progress") {
					t.Fatalf("%s's winning pull = %s, want the card's new column", one.actor, one.body)
				}
				winner, wins = one.actor, wins+1
			case http.StatusConflict:
				refusals++
			default:
				t.Fatalf("%s's pull = %d %s, want 200 or the board's 409", one.actor, one.status, one.body)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the 20 pulls never all answered")
		}
	}
	if wins != 1 || refusals != 19 {
		t.Fatalf("the 20 pulls answered %d wins and %d refusals, want exactly one win and 19 refusals", wins, refusals)
	}

	// the hub's kanban.db holds the card in-progress, held by the winning actor alone
	opened, closeBoard, err := board.Open(context.Background(), board.Board{Root: rig.hubRoot})
	if err != nil {
		t.Fatalf("open the hub's board: %v", err)
	}
	shown, err := opened.Show(context.Background(), "SH-1")
	if err != nil {
		t.Fatalf("show SH-1 on the hub's board: %v", err)
	}
	if err := closeBoard(); err != nil {
		t.Fatalf("close the hub's board: %v", err)
	}
	if shown.Card.Column != "in-progress" || shown.Card.Holder != winner {
		t.Fatalf("SH-1 on the hub's board sits %s held by %q, want in-progress held by %q",
			shown.Card.Column, shown.Card.Holder, winner)
	}

	// the satellite's own copy never moved: no shadow claim stood beside the hub's
	if column := columnOf(t, rig.satRoot, "SH-1"); column != "ready" {
		t.Fatalf("SH-1 sits %s on the satellite's board, want ready", column)
	}
}

// TestBothEventStreamsSeeTheMoveWithinASecond proves the move reaches the pages live: the hub's
// stream carries the satellite's claim within a second of its answer. The satellite's own
// stream reads its local copy — only flow commands relay onto the hub, reads stay local — so it
// keeps showing its own board, where the card never left ready; reads of the shared board
// through the relay are the gap this pin leaves open.
func TestBothEventStreamsSeeTheMoveWithinASecond(t *testing.T) {
	rig := startSharedBoard(t, "SH-1")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hubEvents, hubFailed := openBoardStream(ctx, rig.hubBase, "hub-token", rig.key)
	satEvents, satFailed := openBoardStream(ctx, rig.satBase, "sat-token", rig.key)
	waitSnapshot(t, hubEvents, hubFailed, "the hub's stream")
	waitSnapshot(t, satEvents, satFailed, "the satellite's stream")

	status, body := call(t, rig.satBase, http.MethodPost, "/api/kanban/b/"+rig.key+"/flow", "sat-token", "",
		`{"command":"pull","actor":"w1","id":"SH-1"}`)
	if status != 200 || !strings.Contains(body, "in-progress") {
		t.Fatalf("the satellite's pull = %d %s, want 200 with the card's new column", status, body)
	}

	moved := func(name, data string) bool {
		return name == "delta" && strings.Contains(data, `"id":"SH-1"`) &&
			strings.Contains(data, `"column":"in-progress"`)
	}
	untilEvent(t, hubEvents, hubFailed, time.Second, "the hub's SH-1 move", moved)
	during(t, satEvents, satFailed, 2*time.Second, "the hub's move on the satellite's stream", moved)

	// each machine's page reads its own tab: the hub's board carries the move, the
	// satellite's its unchanged copy
	status, boardBody := call(t, rig.hubBase, http.MethodGet, "/api/kanban/b/"+rig.key+"/api/board", "hub-token", "", "")
	if status != 200 || !strings.Contains(boardBody, `"column":"in-progress"`) {
		t.Fatalf("the hub's board = %d %s, want SH-1 in in-progress", status, boardBody)
	}
	status, boardBody = call(t, rig.satBase, http.MethodGet, "/api/kanban/b/"+rig.key+"/api/board", "sat-token", "", "")
	if status != 200 || !strings.Contains(boardBody, `"column":"ready"`) {
		t.Fatalf("the satellite's own board = %d %s, want SH-1 still in ready", status, boardBody)
	}
}
