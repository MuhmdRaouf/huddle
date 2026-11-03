package channel

// The operations as a transport calls them: what each answers, in its result and in the text an
// agent reads, on the paths a walk through the happy case never takes.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// callOp runs one operation and answers its result; the call must succeed.
func callOp(t *testing.T, ch *Channel, name, me, args string) OpResult {
	t.Helper()
	var v *jsfmt.Value
	if args != "" {
		v = vv(t, args)
	}
	res, err := RunOp(bg, name, ch, me, v)
	if err != nil {
		t.Fatalf("op %s as %s: %v", name, me, err)
	}
	return res
}

// opText answers the text an agent reads for one successful call.
func opText(t *testing.T, ch *Channel, name, me, args string) string {
	t.Helper()
	res := callOp(t, ch, name, me, args)
	if res.Text == nil {
		t.Fatalf("op %s has no text", name)
	}
	return *res.Text
}

// opRefusal runs a call that must be refused and answers its HTTP status.
func opRefusal(t *testing.T, ch *Channel, name, me, args string) int {
	t.Helper()
	var v *jsfmt.Value
	if args != "" {
		v = vv(t, args)
	}
	_, err := RunOp(bg, name, ch, me, v)
	if err == nil {
		t.Fatalf("op %s as %s succeeded, want a refusal", name, me)
	}
	return errStatus(err)
}

func TestTheOwnerRemovesASessionThroughTheRemoveOp(t *testing.T) {
	ch := joinedChannel(t)
	if got := opRefusal(t, ch, "remove", "a", `{"target":"b"}`); got != 403 {
		t.Errorf("a session removing another = %d, want 403", got)
	}
	callOp(t, ch, "remove", Owner, `{"target":"b","why":"stuck"}`)
	s, err := ch.Session(bg, "b")
	if err != nil || s == nil || s.State != "left" {
		t.Errorf("b after the removal = %+v (%v), want left", s, err)
	}
}

func TestGateWithoutBlockingReportsThePauseAtOnce(t *testing.T) {
	ch := joinedChannel(t)
	callOp(t, ch, "pause", "b", `{"target":"a","why":"hold on"}`)
	if got := mustJSONT(t, callOp(t, ch, "gate", "a", `{"block":false}`).Result); got != `{"control":"pause"}` {
		t.Errorf("a paused session's gate = %s, want the pause", got)
	}
	callOp(t, ch, "resume", "b", `{"target":"a"}`)
	if got := mustJSONT(t, callOp(t, ch, "gate", "a", `{"block":false}`).Result); got != `{"control":"run"}` {
		t.Errorf("a resumed session's gate = %s, want run", got)
	}
}

func TestStartOnAPausedSessionStartsNothingAndSaysToGate(t *testing.T) {
	ch := joinedChannel(t)
	callOp(t, ch, "task_create", "a", `{"id":"t1","title":"Ship the board","owner":"a"}`)
	callOp(t, ch, "pause", "b", `{"target":"a","why":"hold on"}`)

	res := callOp(t, ch, "start", "a", `{}`)
	if !strings.Contains(mustJSONT(t, res.Result), `"paused":true`) || deref(res.Text) != "PAUSED: call gate before any work" {
		t.Errorf("start = %s / %q, want the paused answer", mustJSONT(t, res.Result), deref(res.Text))
	}
	if task, err := ch.Task(bg, "t1"); err != nil || task.Status == "doing" {
		t.Errorf("t1 after a refused start = %+v (%v), want it untouched", task, err)
	}
}

func TestTheTaskListReadsAsOneLinePerTaskWithWhatItWaitsOn(t *testing.T) {
	ch := joinedChannel(t)
	if got := opText(t, ch, "tasks", "a", ``); got != "(no tasks)" {
		t.Errorf("an empty plan = %q, want (no tasks)", got)
	}
	callOp(t, ch, "task_create", "a", `{"id":"t1","title":"Write the brief"}`)
	callOp(t, ch, "task_create", "a", `{"id":"t2","title":"Ship the board","owner":"b","after":["t1"]}`)
	lines := strings.Split(opText(t, ch, "tasks", "a", ``), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "t1 [") || !strings.HasSuffix(lines[0], "Write the brief") {
		t.Fatalf("the task lines = %q", lines)
	}
	if !strings.Contains(lines[1], "@b Ship the board") || !strings.HasSuffix(lines[1], "waits on t1") {
		t.Errorf("t2's line = %q, want its owner and what it waits on", lines[1])
	}
}

func TestATaskReadsWithItsChecksAndAnUnknownOneIsRefused(t *testing.T) {
	ch := joinedChannel(t)
	callOp(t, ch, "task_create", "a", `{"id":"t1","title":"Ship the board","what":"the board","verify":["go test ./..."]}`)
	text := opText(t, ch, "task", "a", `{"id":"t1"}`)
	if !strings.Contains(text, "CHECK\n  $ go test ./...") {
		t.Errorf("the task text = %q, want its check listed", text)
	}
	if got := opRefusal(t, ch, "task", "a", `{"id":"nope"}`); got != 404 {
		t.Errorf("an unknown task = %d, want 404", got)
	}
}

func TestNextSaysWhenEveryTaskIsDone(t *testing.T) {
	ch := joinedChannel(t)
	if got := opText(t, ch, "next", "a", ``); got != "every task you can take is done or skipped" {
		t.Errorf("next with no plan = %q", got)
	}
}

func TestAskWaitNeedsRecipientsAndAMessageOrAnEarlierAsk(t *testing.T) {
	ch := joinedChannel(t)
	for _, args := range []string{`{}`, `{"to":["b"]}`, `{"msg":"which port?"}`} {
		if got := opRefusal(t, ch, "ask_wait", "a", args); got != 400 {
			t.Errorf("ask_wait %s = %d, want 400", args, got)
		}
	}
}

func TestTheOwnerClosesAnAskThroughAskClose(t *testing.T) {
	ch, group := askedByLead(t)
	if got := opRefusal(t, ch, "ask_close", "lead", `{"asked":`+seqText(group)+`}`); got != 403 {
		t.Errorf("a session closing an ask = %d, want 403", got)
	}
	callOp(t, ch, "ask_close", Owner, `{"asked":`+seqText(group)+`}`)
	res := callOp(t, ch, "ask_wait", "lead", `{"asked":`+seqText(group)+`,"timeout":5}`)
	if !strings.Contains(mustJSONT(t, res.Result), `"closed":true`) {
		t.Errorf("ask_wait on a closed ask = %s, want closed", mustJSONT(t, res.Result))
	}
}

func seqText(n int64) string { return strconv.FormatInt(n, 10) }

func TestRememberAndShareReadAsTheirOneLine(t *testing.T) {
	hub := NewHub(t.TempDir(), true)
	t.Cleanup(func() { _ = hub.Close(bg) })
	ch, err := hub.Get(bg, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	text := opText(t, ch, "remember", "a", `{"kind":"fact","title":"the box has 64 GB","body":"plenty"}`)
	if !strings.HasSuffix(text, " remembered: the box has 64 GB") || !strings.HasPrefix(text, "#") {
		t.Fatalf("remember = %q", text)
	}
	id := strings.TrimPrefix(strings.SplitN(text, " ", 2)[0], "#")

	shared := opText(t, ch, "share", "a", `{"id":`+id+`}`)
	if !strings.Contains(shared, "is for every channel now (was #"+id+"): the box has 64 GB") {
		t.Errorf("share = %q", shared)
	}
	if got := opRefusal(t, ch, "share", "a", `{"id":9999}`); got != 404 {
		t.Errorf("sharing an unknown entry = %d, want 404", got)
	}
	again := opText(t, ch, "share", "a", `{"id":`+id+`}`)
	if !strings.Contains(again, "is for every channel now:") {
		t.Errorf("sharing an entry that already moved = %q, want the shared entry", again)
	}
}

func TestRecallStopsAtTheLimitItIsGiven(t *testing.T) {
	ch := joinedChannel(t)
	for _, title := range []string{"first fact", "second fact", "third fact"} {
		callOp(t, ch, "remember", "a", `{"kind":"fact","title":"`+title+`","body":"body of `+title+`"}`)
	}
	hits, ok := callOp(t, ch, "recall", "b", `{"q":"fact","limit":2}`).Result.([]KBShape)
	if !ok || len(hits) != 2 {
		t.Fatalf("recall with a limit of 2 = %#v, want two entries", hits)
	}
}

func TestAGateThatIsNotPausedOpensAtOnceWhateverTheTimeout(t *testing.T) {
	ch := joinedChannel(t)
	if got := mustJSONT(t, callOp(t, ch, "gate", "a", `{"timeout":5}`).Result); !strings.Contains(got, `"control":"run"`) {
		t.Errorf("gate = %s, want run", got)
	}
}

func TestAWaitWithNoTopicsWakesForAnyUnreadEvent(t *testing.T) {
	ch := joinedChannel(t)
	callOp(t, ch, "message", "a", `{"topic":"build.ready","msg":"done"}`)
	if got := jsonField(t, callOp(t, ch, "wait", "b", `{"timeout":5}`).Result, "kind"); got != "event" {
		t.Errorf("wait kind = %q, want event", got)
	}
}

func TestAServerWideRememberReadsAsItsOneLine(t *testing.T) {
	hub := NewHub(t.TempDir(), true)
	t.Cleanup(func() { _ = hub.Close(bg) })
	ch, err := hub.Get(bg, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	text := opText(t, ch, "remember", "a", `{"kind":"fact","title":"the box has 64 GB","body":"plenty","scope":"server"}`)
	if !strings.HasSuffix(text, " remembered: the box has 64 GB") {
		t.Errorf("remember = %q", text)
	}
}

func TestATouchFromASessionThatNeverJoinedIsA404(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := Touched(bg, ch, "ghost", TouchArgs{Path: "src/a.go", Repo: "/r/shop"}); errStatus(err) != 404 {
		t.Errorf("a ghost's touch = %v, want a 404", err)
	}
}
