// The report hook: every Claude Code event this session fires — SessionStart, UserPromptSubmit,
// PreToolUse, PostToolUse, Notification, PermissionRequest, Stop, SubagentStop, SessionEnd,
// TeammateIdle —
// becomes one POST to the local huddle's /api/registry/report (SPEC 5.1), so the registry knows
// every session on the machine, in a channel or not. The whole hook runs inside a 2 s budget
// (the deadline constants' sum sits under it): the POST waits only for what is left of the
// budget, so a server that never answers costs exactly that and no more. A report that cannot
// land — the server is down, never started, or refuses the session's credential — is appended to
// the home's outbox.jsonl (0600, capped at 5 MiB) and replayed in order before the next one that
// lands, so the registry sees every event in the order it happened. Hooks that fire together take
// turns on the outbox's lock (outbox.jsonl.lock), so each of them queues its report. Whatever
// fails, the hook answers 0 in silence (quiet.go).
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/prompttext"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The report hook's own deadlines, in milliseconds. Together they sit inside the 2 s budget
// SPEC 5.1 grants a report, well inside the timeout hooks.json gives each event.
const (
	reportStdinMS  = 500
	reportProcMS   = 300
	reportBudgetMS = 1900
	// reportQueueMS is what the report keeps back from its caller's deadline to write the outbox.
	reportQueueMS = 250
)

// outboxCap is the most the outbox holds: past it the oldest lines are dropped, so a server
// that stays dead cannot grow it without end.
const outboxCap = 5 * 1024 * 1024

// outboxLockWait is the most a report waits for the outbox's lock. The wait is part of queueing,
// so it fits in what the report keeps back from its caller's deadline.
const outboxLockWait = reportQueueMS * time.Millisecond

// registryPath is the one endpoint the report hook calls.
const registryPath = "/api/registry/report"

// claudeHarness is the harness every report of this hook names: the hook runs in Claude Code.
// Other agents report through the external word under their own harness names.
const claudeHarness = "claude"

// reportInput is what Claude Code sends the report hook on stdin; every field is optional.
type reportInput struct {
	SessionID        string `json:"session_id"`
	HookEventName    string `json:"hook_event_name"`
	TranscriptPath   string `json:"transcript_path"`
	Cwd              string `json:"cwd"`
	Prompt           string `json:"prompt"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	TeammateName     string `json:"teammate_name"`
	ToolName         string `json:"tool_name"`
	ToolInput        struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"tool_input"`
}

// toolTargetRunes is the most runes a report's tool target and prompt line carry.
const toolTargetRunes = 120

// Report posts one session event to the local huddle's registry, through the outbox when the
// server cannot take it.
func Report(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "report", func() error {
		t0 := nowOf(w)
		budget := reportBudgetMS
		if deadline, ok := ctx.Deadline(); ok {
			// the merged event hook drops a part at its own deadline, which may come first: the
			// report gives up early enough to queue itself, or it vanishes with the dropped part
			budget = min(budget, int(time.Until(deadline).Milliseconds())-reportQueueMS)
		}
		left := func() int {
			return max(1, budget-int(nowOf(w).Sub(t0).Milliseconds()))
		}
		var input reportInput
		readInput(in, min(reportStdinMS, left()), &input)
		if input.SessionID == "" || input.HookEventName == "" {
			return nil // nothing to report: no session, no event
		}
		id := cli.Identify(ctx, w, input.SessionID)
		rep := hub.Report{
			SessionID:   input.SessionID,
			Channel:     id.Channel,
			Member:      id.As,
			Cwd:         input.Cwd,
			Harness:     claudeHarness,
			LastEvent:   input.HookEventName,
			LastEventAt: jsfmt.ISOTime(nowOf(w)),
		}
		if input.HookEventName == "Notification" && (input.Message != "" || input.NotificationType != "") {
			// the notification's own words ride the report: the item a question grows needs them
			rep.Notification = &hub.Notification{Type: input.NotificationType, Message: input.Message}
		}
		if input.HookEventName == "TeammateIdle" && input.TeammateName != "" {
			// an agent-team teammate is about to go idle: the registry lists it under this session
			rep.Teammate = &hub.Teammate{Name: input.TeammateName, State: "idle"}
		}
		if (input.HookEventName == "PreToolUse" || input.HookEventName == "PostToolUse") && input.ToolName != "" {
			rep.Tool = input.ToolName
			rep.ToolTarget = toolTargetOf(input)
		}
		rep.Machine = machineOf(ctx, w)
		rep.Project, rep.Branch = checkoutOf(input.Cwd)
		rep.TranscriptPath = input.TranscriptPath
		// text Claude Code injects — a task notification, a system reminder, `!` shell input,
		// a slash-command wrapper — names nothing: only a prompt the person typed becomes the
		// title (the first prompt cut to 80 runes) and the task (its first line), which the
		// registry keeps as the first title and the newest task
		if input.Prompt != "" && !prompttext.IsHarness(input.Prompt) {
			rep.Title = jsfmt.UTF16Slice(input.Prompt, 0, 80)
			rep.Task = jsfmt.UTF16Slice(firstLineOf(input.Prompt), 0, 80)
			if input.HookEventName == "UserPromptSubmit" {
				rep.PromptLine = cutRunes(firstLineOf(input.Prompt), toolTargetRunes)
			}
		}
		model, used := lastModelOf(input.TranscriptPath)
		if model != "" {
			rep.Model = model
			rep.ContextPercent = contextPercentOf(model, used)
		}
		// a Stop is where a session's turn is done: its digest rides the report it already sends
		// (SPEC 8), so the hub keeps the newest without a second post
		if input.HookEventName == "Stop" {
			rep.Digest = digestOf(input.TranscriptPath, input.Cwd)
		}
		rep.Pid = claudePid(ctx, min(reportProcMS, left()))

		return deliverReport(ctx, w, id, rep, left)
	})
}

// deliverReport marshals one report and lands it: the outbox replays first, in order, then
// the post itself, and what cannot land queues behind what is already there. left answers the
// milliseconds of the hook's budget still left. The report hook and the external word share
// it, so every session's events reach the registry the same way.
func deliverReport(ctx context.Context, w *cli.World, id cli.Identity, rep hub.Report, left func() int) error {
	raw, err := domain.MarshalCompactJSON(rep)
	if err != nil {
		return err
	}
	post := func(body []byte, ms int) bool {
		if id.URL == "" || ms <= 0 {
			return false
		}
		resp, err := cli.HFetch(ctx, w, http.MethodPost, id.URL+registryPath,
			map[string]string{"content-type": "application/json"}, body,
			time.Duration(ms)*time.Millisecond, rep.SessionID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	outbox := ""
	if root := cli.Home(ctx, w); root != "" {
		outbox = home.Home{Root: root}.Outbox()
	}
	// the outbox replays first, in order: a report never lands before an older one
	if lines := readOutbox(outbox); len(lines) > 0 {
		for i, line := range lines {
			if !post([]byte(line), left()) {
				// the rest stays queued, and this report queues behind it
				requeueOutbox(ctx, w, outbox, lines[:i], string(raw))
				return nil
			}
		}
		// drop only what was replayed: another session's hook may have appended meanwhile
		dropReplayed(outbox, lines)
	}
	if post(raw, left()) {
		return nil
	}
	appendOutbox(ctx, w, outbox, string(raw))
	return nil
}

// machineOf answers this machine's name as the home's config.toml sets it.
func machineOf(ctx context.Context, w *cli.World) string {
	root := cli.Home(ctx, w)
	if root == "" {
		return ""
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil {
		return ""
	}
	return cfg.Machine
}

// environOf lists the world's variables for a child process.
func environOf(w *cli.World) []string {
	if w != nil && w.Environ != nil {
		return w.Environ()
	}
	return os.Environ()
}

// toolTargetOf answers the short target of the tool a hook input runs, cut to toolTargetRunes:
// a Bash command's first line, the file an edit or a read names, or an MCP tool's own name. It
// reads nothing else of the tool's input, so file contents and the rest of a command stay home.
func toolTargetOf(input reportInput) string {
	target := ""
	switch {
	case input.ToolName == "Bash":
		target = firstLineOf(input.ToolInput.Command)
	case strings.HasPrefix(input.ToolName, "mcp__"):
		// mcp__<server>__<tool>: the tool's name follows the server's
		name := strings.TrimPrefix(input.ToolName, "mcp__")
		if at := strings.Index(name, "__"); at >= 0 {
			target = name[at+2:]
		}
	case input.ToolInput.FilePath != "":
		target = input.ToolInput.FilePath
	default:
		target = input.ToolInput.NotebookPath
	}
	return cutRunes(target, toolTargetRunes)
}

// firstLineOf answers s up to its first newline, trimmed.
func firstLineOf(s string) string {
	if at := strings.IndexByte(s, '\n'); at >= 0 {
		s = s[:at]
	}
	return strings.TrimSpace(s)
}

// ── the session's process ────────────────────────────────────────────────────

// reportShells are the programs the hook chain runs through; a login shell's argv0 carries a
// leading dash, which is stripped before the name is read.
var reportShells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true,
	"ash": true, "fish": true, "csh": true, "tcsh": true, "cmd": true, "powershell": true,
	"pwsh": true,
}

// claudePid answers the Claude Code process running the session this hook serves: the nearest
// ancestor that is neither a shell nor the plugin's launcher. The whole
// walk is bounded at ms; 0 when the chain cannot be read inside it.
func claudePid(ctx context.Context, ms int) int {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	defer cancel()
	return sessionPid(os.Getppid(), psParentOf(callCtx), psCommandOf(callCtx))
}

// sessionPid walks the ancestor chain from start and answers the first process that is neither
// a shell nor the plugin's launcher. A chain it cannot read, or one that never leaves its
// shells, answers 0.
func sessionPid(start int, parent func(int) (int, bool), command func(int) (string, bool)) int {
	current := start
	for range 12 {
		line, ok := command(current)
		if !ok || strings.TrimSpace(line) == "" {
			return 0
		}
		if !hookChainLine(line) {
			return current
		}
		next, ok := parent(current)
		if !ok || next <= 0 {
			return 0
		}
		current = next
	}
	return 0
}

// hookChainLine is one ancestor the hook chain runs through: a shell, or the plugin's launcher
// (`sh …/<plugin>/dist/run hook <event>`).
func hookChainLine(line string) bool {
	if strings.Contains(line, "/dist/run ") || strings.HasSuffix(strings.TrimSpace(line), "/dist/run") {
		return true
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return true
	}
	name := strings.TrimPrefix(filepath.Base(words[0]), "-")
	return reportShells[name]
}

// psCommandOf answers a pid's command line as `ps -o command=` prints it.
func psCommandOf(ctx context.Context) func(int) (string, bool) {
	return func(pid int) (string, bool) {
		out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
}

// psParentOf answers a pid's parent as `ps -o ppid=` prints it.
func psParentOf(ctx context.Context) func(int) (int, bool) {
	return func(pid int) (int, bool) {
		out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0, false
		}
		parent, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || parent <= 0 {
			return 0, false
		}
		return parent, true
	}
}

// ── the transcript's last model call ─────────────────────────────────────────

// transcriptTail is how much of the transcript the report reads: its last 64 KiB, where the
// newest assistant message sits.
const transcriptTail = 64 * 1024

// lastModelOf reads the transcript's tail and answers the model of its last assistant message
// and how full that call found the context window (its input plus cache tokens).
func lastModelOf(path string) (model string, used int64) {
	if path == "" {
		return "", 0
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return "", 0
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer file.Close()
	start := max(0, info.Size()-transcriptTail)
	buf := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", 0
	}
	for _, line := range strings.Split(string(buf), "\n") {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					Input      int64 `json:"input_tokens"`
					CacheRead  int64 `json:"cache_read_input_tokens"`
					CacheWrite int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Type != "assistant" || entry.Message.Model == "" {
			continue
		}
		model = entry.Message.Model
		used = entry.Message.Usage.Input + entry.Message.Usage.CacheRead + entry.Message.Usage.CacheWrite
	}
	return model, used
}

// millionContext matches the model ids that run in a 1M window ("[1m]", "-1m").
var millionContext = regexp.MustCompile(`(?i)\[1m\]|-1m\b`)

// contextPercentOf answers how full the context window is, the way radar reads it: 200k unless
// the model id or the call itself says 1M. Radar is read over HTTP, never imported, so its rule
// is mirrored here.
func contextPercentOf(model string, used int64) float64 {
	window := int64(200_000)
	if millionContext.MatchString(model) || used > window {
		window = 1_000_000
	}
	if used <= 0 || window <= 0 {
		return 0
	}
	return math.Round(float64(used)/float64(window)*1000) / 10
}

// ── the outbox ───────────────────────────────────────────────────────────────

// Every change to the outbox holds its lock, so hooks that fire together never overwrite one
// another's lines. A read takes no lock: a hook with nothing queued posts without touching it,
// and a hook that rewrites reads again under the lock.

// readOutbox answers the outbox's lines, oldest first; a missing or unreadable outbox is empty.
func readOutbox(path string) []string {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// lockOutbox takes the outbox's lock and answers the func that gives it back. The lock is a flock
// on a file beside the outbox: writeOutbox replaces the outbox's inode, which a flock on the
// outbox itself would not survive. The kernel drops the lock when its holder dies, so a stale
// lock file never blocks a report. A lock that is not taken inside outboxLockWait, or cannot be
// taken at all, is no error: the report queues without it, because a failing lock must not cost
// a report.
func lockOutbox(path string) (release func()) {
	none := func() {}
	file, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return none
	}
	fd := int(file.Fd())
	deadline := time.Now().Add(outboxLockWait)
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = file.Close()
			}
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || !time.Now().Before(deadline) {
			_ = file.Close()
			return none
		}
		time.Sleep(time.Millisecond)
	}
}

// appendOutbox adds one report line to the outbox, then caps it.
func appendOutbox(ctx context.Context, w *cli.World, path, line string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	release := lockOutbox(path)
	defer release()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line + "\n")
	_ = file.Close()
	capOutbox(ctx, w, path)
}

// requeueOutbox keeps the lines the outbox holds past the replayed ones, which have landed,
// queues line behind them and caps the outbox. It reads the outbox again under the lock, so the
// lines other hooks appended since the replay began stay.
func requeueOutbox(ctx context.Context, w *cli.World, path string, replayed []string, line string) {
	release := lockOutbox(path)
	defer release()
	writeOutbox(path, append(dropPrefix(readOutbox(path), replayed), line))
	capOutbox(ctx, w, path)
}

// dropReplayed removes the replayed lines from the head of the outbox and keeps the lines other
// hooks appended since the replay began.
func dropReplayed(path string, replayed []string) {
	release := lockOutbox(path)
	defer release()
	writeOutbox(path, dropPrefix(readOutbox(path), replayed))
}

// capOutbox holds the outbox at outboxCap: past it the oldest lines are dropped (the newest
// always stays, alone when it alone is past the cap) and one line in hooks.log says how many.
func capOutbox(ctx context.Context, w *cli.World, path string) {
	lines := readOutbox(path)
	if len(lines) == 0 {
		return
	}
	size := len(lines) // the newlines the rewrite writes back
	for _, one := range lines {
		size += len(one)
	}
	if size <= outboxCap {
		return
	}
	kept := len(lines)
	for total := len(lines[kept-1]) + 1; kept > 1; kept-- {
		if total+len(lines[kept-2])+1 > outboxCap {
			break
		}
		total += len(lines[kept-2]) + 1
	}
	if dropped := len(lines) - kept; dropped > 0 {
		writeOutbox(path, lines[dropped:])
		LogHookError(ctx, w, "report", errors.New("outbox over its 5 MiB cap: dropped "+
			strconv.Itoa(dropped)+" oldest report(s)"))
	}
}

// writeOutbox replaces the outbox with lines, atomically and privately.
func writeOutbox(path string, lines []string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	body := strings.Builder{}
	for _, line := range lines {
		body.WriteString(line)
		body.WriteString("\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body.String()), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// dropPrefix answers lines minus the longest prefix that equals prefix, so a rewrite keeps
// whatever was appended beside what was replayed.
func dropPrefix(lines, prefix []string) []string {
	at := 0
	for at < len(lines) && at < len(prefix) && lines[at] == prefix[at] {
		at++
	}
	return lines[at:]
}
