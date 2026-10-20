// The satellite link (SPEC 3, 6.7): one outbound connection from a satellite machine to its
// hub. It forwards this machine's reports and its open permission items in batches, long-polls
// for the decisions and messages the hub owes it, and applies what arrives on this machine —
// under this machine's own switches, which the hub cannot override (SPEC 6.6). While the hub is
// unreachable the batches land in the home's outbox.jsonl and replay in order, so nothing a
// machine said while offline is lost. The link never serves anything and never listens: when it
// stops, the machine's own sessions go on exactly as a solo machine's do.

package satellite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/netproxy"
)

// The link's own pace: a flush batches this machine's news, a poll holds the handout wait. The
// hub caps the wait at 30 s; the link asks for a little less so a poll never outlives its
// client's patience. One flow command waits 5 s for the hub's answer; a hub board list is read
// within 2 s and stands for half a minute.
const (
	defaultFlushEvery = time.Second
	defaultPollWait   = 25 * time.Second

	flowTimeout      = 5 * time.Second
	hubBoardsTimeout = 2 * time.Second
	hubBoardsEvery   = 30 * time.Second

	forwardBatch = 200
	outboxLimit  = 5000
)

// Options shapes one link.
type Options struct {
	// Reg is this machine's own registry, where its hooks' reports and items land.
	Reg *hub.DB
	// Home is the machine's huddle home: the outbox lives in it, its switches rule here.
	Home home.Home
	// HubURL and Credential are the hub's address and this machine's credential there.
	HubURL     string
	Credential string
	// Client calls the hub; nil takes one with a timeout past the longest poll.
	Client *http.Client
	// Now is the clock; nil takes the wall clock.
	Now func() time.Time
	// Log takes one line at a time; nil throws them away.
	Log func(string)
	// Wake is called with an item's id once a decision landed on it, so the machine's own
	// permission polls answer at once instead of on their next re-poll.
	Wake func(itemID int64)
	// FlushEvery and PollWait override the pace; tests shorten both.
	FlushEvery time.Duration
	PollWait   time.Duration
}

// Link is one machine's outbound connection to its hub.
type Link struct {
	reg        *hub.DB
	home       home.Home
	hubURL     string
	credential string
	client     *http.Client
	// readClient carries the board reads a relayed page asks for (KBN-SAT-READS):
	// no overall timeout, so the hub's event stream lasts as long as the hub
	// holds it, and a dial bound so a hub that never accepts costs one connect.
	readClient *http.Client
	now        func() time.Time
	log        func(string)
	wake       func(int64)
	flushEvery time.Duration
	pollWait   time.Duration

	mu     sync.Mutex
	cursor int64
	// hubBoards is the hub's board keys as of hubBoardsAt, the copy a relayed
	// flow command decides against (KBN-SHARED): nil before the first read.
	hubBoards   map[string]bool
	hubBoardsAt time.Time
}

// New builds one link over the given options.
func New(o Options) *Link {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	flushEvery := o.FlushEvery
	if flushEvery <= 0 {
		flushEvery = defaultFlushEvery
	}
	pollWait := o.PollWait
	if pollWait <= 0 {
		pollWait = defaultPollWait
	}
	return &Link{
		reg: o.Reg, home: o.Home, hubURL: o.HubURL, credential: o.Credential,
		client: client, now: now, log: log, wake: o.Wake,
		readClient: readClientOf(o.Client),
		flushEvery: flushEvery, pollWait: pollWait,
	}
}

// readClientOf answers the client the board reads go through: no overall timeout, so a live
// stream lasts, but a hub that never connects, never finishes its handshake or never sends its
// headers is given up on after 5 s. A link built with its own client keeps that client's
// transport, and with it whatever trust the client was given.
func readClientOf(given *http.Client) *http.Client {
	if given != nil {
		reads := *given
		reads.Timeout = 0
		return &reads
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 netproxy.FromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}}
}

// Run links the machine to its hub until the context ends: one goroutine forwards what this
// machine said, one holds the long poll for what the hub owes it. A dead hub costs neither its
// loop — the flush writes the outbox, the poll retries — and the machine's own server never
// waits on either.
func (l *Link) Run(ctx context.Context) {
	flushCtx, stopFlush := context.WithCancel(ctx)
	defer stopFlush()
	go l.flushLoop(flushCtx)
	l.pollLoop(ctx)
	stopFlush()
}

// flushLoop walks the machine's news to the hub, one batch at a time.
func (l *Link) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(l.flushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.flush(ctx); err != nil {
				l.log("satellite link: " + err.Error())
			}
		}
	}
}

// pollLoop holds the long poll for this machine's handouts and applies what arrives.
func (l *Link) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := l.poll(ctx); err != nil {
			l.log("satellite link: " + err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(l.flushEvery):
			}
		}
	}
}

// flush replays what the outbox holds, then forwards the machine's news since the cursor. The
// cursor moves only once a batch is safely somewhere — the hub or the outbox — so nothing is
// sent twice and nothing is dropped.
func (l *Link) flush(ctx context.Context) error {
	if err := l.replay(ctx); err != nil {
		return err
	}
	events, err := l.reg.EventsAfter(ctx, l.cursorOf(), forwardBatch)
	if err != nil {
		return fmt.Errorf("read the events to forward: %w", err)
	}
	items, err := l.reg.ForwardList(ctx)
	if err != nil {
		return fmt.Errorf("list the items to forward: %w", err)
	}
	if len(events) == 0 && len(items) == 0 {
		return nil
	}
	batch := reportBatch{Items: items}
	for _, event := range events {
		batch.Reports = append(batch.Reports, event.Report)
	}
	// the cursor moves with the hand-off: whatever the post's fate, these events are someone
	// else's to deliver now (a batch may be items alone)
	if len(events) > 0 {
		l.setCursor(events[len(events)-1].Seq)
	}
	if err := l.post(ctx, "/api/sat/report", batch); err != nil {
		if keep := l.keepForLater(batch); keep != nil {
			return fmt.Errorf("neither the hub nor the outbox took the batch: %v / %v", err, keep)
		}
		if permanent(err) {
			return fmt.Errorf("the hub refused the batch: %v", err)
		}
		return fmt.Errorf("the hub is unreachable: %v", err)
	}
	return nil
}

// replay sends the outbox in order and drops every line the hub took. The first unreachable hub
// stops the replay where it stands, so the order a machine spoke in is the order it arrives in.
func (l *Link) replay(ctx context.Context) error {
	for {
		lines, err := l.outboxLines()
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return nil
		}
		if err := l.post(ctx, "/api/sat/report", json.RawMessage(lines[0].Body)); err != nil {
			if permanent(err) {
				l.dropOutbox(1) // a batch the hub refuses for good is never going to arrive
				l.log("satellite link: a queued batch was refused for good: " + err.Error())
				continue
			}
			return fmt.Errorf("the hub is unreachable: %w", err)
		}
		l.dropOutbox(1)
	}
}

// poll takes one long poll and applies what the hub handed out.
func (l *Link) poll(ctx context.Context) error {
	seconds := int64(l.pollWait / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	var answer struct {
		Handouts []hub.SatHandout `json:"handouts"`
	}
	if err := l.get(ctx, fmt.Sprintf("/api/sat/next?wait=%d", seconds), &answer); err != nil {
		return err
	}
	for _, handout := range answer.Handouts {
		l.settle(ctx, handout)
	}
	return nil
}

// settle applies one handout on this machine and answers the hub with what became of it. The
// machine's switches rule here (SPEC 6.6): an allow the machine's own floor or list refuses is
// recorded as a deny on this machine — the terminal prompt must end — and the hub hears the
// refusal with its reason.
func (l *Link) settle(ctx context.Context, handout hub.SatHandout) {
	result, reason := l.apply(ctx, handout)
	body := map[string]string{"result": result}
	if reason != "" {
		body["reason"] = reason
	}
	if err := l.post(ctx, fmt.Sprintf("/api/sat/result/%d", handout.ID), body); err != nil {
		// the hub re-offers an unanswered handout after SatReofferAfter, and applying a
		// decision twice ends the same way once, so a lost result is a delay, never a loss
		l.log("satellite link: the result for handout " + fmt.Sprint(handout.ID) + " is not told: " + err.Error())
	}
}

// apply runs one handout on this machine and answers the result it earned.
func (l *Link) apply(ctx context.Context, handout hub.SatHandout) (result, reason string) {
	at := jsfmt.ISOTime(l.now())
	switch handout.Kind {
	case hub.SatDecision:
		var payload hub.SatRefusalBody
		if err := hub.DecodeSatPayload(handout.Payload, &payload); err != nil {
			return hub.SatRefused, "the handout cannot be read on this machine"
		}
		return l.applyDecision(ctx, payload, at)
	case hub.SatMessage:
		var payload hub.SatMessageBody
		if err := hub.DecodeSatPayload(handout.Payload, &payload); err != nil {
			return hub.SatRefused, "the handout cannot be read on this machine"
		}
		return l.applyMessage(ctx, payload, at)
	default:
		return hub.SatRefused, "the handout is of no kind this machine knows"
	}
}

// applyDecision lands the owner's decision on the machine's own item.
func (l *Link) applyDecision(ctx context.Context, payload hub.SatRefusalBody, at string) (string, string) {
	item, err := l.reg.PermissionItemByKey(ctx, payload.Key, l.now())
	if err != nil {
		return hub.SatBusy, "this machine's registry could not be read"
	}
	if item == nil {
		return hub.SatRefused, "the item is gone at the machine"
	}
	if item.State != hub.StateOpen {
		if item.Decision == payload.Decision {
			return hub.SatDelivered, "" // a re-offered handout: the decision already landed
		}
		return hub.SatRefused, "answered at the machine first"
	}
	if payload.Decision == "allow" {
		cfg, err := home.LoadConfig(l.home)
		if err != nil {
			return l.refuseDecision(ctx, item, "this machine's switches cannot be read; allow it in the terminal", at)
		}
		if !cfg.Remote.Permissions {
			return l.refuseDecision(ctx, item, "remote permissions are off on this machine", at)
		}
		if why, ok := hub.RemoteAllow(cfg.Remote.Allow, item.Tool, item.Input); !ok {
			return l.refuseDecision(ctx, item, why, at)
		}
	}
	won, err := l.reg.DecidePermission(ctx, item.ID, payload.Decision, payload.Message, at, l.now())
	if err != nil {
		return hub.SatBusy, "this machine's registry could not be written"
	}
	if !won {
		return hub.SatRefused, "answered at the machine first"
	}
	l.wakeItem(item.ID)
	return hub.SatDelivered, ""
}

// refuseDecision records the machine's deny on its own item — the asking hook's poll must end
// whatever the owner asked for — and answers the refusal the hub reads.
func (l *Link) refuseDecision(ctx context.Context, item *hub.PermissionItem, why, at string) (string, string) {
	_, _ = l.reg.DecidePermission(ctx, item.ID, "deny", why, at, l.now())
	l.wakeItem(item.ID)
	return hub.SatRefused, why
}

// applyMessage queues the owner's words for the machine's own session.
func (l *Link) applyMessage(ctx context.Context, payload hub.SatMessageBody, at string) (string, string) {
	cfg, err := home.LoadConfig(l.home)
	if err != nil || !cfg.Remote.Messages {
		// a torn config reads as its switches off, the way an unreadable allow list refuses
		return hub.SatRefused, "remote messages are off on this machine"
	}
	if _, err := l.reg.QueueMessage(ctx, hub.MessageIn{
		SessionID: payload.SessionID, FromKind: hub.FromOwner, Text: payload.Text,
	}, at); err != nil {
		return hub.SatBusy, "this machine's registry could not be written"
	}
	return hub.SatDelivered, ""
}

// wakeItem calls the server's wake, when one is wired, so the machine's own polls answer at once.
func (l *Link) wakeItem(itemID int64) {
	if l.wake != nil {
		l.wake(itemID)
	}
}

// cursorOf answers the forward cursor under its lock.
func (l *Link) cursorOf() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cursor
}

// setCursor moves the forward cursor under its lock.
func (l *Link) setCursor(seq int64) {
	l.mu.Lock()
	l.cursor = seq
	l.mu.Unlock()
}

// StartCursor puts the forward cursor at the machine's newest stored event, where a fresh link
// begins: a restart never replays a history the hub may already hold.
func (l *Link) StartCursor(ctx context.Context) {
	seq, err := l.reg.NewestSeq(ctx)
	if err != nil {
		return
	}
	l.setCursor(seq)
}

// Flow carries one flow command for a hub-hosted board to the hub under this machine's
// credential (KBN-SHARED) and answers the hub's verdict whole — its status and body, so a
// claim this machine asks for is the hub's answer alone. A transport error is the link's own,
// never a verdict; 5 s bound the wait.
func (l *Link) Flow(ctx context.Context, key string, body []byte) (status int, answer []byte, err error) {
	callCtx, cancel := context.WithTimeout(ctx, flowTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		l.hubURL+kanbantab.Prefix+"/b/"+key+"/flow", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	l.authorize(request)
	response, err := l.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, raw, nil
}

// Read carries one read of a hub-hosted board to the hub under this machine's credential
// (KBN-SAT-READS) and answers the hub's response whole — status, headers and streaming body —
// for the caller to write through. The request runs on the caller's context, so the body ends
// when that context does, and the caller closes it; the read client carries no overall timeout,
// so the hub's event stream can last.
func (l *Link) Read(ctx context.Context, key, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		l.hubURL+kanbantab.Prefix+"/b/"+key+"/api/"+path, nil)
	if err != nil {
		return nil, err
	}
	l.authorize(request)
	return l.readClient.Do(request)
}

// HubBoards answers the set of board keys the hub serves, held for hubBoardsEvery so a relayed
// command costs the hub one read per hold, not one per command. A hub that cannot be read
// answers the last good set with the error: a board the hub hosted still relays to it, so its
// claims stay one claim, and a set never read answers nil.
func (l *Link) HubBoards(ctx context.Context) (map[string]bool, error) {
	l.mu.Lock()
	set, at := l.hubBoards, l.hubBoardsAt
	l.mu.Unlock()
	if set != nil && l.now().Sub(at) < hubBoardsEvery {
		return set, nil
	}
	var answer struct {
		Boards []struct {
			Key string `json:"key"`
		} `json:"boards"`
	}
	// bounded on its own: the link's client waits out a long poll, and a relayed command
	// must not wait that long on a hub that accepts and never answers
	boardsCtx, cancel := context.WithTimeout(ctx, hubBoardsTimeout)
	defer cancel()
	if err := l.get(boardsCtx, kanbantab.Prefix+"/boards", &answer); err != nil {
		return set, err
	}
	fresh := make(map[string]bool, len(answer.Boards))
	for _, row := range answer.Boards {
		if row.Key != "" {
			fresh[row.Key] = true
		}
	}
	l.mu.Lock()
	l.hubBoards, l.hubBoardsAt = fresh, l.now()
	l.mu.Unlock()
	return fresh, nil
}

// reportBatch is one body the link posts to /api/sat/report: the machine's events since its
// cursor and its whole open permission item set, deduped at the hub under the items' keys.
type reportBatch struct {
	Reports []hub.Report        `json:"reports"`
	Items   []hub.ForwardedItem `json:"items"`
}

// outboxLine is one queued batch: the exact body the replay posts.
type outboxLine struct {
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// keepForLater writes one batch to the outbox, dropping its oldest line at the limit — a
// machine cut off for good must not eat its disk.
func (l *Link) keepForLater(batch reportBatch) error {
	raw, err := json.Marshal(batch)
	if err != nil {
		return nil // a batch that cannot be encoded is dropped, never a loop
	}
	lines, err := l.outboxLines()
	if err != nil {
		return err
	}
	if len(lines) >= outboxLimit {
		l.log("satellite link: the outbox is full: the oldest batch is dropped")
		lines = lines[1:]
	}
	lines = append(lines, outboxLine{Kind: "report", Body: string(raw)})
	return l.writeOutbox(lines)
}

// outboxLines answers the outbox's lines in order; a torn line is dropped, the replay goes on.
func (l *Link) outboxLines() ([]outboxLine, error) {
	raw, err := os.ReadFile(l.home.Outbox())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []outboxLine{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var one outboxLine
		if err := json.Unmarshal([]byte(line), &one); err != nil || one.Body == "" {
			continue // a torn line is lost, the replay goes on
		}
		out = append(out, one)
	}
	return out, nil
}

// dropOutbox removes the oldest n lines and rewrites the file; an empty replay ends in no file.
func (l *Link) dropOutbox(n int) {
	lines, err := l.outboxLines()
	if err != nil {
		return
	}
	if n >= len(lines) {
		_ = os.Remove(l.home.Outbox())
		return
	}
	_ = l.writeOutbox(lines[n:])
}

// writeOutbox rewrites the outbox whole, one line per batch.
func (l *Link) writeOutbox(lines []outboxLine) error {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			continue
		}
	}
	body := out.Bytes()
	if len(body) == 0 {
		_ = os.Remove(l.home.Outbox())
		return nil
	}
	return writeFile0600(l.home.Outbox(), body)
}

// post sends one JSON body to the hub with this machine's credential.
func (l *Link) post(ctx context.Context, path string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.hubURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	l.authorize(request)
	response, err := l.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close() }()
	if response.StatusCode >= 500 {
		return temporaryError(response.Status)
	}
	if response.StatusCode >= 400 {
		return refusedError(response.Status)
	}
	return nil
}

// get reads one JSON answer from the hub with this machine's credential.
func (l *Link) get(ctx context.Context, path string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, l.hubURL+path, nil)
	if err != nil {
		return err
	}
	l.authorize(request)
	response, err := l.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode >= 500 {
		return temporaryError(response.Status)
	}
	if response.StatusCode >= 400 {
		return refusedError(response.Status)
	}
	return json.Unmarshal(raw, into)
}

// authorize names this machine's credential on the request, and the action a write performs:
// the hub's guards are the hub's guards, for its satellites too.
func (l *Link) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+l.credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-huddle-action", request.URL.Path)
}

// linkError is one hub answer the link understands: whether trying again can ever help.
type linkError struct {
	msg       string
	permanent bool
}

func (e linkError) Error() string { return e.msg }

func temporaryError(status string) error { return linkError{msg: "the hub answered " + status} }
func refusedError(status string) error {
	return linkError{msg: "the hub answered " + status, permanent: true}
}

// permanent answers whether the error is a refusal trying again changes nothing.
func permanent(err error) bool {
	le, ok := err.(linkError)
	return ok && le.permanent
}

// writeFile0600 writes one file atomically with private modes, the home's own way.
func writeFile0600(path string, body []byte) error {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dirOf(path string) string {
	at := strings.LastIndexByte(path, '/')
	if at < 0 {
		return "."
	}
	return path[:at]
}
