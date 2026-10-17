package cli

// The huddle word's quiet edges: the flag readers' nil and bare shapes, the small JSON helpers,
// the admin calls' refusals, the token and join words over a scripted server, and the waits'
// answer shapes.

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestFlagsAnswerTheirNilAndBareShapes pins the flag readers: a nil flag set has nothing, a
// valueless flag reads as true through str and never as text, and a body that reads a missing
// flag stays empty.
func TestFlagsAnswerTheirNilAndBareShapes(t *testing.T) {
	gone := (*flags)(nil)
	if gone.has("x") {
		t.Error("has")
	}
	if _, ok := gone.value("x"); ok {
		t.Error("value")
	}
	if _, ok := gone.text("x"); ok {
		t.Error("text")
	}
	if _, ok := gone.str("x"); ok {
		t.Error("str")
	}
	parsed, _ := parseFlags([]string{"--empty"})
	if got, ok := parsed.str("empty"); !ok || got != "true" {
		t.Errorf("str over a bare flag = (%q, %v)", got, ok)
	}
	if _, ok := parsed.text("empty"); ok {
		t.Error("text over a bare flag")
	}
	valued, _ := parseFlags([]string{"--topic", "task.ready"})
	if got, ok := valued.text("topic"); !ok || got != "task.ready" {
		t.Errorf("text over a valued flag = (%q, %v)", got, ok)
	}
	if fields := newBody().flagValue(gone, "k").build(); len(fields.Keys()) != 0 {
		t.Errorf("flagValue over a nil set = %v", fields.Keys())
	}
	flagged := newBody().flagValue(parsed, "empty").build()
	if len(flagged.Keys()) != 1 || flagged.Fields()[0].EncodeJSON() != "true" {
		t.Errorf("flagValue over a bare flag = %v", flagged.EncodeJSON())
	}
}

// TestErrorMessageSaysTheServerErrorOrTheStatus pins the refusal's wording: the server's error
// field when it gave one, the bare status when it did not.
func TestErrorMessageSaysTheServerErrorOrTheStatus(t *testing.T) {
	spoken, _ := jsfmt.DecodeValue([]byte(`{"error": "no such channel"}`))
	if got := errorMessage(spoken, http.StatusForbidden); got != "no such channel" {
		t.Errorf("errorMessage(spoken) = %q", got)
	}
	silent, _ := jsfmt.DecodeValue([]byte(`{"detail": "nothing"}`))
	if got := errorMessage(silent, http.StatusBadGateway); got != "HTTP 502" {
		t.Errorf("errorMessage(silent) = %q", got)
	}
	if got := (&cli{}).resultJSON(answer{}); got != "null" {
		t.Errorf("resultJSON(nil) = %q", got)
	}
}

// TestTheSmallJSONHelpersAnswerTheirZeros pins the readers over values that name nothing: a nil
// value, a value of the wrong kind, and an event that is not an object.
func TestTheSmallJSONHelpersAnswerTheirZeros(t *testing.T) {
	if got := jsNumberOrNull("tomorrow"); got.EncodeJSON() != "null" {
		t.Errorf("jsNumberOrNull(word) = %s", got.EncodeJSON())
	}
	if got := jsNumberOrNull("12.5"); got.EncodeJSON() != "12.5" {
		t.Errorf("jsNumberOrNull(12.5) = %s", got.EncodeJSON())
	}
	if got := textField(nil, "k"); got != "" {
		t.Errorf("textField(nil) = %q", got)
	}
	if got := boolFieldText(nil, "k"); got != "" {
		t.Errorf("boolFieldText(nil) = %q", got)
	}
	value, err := jsfmt.DecodeValue([]byte(`{"flag": "yes"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := boolFieldText(value, "flag"); got != "" {
		t.Errorf("boolFieldText(string) = %q", got)
	}
	if got := arrayItems(value); got != nil {
		t.Errorf("arrayItems(object) = %v", got)
	}
	if got := numOf(nil, "seq"); got != 0 {
		t.Errorf("numOf(nil) = %v", got)
	}
	if got := numOf(value, "flag"); got != 0 {
		t.Errorf("numOf(string) = %v", got)
	}
	if got := withChannel("c", nil).EncodeJSON(); got != `{"channel":"c"}` {
		t.Errorf("withChannel(nil) = %s", got)
	}
}

// TestTheAdminCallsRefuseWhatTheyCannotReach pins the creator's calls: no port, an unreachable
// huddle, an answer that is not JSON, and a refusal all say so with the right exit.
func TestTheAdminCallsRefuseWhatTheyCannotReach(t *testing.T) {
	quiet := huddleWorld(t, "", map[string]string{"HUDDLE_URL": ""})
	if code, _, errOut := runH(t, quiet, "token", "list"); code != 5 || !strings.Contains(errOut, "no port here yet") {
		t.Errorf("token list without a port = (%d, %q)", code, errOut)
	}
	if code, _, _ := runH(t, quiet, "token", "create"); code != 5 {
		t.Errorf("token create without a port = %d", code)
	}
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Path == "/api/tokens" && call.Method == http.MethodGet:
			return 200, "not json at all"
		case strings.HasSuffix(call.Path, "/op/map"):
			return 200, `{"result": null}`
		default:
			return 404, `{}`
		}
	})
	if code, out, _ := runH(t, huddleWorld(t, fake.server.URL, nil), "token", "list"); code != 0 || !strings.Contains(out, "no tokens") {
		t.Errorf("token list over garbage = (%d, %q)", code, out)
	}
	if code, out, _ := runH(t, huddleWorld(t, fake.server.URL, nil), "map"); code != 0 || !strings.Contains(out, "null") {
		t.Errorf("map over a null result = (%d, %q)", code, out)
	}
	bad := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/tokens" {
			return http.StatusInternalServerError, "not json at all"
		}
		return 404, `{}`
	})
	if code, _, errOut := runH(t, huddleWorld(t, bad.server.URL, nil), "token", "list"); code != 2 || !strings.Contains(errOut, "HTTP 500") {
		t.Errorf("token list over a bare status = (%d, %q)", code, errOut)
	}
	spoken := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return http.StatusInternalServerError, `{"error": "the store is closed"}`
	})
	if code, _, errOut := runH(t, huddleWorld(t, spoken.server.URL, nil), "token", "list"); code != 2 || !strings.Contains(errOut, "the store is closed") {
		t.Errorf("token list over a refusal = (%d, %q)", code, errOut)
	}
}

// TestTheTokenWordRefusesItsBadShapes pins the token word's usage errors: a delete without an
// id, an unknown sub-word, and a ttl that is no time.
func TestTheTokenWordRefusesItsBadShapes(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, `{"result": []}` })
	w := huddleWorld(t, fake.server.URL, nil)
	if code, _, errOut := runH(t, w, "token", "delete"); code != 2 || !strings.Contains(errOut, "token delete <id>") {
		t.Errorf("token delete without an id = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runH(t, w, "token", "sideways"); code != 2 || !strings.Contains(errOut, "token create|list|delete") {
		t.Errorf("token sideways = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runH(t, w, "token", "create", "--ttl", "tomorrow"); code != 2 || !strings.Contains(errOut, "--ttl tomorrow") {
		t.Errorf("token create with a bad ttl = (%d, %q)", code, errOut)
	}
	if code, _, _ := runH(t, w, "kick"); code != 2 {
		t.Errorf("kick without a name = %d", code)
	}
}

// TestTheTokenWordPrintsTheJoinLineAndTheExpiry pins the invite's two printings: the line
// another session pastes in (with how long it lasts), and the minted token with its expiry.
func TestTheTokenWordPrintsTheJoinLineAndTheExpiry(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/tokens" {
			return 200, `{"token": "t1.secret", "id": "t1", "expires": "2026-10-17T12:00:00Z"}`
		}
		return 404, `{}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, errOut := runH(t, w, "token", "create", "--print_join_command", "--single_use")
	if code != 0 || !strings.Contains(out, "join: /huddle:join") || !strings.Contains(out, "until 2026-10-17T12:00:00Z") || !strings.Contains(out, "single use") {
		t.Errorf("the join line = (%d, %q %q)", code, out, errOut)
	}
	code, out, _ = runH(t, huddleWorld(t, fake.server.URL, nil), "token", "create")
	if code != 0 || !strings.Contains(out, "t1.secret") || !strings.Contains(out, "expires 2026-10-17T12:00:00Z") {
		t.Errorf("the minted token = (%d, %q)", code, out)
	}
}

// TestTheTokenWordSaysWhyNothingWasMinted pins the invite's refusal: an unreachable huddle and a
// refused mint both say why with exit 2.
func TestTheTokenWordSaysWhyNothingWasMinted(t *testing.T) {
	w := huddleWorld(t, "http://127.0.0.1:1", nil)
	if code, _, errOut := runH(t, w, "token", "create", "--print_join_command"); code != 2 || errOut == "" {
		t.Errorf("the join line over an unreachable huddle = (%d, %q)", code, errOut)
	}
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return http.StatusForbidden, `{"error": "only the creator may invite"}`
	})
	if code, _, errOut := runH(t, huddleWorld(t, fake.server.URL, nil), "token", "create"); code != 2 || !strings.Contains(errOut, "only the creator may invite") {
		t.Errorf("a refused mint = (%d, %q)", code, errOut)
	}
}

// TestTheJoinWordRefusesItsBadShapes pins the join word's usage errors: a token with no host
// and a host with no token, and an unreachable huddle exits 5.
func TestTheJoinWordRefusesItsBadShapes(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 404, `{}` })
	w := huddleWorld(t, fake.server.URL, nil)
	if code, _, errOut := runH(t, w, "join", "--token", "t.secret"); code != 2 || !strings.Contains(errOut, "join <host:port> --token") {
		t.Errorf("join without a host = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runH(t, w, "join", "127.0.0.1:1"); code != 2 || !strings.Contains(errOut, "join <host:port> --token") {
		t.Errorf("join without a token = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runH(t, w, "join", "127.0.0.1:1", "--token", "t.secret"); code != 5 || !strings.Contains(errOut, "unreachable") {
		t.Errorf("join over an unreachable huddle = (%d, %q)", code, errOut)
	}
}

// TestTheJoinWordKeepsTheSessionsNameAndTakesTheInvitesChannel pins a joined session: the name
// it already holds is asked again, the invite's channel wins over the project's, the new
// credential is kept (never the invite), and the switch is said out loud.
func TestTheJoinWordKeepsTheSessionsNameAndTakesTheInvitesChannel(t *testing.T) {
	ctx := context.Background()
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Path == "/api/join":
			return 200, `{"credential": "hcred_new", "channel": "invited-c", "name": "renamed"}`
		case strings.HasSuffix(call.Path, "/op/join"):
			return 200, `{"result": {"seq": 1}}`
		default:
			return 404, `{}`
		}
	})
	url := fake.server.URL
	w := huddleWorld(t, url, nil)
	if err := SaveCred(ctx, w, Cred{URL: url, Channel: "old-c", As: "kept-name", Credential: "hcred_old"}, ""); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runH(t, w, "join", strings.TrimPrefix(url, "http://"), "--token", "t.secret")
	if code != 0 {
		t.Fatalf("join = (%d, %q %q)", code, out, errOut)
	}
	if !strings.Contains(errOut, "joined channel invited-c (the invite's); this project's file named c") {
		t.Errorf("the channel switch was not said:\n%s", errOut)
	}
	if !strings.Contains(errOut, "joined "+url+" as renamed") {
		t.Errorf("the join was not said:\n%s", errOut)
	}
	cred := LoadCred(ctx, w, url, "")
	if cred == nil || cred.Credential != "hcred_new" || cred.Channel != "invited-c" || cred.As != "renamed" {
		t.Errorf("the saved credential = %+v", cred)
	}
}

// TestTheJoinWordKeepsTheFileChannelWhenTheAnswerNamesNone pins the quieter joins: an answer
// that is not JSON still joins through the environment's channel, and a refusal says why.
func TestTheJoinWordKeepsTheFileChannelWhenTheAnswerNamesNone(t *testing.T) {
	garbage := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Path == "/api/join":
			return 200, "not json at all"
		case strings.HasSuffix(call.Path, "/op/join"):
			return 200, `{"result": {"seq": 1}}`
		default:
			return 404, `{}`
		}
	})
	code, _, errOut := runH(t, huddleWorld(t, garbage.server.URL, nil), "join", garbage.server.URL, "--token", "t.secret")
	if code != 0 || !strings.Contains(errOut, "as a") {
		t.Errorf("join over a garbage answer = (%d, %q)", code, errOut)
	}
	refused := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return http.StatusForbidden, `{"error": "unknown invite"}`
	})
	code, _, errOut = runH(t, huddleWorld(t, refused.server.URL, nil), "join", refused.server.URL, "--token", "t.secret")
	if code != 2 || !strings.Contains(errOut, "unknown invite") {
		t.Errorf("a refused join = (%d, %q)", code, errOut)
	}
}

// TestTheJoinWordNamesTheWishItSends pins the name a join asks for: the environment's name when
// the session holds none of its own, and the project's slug when the environment has neither —
// both wishes, marked so the server makes them unique.
func TestTheJoinWordNamesTheWishItSends(t *testing.T) {
	asked := []string{}
	var mu sync.Mutex
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/join" {
			mu.Lock()
			asked = append(asked, call.Body)
			mu.Unlock()
			return 200, `{"credential": "hcred_new", "channel": "invited-c"}`
		}
		if strings.HasSuffix(call.Path, "/op/join") {
			return 200, `{"result": {"seq": 1}}`
		}
		return 404, `{}`
	})
	if code, _, _ := runH(t, huddleWorld(t, fake.server.URL, nil), "join", fake.server.URL, "--token", "t.secret"); code != 0 {
		t.Error("join with a session name failed")
	}
	if code, _, _ := runH(t, huddleWorld(t, fake.server.URL, map[string]string{
		"HUDDLE_AS": "", "CLAUDE_PROJECT_DIR": filepath.Join(t.TempDir(), "My Project"),
	}), "join", fake.server.URL, "--token", "t.secret"); code != 0 {
		t.Error("join without a name failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("the server saw %d joins: %v", len(asked), asked)
	}
	if !strings.Contains(asked[0], `"name":"a"`) || !strings.Contains(asked[0], `"unique":true`) {
		t.Errorf("the environment's name was not the wish: %s", asked[0])
	}
	if !strings.Contains(asked[1], `"unique":true`) || !strings.Contains(asked[1], `"name":"my-project"`) {
		t.Errorf("the project's slug was not the wish: %s", asked[1])
	}
}

// TestOpsSaysUnreachableWhenTheToolsCannotBeRead pins the ops word over an answer that is not
// JSON: unreachable, not a crash.
func TestOpsSaysUnreachableWhenTheToolsCannotBeRead(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if strings.HasSuffix(call.Path, "/mcp/c") {
			return 200, "not json at all"
		}
		return 404, `{}`
	})
	if code, _, errOut := runH(t, huddleWorld(t, fake.server.URL, nil), "ops"); code != 5 || !strings.Contains(errOut, "unreachable") {
		t.Errorf("ops over garbage = (%d, %q)", code, errOut)
	}
}

// TestWaitKeepsWaitingThroughATimeoutAndCountsTheRest pins the wait loop: a server timeout
// waits again, an event ends with 0, anything else ends with 124.
func TestWaitKeepsWaitingThroughATimeoutAndCountsTheRest(t *testing.T) {
	var mu sync.Mutex
	waits := 0
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if strings.HasSuffix(call.Path, "/op/wait") {
			mu.Lock()
			waits++
			count := waits
			mu.Unlock()
			if count == 1 {
				return 200, `{"result": {"kind": "timeout"}}`
			}
			return 200, `{"result": {"kind": "event", "seq": 4}}`
		}
		return 404, `{}`
	})
	code, out, _ := runH(t, huddleWorld(t, fake.server.URL, nil), "wait", "--timeout", "5")
	if code != 0 || !strings.Contains(out, `"kind":"event"`) {
		t.Errorf("wait through a timeout = (%d, %q)", code, out)
	}
	other := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, `{"result": {"kind": "silence"}}`
	})
	if code, _, _ := runH(t, huddleWorld(t, other.server.URL, nil), "wait", "--timeout", "5"); code != 124 {
		t.Errorf("wait over an unknown kind = %d", code)
	}
}

// TestGateSaysPausedForAnUnreachableHuddleAndRefusesWhenRefused pins the gate: an unreachable
// huddle counts as paused and says so, a refused gate names the refusal, and --no_block
// answers 4 when the channel is paused.
func TestGateSaysPausedForAnUnreachableHuddleAndRefusesWhenRefused(t *testing.T) {
	refused := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if strings.HasSuffix(call.Path, "/op/gate") {
			return 200, `{"error": "the channel is paused"}`
		}
		return 404, `{}`
	})
	if code, _, errOut := runH(t, huddleWorld(t, refused.server.URL, nil), "gate"); code != 2 || !strings.Contains(errOut, "the channel is paused") {
		t.Errorf("a refused gate = (%d, %q)", code, errOut)
	}
	paused := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, `{"result": {"control": "paused"}}`
	})
	if code, _, _ := runH(t, huddleWorld(t, paused.server.URL, nil), "gate", "--no_block"); code != 4 {
		t.Errorf("a paused gate with --no_block = %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut strings.Builder
	if code := Huddle(ctx, huddleWorld(t, "http://127.0.0.1:1", nil), []string{"gate"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "unreachable; treated as paused") {
		t.Errorf("a gate over an unreachable huddle = (%d, %q)", code, errOut.String())
	}
}

// TestTheOpShapesReachTheServerAsTheyWereTyped pins the operation bodies' extra shapes: a task
// spelled as words, --sync, a context from the environment, an assign without a context flag,
// a handoff that does not wait, and a remember that supersedes.
func TestTheOpShapesReachTheServerAsTheyWereTyped(t *testing.T) {
	type seen struct{ op, body string }
	var mu sync.Mutex
	var bodies []seen
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if at := strings.LastIndex(call.Path, "/op/"); at >= 0 {
			mu.Lock()
			bodies = append(bodies, seen{op: call.Path[at+4:], body: call.Body})
			mu.Unlock()
			return 200, `{"result": {"seq": 9}}`
		}
		return 404, `{}`
	})
	url := fake.server.URL
	if code, _, _ := runH(t, huddleWorld(t, url, nil), "join", "watch the gates", "--sync"); code != 0 {
		t.Error("join failed")
	}
	if code, _, _ := runH(t, huddleWorld(t, url, map[string]string{"HUDDLE_CONTEXT": "sync"}), "join"); code != 0 {
		t.Error("join with the file's context failed")
	}
	if code, _, _ := runH(t, huddleWorld(t, url, nil), "assign", "s-9"); code != 0 {
		t.Error("assign failed")
	}
	if code, _, _ := runH(t, huddleWorld(t, url, nil), "handoff", "other", "over to you", "--no_wait"); code != 0 {
		t.Error("handoff failed")
	}
	if code, _, _ := runH(t, huddleWorld(t, url, nil), "remember", "decision", "the gates", "watch them", "--supersedes", "12", "--force"); code != 0 {
		t.Error("remember failed")
	}
	mu.Lock()
	defer mu.Unlock()
	var joins, assign, handoff, remember []string
	for _, one := range bodies {
		switch one.op {
		case "join":
			joins = append(joins, one.body)
		case "assign":
			assign = append(assign, one.body)
		case "handoff":
			handoff = append(handoff, one.body)
		case "remember":
			remember = append(remember, one.body)
		}
	}
	if len(joins) != 2 {
		t.Fatalf("the server saw %d joins", len(joins))
	}
	if !strings.Contains(joins[0], `"task":"watch the gates"`) || !strings.Contains(joins[0], `"context":"sync"`) {
		t.Errorf("the join with words and --sync = %s", joins[0])
	}
	if !strings.Contains(joins[1], `"context":"sync"`) || strings.Contains(joins[1], `"task"`) {
		t.Errorf("the join with the file's context = %s", joins[1])
	}
	if len(assign) == 0 || !strings.Contains(assign[0], `"context":null`) {
		t.Errorf("the assign body = %v", assign)
	}
	if len(handoff) == 0 || !strings.Contains(handoff[0], `"wait":false`) {
		t.Errorf("the handoff body = %v", handoff)
	}
	if len(remember) == 0 || !strings.Contains(remember[0], `"supersedes":12`) || !strings.Contains(remember[0], `"force":true`) {
		t.Errorf("the remember body = %v", remember)
	}
}

// TestRawTimelineReadsAChannelsEventsOrNothing pins the timeline reader: an unreachable huddle,
// a refusal and a body that is not an array all read as nothing.
func TestRawTimelineReadsAChannelsEventsOrNothing(t *testing.T) {
	ctx := context.Background()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	gone := &cli{url: "http://127.0.0.1:1", w: w, ch: "c", me: "a"}
	if events, ok := gone.rawTimeline(ctx, "c", 0, 10); ok || events != nil {
		t.Errorf("rawTimeline over an unreachable huddle = (%v, %v)", events, ok)
	}
	bad := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return http.StatusInternalServerError, `{}`
	})
	refused := &cli{url: bad.server.URL, w: w, ch: "c", me: "a"}
	if events, ok := refused.rawTimeline(ctx, "c", 0, 10); ok || events != nil {
		t.Errorf("rawTimeline over a refusal = (%v, %v)", events, ok)
	}
	odd := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if strings.Contains(call.Path, "/timeline") {
			return 200, `{"not": "an array"}`
		}
		return 404, `{}`
	})
	weird := &cli{url: odd.server.URL, w: w, ch: "c", me: "a"}
	if events, ok := weird.rawTimeline(ctx, "c", 0, 10); ok || events != nil {
		t.Errorf("rawTimeline over an object = (%v, %v)", events, ok)
	}
}
