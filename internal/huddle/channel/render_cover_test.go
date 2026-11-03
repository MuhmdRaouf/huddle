package channel

// How the channel's answers read as text, and the refusals its writes give, on the paths the
// happy walk never takes.

import (
	"strings"
	"testing"
)

func TestTheMapReadsPhaseProgressSessionsAndTheCriticalPath(t *testing.T) {
	one := int64(1)
	orch := "lead"
	view := MapView{
		Title: "Ship", Orchestrator: &orch, Done: 1, Total: 2,
		Phases: []PhaseStat{{N: &one, Title: "Build", Done: 1, Total: 1}, {Title: "Release", Done: 0, Total: 1}},
		Sessions: []MapSession{
			{Name: "a", State: "working", Current: &TaskRef{ID: "t1", Title: "Build it"}, Next: &TaskRefLite{ID: "t2", Ready: false}},
			{Name: "b", State: "idle"},
		},
		CriticalPath: []CriticalTask{{ID: "t2", Title: "Release it", Status: "todo"}},
	}
	got := mapText(view)
	for _, want := range []string{
		"1 ✓ 1/1  Build",
		"  - " + "  0/1  Release",
		"a (working): doing t1 Build it · next t2 (waits)",
		"b (idle): nothing in progress",
		"critical path (1 open task in a chain): ",
		"  t2 [todo] Release it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the map lacks %q:\n%s", want, got)
		}
	}
	if empty := mapText(MapView{Title: "Ship"}); !strings.Contains(empty, "critical path (0 open tasks in a chain): none") {
		t.Errorf("a map with no open task = %q, want the chain reported as none", empty)
	}
}

func TestASnapshotOfAPausedSessionSaysToCallGate(t *testing.T) {
	text := snapText(Snapshot{Channel: "shop", Me: "a", Control: "pause"})
	if !strings.Contains(text, "channel shop · you are a · PAUSED: call gate before any work") {
		t.Errorf("the snapshot = %q", text)
	}
	running := snapText(Snapshot{Channel: "shop", Me: "a", Control: "run", Sessions: []OtherSession{
		{Name: "b", State: "working", Control: "pause", Stale: true},
	}})
	if !strings.Contains(running, "b(paused) working") || !strings.Contains(running, "[stale]") {
		t.Errorf("the other sessions = %q, want b paused and stale", running)
	}
}

func TestAKnowledgeEntryReadsWithItsTaskAndWhereItMoved(t *testing.T) {
	task, moved := "t1", int64(100001)
	text := kbText(KBFull{
		KBView:  KBView{ID: 7, Kind: "fact", Title: "the box has 64 GB", By: "a", CreatedAt: "2026-10-16T09:00:00Z", Body: "plenty", Task: &task},
		MovedTo: &moved,
	})
	if !strings.Contains(text, " · task t1") || !strings.Contains(text, " · now for every channel as #100001") || !strings.HasSuffix(text, "\n\nplenty") {
		t.Errorf("the entry = %q", text)
	}
	if got := kbText("not an entry"); got != "" {
		t.Errorf("a foreign answer = %q, want nothing", got)
	}
}

func TestAnExportOfAnEmptyChannelSaysNothingIsRemembered(t *testing.T) {
	ch := joinedChannel(t)
	all, err := ExportMarkdown(bg, ch, ExportOpts{})
	if err != nil || !strings.Contains(all, "_Nothing remembered yet._") || !strings.Contains(all, "0 entries") {
		t.Errorf("export = %q (%v)", all, err)
	}
	verified, err := ExportMarkdown(bg, ch, ExportOpts{Verified: true})
	if err != nil || !strings.Contains(verified, "_Nothing verified yet._") || !strings.Contains(verified, "verified entries only") {
		t.Errorf("verified export = %q (%v)", verified, err)
	}
}

func TestARememberedBodyPastFiftyThousandCharactersIsRefused(t *testing.T) {
	ch := joinedChannel(t)
	_, err := Remember(bg, ch, "a", RememberArgs{Kind: "fact", Title: "too long", Body: strings.Repeat("x", 50001)})
	if errStatus(err) != 400 {
		t.Errorf("an oversized body = %v, want a 400", err)
	}
}

func TestReadingAnUnknownKnowledgeEntryIsA404(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := KB(bg, ch, 9999); errStatus(err) != 404 {
		t.Errorf("an unknown entry = %v, want a 404", err)
	}
}

func TestPublishRefusesATopicWithAWildcard(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "build.*", Msg: "x"}); errStatus(err) != 400 {
		t.Errorf("a wildcard topic = %v, want a 400", err)
	}
}

func TestOnlyTheAddresseeAnswersAMessage(t *testing.T) {
	ch := threeMembers(t)
	msg, err := ch.Send(bg, "a", []string{"b"}, "hello", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Reply(bg, "c", msg.Seq, "hi"); errStatus(err) != 403 {
		t.Errorf("a bystander's reply = %v, want a 403", err)
	}
	reply, err := ch.Reply(bg, "b", msg.Seq, "")
	if err != nil || deref(reply.Msg) != "ack" {
		t.Errorf("an empty reply = %+v (%v), want ack", reply, err)
	}
}

func TestPassingTheTurnWithoutAMessageSaysWhoHandsItTo(t *testing.T) {
	ch := joinedChannel(t)
	ev, err := ch.PassTurn(bg, "a", "b", "")
	if err != nil || deref(ev.Msg) != "a hands the turn to b" {
		t.Errorf("pass = %+v (%v), want the default line", ev, err)
	}
}
