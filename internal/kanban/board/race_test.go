package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// The race suite: db boards worked by several sessions at once. Every writing
// command runs inside one immediate transaction, so two sessions pulling at
// the same moment never take one card and a refused command writes nothing.

// seedAt stamps every seeded row.
var seedAt = fixedNow.UTC().Format(time.RFC3339Nano)

// openDBBoard inits a db board at a fresh root and opens it; closeBoard
// releases the store when the test ends.
func openDBBoard(t *testing.T) (Board, func() error) {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), ".agents", "kanban")
	if _, err := InitDB(ctx, root, fixedNow); err != nil {
		t.Fatalf("init the db board: %v", err)
	}
	b, closeBoard, err := Open(ctx, Board{Root: root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open the db board: %v", err)
	}
	return b, closeBoard
}

// seedReady writes one ready row carrying the least card text Pull reads.
func seedReady(t *testing.T, b Board, id string) {
	t.Helper()
	text := "---\nid: " + id + "\ntitle: \"" + strings.ToLower(id) + "\"\n---\n\nBody.\n"
	if err := b.Store.Put(context.Background(), store.Ticket{
		ID: id, Column: "ready", Text: text, UpdatedAt: seedAt,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestConcurrentPullsNeverShareACard(t *testing.T) {
	ctx := context.Background()
	b, closeBoard := openDBBoard(t)
	defer func() { _ = closeBoard() }()
	for i := range 8 {
		seedReady(t, b, fmt.Sprintf("RACE-%d", i))
	}

	// eight sessions, each through its own Open of one board, pulling at the
	// same moment
	const sessions = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	boards := make([]Board, sessions)
	held := make([][]string, sessions)
	pullErrs := make([]error, sessions)
	for i := range sessions {
		inner, closeInner, err := Open(ctx, Board{Root: b.Root, Now: func() time.Time { return fixedNow }})
		if err != nil {
			t.Fatalf("open session %d: %v", i, err)
		}
		t.Cleanup(func() { _ = closeInner() })
		boards[i] = inner
	}
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			held[i], pullErrs[i] = boards[i].Pull(ctx, Args{Actor: fmt.Sprintf("glm-%d", i)})
		}(i)
	}
	close(start)
	wg.Wait()

	ids := map[string]string{} // ticket id -> the actor that pulled it
	for i := range sessions {
		if pullErrs[i] != nil {
			t.Fatalf("session %d's pull: %v", i, pullErrs[i])
		}
		if len(held[i]) == 0 {
			t.Fatalf("session %d's pull answered no path", i)
		}
		id := strings.TrimSuffix(filepath.Base(held[i][len(held[i])-1]), ".md")
		if other, dup := ids[id]; dup {
			t.Fatalf("sessions %s and %d both pulled %s", other, i, id)
		}
		ids[id] = fmt.Sprintf("glm-%d", i)
	}
	if len(ids) != sessions {
		t.Fatalf("%d tickets pulled, want %d", len(ids), sessions)
	}
	for id, actor := range ids {
		row, err := b.Store.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if row.Column != "in-progress" {
			t.Fatalf("%s is in %s, want in-progress", id, row.Column)
		}
		if holder := fieldOfTextOr(row.Text, "claimed_by"); holder != actor {
			t.Fatalf("%s is held by %q, want exactly %q", id, holder, actor)
		}
	}
}

func TestConcurrentPullsOfOneCardGiveItToOne(t *testing.T) {
	ctx := context.Background()
	b, closeBoard := openDBBoard(t)
	defer func() { _ = closeBoard() }()
	seedReady(t, b, "SOLO-1")

	const sessions = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	boards := make([]Board, sessions)
	pullErrs := make([]error, sessions)
	for i := range sessions {
		inner, closeInner, err := Open(ctx, Board{Root: b.Root, Now: func() time.Time { return fixedNow }})
		if err != nil {
			t.Fatalf("open session %d: %v", i, err)
		}
		t.Cleanup(func() { _ = closeInner() })
		boards[i] = inner
	}
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, pullErrs[i] = boards[i].Pull(ctx, Args{Actor: fmt.Sprintf("glm-%d", i), ID: "SOLO-1"})
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for i := range sessions {
		if pullErrs[i] == nil {
			wins++
			continue
		}
		if !errors.As(pullErrs[i], new(*Refused)) {
			t.Fatalf("session %d's pull = %v, want a *Refused", i, pullErrs[i])
		}
	}
	if wins != 1 {
		t.Fatalf("%d of %d pulls took SOLO-1, want exactly 1", wins, sessions)
	}
	row, err := b.Store.Get(ctx, "SOLO-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Column != "in-progress" {
		t.Fatalf("SOLO-1 is in %s, want in-progress", row.Column)
	}
	holder := fieldOfTextOr(row.Text, "claimed_by")
	if !strings.HasPrefix(holder, "glm-") {
		t.Fatalf("SOLO-1 is held by %q, want one of the racing actors", holder)
	}
}

func TestARefusedCommandWritesNothing(t *testing.T) {
	ctx := context.Background()
	b, closeBoard := openDBBoard(t)
	defer func() { _ = closeBoard() }()
	text := "---\nid: ROLL-1\ntitle: \"rollback\"\ntype: docs\n---\n\nBody.\n"
	if err := b.Store.Put(ctx, store.Ticket{
		ID: "ROLL-1", Column: "testing", Text: text, UpdatedAt: seedAt,
	}); err != nil {
		t.Fatalf("seed ROLL-1: %v", err)
	}
	before, err := b.Store.All(ctx)
	if err != nil {
		t.Fatalf("read the rows before: %v", err)
	}

	// dropping the meta table makes the LOG append fail after the move the
	// command would have landed: the refusal must take the move back with it
	file := filepath.Join(b.Root, "kanban.db")
	raw, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open %s raw: %v", file, err)
	}
	if _, err := raw.Exec("DROP TABLE meta"); err != nil {
		t.Fatalf("drop the meta table: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close the raw handle: %v", err)
	}

	_, err = b.Done(ctx, Args{Actor: "verifier", ID: "ROLL-1", Note: "shipped"})
	if !errors.As(err, new(*Refused)) {
		t.Fatalf("Done = %v, want a *Refused from the failed LOG append", err)
	}
	after, err := b.Store.All(ctx)
	if err != nil {
		t.Fatalf("read the rows after: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused Done changed the rows:\nbefore %+v\nafter  %+v", before, after)
	}
}

// A closed feature's Done is refused on a db board too, and the refused
// command leaves every row as it was.
func TestAClosedFeatureRefusesDoneAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	b, closeBoard := openDBBoard(t)
	defer func() { _ = closeBoard() }()
	text := "---\nid: ROLL-2\ntitle: \"closed\"\ncluster: swr\ntype: docs\n---\n\nBody.\n"
	if err := b.Store.Put(ctx, store.Ticket{
		ID: "ROLL-2", Column: "testing", Text: text, UpdatedAt: seedAt,
	}); err != nil {
		t.Fatalf("seed ROLL-2: %v", err)
	}
	if _, err := b.CloseFeature(ctx, Args{Actor: "lead", ID: "swr"}); err != nil {
		t.Fatalf("CloseFeature: %v", err)
	}
	before, err := b.Store.All(ctx)
	if err != nil {
		t.Fatalf("read the rows before: %v", err)
	}

	_, err = b.Done(ctx, Args{Actor: "verifier", ID: "ROLL-2", Note: "shipped"})
	if !errors.As(err, new(*Refused)) {
		t.Fatalf("Done = %v, want a *Refused naming the closed feature", err)
	}
	if msg := refuseErr(t, err); !strings.HasPrefix(msg, "Feature closed: swr") {
		t.Fatalf("refusal = %q, want one starting \"Feature closed: swr\"", msg)
	}
	after, err := b.Store.All(ctx)
	if err != nil {
		t.Fatalf("read the rows after: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused Done changed the rows:\nbefore %+v\nafter  %+v", before, after)
	}
}
