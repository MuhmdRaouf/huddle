// Package convo turns a Claude Code transcript into conversation items. It reads the one path
// it is given and nothing else, from a byte offset a caller keeps between reads, so a view can
// page through a session and pick up where it stopped.
package convo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The item kinds a Claude transcript yields.
const (
	KindUser       = "user"        // a prompt the owner typed
	KindAssistant  = "assistant"   // the assistant's text
	KindThinking   = "thinking"    // the assistant's thinking, shown between its replies
	KindToolUse    = "tool_use"    // a tool call: its name and input
	KindToolResult = "tool_result" // what a tool call answered
	KindFile       = "file"        // a file attached to a prompt
	KindImage      = "image"       // an image in a prompt

	KindNotification = "notification"  // a background task's end, reported by Claude Code
	KindMonitorEvent = "monitor_event" // one event a Monitor streamed
)

// MaxText is the most bytes of one item's text or input kept; a longer one is cut on a rune
// boundary and marked Cut.
const MaxText = 4096

// MaxRead is how many bytes one Read consumes at most: it stops after the first complete line
// that takes it past this, so one huge line is still read whole.
const MaxRead = 1 << 20

// MaxHunkLines is the most hunk lines one answer's facts keep in all; past it the remaining
// lines and hunks go and the facts are marked Cut.
const MaxHunkLines = 400

// Item is one conversation item.
type Item struct {
	ID         string     `json:"id"` // "<line uuid>:<block index>", stable across reads
	Kind       string     `json:"kind"`
	At         string     `json:"at"` // the line's timestamp as Claude Code wrote it
	Text       string     `json:"text,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"` // the line's thinkingDurationMs, thinking items only
	Tool       string     `json:"tool,omitempty"`
	ToolUseID  string     `json:"tool_use_id,omitempty"`
	Input      string     `json:"input,omitempty"` // a tool call's input as compact JSON
	IsError    bool       `json:"is_error,omitempty"`
	Result     *ToolFacts `json:"result,omitempty"` // what the tool did, on tool_result items
	Cut        bool       `json:"cut,omitempty"`
	Parent     string     `json:"parent,omitempty"`     // the line's parentUuid, on file items, so a view joins a file to its prompt
	Path       string     `json:"path,omitempty"`       // a file item's displayPath
	Lines      int        `json:"lines,omitempty"`      // a file item's totalLines, else numLines
	MediaType  string     `json:"media_type,omitempty"` // an image item's source media type
	Bytes      int        `json:"bytes,omitempty"`      // an image item's decoded size, from the base64 length alone
	Origin     string     `json:"origin,omitempty"`     // the line's origin.kind, on prompts and notifications
	Status     string     `json:"status,omitempty"`     // a notification's <status>
}

// ToolFacts is what a tool call did, from the transcript's toolUseResult.
type ToolFacts struct {
	ExitCode     *int     `json:"exit_code,omitempty"` // from an error result's "Exit code N"
	Stderr       string   `json:"stderr,omitempty"`    // cut to MaxText
	Interrupted  bool     `json:"interrupted,omitempty"`
	BackgroundID string   `json:"background_id,omitempty"`
	FilePath     string   `json:"file_path,omitempty"` // Edit and Write
	Hunks        []Hunk   `json:"hunks,omitempty"`
	Answers      []Answer `json:"answers,omitempty"` // AskUserQuestion, in question order
	Cut          bool     `json:"cut,omitempty"`     // stderr or the hunks were cut
}

// Hunk is one changed region of a file.
type Hunk struct {
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Lines    []string `json:"lines"` // each line keeps its " ", "-" or "+" prefix
}

// Answer is one question the owner answered and the answer given.
type Answer struct {
	Header   string `json:"header,omitempty"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Page is one read: the items of the complete lines read, and the offset the next read starts at.
type Page struct {
	Items []Item `json:"items"`
	Next  int64  `json:"next"`
}

// transcriptLine is one transcript line's shape: the kind of turn, its identity, and its message
// content, which is a plain string for a typed prompt and a block array otherwise. IsSidechain
// marks a subagent's lines and IsMeta a line the harness injected; neither is this conversation.
// An attachment line carries its payload outside the message, and parentUuid names the line it
// hangs from.
type transcriptLine struct {
	Type               string          `json:"type"`
	UUID               string          `json:"uuid"`
	ParentUUID         string          `json:"parentUuid"`
	Timestamp          string          `json:"timestamp"`
	ThinkingDurationMS int64           `json:"thinkingDurationMs"` // the line's thinking time, in ms
	IsSidechain        bool            `json:"isSidechain"`
	IsMeta             bool            `json:"isMeta"`
	ToolUseResult      json.RawMessage `json:"toolUseResult"` // what a tool did, beside its tool_result block
	Attachment         json.RawMessage `json:"attachment"`

	// A compaction summary carries one of the two flags; Origin names who wrote a user line.
	IsCompactSummary          bool `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool `json:"isVisibleInTranscriptOnly"`
	Origin                    struct {
		Kind     string `json:"kind"`
		Producer string `json:"producer"`
	} `json:"origin"`

	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// toolUseResult is a user line's toolUseResult decoded: what the tool did. The fields the reader
// never serves — a command's stdout, an edit's old and new text and the file it replaced, a
// question's options — are left out of the decode, so they cannot reach an item.
type toolUseResult struct {
	Stderr          string            `json:"stderr"`
	Interrupted     bool              `json:"interrupted"`
	BackgroundID    string            `json:"backgroundTaskId"`
	FilePath        string            `json:"filePath"`
	StructuredPatch []structuredHunk  `json:"structuredPatch"`
	Questions       []askedQuestion   `json:"questions"`
	Answers         map[string]string `json:"answers"` // each answer keyed by its question's text
}

// structuredHunk is one region an edit changed, its lines each keeping their " ", "-" or "+"
// prefix.
type structuredHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// askedQuestion is one question an AskUserQuestion call asked.
type askedQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
}

// contentBlock is one content block: text carries words, thinking carries the assistant's
// thinking, tool_use names a call and its input, tool_result answers a call, and image names a
// picture in a prompt. A thinking block's signature is an opaque token the reader never reads
// or serves.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Source    imageSource     `json:"source"`
}

// imageSource is an image block's source: base64 data the reader sizes from its length but
// never decodes, keeps or serves.
type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// attachment is an attachment line's payload: a file attachment names the file it put in the
// prompt and may carry the content that was read of it, and a queued_command attachment carries
// the text Claude Code queued for the turn; other subtypes are left out.
type attachment struct {
	Type        string          `json:"type"`
	FileName    string          `json:"filename"`
	DisplayPath string          `json:"displayPath"`
	Content     json.RawMessage `json:"content"`
	Prompt      string          `json:"prompt"` // a queued_command attachment's text
}

// attachedContent is a file attachment's content block: the read of one file.
type attachedContent struct {
	Type string       `json:"type"`
	File attachedFile `json:"file"`
}

// attachedFile is one file read into a prompt: its path, its content and its line counts.
type attachedFile struct {
	FilePath   string `json:"filePath"`
	Content    string `json:"content"`
	NumLines   int    `json:"numLines"`
	StartLine  int    `json:"startLine"`
	TotalLines int    `json:"totalLines"`
}

// Read answers the conversation items of the transcript at path from byte offset after. Only
// complete lines count: a torn last line is left unconsumed, so Next stops at its first byte and
// the next read takes it whole. A missing file answers the os error; an empty read answers [].
func Read(ctx context.Context, path string, after int64) (Page, error) {
	return read(ctx, path, after, false)
}

// ReadAgent answers the conversation items of one agent's transcript at path, the same rules as
// Read but keeping the sidechain lines a subagent's transcript is made of.
func ReadAgent(ctx context.Context, path string, after int64) (Page, error) {
	return read(ctx, path, after, true)
}

// read walks the transcript's complete lines past after, decoding each into items; meta lines are
// harness-injected either way, and a subagent's sidechain lines count only when keepSidechain.
func read(ctx context.Context, path string, after int64, keepSidechain bool) (Page, error) {
	page := Page{Items: []Item{}}
	file, err := os.Open(path)
	if err != nil {
		return page, err
	}
	defer file.Close()
	if _, err := file.Seek(after, io.SeekStart); err != nil {
		return page, err
	}

	reader := bufio.NewReader(file)
	var consumed int64
	var queued *queuedNote // the previous line, when it was a queued notification
	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			return Page{Items: []Item{}}, err
		}
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			consumed += int64(len(raw))
			if first && after > 0 && bytes.Contains(raw, []byte(shellMarkDone)) {
				queued = queuedBefore(file, after, keepSidechain)
			}
			var items []Item
			items, queued = itemsOf(raw, keepSidechain, queued)
			page.Items = append(page.Items, items...)
			if consumed > MaxRead {
				break
			}
			continue
		}
		// a short read without its newline is a torn last line: the next read takes it whole
		if err != nil && !errors.Is(err, io.EOF) {
			return Page{Items: []Item{}}, err
		}
		break
	}
	page.Next = after + consumed
	return page, nil
}

// queuedNote is a queued_command attachment's notification, kept for the line that follows it:
// that line may be the user line repeating it.
type queuedNote struct {
	uuid string
	note notification
}

// itemsOf decodes one complete transcript line into its items, in content order, and answers the
// queued notification the line is, when it is one. A line that is not JSON, not a user,
// assistant or attachment turn, or harness-injected yields nothing; a subagent's sidechain line
// yields nothing unless keepSidechain. A user line that repeats the queued notification just
// before it (prev) yields nothing, so a notification shows one row whichever carrier came first.
func itemsOf(raw []byte, keepSidechain bool, prev *queuedNote) ([]Item, *queuedNote) {
	var line transcriptLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return nil, nil
	}
	if line.IsMeta || (line.IsSidechain && !keepSidechain) {
		return nil, nil
	}
	switch line.Type {
	case KindUser:
		if repeatsQueued(line, prev) {
			return nil, nil
		}
		return userItems(line), nil
	case KindAssistant:
		return assistantItems(line), nil
	case "attachment":
		return attachmentItems(line)
	default:
		return nil, nil
	}
}

// repeatsQueued answers whether line is the user line that repeats the queued notification
// before it: it hangs from that attachment and carries the same notification. Only the line
// right after the attachment counts, so a task that reports the same ids again later, as a
// resumed agent does, is a row of its own.
func repeatsQueued(line transcriptLine, prev *queuedNote) bool {
	if prev == nil || prev.uuid == "" || line.ParentUUID != prev.uuid {
		return false
	}
	var text string
	if json.Unmarshal(line.Message.Content, &text) != nil || !isNotificationLine(line, text) {
		return false
	}
	note, ok := parseNotification(text)
	return ok && note.same(prev.note)
}

// queuedBefore answers the queued notification that the line ending at byte offset end is. A
// read that starts mid-transcript calls it for its first line alone, and only when that line
// carries a notification's marker, so the reads that start elsewhere pay nothing. It looks back
// over at most MaxRead bytes: a previous line longer than that is not checked and its repeat
// shows as a row.
func queuedBefore(file *os.File, end int64, keepSidechain bool) *queuedNote {
	var last [1]byte
	if _, err := file.ReadAt(last[:], end-1); err != nil || last[0] != '\n' {
		return nil
	}
	lineEnd, lineStart := end-1, int64(0)
	chunk := make([]byte, 4096)
	for pos := lineEnd; pos > 0; {
		if lineEnd-pos >= MaxRead {
			return nil
		}
		size := min(int64(len(chunk)), pos)
		if _, err := file.ReadAt(chunk[:size], pos-size); err != nil {
			return nil
		}
		if i := bytes.LastIndexByte(chunk[:size], '\n'); i >= 0 {
			lineStart = pos - size + int64(i) + 1
			break
		}
		pos -= size
	}
	raw := make([]byte, lineEnd-lineStart)
	if _, err := file.ReadAt(raw, lineStart); err != nil {
		return nil
	}
	_, queued := itemsOf(raw, keepSidechain, nil)
	return queued
}

// userItems answers a user line's items: a string content is the prompt itself, a block array
// yields its text blocks as prompts, its image blocks as the pictures they carry and its
// tool_result blocks as the answers they carry. A prompt that is empty but for spacing is no
// words the person typed, and a compaction summary is no prompt at all. Text a task notification
// wrote is a notification item, never a prompt (see promptItem).
func userItems(line transcriptLine) []Item {
	if line.IsCompactSummary || line.IsVisibleInTranscriptOnly {
		return nil
	}
	var prompt string
	if err := json.Unmarshal(line.Message.Content, &prompt); err == nil {
		if strings.TrimSpace(prompt) == "" {
			return nil
		}
		return []Item{promptItem(line, 0, prompt)}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(line.Message.Content, &blocks); err != nil {
		return nil
	}
	var items []Item
	factsServed := false // the line's toolUseResult belongs to its first tool_result block alone
	for i, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			items = append(items, promptItem(line, i, block.Text))
		case "image":
			items = append(items, imageItem(line, i, block))
		case "tool_result":
			item := resultItem(line, i, block)
			if !factsServed {
				factsServed = true
				item.Result = resultFacts(line.ToolUseResult, block)
			}
			items = append(items, item)
		}
	}
	return items
}

// assistantItems answers an assistant line's items: each thinking block the assistant's thinking
// with the line's thinking time, each text block the assistant's words, each tool_use block the
// call it made; every other block is left out.
func assistantItems(line transcriptLine) []Item {
	var blocks []contentBlock
	if err := json.Unmarshal(line.Message.Content, &blocks); err != nil {
		return nil
	}
	var items []Item
	for i, block := range blocks {
		switch block.Type {
		case "thinking":
			if block.Thinking == "" {
				continue
			}
			item := textItem(line, i, KindThinking, block.Thinking)
			item.DurationMS = line.ThinkingDurationMS
			items = append(items, item)
		case "text":
			if block.Text == "" {
				continue
			}
			items = append(items, textItem(line, i, KindAssistant, block.Text))
		case "tool_use":
			items = append(items, useItem(line, i, block))
		}
	}
	return items
}

// attachmentItems answers an attachment line's items: a file attachment yields one file item
// naming the file, its line count and the content read into the prompt, cut to MaxText; a file
// with no content still yields its item. A queued_command attachment whose prompt is a task
// notification yields the item the idle carrier of that notification yields, and is answered as
// the queued notification too; any other attachment yields nothing.
func attachmentItems(line transcriptLine) ([]Item, *queuedNote) {
	var att attachment
	if err := json.Unmarshal(line.Attachment, &att); err != nil {
		return nil, nil
	}
	if att.Type == "queued_command" && isNotificationText(att.Prompt) {
		note, _ := parseNotification(att.Prompt)
		return []Item{notificationItem(line, 0, att.Prompt, note)}, &queuedNote{uuid: line.UUID, note: note}
	}
	if att.Type != "file" {
		return nil, nil
	}
	var content attachedContent
	if len(att.Content) > 0 {
		if err := json.Unmarshal(att.Content, &content); err != nil {
			return nil, nil
		}
	}
	item := Item{
		ID:     line.UUID + ":0",
		Kind:   KindFile,
		At:     line.Timestamp,
		Parent: line.ParentUUID,
		Path:   att.DisplayPath,
		Lines:  fileLines(content.File),
	}
	item.Text, item.Cut = cut(content.File.Content)
	return []Item{item}, nil
}

// originTaskNotification is the origin.kind of a line Claude Code wrote to report a background
// task.
const originTaskNotification = "task-notification"

// isNotificationText answers whether text is a task notification's wrapper.
func isNotificationText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<"+shellMarkDone+">")
}

// isNotificationLine answers whether a user line's text is a task notification: its origin says
// so, or it names no origin and the text is a notification's wrapper. A line under any other
// origin is not, so no possible prompt of the owner is hidden.
func isNotificationLine(line transcriptLine, text string) bool {
	kind := line.Origin.Kind
	return kind == originTaskNotification || (kind == "" && isNotificationText(text))
}

// promptItem answers the item of a user line's text: a notification item when the line is a
// task notification, else a prompt that carries its origin as written.
func promptItem(line transcriptLine, index int, text string) Item {
	if isNotificationLine(line, text) {
		note, _ := parseNotification(text)
		return notificationItem(line, index, text, note)
	}
	item := textItem(line, index, KindUser, text)
	item.Origin = line.Origin.Kind
	return item
}

// notificationItem answers the item of a task notification's wrapper, whichever carrier brought
// it. A Monitor's event is a monitor_event item holding the event's text. Any other notification
// is a notification item holding its summary, with the call it ends, its status and its exit
// code. The wrapper's other tags, the output file's path among them, are never served.
func notificationItem(line transcriptLine, index int, text string, note notification) Item {
	if note.Event != "" {
		item := textItem(line, index, KindMonitorEvent, note.Event)
		item.Origin = originTaskNotification
		return item
	}
	item := textItem(line, index, KindNotification, notificationSummaryOf(text))
	item.Origin = originTaskNotification
	item.ToolUseID = note.ToolUseID
	item.Status = note.Status
	if note.ExitCode != nil {
		item.Result = &ToolFacts{ExitCode: note.ExitCode}
	}
	return item
}

// imageItem answers an image block's item: its media type and decoded size. The size comes from
// the base64 length alone; the data itself is never decoded, kept or served.
func imageItem(line transcriptLine, index int, block contentBlock) Item {
	return Item{
		ID:        line.UUID + ":" + strconv.Itoa(index),
		Kind:      KindImage,
		At:        line.Timestamp,
		MediaType: block.Source.MediaType,
		Bytes:     decodedSize(block.Source.Data),
	}
}

// fileLines answers a file's line count: totalLines when the read names it, else numLines.
func fileLines(file attachedFile) int {
	if file.TotalLines > 0 {
		return file.TotalLines
	}
	return file.NumLines
}

// decodedSize answers the byte size base64 data decodes to, from the length alone: DecodedLen
// sizes the length and the trailing padding is taken back off.
func decodedSize(data string) int {
	padding := 0
	for padding < 2 && padding < len(data) && data[len(data)-1-padding] == '=' {
		padding++
	}
	return base64.StdEncoding.DecodedLen(len(data)) - padding
}

// textItem answers a text-bearing item of the given kind, its text cut to MaxText.
func textItem(line transcriptLine, index int, kind, text string) Item {
	item := Item{
		ID:   line.UUID + ":" + strconv.Itoa(index),
		Kind: kind,
		At:   line.Timestamp,
	}
	item.Text, item.Cut = cut(text)
	return item
}

// useItem answers a tool call's item, its input kept as the compact JSON the caller sent.
func useItem(line transcriptLine, index int, block contentBlock) Item {
	input := ""
	if len(block.Input) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, block.Input); err == nil {
			input = buf.String()
		}
	}
	item := Item{
		ID:        line.UUID + ":" + strconv.Itoa(index),
		Kind:      KindToolUse,
		At:        line.Timestamp,
		Tool:      block.Name,
		ToolUseID: block.ID,
	}
	item.Input, item.Cut = cut(input)
	return item
}

// resultItem answers a tool call's answer: its id, whether the call failed, and its text.
func resultItem(line transcriptLine, index int, block contentBlock) Item {
	item := Item{
		ID:        line.UUID + ":" + strconv.Itoa(index),
		Kind:      KindToolResult,
		At:        line.Timestamp,
		ToolUseID: block.ToolUseID,
		IsError:   block.IsError,
	}
	item.Text, item.Cut = cut(resultText(block.Content))
	return item
}

// resultText flattens a tool_result block's content: a string stands as it is, an array gives
// the text of each text block joined by a newline, and image and other blocks are left out.
func resultText(raw json.RawMessage) string {
	var content string
	if err := json.Unmarshal(raw, &content); err == nil {
		return content
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// resultFacts answers what a tool call did, from the toolUseResult beside its tool_result block:
// a failed command's exit code and stderr, whether it was interrupted, a background command's id,
// an edit's file and hunks, the answers an AskUserQuestion call was given. A toolUseResult that
// is absent, not an object, or of a shape naming no fact answers nil.
func resultFacts(raw json.RawMessage, block contentBlock) *ToolFacts {
	var decoded toolUseResult
	if len(raw) == 0 || json.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	facts := &ToolFacts{}
	if block.IsError {
		if code, ok := exitCodeOf(resultText(block.Content)); ok {
			facts.ExitCode = &code
		}
	}
	hunks, hunksCut := hunksOf(decoded.StructuredPatch)
	facts.Hunks = hunks
	facts.Stderr, facts.Cut = cut(decoded.Stderr)
	facts.Cut = facts.Cut || hunksCut
	facts.Interrupted = decoded.Interrupted
	facts.BackgroundID = decoded.BackgroundID
	facts.FilePath = decoded.FilePath
	for _, asked := range decoded.Questions {
		answer, ok := decoded.Answers[asked.Question]
		if !ok {
			continue
		}
		facts.Answers = append(facts.Answers, Answer{Header: asked.Header, Question: asked.Question, Answer: answer})
	}
	if facts.ExitCode == nil && facts.Stderr == "" && !facts.Interrupted && facts.BackgroundID == "" &&
		facts.FilePath == "" && len(facts.Hunks) == 0 && len(facts.Answers) == 0 {
		return nil
	}
	return facts
}

// hunksOf answers an edit's structured patch as hunks, keeping hunk lines in order while they in
// all stay within MaxHunkLines: the hunk that crosses the limit keeps only its first lines up to
// it, every hunk after it goes, and the answer is marked cut.
func hunksOf(patch []structuredHunk) ([]Hunk, bool) {
	hunks := make([]Hunk, 0, len(patch))
	lines := 0
	cut := false
	for _, hunk := range patch {
		room := MaxHunkLines - lines
		if room <= 0 {
			cut = true
			break
		}
		if len(hunk.Lines) > room {
			hunk.Lines = hunk.Lines[:room]
			cut = true
		}
		lines += len(hunk.Lines)
		hunks = append(hunks, Hunk(hunk))
	}
	if len(hunks) == 0 {
		return nil, cut
	}
	return hunks, cut
}

// exitCodeRe matches a failed answer's "Exit code N", anchoring at the text's start, so only the
// first line can match.
var exitCodeRe = regexp.MustCompile(`^Exit code (\d+)`)

// exitCodeOf answers the exit code a failed answer's text names, from the text's first line.
func exitCodeOf(text string) (int, bool) {
	match := exitCodeRe.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// cut keeps at most MaxText bytes of s, never splitting a rune, and says whether it did.
func cut(s string) (string, bool) {
	if len(s) <= MaxText {
		return s, false
	}
	kept := s[:MaxText]
	// back off the bytes of a rune the cut split
	for len(kept) > 0 {
		if r, size := utf8.DecodeLastRuneInString(kept); r != utf8.RuneError || size != 1 {
			break
		}
		kept = kept[:len(kept)-1]
	}
	return kept, true
}
