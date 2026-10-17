package channel

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Conflict warnings (touches): edits recorded as repo-relative paths; a session that edits a file
// another live session edited in the last 30 minutes hears it once per file per window; the panel
// lists both; nothing blocks.
func TestConflicts(t *testing.T) {
	touchChannel := func(t *testing.T) *Channel {
		t.Helper()
		ch := openChannel(t, "t")
		for _, n := range []string{"payments", "cart", "docs"} {
			if _, err := ch.Join(bg, n, JoinOpts{}); err != nil {
				t.Fatal(err)
			}
		}
		return ch
	}
	touch := func(t *testing.T, ch *Channel, me, repo, path string) (TouchResult, error) {
		t.Helper()
		return Touched(bg, ch, me, TouchArgs{Repo: repo, Path: path})
	}
	warnOf := func(r TouchResult) string {
		if r.Warn == nil {
			return ""
		}
		return *r.Warn
	}

	t.Run("the second session to edit a file hears who edited it, once per file per window; the first is not told", func(t *testing.T) {
		ch := touchChannel(t)
		first, err := touch(t, ch, "payments", "/r/shop", "src/cart.ts")
		if err != nil {
			t.Fatal(err)
		}
		if warnOf(first) != "" {
			t.Errorf("first warn = %q, want none", warnOf(first))
		}
		second, err := touch(t, ch, "cart", "/r/shop", "src/cart.ts")
		if err != nil {
			t.Fatal(err)
		}
		w := warnOf(second)
		if !strings.HasPrefix(w, "Huddle: payments edited src/cart.ts ") || !strings.Contains(w, "— coordinate with it") {
			t.Errorf("second warn = %q", w)
		}
		again, err := touch(t, ch, "cart", "/r/shop", "src/cart.ts")
		if err != nil {
			t.Fatal(err)
		}
		if warnOf(again) != "" { // once per window
			t.Errorf("warn again = %q, want once per window", warnOf(again))
		}
		// a subagent's edit is its session's
		sub, err := touch(t, ch, "cart.helper", "/r/shop", "src/cart.ts")
		if err != nil {
			t.Fatal(err)
		}
		if warnOf(sub) != "" {
			t.Errorf("subagent warn = %q, want none", warnOf(sub))
		}
		// the window passed for the warning: told again
		past := isoNow(time.Now().Add(-(WindowMin + 1) * time.Minute))
		if err := ch.Store.Run(bg, "UPDATE touches SET warned_at=? WHERE session='cart'", past); err != nil {
			t.Fatal(err)
		}
		windowPassed, err := touch(t, ch, "cart", "/r/shop", "src/cart.ts")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(warnOf(windowPassed), "payments edited") {
			t.Errorf("warn after the window = %q", warnOf(windowPassed))
		}
		// the first session editing again now hears about the second
		firstAgain, err := touch(t, ch, "payments", "/r/shop", "src/cart.ts")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(warnOf(firstAgain), "cart edited src/cart.ts") {
			t.Errorf("first session's warn = %q", warnOf(firstAgain))
		}
	})

	t.Run("only the same repo, only live sessions, only within the window", func(t *testing.T) {
		ch := touchChannel(t)
		if _, err := touch(t, ch, "payments", "/r/shop", "README.md"); err != nil {
			t.Fatal(err)
		}
		r, err := touch(t, ch, "cart", "/r/other", "README.md")
		if err != nil || warnOf(r) != "" {
			t.Errorf("other repo warn = %q (%v), want none", warnOf(r), err)
		}
		if _, err := ch.Leave(bg, "payments", "done"); err != nil {
			t.Fatal(err)
		}
		r, err = touch(t, ch, "docs", "/r/shop", "README.md")
		if err != nil || warnOf(r) != "" {
			t.Errorf("left session warn = %q (%v), want none", warnOf(r), err)
		}
		if _, err := touch(t, ch, "cart", "/r/shop", "old.ts"); err != nil {
			t.Fatal(err)
		}
		past := isoNow(time.Now().Add(-(WindowMin + 5) * time.Minute))
		if err := ch.Store.Run(bg, "UPDATE touches SET at=? WHERE path='old.ts'", past); err != nil {
			t.Fatal(err)
		}
		r, err = touch(t, ch, "docs", "/r/shop", "old.ts")
		if err != nil || warnOf(r) != "" {
			t.Errorf("out-of-window warn = %q (%v), want none", warnOf(r), err)
		}
	})

	t.Run("the Conflicts panel lists every file two live sessions edited lately, with who and when", func(t *testing.T) {
		ch := touchChannel(t)
		for _, e := range []struct{ me, path string }{
			{"payments", "src/cart.ts"}, {"cart", "src/cart.ts"}, {"docs", "docs/a.md"},
		} {
			if _, err := touch(t, ch, e.me, "/r/shop", e.path); err != nil {
				t.Fatal(err)
			}
		}
		c, err := Conflicts(bg, ch)
		if err != nil {
			t.Fatal(err)
		}
		if c.WindowMin != 30 {
			t.Errorf("window_min = %d, want 30", c.WindowMin)
		}
		if len(c.Conflicts) != 1 {
			t.Fatalf("conflicts = %+v, want one file", c.Conflicts)
		}
		g := c.Conflicts[0]
		if g.RepoName != "shop" || g.Path != "src/cart.ts" {
			t.Errorf("conflict = %+v, want shop src/cart.ts", g)
		}
		names := []string{}
		for _, s := range g.Sessions {
			names = append(names, s.Name)
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"cart", "payments"}) {
			t.Errorf("sessions = %v, want cart and payments", names)
		}
	})

	t.Run("the touched op: the hook's text is the warning; bad paths are refused, contents never asked for", func(t *testing.T) {
		ch := touchChannel(t)
		if _, err := RunOp(bg, "touched", ch, "payments", vv(t, `{"repo": "/r/shop", "path": "a.ts"}`)); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "touched", ch, "cart", vv(t, `{"repo": "/r/shop", "path": "a.ts"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(r.Text), "payments edited a.ts") {
			t.Errorf("touched text = %q, want the warning", deref(r.Text))
		}
		for _, path := range []string{"/etc/passwd", "../up.ts", "a/../../b"} {
			if _, err := RunOp(bg, "touched", ch, "cart", vv(t, `{"repo": "/r/shop", "path": "`+path+`"}`)); err == nil || !strings.Contains(err.Error(), "relative") {
				t.Errorf("path %q error = %v, want a relative-path refusal", path, err)
			}
		}
		// not a tool: agents never see it
		for _, td := range ToolDefs() {
			if td.Name == "touched" {
				t.Error("touched is listed as a tool")
			}
		}
	})
}
