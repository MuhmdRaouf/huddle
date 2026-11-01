package server

// The join op's session link: a join that landed keeps the Claude session id it carried even
// when the client that sent it gave up before the answer came back. The test drives the route
// in this process and holds SQLite's write lock from the moment the join's own writes are
// committed, so the handler is left with its link write alone while the request's context dies.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// joinLinkChannel opens one channel over a fresh SQLite file and names the file, so the test
// can hold a second connection to it.
func joinLinkChannel(t *testing.T, name string) (*channel.Channel, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "t.db")
	st, err := store.Open(bg, file)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ch, err := channel.Open(bg, name, st)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close(bg) })
	return ch, file
}

// joinLinkAttempt sends one join whose context the test cancels once the handler has reached
// its link write and found it blocked: the join's own writes are committed and SQLite's write
// lock is the test's, so the handler is holding nothing but that write. It answers whether the
// run was a fair one (the lock was the write's next step) and whether the link survived.
func joinLinkAttempt(t *testing.T, ch *channel.Channel, file, member, claude string) (fair, linked bool) {
	t.Helper()
	c2, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer c2.Close()

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	body := fmt.Sprintf(`{"role": "worker", "claude_session": %q}`, claude)
	req := httptest.NewRequest(http.MethodPost, "/api/c/shop/op/join?as="+member, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s := &server{auth: auth.New("root-credential", fixedClock(), nil, ""), o: Options{Stdout: io.Discard, Stderr: io.Discard}}
	done := make(chan struct{})
	var routeErr error
	go func() {
		defer close(done)
		routeErr = s.channelRoute(rec, req, ch, "op/join", queryOf("as="+member))
	}()

	// the join's last write moves the member's cursor; the moment it has landed, take SQLite's
	// write lock, so the handler's link write cannot start
	deadline := time.Now().Add(10 * time.Second)
	locked := false
	for time.Now().Before(deadline) {
		var cursor int64
		if err := c2.QueryRow("SELECT COALESCE(cursor, 0) FROM sessions WHERE name = ?", member).Scan(&cursor); err == nil && cursor > 0 {
			if _, err := c2.Exec("BEGIN IMMEDIATE"); err != nil {
				t.Fatalf("take the write lock: %v", err)
			}
			locked = true
			break
		}
		runtime.Gosched()
	}
	if !locked {
		t.Fatal("the join never landed")
	}
	defer func() {
		_, _ = c2.Exec("ROLLBACK")
	}()

	// the link write is the only write that can follow the join, and it holds the store's call
	// lock while it waits for SQLite's: a probe read that answers at once says the handler has
	// not reached its write yet, and a probe read that hangs says the write is parked on the
	// lock this connection holds
	link := make(chan linkProbe, 1)
	probe := func() {
		links, err := channel.Links(bg, ch)
		if err != nil {
			link <- linkProbe{err: err}
			return
		}
		link <- linkProbe{landed: links[claude] != ""}
	}
	deadline = time.Now().Add(5 * time.Second)
	parked := false
	for time.Now().Before(deadline) {
		go probe()
		select {
		case <-time.After(250 * time.Millisecond):
			// the read is stuck behind the store's call lock: the write holds it, parked on
			// SQLite's, so the handler is inside its link
			parked = true
		case p := <-link:
			if p.err != nil {
				t.Fatalf("read the links: %v", p.err)
			}
			if p.landed {
				return false, true // the write outran the lock: not a fair run
			}
		}
		if parked {
			break
		}
		runtime.Gosched()
	}
	if !parked {
		return false, false
	}

	// the client gives up; the handler is left with its link write alone
	cancel()
	if _, err := c2.Exec("COMMIT"); err != nil {
		t.Fatalf("release the write lock: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the join route never came back")
	}
	if routeErr != nil {
		t.Fatalf("the join route: %v", routeErr)
	}
	if rec.Code != 200 {
		t.Fatalf("the join answered %d: %s", rec.Code, rec.Body.String())
	}
	links, err := channel.Links(bg, ch)
	if err != nil {
		t.Fatalf("read the links: %v", err)
	}
	return true, links[claude] == member
}

// linkProbe is one probe read's answer: whether the member already carries the Claude session
// id, and the read's failure when it has one.
type linkProbe struct {
	landed bool
	err    error
}

// TestAJoinThatLandedKeepsItsLinkWhenTheClientGaveUp pins the link to the landed join: the op
// answered and its writes are in, so the member carries the Claude session id even though the
// request's context died before the link write ran.
func TestAJoinThatLandedKeepsItsLinkWhenTheClientGaveUp(t *testing.T) {
	ch, file := joinLinkChannel(t, "shop")
	// a fair run is the usual one; the retries cover the rare run where the handler's link
	// write outran the lock, which says nothing about the behaviour under test
	for attempt := 1; attempt <= 3; attempt++ {
		member, claude := "api", "claude-api"
		if attempt > 1 {
			member, claude = fmt.Sprintf("api-%d", attempt), fmt.Sprintf("claude-api-%d", attempt)
		}
		fair, linked := joinLinkAttempt(t, ch, file, member, claude)
		if !fair {
			continue
		}
		if !linked {
			t.Fatalf("the join landed and the op answered, but %s carries no Claude session id", member)
		}
		return
	}
	t.Fatal("no run held the write lock before the link write")
}
