package hooks

// The report hook names the session's channel member so the registry can tie a bridge to its
// session, and carries a Notification's own words for the question item it grows. The listen
// hook takes the owner's waiting messages on the next prompt (SPEC 6.4's second route) and only
// there: a tool call mid-turn leaves them waiting.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestTheReportHookNamesTheMemberAndCarriesTheNotification(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	transcript := writeTranscript(t)
	runReportHook(t, reportFixture(t, "notification", transcript, t.TempDir()), home,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	body := took[0].Body
	if field(body, "member") != "me" {
		t.Errorf("member = %q, want the channel name the session acts under", field(body, "member"))
	}
	notification, _ := body["notification"].(map[string]any)
	if notification == nil || notification["message"] != "Claude needs your permission to use Bash" {
		t.Errorf("notification = %v, want the notification's message carried whole", body["notification"])
	}
	// a typed notification carries its type too, and a plain event carries no notification at all
	typed := fmt.Sprintf(`{"session_id":"s-n","hook_event_name":"Notification","cwd":%q,"notification_type":"elicitation_dialog","message":"Which database?"}`,
		t.TempDir())
	runReportHook(t, typed, home, map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	runReportHook(t, reportFixture(t, "stop", transcript, t.TempDir()), home,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me"})
	took = reg.took()
	if len(took) != 3 {
		t.Fatalf("the registry took %d POSTs, want 3", len(took))
	}
	typedBody, _ := took[1].Body["notification"].(map[string]any)
	if typedBody == nil || typedBody["type"] != "elicitation_dialog" || typedBody["message"] != "Which database?" {
		t.Errorf("the typed notification = %v", took[1].Body["notification"])
	}
	if _, has := took[2].Body["notification"]; has {
		t.Errorf("a stop carries a notification: %v", took[2].Body["notification"])
	}
}

func TestTheListenHookBringsTheOwnersAnswerOnTheNextPrompt(t *testing.T) {
	s := startHuddle(t)
	report := fmt.Sprintf(`{"session_id":"s-take","channel":"hooks","member":"me","last_event":"UserPromptSubmit","last_event_at":%q}`,
		jsfmt.ISOTime(time.Now()))
	if code, body := post(t, s, "/api/registry/report", s.token, report); code != 200 {
		t.Fatalf("the report: %d %s", code, body)
	}
	if code, body := post(t, s, "/api/sessions/s-take/answer", s.token, `{"text":"ship it"}`); code != 200 {
		t.Fatalf("the answer: %d %s", code, body)
	}
	env := map[string]string{
		"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "hooks", "HUDDLE_AS": "me",
		// this machine exports the XDG dirs: the world must read throwaway ones
		"XDG_CONFIG_HOME": t.TempDir(), "XDG_DATA_HOME": t.TempDir(),
	}
	run := func(event string) string {
		t.Helper()
		input := fmt.Sprintf(`{"session_id":"s-take","hook_event_name":%q}`, event)
		stdout, elapsed, _ := runHook(t, "listen", input, env)
		if elapsed > 5*time.Second {
			t.Errorf("listen took %s, want it inside the hook's budget", elapsed)
		}
		neverBlocks(t, stdout)
		if strings.TrimSpace(stdout) == "" {
			return ""
		}
		return hookContext(t, stdout)
	}
	// a tool call mid-turn leaves the message waiting: only the prompt takes it
	if tool := run("PostToolUse"); strings.Contains(tool, "From the owner via huddle") {
		t.Errorf("a tool call took the owner's message: %q", tool)
	}
	prompt := run("UserPromptSubmit")
	if !strings.Contains(prompt, "From the owner via huddle: ship it") {
		t.Errorf("the prompt's context = %q, want the owner's answer in it", prompt)
	}
	// the take is once: the next prompt gets nothing of it
	if again := run("UserPromptSubmit"); strings.Contains(again, "From the owner via huddle") {
		t.Errorf("the answer came twice: %q", again)
	}
}
