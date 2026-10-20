// The statusline word: one line for Claude Code's status bar — the project's channel and who
// is online, today's spend, and the plan limits off the snapshot Claude Code hands the
// statusline command on stdin (its own rate-limits reading; never a credential). Every part
// fails alone into the next, the whole word runs inside half a second, prints nothing it has
// nothing to say about, and always exits 0: a statusline must never block or fail the session.
package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/harness"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// quotaNumberOrNull answers a percentage as its JSON value, null when the session did not
// know it.
func quotaNumberOrNull(value *float64) *jsfmt.Value {
	if value == nil {
		return nullValue()
	}
	return jsfmt.Number(*value)
}

// quotaSecondsOrNull answers a reset time as its JSON value, null when the session did not
// know it.
func quotaSecondsOrNull(value *int64) *jsfmt.Value {
	if value == nil {
		return nullValue()
	}
	return jsfmt.Number(float64(*value))
}

// The word's budgets: stdin, one server read, and the chained previous statusline. The total
// bounds all of them together, so even a server that accepts and never answers cannot hold the
// status bar.
const (
	statuslineTotalBudget  = 450 * time.Millisecond
	statuslineReadBudget   = 100 * time.Millisecond
	statuslineServerBudget = 150 * time.Millisecond
	statuslineChainBudget  = 300 * time.Millisecond
	statuslineMaxBytes     = 64 * 1024
)

// statuslineWindow is one plan window of the snapshot: how much of it is used and when it
// resets, unix seconds. Both absent when the session did not know (an API-key session, a
// field the harness dropped).
type statuslineWindow struct {
	UsedPercentage *float64
	ResetsAt       *int64
}

// statuslineSnapshot is the fields of Claude Code's stdin JSON this word reads. Any of them
// may be missing or null; have is false when nothing parseable came in at all.
type statuslineSnapshot struct {
	SessionID string
	FiveHour  statuslineWindow
	SevenDay  statuslineWindow
	have      bool
}

// statuslineParts is everything the line may name, in the order the line names them. A nil
// pointer is a part left out.
type statuslineParts struct {
	Channel  string // "huddle <channel>"
	Online   *int   // "<n> online"
	Spend    *float64
	FiveHour *float64
	Week     *float64
}

// formatStatusline renders the line: the parts that are present, a separator only between
// them, no colour codes.
func formatStatusline(p statuslineParts) string {
	pieces := make([]string, 0, 5)
	if p.Channel != "" {
		pieces = append(pieces, "huddle "+p.Channel)
	}
	if p.Online != nil {
		pieces = append(pieces, fmt.Sprintf("%d online", *p.Online))
	}
	if p.Spend != nil {
		pieces = append(pieces, fmt.Sprintf("$%.2f today", *p.Spend))
	}
	if p.FiveHour != nil {
		pieces = append(pieces, statuslinePercent(*p.FiveHour, false, "5h"))
	}
	if p.Week != nil {
		pieces = append(pieces, statuslinePercent(*p.Week, false, "week"))
	}
	return joinStatusline(pieces)
}

// joinStatusline answers the pieces with the separator only between them.
func joinStatusline(pieces []string) string {
	out := ""
	for _, piece := range pieces {
		if piece == "" {
			continue
		}
		if out != "" {
			out += " · "
		}
		out += piece
	}
	return out
}

// statuslinePercent renders one window's percentage, coloured by the theme the status bar's
// own readers expect when colour was asked: ok under 70, warn to 90, danger past it.
func statuslinePercent(percentage float64, colour bool, label string) string {
	word := fmt.Sprintf("%s %.0f%%", label, percentage)
	if !colour {
		return word
	}
	code := "\x1b[32m" // ok
	switch {
	case percentage > 90:
		code = "\x1b[31m" // danger
	case percentage >= 70:
		code = "\x1b[33m" // warn
	}
	return code + word + "\x1b[0m"
}

// statusline runs the word: --install and --remove wire the line into Claude Code's settings
// and take it back out; bare, it prints the line.
func (c *cli) statusline(ctx context.Context) int {
	switch {
	case c.raw.has("install"):
		return c.statuslineInstall(ctx)
	case c.raw.has("remove"):
		return c.statuslineRemove(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, statuslineTotalBudget)
	defer cancel()
	raw, snap := c.readStatuslineSnapshot()
	line := formatStatusline(c.statuslineParts(ctx, snap))
	c.postQuota(ctx, snap)
	if chained := c.statuslineChain(ctx, raw); chained != "" {
		line = joinStatusline([]string{chained, line})
	}
	if line != "" {
		c.say(line)
	}
	return 0
}

// readStatuslineSnapshot reads stdin once: at most 64 KiB, within 100 ms. The bytes come back
// for the chained previous statusline to read as its own stdin.
func (c *cli) readStatuslineSnapshot() ([]byte, statuslineSnapshot) {
	type read struct{ raw []byte }
	done := make(chan read, 1)
	go func() {
		raw, _ := io.ReadAll(io.LimitReader(c.w.stdin(), statuslineMaxBytes+1))
		done <- read{raw}
	}()
	timer := time.NewTimer(statuslineReadBudget)
	defer timer.Stop()
	select {
	case got := <-done:
		if len(got.raw) > statuslineMaxBytes {
			return nil, statuslineSnapshot{}
		}
		return got.raw, parseStatuslineSnapshot(got.raw)
	case <-timer.C:
		return nil, statuslineSnapshot{}
	}
}

// parseStatuslineSnapshot reads the fields the line and the quota POST take out of Claude
// Code's snapshot; anything unparsable is an empty snapshot, never an error.
func parseStatuslineSnapshot(raw []byte) statuslineSnapshot {
	snap := statuslineSnapshot{}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return snap
	}
	snap.have = true
	snap.SessionID, _ = strField(value, "session_id")
	if limits, ok := fieldOf(value, "rate_limits"); ok && limits.Kind() == jsfmt.KindObject {
		snap.FiveHour = statuslineWindowOf(limits, "five_hour")
		snap.SevenDay = statuslineWindowOf(limits, "seven_day")
	}
	return snap
}

// statuslineWindowOf reads one window of the rate_limits object.
func statuslineWindowOf(limits *jsfmt.Value, key string) statuslineWindow {
	window := statuslineWindow{}
	one, ok := fieldOf(limits, key)
	if !ok || one.Kind() != jsfmt.KindObject {
		return window
	}
	if pct, ok := fieldOf(one, "used_percentage"); ok && pct.Kind() == jsfmt.KindNumber {
		value := numberValue(pct)
		window.UsedPercentage = &value
	}
	if at, ok := fieldOf(one, "resets_at"); ok && at.Kind() == jsfmt.KindNumber {
		value := int64(numberValue(at))
		window.ResetsAt = &value
	}
	return window
}

// statuslineParts gathers the parts from the local server: who is online in this channel and
// today's spend, each within its own budget, each left out when the answer does not come.
func (c *cli) statuslineParts(ctx context.Context, snap statuslineSnapshot) statuslineParts {
	parts := statuslineParts{Channel: c.ch}
	parts.FiveHour = snap.FiveHour.UsedPercentage
	parts.Week = snap.SevenDay.UsedPercentage
	if c.url == "" {
		return parts
	}
	// who is online is the channel's question: no channel, nothing to count
	if c.ch != "" {
		if rows := c.statuslineFetch(ctx, c.url+"/api/registry/sessions"); rows != nil {
			online := 0
			if list, ok := fieldOf(rows, "sessions"); ok && list.Kind() == jsfmt.KindArray {
				for _, row := range list.Items() {
					if textField(row, "channel") == c.ch && textField(row, "state") != "ended" {
						online++
					}
				}
			}
			parts.Online = &online
		}
	}
	if spend := c.statuslineFetch(ctx, c.url+"/api/radar/api/spend"); spend != nil {
		if field, ok := fieldOf(spend, "todayUsd"); ok && field.Kind() == jsfmt.KindNumber {
			value := numberValue(field)
			parts.Spend = &value
		}
	}
	return parts
}

// statuslineFetch reads one JSON answer from the local server within the read budget; any
// failure — no credential, a refusal, a timeout — is nil, never an error.
func (c *cli) statuslineFetch(ctx context.Context, path string) *jsfmt.Value {
	callCtx, cancel := context.WithTimeout(ctx, statuslineServerBudget)
	defer cancel()
	resp, err := HFetch(callCtx, c.w, "GET", path, nil, nil, statuslineServerBudget, "")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	if value, err := jsfmt.DecodeValue(raw); err == nil {
		return value
	}
	return nil
}

// postQuota hands the snapshot to the local server's radar quota route, so the Overview's
// Plan limits card reads it; every failure is ignored, the statusline never waits for one.
func (c *cli) postQuota(ctx context.Context, snap statuslineSnapshot) {
	if c.url == "" || !snap.have {
		return
	}
	five := quotaNumberOrNull(snap.FiveHour.UsedPercentage)
	fiveAt := quotaSecondsOrNull(snap.FiveHour.ResetsAt)
	week := quotaNumberOrNull(snap.SevenDay.UsedPercentage)
	weekAt := quotaSecondsOrNull(snap.SevenDay.ResetsAt)
	body := jsfmt.Object(
		[]string{"session_id", "five_hour", "five_hour_resets_at", "seven_day", "seven_day_resets_at", "at"},
		[]*jsfmt.Value{
			jsfmt.String(snap.SessionID), five, fiveAt, week, weekAt,
			jsfmt.Number(float64(c.w.now().Unix())),
		},
	)
	postCtx, cancel := context.WithTimeout(ctx, statuslineServerBudget)
	defer cancel()
	if resp, err := HFetch(postCtx, c.w, "POST", c.url+"/api/radar/api/quota",
		map[string]string{"content-type": "application/json"}, []byte(body.EncodeJSON()),
		statuslineServerBudget, ""); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// statuslineChain runs the statusline install replaced with the same stdin and answers its
// first line, empty when there is none or it said nothing within its budget.
func (c *cli) statuslineChain(ctx context.Context, stdin []byte) string {
	hud, ok := TheHome(ctx, c.w)
	if !ok {
		return ""
	}
	command := harness.StatusLineChain(hud.Root)
	if command == "" {
		return ""
	}
	runCtx, cancel := context.WithTimeout(ctx, statuslineChainBudget)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil && out.Len() == 0 {
		return ""
	}
	return strings.SplitN(out.String(), "\n", 2)[0]
}

// statuslineInstall writes the entry into Claude Code's settings and says what it did.
func (c *cli) statuslineInstall(ctx context.Context) int {
	hud, ok := TheHome(ctx, c.w)
	if !ok {
		return c.die("no home: set HOME or HUDDLE_HOME", 1)
	}
	previous := harness.StatusLineChain(hud.Root)
	got, err := harness.InstallStatusLine(hud.Root, harnessEnv(c.w), harnessBin(c.w))
	if err != nil {
		return c.die(err.Error(), 1)
	}
	switch {
	case got.Already:
		c.say(fmt.Sprintf("statusline: already installed in %s", got.Path))
	case got.Created:
		c.say(fmt.Sprintf("statusline: wrote %s", got.Path))
	default:
		note := "--remove takes the key back out"
		if previous != "" {
			note = "--remove restores your previous statusline"
		}
		c.say(fmt.Sprintf("statusline: installed in %s (%s)", got.Path, note))
	}
	return 0
}

// statuslineRemove takes the entry back out and says what the settings hold now.
func (c *cli) statuslineRemove(ctx context.Context) int {
	hud, ok := TheHome(ctx, c.w)
	if !ok {
		return c.die("no home: set HOME or HUDDLE_HOME", 1)
	}
	previous := harness.StatusLineChain(hud.Root)
	got, err := harness.RemoveStatusLine(hud.Root, harnessEnv(c.w))
	if err != nil {
		return c.die(err.Error(), 1)
	}
	if got.NotThere {
		return c.die(fmt.Sprintf("statusline: nothing to remove at %s", got.Path), 4)
	}
	switch {
	case got.FileGone:
		c.say(fmt.Sprintf("statusline: removed %s (install created it)", got.Path))
	case previous != "":
		c.say(fmt.Sprintf("statusline: removed from %s (your previous statusline is back)", got.Path))
	default:
		c.say(fmt.Sprintf("statusline: removed from %s (the key is gone again)", got.Path))
	}
	return 0
}
