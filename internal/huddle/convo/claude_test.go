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
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// fixtureItems is what the committed fixture yields, in order: the summary, sidechain, meta and
// broken lines yield nothing, each thinking block yields its thinking item with the line's
// duration when the line names one, the 5000-é text is cut to 2048 whole characters, which are
// exactly MaxText bytes, the prompt's image and attached files come out as references
// beside it while the paste attachment yields nothing, and each tool answer carries the facts
// its line's toolUseResult names.
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
		{ID: "u6:0", Kind: KindUser, At: "2026-10-26T10:00:09.000Z", Text: "what does this show"},
		{ID: "u6:1", Kind: KindImage, At: "2026-10-26T10:00:09.000Z", MediaType: "image/png", Bytes: 5},
		{ID: "f1:0", Kind: KindFile, At: "2026-10-26T10:00:10.000Z", Parent: "u6", Path: "spec.md", Lines: 2, Text: "line one\nline two\n"},
		{ID: "f2:0", Kind: KindFile, At: "2026-10-26T10:00:11.000Z", Parent: "f1", Path: "notes.log"},
		{ID: "a6:0", Kind: KindToolUse, At: "2026-10-26T10:00:13.000Z", Tool: "Bash", ToolUseID: "toolu_3", Input: `{"command":"go test ./..."}`},
		{ID: "u7:0", Kind: KindToolResult, At: "2026-10-26T10:00:14.000Z", ToolUseID: "toolu_3", IsError: true,
			Text:   "Exit code 2\nFAIL example.com/pkg\n",
			Result: &ToolFacts{ExitCode: intPtr(2), Stderr: "FAIL example.com/pkg\n"}},
		{ID: "a7:0", Kind: KindToolUse, At: "2026-10-26T10:00:15.000Z", Tool: "Bash", ToolUseID: "toolu_4", Input: `{"command":"bun run dev","run_in_background":true}`},
		{ID: "u8:0", Kind: KindToolResult, At: "2026-10-26T10:00:16.000Z", ToolUseID: "toolu_4", Text: "dev server up\n",
			Result: &ToolFacts{BackgroundID: "b04niotyy"}},
		{ID: "a8:0", Kind: KindToolUse, At: "2026-10-26T10:00:17.000Z", Tool: "Edit", ToolUseID: "toolu_5", Input: `{"file_path":"/abs/a.go","old_string":"keep me","new_string":"kept"}`},
		{ID: "u9:0", Kind: KindToolResult, At: "2026-10-26T10:00:18.000Z", ToolUseID: "toolu_5", Text: "The file /abs/a.go has been updated.",
			Result: &ToolFacts{
				FilePath: "/abs/a.go",
				Hunks: []Hunk{
					{OldStart: 10, OldLines: 3, NewStart: 10, NewLines: 4, Lines: []string{" keep", "-old", "+new", "+added", " keep"}},
					{OldStart: 40, OldLines: 2, NewStart: 41, NewLines: 2, Lines: []string{" keep", "+grown"}},
				},
			}},
		{ID: "a9:0", Kind: KindToolUse, At: "2026-10-26T10:00:19.000Z", Tool: "AskUserQuestion", ToolUseID: "toolu_6",
			Input: `{"questions":[{"question":"Which port?","header":"Port","multiSelect":false,"options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]},{"question":"Ship now?","header":"Ship","multiSelect":false,"options":[{"label":"yes","description":"now"}]}]}`},
		{ID: "u10:0", Kind: KindToolResult, At: "2026-10-26T10:00:20.000Z", ToolUseID: "toolu_6", Text: "The user answered the questions.",
			Result: &ToolFacts{
				Answers: []Answer{
					{Header: "Port", Question: "Which port?", Answer: "A"},
					{Header: "Ship", Question: "Ship now?", Answer: "yes"},
				},
			}},
	}
}

// intPtr answers a pointer to n, for facts that carry an exit code only when one is named.
func intPtr(n int) *int { return &n }

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
		if !sameItem(got[i], want[i]) {
			t.Errorf("item %d:\ngot  %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

// sameItem answers whether two items are the same: the rest compares with ==, while the facts
// compare by value, since ToolFacts carries slices and each expected item holds its own copy.
func sameItem(got, want Item) bool {
	if (got.Result == nil) != (want.Result == nil) {
		return false
	}
	if got.Result != nil && !reflect.DeepEqual(*got.Result, *want.Result) {
		return false
	}
	got.Result, want.Result = nil, nil
	return got == want
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

func TestTheReaderNeverServesImageData(t *testing.T) {
	page, err := Read(context.Background(), fixturePath(t), 0)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	for _, item := range page.Items {
		if item.Kind == KindImage && item.Text != "" {
			t.Errorf("the image item %s carries text %q, want none", item.ID, item.Text)
		}
	}
	served, err := json.Marshal(page.Items)
	if err != nil {
		t.Fatalf("marshal the items: %v", err)
	}
	if strings.Contains(string(served), "aGVsbG8=") {
		t.Errorf("the items carry an image's base64 data: %s", served)
	}
	if strings.Contains(string(served), `"data"`) {
		t.Errorf("the items carry a data field: %s", served)
	}
}

func TestTheImageItemSizesTheDataFromItsLengthAlone(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	imageLine := func(uuid, data string) string {
		return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":%q,"data":%q}}]}}`+"\n", uuid, "image/png", data)
	}
	body := strings.Join([]string{
		imageLine("i1", "aGV5"),     // no padding: 3 bytes
		imageLine("i2", "aGVsbG8="), // one pad byte: 5 bytes
		imageLine("i3", "aGVsbA=="), // two pad bytes: 4 bytes
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "i1:0", Kind: KindImage, At: "2026-10-26T10:00:01.000Z", MediaType: "image/png", Bytes: 3},
		{ID: "i2:0", Kind: KindImage, At: "2026-10-26T10:00:01.000Z", MediaType: "image/png", Bytes: 5},
		{ID: "i3:0", Kind: KindImage, At: "2026-10-26T10:00:01.000Z", MediaType: "image/png", Bytes: 4},
	})
}

func TestTheReaderCutsALongAttachedFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	line := fmt.Sprintf(`{"type":"attachment","uuid":"f9","parentUuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","attachment":{"type":"file","filename":"/abs/path/big.md","displayPath":"big.md","content":{"type":"text","file":{"filePath":"/abs/path/big.md","content":%q,"numLines":5000,"startLine":1,"totalLines":5000}}}}`+"\n", strings.Repeat("é", 5000))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "f9:0", Kind: KindFile, At: "2026-10-26T10:00:01.000Z", Parent: "u1", Path: "big.md", Lines: 5000, Text: strings.Repeat("é", 2048), Cut: true},
	})
}

func TestTheFileItemCountsTheLinesTheReadNames(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	fileLine := func(uuid string, total, num int) string {
		return fmt.Sprintf(`{"type":"attachment","uuid":%q,"parentUuid":"u1","timestamp":"2026-10-26T10:00:01.000Z","attachment":{"type":"file","filename":"/abs/path/spec.md","displayPath":"spec.md","content":{"type":"text","file":{"filePath":"/abs/path/spec.md","content":"line one\n","numLines":%d,"startLine":1,"totalLines":%d}}}}`+"\n", uuid, num, total)
	}
	body := strings.Join([]string{
		fileLine("f1", 9, 3), // totalLines wins
		fileLine("f2", 0, 3), // numLines when the read names no total
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "f1:0", Kind: KindFile, At: "2026-10-26T10:00:01.000Z", Parent: "u1", Path: "spec.md", Lines: 9, Text: "line one\n"},
		{ID: "f2:0", Kind: KindFile, At: "2026-10-26T10:00:01.000Z", Parent: "u1", Path: "spec.md", Lines: 3, Text: "line one\n"},
	})
}

func TestTheReaderCutsHunksPastMaxHunkLines(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	hunkJSON := func(oldStart, newStart, count int) string {
		return fmt.Sprintf(`{"oldStart":%d,"oldLines":%d,"newStart":%d,"newLines":%d,"lines":%s}`,
			oldStart, count, newStart, count, quotedHunkLines(count))
	}
	editLine := func(uuid, patch string) string {
		return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-10-26T10:00:01.000Z","toolUseResult":{"filePath":"/abs/big.go","structuredPatch":[%s]},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"updated"}]}}`,
			uuid, patch)
	}
	body := strings.Join([]string{
		editLine("e1", hunkJSON(1, 1, 500)),                             // one hunk of 500 lines: its first 400 stay and the facts are marked
		editLine("e2", hunkJSON(1, 1, 300)+","+hunkJSON(100, 101, 300)), // the first stays whole and the second keeps its first 100
		editLine("e3", hunkJSON(1, 1, 400)+","+hunkJSON(100, 101, 1)),   // the limit filled: the second hunk goes and the facts are marked
		editLine("e4", hunkJSON(1, 1, 400)),                             // exactly 400: the hunk stays whole, unmarked
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "e1:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", Text: "updated",
			Result: &ToolFacts{
				FilePath: "/abs/big.go",
				Hunks:    []Hunk{{OldStart: 1, OldLines: 500, NewStart: 1, NewLines: 500, Lines: keptHunkLines(400)}},
				Cut:      true,
			}},
		{ID: "e2:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", Text: "updated",
			Result: &ToolFacts{
				FilePath: "/abs/big.go",
				Hunks: []Hunk{
					{OldStart: 1, OldLines: 300, NewStart: 1, NewLines: 300, Lines: keptHunkLines(300)},
					{OldStart: 100, OldLines: 300, NewStart: 101, NewLines: 300, Lines: keptHunkLines(100)},
				},
				Cut: true,
			}},
		{ID: "e3:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", Text: "updated",
			Result: &ToolFacts{
				FilePath: "/abs/big.go",
				Hunks:    []Hunk{{OldStart: 1, OldLines: 400, NewStart: 1, NewLines: 400, Lines: keptHunkLines(400)}},
				Cut:      true,
			}},
		{ID: "e4:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", Text: "updated",
			Result: &ToolFacts{
				FilePath: "/abs/big.go",
				Hunks:    []Hunk{{OldStart: 1, OldLines: 400, NewStart: 1, NewLines: 400, Lines: keptHunkLines(400)}},
			}},
	})
}

// keptHunkLines answers n hunk lines of one kept context line each, as an edit's hunk carries them.
func keptHunkLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = " kept"
	}
	return lines
}

// quotedHunkLines answers n kept hunk lines as they sit in a transcript line's JSON.
func quotedHunkLines(n int) string {
	quoted := make([]string, n)
	for i := range quoted {
		quoted[i] = strconv.Quote(" kept")
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func TestAStringToolUseResultGivesNoFacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	line := `{"type":"user","uuid":"s1","timestamp":"2026-10-26T10:00:01.000Z","toolUseResult":"Edited /abs/a.go","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "s1:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", Text: "done"},
	})
}

func TestTheReaderNeverServesAnEditContents(t *testing.T) {
	page, err := Read(context.Background(), fixturePath(t), 0)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	var served string
	for _, item := range page.Items {
		if item.ID != "u9:0" {
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal the edit's answer: %v", err)
		}
		served = string(raw)
	}
	if served == "" {
		t.Fatalf("the fixture answers no edit result u9:0")
	}
	if strings.Contains(served, "package a") {
		t.Errorf("the edit's answer carries the originalFile text: %s", served)
	}
	if strings.Contains(served, "keep me") {
		t.Errorf("the edit's answer carries the oldString text: %s", served)
	}
}

func TestAnInterruptedRunCarriesNoExitCode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	line := `{"type":"user","uuid":"i1","timestamp":"2026-10-26T10:00:01.000Z","toolUseResult":{"stdout":"partial words","stderr":"","interrupted":true,"isImage":false,"noOutputExpected":false},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"Request interrupted by user"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "i1:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", IsError: true,
			Text: "Request interrupted by user", Result: &ToolFacts{Interrupted: true}},
	})
}

func TestASuccessNeverCarriesAnExitCode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	// a succeeded command whose own output prints "Exit code 3": neither the answer's text nor the
	// stdout names an exit code for a success, and the stdout itself is never served
	line := `{"type":"user","uuid":"c1","timestamp":"2026-10-26T10:00:01.000Z","toolUseResult":{"stdout":"Exit code 3\n","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Exit code 3\ndone"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	page, err := Read(ctx, path, 0)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	checkItems(t, page.Items, []Item{
		{ID: "c1:0", Kind: KindToolResult, At: "2026-10-26T10:00:01.000Z", ToolUseID: "toolu_1", Text: "Exit code 3\ndone"},
	})
}
