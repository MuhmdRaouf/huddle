// Package ingest reads what Claude Code and the provider plugins write: transcript lines,
// spool lines and job files, into the records the store keeps.
package ingest

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// Transcript parsing: Claude Code writes one json line per message into ~/.claude/projects/**.
// Each assistant entry is one model request (streaming may write it several times — merged by
// requestId); tool_use blocks pair with later tool_result blocks to give durations; sidechain
// entries and subagent files attribute work to subagents.

// SyntheticModel is the model name Claude Code writes on assistant messages it makes itself,
// never on a model's answer.
const SyntheticModel = "<synthetic>"

// noticeWhat maps the error kind Claude Code names on a notice line to what people read.
var noticeWhat = map[string]string{
	"rate_limit":            "usage limit",
	"server_error":          "connection error",
	"authentication_failed": "not logged in",
	"invalid_request":       "request refused",
}

// FileIdentity is which session and agent a transcript file belongs to, from its path alone;
// line data refines it.
type FileIdentity struct {
	SessionID string
	AgentID   *string
	Kind      shared.AgentKind
	// Upstream is where every request in the file went, when the file's source knows (a zai
	// job calls Z.ai directly).
	Upstream *string
	// Remap lands every record the file yields on this session and agent, whatever session
	// and agent its lines name: a job attempt's real transcript belongs to the job's pseudo
	// session, not to the session id its lines carry.
	Remap *Remap
	// AgentMeta is what the agent is, from subagents/agent-<id>.meta.json beside a subagent
	// transcript, read once.
	AgentMeta *AgentMeta
}

// Remap pins the session and agent a file's records land on.
type Remap struct {
	SessionID string
	AgentID   string
	ParentID  *string
}

// AgentMeta names what kind of agent ran and its task.
type AgentMeta struct {
	AgentType   string
	Description string
}

// AgentDescriptor is one agent sighted in a transcript, emitted once. Name is nil when no
// meta file named the agent: the store falls back to the agent's first prompt, never the raw
// id.
type AgentDescriptor struct {
	SessionID   string           `json:"sessionId"`
	ID          string           `json:"id"`
	ParentID    *string          `json:"parentId"`
	Kind        shared.AgentKind `json:"kind"`
	Name        *string          `json:"name"`
	AgentType   *string          `json:"agentType"`
	Description *string          `json:"description"`
	// Title is the head of the agent's first prompt, emitted once it is seen; names a
	// subagent with no meta file.
	Title *string `json:"title,omitempty"`
}

// SessionMeta is the naming data the transcript carries; the store keeps the latest of each
// per session.
type SessionMeta struct {
	ID          string  `json:"id"`
	Cwd         *string `json:"cwd,omitempty"`
	CcVersion   *string `json:"ccVersion,omitempty"`
	CustomTitle *string `json:"customTitle,omitempty"`
	AgentName   *string `json:"agentName,omitempty"`
	AITitle     *string `json:"aiTitle,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Branch      *string `json:"branch,omitempty"`
}

// ContentBlock is one captured content block, as the history stores it: a prompt text, a tool
// result or a piece of the assistant's answer. A block's text is capped (see capText), the
// rest is kept whole.
type ContentBlock struct {
	Type string `json:"type"`
	// ToolUseID and IsError ride ahead of the text so a tool result serialises its keys in
	// the order the blocks are built: type, tool_use_id, is_error, text.
	ToolUseID *string `json:"tool_use_id,omitempty"`
	IsError   *bool   `json:"is_error,omitempty"`
	Text      *string `json:"text,omitempty"`
	// Thinking is a thinking block's words, which the wire carries under `thinking` (with its
	// signature, dropped).
	Thinking *string `json:"thinking,omitempty"`
	// MediaType is a media block's kind ("image/png"); the data itself is never stored.
	MediaType *string `json:"media_type,omitempty"`
	// Bytes is how many bytes the media block's data decodes to.
	Bytes *int64 `json:"bytes,omitempty"`
	// Truncated is how many bytes of a capped text did not fit; the UI says the original
	// size with it.
	Truncated *int64          `json:"truncated,omitempty"`
	Name      *string         `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// ContentCapture is one request's INPUT and OUTPUT, serialised elsewhere; the writer puts it
// beside the request.
type ContentCapture struct {
	RequestID string  `json:"requestId"`
	Input     *string `json:"input"`
	Output    *string `json:"output"`
}

// Interrupt is one turn the user cut off (Esc): Claude Code sends no Stop hook for these, the
// transcript says so.
type Interrupt struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Ts        int64  `json:"ts"`
}

// Notice is an error Claude Code wrote itself (usage limit, lost connection): an error of the
// agent, not a model call.
type Notice struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Ts        int64  `json:"ts"`
	What      string `json:"what"`
}

// TranscriptEmit is what one transcript line produced.
type TranscriptEmit struct {
	Requests   []shared.RequestRecord  `json:"requests"`
	ToolCalls  []shared.ToolCallRecord `json:"toolCalls"`
	Agents     []AgentDescriptor       `json:"agents"`
	Session    *SessionMeta            `json:"session"`
	Content    []ContentCapture        `json:"content"`
	APIErrors  []shared.APIErrorRecord `json:"apiErrors"`
	Interrupts []Interrupt             `json:"interrupts"`
	Notices    []Notice                `json:"notices"`
}

// pendingTool is a tool_use waiting for its tool_result, so the pair yields a duration.
type pendingTool struct {
	name      string
	startedAt int64
	sessionID string
	agentID   string
	inputKey  string
}

// lastRequestEntry is the last request an agent chain made and whether any request so far
// carried tokens: a `claude -p` stream on a provider that reports usage only in its final
// result event gets that total on its last request.
type lastRequestEntry struct {
	request   shared.RequestRecord
	anyTokens bool
}

// The per-file windows: a streamed message's entries land within seconds, one after another,
// so thousands of quiet files must not hold a wide window each for the watcher's whole life.
const (
	// streamOutputCap is the in-flight streams whose output side stays open per file.
	streamOutputCap = 16
	// whatInputCap is the remembered `what` inputs, one per stream that can still grow.
	whatInputCap = 64
	// pendingInputCap is the logged inputs, one per agent chain between its requests.
	pendingInputCap = 32
)

// boundedMap is a string-keyed map in insertion order: re-setting a key makes it the newest,
// and the oldest entry drops once the map grows past its cap, keeping a long transcript's
// parser flat. A streamed rewrite lands within seconds, so a window of request ids is plenty.
type boundedMap[V any] struct {
	entries map[string]V
	order   []string
}

func (m *boundedMap[V]) has(key string) bool {
	_, ok := m.entries[key]
	return ok
}

func (m *boundedMap[V]) get(key string) (V, bool) {
	value, ok := m.entries[key]
	return value, ok
}

func (m *boundedMap[V]) delete(key string) {
	delete(m.entries, key)
	for i, k := range m.order {
		if k == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

// set remembers key as the newest entry, dropping the oldest once the map grows past cap.
func (m *boundedMap[V]) set(key string, value V, cap int) {
	if m.entries == nil {
		m.entries = map[string]V{}
	}
	m.delete(key)
	m.entries[key] = value
	m.order = append(m.order, key)
	if len(m.order) <= cap {
		return
	}
	oldest := m.order[0]
	m.order = m.order[1:]
	delete(m.entries, oldest)
}

func (m *boundedMap[V]) len() int { return len(m.entries) }

// TranscriptState is the cross-line state one transcript file's parsing keeps. Pure and
// stateful-by-argument: the watcher owns the state, the parser only interprets lines.
type TranscriptState struct {
	pendingTools boundedMap[pendingTool]
	lastUserTs   map[string]int64
	seenAgents   map[string]struct{}
	// prompts holds the agent chains whose first prompt already became their title: emitted
	// once, never again.
	prompts map[string]struct{}
	// lastRequest is per agent: the last request and whether any request so far carried
	// tokens.
	lastRequest map[string]lastRequestEntry
	// pendingInput is per agent chain: the user and tool_result entries since that agent's
	// previous assistant request.
	pendingInput boundedMap[[]ContentBlock]
	// inputTaken is per agent chain and request id: that the request's first entry already
	// consumed the pending input. A subagent writes one entry per block and its tool results
	// land between them, so the later block entries of a message must leave the logged input
	// to the next request.
	inputTaken boundedMap[bool]
	// streamOutput is per request id: the assistant blocks seen so far, a streamed message
	// being written several times.
	streamOutput boundedMap[[]ContentBlock]
	// whatInput is per request id: the input side already computed for its `what`, so a
	// streamed message's later entries keep naming it.
	whatInput boundedMap[string]
}

// NewTranscriptState is an empty state.
func NewTranscriptState() *TranscriptState {
	return &TranscriptState{
		lastUserTs:  map[string]int64{},
		seenAgents:  map[string]struct{}{},
		prompts:     map[string]struct{}{},
		lastRequest: map[string]lastRequestEntry{},
	}
}

// Compact empties the per-request windows a quiet file keeps — the streamed outputs, the
// pending inputs and the remembered `what` inputs. Everything the file's next line still
// needs (pending tools, titles, the last request) stays.
func (s *TranscriptState) Compact() {
	s.streamOutput = boundedMap[[]ContentBlock]{}
	s.pendingInput = boundedMap[[]ContentBlock]{}
	s.whatInput = boundedMap[string]{}
}

// rawObject is a transcript object read loosely: every field's raw JSON, because the lines
// carry unknown fields and shapes this parser must skip, not reject.
type rawObject = map[string]json.RawMessage

// isNull reports whether raw is the JSON null.
func isNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// asObject decodes raw as an object; a non-object (null included) is no object.
func asObject(raw json.RawMessage) (rawObject, bool) {
	if len(raw) == 0 || isNull(raw) {
		return nil, false
	}
	var obj rawObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// stringField reads key as a string the way typeof does: null and non-strings are absent,
// an empty string is present.
func stringField(obj rawObject, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// boolField reads key as a boolean, absent for anything else.
func boolField(obj rawObject, key string) (bool, bool) {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

// numberField reads key as a finite number: token counts are whole numbers, so it lands as
// an int64; strings and non-finite values are absent.
func numberField(obj rawObject, key string) (int64, bool) {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return 0, false
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(num.String(), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, false
	}
	return int64(value), true
}

// objField reads key as an object, absent for anything else.
func objField(obj rawObject, key string) (rawObject, bool) {
	raw, ok := obj[key]
	if !ok {
		return nil, false
	}
	return asObject(raw)
}

// compactOf prints raw the way JSON.stringify re-prints a parsed value: same key order, no
// insignificant whitespace; invalid JSON (never seen on a parsed line) prints as nothing.
func compactOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	return buf.String()
}

// stringifyBlocks prints blocks the way JSON.stringify does: compact, keys in their declared
// order, a tool_use's input byte for byte as it lay on the wire. Nil when it cannot.
func stringifyBlocks(blocks []ContentBlock) *string {
	printed, err := domain.MarshalCompactJSON(blocks)
	if err != nil {
		return nil
	}
	text := string(printed)
	return &text
}

// ShortHash folds text through FNV-1a over its UTF-16 code units and prints it as 8 hex
// digits: enough to tell "the same call again" from a different one.
func ShortHash(text string) string {
	hash := uint32(0x811c9dc5)
	for _, r := range text {
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			hash = fnvStep(fnvStep(hash, uint16(hi)), uint16(lo))
			continue
		}
		hash = fnvStep(hash, uint16(r))
	}
	return padStart8(strconv.FormatUint(uint64(hash), 16))
}

// padStart8 left-pads a hex digest to 8 digits, the way toString(16).padStart(8, "0") does.
func padStart8(hex string) string {
	if len(hex) >= 8 {
		return hex
	}
	return strings.Repeat("0", 8-len(hex)) + hex
}

func fnvStep(hash uint32, unit uint16) uint32 {
	hash ^= uint32(unit)
	return hash * 0x01000193
}

// ToolInputKey is the identity of a tool call for loop detection: its name plus its whole
// input, hashed. Never stored as text.
func ToolInputKey(name string, input json.RawMessage) string {
	return ShortHash(name + "\x00" + compactOf(input))
}

// retryable says whether Claude Code would retry a status (429 and the 5xx).
func retryable(status int64) bool {
	return status == 429 || (status >= 500 && status <= 599)
}

// apiErrorPattern allows JavaScript's whitespace, Unicode spaces included, which RE2's \s
// does not match.
var apiErrorPattern = regexp.MustCompile(`API Error:` + jsscalar.SpaceClass + `*(\d{3})`)

// APIErrorStatus reads "API Error: 529 {…}" as 529; anything else, or a status nobody
// retries, is no status.
func APIErrorStatus(text string) (int64, bool) {
	match := apiErrorPattern.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	status, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || !retryable(status) {
		return 0, false
	}
	return status, true
}

// stripExt drops a .jsonl suffix.
func stripExt(name string) string {
	return strings.TrimSuffix(name, ".jsonl")
}

// IdentifyFile reads which session and agent a file belongs to, from its path alone.
func IdentifyFile(path string) FileIdentity {
	parts := strings.Split(path, "/")
	subagentsAt := lastIndex(parts, "subagents")
	if subagentsAt > 0 {
		agentPart := stripExt(parts[len(parts)-1])
		sessionID := agentPart
		if subagentsAt >= 1 {
			sessionID = parts[subagentsAt-1]
		}
		return FileIdentity{SessionID: sessionID, AgentID: &agentPart, Kind: shared.KindSubagent}
	}
	return FileIdentity{SessionID: stripExt(parts[len(parts)-1]), Kind: shared.KindMain}
}

// lastIndex finds needle's highest index in haystack, -1 when absent.
func lastIndex(haystack []string, needle string) int {
	for i := len(haystack) - 1; i >= 0; i-- {
		if haystack[i] == needle {
			return i
		}
	}
	return -1
}

// parseEntry decodes one transcript line; nil when it is not a JSON object.
func parseEntry(raw string) rawObject {
	var entry rawObject
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil
	}
	return entry
}

// contentBlocks lists a message's content blocks: a string content is none, and anything that
// is not an object rides out.
func contentBlocks(message rawObject) []rawObject {
	raw, ok := message["content"]
	if !ok {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var blocks []rawObject
	for _, item := range items {
		if block, ok := asObject(item); ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// textCap is how many bytes of one block's text are kept: an ordinary large tool result
// stays whole, and past it the marker names how many bytes did not fit.
const textCap = 1024 * 1024

// capText caps text at textCap bytes, cut on UTF-16 boundaries the way the dashboard measures
// it: the kept prefix, a marker naming the lost bytes, and the cut for the UI.
func capText(text string) (string, *int64) {
	if len(text) <= textCap {
		return text, nil
	}
	kept := jsscalar.UTF16Slice(text, 0, textCap)
	for len(kept) > textCap {
		kept = jsscalar.UTF16Slice(kept, 0, -1)
	}
	cut := int64(len(text) - len(kept))
	return kept + "…[truncated " + strconv.FormatInt(cut, 10) + " bytes]", &cut
}

// decodedBytesOf is the bytes a base64 payload decodes to; false when it is not a string.
func decodedBytesOf(data json.RawMessage) (int64, bool) {
	var text string
	if len(data) == 0 || isNull(data) {
		return 0, false
	}
	if err := json.Unmarshal(data, &text); err != nil {
		return 0, false
	}
	padding := 0
	switch {
	case strings.HasSuffix(text, "=="):
		padding = 2
	case strings.HasSuffix(text, "="):
		padding = 1
	}
	decoded := int64(jsscalar.UTF16Len(text)*3/4) - int64(padding)
	if decoded < 0 {
		return 0, true
	}
	return decoded, true
}

// mediaBlockOf writes a block with no words of its own (an image, a document): its kind,
// media type and the bytes its data decodes to — the data itself never stored. The marker
// keeps it readable anywhere a block's text is shown, so "the model saw an image" never reads
// as "the block was empty".
func mediaBlockOf(blockType string, block rawObject) ContentBlock {
	out := ContentBlock{Type: blockType}
	source, _ := objField(block, "source")
	if mediaType, ok := stringField(source, "media_type"); ok && mediaType != "" {
		out.MediaType = &mediaType
	}
	var decoded int64
	if bytes, ok := decodedBytesOf(source["data"]); ok {
		decoded = bytes
		out.Bytes = &bytes
	}
	what := blockType
	if out.MediaType != nil {
		what = blockType + " " + *out.MediaType
	}
	label := "[" + what
	if out.Bytes != nil {
		label += ", " + strconv.FormatInt(decoded, 10) + " bytes"
	}
	label += "]"
	out.Text = &label
	return out
}

// resultTextOf is the text of a tool result: its content string, else its text blocks joined
// by newlines — with one marker per wordless block, so a result that only carried an image
// does not read as empty.
func resultTextOf(content json.RawMessage) string {
	var asString string
	if len(content) > 0 && !isNull(content) {
		if err := json.Unmarshal(content, &asString); err == nil {
			return asString
		}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return ""
	}
	var parts []string
	for _, item := range items {
		block, ok := asObject(item)
		if !ok {
			continue
		}
		if text, ok := stringField(block, "text"); ok && text != "" {
			parts = append(parts, text)
			continue
		}
		blockType := "unknown"
		if t, ok := stringField(block, "type"); ok {
			blockType = t
		}
		marker := mediaBlockOf(blockType, block)
		parts = append(parts, *marker.Text)
	}
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n")
}

// inputBlockOf writes a user-side block as the history stores it: a prompt text, or a tool
// result with its id and error flag.
func inputBlockOf(block rawObject) ContentBlock {
	blockType := "unknown"
	if t, ok := stringField(block, "type"); ok {
		blockType = t
	}
	if blockType == "tool_result" {
		out := ContentBlock{Type: blockType}
		if id, ok := stringField(block, "tool_use_id"); ok {
			out.ToolUseID = &id
		}
		if isErr, ok := boolField(block, "is_error"); ok && isErr {
			out.IsError = &isErr
		}
		if text := resultTextOf(block["content"]); text != "" {
			capped, cut := capText(text)
			out.Text = &capped
			out.Truncated = cut
		}
		return out
	}
	if text, ok := stringField(block, "text"); ok {
		out := ContentBlock{Type: blockType}
		capped, cut := capText(text)
		out.Text = &capped
		out.Truncated = cut
		return out
	}
	return mediaBlockOf(blockType, block)
}

// outputBlockOf writes an assistant-side block: its text, or the tool it wants to run with
// its name and whole input. A thinking block's words arrive under `thinking`, which the UI
// renders, so they are kept as they came.
func outputBlockOf(block rawObject) ContentBlock {
	out := ContentBlock{Type: "unknown"}
	if t, ok := stringField(block, "type"); ok {
		out.Type = t
	}
	if text, ok := stringField(block, "text"); ok {
		capped, cut := capText(text)
		out.Text = &capped
		out.Truncated = cut
	}
	if thinking, ok := stringField(block, "thinking"); ok {
		capped, cut := capText(thinking)
		out.Thinking = &capped
		out.Truncated = cut
	}
	if out.Type == "tool_use" {
		if name, ok := stringField(block, "name"); ok {
			out.Name = &name
		}
		if input, ok := block["input"]; ok {
			out.Input = json.RawMessage(compactOf(input))
		}
	}
	return out
}

// isJSSpace reports whether r counts as whitespace in JavaScript (\s and trim): Go's space
// set minus NEL, plus the BOM.
func isJSSpace(r rune) bool {
	return r == 0xFEFF || (unicode.IsSpace(r) && r != 0x85)
}

// oneLineOf folds whitespace runs to single spaces and trims the ends: everything a `what`
// line carries is one line.
func oneLineOf(text string) string {
	var folded strings.Builder
	space := false
	for _, r := range text {
		if isJSSpace(r) {
			space = true
			continue
		}
		if space && folded.Len() > 0 {
			folded.WriteByte(' ')
		}
		space = false
		folded.WriteRune(r)
	}
	return strings.TrimFunc(folded.String(), isJSSpace)
}

// How many UTF-16 characters of a prompt a `what` keeps, and how long one tool argument in it
// may run before it is cut.
const (
	whatPromptChars = 40
	whatArgChars    = 24
)

// commandHeadOf is a Bash command's head: its first two words ("git status" out of "git
// status --porcelain"), cut.
func commandHeadOf(command string) string {
	var words []string
	for _, word := range strings.Split(oneLineOf(command), " ") {
		if word != "" {
			words = append(words, word)
		}
	}
	if len(words) > 2 {
		words = words[:2]
	}
	head := strings.Join(words, " ")
	if jsscalar.UTF16Len(head) > whatArgChars {
		return jsscalar.UTF16Slice(head, 0, whatArgChars)
	}
	return head
}

// toolArgOf is the argument a tool call's `what` names: the command's head for Bash, the
// pattern for Grep and Glob, a file's basename for the file tools; empty when the input
// carries none of these and the name stands alone.
func toolArgOf(name string, input json.RawMessage) string {
	fields, _ := asObject(input)
	text := func(key string) (string, bool) {
		if value, ok := stringField(fields, key); ok && value != "" {
			return value, true
		}
		return "", false
	}
	if name == "Bash" {
		command, _ := text("command")
		return commandHeadOf(command)
	}
	if name == "Grep" || name == "Glob" {
		pattern, _ := text("pattern")
		return jsscalar.UTF16Slice(oneLineOf(pattern), 0, whatArgChars)
	}
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if file, ok := text(key); ok {
			parts := strings.Split(file, "/")
			return parts[len(parts)-1]
		}
	}
	return ""
}

// inputPartOf is the input side of a `what`: the prompt's first 40 characters, else how many
// tool results came back.
func inputPartOf(blocks []ContentBlock) *string {
	for i := range blocks {
		block := &blocks[i]
		if block.Type != "text" || block.Text == nil {
			continue
		}
		text := oneLineOf(*block.Text)
		if text == "" {
			return nil
		}
		part := "prompt: " + jsscalar.UTF16Slice(text, 0, whatPromptChars)
		return &part
	}
	var results int
	for _, block := range blocks {
		if block.Type == "tool_result" {
			results++
		}
	}
	if results > 0 {
		part := strconv.Itoa(results) + " tool result"
		if results != 1 {
			part += "s"
		}
		return &part
	}
	return nil
}

// outputPartOf is the output side of a `what`: the first three tool calls with their telling
// argument ("+N" past that), else "text" when the answer carried anything at all.
func outputPartOf(blocks []ContentBlock) *string {
	var calls []ContentBlock
	for _, block := range blocks {
		if block.Type == "tool_use" {
			calls = append(calls, block)
		}
	}
	if len(calls) == 0 {
		if len(blocks) > 0 {
			part := "text"
			return &part
		}
		return nil
	}
	var named []string
	for _, call := range calls[:min(3, len(calls))] {
		name := ""
		if call.Name != nil {
			name = *call.Name
		}
		arg := toolArgOf(name, call.Input)
		switch {
		case arg != "":
			named = append(named, name+" "+arg)
		case name != "":
			named = append(named, name)
		default:
			named = append(named, "tool")
		}
	}
	part := strings.Join(named, ", ")
	if rest := len(calls) - len(named); rest > 0 {
		part += " +" + strconv.Itoa(rest)
	}
	return &part
}

// WhatOf is one line on what a request was, computed once at ingest so the browser never
// parses content to show it: "↳ <input kind>  → <what it did>", the sides joined by two
// spaces; nil with neither side.
func WhatOf(input *string, output []ContentBlock) *string {
	var parts []string
	if input != nil {
		parts = append(parts, "↳ "+*input)
	}
	if out := outputPartOf(output); out != nil {
		parts = append(parts, "→ "+*out)
	}
	if len(parts) == 0 {
		return nil
	}
	joined := strings.Join(parts, "  ")
	return &joined
}

// wordsOf is the words a text or thinking block carries, under whichever field the wire used
// for them.
func wordsOf(block *ContentBlock) (string, bool) {
	if block.Text != nil {
		return *block.Text, true
	}
	if block.Thinking != nil {
		return *block.Thinking, true
	}
	return "", false
}

// strEqual compares two optional strings the way === does on the wire's string-or-absent.
func strEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// growWords folds a streamed rewrite into the block it grows, when one is there: true when
// it was folded.
func growWords(list []ContentBlock, next ContentBlock) bool {
	var words string
	hasWords := false
	if next.Type == "text" || next.Type == "thinking" {
		if w, ok := wordsOf(&next); ok {
			words, hasWords = w, true
		}
	}
	if !hasWords {
		return false
	}
	for i := range list {
		block := &list[i]
		if block.Type != next.Type && block.Type != "text" {
			continue
		}
		base, ok := wordsOf(block)
		if !ok {
			base = "\x00"
		}
		if !strings.HasPrefix(words, base) {
			continue
		}
		if next.Text != nil {
			block.Text = next.Text
		}
		if next.Thinking != nil {
			block.Thinking = next.Thinking
		}
		return true
	}
	return false
}

// accumulateBlock folds one streamed rewrite of a message into its accumulated blocks: a
// longer text replaces its own prefix and a repeated tool_use is kept once, so the list
// reads as one message, not many. Thinking grows the same way, under its own field.
func accumulateBlock(list []ContentBlock, next ContentBlock) []ContentBlock {
	if growWords(list, next) {
		return list
	}
	if next.Type == "tool_use" {
		for i := range list {
			if list[i].Type != "tool_use" || !strEqual(list[i].Name, next.Name) {
				continue
			}
			if compactOf(list[i].Input) == compactOf(next.Input) {
				return list
			}
		}
	}
	return append(list, next)
}

// agentKey is which session and agent a line belongs to.
type agentKey struct {
	sessionID string
	agentID   string
}

// agentContext is the line's agent identity plus its file, carried through one line's
// parsing.
type agentContext struct {
	sessionID string
	agentID   string
	agentTag  string
	ts        int64
	file      FileIdentity
}

// agentKeyOf reads which session and agent a line belongs to. The remap pins the session: a
// job attempt's lines carry the attempt's own session id, never the job's.
func agentKeyOf(entry rawObject, file FileIdentity) agentKey {
	if file.Remap != nil {
		agentID := file.Remap.AgentID
		if file.AgentID != nil {
			agentID = *file.AgentID
		}
		return agentKey{sessionID: file.Remap.SessionID, agentID: agentID}
	}
	sessionID := file.SessionID
	if id, ok := stringField(entry, "sessionId"); ok {
		sessionID = id
	}
	if file.AgentID != nil {
		return agentKey{sessionID: sessionID, agentID: *file.AgentID}
	}
	if sidechain, ok := boolField(entry, "isSidechain"); ok && sidechain {
		agentID := "sidechain"
		if id, ok := stringField(entry, "agentId"); ok {
			agentID = id
		}
		return agentKey{sessionID: sessionID, agentID: agentID}
	}
	return agentKey{sessionID: sessionID, agentID: "main"}
}

// jsTimestampLayout is the ISO form Claude Code writes timestamps in; anything a transcript
// carries that this does not parse counts as no timestamp.
const jsTimestampLayout = "2006-01-02T15:04:05.999999999Z07:00"

// tsOf reads a line's timestamp as epoch ms.
func tsOf(entry rawObject) (int64, bool) {
	value, ok := stringField(entry, "timestamp")
	if !ok {
		return 0, false
	}
	parsed, err := time.Parse(jsTimestampLayout, value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// UsageOf reads the usage block's token counters, including the reasoning share of the output
// the provider breaks out (output_tokens_details). Shared with the route-line path, which
// reads the same wire usage.
func UsageOf(message rawObject) shared.Tokens {
	usage, ok := objField(message, "usage")
	if !ok {
		return shared.ZeroTokens
	}
	pick := func(key string) int64 {
		value, ok := numberField(usage, key)
		if !ok {
			return 0
		}
		return value
	}
	tokens := shared.Tokens{
		Input:      pick("input_tokens"),
		Output:     pick("output_tokens"),
		CacheRead:  pick("cache_read_input_tokens"),
		CacheWrite: pick("cache_creation_input_tokens"),
	}
	// the reasoning share of the output, when the provider breaks it out
	if details, ok := objField(usage, "output_tokens_details"); ok {
		if thinking, ok := numberField(details, "thinking_tokens"); ok && thinking > 0 {
			tokens.Thinking = &thinking
		}
	}
	return tokens
}

// Billing is how a request bills beyond its token counts: the 1-hour cache writes, fast
// mode, geo and service tier. Empty strings and fields the usage block omits stay unset.
// Shared with the route-line path.
type Billing struct {
	CacheWrite1h *int64
	Speed        *string
	Geo          *string
	ServiceTier  *string
}

// BillingOf reads the billing fields the usage block names.
func BillingOf(message rawObject) Billing {
	var billing Billing
	usage, ok := objField(message, "usage")
	if !ok {
		return billing
	}
	if creation, ok := objField(usage, "cache_creation"); ok {
		if oneHour, ok := numberField(creation, "ephemeral_1h_input_tokens"); ok && oneHour > 0 {
			billing.CacheWrite1h = &oneHour
		}
	}
	if speed, ok := stringField(usage, "speed"); ok && speed != "" {
		billing.Speed = &speed
	}
	if geo, ok := stringField(usage, "inference_geo"); ok && geo != "" {
		billing.Geo = &geo
	}
	if tier, ok := stringField(usage, "service_tier"); ok && tier != "" {
		billing.ServiceTier = &tier
	}
	return billing
}

// agentKindOf keeps the file's kind, except inside a main transcript where sidechain agents
// are subagents.
func agentKindOf(ctx agentContext) shared.AgentKind {
	if ctx.file.Kind != shared.KindMain {
		return ctx.file.Kind
	}
	if ctx.agentID == "main" {
		return shared.KindMain
	}
	return shared.KindSubagent
}

// parentIDOf is the agent chain this line's agent hangs under: the remap's parent, else main
// for anything that is not the main agent.
func parentIDOf(ctx agentContext) *string {
	if ctx.file.Remap != nil && ctx.file.Remap.ParentID != nil {
		return ctx.file.Remap.ParentID
	}
	if ctx.agentID == "main" {
		return nil
	}
	parent := "main"
	return &parent
}

// agentDescriptorOf emits an agent's descriptor on its first sighting; later lines get nil.
func agentDescriptorOf(state *TranscriptState, ctx agentContext) *AgentDescriptor {
	if _, seen := state.seenAgents[ctx.agentTag]; seen {
		return nil
	}
	state.seenAgents[ctx.agentTag] = struct{}{}
	var name, agentType, description *string
	if meta := ctx.file.AgentMeta; meta != nil {
		agentType = &meta.AgentType
		name = agentType
		description = &meta.Description
	}
	return &AgentDescriptor{
		SessionID:   ctx.sessionID,
		ID:          ctx.agentID,
		ParentID:    parentIDOf(ctx),
		Kind:        agentKindOf(ctx),
		Name:        name,
		AgentType:   agentType,
		Description: description,
	}
}

// firstTextOf is the first text a user message carries: its content string, else its first
// text block; else none.
func firstTextOf(message rawObject) (string, bool) {
	raw, ok := message["content"]
	if !ok {
		return "", false
	}
	var asString string
	if len(raw) > 0 && !isNull(raw) {
		if err := json.Unmarshal(raw, &asString); err == nil {
			if strings.TrimFunc(asString, isJSSpace) != "" {
				return asString, true
			}
			return "", false
		}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", false
	}
	for _, item := range items {
		block, ok := asObject(item)
		if !ok {
			continue
		}
		if text, ok := stringField(block, "text"); ok && strings.TrimFunc(text, isJSSpace) != "" {
			return text, true
		}
	}
	return "", false
}

// emitPromptTitle names a subagent after the head of its first prompt, emitted once as its
// title: names it when no meta file did. A job's own transcript never renames the job — the
// job's title is the job files' business.
func emitPromptTitle(state *TranscriptState, message rawObject, ctx agentContext, emit *TranscriptEmit) {
	if agentKindOf(ctx) != shared.KindSubagent {
		return
	}
	if _, titled := state.prompts[ctx.agentTag]; titled {
		return
	}
	text, ok := firstTextOf(message)
	if !ok {
		return
	}
	state.prompts[ctx.agentTag] = struct{}{}
	title := shared.PromptTitle(text)
	emit.Agents = append(emit.Agents, AgentDescriptor{
		SessionID:   ctx.sessionID,
		ID:          ctx.agentID,
		ParentID:    parentIDOf(ctx),
		Kind:        agentKindOf(ctx),
		AgentType:   fileAgentTypeOf(ctx),
		Description: fileDescriptionOf(ctx),
		Title:       &title,
	})
}

func fileAgentTypeOf(ctx agentContext) *string {
	if ctx.file.AgentMeta == nil {
		return nil
	}
	return &ctx.file.AgentMeta.AgentType
}

func fileDescriptionOf(ctx agentContext) *string {
	if ctx.file.AgentMeta == nil {
		return nil
	}
	return &ctx.file.AgentMeta.Description
}

// namedTypes maps a naming line's type to the session field it names.
var namedTypes = map[string]string{
	"custom-title": "customTitle",
	"agent-name":   "agentName",
	"ai-title":     "aiTitle",
}

// namedMetaOf reads a naming line ({"type":"custom-title",…}): the field it names, when it
// names it with a real value.
func namedMetaOf(entry rawObject) (field string, value string, ok bool) {
	entryType, _ := stringField(entry, "type")
	field, ok = namedTypes[entryType]
	if !ok {
		return "", "", false
	}
	if v, isString := stringField(entry, field); isString && v != "" {
		return field, v, true
	}
	return "", "", false
}

// nonEmpty reads key as a non-empty string, nil for anything else.
func nonEmpty(obj rawObject, key string) *string {
	if value, ok := stringField(obj, key); ok && value != "" {
		return &value
	}
	return nil
}

// sessionMetaOf reads the naming data a line carries; nil when it carries none. A remapped
// file never names its session: the job's own title, slug and branch stand.
func sessionMetaOf(entry rawObject, sessionID string, named bool) *SessionMeta {
	var nameField, nameValue string
	hasName := false
	var slug, branch *string
	if named {
		nameField, nameValue, hasName = namedMetaOf(entry)
		slug = nonEmpty(entry, "slug")
		branch = nonEmpty(entry, "gitBranch")
	}
	cwd := nonEmpty(entry, "cwd")
	ccVersion := nonEmpty(entry, "version")
	if !hasName && slug == nil && branch == nil && cwd == nil && ccVersion == nil {
		return nil
	}
	meta := &SessionMeta{ID: sessionID}
	if cwd != nil {
		meta.Cwd = cwd
	}
	if ccVersion != nil {
		meta.CcVersion = ccVersion
	}
	if hasName {
		setNamed(meta, nameField, nameValue)
	}
	if slug != nil {
		meta.Slug = slug
	}
	if branch != nil {
		meta.Branch = branch
	}
	return meta
}

// setNamed sets the one session field a naming line names.
func setNamed(meta *SessionMeta, field, value string) {
	switch field {
	case "customTitle":
		meta.CustomTitle = &value
	case "agentName":
		meta.AgentName = &value
	case "aiTitle":
		meta.AITitle = &value
	}
}

// jsStringOf prints a JSON value the way JavaScript's String() does for the value types a
// uuid can be; objects and arrays (never a uuid) fall back to their JSON text.
func jsStringOf(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || isNull(raw) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		if value, err := strconv.ParseFloat(num.String(), 64); err == nil {
			return jsscalar.JSNumber(value), true
		}
		return "", false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return strconv.FormatBool(b), true
	}
	return compactOf(raw), true
}

// requestIdOf is the id one model call is known by: the API request id, else the assistant
// message's own id — a subagent transcript names no request and writes one entry per block,
// each naming the same message — else the entry's uuid (or timestamp).
func requestIdOf(entry rawObject, message rawObject, ctx agentContext) string {
	if id, ok := stringField(entry, "requestId"); ok {
		return id
	}
	if id, ok := stringField(message, "id"); ok && id != "" {
		return id
	}
	if raw, ok := entry["uuid"]; ok {
		if text, isValue := jsStringOf(raw); isValue {
			return "msg:" + text
		}
	}
	return "msg:" + strconv.FormatInt(ctx.ts, 10)
}

// requestOf builds the request record one assistant message stands for; nil when the message
// names no model.
func requestOf(state *TranscriptState, entry rawObject, message rawObject, ctx agentContext) *shared.RequestRecord {
	model, ok := stringField(message, "model")
	if !ok || model == "" {
		return nil
	}
	var latency *int64
	if lastUserTs, seen := state.lastUserTs[ctx.agentTag]; seen && ctx.ts >= lastUserTs {
		wait := ctx.ts - lastUserTs
		latency = &wait
	}
	upstream := ""
	if ctx.file.Upstream != nil {
		upstream = *ctx.file.Upstream
	}
	var stopReason *string
	if reason, ok := stringField(message, "stop_reason"); ok {
		stopReason = &reason
	}
	request := shared.RequestRecord{
		ID:         requestIdOf(entry, message, ctx),
		SessionID:  ctx.sessionID,
		AgentID:    ctx.agentID,
		Model:      model,
		Upstream:   upstream,
		Ts:         ctx.ts,
		LatencyMs:  latency,
		Tokens:     UsageOf(message),
		StopReason: stopReason,
		Provider:   shared.ProviderOf(model),
	}
	billing := BillingOf(message)
	request.CacheWrite1h = billing.CacheWrite1h
	request.Speed = billing.Speed
	request.Geo = billing.Geo
	request.ServiceTier = billing.ServiceTier
	request.RequestID, _ = stringField(entry, "requestId")
	request.TailKeys = []string{}
	return &request
}

// takePendingInput is the request's logged input, taken by its FIRST entry only: a
// subagent's later block entries arrive after the tool results its own earlier blocks caused,
// and those results belong to the next request.
func takePendingInput(state *TranscriptState, requestID string, ctx agentContext) ([]ContentBlock, bool) {
	key := ctx.agentTag + "␟" + requestID
	if state.inputTaken.has(key) {
		return nil, false
	}
	state.inputTaken.set(key, true, 512)
	input, ok := state.pendingInput.get(ctx.agentTag)
	state.pendingInput.delete(ctx.agentTag)
	return input, ok
}

// captureContent captures a request's INPUT and OUTPUT for the history. OUTPUT is the
// assistant message's blocks, accumulated across the entries a stream writes for one request
// id; INPUT is the user and tool_result entries the chain logged since the agent's previous
// request, consumed by the request's first entry. Returns the request's one-line `what`,
// from the input just taken and the output accumulated so far.
func captureContent(
	state *TranscriptState,
	entry rawObject,
	message rawObject,
	ctx agentContext,
	emit *TranscriptEmit,
) *string {
	requestID := requestIdOf(entry, message, ctx)
	output, _ := state.streamOutput.get(requestID)
	for _, block := range contentBlocks(message) {
		output = accumulateBlock(output, outputBlockOf(block))
	}
	state.streamOutput.set(requestID, output, streamOutputCap)
	input, hasInput := takePendingInput(state, requestID, ctx)
	capture := ContentCapture{RequestID: requestID}
	if hasInput && len(input) > 0 {
		capture.Input = stringifyBlocks(input)
	}
	if len(output) > 0 {
		capture.Output = stringifyBlocks(output)
	}
	emit.Content = append(emit.Content, capture)
	// a non-null stop_reason ends the request: its capture holds the whole output, so the
	// window need not keep it open for a stream that will never grow
	if _, ended := stringField(message, "stop_reason"); ended {
		state.streamOutput.delete(requestID)
	}
	// the entry that took the logged input computes the `what`'s input side; a streamed
	// message's later entries see no input of their own, so the remembered part stands in
	var inputPart *string
	if remembered, ok := state.whatInput.get(requestID); ok {
		inputPart = &remembered
	}
	if hasInput {
		if taken := inputPartOf(input); taken != nil {
			inputPart = taken
			state.whatInput.set(requestID, *taken, whatInputCap)
		}
	}
	return WhatOf(inputPart, output)
}

// assistantErrorStatus reads an assistant entry Claude Code wrote for a request that failed:
// its status, else none.
func assistantErrorStatus(entry rawObject, message rawObject) (int64, bool) {
	isAPIError, _ := boolField(entry, "isApiErrorMessage")
	model, _ := stringField(message, "model")
	if !isAPIError && model != SyntheticModel {
		return 0, false
	}
	for _, block := range contentBlocks(message) {
		if blockType, ok := stringField(block, "type"); !ok || blockType != "text" {
			continue
		}
		if text, ok := stringField(block, "text"); ok {
			if status, retry := APIErrorStatus(text); retry {
				return status, true
			}
		}
	}
	return 0, false
}

// systemErrorStatus reads a system entry recording one retry ("api_error" with the HTTP
// status), else none.
func systemErrorStatus(entry rawObject) (int64, bool) {
	entryType, _ := stringField(entry, "type")
	subtype, _ := stringField(entry, "subtype")
	if entryType != "system" || subtype != "api_error" {
		return 0, false
	}
	if errObject, ok := objField(entry, "error"); ok {
		if status, ok := numberField(errObject, "status"); ok {
			return status, retryable(status)
		}
	}
	status, ok := numberField(entry, "status")
	if !ok {
		return 0, false
	}
	return status, retryable(status)
}

// noticeOf writes the notice an error entry stands for.
func noticeOf(entry rawObject, ctx agentContext) Notice {
	kind, _ := stringField(entry, "error")
	what, known := noticeWhat[kind]
	if !known {
		what = "error"
	}
	return Notice{SessionID: ctx.sessionID, AgentID: ctx.agentID, Ts: ctx.ts, What: what}
}

// apiErrorOf writes the API error record a status stands for, attributed to the line's agent.
func apiErrorOf(ctx agentContext, status int64) shared.APIErrorRecord {
	sessionID, agentID := ctx.sessionID, ctx.agentID
	return shared.APIErrorRecord{
		Ts:        ctx.ts,
		SessionID: &sessionID,
		AgentID:   &agentID,
		Status:    status,
		Source:    shared.SourceTranscript,
	}
}

// applyAssistant reads one assistant entry: remembers its tool_use blocks, reports the errors
// it carries, and emits the request it stands for with its captured content.
func applyAssistant(state *TranscriptState, entry rawObject, message rawObject, ctx agentContext, emit *TranscriptEmit) {
	for _, block := range contentBlocks(message) {
		blockType, _ := stringField(block, "type")
		id, idOK := stringField(block, "id")
		name, nameOK := stringField(block, "name")
		if blockType != "tool_use" || !idOK || !nameOK {
			continue
		}
		// bounded like its sibling maps: a tool_use whose result never lands (an Esc
		// interrupt, a crashed subagent) loses only its duration once the window moves past
		// it, instead of leaking forever
		state.pendingTools.set(id, pendingTool{
			name:      name,
			startedAt: ctx.ts,
			sessionID: ctx.sessionID,
			agentID:   ctx.agentID,
			inputKey:  ToolInputKey(name, block["input"]),
		}, 512)
	}
	failed, hasFailed := assistantErrorStatus(entry, message)
	if hasFailed {
		emit.APIErrors = append(emit.APIErrors, apiErrorOf(ctx, failed))
	}
	isAPIError, _ := boolField(entry, "isApiErrorMessage")
	if isAPIError {
		emit.Notices = append(emit.Notices, noticeOf(entry, ctx))
	}
	// Claude Code's own messages and the API errors it reports are not model requests
	model, _ := stringField(message, "model")
	if hasFailed || model == SyntheticModel || isAPIError {
		return
	}
	request := requestOf(state, entry, message, ctx)
	if request == nil {
		return
	}
	emit.Requests = append(emit.Requests, *request)
	if what := captureContent(state, entry, message, ctx, emit); what != nil {
		last := &emit.Requests[len(emit.Requests)-1]
		last.What = what
		last.TailKeys = append(last.TailKeys, "what")
	}
	last := emit.Requests[len(emit.Requests)-1]
	anyTokens := false
	if seen, ok := state.lastRequest[ctx.agentTag]; ok {
		anyTokens = seen.anyTokens
	}
	if shared.TotalTokens(last.Tokens) > 0 {
		anyTokens = true
	}
	state.lastRequest[ctx.agentTag] = lastRequestEntry{request: last, anyTokens: anyTokens}
}

// applyResult reads a stream's closing `result` event: when no request reported tokens, its
// usage is the run's total.
func applyResult(state *TranscriptState, entry rawObject, file FileIdentity) TranscriptEmit {
	key := agentKeyOf(entry, file)
	tag := key.sessionID + ":" + key.agentID
	seen, ok := state.lastRequest[tag]
	if !ok || seen.anyTokens {
		return TranscriptEmit{}
	}
	tokens := UsageOf(entry)
	if shared.TotalTokens(tokens) == 0 {
		return TranscriptEmit{}
	}
	state.lastRequest[tag] = lastRequestEntry{request: seen.request, anyTokens: true}
	filled := seen.request
	filled.Tokens = tokens
	filled.Totals = true
	if !slices.Contains(filled.TailKeys, "totals") {
		filled.TailKeys = append(filled.TailKeys, "totals")
	}
	return TranscriptEmit{Requests: []shared.RequestRecord{filled}}
}

// toolResultOf pairs a tool_result block with the tool_use that asked for it: the call's
// duration, ok flag and identity. A result whose tool_use was never read — the file opened on a
// later offset, or after a compaction — has no pair, so it reports false and the caller emits
// nothing: an unmatched result is not a tool call.
func toolResultOf(state *TranscriptState, block rawObject, toolUseID string, ctx agentContext) (shared.ToolCallRecord, bool) {
	pending, found := state.pendingTools.get(toolUseID)
	state.pendingTools.delete(toolUseID)
	if !found {
		return shared.ToolCallRecord{}, false
	}
	var duration *int64
	if pending.startedAt < ctx.ts {
		wait := ctx.ts - pending.startedAt
		duration = &wait
	}
	isErr, _ := boolField(block, "is_error")
	key := pending.inputKey
	return shared.ToolCallRecord{
		ID:         toolUseID,
		SessionID:  pending.sessionID,
		AgentID:    &pending.agentID,
		Name:       pending.name,
		StartedAt:  pending.startedAt,
		DurationMs: duration,
		Ok:         !isErr,
		InputKey:   &key,
	}, true
}

// isInterruption reads "[Request interrupted by user]" (and "… for tool use"), as a string
// content or a text block.
func isInterruption(message rawObject) bool {
	marker := func(text string) bool {
		return strings.HasPrefix(text, "[Request interrupted by user")
	}
	if raw, ok := message["content"]; ok {
		var asString string
		if len(raw) > 0 && !isNull(raw) && json.Unmarshal(raw, &asString) == nil && marker(asString) {
			return true
		}
	}
	for _, block := range contentBlocks(message) {
		if blockType, ok := stringField(block, "type"); ok && blockType == "text" {
			if text, ok := stringField(block, "text"); ok && marker(text) {
				return true
			}
		}
	}
	return false
}

// applyUser reads one user entry: the interrupts it marks, the input it logs for the next
// request, a subagent's prompt title, and the tool calls its results close.
func applyUser(state *TranscriptState, message rawObject, ctx agentContext, emit *TranscriptEmit) {
	if isInterruption(message) {
		emit.Interrupts = append(emit.Interrupts, Interrupt{
			SessionID: ctx.sessionID, AgentID: ctx.agentID, Ts: ctx.ts,
		})
	}
	captureUserInput(state, message, ctx)
	emitPromptTitle(state, message, ctx, emit)
	for _, block := range contentBlocks(message) {
		blockType, _ := stringField(block, "type")
		toolUseID, hasID := stringField(block, "tool_use_id")
		if blockType != "tool_result" || !hasID {
			continue
		}
		if call, ok := toolResultOf(state, block, toolUseID, ctx); ok {
			emit.ToolCalls = append(emit.ToolCalls, call)
		}
	}
}

// captureUserInput logs the input side of the next request: the prompt text, or tool
// results, until the agent answers again.
func captureUserInput(state *TranscriptState, message rawObject, ctx agentContext) {
	blocks, _ := state.pendingInput.get(ctx.agentTag)
	if content, ok := message["content"]; ok {
		var asString string
		if len(content) > 0 && !isNull(content) && json.Unmarshal(content, &asString) == nil {
			if asString != "" {
				block := ContentBlock{Type: "text"}
				capped, cut := capText(asString)
				block.Text = &capped
				block.Truncated = cut
				blocks = append(blocks, block)
			}
		} else {
			for _, raw := range contentBlocks(message) {
				blocks = append(blocks, inputBlockOf(raw))
			}
		}
	}
	if len(blocks) == 0 {
		return
	}
	if len(blocks) > 256 {
		blocks = blocks[len(blocks)-256:]
	}
	state.pendingInput.set(ctx.agentTag, blocks, pendingInputCap)
}

// FeedTranscriptLine parses one transcript line into records, updating the cross-line state
// (pending tools, last user ts).
func FeedTranscriptLine(state *TranscriptState, file FileIdentity, raw string) TranscriptEmit {
	entry := parseEntry(raw)
	if entry == nil {
		return TranscriptEmit{}
	}
	entryType, _ := stringField(entry, "type")
	if entryType == "result" {
		return applyResult(state, entry, file)
	}
	key := agentKeyOf(entry, file)
	// a naming line has no timestamp; a job's own transcript never renames its job session
	named := file.Remap == nil
	if named {
		if field, value, isNaming := namedMetaOf(entry); isNaming {
			meta := &SessionMeta{ID: key.sessionID}
			setNamed(meta, field, value)
			return TranscriptEmit{Session: meta}
		}
	}
	ts, ok := tsOf(entry)
	if !ok {
		return TranscriptEmit{}
	}
	ctx := agentContext{
		sessionID: key.sessionID,
		agentID:   key.agentID,
		agentTag:  key.sessionID + ":" + key.agentID,
		ts:        ts,
		file:      file,
	}
	var emit TranscriptEmit

	if agent := agentDescriptorOf(state, ctx); agent != nil {
		emit.Agents = append(emit.Agents, *agent)
	}
	emit.Session = sessionMetaOf(entry, key.sessionID, named)

	message, hasMessage := objField(entry, "message")
	if entryType == "assistant" && hasMessage {
		applyAssistant(state, entry, message, ctx, &emit)
		return emit
	}

	if retried, isRetry := systemErrorStatus(entry); isRetry {
		emit.APIErrors = append(emit.APIErrors, apiErrorOf(ctx, retried))
	}
	// user and system entries mark the boundary before the next assistant turn
	state.lastUserTs[ctx.agentTag] = ts
	if entryType == "user" && hasMessage {
		applyUser(state, message, ctx, &emit)
	}
	return emit
}
