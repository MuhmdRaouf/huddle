package main

// Two sessions in two repositories build one thing together through one channel: the Homelab
// session and the ProxBeam session, each in its own repo, each with its own `huddle mcp`
// bridge, on one hub. The test walks the path a user takes: setup in the first repo starts the
// hub and the channel, the second repo joins with the join line, both sessions start, and each
// sends the other one message. Each message reaches its addressee through the bridge's poll
// tool and as the channel push Claude Code receives. Every process is this test's child and
// every home a temp one.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// twoReposRPC is one JSON-RPC line a bridge wrote.
type twoReposRPC struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// twoReposPush is the params of one notifications/claude/channel push.
type twoReposPush struct {
	Content string `json:"content"`
	Meta    struct {
		Channel string `json:"channel"`
		Seq     string `json:"seq"`
		Topic   string `json:"topic"`
		From    string `json:"from"`
	} `json:"meta"`
}

// twoReposEvent is one channel event as the send and poll tools answer it.
type twoReposEvent struct {
	Seq   int64  `json:"seq"`
	From  string `json:"from"`
	To    string `json:"to"`
	Topic string `json:"topic"`
	Msg   string `json:"msg"`
}

// twoReposBridge is one `huddle mcp` child driven over stdin and stdout, the way Claude Code
// drives it.
type twoReposBridge struct {
	t     *testing.T
	name  string
	stdin io.WriteCloser
	done  chan struct{}
	cmd   *exec.Cmd

	mu     sync.Mutex
	lines  []twoReposRPC
	stderr bytes.Buffer
	nextID int
}

// Write collects the bridge's diagnostics for a failure message.
func (b *twoReposBridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stderr.Write(p)
}

// startTwoReposBridge starts one bridge in env and dir and registers its stop: stdin closes, the
// bridge must exit 0 on its own.
func startTwoReposBridge(t *testing.T, name string, env map[string]string, dir string) *twoReposBridge {
	t.Helper()
	b := &twoReposBridge{t: t, name: name, done: make(chan struct{})}
	b.cmd = exec.Command(huddleBin, "mcp")
	b.cmd.Env = argvEntries(env)
	b.cmd.Dir = dir
	b.cmd.Stderr = b
	stdin, err := b.cmd.StdinPipe()
	if err != nil {
		t.Fatalf("%s bridge stdin: %v", name, err)
	}
	b.stdin = stdin
	stdout, err := b.cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("%s bridge stdout: %v", name, err)
	}
	if err := b.cmd.Start(); err != nil {
		t.Fatalf("start the %s bridge: %v", name, err)
	}
	go func() {
		defer close(b.done)
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if strings.TrimSpace(line) != "" {
				var rpc twoReposRPC
				if json.Unmarshal([]byte(line), &rpc) == nil {
					b.mu.Lock()
					b.lines = append(b.lines, rpc)
					b.mu.Unlock()
				}
			}
			if err != nil {
				_ = b.cmd.Wait()
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = b.stdin.Close()
		select {
		case <-b.done:
			if code := b.cmd.ProcessState.ExitCode(); code != 0 {
				t.Errorf("the %s bridge exited %d after its stdin closed, want 0; stderr:\n%s", name, code, b.errText())
			}
		case <-time.After(10 * time.Second):
			_ = b.cmd.Process.Kill()
			<-b.done
			t.Errorf("the %s bridge did not exit within 10 s of its stdin closing; stderr:\n%s", name, b.errText())
		}
	})
	return b
}

// errText answers the bridge's diagnostics so far.
func (b *twoReposBridge) errText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stderr.String()
}

// write puts one JSON-RPC line on the bridge's stdin.
func (b *twoReposBridge) write(line string) {
	b.t.Helper()
	if _, err := io.WriteString(b.stdin, line+"\n"); err != nil {
		b.t.Fatalf("write to the %s bridge: %v", b.name, err)
	}
}

// find answers the first line, in the order the bridge wrote them, that match accepts, waiting
// up to within for one.
func (b *twoReposBridge) find(within time.Duration, match func(twoReposRPC) bool) (twoReposRPC, bool) {
	var found twoReposRPC
	ok := pollFor(within, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, rpc := range b.lines {
			if match(rpc) {
				found = rpc
				return true
			}
		}
		return false
	})
	return found, ok
}

// request sends one request and answers its reply.
func (b *twoReposBridge) request(method string, params any) twoReposRPC {
	b.t.Helper()
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.mu.Unlock()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		b.t.Fatalf("encode %s: %v", method, err)
	}
	b.write(string(raw))
	reply, ok := b.find(20*time.Second, func(rpc twoReposRPC) bool { return rpc.ID != nil && *rpc.ID == id })
	if !ok {
		b.t.Fatalf("the %s bridge never answered %s; stderr:\n%s", b.name, method, b.errText())
	}
	if len(reply.Error) > 0 {
		b.t.Fatalf("the %s bridge answered %s with an error: %s", b.name, method, reply.Error)
	}
	return reply
}

// initialize runs the handshake and answers the instructions the bridge gave the session.
func (b *twoReposBridge) initialize() string {
	b.t.Helper()
	reply := b.request("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
	b.write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	var result struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		b.t.Fatalf("decode the %s initialize result %s: %v", b.name, reply.Result, err)
	}
	return result.Instructions
}

// tool calls one tool and answers its text; a tool error fails the test.
func (b *twoReposBridge) tool(name string, args map[string]any) string {
	b.t.Helper()
	reply := b.request("tools/call", map[string]any{"name": name, "arguments": args})
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(reply.Result, &result); err != nil || len(result.Content) == 0 {
		b.t.Fatalf("decode the %s %s result %s: %v", b.name, name, reply.Result, err)
	}
	if result.IsError {
		b.t.Fatalf("the %s bridge's %s failed: %s", b.name, name, result.Content[0].Text)
	}
	return result.Content[0].Text
}

// twoReposSent is the send tool's answer: the seq of the message it made.
var twoReposSent = regexp.MustCompile(`^#(\d+) sent to (\S+)`)

// send messages one session through the send tool and answers the event it made, as the
// channel will list it.
func (b *twoReposBridge) send(to, text string) twoReposEvent {
	b.t.Helper()
	answer := b.tool("send", map[string]any{"to": to, "msg": text})
	match := twoReposSent.FindStringSubmatch(answer)
	if match == nil || match[2] != to {
		b.t.Fatalf("the %s bridge's send to %s answered %q, want %q", b.name, to, answer, "#<seq> sent to "+to)
	}
	seq, _ := strconv.ParseInt(match[1], 10, 64)
	return twoReposEvent{Seq: seq, From: b.name, To: to, Topic: "msg", Msg: text}
}

// poll reads the channel's messages through the poll tool.
func (b *twoReposBridge) poll() []twoReposEvent {
	b.t.Helper()
	answer := b.tool("poll", map[string]any{"after": 0, "topics": []string{"msg"}, "timeout_ms": 5000})
	var events []twoReposEvent
	if err := json.Unmarshal([]byte(answer), &events); err != nil {
		b.t.Fatalf("decode the %s poll answer %q: %v", b.name, answer, err)
	}
	return events
}

// push answers the first channel push whose content holds text, waiting up to within for it.
func (b *twoReposBridge) push(within time.Duration, text string) (twoReposPush, bool) {
	rpc, ok := b.find(within, func(rpc twoReposRPC) bool {
		if rpc.Method != "notifications/claude/channel" {
			return false
		}
		var push twoReposPush
		return json.Unmarshal(rpc.Params, &push) == nil && strings.Contains(push.Content, text)
	})
	if !ok {
		return twoReposPush{}, false
	}
	var push twoReposPush
	_ = json.Unmarshal(rpc.Params, &push)
	return push, true
}

// pushes counts the channel pushes whose content holds text.
func (b *twoReposBridge) pushes(text string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, rpc := range b.lines {
		var push twoReposPush
		if rpc.Method == "notifications/claude/channel" && json.Unmarshal(rpc.Params, &push) == nil &&
			strings.Contains(push.Content, text) {
			count++
		}
	}
	return count
}

// twoReposRepo makes a git repository in a temp dir under root.
func twoReposRepo(t *testing.T, user, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	devGit(t, user, dir, "init", "-q")
	return dir
}

// twoReposSettingsFile is the part of a repo's huddle.json this test reads.
type twoReposSettingsFile struct {
	Channel   string `json:"channel"`
	As        string `json:"as"`
	Autostart bool   `json:"autostart"`
}

// twoReposSettings reads a repo's .agents/huddle/huddle.json.
func twoReposSettings(t *testing.T, repo string) twoReposSettingsFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, ".agents", "huddle", "huddle.json"))
	if err != nil {
		t.Fatalf("read %s's huddle.json: %v", repo, err)
	}
	var settings twoReposSettingsFile
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse %s's huddle.json %q: %v", repo, raw, err)
	}
	return settings
}

// twoReposHubPid reads the running hub's pid from the home.
func twoReposHubPid(home string) int {
	raw, err := os.ReadFile(filepath.Join(home, "run", "huddle.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}

// TestE2ETwoReposTalkThroughOneChannel is the guard for two repositories building one thing
// together: the Homelab session and the ProxBeam session message each other through channel
// homelab, and each message arrives by the poll tool and by the push Claude Code gets.
func TestE2ETwoReposTalkThroughOneChannel(t *testing.T) {
	// a short root: the homes and sockets under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-tr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	user := filepath.Join(root, "user")
	hubHome := filepath.Join(root, "huddle-home")
	hubPort := ports.Free(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	env := isoenv.Lookup(isoenv.Environ(t, user,
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"RADAR_AUTOSTART=0",
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_ROUTER_WATCH=0",
		"HUDDLE_NOTIFY_LOG="+filepath.Join(root, "notify.log"),
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))

	homelab := twoReposRepo(t, user, root, "homelab")
	proxbeam := twoReposRepo(t, user, root, "proxbeam")
	// each Claude session names itself the way Claude Code does, and works in its own repo
	sessionEnv := func(repo, sessionID string) map[string]string {
		return argvLayer(env, map[string]string{"CLAUDE_PROJECT_DIR": repo, "CLAUDE_CODE_SESSION_ID": sessionID})
	}
	homelabEnv := sessionEnv(homelab, "sess-homelab")
	proxbeamEnv := sessionEnv(proxbeam, "sess-proxbeam")

	t.Cleanup(func() {
		pid := twoReposHubPid(hubHome)
		_, _, _ = runBin(t, huddleBin, homelabEnv, homelab, "down")
		if !pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) }) && pid > 0 {
			// `down` left the test's own hub running: end it the direct way
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) }) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if pid > 0 && !pollFor(5*time.Second, func() bool { return syscall.Kill(pid, 0) != nil }) {
			t.Errorf("the hub (pid %d) still runs after the test", pid)
		}
	})

	// 1. setup in the first repo starts the hub and creates channel homelab, with this session in
	// it as homelab; it prints the join line for the next session
	code, out, errOut := runBin(t, huddleBin, homelabEnv, homelab, "setup", "--channel", twoReposChannel, "--as", "homelab", "--start")
	if code != 0 {
		t.Fatalf("setup --start in the first repo = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
	join := regexp.MustCompile(`/huddle:join (\S+) --token (\S+)`).FindStringSubmatch(out)
	if join == nil {
		t.Fatalf("setup printed no join line:\n%s", out)
	}
	first := twoReposSettings(t, homelab)
	if first != (twoReposSettingsFile{Channel: twoReposChannel, As: "homelab", Autostart: true}) {
		t.Fatalf("the first repo's huddle.json = %+v, want channel homelab, as homelab, autostart true", first)
	}

	// 2. the second repo names the same channel as proxbeam and joins with that line
	second := `{"channel":"` + twoReposChannel + `","as":"proxbeam","autostart":false}` + "\n"
	if err := os.MkdirAll(filepath.Join(proxbeam, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxbeam, ".agents", "huddle", "huddle.json"), []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runBin(t, huddleBin, proxbeamEnv, proxbeam, "join", join[1], "--token", join[2], "--as", "proxbeam")
	if code != 0 {
		t.Fatalf("join in the second repo = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// 3. both sessions start: the session-start hook of each puts it in the channel
	for _, session := range []struct{ name, repo, id string }{
		{"homelab", homelab, "sess-homelab"},
		{"proxbeam", proxbeam, "sess-proxbeam"},
	} {
		stdin := `{"hook_event_name":"SessionStart","session_id":"` + session.id + `","source":"startup","cwd":` + strconv.Quote(session.repo) + `}`
		code, out, errOut := hooksRun(t, sessionEnv(session.repo, session.id), session.repo, stdin, "hook", "session-start")
		if code != 0 {
			t.Fatalf("the %s session-start hook = %d\nstdout:\n%s\nstderr:\n%s", session.name, code, out, errOut)
		}
		var answer struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(out), &answer); err != nil {
			t.Fatalf("the %s session-start hook printed %q, want its context as JSON: %v", session.name, out, err)
		}
		want := `You are in Huddle channel "homelab" as "` + session.name + `"`
		if !strings.Contains(answer.HookSpecificOutput.AdditionalContext, want) {
			t.Fatalf("the %s session-start hook said %q, want it to hold %q", session.name, answer.HookSpecificOutput.AdditionalContext, want)
		}
	}

	// 4. one bridge per session, each handshaking as Claude Code does
	homelabBridge := startTwoReposBridge(t, "homelab", homelabEnv, homelab)
	proxbeamBridge := startTwoReposBridge(t, "proxbeam", proxbeamEnv, proxbeam)
	for _, bridge := range []*twoReposBridge{homelabBridge, proxbeamBridge} {
		want := `You are "` + bridge.name + `" in Huddle channel "homelab"`
		if instructions := bridge.initialize(); !strings.Contains(instructions, want) {
			t.Fatalf("the %s bridge's instructions = %q, want them to hold %q", bridge.name, instructions, want)
		}
	}

	// The live stream does not replay a message sent before it connects, and the bridge gives no
	// sign when it has: probes sent until the push arrives say the stream is up, in both
	// directions, before the exchange below begins.
	probes := 0
	for _, direction := range []struct{ from, to *twoReposBridge }{
		{homelabBridge, proxbeamBridge},
		{proxbeamBridge, homelabBridge},
	} {
		until(t, 30*time.Second, func() bool {
			probes++
			probe := fmt.Sprintf("probe %d: is the push stream up?", probes)
			direction.from.send(direction.to.name, probe)
			_, arrived := direction.to.push(time.Second, probe)
			return arrived
		}, "the "+direction.to.name+" bridge's push stream to carry a probe")
	}

	// 5. each session sends the other one message
	toProxbeam := "homelab to proxbeam: the NAS sits on VLAN 20, can ProxBeam reach it?"
	toHomelab := "proxbeam to homelab: reachable, the storage pool mounts at /mnt/nas"
	sentToProxbeam := homelabBridge.send("proxbeam", toProxbeam)
	sentToHomelab := proxbeamBridge.send("homelab", toHomelab)
	recipients := []struct {
		bridge *twoReposBridge
		sent   twoReposEvent
		text   string
	}{
		{proxbeamBridge, sentToProxbeam, toProxbeam},
		{homelabBridge, sentToHomelab, toHomelab},
	}
	for _, c := range recipients {
		// (a) the poll tool lists it, from the sender, to the recipient
		var polled *twoReposEvent
		for _, event := range c.bridge.poll() {
			if event.Msg == c.text {
				polled = &event
			}
		}
		if polled == nil {
			t.Fatalf("the %s bridge's poll never listed %q", c.bridge.name, c.text)
		}
		if *polled != c.sent {
			t.Errorf("the %s bridge's poll listed %+v, want the event send made, %+v", c.bridge.name, *polled, c.sent)
		}

		// (b) the channel push Claude Code gets names the same sender, recipient and text
		push, ok := c.bridge.push(10*time.Second, c.text)
		if !ok {
			t.Fatalf("the %s bridge wrote no channel push for %q; stderr:\n%s", c.bridge.name, c.text, c.bridge.errText())
		}
		wantContent := fmt.Sprintf("%s → %s · msg #%d: %s", c.sent.From, c.sent.To, c.sent.Seq, c.text)
		if push.Content != wantContent {
			t.Errorf("the %s bridge's push content = %q, want %q", c.bridge.name, push.Content, wantContent)
		}
		if push.Meta.Channel != twoReposChannel || push.Meta.From != c.sent.From ||
			push.Meta.Topic != "msg" || push.Meta.Seq != strconv.FormatInt(c.sent.Seq, 10) {
			t.Errorf("the %s bridge's push meta = %+v, want channel %s, from %s, topic msg, seq %d",
				c.bridge.name, push.Meta, twoReposChannel, c.sent.From, c.sent.Seq)
		}
	}

	// a session is not pushed its own message, and each message arrives once
	for _, c := range []struct {
		bridge *twoReposBridge
		text   string
		want   int
	}{
		{homelabBridge, toProxbeam, 0},
		{proxbeamBridge, toHomelab, 0},
		{proxbeamBridge, toProxbeam, 1},
		{homelabBridge, toHomelab, 1},
	} {
		if got := c.bridge.pushes(c.text); got != c.want {
			t.Errorf("the %s bridge wrote %d pushes for %q, want %d", c.bridge.name, got, c.text, c.want)
		}
	}
}

// twoReposChannel is the channel the two repositories share.
const twoReposChannel = "homelab"
