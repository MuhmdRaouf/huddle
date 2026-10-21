// Package prompttext tells Claude Code's own injected text from the words a person typed, so
// neither ever stands in for the other: a prompt the harness submits names no session's title
// or task, and a transcript line it wrote never becomes a digest's goal.
package prompttext

import "strings"

// harnessPrefixes open the text Claude Code itself writes into a prompt or transcript — a
// slash-command's name, message and args, its local stdout and stderr, a system reminder, a
// background task's notification, `!` shell input and its output, a prompt-submit hook's word
// and the transcript's caveat — not words the person typed. Never appended to: it is constant
// data.
var harnessPrefixes = []string{
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<system-reminder>",
	"<task-notification>",
	"<bash-input>",
	"<bash-stdout>",
	"<bash-stderr>",
	"<user-prompt-submit-hook>",
	"Caveat:",
}

// IsHarness answers whether text is one of those wrappers: true when the text, once trimmed of
// its surrounding whitespace, starts with one of the harness's prefixes.
func IsHarness(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range harnessPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}
