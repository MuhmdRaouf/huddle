package prompttext

// IsHarness tells Claude Code's own injected text from the words a person typed: every prefix
// of harnessPrefixes is the harness's, bare or behind spaces and newlines; the person's words,
// a slash command and a tag that only looks like a wrapper never are.

import "testing"

func TestIsHarness(t *testing.T) {
	said := map[string]bool{}
	for _, prefix := range harnessPrefixes {
		said[prefix+" rest"] = true
		said["  "+prefix+" rest"] = true
		said["\n\t"+prefix+" rest"] = true
	}
	// the cases the digest's wrapper check carried, moved whole
	said["<command-name>/huddle:join</command-name>"] = true
	said["  <command-message>huddle:join</command-message>"] = true
	said["<local-command-stdout>joined qa-three-a</local-command-stdout>"] = true
	said["<system-reminder>the channel</system-reminder>"] = true
	said["Caveat: the messages below were generated"] = true
	// the person's words, a slash command and a tag that only looks like a wrapper
	said["fix the login bug"] = false
	said["/huddle:join 127.0.0.1:4100"] = false
	said["<div> is misaligned"] = false
	said["plain words"] = false
	said["<b>not a wrapper</b>"] = false
	said[""] = false
	for text, want := range said {
		if got := IsHarness(text); got != want {
			t.Errorf("IsHarness(%q) = %v, want %v", text, got, want)
		}
	}
}
