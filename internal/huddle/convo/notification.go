package convo

// The <task-notification> Claude Code writes when a background task ends. A transcript carries
// it in two shapes: a queued_command attachment's prompt, and — while the session is idle — a
// plain user line whose content is the text itself. One parser reads both.

import (
	"regexp"
	"strconv"
	"strings"
)

// notification is one task notification's own tags. ExitCode is set only when the text carries
// one; a killed task's notification names no exit code.
type notification struct {
	TaskID    string
	ToolUseID string
	Status    string
	Event     string
	ExitCode  *int
}

// A task notification writes its tags as XML-ish lines; the exit code rides inside the summary.
var (
	notificationTaskID  = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
	notificationToolUse = regexp.MustCompile(`<tool-use-id>([^<]+)</tool-use-id>`)
	notificationStatus  = regexp.MustCompile(`<status>([^<]+)</status>`)
	notificationEvent   = regexp.MustCompile(`(?s)<event>(.*?)</event>`)
	notificationExit    = regexp.MustCompile(`\(exit code (\d+)\)`)
	notificationSummary = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
)

// notificationTag answers the text of a tag's first occurrence in text.
func notificationTag(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	return match[1]
}

// parseNotification reads a task notification's tags out of its text. ok is false for any text
// that is not one: a notification carries the marker and its tag block, so a line that merely
// says the words is not one.
func parseNotification(text string) (notification, bool) {
	if !strings.Contains(text, shellMarkDone) {
		return notification{}, false
	}
	note := notification{
		TaskID:    notificationTag(notificationTaskID, text),
		ToolUseID: notificationTag(notificationToolUse, text),
		Status:    notificationTag(notificationStatus, text),
		Event:     notificationTag(notificationEvent, text),
	}
	if note.TaskID == "" && note.ToolUseID == "" && note.Event == "" {
		return notification{}, false
	}
	if code := notificationExit.FindStringSubmatch(text); code != nil {
		note.ExitCode = notificationCode(code[1])
	}
	return note, true
}

// notificationCode answers one notification exit code's digits as a number.
func notificationCode(digits string) *int {
	code, err := strconv.Atoi(digits)
	if err != nil {
		return nil
	}
	return &code
}

// notificationSummaryOf answers a notification's one-line account of what ended, empty when the
// text carries none.
func notificationSummaryOf(text string) string {
	return strings.TrimSpace(notificationTag(notificationSummary, text))
}

// same answers whether two notifications name the same task, call, status and event.
func (n notification) same(other notification) bool {
	return n.TaskID == other.TaskID && n.ToolUseID == other.ToolUseID && n.Status == other.Status && n.Event == other.Event
}
