// The usage half of the gateway: the token counters each API family reports, read off a copy of
// the answer's bytes while they stream past. Nothing here holds a whole streamed body — the SSE
// collectors keep one line at a time, and the document collector stops copying once its small
// cap is full, so a giant answer costs the gateway nothing.
package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// apiKind names one of the three API surfaces the gateway speaks: Anthropic's Messages API and
// the OpenAI Chat Completions and Responses APIs.
type apiKind string

const (
	kindAnthropic apiKind = "anthropic"
	kindChat      apiKind = "chat"
	kindResponses apiKind = "responses"
)

// family answers which of the two API families a kind belongs to, the split the config's two
// defaults follow.
func (k apiKind) family() string {
	if k == kindAnthropic {
		return "anthropic"
	}
	return "openai"
}

// Usage is the token count an answer reported, in the counters radar's route lines carry.
type Usage struct {
	Input      *int64 `json:"input_tokens,omitempty"`
	Output     *int64 `json:"output_tokens,omitempty"`
	CacheRead  *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheWrite *int64 `json:"cache_creation_input_tokens,omitempty"`
}

// spool renders the usage as a route line's usage block; nil when no counter was said, so the
// line records nothing rather than zeros.
func (u *Usage) spool() *domain.RouteUsage {
	if u == nil {
		return nil
	}
	out := domain.RouteUsage{}
	if u.Input != nil {
		out["input_tokens"] = *u.Input
	}
	if u.Output != nil {
		out["output_tokens"] = *u.Output
	}
	if u.CacheRead != nil {
		out["cache_read_input_tokens"] = *u.CacheRead
	}
	if u.CacheWrite != nil {
		out["cache_creation_input_tokens"] = *u.CacheWrite
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

// The usage blocks as each API reports them.

type anthropicUsageBlock struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}

// openaiCached is the details object both OpenAI shapes carry the cached count in: part of the
// input, not on top of it.
type openaiCached struct {
	Cached *int64 `json:"cached_tokens"`
}

type chatUsageBlock struct {
	Prompt     *int64        `json:"prompt_tokens"`
	Completion *int64        `json:"completion_tokens"`
	Details    *openaiCached `json:"prompt_tokens_details"`
}

type responsesUsageBlock struct {
	Input   *int64        `json:"input_tokens"`
	Output  *int64        `json:"output_tokens"`
	Details *openaiCached `json:"input_tokens_details"`
}

// of converts an Anthropic block, whose counters are already the ones radar reads.
func (b *anthropicUsageBlock) of() *Usage {
	if b == nil {
		return nil
	}
	return &Usage{Input: b.Input, Output: b.Output, CacheRead: b.CacheRead, CacheWrite: b.CacheWrite}
}

// of converts an OpenAI block, whose input counts the cached tokens: the cache split comes out
// of the input, never on top of it.
func ofOpenAI(input, output *int64, cached *openaiCached) *Usage {
	in := input
	var cacheRead *int64
	if cached != nil && cached.Cached != nil && input != nil {
		split := *input - *cached.Cached
		in = &split
		cacheRead = cached.Cached
	}
	return &Usage{Input: in, Output: output, CacheRead: cacheRead}
}

func (b *chatUsageBlock) of() *Usage {
	if b == nil {
		return nil
	}
	return ofOpenAI(b.Prompt, b.Completion, b.Details)
}

func (b *responsesUsageBlock) of() *Usage {
	if b == nil {
		return nil
	}
	return ofOpenAI(b.Input, b.Output, b.Details)
}

// The documents each non-stream answer parses as, usage nested where each family puts it.

type anthropicAnswer struct {
	Usage *anthropicUsageBlock `json:"usage"`
}

type chatAnswer struct {
	Usage *chatUsageBlock `json:"usage"`
}

type responsesAnswer struct {
	Usage *responsesUsageBlock `json:"usage"`
}

// The stream events each family's SSE carries its usage in. Anthropic's message_start wraps its
// counters in the message the event opens.

type anthropicStreamMessage struct {
	Usage *anthropicUsageBlock `json:"usage"`
}

type anthropicStartEvent struct {
	Message *anthropicStreamMessage `json:"message"`
}

type anthropicDeltaEvent struct {
	Usage *anthropicUsageBlock `json:"usage"`
}

type chatChunk struct {
	Usage *chatUsageBlock `json:"usage"`
}

type responsesCompletedEvent struct {
	Response *responsesAnswer `json:"response"`
}

// usageCap bounds the document collector's copy: a JSON answer's usage sits well inside a
// megabyte, and a body bigger than the cap stops feeding it, its usage reading as unknown.
const usageCap = 1 << 20

// sseDataCap bounds one SSE event's data the same way; a longer event has no usage worth
// keeping.
const sseDataCap = 1 << 20

// usageFinder collects usage from a copy of the answer's bytes as they stream past. Write never
// fails: a full cap or an unreadable line costs the usage, never the answer.
type usageFinder interface {
	Write(p []byte) (int, error)
	Usage() *Usage
}

// newUsageFinder builds the collector for one answer: streaming answers scan SSE events, single
// documents hold a bounded copy for one parse at the end.
func newUsageFinder(kind apiKind, streaming bool) usageFinder {
	if streaming {
		return &sseFinder{kind: kind}
	}
	return &documentFinder{kind: kind}
}

// documentFinder keeps a capped copy of a non-stream answer for one parse when it ends.
type documentFinder struct {
	kind apiKind
	buf  []byte
}

func (f *documentFinder) Write(p []byte) (int, error) {
	if room := usageCap - len(f.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		f.buf = append(f.buf, p[:room]...)
	}
	return len(p), nil
}

func (f *documentFinder) Usage() *Usage {
	switch f.kind {
	case kindAnthropic:
		var answer anthropicAnswer
		if json.Unmarshal(f.buf, &answer) != nil {
			return nil
		}
		return answer.Usage.of()
	case kindChat:
		var answer chatAnswer
		if json.Unmarshal(f.buf, &answer) != nil {
			return nil
		}
		return answer.Usage.of()
	case kindResponses:
		var answer responsesAnswer
		if json.Unmarshal(f.buf, &answer) != nil {
			return nil
		}
		return answer.Usage.of()
	}
	return nil
}

// sseFinder scans an event stream line by line, holding one event's data at a time. Each
// family's events carry their usage where the family puts it: Anthropic opens with
// message_start and refines in message_delta, a chat chunk closes with its usage block, and the
// Responses API says done with response.completed.
type sseFinder struct {
	kind      apiKind
	line      []byte
	data      []byte
	eventName string
	inData    bool
	start     *anthropicUsageBlock
	delta     *anthropicUsageBlock
	last      *Usage
}

func (f *sseFinder) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != '\n' {
			f.line = append(f.line, b)
			continue
		}
		f.readLine()
		f.line = f.line[:0]
	}
	return len(p), nil
}

// readLine handles one complete line: field lines fill the current event, a blank one closes it.
func (f *sseFinder) readLine() {
	line := f.line
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		f.closeEvent()
		return
	}
	if name, ok := trimField(line, "event:"); ok {
		f.eventName = name
		return
	}
	if data, ok := trimField(line, "data:"); ok {
		if len(f.data) > 0 {
			f.data = append(f.data, '\n')
		}
		if len(f.data)+len(data) <= sseDataCap {
			f.data = append(f.data, data...)
		}
		f.inData = true
	}
}

// closeEvent reads the finished event the way its family wants, then forgets it.
func (f *sseFinder) closeEvent() {
	defer func() {
		f.data = f.data[:0]
		f.eventName = ""
		f.inData = false
	}()
	if !f.inData {
		return
	}
	switch f.kind {
	case kindAnthropic:
		f.closeAnthropicEvent()
	case kindChat:
		var chunk chatChunk
		if json.Unmarshal(f.data, &chunk) == nil {
			if usage := chunk.Usage.of(); usage != nil {
				f.last = usage
			}
		}
	case kindResponses:
		if f.eventName != "response.completed" {
			return
		}
		var completed responsesCompletedEvent
		if json.Unmarshal(f.data, &completed) == nil && completed.Response != nil {
			if usage := completed.Response.Usage.of(); usage != nil {
				f.last = usage
			}
		}
	}
}

// closeAnthropicEvent keeps message_start's opening counters and every message_delta's overlay;
// the last delta wins on the fields it names.
func (f *sseFinder) closeAnthropicEvent() {
	switch f.eventName {
	case "message_start":
		var event anthropicStartEvent
		if json.Unmarshal(f.data, &event) == nil && event.Message != nil {
			f.start = event.Message.Usage
		}
	case "message_delta":
		var event anthropicDeltaEvent
		if json.Unmarshal(f.data, &event) == nil && event.Usage != nil {
			f.delta = event.Usage
		}
	}
}

func (f *sseFinder) Usage() *Usage {
	// a stream cut short before its closing blank line still said its usage: close what is open
	f.closeEvent()
	switch f.kind {
	case kindAnthropic:
		return overlayUsage(f.start, f.delta)
	case kindChat, kindResponses:
		return f.last
	}
	return nil
}

// overlayUsage lays a stream's deltas over its opening block: a field the deltas never named
// keeps the opening value.
func overlayUsage(start, delta *anthropicUsageBlock) *Usage {
	if start == nil && delta == nil {
		return nil
	}
	out := &Usage{}
	if start != nil {
		out.Input, out.Output = start.Input, start.Output
		out.CacheRead, out.CacheWrite = start.CacheRead, start.CacheWrite
	}
	if delta != nil {
		if delta.Input != nil {
			out.Input = delta.Input
		}
		if delta.Output != nil {
			out.Output = delta.Output
		}
		if delta.CacheRead != nil {
			out.CacheRead = delta.CacheRead
		}
		if delta.CacheWrite != nil {
			out.CacheWrite = delta.CacheWrite
		}
	}
	return out
}

// trimField reads one SSE field's value: the name, then one optional space.
func trimField(line []byte, name string) (string, bool) {
	if !bytes.HasPrefix(line, []byte(name)) {
		return "", false
	}
	value := line[len(name):]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(value), true
}
