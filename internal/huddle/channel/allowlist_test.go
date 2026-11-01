package channel

// The optional allowlist: members, when set, names the only sessions the channel admits, a
// member's subagent joins under the member, and an empty list admits every signed-in session.

import "testing"

// TestAnEmptyAllowlistAdmitsEverySession pins the open channel: with no members set, and with an
// empty members array, any session joins, and the config answers members as an array either way.
func TestAnEmptyAllowlistAdmitsEverySession(t *testing.T) {
	ch := openChannel(t, "t")
	if cfg := ch.Config(); cfg.Members == nil || len(cfg.Members) != 0 {
		t.Fatalf("a new channel's members = %#v, want an empty array", cfg.Members)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := ch.Join(bg, name, JoinOpts{}); err != nil {
			t.Fatalf("join %s with no members set: %v", name, err)
		}
	}
	if _, err := ch.Configure(bg, ConfigureArgs{Members: vv(t, `[]`)}); err != nil {
		t.Fatal(err)
	}
	if cfg := ch.Config(); cfg.Members == nil || len(cfg.Members) != 0 {
		t.Fatalf("members after [] = %#v, want an empty array", cfg.Members)
	}
	if _, err := ch.Join(bg, "d", JoinOpts{}); err != nil {
		t.Fatalf("join d with members []: %v", err)
	}
}

// TestAnAllowlistRefusesASessionOffIt pins the closed channel: a member joins, its subagent
// joins under the member, and a session off the list is refused with 403 and the members named.
func TestAnAllowlistRefusesASessionOffIt(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := ch.Configure(bg, ConfigureArgs{Members: vv(t, `["a", "b"]`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
		t.Fatalf("member a joining: %v", err)
	}
	snap, err := ch.Join(bg, "a.helper", JoinOpts{})
	if err != nil {
		t.Fatalf("a member's subagent joining: %v", err)
	}
	if snap.Me != "a.helper" {
		t.Fatalf("join(a.helper).me = %q", snap.Me)
	}
	_, err = ch.Join(bg, "stranger", JoinOpts{})
	if errStatus(err) != 403 {
		t.Fatalf("a session off the list = %v, want 403", err)
	}
	if want := "channel t admits a, b (the owner adds members)"; err == nil || err.Error() != want {
		t.Fatalf("the refusal = %v, want %q", err, want)
	}
}
