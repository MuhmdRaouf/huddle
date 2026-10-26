package convo

// The reader turns a Claude Code transcript's lines into conversation items from a byte offset:
// the committed fixture pins the order and the ids, a torn last line waits for its newline, and
// a read never takes more than MaxRead bytes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fixtureItems is what the committed fixture yields, in order: the summary, sidechain, meta and
// broken lines yield nothing, each thinking block yields its thinking item with the line's
// duration when the line names one, and the 5000-é text is cut to 2048 whole characters, which
// are exactly MaxText bytes.
func fixtureItems() []Item {
	return []Item{
		{ID: "u1:0", Kind: KindUser, At: "2026-10-26T10:00:01.000Z", Text: "run the tests"},
		{ID: "a1:0", Kind: KindAssistant, At: "2026-10-26T10:00:02.000Z", Text: "Running them."},
		{ID: "a1:1", Kind: KindToolUse, At: "2026-10-26T10:00:02.000Z", Tool: "Bash", ToolUseID: "toolu_1", Input: `{"command":"go test ./..."}`},
		{ID: "u2:0", Kind: KindToolResult, At: "2026-10-26T10:00:03.000Z", ToolUseID: "toolu_1", Text: "ok  \tpkg"},
		{ID: "u3:0", Kind: KindToolResult, At: "2026-10-26T10:00:04.000Z", ToolUseID: "toolu_2", IsError: true, Text: "exit 1"},
		{ID: "a3:0", Kind: KindThinking, At: "2026-10-26T10:00:07.000Z", Text: "which package"},
		{ID: "a3:1", Kind: KindAssistant, At: "2026-10-26T10:00:07.000Z", Text: "Done."},
		{ID: "a5:0", Kind: KindThinking, At: "2026-10-26T10:00:07.500Z", Text: "checking the failing package", DurationMS: 2400},
		{ID: "a4:0", Kind: KindAssistant, At: "2026-10-26T10:00:08.000Z", Text: strings.Repeat("é", 2048), Cut: true},
	}
}

func fixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "claude.jsonl")
}

func checkItems(t *testing.T, got, want []Item) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d:\ngot  %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestTheReaderAnswersTheConversationItems(t *testing.T) {
	page, err := Read(context.Background(), fixturePath(t), 0)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	checkItems(t, page.Items, fixtureItems())

	last := page.Items[len(page.Items)-1]
	if len(last.Text) > MaxText {
		t.Errorf("the cut text is %d bytes, want at most %d", len(last.Text), MaxText)
	}
	if !utf8.ValidString(last.Text) {
		t.Errorf("the cut text is not valid UTF-8: %q", last.Text[len(last.Text)-8:])
	}

	info, err := os.Stat(fixturePath(t))
	if err != nil {
		t.Fatalf("stat the fixture: %v", err)
	}
	if page.Next != info.Size() {
		t.Errorf("the read stops at %d, want the file's %d bytes", page.Next, info.Size())
	}
}

func TestTheReaderResumesAtItsCursor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	full, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	// a torn last line: half a JSON line, no newline yet
	line := `{"type":"user","uuid":"u9","timestamp":"2026-10-26T10:09:09.000Z","message":{"role":"user","content":"picked up"}}` + "\n"
	torn := line[:len(line)/2]

	if err := os.WriteFile(path, append(append([]byte{}, full...), torn...), 0o600); err != nil {
		t.Fatalf("write the copy: %v", err)
	}

	first, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	checkItems(t, first.Items, fixtureItems())
	if first.Next != int64(len(full)) {
		t.Fatalf("the first read stops at %d, want the complete lines' %d", first.Next, len(full))
	}

	second, err := Read(ctx, path, first.Next)
	if err != nil {
		t.Fatalf("read the torn tail: %v", err)
	}
	if len(second.Items) != 0 {
		t.Errorf("the torn tail answered %d items, want none", len(second.Items))
	}
	if second.Items == nil {
		t.Errorf("the torn tail's items are nil, want an empty slice")
	}
	if second.Next != first.Next {
		t.Errorf("the torn tail moves the cursor to %d, want it held at %d", second.Next, first.Next)
	}

	if err := os.WriteFile(path, append(append([]byte{}, full...), line...), 0o600); err != nil {
		t.Fatalf("write the completed line: %v", err)
	}

	third, err := Read(ctx, path, first.Next)
	if err != nil {
		t.Fatalf("read the completed line: %v", err)
	}
	checkItems(t, third.Items, []Item{
		{ID: "u9:0", Kind: KindUser, At: "2026-10-26T10:09:09.000Z", Text: "picked up"},
	})
	if third.Next != int64(len(full)+len(line)) {
		t.Errorf("the completed read stops at %d, want the new size %d", third.Next, len(full)+len(line))
	}
}

func TestTheReaderStopsPastMaxRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "big.jsonl")
	const lines = 300
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, `{"type":"user","uuid":"big%03d","timestamp":"2026-10-26T11:00:00.000Z","message":{"role":"user","content":"line %03d %s"}}`+"\n", i, i, strings.Repeat("x", 8192))
	}
	body := []byte(b.String())
	if len(body) <= 2*1024*1024 {
		t.Fatalf("the transcript is %d bytes, want over 2 MiB", len(body))
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}

	first, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	if first.Next <= MaxRead {
		t.Errorf("the first read took %d bytes, want more than MaxRead (%d)", first.Next, MaxRead)
	}
	if first.Next >= int64(len(body)) {
		t.Errorf("the first read took %d bytes, want less than the file's %d", first.Next, len(body))
	}

	items := first.Items
	next := first.Next
	for {
		page, err := Read(ctx, path, next)
		if err != nil {
			t.Fatalf("read at %d: %v", next, err)
		}
		items = append(items, page.Items...)
		if page.Next == next {
			break
		}
		next = page.Next
	}
	if len(items) != lines {
		t.Fatalf("reading on answered %d items, want one per line (%d)", len(items), lines)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.ID] {
			t.Fatalf("the item %s came twice", item.ID)
		}
		seen[item.ID] = true
	}
	if items[0].ID != "big000:0" || items[lines-1].ID != "big299:0" {
		t.Errorf("the items run %s to %s, want big000:0 to big299:0", items[0].ID, items[lines-1].ID)
	}
}

func TestReadAgentKeepsSidechainLines(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agent-x9.jsonl")
	body := strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","isSidechain":true,"message":{"role":"user","content":"scan the files"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:02.000Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"Scanned."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	main, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read as a main transcript: %v", err)
	}
	if len(main.Items) != 0 {
		t.Errorf("the main read answered %d sidechain items, want none", len(main.Items))
	}

	agent, err := ReadAgent(ctx, path, 0)
	if err != nil {
		t.Fatalf("read as an agent transcript: %v", err)
	}
	checkItems(t, agent.Items, []Item{
		{ID: "u1:0", Kind: KindUser, At: "2026-10-26T10:00:01.000Z", Text: "scan the files"},
		{ID: "a1:0", Kind: KindAssistant, At: "2026-10-26T10:00:02.000Z", Text: "Scanned."},
	})
	if agent.Next != int64(len(body)) {
		t.Errorf("the agent read stops at %d, want the file's %d bytes", agent.Next, len(body))
	}
}

func TestTheReaderAnswersTheMissingFile(t *testing.T) {
	_, err := Read(context.Background(), filepath.Join(t.TempDir(), "gone.jsonl"), 0)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the missing file answered %v, want a not-exist error", err)
	}
}

func TestTheReaderStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Read(ctx, fixturePath(t), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled read answered %v, want context.Canceled", err)
	}
}

func TestTheReaderCutsLongThinking(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	line := fmt.Sprintf(`{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":%q,"signature":"sig-a1"}]}}`+"\n", strings.Repeat("é", 5000))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "a1:0", Kind: KindThinking, At: "2026-10-26T10:00:02.000Z", Text: strings.Repeat("é", 2048), Cut: true},
	})
}

func TestTheReaderSkipsAnEmptyThinkingBlock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	line := `{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig-a1"},{"type":"text","text":"Done."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "a1:1", Kind: KindAssistant, At: "2026-10-26T10:00:02.000Z", Text: "Done."},
	})
}

func TestTheReaderNeverServesTheThinkingSignature(t *testing.T) {
	page, err := Read(context.Background(), fixturePath(t), 0)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	served, err := json.Marshal(page.Items)
	if err != nil {
		t.Fatalf("marshal the items: %v", err)
	}
	if strings.Contains(string(served), "sig-a5") {
		t.Errorf("the items carry the thinking's signature: %s", served)
	}
}
