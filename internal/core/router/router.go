// Routes Claude Code's API requests by model: the provider's own ids to it with its key, another
// plugin's ids to that plugin's router unchanged, and everything else — every `claude-*` request —
// to Anthropic as is. Only this machine may use it: the server listens on 127.0.0.1 and refuses
// any other Host (DNS rebinding). The router must never cut Claude off: every error path degrades
// to an answer, never a dropped connection.
package router

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/andybalholm/brotli"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// RouterOptions is what the router needs to know: whose router this is (the resolved provider:
// env model overrides applied), where each kind of request goes, and the plugs the caller owns
// (the key, the log, the spool, the budget gate, the front's token).
type RouterOptions struct {
	// Provider is whose router this is, as resolved for this process (ResolveProvider): the model
	// ids it claims, the fields it strips, its health path and every line it prints.
	Provider domain.Provider
	Port     int
	// Anthropic is where every request for a model this provider does not serve goes, unchanged.
	Anthropic *url.URL
	// ProviderURL is the provider's Anthropic-compatible endpoint.
	ProviderURL *url.URL
	// Key reads the provider's key, only when one of its models is requested (never on the
	// `claude-*` path); "" or an error when there is none. Never logged. The router serves it
	// through its key cache: one shared lookup at a time, kept until the provider refuses the key.
	Key func(ctx context.Context) (string, error)
	// KeyLookup reads the provider's key with its outcome typed: a keystore timeout and a
	// missing key stay distinct (a 503 and a 400). It takes precedence over Key.
	KeyLookup func(ctx context.Context) (ports.ProviderKey, *ports.WorkerError)
	// Log receives one line per request: method, path, model, route, status. Never headers or
	// bodies.
	Log func(line string)
	// Peers says which other plugin's router serves a model this one does not; set by the service,
	// nil in tests.
	Peers func(model string) (PeerTarget, bool)
	// Events receives one spool event per forwarded request and the health events (refusals, rate
	// limits, a budget stop), when Radar's spool is reachable.
	Events func(event domain.SpoolEvent)
	// AnthropicFail counts one upstream failure of a request the Anthropic route serves (every
	// `claude-*` request); the multi router owns the streak the count feeds. nil counts nothing.
	// AnthropicOk counts one request the route served with an answer.
	AnthropicFail func(code, model string)
	AnthropicOk   func()
	// Budget is Radar's budget stop in force for this provider's models (cached by the caller);
	// false: none.
	Budget func() (domain.BudgetStop, bool)
	// Env is the proxy environment upstream requests honour (HTTPS_PROXY, NO_PROXY, …).
	Env ProxyEnv
	// OnUpstream is called just before a request goes upstream, so the front knows the upstream may
	// have seen it.
	OnUpstream func(req *http.Request)
	// Token is the front's token, required on the data path when set: a request without it is
	// answered 403 and never read or forwarded (the worker behind its front takes one; tests on
	// port 0 run without).
	Token string
	// KeyTimeoutMs is how long a key lookup may take before a provider request is answered 503; 0
	// is the default two seconds.
	KeyTimeoutMs int64
	// IdleMs is the upstream silence that ends a request; 0 is the default ten minutes (never a
	// total timeout).
	IdleMs int64
	// BodyLimit is the body limit; 0 is the default 64 MiB.
	BodyLimit int64
	// Fallback is the lane's fallback list: the models a model's requests go to, in order,
	// after it answers three 5xx, 429 or dropped answers in a row. nil never falls back.
	Fallback []string
}

// PeerHop is the hop header a router stamps on a request it forwards to a peer, so no request is
// ever forwarded twice.
const PeerHop = "x-provider-router-hop"

// RequestID is the header the front stamps on a request it hands its worker, echoed back over IPC
// once it goes upstream.
const RequestID = "x-provider-router-request"

// keyTimeoutDefault is the key lookup's budget: a keychain prompt or a hung store answers the
// provider request 503, never hangs it.
const keyTimeoutDefault int64 = 2000

// keyStoreBudget is the store's share of the key budget: a store that does not answer in it is
// dropped for that lookup, so the key file still gets its turn inside the whole budget. Setup and
// the CLI read the store without it (their own 60 s tool budget stands).
func keyStoreBudget(timeoutMs int64) time.Duration {
	return time.Duration(timeoutMs*3/4) * time.Millisecond
}

// reasonBytes is the most of an error answer the router reads to say why it failed: the judge
// already holds the whole answer back, so these are its first bytes re-read — never a second read,
// and a success answer is never read at all.
const reasonBytes = 2048

// reasonChars is the longest reason a route event or a log line carries.
const reasonChars = 300

// usageLimitBytes is the most of a JSON answer usage tracking buffers: past it nothing more is
// kept and no usage is recorded (the same ceiling the router's other interception points keep, so
// a big answer is never held twice).
const usageLimitBytes = 64 * 1024

// promptRewriteLimitBytes is the most of a provider stream the prompt rewriter may hold back while
// waiting for its first `message_start`; past it the stream goes out as it came, unrewritten (the
// estimate is a courtesy, never a delay).
const promptRewriteLimitBytes = 64 * 1024

// captureMemoMax is the most hashes one router process remembers as already written; past it the
// oldest drops.
const captureMemoMax = 512

// newlineByte is the byte an SSE stream's lines are split on.
const newlineByte = 0x0a

// ── the request body's JSON ─────────────────────────────────────────────────────────────────────────────────────────

// parseBodyNode is the parsed JSON body, or nil when it is not JSON.
func parseBodyNode(body []byte) *jsfmt.Value {
	node, err := jsfmt.DecodeValue(body)
	if err != nil {
		return nil
	}
	return node
}

// modelIn is the `model` field of a parsed body, or "".
func modelIn(json *jsfmt.Value) string {
	if model := json.Get("model"); model != nil && model.Kind() == jsfmt.KindString {
		return model.Text()
	}
	return ""
}

// ── the prompt capture ─────────────────────────────────────────────────────────────────────────────────────────────
// What a request carried at its top level — its `system` and `tools`, never a header, a key or a
// message — stored once per distinct content. The capture runs on its own goroutine, on its own
// parse of the body's bytes, and the gzip and spool write with it; a request only waits for the
// hash when it writes its route event.

// capturedHeaderRE is the response headers a route event may carry, case-insensitively; nothing
// else ever leaves.
var capturedHeaderRE = regexp.MustCompile(`^(?i:request-id|x-request-id|retry-after|content-type|anthropic-ratelimit-.+|x-ratelimit-.+)$`)

// CapturedHeaders is the allow-listed response headers one answer carries, lowercased the way the
// route event spells a header name; nil when it answered with none of them.
func CapturedHeaders(header http.Header) *map[string]string {
	out := map[string]string{}
	for name, values := range header {
		if capturedHeaderRE.MatchString(name) {
			// An array header is joined as Node's join(", ") reads it.
			out[strings.ToLower(name)] = strings.Join(values, ", ")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

// writeCanonical is the canonical JSON of one value: every object's keys sorted, so the same
// content hashes the same however it arrived. Only parsed values reach it, so no undefined guard
// is needed (PromptHashOf drops the absent tops first).
func writeCanonical(b *strings.Builder, node *jsfmt.Value) {
	switch node.Kind() {
	case jsfmt.KindObject:
		keys := append([]string(nil), node.Keys()...)
		sort.Strings(keys)
		b.WriteString("{")
		for i, key := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			jsfmt.WriteQuoted(b, key)
			b.WriteString(":")
			writeCanonical(b, node.Get(key))
		}
		b.WriteString("}")
	case jsfmt.KindArray:
		b.WriteString("[")
		for i, item := range node.Items() {
			if i > 0 {
				b.WriteString(",")
			}
			writeCanonical(b, item)
		}
		b.WriteString("]")
	case jsfmt.KindString:
		jsfmt.WriteQuoted(b, node.Text())
	case jsfmt.KindNumber:
		b.WriteString(string(node.Literal()))
	case jsfmt.KindBool:
		b.WriteString(strconv.FormatBool(node.Boolean()))
	case jsfmt.KindNull:
		b.WriteString("null")
	}
}

// billingPrefix opens the text block Claude Code puts in `system` on every request; its ids
// change each time, so the block stays out of the capture and its hash.
const billingPrefix = "x-anthropic-billing-header:"

// billingLine is what a billing block carries that the route event keeps: cc_prompt_id,
// cc_prev_req and cc_turn_index, each nil when the block did not carry it.
type billingLine struct {
	promptID    *string
	prevRequest *string
	turnIndex   *int64
}

// capturedPrompt is the part of a body a capture keeps: its `system` without the billing block,
// its `tools`, and the billing block's ids. A nil top is one the body did not carry.
type capturedPrompt struct {
	system  *jsfmt.Value
	tools   *jsfmt.Value
	billing billingLine
}

// promptOf is the body's captured prompt; false when the body carried neither `system` nor
// `tools` (a null counts as carried).
func promptOf(json *jsfmt.Value) (capturedPrompt, bool) {
	if !json.IsObject() {
		return capturedPrompt{}, false
	}
	return promptParts(json.Get("system"), json.Get("tools"))
}

// promptParts is the capture of two top-level members as the tree parsed them; false when both
// are absent (a null counts as carried).
func promptParts(system, tools *jsfmt.Value) (capturedPrompt, bool) {
	if system == nil && tools == nil {
		return capturedPrompt{}, false
	}
	prompt := capturedPrompt{system: system, tools: tools}
	if system.IsArray() {
		kept := make([]*jsfmt.Value, 0, len(system.Items()))
		for _, block := range system.Items() {
			if text, ok := block.Get("text").TextOf(); ok && strings.HasPrefix(text, billingPrefix) {
				prompt.billing = billingOf(strings.TrimPrefix(text, billingPrefix))
				continue
			}
			kept = append(kept, block)
		}
		if len(kept) < len(system.Items()) {
			prompt.system = jsfmt.Array(kept)
		}
	}
	return prompt, true
}

// capturedOf is the body's captured prompt, read on the body's own scan: the splitter finds the
// two members the capture needs and only they are parsed, so a body carrying neither — most
// requests — parses nothing at all. The provider route hands over the spans its rewrite already
// read, so the bytes are never scanned twice; nil spans have the capture scan for itself, and a
// body the scan refuses keeps the whole-body parse.
func capturedOf(original []byte, spans []keySpan) (capturedPrompt, bool) {
	if spans == nil {
		var whole bool
		if spans, whole = splitTopLevel(original); !whole {
			return promptOf(parseBodyNode(original))
		}
	}
	var system, tools *jsfmt.Value
	if span := spanOf(spans, "system"); span != nil {
		system = parseBodyNode(original[span.valueStart:span.valueEnd])
	}
	if span := spanOf(spans, "tools"); span != nil {
		tools = parseBodyNode(original[span.valueStart:span.valueEnd])
	}
	return promptParts(system, tools)
}

// billingOf reads the `key=value;` pairs of a billing block's text.
func billingOf(text string) billingLine {
	var billing billingLine
	for _, pair := range strings.Split(text, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || value == "" {
			continue
		}
		switch key {
		case "cc_prompt_id":
			billing.promptID = &value
		case "cc_prev_req":
			billing.prevRequest = &value
		case "cc_turn_index":
			if turn, err := strconv.ParseInt(value, 10, 64); err == nil {
				billing.turnIndex = &turn
			}
		}
	}
	return billing
}

// hash is sha256 of the canonical JSON of the prompt's `system` and `tools`.
func (p capturedPrompt) hash() string {
	var b strings.Builder
	b.WriteString("{")
	first := true
	for _, top := range []struct {
		key   string
		value *jsfmt.Value
	}{{"system", p.system}, {"tools", p.tools}} {
		if top.value == nil {
			continue
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		jsfmt.WriteQuoted(&b, top.key)
		b.WriteString(":")
		writeCanonical(&b, top.value)
	}
	b.WriteString("}")
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// PromptHashOf is the capture hash of a parsed body — sha256 of the canonical JSON of its
// `system` (without the billing block) and `tools` alone — false when the body carried neither
// (a null counts as carried).
func PromptHashOf(json *jsfmt.Value) (string, bool) {
	prompt, ok := promptOf(json)
	if !ok {
		return "", false
	}
	return prompt.hash(), true
}

// captureMemo marks the hashes this process has already written; the oldest drops past the memo.
type captureMemo struct {
	mu    sync.Mutex
	seen  map[string]bool
	order []string
}

func newCaptureMemo() *captureMemo {
	return &captureMemo{seen: map[string]bool{}}
}

// first marks the hash as seen; true when this is its first time here (so the event is due).
func (m *captureMemo) first(hash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen[hash] {
		return false
	}
	m.seen[hash] = true
	m.order = append(m.order, hash)
	if len(m.seen) > captureMemoMax {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.seen, oldest)
	}
	return true
}

// promptRef is one request's prompt hash and billing ids, filled in by its capture on its own
// goroutine; the route event reads them when the request completes, waiting for a capture that
// has started.
type promptRef struct {
	mu      sync.Mutex
	done    chan struct{}
	hash    string
	has     bool
	billing billingLine
}

func (p *promptRef) set(hash string, billing billingLine) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hash, p.has, p.billing = hash, true, billing
}

// wait blocks until a started capture has filled the ref in; a ref no capture started answers at
// once. The capture closes done after set, so a wait that returns reads what set wrote.
func (p *promptRef) wait() {
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (p *promptRef) Hash() *string {
	p.wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.has {
		return nil
	}
	return &p.hash
}

func (p *promptRef) Billing() billingLine {
	p.wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.billing
}

// ── the provider-bound body ────────────────────────────────────────────────────────────────────────────────────────

// stripFromNode removes the fields in place; true when at least one was present anywhere. Every
// element is visited: a field found in the first must not end the walk, or the rest keep theirs.
func stripFromNode(value *jsfmt.Value, rejected map[string]bool) bool {
	switch {
	case value == nil:
		return false
	case value.Kind() == jsfmt.KindArray:
		any := false
		for _, item := range value.Items() {
			if stripFromNode(item, rejected) {
				any = true
			}
		}
		return any
	case value.Kind() == jsfmt.KindObject:
		stripped := false
		// The keys are snapshotted: Del mutates the object's own order.
		for _, key := range append([]string(nil), value.Keys()...) {
			if rejected[key] {
				value.Del(key)
				stripped = true
			} else if stripFromNode(value.Get(key), rejected) {
				stripped = true
			}
		}
		return stripped
	default:
		return false
	}
}

// providerBody is a provider-bound request as this provider accepts it: rejected fields stripped
// (every message, every block), `max_tokens` clamped to the model's cap, and a web_search tool sent
// to the catalog model that serves it. The rewrite runs on the body's own bytes when forward's
// splitter read the body whole and no rejected field is named in it, and on the parsed tree for
// every other body — both answer the same JSON. The original bytes and model when nothing changed,
// else the rewritten body and the model it names.
func providerBody(w *routerWorker, original []byte, spans []keySpan, model, fallback string, req *http.Request) ([]byte, string) {
	if spans != nil && !stripsAny(w.opts.Provider, original) {
		if out, named, ok := providerBodySplice(w, original, spans, model, fallback, req); ok {
			return out, named
		}
	}
	return providerBodyTree(w, original, model, fallback, req)
}

// stripsAny is true when one of the provider's rejected fields may be named in the body: a cheap
// maybe over raw bytes that only ever sends a body to the parsed path, never keeps one from it — a
// name inside a string value strips nothing, and the parsed path leaves such a body as it came. A
// name spelled with an escape is invisible to the raw-byte test — the parse unescapes keys — so an
// escaped printable-ASCII byte anywhere sends the body to the parsed path too.
func stripsAny(provider domain.Provider, body []byte) bool {
	if len(provider.Strip) == 0 {
		return false
	}
	for _, field := range provider.Strip {
		if bytes.Contains(body, []byte(`"`+field+`"`)) {
			return true
		}
	}
	return escapedPrintableIn(body)
}

// escapedPrintableIn is true when the body carries a backslash-u-0-0 escape whose first hex digit
// is `2` through `7`: an escaped printable-ASCII byte, the one place a name the parse would
// unescape can hide from a raw-byte test. A control escape (first digit `0` or `1`) does not read
// as one.
func escapedPrintableIn(body []byte) bool {
	for at := 0; at < len(body); {
		found := bytes.Index(body[at:], []byte(`\u00`))
		if found < 0 {
			return false
		}
		at += found
		if at+5 <= len(body) && body[at+4] >= '2' && body[at+4] <= '7' {
			return true
		}
		at++
	}
	return false
}

// providerBodyTree is the rewrite on the parsed body: the fallback's rename, then the in-place
// edits, and the whole body re-encoded. Every body the splice does not take comes here.
func providerBodyTree(w *routerWorker, original []byte, model, fallback string, req *http.Request) ([]byte, string) {
	body := parseBodyNode(original)
	if fallback != "" {
		if body != nil {
			body.Set("model", jsfmt.String(fallback))
			original = []byte(body.EncodeLiteral())
		}
		model = fallback
	}
	if body == nil || !body.IsObject() {
		return original, model
	}
	if !adaptBody(w, body, model, req) {
		return original, model
	}
	if named := body.Get("model"); named == nil || named.Kind() != jsfmt.KindString {
		return []byte(body.EncodeLiteral()), model
	}
	return []byte(body.EncodeLiteral()), modelIn(body)
}

// providerBodySplice is the rewrite as splices on the body's own bytes: the model renamed, and
// `max_tokens` clamped to the model's cap when the member carries a number above it. It answers
// ok=false for a body whose edit needs the parse — a model member a rename would have to add.
func providerBodySplice(w *routerWorker, original []byte, spans []keySpan, model, fallback string, req *http.Request) ([]byte, string, bool) {
	provider := w.opts.Provider
	named := model
	var modelValue []byte
	if fallback != "" {
		named = fallback
		modelValue = []byte(jsfmt.String(fallback).EncodeLiteral())
	}
	if provider.WebSearchModel != nil && model != *provider.WebSearchModel {
		if tools := spanOf(spans, "tools"); tools != nil {
			raw := original[tools.valueStart:tools.valueEnd]
			// The bytes only hint; the parse says whether a web_search tool is really there. An
			// escaped byte hides a name from a raw-byte test, so an escape hints too.
			if bytes.Contains(raw, []byte("web_search")) || bytes.Contains(raw, []byte(`\u`)) {
				if !carriesWebSearch(parseBodyNode(raw)) {
					return nil, "", false
				}
				modelValue = []byte(jsfmt.String(*provider.WebSearchModel).EncodeLiteral())
				named = *provider.WebSearchModel
				w.log(fmt.Sprintf("%s %s model=%s → provider: web_search tool served by %s",
					req.Method, requestPath(req), model, *provider.WebSearchModel))
			}
		}
	}
	var capped *keySpan
	var capValue []byte
	if cap := catalogCap(provider, model); cap != nil {
		if tokens := spanOf(spans, "max_tokens"); tokens != nil {
			digits := original[tokens.valueStart:tokens.valueEnd]
			if len(digits) > 0 && (digits[0] == '-' || digits[0] >= '0' && digits[0] <= '9') {
				if value, err := json.Number(digits).Float64(); err == nil && value > float64(*cap) {
					// The span lives in the spans slice the capture goroutine also reads, and the
					// shift below writes it: shift a copy.
					span := *tokens
					capped, capValue = &span, []byte(strconv.FormatInt(*cap, 10))
				}
			}
		}
	}
	if modelValue == nil && capped == nil {
		return original, named, true
	}
	modelSpan := spanOf(spans, "model")
	if modelValue != nil && modelSpan == nil {
		return nil, "", false
	}
	out := original
	if modelValue != nil {
		out = spliced(original, *modelSpan, modelValue)
	}
	if capped != nil {
		if modelValue != nil && capped.valueStart >= modelSpan.valueEnd {
			shift := len(out) - len(original)
			capped.valueStart += shift
			capped.valueEnd += shift
		}
		out = spliced(out, *capped, capValue)
	}
	return out, named, true
}

// catalogCap is the model's output-token cap out of the provider's catalog, or nil when the
// catalog carries no such model.
func catalogCap(provider domain.Provider, model string) *int64 {
	for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
		if entry := provider.Catalog[tier]; entry.ID == model {
			return entry.MaxOutputTokens
		}
	}
	return nil
}

// adaptBody is the in-place edits a provider-bound body needs, one truth each: rejected fields
// stripped, `max_tokens` clamped down to the model's cap, and a web_search tool rerouted to the
// model that takes it (one line when it happens).
func adaptBody(w *routerWorker, record *jsfmt.Value, model string, req *http.Request) bool {
	provider := w.opts.Provider
	changed := false
	if len(provider.Strip) > 0 {
		rejected := make(map[string]bool, len(provider.Strip))
		for _, field := range provider.Strip {
			rejected[field] = true
		}
		changed = stripFromNode(record, rejected)
	}
	if cap := catalogCap(provider, model); cap != nil {
		if tokens := record.Get("max_tokens"); tokens != nil && tokens.Kind() == jsfmt.KindNumber {
			if value, err := tokens.Literal().Float64(); err == nil && value > float64(*cap) {
				record.Set("max_tokens", jsfmt.NumberLiteral(json.Number(strconv.FormatInt(*cap, 10))))
				changed = true
			}
		}
	}
	if provider.WebSearchModel != nil && model != *provider.WebSearchModel && carriesWebSearch(record.Get("tools")) {
		record.Set("model", jsfmt.String(*provider.WebSearchModel))
		changed = true
		w.log(fmt.Sprintf("%s %s model=%s → provider: web_search tool served by %s",
			req.Method, requestPath(req), model, *provider.WebSearchModel))
	}
	return changed
}

// carriesWebSearch is true when any tool of the request is a web_search server tool: its `type`
// starts with `web_search_` (`web_search_20250305`, …). A regular tool carries no `type` at all.
func carriesWebSearch(tools *jsfmt.Value) bool {
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Items() {
		if typ := tool.Get("type"); typ != nil && typ.Kind() == jsfmt.KindString && strings.HasPrefix(typ.Text(), "web_search_") {
			return true
		}
	}
	return false
}

// requestPath is the request's own path with its query.
func requestPath(req *http.Request) string {
	if req.RequestURI != "" {
		return req.RequestURI
	}
	return req.URL.RequestURI()
}

// UpstreamHeaders is the client's headers for the upstream. A provider-bound request swaps the
// caller's Anthropic credentials for the provider's key, so they never reach the provider; every
// other route keeps them untouched.
func UpstreamHeaders(incoming http.Header, target *url.URL, length int, key string) http.Header {
	out := ForwardHeaders(incoming, target, length)
	out.Del(RequestID)
	out.Del(PeerHop)
	// The front's token belongs to the loopback hop alone; it never reaches an upstream.
	out.Del(TokenHeader)
	if key != "" {
		out.Del("X-Api-Key")
		out.Set("Authorization", "Bearer "+key)
	}
	return out
}

// forwardable is why a target may not be forwarded to, or false: an http upstream must be loopback.
func forwardable(route string, u *url.URL) (string, bool) {
	if u.Scheme == "https" || domain.IsLoopback(u.Hostname()) {
		return "", false
	}
	return fmt.Sprintf("%s upstream must be https unless it is loopback (%s)", route, u.Host), true
}

// ── the worker one router server owns ──────────────────────────────────────────────────────────────────────────────

// routerWorker is the state one router server serves with: the budget stop's health dedup, the
// prompt hashes already captured, and the conversation estimator.
type routerWorker struct {
	opts      RouterOptions
	provider  domain.Provider
	claim     domain.ModelClaim
	captured  *captureMemo
	estimator *PromptEstimator
	// keys is the provider key every request shares, kept until the provider refuses it.
	keys *keyCache
	// fallback is the lane's model fallback; nil when the lane has no fallback list.
	fallback *laneFallback
	// budgetStop dedups the budget_stop health events: the scope last seen.
	budgetMu    sync.Mutex
	budgetKnown bool
	budgetScope string
}

// log prints one line when the caller listens.
func (w *routerWorker) log(line string) {
	if w.opts.Log != nil {
		w.opts.Log(line)
	}
}

// health appends one health event for this router's plugin, when anyone listens. Never the key, a
// header or a body.
func (w *routerWorker) health(event domain.RouterHealth, reason, model string) {
	if w.opts.Events != nil {
		w.opts.Events(domain.HealthEvent(w.provider.Name, event, reason, model, time.Now().UnixMilli()))
	}
}

// budget is the stop in force, with one `budget_stop` event each time a scope newly turns this
// router's models off.
func (w *routerWorker) budget(model string) (domain.BudgetStop, bool) {
	var stop domain.BudgetStop
	stopped := false
	if w.opts.Budget != nil {
		stop, stopped = w.opts.Budget()
	}
	w.budgetMu.Lock()
	defer w.budgetMu.Unlock()
	if stopped && (!w.budgetKnown || stop.Scope != w.budgetScope) {
		period := "period"
		if stop.Period != nil {
			period = string(*stop.Period)
		}
		w.health(domain.HealthBudgetStop, fmt.Sprintf("%s budget reached for this %s", stop.Scope, period), model)
	}
	w.budgetKnown, w.budgetScope = stopped, stop.Scope
	return stop, stopped
}

// newRouterWorker builds the lane a router server serves with: the key lookup adapter, the key
// cache and the request state. keyLookup is the worker's own key lookup (the key's source
// included, which its IPC answers name); nil takes opts.KeyLookup when it is set, else adapts
// opts.Key.
func newRouterWorker(opts RouterOptions, keyLookup func(ctx context.Context) (ports.ProviderKey, *ports.WorkerError)) *routerWorker {
	lookup := keyLookup
	if lookup == nil {
		lookup = opts.KeyLookup
	}
	if lookup == nil {
		lookup = func(ctx context.Context) (ports.ProviderKey, *ports.WorkerError) {
			// The plug's error and empty key are both "no key": a request is answered by the
			// outcome (no key, or the timeout), never by the error.
			if opts.Key == nil {
				return ports.ProviderKey{}, nil
			}
			value, err := opts.Key(ctx)
			if err != nil || value == "" {
				return ports.ProviderKey{}, nil
			}
			return ports.ProviderKey{Value: value}, nil
		}
	}
	timeout := keyTimeoutDefault
	if opts.KeyTimeoutMs != 0 {
		timeout = opts.KeyTimeoutMs
	}
	return &routerWorker{
		opts:      opts,
		provider:  opts.Provider,
		claim:     opts.Provider.ModelClaim(),
		captured:  newCaptureMemo(),
		estimator: NewPromptEstimator(),
		keys:      newKeyCache(lookup, keyStoreBudget(timeout)),
		fallback:  newLaneFallback(opts.Fallback),
	}
}

// routeOf is where a request for this model goes: "provider", "anthropic", or "peer:<name>".
func routeOf(claim domain.ModelClaim, model string, peers func(string) (PeerTarget, bool)) (string, PeerTarget, bool) {
	if domain.Claims(claim, model) {
		return "provider", PeerTarget{}, false
	}
	if model != "" && peers != nil {
		if peer, ok := peers(model); ok {
			return "peer:" + peer.Name, peer, true
		}
	}
	return "anthropic", PeerTarget{}, false
}

// hopPresent reports whether the request already carries the peer-hop header.
func hopPresent(req *http.Request) bool {
	return len(req.Header.Values(PeerHop)) > 0
}

// threadsBeta collects the message-threads tokens of the anthropic-beta header: every
// comma-separated token, trimmed, across all header lines, named in the spelling the request
// carried; nil when none. The message-threads betas are Anthropic-only.
func threadsBeta(req *http.Request) []string {
	var tokens []string
	for _, value := range req.Header.Values("anthropic-beta") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.TrimSpace(token); strings.HasPrefix(strings.ToLower(token), "message-threads-") {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

// textHeader is a request header as a non-empty string, or false.
func textHeader(req *http.Request, name string) (string, bool) {
	value := req.Header.Get(name)
	if value == "" {
		return "", false
	}
	return value, true
}

// routeModel is the model a route event names, and the model the request asked for when the lane
// fell back from it; set before the request goes upstream.
type routeModel struct {
	model    string
	fallback *string
}

// eventNote is the route event for one request, emitted when it completes; nil when nobody
// listens.
type eventNote func(status int64, route, upstream string, usage domain.RouteUsage, reason *string, headers *map[string]string)

// answerTiming is when one answer's head and first token arrived, in ms since the epoch; zero
// while not seen.
type answerTiming struct{ headAt, firstTokenAt int64 }

// noteOf builds the request's note: the ids Claude Code stamped the request with, the usage and
// the allow-listed headers the answer carried, the prompt hash and the billing block's ids — each
// only when there was one.
func (w *routerWorker) noteOf(req *http.Request, served *routeModel, startedAt int64, prompt *promptRef, timing *answerTiming) eventNote {
	if w.opts.Events == nil {
		return nil
	}
	return func(status int64, route, upstream string, usage domain.RouteUsage, reason *string, headers *map[string]string) {
		event := domain.RouteEvent{
			Ts:         isoNow(),
			Event:      domain.EventRoute,
			Plugin:     w.provider.Name,
			Model:      served.model,
			Fallback:   served.fallback,
			Upstream:   upstream,
			Route:      route,
			Status:     status,
			LatencyMs:  time.Now().UnixMilli() - startedAt,
			Error:      reason,
			Headers:    headers,
			PromptHash: prompt.Hash(),
		}
		billing := prompt.Billing()
		event.PromptID, event.PrevRequest, event.TurnIndex = billing.promptID, billing.prevRequest, billing.turnIndex
		if session, ok := textHeader(req, "x-claude-code-session-id"); ok {
			event.SessionID = &session
		}
		if agent, ok := textHeader(req, "x-claude-code-agent-id"); ok {
			event.AgentID = &agent
		}
		if parent, ok := textHeader(req, "x-claude-code-parent-agent-id"); ok {
			event.ParentAgentID = &parent
		}
		if timing.headAt != 0 {
			ttfb := timing.headAt - startedAt
			event.TtfbMs = &ttfb
		}
		if timing.firstTokenAt != 0 {
			ttft := timing.firstTokenAt - startedAt
			event.TtftMs = &ttft
		}
		if len(usage) > 0 {
			carried := usage
			event.Usage = &carried
		}
		if headers != nil {
			event.Utilization = domain.UnifiedUtilizationOf(*headers)
		}
		w.opts.Events(event)
	}
}

// capture reads what the request's body carried, once per distinct content, and writes its one
// capture event off the request's own goroutine, on the body's own bytes: the provider route hands
// over the spans its rewrite read, every other route has the capture scan for itself, and a
// provider-bound rewrite of the request never reaches the captured prompt. The ref is marked
// started under the lock at once, and the hash lands before the capture ends, so the route event,
// which waits for that end, always names it. A failure costs one log line.
func (w *routerWorker) capture(prompt *promptRef, original []byte, spans []keySpan) {
	if w.opts.Events == nil {
		return
	}
	prompt.mu.Lock()
	prompt.done = make(chan struct{})
	prompt.mu.Unlock()
	done := prompt.done
	safego.Go(w.log, func() {
		defer close(done)
		captured, ok := capturedOf(original, spans)
		if !ok {
			return
		}
		hash := captured.hash()
		prompt.set(hash, captured.billing)
		if !w.captured.first(hash) {
			return
		}
		body := jsfmt.Object([]string{"system", "tools"}, []*jsfmt.Value{captured.system, captured.tools}).EncodeLiteral()
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
		w.opts.Events(domain.CaptureEvent{
			Ts:         isoNow(),
			Event:      domain.EventCapture,
			Plugin:     w.provider.Name,
			PromptHash: hash,
			Gz:         base64.StdEncoding.EncodeToString(compressed.Bytes()),
		})
	})
}

// ── usage tracking ─────────────────────────────────────────────────────────────────────────────────────────────────

// UsageTracker reads an answer's usage as it passes through, never ahead of the pipe: an SSE
// stream is parsed line by line as it goes (only a partial line is ever held), a JSON body carries
// it at the top level and is buffered up to a cap before it is read once it ends — past the cap
// nothing is buffered and no usage is recorded. A compressed answer is read through its
// decompressed copy; the client's copy keeps its encoding.
type UsageTracker struct {
	push       func(chunk []byte)
	usage      func() domain.RouteUsage
	close      func()
	firstToken func() int64
}

// NewUsageTracker tracks the usage of one answer of this content type ("" when the answer carried
// none) and content encoding ("" when it is plain).
func NewUsageTracker(contentType, encoding string) *UsageTracker {
	if strings.Contains(contentType, "text/event-stream") {
		return encodedStreamTracker(encoding, sseUsageTracker())
	}
	if strings.Contains(contentType, "application/json") {
		return jsonUsageTracker(encoding)
	}
	return &UsageTracker{}
}

// Push reads one chunk of the passing answer.
func (t *UsageTracker) Push(chunk []byte) {
	if t != nil && t.push != nil {
		t.push(chunk)
	}
}

// Usage is the usage the answer carried; nil when it carried none.
func (t *UsageTracker) Usage() domain.RouteUsage {
	if t == nil || t.usage == nil {
		return nil
	}
	return t.usage()
}

// FirstTokenAt is when the stream's first content_block_delta passed through, in ms since the
// epoch; zero when this answer streamed none (a JSON answer, an error, an empty stream).
func (t *UsageTracker) FirstTokenAt() int64 {
	if t == nil || t.firstToken == nil {
		return 0
	}
	return t.firstToken()
}

// Close ends a tracker whose answer stops before its usage is read (a failed or abandoned call).
func (t *UsageTracker) Close() {
	if t != nil && t.close != nil {
		t.close()
	}
}

// streamDecoder opens the reader that decompresses one content encoding; nil for an encoding the
// router cannot read.
func streamDecoder(encoding string) func(io.Reader) (io.ReadCloser, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "gzip":
		return func(reader io.Reader) (io.ReadCloser, error) { return gzip.NewReader(reader) }
	case "deflate":
		return func(reader io.Reader) (io.ReadCloser, error) { return zlib.NewReader(reader) }
	case "br":
		return func(reader io.Reader) (io.ReadCloser, error) { return io.NopCloser(brotli.NewReader(reader)), nil }
	default:
		return nil
	}
}

// encodedStreamTracker reads a compressed stream through its decoder. The passing chunks are
// queued, never waited on: one goroutine decompresses them into the plain tracker, and the usage
// is read once the queue is closed and drained. A decoder more than the cap behind, or an encoding
// the router cannot read, records no usage.
func encodedStreamTracker(encoding string, plain *UsageTracker) *UsageTracker {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return plain
	}
	open := streamDecoder(encoding)
	if open == nil {
		return &UsageTracker{}
	}
	queue := newChunkQueue(usageLimitBytes)
	done := make(chan struct{})
	safego.Go(nil, func() {
		defer close(done)
		defer queue.stop()
		reader, err := open(queue)
		if err != nil {
			return
		}
		defer reader.Close()
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := reader.Read(buffer)
			if n > 0 {
				plain.Push(buffer[:n])
			}
			if readErr != nil {
				return
			}
		}
	})
	var once sync.Once
	var usage domain.RouteUsage
	read := func() {
		queue.end()
		<-done
		if !queue.overflowed() {
			usage = plain.Usage()
		}
	}
	return &UsageTracker{
		push: queue.push,
		usage: func() domain.RouteUsage {
			once.Do(read)
			return usage
		},
		close: queue.end,
		// the plain tracker saw the decompressed stream, so its first token is the answer's
		firstToken: func() int64 {
			once.Do(read)
			return plain.FirstTokenAt()
		},
	}
}

// chunkQueue hands the passing chunks to one reader in order: push never blocks, Read waits for
// the next chunk or the end.
type chunkQueue struct {
	mu      sync.Mutex
	ready   *sync.Cond
	chunks  [][]byte
	queued  int
	limit   int
	ended   bool
	stopped bool
	over    bool
}

func newChunkQueue(limit int) *chunkQueue {
	queue := &chunkQueue{limit: limit}
	queue.ready = sync.NewCond(&queue.mu)
	return queue
}

// push queues one chunk; past the limit the queue drops everything and ends.
func (q *chunkQueue) push(chunk []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ended || q.stopped {
		return
	}
	q.queued += len(chunk)
	if q.queued > q.limit {
		q.over, q.ended, q.chunks, q.queued = true, true, nil, 0
	} else {
		q.chunks = append(q.chunks, chunk)
	}
	q.ready.Broadcast()
}

// end says no more chunks come; the reader drains what is queued, then reads io.EOF.
func (q *chunkQueue) end() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ended = true
	q.ready.Broadcast()
}

// stop drops what is queued and every later chunk: the reader is gone.
func (q *chunkQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped, q.chunks, q.queued = true, nil, 0
}

func (q *chunkQueue) overflowed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.over
}

func (q *chunkQueue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.chunks) == 0 && !q.ended {
		q.ready.Wait()
	}
	if len(q.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, q.chunks[0])
	if n == len(q.chunks[0]) {
		q.chunks = q.chunks[1:]
	} else {
		q.chunks[0] = q.chunks[0][n:]
	}
	q.queued -= n
	return n, nil
}

// jsonUsageTracker buffers a JSON answer up to the cap, then reads its top-level usage once.
func jsonUsageTracker(encoding string) *UsageTracker {
	var chunks [][]byte
	size, over := int64(0), false
	var parsed domain.RouteUsage
	done := false
	return &UsageTracker{
		push: func(chunk []byte) {
			size += int64(len(chunk))
			if size > usageLimitBytes {
				over = true
			}
			if !over {
				chunks = append(chunks, append([]byte(nil), chunk...))
			}
		},
		usage: func() domain.RouteUsage {
			if !done {
				done = true
				if !over {
					parsed = topLevelUsage(concatChunks(chunks), encoding)
				}
			}
			return parsed
		},
	}
}

// topLevelUsage is the `usage` at the top level of the collected JSON body, when it parses and
// carries one.
func topLevelUsage(body []byte, encoding string) domain.RouteUsage {
	var parsed any
	if json.Unmarshal(InspectBody(body, encoding), &parsed) != nil {
		return nil
	}
	return nestedUsage(parsed)
}

// sseUsageTracker reads an SSE stream's usage line by line: message_start carries it inside
// `message`, message_delta at its top level; the later event's counters win. The first
// content_block_delta is the answer's first token, timed as it passes.
func sseUsageTracker() *UsageTracker {
	rest := ""
	var usage domain.RouteUsage
	firstTokenAt := int64(0)
	take := func(line string) {
		event, ok := sseEventAny(line)
		if !ok {
			return
		}
		if firstTokenAt == 0 && event["type"] == "content_block_delta" {
			firstTokenAt = time.Now().UnixMilli()
		}
		var found domain.RouteUsage
		switch event["type"] {
		case "message_start":
			found = nestedUsage(event["message"])
		case "message_delta":
			found = nestedUsage(event)
		}
		if found == nil {
			return
		}
		if usage == nil {
			usage = domain.RouteUsage{}
		}
		for key, value := range found {
			usage[key] = value
		}
	}
	return &UsageTracker{
		push: func(chunk []byte) {
			rest += string(chunk)
			for {
				at := strings.IndexByte(rest, '\n')
				if at < 0 {
					break
				}
				line := rest[:at]
				rest = rest[at+1:]
				take(line)
			}
		},
		usage: func() domain.RouteUsage {
			if len(usage) == 0 {
				return nil
			}
			return usage
		},
		firstToken: func() int64 { return firstTokenAt },
	}
}

// sseEventAny is the `data:` line's event object — an SSE event only when it parses to one.
func sseEventAny(line string) (map[string]any, bool) {
	if !strings.HasPrefix(line, "data:") {
		return nil, false
	}
	var event any
	if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &event) != nil {
		return nil, false
	}
	object, ok := event.(map[string]any)
	return object, ok
}

// nestedUsage is the `usage` object a value carries, when it carries one.
func nestedUsage(value any) domain.RouteUsage {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := object["usage"].(map[string]any)
	if !ok {
		return nil
	}
	return domain.RouteUsage(usage)
}

// bodyTap reads the answer once for the tracker as it passes to the client: the pipe comes first,
// so the client never waits on it.
type bodyTap struct {
	source io.ReadCloser
	push   func(chunk []byte)
}

func (t *bodyTap) Read(p []byte) (int, error) {
	n, err := t.source.Read(p)
	if n > 0 {
		t.push(append([]byte(nil), p[:n]...))
	}
	return n, err
}

func (t *bodyTap) Close() error { return t.source.Close() }

// ── the prompt rewriter ────────────────────────────────────────────────────────────────────────────────────────────

// promptRewriter is the stream a provider answer's first `message_start` is rewritten through: a
// pass-through reader that gives it a real prompt size. Z.ai (and other providers) send zeros
// there and only the real numbers in the final `message_delta`, which left Claude Code showing
// "183 tokens" for a subagent holding a 150k-token context. `input_tokens` becomes the
// conversation's estimate and the cache fields are added as 0; every other line goes out byte for
// byte. Whole lines only are looked at (a line may straddle chunks, and no UTF-8 sequence ever
// contains a newline byte), and bytes are held only until the first `message_start` has gone out —
// rewritten or not.
type promptRewriter struct {
	source    io.Reader
	prompt    PromptHint
	estimator *PromptEstimator
	pending   []byte
	out       []byte
	decided   bool
	done      bool
	flushed   bool
}

// Read hands out the rewritten stream, holding back only a partial line until the first
// `message_start` has gone out; after that every byte passes through as it came.
func (r *promptRewriter) Read(p []byte) (int, error) {
	for {
		if len(r.out) > 0 {
			n := copy(p, r.out)
			r.out = r.out[n:]
			return n, nil
		}
		if r.decided {
			return r.source.Read(p)
		}
		if r.done {
			// A stream that ended before its first `message_start` goes out as it came.
			if !r.flushed && len(r.pending) > 0 {
				r.flushed = true
				r.out = r.pending
				r.pending = nil
				continue
			}
			return 0, io.EOF
		}
		buffer := make([]byte, 32*1024)
		n, err := r.source.Read(buffer)
		if n > 0 {
			head := rewriteHead(r.pending, buffer[:n], r.prompt, r.estimator)
			r.pending = nil
			r.decided = head.decided
			if head.decided {
				r.out = append(concatChunks(head.out), head.rest...)
			} else {
				r.out = concatChunks(head.out)
				r.pending = head.rest
			}
		}
		if err == io.EOF {
			r.done = true
		} else if err != nil {
			return 0, err
		}
	}
}

// rewriteResult is one pass of the rewriter: the whole lines ready to go out, the incomplete tail
// held again, and whether the first `message_start` has gone out.
type rewriteResult struct {
	out     [][]byte
	rest    []byte
	decided bool
}

// rewriteHead is one pass of the rewriter over the bytes held so far plus a new chunk: every line
// before the first `message_start` and that line itself (rewritten when it needs it) go out; the
// rest is held again, or given up on past the limit.
func rewriteHead(held, chunk []byte, prompt PromptHint, estimator *PromptEstimator) rewriteResult {
	rest := chunk
	if len(held) > 0 {
		rest = append(append([]byte(nil), held...), chunk...)
	}
	var out [][]byte
	for {
		at := bytes.IndexByte(rest, newlineByte)
		if at < 0 {
			break
		}
		line := rest[:at+1]
		rest = rest[at+1:]
		text := string(line)
		rewritten, decided, changed := rewrittenStartLine(text, prompt, estimator)
		if !decided {
			out = append(out, line)
			continue
		}
		if !changed {
			out = append(out, line)
		} else {
			out = append(out, []byte(rewritten))
		}
		return rewriteResult{out: out, rest: rest, decided: true}
	}
	return rewriteResult{out: out, rest: rest, decided: len(rest) > promptRewriteLimitBytes}
}

// rewrittenStartLine is the replacement for one complete SSE line — the first `message_start`'s
// zero usage rewritten to this conversation's estimate, or the line unchanged when it carries a
// real prompt size or the cache fields already — decided false for any other line.
func rewrittenStartLine(line string, prompt PromptHint, estimator *PromptEstimator) (rewritten string, decided, changed bool) {
	event := sseEventNode(line)
	if event == nil || event.Get("type") == nil || event.Get("type").Text() != "message_start" {
		return "", false, false
	}
	usage := event.Get("message").Get("usage")
	// A real prompt size, or the cache fields Claude Code reads beside it, are left exactly as
	// they came.
	if usage == nil || !usage.IsObject() || !zeroInputTokens(usage) || usage.Has("cache_read_input_tokens") {
		return line, true, false
	}
	estimate := estimator.EstimatePrompt(prompt.Key, prompt.BodyChars)
	usage.Set("input_tokens", jsfmt.NumberLiteral(json.Number(strconv.FormatInt(estimate, 10))))
	usage.Set("cache_creation_input_tokens", jsfmt.NumberLiteral("0"))
	usage.Set("cache_read_input_tokens", jsfmt.NumberLiteral("0"))
	ending := "\n"
	if strings.HasSuffix(line, "\r\n") {
		ending = "\r\n"
	}
	return "data: " + event.EncodeLiteral() + ending, true, true
}

// zeroInputTokens is the lie the rewriter exists to paper over: an input_tokens of exactly 0.
func zeroInputTokens(usage *jsfmt.Value) bool {
	tokens := usage.Get("input_tokens")
	if tokens == nil || tokens.Kind() != jsfmt.KindNumber {
		return false
	}
	value, err := tokens.Literal().Float64()
	return err == nil && value == 0
}

// sseEventNode is the `data:` line's event as an ordered value, when it parses to an object.
func sseEventNode(line string) *jsfmt.Value {
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	node := parseBodyNode([]byte(strings.TrimSpace(line[len("data:"):])))
	if !node.IsObject() {
		return nil
	}
	return node
}

// promptPartsOf is the two top-level parts a conversation's key comes from, read straight off the
// body's own bytes: the `system` and the first message, each as its own JSON text so the key stays
// byte-stable. A body the splitter read as no object carries neither.
func promptPartsOf(body []byte, spans []keySpan) (system, first json.RawMessage) {
	if spans == nil {
		return nil, nil
	}
	for at := range spans {
		switch spans[at].key {
		case "system":
			system = json.RawMessage(body[spans[at].valueStart:spans[at].valueEnd])
		case "messages":
			if item, ok := firstItem(body[spans[at].valueStart:spans[at].valueEnd]); ok {
				first = json.RawMessage(item)
			}
		}
	}
	return system, first
}

// promptHintOf is the conversation a provider request belongs to, as its stream's first
// `message_start` may need it: the key its parts build and the request body's length, the measure
// the estimate grows by.
func promptHintOf(system, first json.RawMessage, bodyChars int64) *PromptHint {
	return &PromptHint{Key: PromptKey(system, first), BodyChars: bodyChars}
}

// ── the provider key ───────────────────────────────────────────────────────────────────────────────────────────────

// keyOutcome is what a key lookup ended in.
type keyOutcome int

const (
	keyValue keyOutcome = iota
	keyNone
	keyTimeout
)

// keyWithin is the provider's key within the budget: the key, none when there is none, a timeout
// when the lookup hung. The budget bounds the wait, never the shared lookup itself: it runs on
// under the cache's own deadline and the next request joins it.
func keyWithin(ctx context.Context, w *routerWorker) (string, keyOutcome) {
	timeout := keyTimeoutDefault
	if w.opts.KeyTimeoutMs != 0 {
		timeout = w.opts.KeyTimeoutMs
	}
	type lookup struct {
		key     string
		outcome keyOutcome
	}
	found := make(chan lookup, 1)
	safego.Go(w.log, func() {
		answer := w.keys.get(ctx)
		switch {
		case answer.key.Value != "":
			found <- lookup{key: answer.key.Value, outcome: keyValue}
		case answer.timedOut:
			found <- lookup{outcome: keyTimeout}
		default:
			found <- lookup{outcome: keyNone}
		}
	})
	select {
	case result := <-found:
		return result.key, result.outcome
	case <-time.After(time.Duration(timeout) * time.Millisecond):
		return "", keyTimeout
	}
}

// ── the forward ────────────────────────────────────────────────────────────────────────────────────────────────────

// forward serves one request on its original bytes: the body is parsed only on the provider
// route, where its fields are edited; every other route forwards the bytes as they came. A
// refused request — a second hop, the message-threads beta, a stopped budget, no key — parses
// nothing.
func (w *routerWorker) forward(req *http.Request, res *Response, original []byte, model string) {
	provider := w.provider
	prompt := &promptRef{}
	served := &routeModel{model: model}
	startedAt := time.Now().UnixMilli()
	timing := &answerTiming{}
	note := w.noteOf(req, served, startedAt, prompt, timing)
	route, peer, hasPeer := routeOf(w.claim, model, w.opts.Peers)
	// A request another plugin's router forwarded here is served when it is this provider's model;
	// anything else would be a second hop (a loop, or two routers disagreeing about who serves the
	// model), so it is refused.
	if hopPresent(req) && route != "provider" {
		w.log(fmt.Sprintf("%s %s model=%s → refused: second peer hop", req.Method, requestPath(req), modelOrDash(model)))
		AnthropicError(res, 508, "api_error", provider.Name+" router: refusing to forward a request twice")
		if note != nil {
			reason := "refusing to forward a request twice"
			note(508, "refused", "-", nil, &reason, nil)
		}
		return
	}
	// The request is on its way: read what its body carried, once per content. The provider route
	// scans its members once, before the capture starts, and hands the spans to the capture and its
	// own rewrite alike; every other route lets the capture scan for itself.
	var spans []keySpan
	if route == "provider" && !hasPeer {
		spans, _ = splitTopLevel(original)
	}
	w.capture(prompt, original, spans)
	if hasPeer {
		target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.FormatInt(peer.Port, 10))}
		w.dispatch(req, res, original, model, route, target, note, timing, &peer, "", nil)
		return
	}
	if route != "provider" {
		w.dispatch(req, res, original, model, route, w.opts.Anthropic, note, timing, nil, "", nil)
		return
	}
	// The message-threads beta is Anthropic-only: Claude Code sends only the newest messages and
	// expects the server to hold the rest, which the provider keeps no thread of. This exact 400
	// is the one Claude Code reads as "beta rejected": it drops the header and resends the whole
	// conversation, which the router then forwards in full.
	if tokens := threadsBeta(req); len(tokens) > 0 {
		w.log(fmt.Sprintf("%s %s model=%s → refused: message threads", req.Method, requestPath(req), modelOrDash(model)))
		AnthropicError(res, 400, "invalid_request_error", "Unexpected value(s) `"+strings.Join(tokens, "`, `")+"` for the `anthropic-beta` header: "+provider.Name+" keeps no server-side message thread, so Claude Code must resend the whole conversation.")
		if note != nil {
			reason := "message threads beta"
			note(400, "refused", "-", nil, &reason, nil)
		}
		return
	}
	// A stopped budget turns the provider's models off before anything else, the key included;
	// Claude traffic never reaches this line.
	if stop, stopped := w.budget(model); stopped {
		w.budgetStopped(req, res, model, stop, note)
		return
	}
	// Only a provider request ever reads the key: a keychain prompt or a missing key never delays
	// Claude traffic. The client is watched while it reads: one that leaves during the lookup is
	// answered by nobody, so nothing goes upstream for it.
	gone := WatchClient(req)
	key, outcome := keyWithin(req.Context(), w)
	if gone() {
		return
	}
	if outcome != keyValue {
		w.noKey(req, res, model, outcome, note)
		return
	}
	// The provider route's rewrite runs on the body's own bytes: the splitter reads the top-level
	// members, the fallback's rename and the hint's parts come off them, and a body the splitter
	// refuses is parsed instead. A model out after three failures in a row goes to the lane's next
	// model, its body naming that model from here on.
	next := w.fallback.pick(model)
	fallback := ""
	if next != model {
		asked := model
		served.model, served.fallback = next, &asked
		fallback = next
		model = next
	}
	system, first := promptPartsOf(original, spans)
	adapted, named := providerBody(w, original, spans, model, fallback, req)
	w.dispatch(req, res, adapted, named, "provider", w.opts.ProviderURL, note, timing, nil, key, promptHintOf(system, first, int64(len(adapted))))
}

// budgetStopped answers a provider request while its budget is stopped: the same non-retryable
// 400 as a refused key, naming the dashboard.
func (w *routerWorker) budgetStopped(req *http.Request, res *Response, model string, stop domain.BudgetStop, note eventNote) {
	provider := w.provider
	w.log(fmt.Sprintf("%s %s model=%s → provider: budget stopped (%s)", req.Method, requestPath(req), model, stop.Scope))
	answer := BudgetAnswer(domain.BudgetMessage(provider.Display, stop))
	writeHead(res, answer.Status, answer.Headers)
	res.End([]byte(answer.Body))
	w.health(domain.HealthRefusal, "budget: "+stop.Scope, model)
	if note != nil {
		note(int64(answer.Status), "provider", w.opts.ProviderURL.Host, nil, nil, nil)
	}
}

// noKey answers a provider request without a key: a non-retryable 400 that names setup when there
// is none (never a 401, which Claude Code would read as its own login failing), 503 when the
// lookup timed out.
func (w *routerWorker) noKey(req *http.Request, res *Response, model string, outcome keyOutcome, note eventNote) {
	provider := w.provider
	timedOut := outcome == keyTimeout
	why := "no key"
	if timedOut {
		why = "key lookup timed out"
	}
	w.log(fmt.Sprintf("%s %s model=%s → provider: %s", req.Method, requestPath(req), model, why))
	if timedOut {
		AnthropicError(res, 503, "api_error", fmt.Sprintf("%s router: reading the %s key timed out", provider.Name, provider.Display))
		if note != nil {
			note(503, "provider", w.opts.ProviderURL.Host, nil, nil, nil)
		}
		return
	}
	answer := RefusalAnswer(RefusalNames{Display: provider.Display, Slash: provider.Slash, BillingURL: provider.BillingURL}, RefusalKey, "no key is set")
	writeHead(res, answer.Status, answer.Headers)
	res.End([]byte(answer.Body))
	w.health(domain.HealthRefusal, "key: no key is set", model)
	if note != nil {
		note(int64(answer.Status), "provider", w.opts.ProviderURL.Host, nil, nil, nil)
	}
}

// ── the error reason ───────────────────────────────────────────────────────────────────────────────────────────────

// secretLike is a run of key- and token-shaped characters long enough to be a credential: gone
// before a reason is stored.
var secretLike = regexp.MustCompile(`[A-Za-z0-9._-]{24,}`)

// whitespaceRun is the runs of whitespace a reason collapses into one space of.
var whitespaceRun = regexp.MustCompile(`\s+`)

// UpstreamErrorReason is the one-line reason an upstream error answer gives, or false when it
// gives none: its `type: message` (or the `code`-shaped errors Z.ai answers with), else the first
// 300 characters of its text — whitespace collapsed, the route's own key and every
// credential-shaped run redacted, so no secret ever reaches a log or the spool, and no request
// content ever appears in an error answer to begin with.
func UpstreamErrorReason(body []byte, key string) (string, bool) {
	if len(body) > reasonBytes {
		body = body[:reasonBytes]
	}
	text := string(body)
	said, ok := errorMessage(text)
	if !ok {
		said = text
	}
	if key != "" {
		said = strings.ReplaceAll(said, key, "…")
	}
	line := strings.TrimSpace(whitespaceRun.ReplaceAllString(said, " "))
	line = secretLike.ReplaceAllString(line, "…")
	line = strings.TrimSpace(line)
	line = utf16Slice(line, reasonChars)
	line = strings.TrimRightFunc(line, unicode.IsSpace)
	if line == "" {
		return "", false
	}
	return line, true
}

// utf16Slice keeps the first units UTF-16 code units of text: the measure a JS String.slice takes.
func utf16Slice(text string, units int) string {
	if utf16Len(text) <= int64(units) {
		return text
	}
	var b strings.Builder
	count := 0
	for _, r := range text {
		width := max(utf16.RuneLen(r), 1)
		if count+width > units {
			break
		}
		b.WriteRune(r)
		count += width
	}
	return b.String()
}

// errorMessage is the message an upstream error body carries, in the shapes providers actually
// send: Anthropic's `{error:{type,message}}` and the `code`-shaped errors Z.ai answers with
// (`{error:{code,message}}`, `{code,msg}`); false for anything else, leaving the raw text as the
// reason.
func errorMessage(text string) (string, bool) {
	var parsed any
	if json.Unmarshal([]byte(text), &parsed) != nil {
		return "", false
	}
	body, ok := parsed.(map[string]any)
	if !ok {
		return "", false
	}
	if inner, ok := body["error"].(map[string]any); ok {
		return saidWhat(errorField(inner, "type"), errorField(inner, "code"), errorField(inner, "message"))
	}
	message := errorField(body, "msg")
	if message == "" {
		message = errorField(body, "message")
	}
	return saidWhat(errorField(body, "code"), "", message)
}

// errorField is a field of a parsed error body that is a non-empty string.
func errorField(record map[string]any, name string) string {
	if value, ok := record[name].(string); ok && value != "" {
		return value
	}
	return ""
}

// saidWhat is an error's `what: message`, its message alone without a `what`, false without both.
func saidWhat(what, alt, message string) (string, bool) {
	if what == "" {
		what = alt
	}
	if message == "" {
		return what, what != ""
	}
	if what == "" {
		return message, true
	}
	return what + ": " + message, true
}

// ── the provider route's error answers ─────────────────────────────────────────────────────────────────────────────

// providerError is what a provider's error answer becomes: a refused key or an empty balance the
// non-retryable refusal, an error that quotes the key itself the same answer with the key blanked
// out; false to send it as it came. A refusal and a rate limit are reported as health events (the
// status only, never the body).
func (w *routerWorker) providerError(answer *http.Response, body []byte, key, model string) (Replacement, string, bool) {
	provider := w.provider
	status := answer.StatusCode
	refusal, refused := RefusalOf(status, body)
	if !refused && status == 429 {
		w.health(domain.HealthRateLimited, "HTTP 429", model)
	}
	if refused {
		w.health(domain.HealthRefusal, fmt.Sprintf("%s: HTTP %d", refusal, status), model)
		why := "out of balance"
		if refusal == RefusalKey {
			why = "key refused"
			// The key may have been rotated: the kept answer is dropped so the next request
			// reads it again. Only the provider's answers reach here — Anthropic's never do.
			w.keys.clear()
		}
		names := RefusalNames{Display: provider.Display, Slash: provider.Slash, BillingURL: provider.BillingURL}
		return RefusalAnswer(names, refusal, fmt.Sprintf("%s answered HTTP %d", provider.Display, status)), why, true
	}
	text := string(body)
	if key == "" || !strings.Contains(text, key) {
		return Replacement{}, "", false
	}
	// The replacement goes out plain, so it does not claim the encoding the provider's bytes came
	// with.
	headers := ResponseHeaders(answer.Header)
	headers.Del("Content-Length")
	headers.Del("Content-Encoding")
	return Replacement{
		Status:  status,
		Headers: headers,
		Body:    strings.ReplaceAll(text, key, "[key redacted]"),
	}, "key redacted", true
}

// inspection is what one request's inspection decided: the status the client was answered instead
// of the upstream's, when the router rewrote its answer, and why the upstream failed (once its
// held-back body has been read).
type inspection struct {
	rewritten *int
	reason    *string
}

// providerInspection is the hooks that read a provider route's error answers (the only route
// carrying a key): each held-back answer is read once — refused or not — and its one log line
// carries the reason.
func (w *routerWorker) providerInspection(req *http.Request, u *url.URL, route, model, key string, decided *inspection) (func(*http.Response) func([]byte) (Replacement, bool), func(*http.Response)) {
	line := func(status int, why string) string {
		return fmt.Sprintf("%s %s model=%s → %s %d%s", req.Method, u.Path, modelOrDash(model), route, status, why)
	}
	inspect := func(answer *http.Response) func([]byte) (Replacement, bool) {
		if !MayRefuse(answer.StatusCode) {
			return nil
		}
		return func(text []byte) (Replacement, bool) {
			// A compressed answer is judged through its decompressed copy; its own bytes are what
			// the client gets.
			body := InspectBody(text, answer.Header.Get("Content-Encoding"))
			replacement, why, rewritten := w.providerError(answer, body, key, model)
			// A rewritten answer's why is already on its health event, and its text never leaves
			// the router (a refusal quotes nothing upstream, one quoting the key is blanked out);
			// only an error that went through as it came records its reason — the route's key
			// redacted out regardless.
			if !rewritten {
				if reason, ok := UpstreamErrorReason(body, key); ok {
					decided.reason = &reason
				} else {
					decided.reason = nil
				}
			} else {
				decided.reason = nil
			}
			why0 := ""
			if decided.reason != nil {
				why0 = " (" + *decided.reason + ")"
			}
			w.log(line(answer.StatusCode, why0))
			if rewritten {
				answered := replacement.Status
				decided.rewritten = &answered
				w.log(line(answer.StatusCode, fmt.Sprintf(" answered %d (%s)", replacement.Status, why)))
			}
			return replacement, rewritten
		}
	}
	// An answer too big for its judge went out as it came, its reason unread: the line is logged
	// plain.
	oversize := func(answer *http.Response) {
		w.log(line(answer.StatusCode, ""))
	}
	return inspect, oversize
}

// failureStatus is the status one failed request records and answers: 499 when its client left,
// 504 when the upstream stayed silent before the head, 502 for every other failure.
func failureStatus(failure UpstreamFailure) int64 {
	switch {
	case failure.ClientClosed:
		return 499
	case failure.Code == "ETIMEDOUT" && !failure.AfterHeaders:
		return 504
	default:
		return 502
	}
}

// dispatch sends one request on its route and pipes its answer back, streaming it.
func (w *routerWorker) dispatch(req *http.Request, res *Response, body []byte, model, route string, target *url.URL, note eventNote, timing *answerTiming, peer *PeerTarget, key string, prompt *PromptHint) {
	provider := w.provider
	u, err := UpstreamURL(target, requestPath(req))
	if err != nil {
		AnthropicError(res, 502, "api_error", provider.Name+" router: bad request path")
		if note != nil {
			note(502, route, target.Host, nil, nil, nil)
		}
		return
	}
	if why, refused := forwardable(route, u); refused {
		w.log(fmt.Sprintf("%s %s model=%s → %s refused", req.Method, u.Path, modelOrDash(model), route))
		AnthropicError(res, 502, "api_error", provider.Name+" router: "+why)
		if note != nil {
			note(502, route, u.Host, nil, nil, nil)
		}
		return
	}
	headers := UpstreamHeaders(req.Header, target, len(body), key)
	if peer != nil {
		headers.Set(PeerHop, "1")
	}
	// The answer's allow-listed headers, read once as its head arrives, for the route event's
	// note; usage is parsed out of the passing stream, so the client never waits on it.
	var tracker *UsageTracker
	var answerHeaders *map[string]string
	// What this answer's inspection decided (a provider route only): the client's status when the
	// answer was rewritten, and why the upstream failed.
	decided := inspection{}
	if w.opts.OnUpstream != nil {
		w.opts.OnUpstream(req)
	}
	hooks := UpstreamHooks{
		Answered: func(answer *http.Response) {
			timing.headAt = time.Now().UnixMilli()
			// A provider error answer is held back for its judge, so its line waits and carries
			// the reason; every other answer is logged the moment its headers arrive.
			if !(key != "" && MayRefuse(answer.StatusCode)) {
				w.log(fmt.Sprintf("%s %s model=%s → %s %d", req.Method, u.Path, modelOrDash(model), route, answer.StatusCode))
			}
			if note != nil {
				tracker = NewUsageTracker(answer.Header.Get("Content-Type"), answer.Header.Get("Content-Encoding"))
				answer.Body = &bodyTap{source: answer.Body, push: tracker.Push}
				answerHeaders = CapturedHeaders(answer.Header)
			}
		},
		Rewrite: func(answer *http.Response) io.Reader {
			if prompt == nil {
				return nil
			}
			if !strings.Contains(answer.Header.Get("Content-Type"), "text/event-stream") {
				return nil
			}
			return &promptRewriter{source: answer.Body, prompt: *prompt, estimator: w.estimator}
		},
		Finished: func(answer *http.Response) {
			// The final numbers are the real ones: remembered here, so the conversation's next
			// turn can estimate the prompt size its provider will not report until the end.
			if prompt != nil {
				w.estimator.RecordPrompt(prompt.Key, tracker.Usage(), prompt.BodyChars)
			}
			if route == "anthropic" && w.opts.AnthropicOk != nil {
				w.opts.AnthropicOk()
			}
			if route == "provider" {
				w.fellBack(w.fallback.answered(model, answer.StatusCode))
			}
			if note != nil {
				status := int64(answer.StatusCode)
				if status == 0 {
					status = 502
				}
				if decided.rewritten != nil {
					status = int64(*decided.rewritten)
				}
				timing.firstTokenAt = tracker.FirstTokenAt()
				note(status, route, u.Host, tracker.Usage(), decided.reason, answerHeaders)
			}
		},
		Failed: func(failure UpstreamFailure) {
			tracker.Close()
			status := failureStatus(failure)
			if failure.ClientClosed {
				// A client that left ends its own call: that says nothing about the upstream or the
				// model, and nobody reads an answer.
				w.log(fmt.Sprintf("%s %s model=%s → %s cancelled", req.Method, u.Path, modelOrDash(model), route))
				if note != nil {
					reason := "client closed"
					note(status, route, u.Host, nil, &reason, nil)
				}
				return
			}
			w.log(fmt.Sprintf("%s %s model=%s → %s error %s", req.Method, u.Path, modelOrDash(model), route, failure.Code))
			if route == "anthropic" && w.opts.AnthropicFail != nil {
				w.opts.AnthropicFail(failure.Code, model)
			}
			if route == "provider" {
				w.fellBack(w.fallback.dropped(model, failure.Code))
			}
			if !failure.AfterHeaders {
				message := fmt.Sprintf("%s router: %s unreachable (%s)", provider.Name, route, failure.Code)
				if peer != nil {
					message = fmt.Sprintf("%s router: the %s plugin's router is down; run /%s:setup to restart it",
						provider.Name, peer.Name, peer.Name)
				}
				AnthropicError(res, int(status), "api_error", message)
			}
			if note != nil {
				// the errno is the whole reason a dropped connection has; never a header or a
				// body byte
				reason := "connection failed (" + failure.Code + ")"
				note(status, route, u.Host, nil, &reason, nil)
			}
		},
	}
	// Only a provider route (the one carrying the key) has its error answers read; Anthropic's and
	// a peer's always go through as they came.
	if key != "" {
		hooks.Inspect, hooks.Oversize = w.providerInspection(req, u, route, model, key, &decided)
	}
	SendUpstream(req.Context(), UpstreamCall{
		URL:     u,
		Method:  req.Method,
		Headers: headers,
		Body:    body,
		Env:     w.opts.Env,
		Direct:  peer != nil,
		IdleMs:  w.opts.IdleMs,
	}, res, hooks)
}
