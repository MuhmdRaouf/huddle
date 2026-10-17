package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/watch"
	"github.com/MuhmdRaouf/huddle/internal/kanban/web"
)

// The export: one page with the UI inlined and the snapshot plus every ticket
// body embedded, so the board renders with no server and no network.

// exportNow pins the one stamp the page carries.
func exportNow() time.Time {
	return time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("+0300", 3*60*60))
}

// exportedPage is the payload the data script carries.
type exportedPage struct {
	Snapshot watch.Snapshot        `json:"snapshot"`
	Bodies   map[string]ticketBody `json:"bodies"`
}

// payloadOf answers the page's data payload.
func payloadOf(t *testing.T, page string) exportedPage {
	t.Helper()
	const head = "window.__KANBAN__ = "
	start := strings.Index(page, head)
	if start < 0 {
		t.Fatalf("the page carries no data script")
	}
	rest := page[start+len(head):]
	end := strings.Index(rest, ";</script>")
	if end < 0 {
		t.Fatalf("the data script never ends")
	}
	var payload exportedPage
	if err := json.Unmarshal([]byte(rest[:end]), &payload); err != nil {
		t.Fatalf("the data payload is not JSON: %v", err)
	}
	return payload
}

// referencedAssets answers the asset names the built page references.
func referencedAssets(t *testing.T) []string {
	t.Helper()
	page, err := fs.ReadFile(web.FS(), "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	referenced := regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	var names []string
	for _, match := range referenced.FindAllStringSubmatch(string(page), -1) {
		names = append(names, strings.TrimPrefix(match[1], "./"))
	}
	if len(names) == 0 {
		t.Fatal("index.html references no asset")
	}
	return names
}

func TestExportPage(t *testing.T) {
	t.Run("inlines the UI with no external reference", func(t *testing.T) {
		b := newBoard(t)
		writeTicket(t, b, "backlog", "X-ONE")
		writeTicket(t, b, "ready", "X-TWO")
		page, count, err := ExportPage(context.Background(), b, exportNow)
		if err != nil {
			t.Fatalf("ExportPage: %v", err)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2", count)
		}
		html := string(page)
		for _, banned := range []string{"<script src=", "<link href=", `src="./assets`, `href="./assets`} {
			if strings.Contains(html, banned) {
				t.Errorf("the page still references %q", banned)
			}
		}
		if got := strings.Count(html, "<script"); got != 2 {
			t.Errorf("the page holds %d scripts, want exactly the data script and the app script", got)
		}
		if got := strings.Count(html, "</script>"); got != 2 {
			t.Errorf("the page holds %d script ends, want 2", got)
		}
		if !strings.Contains(html, "<style>") {
			t.Error("the stylesheet is not inlined")
		}
		// the data script runs before the app script reads it
		data := strings.Index(html, "window.__KANBAN__")
		app := strings.Index(html, `<script type="module">`)
		if data < 0 || app < 0 || data > app {
			t.Errorf("the data script at %d must sit before the app script at %d", data, app)
		}
		// every asset the built page references sits in the page whole
		for _, name := range referencedAssets(t) {
			asset, err := fs.ReadFile(web.FS(), name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			if !strings.Contains(html, string(asset)) {
				t.Errorf("the page does not hold %s whole", name)
			}
		}
	})

	t.Run("embeds every ticket id, card and body", func(t *testing.T) {
		b := newBoard(t)
		writeTicket(t, b, "backlog", "X-ONE")
		writeTicket(t, b, "done", "X-TWO")
		page, _, err := ExportPage(context.Background(), b, exportNow)
		if err != nil {
			t.Fatalf("ExportPage: %v", err)
		}
		payload := payloadOf(t, string(page))
		if len(payload.Snapshot.Cards) != 2 {
			t.Fatalf("the snapshot holds %d cards, want 2", len(payload.Snapshot.Cards))
		}
		for _, id := range []string{"X-ONE", "X-TWO"} {
			shown, err := b.Show(context.Background(), id)
			if err != nil {
				t.Fatalf("show %s: %v", id, err)
			}
			ticket, ok := payload.Bodies[id]
			if !ok {
				t.Errorf("no body embedded for %s", id)
				continue
			}
			if ticket.Card.ID != id || ticket.Card.Title != shown.Card.Title {
				t.Errorf("%s card = %s %q, want the board's card", id, ticket.Card.ID, ticket.Card.Title)
			}
			if ticket.Body != shown.Body {
				t.Errorf("%s body = %q, want the board's body", id, ticket.Body)
			}
			if ticket.Path != shown.Path {
				t.Errorf("%s path = %q, want %q", id, ticket.Path, shown.Path)
			}
		}
		// the snapshot the payload carries is the watcher's own shape
		if payload.Snapshot.Project == "" || len(payload.Snapshot.Columns) != len(board.Columns) {
			t.Errorf("the snapshot's project or columns are missing: %+v", payload.Snapshot)
		}
	})

	t.Run("a body holding </script> and <!-- cannot break out", func(t *testing.T) {
		b := newBoard(t)
		hostile := "</script><script>alert(1)</script> and <!-- a comment -->"
		text := "---\nid: X-EVIL\ntitle: \"evil\"\ntype: docs\ncluster: kanban\nsummary: \"one\"\n---\n\n" +
			"# X-EVIL\n\n" + hostile + "\n"
		if err := os.WriteFile(filepath.Join(b.Root, "backlog", "X-EVIL.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		page, count, err := ExportPage(context.Background(), b, exportNow)
		if err != nil {
			t.Fatalf("ExportPage: %v", err)
		}
		if count != 1 {
			t.Errorf("count = %d, want 1", count)
		}
		html := string(page)
		if got := strings.Count(html, "<script"); got != 2 {
			t.Errorf("the page holds %d scripts, want exactly the two we wrote", got)
		}
		shown, err := b.Show(context.Background(), "X-EVIL")
		if err != nil {
			t.Fatalf("show X-EVIL: %v", err)
		}
		payload := payloadOf(t, html)
		if got := payload.Bodies["X-EVIL"].Body; got != shown.Body {
			t.Errorf("the body came back as %q, want %q", got, shown.Body)
		}
		if !strings.Contains(payload.Bodies["X-EVIL"].Body, hostile) {
			t.Errorf("the hostile body lost its text: %q", payload.Bodies["X-EVIL"].Body)
		}
	})

	t.Run("an empty board exports a page with zero tickets", func(t *testing.T) {
		b := newBoard(t)
		page, count, err := ExportPage(context.Background(), b, exportNow)
		if err != nil {
			t.Fatalf("ExportPage: %v", err)
		}
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
		payload := payloadOf(t, string(page))
		if len(payload.Bodies) != 0 {
			t.Errorf("bodies = %v, want none", payload.Bodies)
		}
		for _, col := range payload.Snapshot.Columns {
			if col.Count != 0 {
				t.Errorf("column %s counts %d, want 0", col.ID, col.Count)
			}
		}
	})
}

// TestExportPageNeedsItsAssets: an asset missing from the build is a build
// failure, never a page that still reaches the network.
func TestExportPageNeedsItsAssets(t *testing.T) {
	b := newBoard(t)
	writeTicket(t, b, "backlog", "X-ONE")
	broken := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(
		`<html><head><script type="module" src="./assets/gone.js"></script></head></html>`,
	)}}
	if _, _, err := exportPage(context.Background(), b, exportNow, broken); err == nil {
		t.Fatal("a missing asset exported anyway")
	}
}
