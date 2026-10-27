package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// The command table: one row per command of the surface, then the usage-error,
// no-board and refusal rows, the --json documents and the cli-level rows of the
// board suite (the board package holds the board-level halves).

const longSummary = "Ports the router front process to Go: it forks the worker, restarts it when it dies, " +
	"retries requests on a fresh worker, and serves passthrough while none is up. " +
	"Done when all front cases pass against a fake worker."

// usageLine is the line every unknown command prints.
const usageLine = "usage: kanban <command> [flags]; run kanban help for the list"

// fixedNow pins every stamp the board writes.
var fixedNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("+0300", 3*60*60))

// fixture is one throwaway board in a throwaway project.
type fixture struct {
	t    *testing.T
	root string // the board directory
	cwd  string // the working directory the world reports
	env  map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	project := t.TempDir()
	f := &fixture{t: t, root: filepath.Join(project, ".agents", "kanban"), cwd: project}
	for _, col := range board.Columns {
		if err := os.MkdirAll(filepath.Join(f.root, col), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.writeFile("EPOCH", "1\n")
	f.writeFile("LOG", "")
	return f
}

func (f *fixture) writeFile(name, text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, name), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) readFile(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

// card writes one ticket file and answers its path.
func (f *fixture) card(col, id, fm string, body ...string) string {
	f.t.Helper()
	text := "---\n" + strings.TrimSpace(fm) + "\n---\n\n"
	if len(body) == 1 {
		text += strings.TrimRight(body[0], "\n") + "\n"
	} else {
		text += "# " + id + "\n\nBody text.\n"
	}
	p := filepath.Join(f.root, col, id+".md")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// fullBody answers a body that clears the ticket bar.
func fullBody(id string) string {
	return "# " + id + ": full\n\n" +
		"## Description\nWhat changes and why. Done when: the bar passes.\n\n" +
		"## Context\nThe reference suites.\n\n" +
		"## Implementation plan\n1. do the work\n\n" +
		"## Implementation details\n" + strings.Repeat("planned decisions ", 15) + "\n\n" +
		"## Acceptance\n- [ ] it works\n\n" +
		"## Verification\n```\ngo test ./...\n```\nexit 0\n\n" +
		"## Out of scope\nNothing else.\n\n" +
		"## Log\n"
}

// fullCard writes a ticket that clears the bar wherever it sits; extraFM joins
// onto the front matter (holders, notes, dependencies).
func (f *fixture) fullCard(col, id string, extraFM ...string) string {
	return f.card(col, id, "id: "+id+"\ntitle: \""+id+" title\"\ncluster: kanban\n"+
		"summary: \""+longSummary+"\"\ntype: docs\npriority: P2\nest: M"+strings.Join(extraFM, ""),
		fullBody(id))
}

// stopped leaves the board as stop-line does: epoch 2 and a STOP-LINE record.
func (f *fixture) stopped() {
	f.writeFile("EPOCH", "2\n")
	f.writeFile("STOP-LINE", `{"epoch":2,"actor":"lead","note":"main is red","ts":"2026-10-15T12:00:00+0300"}`)
}

func (f *fixture) exists(col, id string) bool {
	_, err := os.Stat(filepath.Join(f.root, col, id+".md"))
	return err == nil
}

func (f *fixture) ticketText(col, id string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, col, id+".md"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) world() World {
	return World{
		Getenv: func(key string) (string, bool) { v, ok := f.env[key]; return v, ok },
		Cwd:    func() string { return f.cwd },
		Now:    func() time.Time { return fixedNow },
	}
}

// run runs one command line and answers the exit code, stdout and stderr.
func (f *fixture) run(argv ...string) (int, string, string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), f.world(), argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func has(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		// A wanted path may be printed with the OS's own separator.
		if !strings.Contains(text, want) && !strings.Contains(text, filepath.FromSlash(want)) {
			t.Errorf("%s = %q, want it to contain %q", what, text, want)
		}
	}
}

func hasNot(t *testing.T, what, text string, banned ...string) {
	t.Helper()
	for _, ban := range banned {
		if strings.Contains(text, ban) {
			t.Errorf("%s = %q, want it without %q", what, text, ban)
		}
	}
}

func equals(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// referenceBoard builds a pre-kanban board holding one ticket.
func referenceBoard(t *testing.T) string {
	t.Helper()
	old := filepath.Join(t.TempDir(), ".agents", "backlog")
	if err := os.MkdirAll(filepath.Join(old, "backlog"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nid: OLD-1\ntitle: \"old\"\ntype: docs\n---\n\n# OLD-1\n"
	if err := os.WriteFile(filepath.Join(old, "backlog", "OLD-1.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return old
}

// TestEveryCommand runs one row per command of the surface.
func TestEveryCommand(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(f *fixture)
		argv    func(t *testing.T, f *fixture) []string
		code    int
		outHas  []string
		errHas  []string
		check   func(t *testing.T, f *fixture, stdout string)
	}{
		{
			name: "init creates the layout",
			prepare: func(f *fixture) {
				f.cwd = t.TempDir() // a project with no board yet
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"init"} },
			code:   0,
			outHas: []string{": created ", "backlog/", "README.md", "EPOCH", "LOG"},
			check: func(t *testing.T, f *fixture, _ string) {
				equals(t, "EPOCH", strings.TrimSpace(f.readFile("EPOCH")), "1")
			},
		},
		{
			name: "init --from migrates a reference board",
			argv: func(t *testing.T, _ *fixture) []string {
				return []string{"init", "--from", referenceBoard(t)}
			},
			code:   0,
			outHas: []string{"migrated 1 ticket(s)"},
			check: func(t *testing.T, f *fixture, _ string) {
				if !f.exists("backlog", "OLD-1") {
					t.Error("the reference ticket did not land in backlog")
				}
			},
		},
		{
			name: "init --db creates the board inside one file",
			prepare: func(f *fixture) {
				f.cwd = t.TempDir() // a project with no board yet
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"init", "--db"} },
			code:   0,
			outHas: []string{": created kanban.db, README.md, EPOCH, LOG"},
			check: func(t *testing.T, f *fixture, _ string) {
				root := filepath.Join(f.cwd, ".agents", "kanban")
				if _, err := os.Stat(filepath.Join(root, "kanban.db")); err != nil {
					t.Fatalf("kanban.db missing: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "backlog")); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("init --db created a column folder: %v", err)
				}
			},
		},
		{
			name: "new writes a full ticket into backlog",
			argv: func(t *testing.T, _ *fixture) []string {
				body := filepath.Join(t.TempDir(), "body.md")
				if err := os.WriteFile(body, []byte(fullBody("NEW-1")), 0o644); err != nil {
					t.Fatal(err)
				}
				return []string{"new", "NEW-1", "--actor", "lead",
					"--title", "add the cli: argv, wording and exit codes",
					"--cluster", "kanban", "--summary", longSummary,
					"--tag", "cli", "--tag", "exit-codes", "--repos", "shopapp", "--body-file", body}
			},
			code:   0,
			outHas: []string{"backlog/NEW-1.md"},
			check: func(t *testing.T, f *fixture, _ string) {
				has(t, "the new ticket", f.ticketText("backlog", "NEW-1"),
					"id: NEW-1", "tags: [cli, exit-codes]", "repos: shopapp", "## Description")
			},
		},
		{
			name: "promote moves a backlog ticket to ready",
			prepare: func(f *fixture) {
				f.fullCard("backlog", "PRO-1")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"promote", "PRO-1", "--actor", "lead"} },
			code:   0,
			outHas: []string{"ready/PRO-1.md"},
		},
		{
			name: "pull claims the best ready ticket",
			prepare: func(f *fixture) {
				f.fullCard("ready", "PUL-1")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"pull", "--actor", "w1"} },
			code:   0,
			outHas: []string{"in-progress/PUL-1.md"},
		},
		{
			name: "review submits an in-progress ticket",
			prepare: func(f *fixture) {
				f.fullCard("in-progress", "REV-1", "\nclaimed_by: dev")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"review", "REV-1", "--actor", "dev", "--note", "look at x"}
			},
			code:   0,
			outHas: []string{"review/REV-1.md"},
		},
		{
			name: "reject returns a review ticket with the verdict",
			prepare: func(f *fixture) {
				f.fullCard("review", "REJ-1", "\nclaimed_by: dev\nauthored_by: dev")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"reject", "REJ-1", "--actor", "rev", "--note", "missing test"}
			},
			code:   0,
			outHas: []string{"in-progress/REJ-1.md"},
		},
		{
			name: "test moves a review ticket under an evidence note",
			prepare: func(f *fixture) {
				f.fullCard("review", "TST-1", "\nclaimed_by: dev\nauthored_by: dev")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"test", "TST-1", "--actor", "qa", "--note", "go test rc=0"}
			},
			code:   0,
			outHas: []string{"testing/TST-1.md"},
		},
		{
			name: "done finishes a testing ticket",
			prepare: func(f *fixture) {
				f.fullCard("testing", "DON-1", "\nclaimed_by: qa\nauthored_by: dev")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"done", "DON-1", "--actor", "qa", "--note", "shipped",
					"--checks", "go test exit 0", "--merge", "abc123"}
			},
			code:   0,
			outHas: []string{"done/DON-1.md"},
			check: func(t *testing.T, f *fixture, _ string) {
				has(t, "the finished ticket", f.ticketText("done", "DON-1"), `result: "shipped"`, "finished_at:")
			},
		},
		{
			name: "block parks a ticket with its cause",
			prepare: func(f *fixture) {
				f.fullCard("ready", "BLO-1")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"block", "BLO-1", "--actor", "lead", "--note", "the CI is red",
					"--on", "WIN-1", "--on", "a push", "--until", "CI is green"}
			},
			code:   0,
			outHas: []string{"blocked/BLO-1.md"},
		},
		{
			name: "unblock returns a blocked ticket to ready",
			prepare: func(f *fixture) {
				f.fullCard("blocked", "UNB-1",
					"\nblocked_reason: \"x\"\nblocked_on: [X-1]\nunblock_when: \"ok\"\nblocked_from: ready")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"unblock", "UNB-1", "--actor", "lead"} },
			code:   0,
			outHas: []string{"ready/UNB-1.md"},
		},
		{
			name: "set edits descriptive fields",
			prepare: func(f *fixture) {
				f.fullCard("backlog", "SET-1")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"set", "SET-1", "--actor", "lead", "title=a new title that fits", "priority=P0"}
			},
			code: 0,
			check: func(t *testing.T, f *fixture, _ string) {
				has(t, "the edited ticket", f.ticketText("backlog", "SET-1"),
					`title: "a new title that fits"`, "priority: P0")
			},
		},
		{
			name: "move repairs any column to any column",
			prepare: func(f *fixture) {
				f.fullCard("backlog", "MOV-1")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"move", "MOV-1", "review", "--actor", "lead", "--note", "repair"}
			},
			code:   0,
			outHas: []string{"review/MOV-1.md"},
		},
		{
			name: "log appends an entry to the Log section",
			prepare: func(f *fixture) {
				f.fullCard("backlog", "LOG-1")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"log", "LOG-1", "--actor", "w1", "--note", "started the work"}
			},
			code: 0,
			check: func(t *testing.T, f *fixture, _ string) {
				has(t, "the ticket's log", f.ticketText("backlog", "LOG-1"), "w1: started the work")
			},
		},
		{
			name: "feature close archives a feature's cards",
			prepare: func(f *fixture) {
				f.card("backlog", "SWR-1", "id: SWR-1\ntitle: \"swr one\"\ntype: docs\ncluster: swr\nsummary: \"one\"")
				f.card("ready", "SWR-2", "id: SWR-2\ntitle: \"swr two\"\ntype: fix\ncluster: swr\nsummary: \"one\"")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"feature", "close", "swr", "--actor", "lead"}
			},
			code:   0,
			outHas: []string{"feature swr closed: 2 cards archived"},
			check: func(t *testing.T, f *fixture, _ string) {
				has(t, "FEATURES", f.readFile("FEATURES"), `"name":"swr"`, `"actor":"lead"`)
			},
		},
		{
			name: "feature reopen brings a closed feature's cards back",
			prepare: func(f *fixture) {
				f.card("backlog", "RPN-1", "id: RPN-1\ntitle: \"swr one\"\ntype: docs\ncluster: swr\nsummary: \"one\"")
				f.card("ready", "RPN-2", "id: RPN-2\ntitle: \"swr two\"\ntype: fix\ncluster: swr\nsummary: \"one\"")
				f.writeFile("FEATURES", `{"closed":[{"name":"swr","actor":"lead","closed_at":"2026-10-15T12:00:00+0300"}]}`+"\n")
			},
			argv: func(*testing.T, *fixture) []string {
				return []string{"feature", "reopen", "swr", "--actor", "lead"}
			},
			code:   0,
			outHas: []string{"feature swr reopened: 2 cards back"},
			check: func(t *testing.T, f *fixture, _ string) {
				if _, err := os.Stat(filepath.Join(f.root, "FEATURES")); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("FEATURES survived the reopen: %v", err)
				}
			},
		},
		{
			name: "list prints one line per ticket with its badges",
			prepare: func(f *fixture) {
				f.fullCard("ready", "LST-1")
				f.card("ready", "LST-2", "id: LST-2\ntitle: \"bare\"\ntype: fix\nexpedite: true\n"+
					"depends_on: [LST-1]\nsummary: \""+longSummary+"\"", fullBody("LST-2"))
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"list"} },
			code:   0,
			outHas: []string{"LST-1", "LST-2", "EXPEDITE", "deps=LST-1(ready)"},
		},
		{
			name: "list takes one column",
			prepare: func(f *fixture) {
				f.fullCard("ready", "COL-1")
				f.fullCard("blocked", "COL-2")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"list", "blocked"} },
			code:   0,
			outHas: []string{"COL-2"},
			check: func(t *testing.T, f *fixture, stdout string) {
				hasNot(t, "stdout", stdout, "COL-1")
			},
		},
		{
			name: "list shows the parked state of a blocked ticket",
			prepare: func(f *fixture) {
				f.fullCard("blocked", "BLK-9",
					"\nblocked_reason: \"waiting on sign-off\"\nblocked_on: [WIN-1]\nunblock_when: \"legal says yes\"\nblocked_from: review")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"list"} },
			code:   0,
			outHas: []string{"blocked: waiting on sign-off", "on: WIN-1", "until: legal says yes", "from review"},
		},
		{
			name: "show prints the ticket whole",
			prepare: func(f *fixture) {
				f.fullCard("ready", "SHO-1")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"show", "SHO-1"} },
			code:   0,
			outHas: []string{"## Description", "title: "},
		},
		{
			name: "status prints the epoch, the lane and the counts",
			prepare: func(f *fixture) {
				f.fullCard("ready", "STA-1", "\nexpedite: true")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"status"} },
			code:   0,
			outHas: []string{"epoch=1 running", "EXPEDITE (fast track): STA-1 (ready)", "backlog: 0", "ready: 1"},
		},
		{
			name: "status names the stop while it is set",
			prepare: func(f *fixture) {
				f.stopped()
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"status"} },
			code:   0,
			outHas: []string{"epoch=2 STOPPED by lead: main is red"},
		},
		{
			name: "check answers the stop-line state and one ticket",
			prepare: func(f *fixture) {
				f.fullCard("backlog", "CHK-1")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"check", "CHK-1"} },
			code:   0,
			outHas: []string{"epoch=1 running", "CHK-1: column=backlog type=docs"},
		},
		{
			name: "lint passes a clean board",
			prepare: func(f *fixture) {
				f.fullCard("ready", "CLE-1")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"lint"} },
			code:   0,
			outHas: []string{"lint: every ticket clears the bar"},
		},
		{
			name: "epoch prints the epoch",
			argv: func(*testing.T, *fixture) []string { return []string{"epoch"} },
			code: 0,
			check: func(t *testing.T, f *fixture, stdout string) {
				equals(t, "stdout", stdout, "1\n")
			},
		},
		{
			name: "stop-line halts the board",
			argv: func(*testing.T, *fixture) []string {
				return []string{"stop-line", "--actor", "lead", "--note", "main is red"}
			},
			code:   0,
			outHas: []string{"STOP-LINE set at epoch 2 by lead: main is red"},
			check: func(t *testing.T, f *fixture, _ string) {
				if _, err := os.Stat(filepath.Join(f.root, "STOP-LINE")); err != nil {
					t.Error("STOP-LINE missing")
				}
				equals(t, "EPOCH", strings.TrimSpace(f.readFile("EPOCH")), "2")
			},
		},
		{
			name: "resume-line clears the stop",
			prepare: func(f *fixture) {
				f.stopped()
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"resume-line", "--actor", "lead"} },
			code:   0,
			outHas: []string{"stop-line cleared by lead (was epoch 2)"},
		},
		{
			name: "export writes a self-contained page",
			prepare: func(f *fixture) {
				f.card("backlog", "EXP-1", "id: EXP-1\ntitle: \"export one\"\ntype: docs\ncluster: kanban\nsummary: \"one\"")
				f.card("ready", "EXP-2", "id: EXP-2\ntitle: \"export two\"\ntype: docs\ncluster: kanban\nsummary: \"one\"")
			},
			argv: func(t *testing.T, _ *fixture) []string {
				return []string{"export", "--out", filepath.Join(t.TempDir(), "board.html")}
			},
			code: 0,
			check: func(t *testing.T, f *fixture, stdout string) {
				if !strings.HasPrefix(stdout, "kanban: wrote ") || !strings.HasSuffix(stdout, " (2 tickets)\n") {
					t.Fatalf("stdout = %q, want the wrote line with the count", stdout)
				}
				line := strings.TrimSuffix(strings.TrimPrefix(stdout, "kanban: wrote "), " (2 tickets)\n")
				page, err := os.ReadFile(line)
				if err != nil {
					t.Fatalf("read the exported page: %v", err)
				}
				for _, want := range []string{"window.__KANBAN__", "EXP-1", "EXP-2", "export one"} {
					if !strings.Contains(string(page), want) {
						t.Errorf("the exported page lacks %q", want)
					}
				}
				if strings.Contains(string(page), "<script src=") {
					t.Error("the exported page references an external script")
				}
			},
		},
		{
			name: "migrate-db converts the folder board in place",
			prepare: func(f *fixture) {
				f.fullCard("backlog", "MDB-1")
			},
			argv:   func(*testing.T, *fixture) []string { return []string{"migrate-db"} },
			code:   0,
			outHas: []string{"migrated 1 tickets into ", "; the folders are kept in "},
			check: func(t *testing.T, f *fixture, _ string) {
				if _, err := os.Stat(filepath.Join(f.root, "kanban.db")); err != nil {
					t.Fatalf("kanban.db missing: %v", err)
				}
				if _, err := os.Stat(filepath.Join(f.root, "backlog")); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("backlog still sits at the root: %v", err)
				}
				// the board reads the database now
				code, stdout, stderr := f.run("show", "MDB-1")
				if code != 0 || !strings.Contains(stdout, "MDB-1 title") {
					t.Fatalf("show after the migration = %d, %q, %q", code, stdout, stderr)
				}
			},
		},
		{
			name: "export-md writes the board back as folders",
			prepare: func(f *fixture) {
				f.fullCard("ready", "EXM-1")
			},
			argv: func(t *testing.T, _ *fixture) []string {
				return []string{"export-md", "--out", filepath.Join(t.TempDir(), "out")}
			},
			code:   0,
			outHas: []string{"exported 1 tickets to "},
			check: func(t *testing.T, f *fixture, stdout string) {
				dir := strings.TrimSpace(strings.TrimPrefix(stdout, "exported 1 tickets to "))
				data, err := os.ReadFile(filepath.Join(dir, "ready", "EXM-1.md"))
				if err != nil {
					t.Fatalf("the exported card: %v", err)
				}
				has(t, "the exported card", string(data), "id: EXM-1", "## Description")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.prepare != nil {
				tt.prepare(f)
			}
			code, stdout, stderr := f.run(tt.argv(t, f)...)
			if code != tt.code {
				t.Errorf("exit = %d, want %d (stdout %q, stderr %q)", code, tt.code, stdout, stderr)
			}
			has(t, "stdout", stdout, tt.outHas...)
			has(t, "stderr", stderr, tt.errHas...)
			if tt.check != nil {
				tt.check(t, f, stdout)
			}
		})
	}
}

// TestUsageErrors pins every usage error's wording and exit code.
func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"promote without --actor", []string{"promote", "X-1"},
			"kanban promote: --actor is required\nrun kanban promote --help\n"},
		{"test without --note", []string{"test", "X-1", "--actor", "qa"},
			"kanban test: --note is required\nrun kanban test --help\n"},
		{"block without --actor and --note", []string{"block", "X-1"},
			"kanban block: --actor and --note are required\nrun kanban block --help\n"},
		{"an unknown flag", []string{"list", "--nope"},
			"kanban list: --nope is not a flag of this command\nrun kanban list --help\n"},
		{"a flag that ends the line without its value", []string{"new", "X-1", "--title"},
			"kanban new: --title needs a value\nrun kanban new --help\n"},
		{"a missing positional", []string{"show"},
			"kanban show: ID is required\nrun kanban show --help\n"},
		{"move without its column", []string{"move", "X-1", "--actor", "lead", "--note", "n"},
			"kanban move: ID and COLUMN are required\nrun kanban move --help\n"},
		{"set without a key=value", []string{"set", "X-1", "--actor", "lead"},
			"kanban set: ID and key=value are required\nrun kanban set --help\n"},
		{"feature alone", []string{"feature"},
			"kanban feature: --actor is required\nrun kanban feature --help\n"},
		{"feature close without --actor", []string{"feature", "close", "swr"},
			"kanban feature: --actor is required\nrun kanban feature --help\n"},
		{"feature without its verb and name", []string{"feature", "--actor", "lead"},
			"kanban feature: close|reopen and FEATURE are required\nrun kanban feature --help\n"},
		{"feature close without its feature name", []string{"feature", "close", "--actor", "lead"},
			"kanban feature: close|reopen and FEATURE are required\nrun kanban feature --help\n"},
		{"a verb that is neither close nor reopen", []string{"feature", "open", "swr", "--actor", "lead"},
			"kanban feature: open is not a verb; use close or reopen\nrun kanban feature --help\n"},
		{"one positional too many", []string{"list", "ready", "done"},
			"kanban list: unexpected argument done\nrun kanban list --help\n"},
		{"a column that is not one", []string{"list", "done2"},
			"kanban list: done2 is not a column; use one of backlog, ready, in-progress, review, testing, blocked, done\n" +
				"run kanban list --help\n"},
		{"a bool flag with a broken value", []string{"status", "--json=maybe"},
			"kanban status: invalid boolean value \"maybe\" for -json: parse error\nrun kanban status --help\n"},
		{"export without --out", []string{"export"},
			"kanban export: --out is required\nrun kanban export --help\n"},
		{"export into a directory that is not there", []string{"export", "--out", "missing-dir/board.html"},
			"kanban export: the directory missing-dir does not exist; create it first\nrun kanban export --help\n"},
		{"export-md without --out", []string{"export-md"},
			"kanban export-md: --out is required\nrun kanban export-md --help\n"},
		{"an unknown command", []string{"frobnicate"},
			"kanban: unknown command: frobnicate\n" + usageLine + "\n"},
		{"no command at all", nil,
			"kanban: unknown command: (none)\n" + usageLine + "\n"},
		{"a --board with no value", []string{"status", "--board"},
			"kanban status: --board is not a flag of this command\nrun kanban status --help\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			code, stdout, stderr := f.run(tt.argv...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (stdout %q, stderr %q)", code, stdout, stderr)
			}
			equals(t, "stderr", stderr, tt.want)
			equals(t, "stdout", stdout, "")
		})
	}
}

// TestNoBoard pins exit 4 and its line, and that a usage error wins over the
// board lookup.
func TestNoBoard(t *testing.T) {
	for _, argv := range [][]string{
		{"status"}, {"list"}, {"epoch"}, {"promote", "X-1", "--actor", "lead"}, {"lint"},
		{"export", "--out", "board.html"},
	} {
		f := newFixture(t)
		f.cwd = t.TempDir() // a directory with no board above it
		code, _, stderr := f.run(argv...)
		if code != 4 {
			t.Errorf("%v exit = %d, want 4 (stderr %q)", argv, code, stderr)
		}
		equals(t, "stderr", stderr, noBoardLine+"\n")
	}
	f := newFixture(t)
	f.cwd = t.TempDir()
	code, _, stderr := f.run("promote", "--nope")
	if code != 2 {
		t.Errorf("a usage error under a missing board exits %d, want 2", code)
	}
	has(t, "stderr", stderr, "is not a flag")
}

// TestRefusals pins exit 1 and where each message lands.
func TestRefusals(t *testing.T) {
	t.Run("a gate refusal lands on stderr", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("ready", "REF-1")
		code, stdout, stderr := f.run("promote", "REF-1", "--actor", "lead")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		equals(t, "stdout", stdout, "")
		has(t, "stderr", stderr, "REF-1: no card in backlog/", "it is in ready/")
	})

	t.Run("show refuses a missing ticket", func(t *testing.T) {
		f := newFixture(t)
		code, _, stderr := f.run("show", "NOPE-1")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stderr", stderr, "NOPE-1: not on the board")
	})

	t.Run("check keeps its report on stdout while stopped", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("ready", "CHK-1")
		f.stopped()
		code, stdout, stderr := f.run("check", "CHK-1")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		equals(t, "stderr", stderr, "")
		has(t, "stdout", stdout, "STOPPED epoch=2", "CHK-1: column=ready")
	})

	t.Run("check names a missing ticket on stdout", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("check", "NOPE-1")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stdout", stdout, "NOPE-1: not on the board")
	})

	t.Run("lint exits 1 on a failure and shows it", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "LNT-1", "id: LNT-1\ntitle: \"thin\"\ntype: docs")
		code, stdout, stderr := f.run("lint")
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stderr %q)", code, stderr)
		}
		has(t, "stdout", stdout, "LNT-1:", "lint: 1 ticket(s) flagged")
	})

	t.Run("lint shows warnings without failing", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "WAR-1", "id: WAR-1\ntitle: \"old style\"\ntype: docs\ncluster: kanban\n"+
			"priority: P2\nest: S\nsummary: \""+longSummary+"\"",
			"# WAR-1\n\nGoal: one line.\n\nSteps:\n1. a\n\nAcceptance:\n- [ ] b\n\n"+
				"Evidence:\n```\ngo test ./...\n```\nexit 0\n")
		code, stdout, _ := f.run("lint")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (warnings never fail); stdout %q", code, stdout)
		}
		has(t, "stdout", stdout, "WAR-1:", "old layout")
	})

	t.Run("lint exits 0 when a card has no feature word", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "NOF-1", "id: NOF-1\ntitle: \"no feature word\"\ntype: docs\n"+
			"priority: P2\nest: M\nsummary: \""+longSummary+"\"", fullBody("NOF-1"))
		code, stdout, stderr := f.run("lint")
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (a missing cluster only warns); stdout %q stderr %q", code, stdout, stderr)
		}
		has(t, "stdout", stdout, "NOF-1:", "warning: no feature word: the card shows under No feature",
			"lint: 1 ticket(s) flagged, 0 failure(s), 1 warning(s)")
	})

	t.Run("a bad actor is refused with exit 1", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("backlog", "ACT-1")
		code, _, stderr := f.run("promote", "ACT-1", "--actor", "a b")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stderr", stderr, "--actor")
	})

	t.Run("new refuses a duplicate id", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("backlog", "DUP-1")
		code, _, stderr := f.run("new", "DUP-1", "--actor", "lead", "--title", "t",
			"--cluster", "kanban", "--summary", longSummary)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stderr", stderr, "already on the board")
	})

	t.Run("set refuses a status field", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("backlog", "SET-1")
		code, _, stderr := f.run("set", "SET-1", "--actor", "lead", "claimed_by=w1")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stderr", stderr, "not a settable field")
	})

	t.Run("feature close refuses a name no card carries", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "FTR-1", "id: FTR-1\ntitle: \"other\"\ntype: docs\ncluster: web\nsummary: \"one\"")
		code, stdout, stderr := f.run("feature", "close", "nope", "--actor", "lead")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		equals(t, "stdout", stdout, "")
		has(t, "stderr", stderr, "no feature nope on this board")
	})

	t.Run("init --db and --from do not combine", func(t *testing.T) {
		f := newFixture(t)
		code, _, stderr := f.run("init", "--db", "--from", referenceBoard(t))
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stderr", stderr, "--db and --from do not combine; init the folder board, then run migrate-db")
	})

	t.Run("migrate-db refuses a board that already holds kanban.db", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("backlog", "SEC-1")
		if code, _, stderr := f.run("migrate-db"); code != 0 {
			t.Fatalf("the first migrate-db exited %d (stderr %q)", code, stderr)
		}
		code, stdout, stderr := f.run("migrate-db")
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		equals(t, "stdout", stdout, "")
		has(t, "stderr", stderr, "already holds kanban.db")
	})

	t.Run("export-md refuses a directory that is not empty", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("ready", "EXM-2")
		out := filepath.Join(t.TempDir(), "out")
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "keep.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := f.run("export-md", "--out", out)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		equals(t, "stdout", stdout, "")
		has(t, "stderr", stderr, "not empty")
	})

	t.Run("an unexpected error prints kanban: and exits 1", func(t *testing.T) {
		f := newFixture(t)
		locked := filepath.Join(f.root, "backlog") // the column new must write into
		if err := os.Chmod(locked, 0o555); err != nil {
			t.Skipf("cannot lock the column: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		code, _, stderr := f.run("new", "ERR-1", "--actor", "lead", "--title", "t",
			"--cluster", "kanban", "--summary", longSummary)
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		has(t, "stderr", stderr, "kanban: ")
	})
}

// TestLintPassesABoardWhoseDoneTicketsBreakOnlyFieldLimits pins the migrated
// board's escape: a done ticket is never edited, so its stale field limits warn
// and never flip the exit code; an open ticket still fails the same limits.
func TestLintPassesABoardWhoseDoneTicketsBreakOnlyFieldLimits(t *testing.T) {
	f := newFixture(t)
	f.card("done", "OLD-1", "id: OLD-1\ntitle: \""+strings.Repeat("x", 120)+"\"\ncluster: kanban\n"+
		"summary: \""+longSummary+"\"\ntype: docs\npriority: P2\nest: M", fullBody("OLD-1"))
	f.fullCard("ready", "OPEN-1")
	code, stdout, stderr := f.run("lint")
	if code != 0 {
		t.Fatalf("exit = %d, want 0: only a done ticket's field limits are broken (stderr %q)", code, stderr)
	}
	has(t, "stdout", stdout, "OLD-1:", "warning: title is 120 characters", "(done tickets are not edited)",
		"lint: 1 ticket(s) flagged, 0 failure(s), 1 warning(s)")
	hasNot(t, "stdout", stdout, "OPEN-1")
}

// TestJSON pins the --json documents: their shape, their two-space indent and
// their trailing newline.
func TestJSON(t *testing.T) {
	indent := func(t *testing.T, doc string) {
		t.Helper()
		if !strings.HasSuffix(doc, "\n") {
			t.Errorf("json = %q, want a trailing newline", doc)
		}
		if !strings.Contains(doc, "\n  ") {
			t.Errorf("json = %q, want a two-space indent", doc)
		}
	}

	t.Run("list prints one card per element", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("ready", "JSL-1")
		f.card("ready", "JSL-2", "id: JSL-2\ntitle: \"deps\"\ntype: fix\ncluster: kanban\n"+
			"depends_on: [JSL-1, NOPE-9, plain text]\nsummary: \""+longSummary+"\"", fullBody("JSL-2"))
		code, stdout, stderr := f.run("list", "--json")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		var cards []board.Card
		if err := json.Unmarshal([]byte(stdout), &cards); err != nil {
			t.Fatalf("decode: %v (%q)", err, stdout)
		}
		if len(cards) != 2 {
			t.Fatalf("cards = %d, want 2", len(cards))
		}
		deps := map[string]string{}
		for _, d := range cards[1].Deps {
			deps[d.ID] = d.State
		}
		if deps["JSL-1"] != "ready" || deps["NOPE-9"] != "missing" || deps["plain text"] != "text" {
			t.Errorf("deps = %v", deps)
		}
		if cards[0].Title != "JSL-1 title" || cards[0].Badges == nil || cards[0].Tags == nil {
			t.Errorf("card = %+v", cards[0])
		}
		indent(t, stdout)
	})

	t.Run("list prints an empty array for an empty board", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("list", "--json")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		equals(t, "stdout", stdout, "[]\n")
	})

	t.Run("show prints the card, the fields and the body", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("review", "JSH-1", "\nclaimed_by: w1")
		code, stdout, stderr := f.run("show", "JSH-1", "--json")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		var doc struct {
			Card   board.Card        `json:"card"`
			Fields map[string]string `json:"fields"`
			Body   string            `json:"body"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("decode: %v (%q)", err, stdout)
		}
		if doc.Card.ID != "JSH-1" || doc.Card.Column != "review" || doc.Card.Holder != "w1" {
			t.Errorf("card = %+v", doc.Card)
		}
		if doc.Fields["cluster"] != "kanban" {
			t.Errorf("fields = %v", doc.Fields)
		}
		has(t, "body", doc.Body, "## Description")
		indent(t, stdout)
	})

	t.Run("show flags a closed feature's card", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("review", "SHC-1")
		f.card("review", "SHC-2", "id: SHC-2\ntitle: \"open\"\ncluster: web\ntype: docs")
		f.writeFile("FEATURES", `{"closed":[{"name":"kanban","actor":"lead","closed_at":"2026-10-15T12:00:00+0300"}]}`+"\n")
		code, stdout, stderr := f.run("show", "SHC-1")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		if first := strings.SplitN(stdout, "\n", 2)[0]; first != "Feature closed: kanban" {
			t.Errorf("first line = %q, want the closed-feature line", first)
		}
		code, stdout, _ = f.run("show", "SHC-2")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if strings.HasPrefix(stdout, "Feature closed:") {
			t.Errorf("an open feature's card prints %q first", stdout)
		}
	})

	t.Run("show --json marks a closed feature's card", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("review", "JHC-1")
		f.writeFile("FEATURES", `{"closed":[{"name":"kanban","actor":"lead","closed_at":"2026-10-15T12:00:00+0300"}]}`+"\n")
		code, stdout, stderr := f.run("show", "JHC-1", "--json")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		var doc struct {
			Card struct {
				FeatureClosed bool `json:"feature_closed"`
			} `json:"card"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("decode: %v (%q)", err, stdout)
		}
		if !doc.Card.FeatureClosed {
			t.Errorf("feature_closed = false, want true for a closed feature's card")
		}
		indent(t, stdout)
	})

	t.Run("status prints the epoch, the columns and the lane", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("ready", "JST-1", "\nexpedite: true")
		f.fullCard("ready", "JST-2", "\nexpedite: true\ndepends_on: [JST-1]")
		code, stdout, stderr := f.run("status", "--json")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		var doc struct {
			Epoch    int `json:"epoch"`
			StopLine *struct {
				Epoch int    `json:"epoch"`
				Actor string `json:"actor"`
				Note  string `json:"note"`
			} `json:"stopLine"`
			Columns []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Count int    `json:"count"`
			} `json:"columns"`
			Expedite []struct {
				ID      string `json:"id"`
				Column  string `json:"column"`
				WaitsOn string `json:"waitsOn"`
			} `json:"expedite"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("decode: %v (%q)", err, stdout)
		}
		if doc.Epoch != 1 || doc.StopLine != nil {
			t.Errorf("epoch/stop = %+v", doc)
		}
		if len(doc.Columns) != 7 || doc.Columns[0].ID != "backlog" || doc.Columns[0].Title != "Backlog" {
			t.Errorf("columns = %+v", doc.Columns)
		}
		if len(doc.Expedite) != 2 || doc.Expedite[0].ID != "JST-1" || doc.Expedite[0].WaitsOn != "" {
			t.Errorf("expedite = %+v", doc.Expedite)
		}
		if doc.Expedite[1].ID != "JST-2" || doc.Expedite[1].WaitsOn != "JST-1" {
			t.Errorf("expedite = %+v", doc.Expedite)
		}
		indent(t, stdout)
	})

	t.Run("lint prints its counts and every problem", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "JLN-1", "id: JLN-1\ntitle: \"thin\"\ntype: docs")
		code, stdout, stderr := f.run("lint", "--json")
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stderr %q)", code, stderr)
		}
		var doc struct {
			Failures int `json:"failures"`
			Warnings int `json:"warnings"`
			Tickets  []struct {
				ID       string `json:"id"`
				Column   string `json:"column"`
				Problems []struct {
					Severity string `json:"severity"`
					Rule     string `json:"rule"`
					Message  string `json:"message"`
				} `json:"problems"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("decode: %v (%q)", err, stdout)
		}
		if len(doc.Tickets) != 1 || doc.Tickets[0].ID != "JLN-1" || doc.Tickets[0].Column != "ready" {
			t.Fatalf("tickets = %+v", doc.Tickets)
		}
		if doc.Failures == 0 || doc.Warnings == 0 || len(doc.Tickets[0].Problems) == 0 {
			t.Errorf("counts = %d failures, %d warnings, %d problems",
				doc.Failures, doc.Warnings, len(doc.Tickets[0].Problems))
		}
		indent(t, stdout)
	})

	t.Run("lint prints empty arrays for a clean board", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("lint", "--json")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		has(t, "stdout", stdout, `"failures": 0`, `"tickets": []`)
	})
}

// TestHelp pins the help surface: the command list, one command's synopsis and
// flags, and the positional help of new.
func TestHelp(t *testing.T) {
	t.Run("help, --help and -h print the command list", func(t *testing.T) {
		f := newFixture(t)
		for _, word := range []string{"help", "--help", "-h"} {
			code, stdout, stderr := f.run(word)
			if code != 0 {
				t.Fatalf("%s exit = %d, stderr %q", word, code, stderr)
			}
			has(t, "stdout", stdout, "usage: kanban <command> [flags]", "not UTF-8", "promote", "resume-line")
		}
	})

	t.Run("a command's --help prints its synopsis and flags", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, stderr := f.run("list", "--help")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		has(t, "stdout", stdout, "usage: kanban list [COLUMN] [flags]", "--json", "--board")
	})

	t.Run("new's help names the id rule", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("new", "--help")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		has(t, "stdout", stdout, "usage: kanban new ID [flags]", "single dashes", "(required)")
		hasNot(t, "stdout", stdout, "<AREA>-<NAME>")
	})

	t.Run("a command's -h prints the same help", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("status", "-h")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		has(t, "stdout", stdout, "usage: kanban status [flags]", "--board")
	})
}

// TestCLIBoardAndInputRules holds the cli-level rows of the board suite: --board
// and $KANBAN_BOARD pointing the CLI at any project, the missing-board error,
// the actor every flow command requires, and the utf8 rules the help names.
func TestCLIBoardAndInputRules(t *testing.T) {
	t.Run("test_board_option_and_env_point_the_skill_script_at_any_project", func(t *testing.T) {
		f := newFixture(t)
		project := f.cwd
		f.fullCard("ready", "A-1")
		elsewhere := t.TempDir()

		// --board before the subcommand
		f.cwd = elsewhere
		code, stdout, stderr := f.run("--board", f.root, "status")
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		has(t, "stdout", stdout, "ready: 1")

		// $KANBAN_BOARD
		f.env = map[string]string{"KANBAN_BOARD": f.root}
		code, stdout, _ = f.run("status")
		if code != 0 || !strings.Contains(stdout, "ready: 1") {
			t.Errorf("env status = %d, %q", code, stdout)
		}
		f.env = nil

		// the working directory's project board
		f.cwd = project
		code, stdout, _ = f.run("status")
		if code != 0 || !strings.Contains(stdout, "ready: 1") {
			t.Errorf("cwd status = %d, %q", code, stdout)
		}

		// a mutation through the resolved board lands in that board
		f.cwd = elsewhere
		f.env = map[string]string{"KANBAN_BOARD": f.root}
		if code, _, _ := f.run("pull", "--actor", "w1"); code != 0 {
			t.Fatalf("pull exit = %d", code)
		}
		if !f.exists("in-progress", "A-1") {
			t.Error("the mutation did not land in the board")
		}
	})

	t.Run("test_board_option_works_before_and_after_the_subcommand", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("ready", "A-1")
		f.cwd = t.TempDir() // no board of its own

		code, stdout, stderr := f.run("list", "--board", f.root) // after
		if code != 0 {
			t.Fatalf("exit = %d, stderr %q", code, stderr)
		}
		has(t, "stdout", stdout, "A-1")

		if code, _, _ := f.run("pull", "--actor", "w1", "--board", f.root); code != 0 {
			t.Fatalf("pull with a trailing --board failed")
		}
		if !f.exists("in-progress", "A-1") {
			t.Error("the mutation did not land in the explicit board")
		}

		code, stdout, _ = f.run("--board", f.root, "status") // before
		if code != 0 || !strings.Contains(stdout, "in-progress: 1") {
			t.Errorf("status with a leading --board = %d, %q", code, stdout)
		}

		// given twice, the later value wins
		other := newFixture(t)
		other.fullCard("ready", "B-2")
		code, stdout, _ = f.run("--board", other.root, "status", "--board", f.root)
		if code != 0 || !strings.Contains(stdout, "in-progress: 1") || strings.Contains(stdout, "ready: 1") {
			t.Errorf("the later --board did not win: %q", stdout)
		}

		// the subcommand help keeps --board on it
		code, stdout, _ = f.run("list", "--help")
		if code != 0 || !strings.Contains(stdout, "--board") {
			t.Errorf("list --help = %d, %q", code, stdout)
		}

		// the --board=path form reads on either side too
		code, stdout, _ = f.run("--board="+f.root, "epoch")
		if code != 0 || stdout != "1\n" {
			t.Errorf("--board= form = %d, %q", code, stdout)
		}
		code, stdout, _ = f.run("epoch", "--board="+f.root)
		if code != 0 || stdout != "1\n" {
			t.Errorf("trailing --board= form = %d, %q", code, stdout)
		}
	})

	t.Run("test_missing_board_is_a_clear_error_not_a_traceback", func(t *testing.T) {
		f := newFixture(t)
		f.cwd = t.TempDir()
		code, _, stderr := f.run("status")
		if code != 4 {
			t.Fatalf("exit = %d, want 4", code)
		}
		has(t, "stderr", stderr, "init")
		hasNot(t, "stderr", stderr, "panic")
	})

	// the usage halves: exit 2, the ticket unmoved, no null actor in LOG.
	t.Run("test_block_and_promote_require_an_actor", func(t *testing.T) {
		f := newFixture(t)
		f.fullCard("backlog", "ACT-1")
		code, _, _ := f.run("promote", "ACT-1")
		if code != 2 {
			t.Errorf("promote without --actor exits %d, want 2", code)
		}
		if !f.exists("backlog", "ACT-1") {
			t.Error("promote moved the ticket without an actor")
		}
		code, _, _ = f.run("block", "ACT-1", "--note", "x")
		if code != 2 {
			t.Errorf("block without --actor exits %d, want 2", code)
		}
		if !f.exists("backlog", "ACT-1") {
			t.Error("block moved the ticket without an actor")
		}
		if code, _, _ := f.run("block", "ACT-1", "--actor", "lead", "--note", "x"); code != 0 {
			t.Fatalf("block with an actor exits %d", code)
		}
		hasNot(t, "LOG", f.readFile("LOG"), `"actor":null`)
	})

	// the help half; the board package holds the id rules.
	t.Run("test_every_file_open_names_utf8_and_ids_allow_single_dashes_only", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("new", "--help")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		has(t, "stdout", stdout, "single dashes")
		hasNot(t, "stdout", stdout, "<AREA>-<NAME>")
	})

	// the top-level help half.
	t.Run("test_top_help_names_the_utf8_check", func(t *testing.T) {
		f := newFixture(t)
		code, stdout, _ := f.run("--help")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		has(t, "stdout", stdout, "not UTF-8")
	})
}

// TestFlagsParseInterleaved proves flags and positionals may interleave, that
// both --flag value and --flag=value read, and that a repeated flag collects
// every value.
func TestFlagsParseInterleaved(t *testing.T) {
	f := newFixture(t)
	f.fullCard("backlog", "ORD-1")
	code, stdout, stderr := f.run("set", "ORD-1", "--actor=lead", "title=first try", "priority=P1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr)
	}
	has(t, "stdout", stdout, "backlog/ORD-1.md")
	has(t, "the ticket", f.ticketText("backlog", "ORD-1"), `title: "first try"`, "priority: P1")

	f.fullCard("ready", "ORD-2")
	code, _, stderr = f.run("block", "ORD-2", "--actor", "lead", "--note", "n",
		"--on", "WIN-1", "--on", "a push", "--until", "green")
	if code != 0 {
		t.Fatalf("block exit = %d, stderr %q", code, stderr)
	}
	has(t, "the ticket", f.ticketText("blocked", "ORD-2"), `blocked_on: [WIN-1, "a push"]`)
}

// TestDefaultWorld runs the real world's getters once.
func TestDefaultWorld(t *testing.T) {
	w := DefaultWorld("dev")
	if _, ok := w.Getenv("PATH"); !ok {
		t.Error("Getenv cannot read the environment")
	}
	if w.Cwd() == "" {
		t.Error("Cwd is empty")
	}
	if w.Now().Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Now = %v", w.Now())
	}
	if w.Version != "dev" {
		t.Errorf("Version = %q", w.Version)
	}
}
