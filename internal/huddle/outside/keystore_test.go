package outside

// The OS keystore's commands, proved against a recording runner: the secret travels on the
// tool's stdin or its stdout, never its command line, and the service and account are spelled
// the same on both platforms. No real keystore is ever read here — the runner answers canned
// outcomes.

import (
	"context"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
)

// ranRes is a tool run that exited code with the given output.
func ranRes(code int, stdout string) keystore.RunResult {
	return keystore.RunResult{Code: &code, Stdout: stdout}
}

// fakeTool answers one platform's tool: it records every command line and stdin, and answers
// what each command should.
type fakeTool struct {
	lines  []string
	stdins []string
	answer map[string]keystore.RunResult
}

// run records the call and answers the canned outcome for the command's first word.
func (f *fakeTool) run(_ context.Context, name string, args []string, stdin *string) keystore.RunResult {
	f.lines = append(f.lines, name+" "+strings.Join(args, " "))
	if stdin != nil {
		f.stdins = append(f.stdins, *stdin)
	} else {
		f.stdins = append(f.stdins, "")
	}
	return f.answer[name+" "+args[0]]
}

func TestTheMacOSKeychainKeepsItsSecretsOffTheCommandLine(t *testing.T) {
	tool := &fakeTool{answer: map[string]keystore.RunResult{
		"/usr/bin/security default-keychain":      ranRes(0, ""),
		"/usr/bin/security find-generic-password": ranRes(0, "the-token\n"),
		"/usr/bin/security -i":                    ranRes(0, ""),
	}}
	store := &osKeyStore{run: tool.run, platform: "darwin"}
	if !store.Available(context.Background()) {
		t.Fatal("a keychain that answers is not available")
	}
	if got, ok := store.Get(context.Background(), "dns:cloudflare"); !ok || got != "the-token" {
		t.Fatalf("Get = %q, %v", got, ok)
	}
	if err := store.Set(context.Background(), "dns:cloudflare", "the-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// every command line the store built: the probe, the get, the set, the read-back that
	// proves the write
	if len(tool.lines) != 4 {
		t.Fatalf("commands = %v", tool.lines)
	}
	for _, line := range tool.lines {
		if strings.Contains(line, "the-token") {
			t.Errorf("a secret reached a command line: %s", line)
		}
	}
	// the secret travelled inside `security -i`'s stdin command instead
	if len(tool.stdins) != 4 || !strings.Contains(tool.stdins[2], "the-token") {
		t.Errorf("the store command did not carry the secret on stdin: %v", tool.stdins)
	}
	if !strings.Contains(tool.lines[1], "-s huddle -a dns:cloudflare -w") {
		t.Errorf("the lookup is not by the service and account: %s", tool.lines[1])
	}
}

func TestTheSecretServiceIsAskedThroughSecretTool(t *testing.T) {
	tool := &fakeTool{answer: map[string]keystore.RunResult{
		"secret-tool lookup": ranRes(1, ""), // no such secret: the service answered
		"secret-tool store":  ranRes(0, ""),
	}}
	store := &osKeyStore{run: tool.run, platform: "linux"}
	if !store.Available(context.Background()) {
		t.Fatal("a secret service that answers is not available")
	}
	if err := store.Set(context.Background(), "dns:desec", "one-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(tool.lines) < 2 || !strings.Contains(tool.lines[1], "store --label=Huddle dns:desec service huddle account dns:desec") {
		t.Fatalf("the store command = %v", tool.lines)
	}
	if len(tool.stdins) < 2 || tool.stdins[1] != "one-token" {
		t.Errorf("the token did not travel on stdin: %v", tool.stdins)
	}
}

func TestAKeystoreThatDoesNotAnswerIsNotAvailable(t *testing.T) {
	tool := &fakeTool{answer: map[string]keystore.RunResult{
		"/usr/bin/security default-keychain": {}, // the tool never ran
	}}
	store := &osKeyStore{run: tool.run, platform: "darwin"}
	if store.Available(context.Background()) {
		t.Error("a machine whose keychain tool never ran counts as available")
	}
	if _, ok := store.Get(context.Background(), "dns:cloudflare"); ok {
		t.Error("a get through a dead keystore answered ok")
	}
	if err := store.Set(context.Background(), "dns:cloudflare", "x"); err == nil {
		t.Error("a set through a dead keystore answered no error")
	}
}

func TestARefusingAccountNameIsRefusedBeforeAnyToolRuns(t *testing.T) {
	tool := &fakeTool{}
	store := &osKeyStore{run: tool.run, platform: "darwin"}
	if err := store.Set(context.Background(), "dns:cloud flare", "x"); err == nil {
		t.Error("an account with a space was accepted")
	}
	if len(tool.lines) != 0 {
		t.Errorf("a refused account still ran a tool: %v", tool.lines)
	}
}

// TestTheKeystoreDeleteRunsThePlatformsRemove pins delete on both platforms: the platform's own
// remove names the service and the account, which are no secret; the tool's stdin carries none;
// a dead tool answers an error; and a bad account name runs no tool at all.
func TestTheKeystoreDeleteRunsThePlatformsRemove(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		tool := &fakeTool{answer: map[string]keystore.RunResult{
			"/usr/bin/security -i": ranRes(0, ""),
		}}
		store := &osKeyStore{run: tool.run, platform: "darwin"}
		if err := store.Delete(context.Background(), "cc-messaging:s-rep"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(tool.lines) != 1 || tool.lines[0] != "/usr/bin/security -i" {
			t.Fatalf("commands = %v, want the one `security -i`", tool.lines)
		}
		for _, want := range []string{"delete-generic-password", `-s "huddle"`, `-a "cc-messaging:s-rep"`} {
			if !strings.Contains(tool.stdins[0], want) {
				t.Errorf("the stdin command %q does not name %s", tool.stdins[0], want)
			}
		}
	})
	t.Run("linux", func(t *testing.T) {
		tool := &fakeTool{answer: map[string]keystore.RunResult{
			"secret-tool clear": ranRes(0, ""),
		}}
		store := &osKeyStore{run: tool.run, platform: "linux"}
		if err := store.Delete(context.Background(), "cc-messaging:s-rep"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(tool.lines) != 1 || !strings.Contains(tool.lines[0], "clear service huddle account cc-messaging:s-rep") {
			t.Fatalf("commands = %v, want secret-tool clear by service and account", tool.lines)
		}
		if tool.stdins[0] != "" {
			t.Errorf("stdin = %q, want it empty", tool.stdins[0])
		}
	})
	t.Run("a dead tool is an error", func(t *testing.T) {
		tool := &fakeTool{answer: map[string]keystore.RunResult{
			"/usr/bin/security -i": {Missing: true}, // what SpawnRunner answers when no tool starts
		}}
		store := &osKeyStore{run: tool.run, platform: "darwin"}
		if err := store.Delete(context.Background(), "cc-messaging:s-rep"); err == nil {
			t.Error("a keystore whose tool could not start answered no error")
		}
		if len(tool.lines) != 1 {
			t.Fatalf("commands = %v, want the one `security -i`", tool.lines)
		}
	})
	t.Run("a bad account runs no tool", func(t *testing.T) {
		tool := &fakeTool{}
		store := &osKeyStore{run: tool.run, platform: "darwin"}
		if err := store.Delete(context.Background(), "bad account"); err == nil {
			t.Error("an account with a space was accepted")
		}
		if len(tool.lines) != 0 {
			t.Errorf("a refused account still ran a tool: %v", tool.lines)
		}
	})
}
