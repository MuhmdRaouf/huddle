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
)

// MaxText is the most bytes of one item's text or input kept; a longer one is cut on a rune
// boundary and marked Cut.
const MaxText = 4096

// MaxRead is how many bytes one Read consumes at most: it stops after the first complete line
// that takes it past this, so one huge line is still read whole.
const MaxRead = 1 << 20

// Item is one conversation item.
type Item struct {
	ID         string `json:"id"` // "<line uuid>:<block index>", stable across reads
	Kind       string `json:"kind"`
	At         string `json:"at"` // the line's timestamp as Claude Code wrote it
	Text       string `json:"text,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"` // the line's thinkingDurationMs, thinking items only
	Tool       string `json:"tool,omitempty"`
	ToolUseID  string `json:"tool_use_id,omitempty"`
	Input      string `json:"input,omitempty"` // a tool call's input as compact JSON
	IsError    bool   `json:"is_error,omitempty"`
	Cut        bool   `json:"cut,omitempty"`
	Parent     string `json:"parent,omitempty"`     // the line's parentUuid, on file items, so a view joins a file to its prompt
	Path       string `json:"path,omitempty"`       // a file item's displayPath
	Lines      int    `json:"lines,omitempty"`      // a file item's totalLines, else numLines
	MediaType  string `json:"media_type,omitempty"` // an image item's source media type
	Bytes      int    `json:"bytes,omitempty"`      // an image item's decoded size, from the base64 length alone
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
	Attachment         json.RawMessage `json:"attachment"`
	Message            struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
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
// prompt and may carry the content that was read of it; other subtypes are left out.
type attachment struct {
	Type        string          `json:"type"`
	FileName    string          `json:"filename"`
	DisplayPath string          `json:"displayPath"`
	Content     json.RawMessage `json:"content"`
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
	for {
		if err := ctx.Err(); err != nil {
			return Page{Items: []Item{}}, err
		}
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			consumed += int64(len(raw))
			page.Items = append(page.Items, itemsOf(raw, keepSidechain)...)
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

// itemsOf decodes one complete transcript line into its items, in content order. A line that is
// not JSON, not a user, assistant or attachment turn, or harness-injected yields nothing; a
// subagent's sidechain line yields nothing unless keepSidechain.
func itemsOf(raw []byte, keepSidechain bool) []Item {
	var line transcriptLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return nil
	}
	if line.IsMeta || (line.IsSidechain && !keepSidechain) {
		return nil
	}
	switch line.Type {
	case KindUser:
		return userItems(line)
	case KindAssistant:
		return assistantItems(line)
	case "attachment":
		return attachmentItems(line)
	default:
		return nil
	}
}

// userItems answers a user line's items: a string content is the prompt itself, a block array
// yields its text blocks as prompts, its image blocks as the pictures they carry and its
// tool_result blocks as the answers they carry. A prompt that is empty but for spacing is no
// words the person typed.
func userItems(line transcriptLine) []Item {
	var prompt string
	if err := json.Unmarshal(line.Message.Content, &prompt); err == nil {
		if strings.TrimSpace(prompt) == "" {
			return nil
		}
		return []Item{textItem(line, 0, KindUser, prompt)}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(line.Message.Content, &blocks); err != nil {
		return nil
	}
	var items []Item
	for i, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			items = append(items, textItem(line, i, KindUser, block.Text))
		case "image":
			items = append(items, imageItem(line, i, block))
		case "tool_result":
			items = append(items, resultItem(line, i, block))
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
// with no content still yields its item, and an attachment of another subtype yields nothing.
func attachmentItems(line transcriptLine) []Item {
	var att attachment
	if err := json.Unmarshal(line.Attachment, &att); err != nil {
		return nil
	}
	if att.Type != "file" {
		return nil
	}
	var content attachedContent
	if len(att.Content) > 0 {
		if err := json.Unmarshal(att.Content, &content); err != nil {
			return nil
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
	return []Item{item}
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
