package channel

import (
	"strings"
	"testing"
)

// The turn ops' refusals and the replay: the rules a removal leans on, pinned here because the
// removal hands a held turn to the owner through them.
func TestTurnRefusalsAndReplay(t *testing.T) {
	t.Run("a turn cannot be passed to a session that never joined", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.PassTurn(bg, "a", "ghost", ""); errStatus(err) != 404 {
			t.Fatalf("status: %d, want 404", errStatus(err))
		}
	})

	t.Run("a paused session cannot pass or take the turn", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Control(bg, "owner", "a", "pause", "hold on"); err != nil {
			t.Fatalf("pause: %v", err)
		}
		if _, err := ch.PassTurn(bg, "a", "b", ""); errStatus(err) != 423 {
			t.Fatalf("pass status: %d, want 423", errStatus(err))
		}
		if _, err := ch.TakeTurn(bg, "a", ""); errStatus(err) != 423 {
			t.Fatalf("take status: %d, want 423", errStatus(err))
		}
	})

	t.Run("a replay rebuilds the holder from the whole history, handover included", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Configure(bg, ConfigureArgs{
			Handover: vv(t, `{"b": {"to": "a", "topics": ["build.done"]}}`),
		}); err != nil {
			t.Fatalf("configure: %v", err)
		}
		if _, err := ch.TakeTurn(bg, "b", ""); err != nil {
			t.Fatalf("take: %v", err)
		}
		if _, err := ch.Publish(bg, "b", PublishArgs{Topic: "build.done", Msg: "up"}); err != nil {
			t.Fatalf("publish: %v", err)
		}
		turn, err := ch.Turn(bg)
		if err != nil {
			t.Fatalf("turn: %v", err)
		}
		if turn.Holder == nil || *turn.Holder != "a" {
			t.Fatalf("handover holder: %v", turn.Holder)
		}
		if err := ch.ReplayTurn(bg); err != nil {
			t.Fatalf("replay: %v", err)
		}
		turn, err = ch.Turn(bg)
		if err != nil {
			t.Fatalf("turn: %v", err)
		}
		if turn.Holder == nil || *turn.Holder != "a" {
			t.Fatalf("replayed holder: %v", turn.Holder)
		}
	})

	t.Run("the sessions text marks the paused and the stale", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Control(bg, "owner", "b", "pause", "hold on"); err != nil {
			t.Fatalf("pause: %v", err)
		}
		res, err := RunOp(bg, "status", ch, "a", nil)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if res.Text == nil || !strings.Contains(*res.Text, "(paused)") {
			t.Fatalf("status text: %v", res.Text)
		}
	})
}
