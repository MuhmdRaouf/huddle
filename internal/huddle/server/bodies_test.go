package server

// The owner's read of one request's bodies in the router's store (GET /api/bodies/<id>):
// what it answers for a kept, an unknown and an unreadable store, that the owner's
// credential alone reads it, and that a read never creates the store it reads. One server
// runs in this process, so the test hands the route its own key source and environment —
// nothing here reaches a real keystore or the real state dirs.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// bodiesKey is the test's body key: 32 bytes, the length an AES-256 seal takes.
var bodiesKey = []byte("0123456789abcdef0123456789abcdef")

// fakeKeySource is a key store the test owns: one fixed answer, no machine behind it.
type fakeKeySource struct {
	value string
	ok    bool
}

func (f *fakeKeySource) Get(ctx context.Context) (string, bool) { return f.value, f.ok }

func (f *fakeKeySource) Set(ctx context.Context, value string) error {
	f.value, f.ok = value, true
	return nil
}

// bodiesRig is one in-process server and the radar home its bodies route reads.
type bodiesRig struct {
	base  string // the server's own origin
	radar string // its RADAR_HOME
	token string // the owner's credential
}

// bodiesServer runs one server in this process, the way the permission sweeper's test does,
// so the test can hand the bodies route its own key source and environment.
func bodiesServer(t *testing.T, key ledger.KeySource) bodiesRig {
	t.Helper()
	radar := t.TempDir()
	environ := []string{
		"RADAR_HOME=" + radar,
		"HOME=" + t.TempDir(),
		"CLAUDE_CONFIG_DIR=" + t.TempDir(),
		"XDG_CONFIG_HOME=" + t.TempDir(),
		"XDG_DATA_HOME=" + t.TempDir(),
		"XDG_STATE_HOME=" + t.TempDir(),
		"XDG_CACHE_HOME=" + t.TempDir(),
	}
	pipeReader, pipeWriter := io.Pipe()
	announced := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pipeReader).ReadString('\n')
		announced <- line
		_, _ = io.Copy(io.Discard, pipeReader)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		_ = Serve(ctx, Options{
			Version: "test", Host: "127.0.0.1", Port: 0,
			Home: t.TempDir(), Data: t.TempDir(), Token: "bodies-owner", AutoCreate: true,
			Environ: environ,
			BodyKey: key,
			Stdout:  pipeWriter, Stderr: io.Discard,
		})
		pipeWriter.Close()
		close(served)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})
	line := <-announced
	match := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the server did not say its port: %q", line)
	}
	base := "http://127.0.0.1:" + match[1]
	if ok := until(5*time.Second, func() bool {
		resp, err := http.Get(base + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	}); !ok {
		t.Fatal("the server never answered /health")
	}
	return bodiesRig{base: base, radar: radar, token: "bodies-owner"}
}

// bodiesCall issues one request against the rig and answers the status and body. A write
// names its action the way the guard wants; the credential rides the token header, empty
// for a caller that holds none.
func bodiesCall(t *testing.T, rig bodiesRig, method, path, body, token string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, rig.base+path, reader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != "" {
		request.Header.Set("content-type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set(ActionHeader, path)
	}
	request.Header.Set("x-huddle-token", token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer of %s: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// bodiesMember joins one member over HTTP, the way permitMember does against a child server,
// and answers the credential a session that is not the owner holds.
func bodiesMember(t *testing.T, rig bodiesRig) string {
	t.Helper()
	code, out := bodiesCall(t, rig, http.MethodPost, "/api/tokens", `{}`, rig.token)
	if code != 200 {
		t.Fatalf("make an invite: %d %s", code, out)
	}
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &invite); err != nil || invite.Token == "" {
		t.Fatalf("the invite = %s (%v)", out, err)
	}
	code, out = bodiesCall(t, rig, http.MethodPost, "/api/join",
		`{"token":"`+invite.Token+`","name":"worker"}`, "")
	if code != 200 {
		t.Fatalf("join: %d %s", code, out)
	}
	var joined struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(out), &joined); err != nil || joined.Credential == "" {
		t.Fatalf("the join's answer = %s (%v)", out, err)
	}
	return joined.Credential
}

// bodiesSeed stores one request's two sides in the router's own body store, the way the
// router writes them: redacted on the way in, so the seeded text carries no secret shape.
func bodiesSeed(t *testing.T, radar, id, input, output string) {
	t.Helper()
	ctx := context.Background()
	store, err := ledger.OpenBodies(paths.RouterBodiesPath(ctx, domain.EnvLookup{"RADAR_HOME": radar}), bodiesKey)
	if err != nil {
		t.Fatalf("open the body store: %v", err)
	}
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, side := range []struct{ name, body string }{
		{"input", input}, {"output", output},
	} {
		if err := store.PutBody(ctx, id, side.name, []byte(side.body), at); err != nil {
			t.Fatalf("store the %s body: %v", side.name, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the body store: %v", err)
	}
}

// bodiesKeyed answers the key source a store opens under: the test key's own encoding.
func bodiesKeyed() *fakeKeySource {
	return &fakeKeySource{value: base64.RawStdEncoding.EncodeToString(bodiesKey), ok: true}
}

func TestTheBodiesRouteAnswersTheOwnerARequestsKeptBodies(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	const (
		id     = "b-1"
		input  = `{"model":"test","messages":["hello there"]}`
		output = `{"reply":"hi, all good"}`
	)
	bodiesSeed(t, rig.radar, id, input, output)
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/"+id, "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	answer := bodyMap(t, out)
	if answer["state"] != "kept" {
		t.Errorf("the state = %v, want kept", answer["state"])
	}
	if answer["input"] != input || answer["output"] != output {
		t.Errorf("the bodies = %v %v, want the stored text", answer["input"], answer["output"])
	}
}

func TestTheBodiesRouteAnswersMissingForAnUnknownRequest(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	bodiesSeed(t, rig.radar, "b-1", "in", "out")
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/nope", "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	if answer := bodyMap(t, out); answer["state"] != "missing" {
		t.Errorf("the state = %v, want missing", answer["state"])
	}
}

func TestTheBodiesRouteBelongsToTheOwnerAlone(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	member := bodiesMember(t, rig)
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/b-1", "", member)
	if code != 403 {
		t.Fatalf("a member's read = %d %s", code, out)
	}
	if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "only the owner") {
		t.Errorf("the refusal = %q, want only the owner", msg)
	}
}

func TestTheBodiesRouteAnswersOffWhenNoKeyIsStored(t *testing.T) {
	rig := bodiesServer(t, &fakeKeySource{})
	bodiesSeed(t, rig.radar, "b-1", "in", "out")
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/b-1", "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	if answer := bodyMap(t, out); answer["state"] != "off" {
		t.Errorf("the state = %v, want off", answer["state"])
	}
}

func TestTheBodiesRouteAnswersOffWithoutAStoreAndNeverCreatesOne(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/b-1", "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	if answer := bodyMap(t, out); answer["state"] != "off" {
		t.Errorf("the state = %v, want off", answer["state"])
	}
	if _, err := os.Stat(filepath.Join(rig.radar, "ledger", "router.db")); !os.IsNotExist(err) {
		t.Fatalf("the read left a body store behind: %v", err)
	}
}

func TestTheBodiesRouteRefusesABadId(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	for _, id := range []string{"a/b", ".."} {
		code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies/"+id, "", rig.token)
		if code != 400 {
			t.Fatalf("the id %q reads = %d %s, want 400", id, code, out)
		}
	}
}

func TestTheBodiesRouteAnswersTheOwnerTheKeptNumbers(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	bodiesSeed(t, rig.radar, "b-1", "in", "out")
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies", "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	answer := bodyMap(t, out)
	if answer["state"] != "kept" {
		t.Errorf("the state = %v, want kept", answer["state"])
	}
	if requests, _ := answer["requests"].(float64); requests != 1 {
		t.Errorf("the requests = %v, want the one stored request", answer["requests"])
	}
	if disk, _ := answer["bytes"].(float64); disk <= 0 {
		t.Errorf("the bytes = %v, want the store's own disk", answer["bytes"])
	}
}

func TestTheBodiesNumbersBelongToTheOwnerAlone(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	member := bodiesMember(t, rig)
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies", "", member)
	if code != 403 {
		t.Fatalf("a member's read = %d %s", code, out)
	}
	if msg, _ := bodyMap(t, out)["error"].(string); !strings.Contains(msg, "only the owner") {
		t.Errorf("the refusal = %q, want only the owner", msg)
	}
}

func TestTheBodiesNumbersAnswerOffWithoutAStoreAndNeverCreateOne(t *testing.T) {
	rig := bodiesServer(t, bodiesKeyed())
	code, out := bodiesCall(t, rig, http.MethodGet, "/api/bodies", "", rig.token)
	if code != 200 {
		t.Fatalf("the owner's read = %d %s", code, out)
	}
	answer := bodyMap(t, out)
	if answer["state"] != "off" {
		t.Errorf("the state = %v, want off", answer["state"])
	}
	if answer["requests"] != float64(0) || answer["bytes"] != float64(0) {
		t.Errorf("the numbers = %v %v, want zeros", answer["requests"], answer["bytes"])
	}
	if _, err := os.Stat(filepath.Join(rig.radar, "ledger", "router.db")); !os.IsNotExist(err) {
		t.Fatalf("the read left a body store behind: %v", err)
	}
}
