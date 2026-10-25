package server

// The satellite routes (SPEC 6.7): a satellite machine reports its sessions' events and its
// permission items in batches, long-polls for the decisions and messages the hub owes it, and
// answers each handout delivered, refused or busy. Every route but the join names a satellite
// credential in its Authorization header — the machine is who its credential says, whatever a
// body claims — and the join spends the one-use invite `huddle pair --satellite` minted. The
// routes answer over the loopback listener too, so an in-process satellite talks to its hub the
// same way a distant one talks over the hub listener.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/satellite"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// satJoinPath is the one public satellite path: the exchange turns a one-use invite into the
// machine's credential, so it must answer before any credential exists.
const satJoinPath = "/api/sat/join"

// satPathPrefix is every satellite route's prefix; each but the join verifies the machine's own
// credential.
const satPathPrefix = "/api/sat/"

// satelliteRoute is the delivery route a message to a satellite session takes: the hub hands it
// to the machine's link, and the machine queues it for its own session.
const satelliteRoute = "satellite"

// satResultPath matches the result route: one queued handout's number.
var satResultPath = regexp.MustCompile(`^/api/sat/result/([1-9][0-9]*)$`)

// satBatchLimits cap one report batch: a satellite that loses its place never drowns the hub.
const (
	satMaxReports = 500
	satMaxItems   = 500
)

// satWaiters wakes the long polls holding one machine's handouts: every waiter gets its own
// channel, closed when a handout is queued for it, so each re-offers and answers.
type satWaiters struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newSatWaiters() *satWaiters {
	return &satWaiters{waiters: map[string]map[chan struct{}]struct{}{}}
}

// arm registers one waiter for one machine and answers its channel.
func (n *satWaiters) arm(machine string) chan struct{} {
	wake := make(chan struct{})
	n.mu.Lock()
	defer n.mu.Unlock()
	set, ok := n.waiters[machine]
	if !ok {
		set = map[chan struct{}]struct{}{}
		n.waiters[machine] = set
	}
	set[wake] = struct{}{}
	return wake
}

// done drops one waiter that finished without a wake.
func (n *satWaiters) done(machine string, wake chan struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if set, ok := n.waiters[machine]; ok {
		delete(set, wake)
		if len(set) == 0 {
			delete(n.waiters, machine)
		}
	}
}

// wake closes and drops every waiter of one machine.
func (n *satWaiters) wake(machine string) {
	n.mu.Lock()
	set := n.waiters[machine]
	delete(n.waiters, machine)
	n.mu.Unlock()
	for wake := range set {
		close(wake)
	}
}

// satRoute answers everything under /api/sat/: the join opens to an invite, everything else
// demands the machine's credential and carries the verified machine name in its context.
func (s *server) satRoute(w http.ResponseWriter, r *http.Request, path string) {
	if path == satJoinPath {
		if r.Method != http.MethodPost {
			s.writeJSON(w, 405, errorAnswer{Error: "this endpoint answers POST"}, nil)
			return
		}
		s.satJoinRoute(w, r)
		return
	}
	if s.registry == nil {
		s.writeJSON(w, 500, errorAnswer{Error: "the registry's database is unavailable"}, nil)
		return
	}
	machine, ok := satelliteFrom(r.Context())
	if !ok {
		machine, ok = s.satelliteBearer(r)
	}
	if !ok {
		s.writeJSON(w, 401, errorAnswer{Error: "a satellite route needs its credential: join with `huddle join-hub`"}, nil)
		return
	}
	ctx := withSatellite(r.Context(), machine)
	switch {
	case path == satPathPrefix+"report" && r.Method == http.MethodPost:
		s.satReportRoute(w, r.WithContext(ctx), machine)
	case path == satPathPrefix+"next" && r.Method == http.MethodGet:
		s.satNextRoute(w, r.WithContext(ctx), machine)
	case satResultPath.MatchString(path) && r.Method == http.MethodPost:
		s.satResultRoute(w, r.WithContext(ctx), path, machine)
	default:
		s.writeJSON(w, 404, errorAnswer{Error: "no such satellite route"}, nil)
	}
}

// satJoinRoute spends one invite (SPEC 6.7: one use, 10 minutes) and mints the machine's
// credential. The invite is consumed before anything is stored, so a replayed join buys
// nothing; the mint writes the audit row and the alert a satellite added owes.
func (s *server) satJoinRoute(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		s.writeJSON(w, 500, errorAnswer{Error: "the registry's database is unavailable"}, nil)
		return
	}
	body, err := s.body(r)
	if err != nil {
		s.writeJSON(w, 400, errorAnswer{Error: err.Error()}, nil)
		return
	}
	invite := textOf(fieldOf(body, "invite"))
	if !auth.SatelliteRe.MatchString(invite) {
		s.writeJSON(w, 400, errorAnswer{Error: "invite: the credential `huddle pair --satellite` printed"}, nil)
		return
	}
	name := textOf(fieldOf(body, "name"))
	if !auth.DeviceNameRe.MatchString(name) {
		s.writeJSON(w, 400, errorAnswer{Error: "name: a letter or digit first, up to 32 of them, spaces and dashes"}, nil)
		return
	}
	credential, err := s.registry.ExchangeSatellite(r.Context(), invite, name, s.now().UnixMilli())
	if err != nil {
		s.writeJSON(w, 401, errorAnswer{Error: err.Error()}, nil)
		return
	}
	// the alert row was stored inside the exchange's transaction: pass the newest on from here
	s.emitNewestAlert(r.Context())
	s.writeJSON(w, 200, struct {
		Credential string `json:"credential"`
		Name       string `json:"name"`
	}{credential, name}, nil)
}

// satReport is one batch a satellite link posts: its sessions' events and its open permission
// items, the items under their own keys.
type satReport struct {
	Reports []hub.Report        `json:"reports"`
	Items   []hub.ForwardedItem `json:"items"`
}

// satReportRoute takes one machine's batch. Every report is stamped with the machine its
// credential verified — the registry's machine column is the hub's own fact, never a body's
// claim — and each session's row and the needs-you list hear the change as the local reports do.
func (s *server) satReportRoute(w http.ResponseWriter, r *http.Request, machine string) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		raw = nil
	}
	var batch satReport
	if err := json.Unmarshal(raw, &batch); err != nil {
		s.writeJSON(w, 400, errorAnswer{Error: "body is not JSON"}, nil)
		return
	}
	if len(batch.Reports) > satMaxReports || len(batch.Items) > satMaxItems {
		s.writeJSON(w, 400, errorAnswer{Error: "the batch is too large: split it"}, nil)
		return
	}
	ctx := r.Context()
	for _, rep := range batch.Reports {
		if rep.SessionID == "" || rep.LastEvent == "" || rep.LastEventAt == "" {
			s.writeJSON(w, 400, errorAnswer{Error: "a report needs session_id, last_event and last_event_at"}, nil)
			return
		}
		rep.Machine = machine
		if err := s.registry.Report(ctx, rep); err != nil {
			s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
			return
		}
		if rep.Digest != "" {
			s.shareDigest(ctx, rep)
		}
		s.emitRegistrySession(ctx, rep.SessionID)
		// the machine's terminal answering its own prompt withdraws the hub's copies, exactly
		// as a local PostToolUse or Stop does
		if rep.LastEvent == "PostToolUse" || rep.LastEvent == "Stop" {
			s.withdrawPermissions(ctx, rep.SessionID)
		}
	}
	for _, item := range batch.Items {
		if item.Key == "" || item.SessionID == "" {
			s.writeJSON(w, 400, errorAnswer{Error: "a forwarded item needs key and session"}, nil)
			return
		}
		if err := s.registry.SaveForwardedItem(ctx, item); err != nil {
			s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
			return
		}
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
}

// satNextRoute is the machine's long poll (SPEC 6.7: wait up to 30 s): the poll itself is the
// proof the machine's hooks live, so it first keeps every open permission item of the machine's
// sessions off the silent clock, then answers what is queued — or holds until something is.
func (s *server) satNextRoute(w http.ResponseWriter, r *http.Request, machine string) {
	seconds := int64(waitMax.Seconds())
	if raw := r.URL.Query().Get("wait"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			s.writeJSON(w, 400, errorAnswer{Error: "wait: seconds, at most 30"}, nil)
			return
		}
		if n < 1 {
			n = 1
		}
		if time.Duration(n)*time.Second > waitMax {
			n = int64(waitMax.Seconds())
		}
		seconds = n
	}
	ctx := r.Context()
	at := s.now()
	if err := s.registry.TouchPollsOfMachine(ctx, machine, jsfmt.ISOTime(at)); err != nil {
		s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
		return
	}
	// Armed before the queue is read: a handout queued between an empty read and a later arm
	// would wake no one, and the machine would sit out the whole wait.
	wake := s.satQueue.arm(machine)
	handouts, err := s.registry.SatOffer(ctx, machine, at)
	if err != nil {
		s.satQueue.done(machine, wake)
		s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
		return
	}
	if len(handouts) == 0 && seconds > 0 {
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-wake:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			s.satQueue.done(machine, wake)
			return // the machine went away: nothing to answer
		}
		timer.Stop()
		s.satQueue.done(machine, wake)
		if handouts, err = s.registry.SatOffer(ctx, machine, s.now()); err != nil {
			s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
			return
		}
	} else {
		s.satQueue.done(machine, wake)
	}
	s.writeJSON(w, 200, struct {
		Handouts []hub.SatHandout `json:"handouts"`
	}{handouts}, nil)
}

// satResultBody is one answer a machine posts for a handout it took.
type satResultBody struct {
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// satResultRoute takes the machine's answer for one handout. delivered and refused end the row;
// busy leaves it pending for its re-offer. A refused decision is written onto the hub's own row
// for the item — the app shows why the allow never reached the terminal — and a delivered
// message is marked on the row the owner's send queued.
func (s *server) satResultRoute(w http.ResponseWriter, r *http.Request, path, machine string) {
	m := satResultPath.FindStringSubmatch(path)
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		s.writeJSON(w, 404, errorAnswer{Error: "no such handout"}, nil)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		raw = nil
	}
	var body satResultBody
	if err := json.Unmarshal(raw, &body); err != nil {
		s.writeJSON(w, 400, errorAnswer{Error: "body is not JSON"}, nil)
		return
	}
	if body.Result != hub.SatDelivered && body.Result != hub.SatRefused && body.Result != hub.SatBusy {
		s.writeJSON(w, 400, errorAnswer{Error: "result: delivered, refused or busy"}, nil)
		return
	}
	if body.Result == hub.SatRefused && body.Reason == "" {
		s.writeJSON(w, 400, errorAnswer{Error: "a refusal carries its reason"}, nil)
		return
	}
	ctx := r.Context()
	at := jsfmt.ISOTime(s.now())
	handout, live, err := s.registry.SatResult(ctx, id, body.Result, body.Reason, at)
	if err != nil {
		s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
		return
	}
	if !live {
		s.writeJSON(w, 409, errorAnswer{Error: "already answered: the machine's first result holds"}, nil)
		return
	}
	switch {
	case body.Result == hub.SatRefused && handout.Kind == hub.SatDecision:
		s.refuseForwardedItem(ctx, handout, machine, body.Reason, at)
	case body.Result == hub.SatDelivered && handout.Kind == hub.SatMessage && handout.MessageID != 0:
		if err := s.registry.MarkDelivered(ctx, handout.MessageID, satelliteRoute, at); err != nil {
			s.writeJSON(w, 500, errorAnswer{Error: err.Error()}, nil)
			return
		}
		s.emitRegistrySession(ctx, handout.SessionID)
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
}

// refuseForwardedItem writes a satellite's refusal onto the hub's own row for the item — the
// machine refused the owner's allow, so the row reads refused with the machine's reason — and
// wakes the polls and streams that wait on it.
func (s *server) refuseForwardedItem(ctx context.Context, handout hub.SatHandout, machine, reason, at string) {
	var payload hub.SatRefusalBody
	if err := hub.DecodeSatPayload(handout.Payload, &payload); err != nil {
		fmt.Fprintf(s.o.Stderr, "sat: refused handout %d: %v\n", handout.ID, err)
		return
	}
	item, err := s.registry.PermissionItemByKey(ctx, payload.Key, s.now())
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "sat: refused handout %d: %v\n", handout.ID, err)
		return
	}
	if item == nil {
		return
	}
	moved, err := s.registry.MarkPermissionRefused(ctx, item.ID, reason, at)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "sat: refused handout %d: %v\n", handout.ID, err)
		return
	}
	if !moved {
		return
	}
	// the machine's refusal is this allow's end: the audit names the machine that refused,
	// the way a device's decision names the device (SPEC 7.7)
	if _, err := s.audit(ctx, machine, "refused", strconv.FormatInt(item.ID, 10), nil); err != nil {
		fmt.Fprintf(s.o.Stderr, "sat: the refusal is unaudited: %v\n", err)
	}
	if fresh, err := s.registry.PermissionItemByID(ctx, item.ID, s.now()); err == nil && fresh != nil {
		s.itemPermissionChanged(ctx, *fresh)
	}
}

// satSessionMachine answers the satellite a session belongs to, and whether one is held: ""
// with false is a session of this machine, and nothing is queued for it.
func (s *server) satSessionMachine(ctx context.Context, session string) (string, bool) {
	machine, known, err := s.registry.SessionMachine(ctx, session)
	if err != nil || !known || machine == "" {
		return "", false
	}
	isSat, err := s.registry.SatelliteKnown(ctx, machine)
	if err != nil || !isSat {
		return "", false
	}
	return machine, true
}

// queueForSession queues the owner's words for one session and answers the route they take: a
// session on a satellite waits in the sat queue for its machine's poll, every other session on
// its own delivery route. The hub's row stays pending either way — delivered is what the
// machine's result says, never the enqueue.
func (s *server) queueForSession(ctx context.Context, session string, in hub.MessageIn) (string, int64, error) {
	at := jsfmt.ISOTime(s.now())
	messageID, err := s.registry.QueueMessage(ctx, in, at)
	if err != nil {
		return "", 0, err
	}
	machine, ok := s.satSessionMachine(ctx, session)
	if !ok {
		return s.pushMessage(ctx, messageID, in), messageID, nil
	}
	payload, err := hub.EncodeSatPayload(hub.SatMessageBody{SessionID: session, Text: in.Text})
	if err != nil {
		return "", messageID, err
	}
	if _, err := s.registry.SatEnqueue(ctx, machine, hub.SatMessage, session, 0, messageID, payload, at); err != nil {
		return "", messageID, err
	}
	s.satQueue.wake(machine)
	return satelliteRoute, messageID, nil
}

// offerDecision queues one decision for the machine whose session the item belongs to: the
// handout carries the item's key, so the satellite finds its own row without trusting an id
// from the wire.
func (s *server) offerDecision(ctx context.Context, item hub.PermissionItem, decision, message string) {
	machine, ok := s.satSessionMachine(ctx, item.SessionID)
	if !ok {
		return
	}
	key, err := s.registry.KeyOfItem(ctx, item.ID)
	if err != nil || key == "" {
		fmt.Fprintf(s.o.Stderr, "sat: the decision for item %d has no key to travel under: %v\n", item.ID, err)
		return
	}
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{
		Key: key, SessionID: item.SessionID, Decision: decision, Message: message,
	})
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "sat: the decision for item %d cannot be encoded: %v\n", item.ID, err)
		return
	}
	if _, err := s.registry.SatEnqueue(ctx, machine, hub.SatDecision, item.SessionID, item.ID, 0,
		payload, jsfmt.ISOTime(s.now())); err != nil {
		fmt.Fprintf(s.o.Stderr, "sat: the decision for item %d is not queued: %v\n", item.ID, err)
		return
	}
	s.satQueue.wake(machine)
}

// secrets resolves the home's secret store the satellite link reads its credential from; nil
// takes the OS keystore with the home's keys/ files behind it.
func (s *server) secrets(ctx context.Context, h home.Home) home.SecretStore {
	if s.o.Secrets != nil {
		return s.o.Secrets(ctx, h)
	}
	return outside.SecretsFor(ctx, h)
}

// startSatelliteLink opens the outbound link a satellite role keeps to its hub (SPEC 3): it
// rides this server's process and registry, stops with the server, and never blocks a start —
// a machine whose config or credential is not in order serves its own sessions as solo does,
// with one line saying what is missing. The link also carries this machine's board writes for
// the boards its hub hosts (KBN-SHARED): it is held on the server for the kanban tab's relay.
func (s *server) startSatelliteLink(ctx context.Context) {
	cfg, err := home.LoadConfig(home.Home{Root: s.o.Home})
	if err != nil || cfg.Role != "satellite" || cfg.Hub.URL == "" {
		return
	}
	credential, err := home.LoadSatelliteSecret(ctx, s.secrets(ctx, home.Home{Root: s.o.Home}), cfg.Hub.URL)
	if err != nil {
		fmt.Fprintf(s.o.Stdout, "huddle: role satellite: %v\n", err)
		return
	}
	link := satellite.New(satellite.Options{
		Reg:        s.registry,
		Home:       home.Home{Root: s.o.Home},
		HubURL:     cfg.Hub.URL,
		Credential: credential,
		Now:        s.now,
		Log:        func(line string) { fmt.Fprintln(s.o.Stdout, line) },
		Wake:       s.wakeAppliedItem,
	})
	link.StartCursor(ctx)
	s.satMu.Lock()
	s.satLink, s.satHubURL = link, cfg.Hub.URL
	s.satMu.Unlock()
	safego.Go(s.warn, func() { link.Run(ctx) })
	fmt.Fprintf(s.o.Stdout, "satellite link to %s as %s\n", cfg.Hub.URL, cfg.Machine)
}

// relayFlowToHub carries one flow command for a board the hub hosts onto it, under this
// machine's satellite credential (KBN-SHARED): a claim either machine asks for is the hub's
// answer alone. A board the hub does not list — or a machine with no link, solo or a link that
// never came up — stays local, and so does one the hub no longer answers for (its 404). A hub
// that cannot be reached answers 503 with the one plain line, because a claim needs the hub's
// answer and is never queued.
func (s *server) relayFlowToHub(ctx context.Context, key string, body []byte) (int, []byte, bool) {
	s.satMu.Lock()
	link, hubURL := s.satLink, s.satHubURL
	s.satMu.Unlock()
	if link == nil {
		return 0, nil, false
	}
	hosted, _ := link.HubBoards(ctx)
	if !hosted[key] {
		return 0, nil, false
	}
	status, answer, err := link.Flow(ctx, key, body)
	if err != nil {
		line, _ := json.Marshal(errorAnswer{Error: fmt.Sprintf(
			"the hub at %s is unreachable; a claim needs its answer, try again when it is back", hubURL)})
		return http.StatusServiceUnavailable, append(line, '\n'), true
	}
	if status == http.StatusNotFound {
		return 0, nil, false
	}
	return status, answer, true
}

// readFromHub carries one read of a board the hub hosts onto it and answers the hub's response
// for the tab to stream through (KBN-SAT-READS). Reads may show the last local state while the
// hub cannot be reached — the page already carries its stale marker — so a transport error
// falls back to the local copy, and so do no link, a key the hub does not list and the hub's
// own 404; any other hub answer is the page's.
func (s *server) readFromHub(ctx context.Context, key, path string) (*http.Response, bool) {
	s.satMu.Lock()
	link := s.satLink
	s.satMu.Unlock()
	if link == nil {
		return nil, false
	}
	hosted, _ := link.HubBoards(ctx)
	if !hosted[key] {
		return nil, false
	}
	response, err := link.Read(ctx, key, path)
	if err != nil {
		return nil, false
	}
	if response.StatusCode == http.StatusNotFound {
		_ = response.Body.Close()
		return nil, false
	}
	return response, true
}

// wakeAppliedItem wakes this machine's own permission polls and streams the moment a decision
// from the hub lands on one of its items: the terminal prompt must end at once, not on the
// next re-poll.
func (s *server) wakeAppliedItem(itemID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.registry == nil {
		return
	}
	item, err := s.registry.PermissionItemByID(ctx, itemID, s.now())
	if err != nil || item == nil {
		return
	}
	s.itemPermissionChanged(ctx, *item)
}
