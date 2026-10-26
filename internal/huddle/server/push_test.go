package server

// Web Push (SPEC 7.6): the payload carries who, what kind and the tool name only — never the
// command, path, prompt or answer text — encrypted to each paired device's subscription; one
// push per needs-you item plus one escalation while it still waits, an alert to the devices
// that asked for them, and a subscription the push service calls gone is dropped. The sends go
// to a fake push service on a free loopback port; the payloads are decrypted back with the
// test's own subscription keys.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// pushClock is one fixed moment the sweep cases build their timestamps around.
var pushClock = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// browserKeys is one subscription's key pair as the browser generated it: the public values the
// subscription carries, and the private key only this test holds, to decrypt what arrives.
type browserKeys struct {
	p256dh  string
	auth    string
	private *ecdh.PrivateKey
}

// newBrowserKeys draws one subscription's keys.
func newBrowserKeys(t *testing.T) browserKeys {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("draw the browser's key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("draw the auth secret: %v", err)
	}
	return browserKeys{
		p256dh:  base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
		auth:    base64.RawURLEncoding.EncodeToString(auth),
		private: private,
	}
}

// fakePush is the push service: one loopback endpoint that records every message it is handed
// and answers the status the case set.
type fakePush struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	auths  []string
	paths  []string
	status int
}

// newFakePush starts the push service on a free loopback port.
func newFakePush(t *testing.T) *fakePush {
	t.Helper()
	service := &fakePush{status: http.StatusCreated}
	service.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		service.mu.Lock()
		service.bodies = append(service.bodies, body)
		service.auths = append(service.auths, r.Header.Get("Authorization"))
		service.paths = append(service.paths, r.URL.Path)
		status := service.status
		service.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(service.srv.Close)
	return service
}

// count answers how many messages the service took.
func (f *fakePush) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// body answers the message at i.
func (f *fakePush) body(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies) {
		return nil
	}
	return f.bodies[i]
}

// auth answers the Authorization header the message at i carried.
func (f *fakePush) auth(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.auths) {
		return ""
	}
	return f.auths[i]
}

// bodyAt answers the first message the service took at one endpoint path.
func (f *fakePush) bodyAt(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.paths {
		if p == path {
			return f.bodies[i]
		}
	}
	return nil
}

// answer switches the status every later message meets.
func (f *fakePush) answer(status int) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

// pushServerForTest builds the server face over a real registry in a temp home, its clock in
// the case's hands: the sweep and the payload run in-process, only the send leaves to the fake
// push service.
func pushServerForTest(t *testing.T, endpoint string, now func() time.Time) *server {
	t.Helper()
	dir := t.TempDir()
	db, err := hub.Open(context.Background(), home.Home{Root: dir}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := func(string, ...any) {}
	return &server{
		o:        Options{Home: dir, Data: dir, Stdout: io.Discard, Stderr: io.Discard},
		hub:      channel.NewHub(dir, true),
		registry: db,
		now:      now,
		alerts:   newAlertFanout(),
		vapid:    newVapidSource(nil, filepath.Join(dir, "keys", "vapid"), log),
		pushHTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

// subscribeDevice pairs one device in the registry and stores its subscription with the push
// service the fake serves, under the notification choice the case names.
func subscribeDevice(t *testing.T, s *server, name, endpoint string, keys browserKeys, notify string) {
	t.Helper()
	if err := s.registry.SavePairing(context.Background(), "digest-"+name, 10_000); err != nil {
		t.Fatalf("save the pairing: %v", err)
	}
	if err := s.registry.PairFinish(context.Background(), "digest-"+name, hub.Device{
		Name: name, Handle: []byte("handle-" + name), CredentialJSON: []byte("{}"),
	}, "session-"+name, 2, 1); err != nil {
		t.Fatalf("finish the pairing: %v", err)
	}
	if err := s.registry.SavePushSubscription(context.Background(), hub.PushSubscription{
		Device: name, Endpoint: endpoint, P256dh: keys.p256dh, Auth: keys.auth,
	}, 1); err != nil {
		t.Fatalf("save the subscription: %v", err)
	}
	if notify != hub.NotifyNeeds {
		if err := s.registry.SetDeviceNotify(context.Background(), name, notify); err != nil {
			t.Fatalf("set the choice: %v", err)
		}
	}
}

func TestThePushPayloadCarriesOnlyWhoKindAndTool(t *testing.T) {
	// a permission item whose input carries a secret: the payload may name the tool, never the
	// command the tool was about to run
	item := hub.Item{
		ID: 7, SessionID: "s-1", Kind: hub.KindPermission, Summary: "Bash: echo secret-token",
		Tool: "Bash", Input: `{"command":"echo secret-token"}`,
	}
	raw, err := json.Marshal(needsPushPayload(item, "laptop", 3))
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"item", "session", "machine", "kind", "tool", "url", "badge"} {
		if _, has := asMap[key]; !has {
			t.Fatalf("the payload %s misses %q", raw, key)
		}
	}
	if len(asMap) != 7 {
		t.Fatalf("the payload %s carries more than who, the kind, the tool name, the link and the badge", raw)
	}
	if asMap["session"] != "s-1" || asMap["machine"] != "laptop" || asMap["badge"] != float64(3) ||
		asMap["kind"] != hub.KindPermission || asMap["tool"] != "Bash" || asMap["item"] != float64(7) {
		t.Fatalf("the payload = %s, want who and the kind the item carries", raw)
	}
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "command") {
		t.Fatalf("the payload %s carries the command's text", raw)
	}

	// a question names no tool: the field stays off the answer
	question := hub.Item{ID: 8, SessionID: "s-2", Kind: hub.KindQuestion, Summary: "Pick a database?"}
	raw, _ = json.Marshal(needsPushPayload(question, "", 1))
	if strings.Contains(string(raw), `"tool"`) {
		t.Fatalf("a question's payload %s carries a tool", raw)
	}

	// an alert's payload is the same shape: the alert's own name for the kind, who it names, and
	// no item row to decide on
	event := AlertEvent{Type: "alert.remote_allow", ID: 3, At: jsscalar.ISOTime(pushClock), Body: vv(t,
		`{"item":9,"session":"s-3","tool":"Bash"}`)}
	raw, _ = json.Marshal(alertPushPayload(event, "laptop"))
	for _, want := range []string{`"kind":"alert.remote_allow"`, `"session":"s-3"`, `"machine":"laptop"`, `"tool":"Bash"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the alert payload %s misses %s", raw, want)
		}
	}
	if strings.Contains(string(raw), `"item"`) {
		t.Fatalf("the alert payload %s carries an item row", raw)
	}
}

func TestTheEncryptedPushCarriesNoCommandText(t *testing.T) {
	service := newFakePush(t)
	keys := newBrowserKeys(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	subscribeDevice(t, s, "phone", service.srv.URL, keys, hub.NotifyNeeds)

	// the session reports its machine, so the push can name who waits
	if err := s.registry.Report(context.Background(), hub.Report{
		SessionID: "s-1", Machine: "laptop", LastEvent: "UserPromptSubmit",
		LastEventAt: jsscalar.ISOTime(pushClock),
	}); err != nil {
		t.Fatalf("report the session: %v", err)
	}
	id, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test with secret-token inside"}`), pushClock)
	if err != nil {
		t.Fatalf("create the item: %v", err)
	}
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the push service took %d messages, want the one push", service.count())
	}

	// decrypt the message with the subscription's own keys: the plaintext must be the payload,
	// and it must not carry the command's text
	plaintext := decryptPush(t, service.body(0), keys)
	var payload map[string]any
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		t.Fatalf("the plaintext %q is not the payload's JSON: %v", plaintext, err)
	}
	if payload["kind"] != hub.KindPermission || payload["tool"] != "Bash" ||
		payload["session"] != "s-1" || payload["machine"] != "laptop" || payload["item"] != float64(id) {
		t.Fatalf("the plaintext payload = %s", plaintext)
	}
	if strings.Contains(string(plaintext), "secret-token") || strings.Contains(string(plaintext), "npm test") {
		t.Fatalf("the encrypted plaintext %s carries the command's text", plaintext)
	}
}

// decryptPush opens one aes128gcm Web Push message (RFC 8291) with the subscription's keys:
// the shared secret is the ephemeral server key against the browser's private key, the CEK and
// the nonce come off the header's salt, and the plaintext ends at its padding delimiter.
func decryptPush(t *testing.T, message []byte, keys browserKeys) []byte {
	t.Helper()
	if len(message) < 16+4+1 {
		t.Fatalf("the message %x is shorter than its header", message)
	}
	salt := message[:16]
	keyLen := int(message[20])
	if len(message) < 21+keyLen {
		t.Fatalf("the message %x is shorter than its key id", message)
	}
	serverPub := message[21 : 21+keyLen]
	body := message[21+keyLen:]

	browserPub, _ := base64.RawURLEncoding.DecodeString(keys.p256dh)
	serverKey, err := ecdh.P256().NewPublicKey(serverPub)
	if err != nil {
		t.Fatal("the message's ephemeral key is not a point on the curve")
	}
	shared, err := keys.private.ECDH(serverKey)
	if err != nil {
		t.Fatalf("the shared secret: %v", err)
	}
	auth, _ := base64.RawURLEncoding.DecodeString(keys.auth)

	prkInfo := append(append([]byte("WebPush: info\x00"), browserPub...), serverPub...)
	ikm := hkdfBytes(auth, shared, prkInfo, 32)
	cek := hkdfBytes(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfBytes(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	open, err := gcm.Open([]byte{}, nonce, body, nil)
	if err != nil {
		t.Fatalf("open the message: %v", err)
	}
	// the record ends in the padding delimiter (0x02), and the JSON before it never carries one
	if at := strings.IndexByte(string(open), '\x02'); at >= 0 {
		open = open[:at]
	}
	return open
}

// hkdfBytes is RFC 5869 over SHA-256: extract with the salt, expand with the info, answer
// length bytes.
func hkdfBytes(salt, secret, info []byte, length int) []byte {
	extractor := hmac.New(sha256.New, salt)
	extractor.Write(secret)
	prk := extractor.Sum(nil)
	var out, round []byte
	for i := byte(1); len(out) < length; i++ {
		expander := hmac.New(sha256.New, prk)
		expander.Write(round)
		expander.Write(info)
		expander.Write([]byte{i})
		round = expander.Sum(nil)
		out = append(out, round...)
	}
	return out[:length]
}

func TestThePushSweepSendsOnceAndEscalatesOnceAndNeverAfterADecision(t *testing.T) {
	service := newFakePush(t)
	clock := pushClock
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return clock })
	subscribeDevice(t, s, "phone", service.srv.URL, newBrowserKeys(t), hub.NotifyNeeds)

	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), clock); err != nil {
		t.Fatalf("create the item: %v", err)
	}

	// the first sweep pushes once; another sweep before five minutes adds nothing
	s.sweepPushes(context.Background())
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after two sweeps = %d, want the one", service.count())
	}

	// at five minutes still open, one escalation — and one only
	clock = clock.Add(hub.PushEscalatesAfter)
	s.sweepPushes(context.Background())
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes after the escalation window = %d, want the one escalation too", service.count())
	}

	// a decided item never pushes: the second item is answered before any sweep sees it
	clock = clock.Add(time.Minute)
	second, err := s.registry.CreatePermissionItem(context.Background(), "s-2", "Read",
		[]byte(`{"file_path":"/tmp/x"}`), clock)
	if err != nil {
		t.Fatalf("create the second item: %v", err)
	}
	if won, err := s.registry.DecidePermission(context.Background(), second, "deny", "",
		jsscalar.ISOTime(clock), clock); err != nil || !won {
		t.Fatalf("decide the second = (%v, %v), want the decision to win", won, err)
	}
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes after a decided item = %d, want none for it", service.count())
	}
	// and the bookkeeping keeps its marks: the decided item never waits for a push again
	fresh, err := s.registry.FreshPushItems(context.Background(), clock.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range fresh {
		if item.ID == second {
			t.Fatalf("the decided item %d waits for a push", second)
		}
	}
}

// A session that enters needs-you pushes once — a permission request with no item to speak for
// it is an entry of its own — and pushes no second time while it stays there; leaving the state
// ends the entry, and the next entry pushes again.
func TestAPushGoesOncePerNeedsYouEntry(t *testing.T) {
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	keys := newBrowserKeys(t)
	subscribeDevice(t, s, "phone", service.srv.URL, keys, hub.NotifyNeeds)
	report := func(event string) {
		if err := s.registry.Report(context.Background(), hub.Report{
			SessionID: "s-1", Machine: "laptop", LastEvent: event,
			LastEventAt: jsscalar.ISOTime(pushClock),
		}); err != nil {
			t.Fatalf("report %s: %v", event, err)
		}
	}

	report("PermissionRequest")
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after the entry = %d, want the one", service.count())
	}
	plaintext := decryptPush(t, service.body(0), keys)
	if !strings.Contains(string(plaintext), `"kind":"session"`) ||
		!strings.Contains(string(plaintext), `"session":"s-1"`) ||
		!strings.Contains(string(plaintext), `"machine":"laptop"`) {
		t.Fatalf("the entry's plaintext = %s, want who and that the session waits", plaintext)
	}
	if strings.Contains(string(plaintext), `"item"`) {
		t.Fatalf("the entry's plaintext %s carries an item row", plaintext)
	}

	// still in needs-you: the sweeps repeat, the push does not
	s.sweepPushes(context.Background())
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after three sweeps = %d, want no repeat", service.count())
	}

	// a prompt answers the ask: the session left needs-you, and no push goes out for that
	report("UserPromptSubmit")
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after the answer = %d, want none", service.count())
	}

	// a second permission request is a new entry: it pushes once more
	report("PermissionRequest")
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes after the re-entry = %d, want the one more", service.count())
	}
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes after the re-entry's second sweep = %d, want no repeat", service.count())
	}
}

// A needs-you push carries the open needs-you count, the sessions the Now tab lists as waiting,
// so the service worker can set the Home Screen badge while the app is closed.
func TestANeedsYouPushCarriesTheOpenNeedsYouCount(t *testing.T) {
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	keys := newBrowserKeys(t)
	subscribeDevice(t, s, "phone", service.srv.URL, keys, hub.NotifyNeeds)
	for _, id := range []string{"s-1", "s-2", "s-3"} {
		if err := s.registry.Report(context.Background(), hub.Report{
			SessionID: id, Machine: "laptop", LastEvent: "UserPromptSubmit",
			LastEventAt: jsscalar.ISOTime(pushClock),
		}); err != nil {
			t.Fatalf("report %s: %v", id, err)
		}
	}
	// two sessions wait on an item each, the third works: the count is two
	for _, id := range []string{"s-1", "s-2"} {
		if _, err := s.registry.CreatePermissionItem(context.Background(), id, "Bash",
			[]byte(`{"command":"npm test"}`), pushClock); err != nil {
			t.Fatalf("create the item for %s: %v", id, err)
		}
	}
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes = %d, want one per item", service.count())
	}
	for i := 0; i < service.count(); i++ {
		var payload map[string]any
		plaintext := decryptPush(t, service.body(i), keys)
		if err := json.Unmarshal(plaintext, &payload); err != nil {
			t.Fatalf("the plaintext %q is not JSON: %v", plaintext, err)
		}
		if payload["badge"] != float64(2) {
			t.Fatalf("the push %s carries badge %v, want the 2 sessions that wait", plaintext, payload["badge"])
		}
	}

	// a session's own entry push carries the same count
	if err := s.registry.Report(context.Background(), hub.Report{
		SessionID: "s-3", Machine: "laptop", LastEvent: "PermissionRequest",
		LastEventAt: jsscalar.ISOTime(pushClock),
	}); err != nil {
		t.Fatalf("report the entry: %v", err)
	}
	s.sweepPushes(context.Background())
	if service.count() != 3 {
		t.Fatalf("the pushes after the entry = %d, want one more", service.count())
	}
	if plaintext := string(decryptPush(t, service.body(2), keys)); !strings.Contains(plaintext, `"badge":3`) {
		t.Fatalf("the entry's push %s does not carry badge 3", plaintext)
	}
}

// An item's first push is its session's entry push: the session pass sends no second one for
// the same entry, and after the item is answered the next entry pushes once.
func TestAnItemsFirstPushSpeaksForTheSessionsEntry(t *testing.T) {
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	keys := newBrowserKeys(t)
	subscribeDevice(t, s, "phone", service.srv.URL, keys, hub.NotifyNeeds)
	report := func(event string) {
		if err := s.registry.Report(context.Background(), hub.Report{
			SessionID: "s-1", Machine: "laptop", LastEvent: event,
			LastEventAt: jsscalar.ISOTime(pushClock),
		}); err != nil {
			t.Fatalf("report %s: %v", event, err)
		}
	}

	report("UserPromptSubmit")
	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), pushClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	s.sweepPushes(context.Background())
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after the item = %d, want the item's one alone", service.count())
	}
	if strings.Contains(string(service.body(0)), `"kind":"session"`) {
		t.Fatalf("the item's push %s came from the session pass", service.body(0))
	}

	// the tool runs: the session leaves needs-you, and the next entry pushes again
	if _, err := s.registry.WithdrawPermissions(context.Background(), "s-1",
		jsscalar.ISOTime(pushClock)); err != nil {
		t.Fatalf("withdraw the item: %v", err)
	}
	report("PostToolUse")
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after the answer = %d, want none", service.count())
	}
	report("PermissionRequest")
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes after the new entry = %d, want the one more", service.count())
	}
	if plaintext := string(decryptPush(t, service.body(1), keys)); !strings.Contains(plaintext, `"kind":"session"`) {
		t.Fatalf("the new entry's push %s is not the session's", plaintext)
	}
}

func TestAlertsReachOnlyTheDevicesThatAskedForThem(t *testing.T) {
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	// each device holds its own subscription: the endpoint is the push service's own name for
	// one, so no two of them share one
	subscribeDevice(t, s, "quiet", service.srv.URL+"/quiet", newBrowserKeys(t), hub.NotifyNeeds)
	watching := newBrowserKeys(t)
	subscribeDevice(t, s, "watching", service.srv.URL+"/watching", watching, hub.NotifyAll)
	subscribeDevice(t, s, "dark", service.srv.URL+"/dark", newBrowserKeys(t), hub.NotifyOff)

	// a fresh item reaches every device but the one that chose off
	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), pushClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the item's pushes = %d, want one for each device but the dark one", service.count())
	}

	// an alert reaches only the devices that chose needs-and-alerts
	before := service.count()
	s.pushAlertEvent(context.Background(), AlertEvent{
		Type: "alert.device_paired", ID: 1, At: jsscalar.ISOTime(pushClock),
		Body: vv(t, `{"device":"tablet","at":"2026-10-16T12:00:00.000Z"}`),
	})
	if service.count() != before+1 {
		t.Fatalf("the alert's pushes = %d, want the one for the watching device", service.count()-before)
	}
	plaintext := decryptPush(t, service.body(before), watching)
	if !strings.Contains(string(plaintext), `"kind":"alert.device_paired"`) {
		t.Fatalf("the alert's plaintext = %s, want the alert's own kind", plaintext)
	}
}

// countAt answers how many messages the service took at one endpoint path.
func (f *fakePush) countAt(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == path {
			n++
		}
	}
	return n
}

// A finished turn is its own wake choice, off by default: only the device that turned it on
// hears the turn item, at create and at its escalation, and no session entry push speaks for
// the turn to the others; a permission ask still reaches every device its choice lets hear it.
func TestAFinishedTurnPushesOnlyTheDevicesThatChoseIt(t *testing.T) {
	service := newFakePush(t)
	clock := pushClock
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return clock })
	subscribeDevice(t, s, "phone", service.srv.URL+"/phone", newBrowserKeys(t), hub.NotifyNeeds)
	tabletKeys := newBrowserKeys(t)
	subscribeDevice(t, s, "tablet", service.srv.URL+"/tablet", tabletKeys, hub.NotifyNeeds)
	subscribeDevice(t, s, "dark", service.srv.URL+"/dark", newBrowserKeys(t), hub.NotifyOff)
	for _, name := range []string{"tablet", "dark"} {
		if err := s.registry.SetDeviceTurn(context.Background(), name, true); err != nil {
			t.Fatalf("turn the finished-turn push on for %s: %v", name, err)
		}
	}
	report := func(event string, at time.Time) {
		if err := s.registry.Report(context.Background(), hub.Report{
			SessionID: "s-1", Machine: "laptop", LastEvent: event, LastEventAt: jsscalar.ISOTime(at),
			Digest: "ran the tests",
		}); err != nil {
			t.Fatalf("report %s: %v", event, err)
		}
	}
	report("UserPromptSubmit", clock)
	report("Stop", clock.Add(time.Second))
	clock = clock.Add(2 * time.Second)

	s.sweepPushes(context.Background())
	s.sweepPushes(context.Background())
	if got := [3]int{service.countAt("/phone"), service.countAt("/tablet"), service.countAt("/dark")}; got != [3]int{0, 1, 0} {
		t.Fatalf("the turn's pushes (phone, tablet, dark) = %v, want the tablet's one alone", got)
	}
	plaintext := decryptPush(t, service.bodyAt("/tablet"), tabletKeys)
	if !strings.Contains(string(plaintext), `"kind":"turn"`) {
		t.Fatalf("the tablet's push %s is not the turn item's", plaintext)
	}

	// the escalation keeps the same rule
	clock = clock.Add(hub.PushEscalatesAfter)
	s.sweepPushes(context.Background())
	if got := [3]int{service.countAt("/phone"), service.countAt("/tablet"), service.countAt("/dark")}; got != [3]int{0, 2, 0} {
		t.Fatalf("the turn's pushes after the escalation (phone, tablet, dark) = %v, want the tablet's two", got)
	}

	// a permission ask keeps each device's own choice: the phone and the tablet hear it
	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-2", "Bash",
		[]byte(`{"command":"npm test"}`), clock); err != nil {
		t.Fatalf("create the permission item: %v", err)
	}
	s.sweepPushes(context.Background())
	if got := [3]int{service.countAt("/phone"), service.countAt("/tablet"), service.countAt("/dark")}; got != [3]int{1, 3, 0} {
		t.Fatalf("the pushes after the permission ask (phone, tablet, dark) = %v, want one more each but dark", got)
	}
}

// The finished-turn choice is the device's own, off until it turns it on, and kept.
func TestTheFinishedTurnChoiceOverTheServer(t *testing.T) {
	srv := startServer(t, nil)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	device := pushDeviceHeaders(auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie))

	if code, _, out := srv.get(t, "/api/push/notify", device); code != 200 || bodyMap(t, out)["turn"] != false {
		t.Fatalf("the default turn choice = %d %s, want off", code, out)
	}
	if code, out := srv.post(t, "/api/push/notify", `{"turn":true}`, device); code != 200 {
		t.Fatalf("turn it on = %d %s", code, out)
	}
	code, _, out := srv.get(t, "/api/push/notify", device)
	if code != 200 || bodyMap(t, out)["turn"] != true || bodyMap(t, out)["notify"] != hub.NotifyNeeds {
		t.Fatalf("the choice after turning it on = %d %s, want turn on and needs you only kept", code, out)
	}
	if code, out := srv.post(t, "/api/push/notify", `{"turn":"yes"}`, device); code != 400 {
		t.Fatalf("a torn turn choice = %d %s, want 400", code, out)
	}
	if code, out := srv.post(t, "/api/push/notify", `{}`, device); code != 400 {
		t.Fatalf("an empty choice = %d %s, want 400", code, out)
	}
}

func TestAGoneSubscriptionIsDropped(t *testing.T) {
	service := newFakePush(t)
	clock := pushClock
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return clock })
	subscribeDevice(t, s, "phone", service.srv.URL, newBrowserKeys(t), hub.NotifyNeeds)

	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), pushClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes = %d, want the one", service.count())
	}
	service.answer(http.StatusGone)

	// the escalation meets a gone subscription: it is dropped, and the sweep after it finds
	// nothing left to send to
	clock = clock.Add(hub.PushEscalatesAfter)
	s.sweepPushes(context.Background())
	subs, err := s.registry.PushSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 0 {
		t.Fatalf("the subscriptions after a 410 = %v, want none left", subs)
	}
}

func TestTheVapidPublicHalfIsTheKeysOwnP256Point(t *testing.T) {
	// d = 2 on P-256, a published point: the public half must be the point's exact uncompressed
	// bytes, the encoding every browser subscription and push service reads
	scalar := make([]byte, 32)
	scalar[31] = 2
	keys, err := vapidKeysOf(base64.RawURLEncoding.EncodeToString(scalar))
	if err != nil {
		t.Fatalf("the known key: %v", err)
	}
	point, err := base64.RawURLEncoding.DecodeString(keys.Public)
	if err != nil {
		t.Fatalf("the public half: %v", err)
	}
	want := append([]byte{0x04},
		0x7c, 0xf2, 0x7b, 0x18, 0x8d, 0x03, 0x4f, 0x7e, 0x8a, 0x52, 0x38, 0x03, 0x04, 0xb5, 0x1a, 0xc3,
		0xc0, 0x89, 0x69, 0xe2, 0x77, 0xf2, 0x1b, 0x35, 0xa6, 0x0b, 0x48, 0xfc, 0x47, 0x66, 0x99, 0x78,
		0x07, 0x77, 0x55, 0x10, 0xdb, 0x8e, 0xd0, 0x40, 0x29, 0x3d, 0x9a, 0xc6, 0x9f, 0x74, 0x30, 0xdb,
		0xba, 0x7d, 0xad, 0xe6, 0x3c, 0xe9, 0x82, 0x29, 0x9e, 0x04, 0xb7, 0x9d, 0x22, 0x78, 0x73, 0xd1,
	)
	if !bytes.Equal(point, want) {
		t.Fatalf("the public half = %x, want the known 2·G point %x", point, want)
	}
}

func TestTheVapidKeysFallBackToTheFileWhenTheKeystoreCannotAnswer(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keys", "vapid")
	source := newVapidSource(hangingVapidStore{}, file, func(string, ...any) {})

	first, err := source.Keys(context.Background())
	if err != nil {
		t.Fatalf("the first keys: %v", err)
	}
	if first.Private == "" || first.Public == "" {
		t.Fatalf("the keys = %+v, want a made pair", first)
	}
	// the file holds the private key, private by mode
	stored, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("the key file: %v", err)
	}
	if string(stored) != first.Private {
		t.Fatalf("the key file holds %q, want the private key", stored)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key file's mode = %v (%v), want 0600", info, err)
	}
	// a second source over the same file answers the same pair without making a new one
	second, err := newVapidSource(hangingVapidStore{}, file, func(string, ...any) {}).Keys(context.Background())
	if err != nil {
		t.Fatalf("the second keys: %v", err)
	}
	if second != first {
		t.Fatalf("the second keys = %+v, want the stored pair", second)
	}
}

// hangingVapidStore is a keystore that never answers: every call falls through to the file.
type hangingVapidStore struct{}

func (hangingVapidStore) Get(context.Context) (string, bool) {
	select {}
}

func (hangingVapidStore) Set(context.Context, string) error {
	select {}
}

// ── the routes, over the real server ───────────────────────────────────────────────────────────────────────────────

// deviceHeaders is one paired device's cookie as request headers.
func pushDeviceHeaders(cookie string) map[string]string {
	return map[string]string{"Cookie": cookie, "x-huddle-token": "", "content-type": "application/json"}
}

func TestThePushRoutesOverTheServer(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_PUSH_SWEEP_MS": "150"})
	service := newFakePush(t)
	keys := newBrowserKeys(t)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	cookie := auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie)
	device := pushDeviceHeaders(cookie)

	// the public key the browser subscribes with: 65 bytes of P-256 point, base64url
	code, _, out := srv.get(t, "/api/push/key", nil)
	if code != 200 {
		t.Fatalf("the public key = %d %s", code, out)
	}
	public, _ := bodyMap(t, out)["public_key"].(string)
	point, err := base64.RawURLEncoding.DecodeString(public)
	if err != nil || len(point) != 65 {
		t.Fatalf("the public key %q is not a P-256 point", public)
	}

	// a subscription is a paired device's: the root's own credential may not hold one
	body := fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}}`,
		service.srv.URL, keys.p256dh, keys.auth)
	if code, out := srv.post(t, "/api/push/subscribe", body, map[string]string{"x-huddle-token": srv.token}); code != 403 {
		t.Fatalf("the root's subscribe = %d %s, want 403", code, out)
	}
	if code, out := srv.post(t, "/api/push/subscribe", body, device); code != 200 {
		t.Fatalf("the device's subscribe = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/push/subscribe", `{"endpoint":"","keys":{}}`, device); code != 400 {
		t.Fatalf("a torn subscribe = %d %s, want 400", code, out)
	}

	// the choice the Settings screen reads and writes
	if code, _, out := srv.get(t, "/api/push/notify", device); code != 200 || bodyMap(t, out)["notify"] != hub.NotifyNeeds {
		t.Fatalf("the default choice = %d %s, want needs you only", code, out)
	}
	if code, out := srv.post(t, "/api/push/notify", `{"notify":"all"}`, device); code != 200 {
		t.Fatalf("choose all = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/push/notify", `{"notify":"sometimes"}`, device); code != 400 {
		t.Fatalf("a torn choice = %d %s, want 400", code, out)
	}
	if code, _, _ := srv.get(t, "/api/push/notify", map[string]string{"x-huddle-token": srv.token}); code != 403 {
		t.Fatalf("the root's choice read = %d, want 403", code)
	}

	// a needs-you item pushes the device: the sweep the create wakes reaches the push service
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	if !until(5*time.Second, func() bool { return service.count() >= 1 }) {
		t.Fatalf("the push service took %d messages, want the item's push", service.count())
	}
	plaintext := decryptPush(t, service.body(0), keys)
	if !strings.Contains(string(plaintext), `"kind":"permission"`) ||
		!strings.Contains(string(plaintext), fmt.Sprintf(`"item":%d`, id)) {
		t.Fatalf("the push's plaintext = %s, want the item's payload", plaintext)
	}

	// a remote allow raises the alert, and this device chose needs-and-alerts: it pushes too
	if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`,
		map[string]string{"x-huddle-token": srv.token}); code != 200 {
		t.Fatalf("the allow = %d %s", code, out)
	}
	if !until(5*time.Second, func() bool { return service.count() >= 2 }) {
		t.Fatalf("the push service took %d messages, want the alert's push too", service.count())
	}
	alert := decryptPush(t, service.body(service.count()-1), keys)
	if !strings.Contains(string(alert), `"kind":"alert.remote_allow"`) {
		t.Fatalf("the alert's plaintext = %s, want the remote allow's kind", alert)
	}

	// the device ends its subscription: removing what is gone answers 404 the second time
	if code, _, _, err := srv.request(context.Background(), http.MethodDelete,
		"/api/push/subscribe?endpoint="+service.srv.URL, "",
		map[string]string{"Cookie": cookie, "x-huddle-token": "", "x-huddle-action": "/api/push/subscribe"}); err != nil || code != 200 {
		t.Fatalf("the unsubscribe = %d (%v), want 200", code, err)
	}
	if code, _, _, _ := srv.request(context.Background(), http.MethodDelete,
		"/api/push/subscribe?endpoint="+service.srv.URL, "",
		map[string]string{"Cookie": cookie, "x-huddle-token": "", "x-huddle-action": "/api/push/subscribe"}); code != 404 {
		t.Fatalf("the unsubscribe again = %d, want 404", code)
	}
}

// ── the deep links, the VAPID signature and the failing path ────────────────────────────────────────────────────────

func TestEveryPushCarriesTheAppAddressItOpens(t *testing.T) {
	item := hub.Item{ID: 7, SessionID: "s 1", Kind: hub.KindPermission, Tool: "Bash"}
	if got := needsPushPayload(item, "laptop", 1).URL; got != "/#/now/s%201?item=7" {
		t.Fatalf("the item's link = %q, want the session's screen at the item", got)
	}
	if got := sessionPushOf(hub.SessionRow{SessionID: "s-2", Machine: "laptop"}, 1).URL; got != "/#/now/s-2" {
		t.Fatalf("the session entry's link = %q, want the session's screen", got)
	}
	event := AlertEvent{Type: "alert.device_paired", ID: 1, At: jsscalar.ISOTime(pushClock), Body: vv(t, `{"device":"tablet"}`)}
	if got := alertPushPayload(event, "laptop").URL; got != "/#/now" {
		t.Fatalf("an owner alert's link = %q, want the Now list", got)
	}
	failing := upstreamPushOf(radarAlert{ID: "upstream:anthropic:1", Kind: "upstream", Detail: "ECERT: fix"}, "laptop")
	if failing.URL != "/#/radar/alerts" || failing.Kind != "upstream_failing" || failing.Detail != "ECERT: fix" {
		t.Fatalf("the failing path's payload = %+v, want its kind, detail and Radar's alerts", failing)
	}
}

func TestEveryPushIsSignedWithTheHubsVapidKey(t *testing.T) {
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	subscribeDevice(t, s, "phone", service.srv.URL+"/sub/1", newBrowserKeys(t), hub.NotifyNeeds)
	if _, err := s.registry.CreatePermissionItem(context.Background(), "s-1", "Bash",
		[]byte(`{"command":"npm test"}`), pushClock); err != nil {
		t.Fatalf("create the item: %v", err)
	}
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes = %d, want the one", service.count())
	}
	keys, err := s.vapid.Keys(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// RFC 8292: "vapid t=<JWT>, k=<the public key>", the JWT ES256 over the endpoint's origin
	header := service.auth(0)
	token, public, found := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !strings.HasPrefix(header, "vapid t=") || !found {
		t.Fatalf("the Authorization header = %q, want the vapid scheme", header)
	}
	if public != keys.Public {
		t.Fatalf("the header's key = %q, want the hub's public key %q", public, keys.Public)
	}
	claims := verifyVapidJWT(t, token, public)
	if claims.Aud != service.srv.URL {
		t.Fatalf("the JWT's audience = %q, want the push service's origin %q", claims.Aud, service.srv.URL)
	}
	if claims.Sub != "mailto:huddle-push@localhost" {
		t.Fatalf("the JWT's subject = %q, want one mailto address", claims.Sub)
	}
	if left := time.Until(time.Unix(claims.Exp, 0)); left <= 0 || left > 24*time.Hour {
		t.Fatalf("the JWT expires in %s, want within the 24 hours RFC 8292 allows", left)
	}
}

// vapidClaims is the JWT body a VAPID signature carries.
type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// verifyVapidJWT checks one ES256 JWT against the uncompressed P-256 point it names and answers
// its claims: the signature is the raw 32-byte r and s, as JWS writes it.
func verifyVapidJWT(t *testing.T, token, public string) vapidClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the JWT %q has %d parts, want 3", token, len(parts))
	}
	head, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if !strings.Contains(string(head), `"ES256"`) {
		t.Fatalf("the JWT header = %s, want ES256", head)
	}
	point, err := base64.RawURLEncoding.DecodeString(public)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		t.Fatalf("the public key is not a P-256 point: %v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("the JWT signature is %d bytes, want r and s of 32 each", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the JWT signature does not verify against the hub's public key")
	}
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims vapidClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatalf("the JWT claims %s: %v", body, err)
	}
	return claims
}

// fakeRadar answers the alerts radar raises, as its in-process API writes them.
type fakeRadar struct {
	mu     sync.Mutex
	alerts string
}

func (f *fakeRadar) set(alerts string) {
	f.mu.Lock()
	f.alerts = alerts
	f.mu.Unlock()
}

func (f *fakeRadar) RadarAnswer(_ context.Context, path string) ([]byte, bool) {
	if path != "/api/alerts" {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte(f.alerts), true
}

func TestAFailingAnthropicPathPushesOncePerFailure(t *testing.T) {
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	radar := &fakeRadar{alerts: `{"alerts":[]}`}
	s.radarAlerts = radar
	all := newBrowserKeys(t)
	subscribeDevice(t, s, "needs", service.srv.URL+"/needs", newBrowserKeys(t), hub.NotifyNeeds)
	subscribeDevice(t, s, "all", service.srv.URL+"/all", all, hub.NotifyAll)
	subscribeDevice(t, s, "dark", service.srv.URL+"/dark", newBrowserKeys(t), hub.NotifyOff)

	s.sweepPushes(context.Background())
	if service.count() != 0 {
		t.Fatalf("the pushes with a healthy path = %d, want none", service.count())
	}

	// radar raises the upstream alert: only the device that chose needs-and-alerts hears of it,
	// once; the needs-only device and the dark one hear nothing
	radar.set(`{"alerts":[{"kind":"stuck","sessionId":"s-1","id":"stuck:s-1","detail":"quiet"},` +
		`{"kind":"upstream","sessionId":"","id":"upstream:anthropic:1000","since":1000,"detail":"ECERT: trust the CA","severity":"err"}]}`)
	s.sweepPushes(context.Background())
	if service.count() != 1 || service.bodyAt("/all") == nil {
		t.Fatalf("the failing path's pushes = %d, want the one for the alerts device", service.count())
	}
	if service.bodyAt("/needs") != nil || service.bodyAt("/dark") != nil {
		t.Fatal("a device that chose needs-you only, or off, heard the failing path")
	}
	plaintext := string(decryptPush(t, service.bodyAt("/all"), all))
	for _, want := range []string{`"kind":"upstream_failing"`, `"detail":"ECERT: trust the CA"`, `"url":"/#/radar/alerts"`} {
		if !strings.Contains(plaintext, want) {
			t.Fatalf("the failing path's plaintext = %s, want %s", plaintext, want)
		}
	}
	s.sweepPushes(context.Background())
	if service.count() != 1 {
		t.Fatalf("the pushes after a second sweep = %d, want no repeat", service.count())
	}

	// the path recovers and fails again: the new failure is a new alert and pushes once more
	radar.set(`{"alerts":[]}`)
	s.sweepPushes(context.Background())
	radar.set(`{"alerts":[{"kind":"upstream","id":"upstream:anthropic:5000","since":5000,"detail":"E401: sign in"}]}`)
	s.sweepPushes(context.Background())
	if service.count() != 2 {
		t.Fatalf("the pushes after the second failure = %d, want one more", service.count())
	}
}

// ── the desk's focus ────────────────────────────────────────────────────────────────────────────────────────────────

// A hub tab that is visible and focused on one device keeps every other device quiet for 60 s
// after its last report: the items it would push take their marks unsent. With no report, or
// one 61 s old, the pushes go out; a device's own focus never quiets that device.
func TestAFocusedDeskTabKeepsTheOtherDevicesQuiet(t *testing.T) {
	service := newFakePush(t)
	clock := pushClock
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return clock })
	subscribeDevice(t, s, "phone", service.srv.URL+"/phone", newBrowserKeys(t), hub.NotifyNeeds)
	subscribeDevice(t, s, "tablet", service.srv.URL+"/tablet", newBrowserKeys(t), hub.NotifyNeeds)
	ask := func(session string) {
		t.Helper()
		if _, err := s.registry.CreatePermissionItem(context.Background(), session, "Bash",
			[]byte(`{"command":"npm test"}`), clock); err != nil {
			t.Fatalf("create the item for %s: %v", session, err)
		}
		s.sweepPushes(context.Background())
	}
	counts := func() [2]int { return [2]int{service.countAt("/phone"), service.countAt("/tablet")} }

	// no tab ever reported its focus: the item pushes both
	ask("s-1")
	if got := counts(); got != [2]int{1, 1} {
		t.Fatalf("the pushes with no focus reported (phone, tablet) = %v, want one each", got)
	}

	// the owner's desk tab is focused: nothing pushes, and the item still takes its mark
	s.noteFocus("", "desk-tab", true, clock)
	clock = clock.Add(time.Second)
	ask("s-2")
	if got := counts(); got != [2]int{1, 1} {
		t.Fatalf("the pushes with the desk focused (phone, tablet) = %v, want none more", got)
	}

	// 61 s after the desk's last report the quiet is over; the quiet item never pushes late
	clock = clock.Add(60 * time.Second)
	ask("s-3")
	if got := counts(); got != [2]int{2, 2} {
		t.Fatalf("the pushes 61 s after the desk's report (phone, tablet) = %v, want one more each", got)
	}

	// a tab that turns hidden ends its quiet at once
	s.noteFocus("", "desk-tab", true, clock)
	s.noteFocus("", "desk-tab", false, clock)
	ask("s-4")
	if got := counts(); got != [2]int{3, 3} {
		t.Fatalf("the pushes after the desk turned hidden (phone, tablet) = %v, want one more each", got)
	}

	// the phone's own focused tab quiets the tablet, never the phone itself
	s.noteFocus("phone", "phone-tab", true, clock)
	ask("s-5")
	if got := counts(); got != [2]int{4, 3} {
		t.Fatalf("the pushes with the phone focused (phone, tablet) = %v, want the phone's one alone", got)
	}
}

// The focus report is the browser's own: a tab names itself and says whether it is visible and
// focused; a report that names no tab, or no state, is refused. A focused owner tab keeps the
// paired phone quiet until it reports itself hidden.
func TestTheFocusReportOverTheServer(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_PUSH_SWEEP_MS": "150"})
	service := newFakePush(t)
	keys := newBrowserKeys(t)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	device := pushDeviceHeaders(auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie))
	body := fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}}`, service.srv.URL, keys.p256dh, keys.auth)
	if code, out := srv.post(t, "/api/push/subscribe", body, device); code != 200 {
		t.Fatalf("the device's subscribe = %d %s", code, out)
	}
	owner := map[string]string{"x-huddle-token": srv.token}

	if code, out := srv.post(t, "/api/push/focus", `{"focused":true}`, owner); code != 400 {
		t.Fatalf("a report with no tab = %d %s, want 400", code, out)
	}
	if code, out := srv.post(t, "/api/push/focus", `{"tab":"desk"}`, owner); code != 400 {
		t.Fatalf("a report with no state = %d %s, want 400", code, out)
	}
	if code, out := srv.post(t, "/api/push/focus", `{"tab":"desk","focused":true}`, owner); code != 200 {
		t.Fatalf("the desk's focus = %d %s", code, out)
	}
	permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	if until(1500*time.Millisecond, func() bool { return service.count() > 0 }) {
		t.Fatalf("the push service took %d messages while the desk was focused, want none", service.count())
	}

	if code, out := srv.post(t, "/api/push/focus", `{"tab":"desk","focused":false}`, owner); code != 200 {
		t.Fatalf("the desk's hidden report = %d %s", code, out)
	}
	permitPost(t, srv, "s-2", "Bash", `{"command":"npm test"}`)
	if !until(5*time.Second, func() bool { return service.count() >= 1 }) {
		t.Fatalf("the push service took %d messages after the desk turned hidden, want the item's push", service.count())
	}
}
