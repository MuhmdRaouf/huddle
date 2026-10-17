// Package cli is the kanban command line: argv parsing, help text, output
// wording and exit codes over the board package. Exit codes: 0 ok, 1 refused,
// 2 usage, 4 no board, 6 no server.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// World is every effect one command run can have; the binary builds the real
// ones, the tests pin them.
type World struct {
	Getenv func(string) (string, bool)
	Cwd    func() string
	Now    func() time.Time
	// Version prints in the server's health answer.
	Version string
	// ServerFetch answers one board-server request; nil is the real loopback
	// client.
	ServerFetch func(ctx context.Context, method, url string, header map[string]string) (int, []byte, bool)
	// SpawnServe starts the detached serve; nil starts this binary.
	SpawnServe func(command string, args []string, logFile string) int64
	// OpenBrowser opens one url; nil is the platform opener.
	OpenBrowser func(url string)
	// Sleep waits between polls; nil really sleeps.
	Sleep func(ms int64)
}

// DefaultWorld is the process's world: the real environment, working directory
// and clock, with the server version named.
func DefaultWorld(version string) World {
	return World{
		Getenv: os.LookupEnv,
		Cwd: func() string {
			dir, err := os.Getwd()
			if err != nil {
				return "."
			}
			return dir
		},
		Now:     time.Now,
		Version: version,
	}
}

// Flag kinds: a string, a bool, or a repeatable string list.
const (
	kindString  = "string"
	kindBool    = "bool"
	kindStrings = "strings"
)

// flagDef is one flag of one command.
type flagDef struct {
	name     string
	help     string
	kind     string
	required bool
}

// positional is one positional argument's name and help.
type positional struct{ name, help string }

// command is one subcommand's shape: its usage form, its description, its
// flags and its positionals.
type command struct {
	word        string
	use         string // the usage form after "kanban "
	short       string
	defs        []flagDef
	positionals []positional
	min         int // positionals required
	max         int // positionals allowed; -1 for any number
}

const idHelp = "a letter, then A-Z/0-9 groups joined by single dashes (AUTH-TOKEN), stable forever"
const actorHelp = "your short actor id (fix1, lead); it goes into LOG"
const columnHelp = "one of backlog, ready, in-progress, review, testing, blocked, done"
const boardHelp = "the board directory (default: found from the working directory up)"

// Words answers every command word the cli runs, sorted.
func Words() []string {
	words := make([]string, 0, len(commands))
	for word := range commands {
		words = append(words, word)
	}
	sort.Strings(words)
	return words
}

// commands is the surface: every board command, with the flags and positionals
// its runner reads.
var commands = map[string]*command{
	"init": {
		word: "init", use: "init", min: 0, max: 0,
		short: "create the board at .agents/kanban: the seven columns, README, EPOCH and LOG",
		defs: []flagDef{
			{name: "from", kind: kindString,
				help: "migrate this reference board (.agents/backlog) into the new one"},
			{name: "db", kind: kindBool,
				help: "create the board inside one kanban.db file, with no column folders"},
		},
	},
	"new": {
		word: "new", use: "new ID", min: 1, max: 1,
		short:       "write a full ticket into backlog, or into ready with --ready or --expedite",
		positionals: []positional{{"ID", idHelp}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "title", kind: kindString, required: true, help: "one line, what and where (max 90 chars)"},
			{name: "cluster", kind: kindString, required: true,
				help: "one short lowercase word or two: the group the ticket belongs to"},
			{name: "summary", kind: kindString, required: true,
				help: "2-4 plain sentences (min 80 chars): what changes, why, what done looks like"},
			{name: "type", kind: kindString,
				help: "fix, feature, test, docs, ops, decision, chore (default feature)"},
			{name: "priority", kind: kindString, help: "P0..P3 (default P2)"},
			{name: "est", kind: kindString, help: "S, M or L (default M)"},
			{name: "tag", kind: kindStrings,
				help: "repeatable; lowercase words joined by dashes, at most 8 of 24 characters"},
			{name: "depends", kind: kindStrings,
				help: "repeatable; ticket ids must exist, plain text is allowed with a warning"},
			{name: "repos", kind: kindString, help: "repositories the ticket touches, free text"},
			{name: "expedite", kind: kindBool,
				help: "fast track: lands in ready, pulled before everything; the gates stay"},
			{name: "ready", kind: kindBool,
				help: "write into ready instead of backlog (the ticket bar applies)"},
			{name: "force", kind: kindBool, help: "override the ticket bar (logged)"},
			{name: "body-file", kind: kindString, help: "full body from a file, instead of the skeleton"},
		},
	},
	"promote": {
		word: "promote", use: "promote ID", min: 1, max: 1,
		short:       "move a backlog ticket to ready, under the ticket bar",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "force", kind: kindBool, help: "override the ticket bar (logged)"},
		},
	},
	"pull": {
		word: "pull", use: "pull", min: 0, max: 0,
		short: "claim the best ready ticket (expedite, then priority), or --id one",
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "id", kind: kindString, help: "claim this ready ticket instead of the best one"},
			{name: "branch", kind: kindString, help: "record the working branch on the ticket"},
			{name: "worktree", kind: kindString, help: "record the worktree path on the ticket"},
			{name: "force", kind: kindBool,
				help: "pull a normal --id ticket past a waiting expedite one (logged)"},
		},
	},
	"review": {
		word: "review", use: "review ID", min: 1, max: 1,
		short:       "submit an in-progress ticket to review",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, help: "what the reviewer should look at; appends to review_note"},
		},
	},
	"reject": {
		word: "reject", use: "reject ID", min: 1, max: 1,
		short:       "return a review ticket to in-progress with the verdict",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, help: "the verdict; appends to review_note"},
		},
	},
	"test": {
		word: "test", use: "test ID", min: 1, max: 1,
		short:       "move a review ticket to testing under a verifier's evidence note",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, required: true, help: "the commands run and their exit codes"},
		},
	},
	"done": {
		word: "done", use: "done ID", min: 1, max: 1,
		short:       "finish a ticket (implementation tickets from testing, not by the author)",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "checks", kind: kindString, help: "the checks that passed, with exit codes"},
			{name: "merge", kind: kindString, help: "the verified merge (commit or MR)"},
			{name: "note", kind: kindString, help: "the result, one or two sentences"},
		},
	},
	"block": {
		word: "block", use: "block ID", min: 1, max: 1,
		short:       "park a ticket with its cause and unblock condition",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, required: true, help: "the cause, one sentence"},
			{name: "on", kind: kindStrings, help: "what it waits on: a ticket id or plain text; repeatable"},
			{name: "until", kind: kindString, help: "how anyone can tell it is unblocked"},
		},
	},
	"unblock": {
		word: "unblock", use: "unblock ID", min: 1, max: 1,
		short:       "return a blocked ticket to ready, clearing its holder",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, help: "why it is unblocked (LOG only)"},
		},
	},
	"set": {
		word: "set", use: "set ID key=value ...", min: 2, max: -1,
		short: "edit a ticket's descriptive fields (key=value ...)",
		positionals: []positional{
			{"ID", "the ticket's id"},
			{"key=value", "one of cluster, summary, title, type, priority, est, branch, worktree, depends_on, repos, blocked_on, unblock_when"},
		},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
		},
	},
	"move": {
		word: "move", use: "move ID COLUMN", min: 2, max: 2,
		short: "the lead's repair: any column to any column, always logged as forced",
		positionals: []positional{
			{"ID", "the ticket's id"},
			{"COLUMN", columnHelp},
		},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, required: true, help: "why the repair moves the ticket"},
		},
	},
	"log": {
		word: "log", use: "log ID", min: 1, max: 1,
		short:       "append one entry to a ticket's Log section",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, required: true, help: "the entry's text: evidence, findings, hand-offs"},
		},
	},
	"list": {
		word: "list", use: "list [COLUMN]", min: 0, max: 1,
		short:       "one line per ticket: badges and dependency states",
		positionals: []positional{{"COLUMN", columnHelp}},
		defs:        []flagDef{{name: "json", kind: kindBool, help: "print one JSON document instead of lines"}},
	},
	"show": {
		word: "show", use: "show ID", min: 1, max: 1,
		short:       "one ticket whole: its fields and its body",
		positionals: []positional{{"ID", "the ticket's id"}},
		defs:        []flagDef{{name: "json", kind: kindBool, help: "print one JSON document instead of the ticket text"}},
	},
	"status": {
		word: "status", use: "status", min: 0, max: 0,
		short: "epoch, stop-line, the expedite lane and the per-column counts",
		defs:  []flagDef{{name: "json", kind: kindBool, help: "print one JSON document instead of lines"}},
	},
	"check": {
		word: "check", use: "check [ID]", min: 0, max: 1,
		short: "the stop-line state, plus one ticket's column, type and gates",
		positionals: []positional{
			{"ID", "one ticket's column, type and gates; without it, just the stop-line state"},
		},
	},
	"lint": {
		word: "lint", use: "lint", min: 0, max: 0,
		short: "every ticket against the bar; exit 1 when any fails",
		defs:  []flagDef{{name: "json", kind: kindBool, help: "print one JSON document instead of lines"}},
	},
	"epoch": {
		word: "epoch", use: "epoch", min: 0, max: 0,
		short: "print the coordination epoch",
	},
	"stop-line": {
		word: "stop-line", use: "stop-line", min: 0, max: 0,
		short: "halt the board: epoch +1 and the STOP-LINE record",
		defs: []flagDef{
			{name: "actor", kind: kindString, required: true, help: actorHelp},
			{name: "note", kind: kindString, required: true, help: "the evidence: what is broken"},
		},
	},
	"resume-line": {
		word: "resume-line", use: "resume-line", min: 0, max: 0,
		short: "clear the stop-line after verified green",
		defs:  []flagDef{{name: "actor", kind: kindString, required: true, help: actorHelp}},
	},
	"start": {
		word: "start", use: "start", min: 0, max: 0,
		short: "ensure the board's server and print its address",
		defs: []flagDef{
			{name: "port", kind: kindString,
				help: "pin one port; 0 lets the OS pick (default: 18800 up to 18849, then the OS)"},
			{name: "no-open", kind: kindBool, help: "do not open the browser"},
		},
	},
	"stop": {
		word: "stop", use: "stop", min: 0, max: 0,
		short: "stop the board's server",
	},
	"open": {
		word: "open", use: "open", min: 0, max: 0,
		short: "print the board's address and open it in the browser",
	},
	"refresh": {
		word: "refresh", use: "refresh", min: 0, max: 0,
		short: "rescan the board now through the running server",
	},
	"export": {
		word: "export", use: "export", min: 0, max: 0,
		short: "write the board as one self-contained HTML page",
		defs: []flagDef{
			{name: "out", kind: kindString, required: true,
				help: "the page to write; its directory must exist"},
		},
	},
	"migrate-db": {
		word: "migrate-db", use: "migrate-db", min: 0, max: 0,
		short: "convert the folder board in place into kanban.db, keeping the folders aside",
	},
	"export-md": {
		word: "export-md", use: "export-md", min: 0, max: 0,
		short: "write the board back out as Markdown folders",
		defs: []flagDef{
			{name: "out", kind: kindString, required: true,
				help: "the directory to write; it must not exist, or must be empty"},
		},
	},
	"serve": {
		word: "serve", use: "serve", min: 0, max: 0,
		short: "the foreground server kanban start runs",
		defs: []flagDef{
			{name: "port", kind: kindString,
				help: "pin one port; 0 lets the OS pick (default: 18800 up to 18849, then the OS)"},
		},
	},
	"hook": {
		word: "hook", use: "hook session-start", min: 1, max: 1,
		short:       "the Claude Code session hook: ensure the board's server, then name the board",
		positionals: []positional{{"session-start", "the hook Claude Code runs when a session starts"}},
	},
}

// serverCommands are the five commands that drive the board's server instead
// of the board's files.
var serverCommands = map[string]bool{
	"start": true, "stop": true, "open": true, "refresh": true, "serve": true,
}

// Run is one invocation; argv[0] is the command word. The returned int is the
// process exit code: 0 ok, 1 refused or failed, 2 usage, 4 no board, 6 no
// server.
func Run(ctx context.Context, w World, argv []string, stdout, stderr io.Writer) int {
	boardPath, rest := extractBoard(argv)
	command := ""
	if len(rest) > 0 {
		command, rest = rest[0], rest[1:]
	}
	switch command {
	case "help", "--help", "-h":
		printTopHelp(stdout)
		return 0
	}
	cmd, ok := commands[command]
	if !ok {
		fmt.Fprintf(stderr, "kanban: unknown command: %s\n%s\n", commandWord(command), usage)
		return 2
	}
	v, positional, err := parseCommand(cmd, rest)
	if err != nil {
		if errors.Is(err, errShowHelp) {
			printCommandHelp(stdout, cmd)
			return 0
		}
		usageError(stderr, cmd, err.Error())
		return 2
	}
	if command == "list" && len(positional) == 1 && !isColumn(positional[0]) {
		usageError(stderr, cmd, positional[0]+" is not a column; use one of "+strings.Join(board.Columns, ", "))
		return 2
	}
	if serverCommands[command] {
		return runServerCommand(ctx, w, command, v, boardPath, stdout, stderr)
	}
	if command == "hook" {
		return runHook(ctx, w, positional, boardPath, stdout, stderr)
	}
	if command == "init" {
		if v.boolean("db") {
			if v.str("from") != "" {
				refusal := &board.Refused{Message: "--db and --from do not combine; init the folder board, then run migrate-db"}
				return answer(stdout, stderr, nil, refusal, false)
			}
			lines, err := board.InitDB(ctx, initRoot(ctx, w, boardPath), w.Now())
			return answer(stdout, stderr, lines, err, false)
		}
		lines, err := board.Init(ctx, initRoot(ctx, w, boardPath), v.str("from"), w.Now())
		return answer(stdout, stderr, lines, err, false)
	}
	b, closeBoard, err := boardAt(ctx, w, boardPath)
	if err != nil {
		fmt.Fprintln(stderr, noBoardLine)
		return 4
	}
	defer closeBoard()
	switch command {
	case "new":
		n := board.NewTicket{
			Actor: v.str("actor"), ID: at(positional, 0), Title: v.str("title"),
			Cluster: v.str("cluster"), Summary: v.str("summary"), Type: v.str("type"),
			Priority: v.str("priority"), Est: v.str("est"), Repos: v.str("repos"),
			Tags: v.list("tag"), DependsOn: v.list("depends"),
			Expedite: v.boolean("expedite"), Ready: v.boolean("ready"),
			Force: v.boolean("force"), BodyFile: v.str("body-file"),
		}
		lines, err := b.New(ctx, n)
		return answer(stdout, stderr, lines, err, false)
	case "promote":
		lines, err := b.Promote(ctx, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Force: v.boolean("force")})
		return answer(stdout, stderr, lines, err, false)
	case "pull":
		lines, err := b.Pull(ctx, board.Args{
			Actor: v.str("actor"), ID: v.str("id"), Branch: v.str("branch"),
			Worktree: v.str("worktree"), Force: v.boolean("force")})
		return answer(stdout, stderr, lines, err, false)
	case "review", "reject", "unblock":
		lines, err := flowByID(ctx, b, command, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Note: v.str("note")})
		return answer(stdout, stderr, lines, err, false)
	case "test":
		lines, err := b.Test(ctx, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Note: v.str("note")})
		return answer(stdout, stderr, lines, err, false)
	case "done":
		lines, err := b.Done(ctx, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Note: v.str("note"),
			Checks: v.str("checks"), Merge: v.str("merge")})
		return answer(stdout, stderr, lines, err, false)
	case "block":
		lines, err := b.Block(ctx, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Note: v.str("note"),
			On: v.list("on"), Until: v.str("until")})
		return answer(stdout, stderr, lines, err, false)
	case "set":
		lines, err := b.Set(ctx, v.str("actor"), at(positional, 0), positional[1:])
		return answer(stdout, stderr, lines, err, false)
	case "move":
		lines, err := b.Move(ctx, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Note: v.str("note"),
			Column: at(positional, 1)})
		return answer(stdout, stderr, lines, err, false)
	case "log":
		lines, err := b.Log(ctx, board.Args{
			Actor: v.str("actor"), ID: at(positional, 0), Note: v.str("note")})
		return answer(stdout, stderr, lines, err, false)
	case "stop-line":
		lines, err := b.StopLine(ctx, board.Args{Actor: v.str("actor"), Note: v.str("note")})
		return answer(stdout, stderr, lines, err, false)
	case "resume-line":
		lines, err := b.ResumeLine(ctx, board.Args{Actor: v.str("actor")})
		return answer(stdout, stderr, lines, err, false)
	case "list":
		cards, err := b.List(ctx, at(positional, 0))
		if err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 1
		}
		if v.boolean("json") {
			if cards == nil {
				cards = []board.Card{}
			}
			writeJSON(stdout, cards)
			return 0
		}
		for _, c := range cards {
			fmt.Fprintln(stdout, listLine(c))
		}
		return 0
	case "show":
		sh, err := b.Show(ctx, at(positional, 0))
		if err != nil {
			return answer(stdout, stderr, nil, err, false)
		}
		if v.boolean("json") {
			writeJSON(stdout, showJSON{Card: sh.Card, Fields: sh.Fields, Body: sh.Body})
			return 0
		}
		fmt.Fprint(stdout, sh.Text)
		return 0
	case "status":
		st, err := b.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 1
		}
		if v.boolean("json") {
			writeJSON(stdout, statusJSONOf(st))
			return 0
		}
		printLines(stdout, statusLines(st))
		return 0
	case "check":
		lines, err := b.Check(ctx, at(positional, 0))
		// check's refusal is part of its report: the refusing line is among
		// the lines it answers, so it stays on stdout.
		return answer(stdout, stderr, lines, err, true)
	case "lint":
		report, err := b.Lint(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 1
		}
		if v.boolean("json") {
			doc := lintJSONOf(report)
			writeJSON(stdout, doc)
			if doc.Failures > 0 {
				return 1
			}
			return 0
		}
		lines, failures := lintLines(report)
		printLines(stdout, lines)
		if failures > 0 {
			return 1
		}
		return 0
	case "epoch":
		st, err := b.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, strconv.Itoa(st.Epoch))
		return 0
	case "export":
		return runExport(ctx, b, v.str("out"), w.Now, stdout, stderr)
	case "migrate-db":
		lines, err := board.MigrateDB(ctx, b)
		return answer(stdout, stderr, lines, err, false)
	case "export-md":
		lines, err := board.ExportMarkdown(ctx, b, v.str("out"))
		return answer(stdout, stderr, lines, err, false)
	}
	fmt.Fprintf(stderr, "kanban: unknown command: %s\n%s\n", commandWord(command), usage)
	return 2
}

// flowByID routes the three commands that share one argument shape.
func flowByID(ctx context.Context, b board.Board, command string, a board.Args) ([]string, error) {
	switch command {
	case "review":
		return b.Review(ctx, a)
	case "reject":
		return b.Reject(ctx, a)
	default:
		return b.Unblock(ctx, a)
	}
}

// at answers the positional at i, "" when it is not there.
func at(positional []string, i int) string {
	if i < len(positional) {
		return positional[i]
	}
	return ""
}

// commandWord is what an unknown-command error names; an empty one shows as
// "(none)".
func commandWord(command string) string {
	if command == "" {
		return "(none)"
	}
	return command
}

// noBoardLine is what a resolution that found no board prints, with exit 4.
const noBoardLine = "kanban: no board here; run kanban init (or /huddle:kanban-init)"

// usageError prints one usage error: the problem, then the way to the command's
// help.
func usageError(stderr io.Writer, cmd *command, problem string) {
	fmt.Fprintf(stderr, "kanban %s: %s\nrun kanban %s --help\n", cmd.word, problem, cmd.word)
}

// answer prints one command's answer: its lines on stdout, or the failure on
// stderr. refusalOnStdout keeps a refusal that belongs to the report (check)
// among the lines and exits 1.
func answer(stdout, stderr io.Writer, lines []string, err error, refusalOnStdout bool) int {
	if err != nil {
		var refused *board.Refused
		if errors.As(err, &refused) {
			if refusalOnStdout {
				printLines(stdout, lines)
				return 1
			}
			fmt.Fprintln(stderr, refused.Message)
			return 1
		}
		fmt.Fprintf(stderr, "kanban: %v\n", err)
		return 1
	}
	printLines(stdout, lines)
	return 0
}

// printLines writes one line per answer.
func printLines(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

// extractBoard lifts every --board pair out of the argv (a pair before the
// command word included); the last value wins, and the argv without the pairs
// answers back.
func extractBoard(argv []string) (string, []string) {
	value := ""
	rest := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--board" && i+1 < len(argv):
			value = argv[i+1]
			i++
		case strings.HasPrefix(argv[i], "--board="):
			value = strings.TrimPrefix(argv[i], "--board=")
		default:
			rest = append(rest, argv[i])
		}
	}
	return value, rest
}

// errShowHelp marks -h, --help or the flag package's own help request.
var errShowHelp = errors.New("show help")

// parseCommand reads one command's arguments. Flags and positionals may
// interleave, so the pass below moves the flags out before the flag package
// parses them; a flag that takes a value eats the next token, whatever it
// starts with.
func parseCommand(cmd *command, argv []string) (*flagValues, []string, error) {
	fs, v := cmd.flagSet()
	var flags, positional []string
	endOfFlags := false
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if endOfFlags || tok == "-" || !strings.HasPrefix(tok, "-") {
			positional = append(positional, tok)
			continue
		}
		if tok == "--" {
			endOfFlags = true
			continue
		}
		flags = append(flags, tok)
		if strings.Contains(tok, "=") {
			continue
		}
		if f := fs.Lookup(flagName(tok)); f != nil && !isBoolValue(f.Value) && i+1 < len(argv) {
			i++
			flags = append(flags, argv[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil, errShowHelp
		}
		return nil, nil, errors.New(flagProblem(err))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var missing []string
	for _, def := range cmd.defs {
		if def.required && !set[def.name] {
			missing = append(missing, "--"+def.name)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("%s %s required", strings.Join(missing, " and "), verb(len(missing)))
	}
	if len(positional) < cmd.min {
		names := make([]string, 0, cmd.min)
		for _, p := range cmd.positionals[:cmd.min] {
			names = append(names, p.name)
		}
		return nil, nil, fmt.Errorf("%s %s required", strings.Join(names, " and "), verb(len(names)))
	}
	if cmd.max >= 0 && len(positional) > cmd.max {
		return nil, nil, fmt.Errorf("unexpected argument %s", positional[cmd.max])
	}
	return v, positional, nil
}

// verb agrees with the count: "is" for one, "are" for more.
func verb(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// flagName is the token's flag name, with at most the two leading dashes the
// flag package counts gone.
func flagName(tok string) string {
	if strings.HasPrefix(tok, "--") {
		return tok[2:]
	}
	return tok[1:]
}

// isBoolValue says whether one flag value reads as a bool: those take no
// separate value token.
func isBoolValue(value flag.Value) bool {
	b, ok := value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// flagProblem turns one flag package error into the problem line a usage error
// prints, with the flag named the way the user wrote it.
func flagProblem(err error) string {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
		return "--" + name + " is not a flag of this command"
	}
	if name, ok := strings.CutPrefix(msg, "flag needs an argument: -"); ok {
		return "--" + name + " needs a value"
	}
	return msg
}

// flagValues holds one parsed command's flag values, by flag name.
type flagValues struct {
	strs  map[string]*string
	bools map[string]*bool
	lists map[string]*[]string
}

func (v *flagValues) str(name string) string {
	if p := v.strs[name]; p != nil {
		return *p
	}
	return ""
}

func (v *flagValues) boolean(name string) bool {
	if p := v.bools[name]; p != nil {
		return *p
	}
	return false
}

func (v *flagValues) list(name string) []string {
	if p := v.lists[name]; p != nil {
		return *p
	}
	return nil
}

// flagSet builds one command's flag set and the holders its runner reads.
func (cmd *command) flagSet() (*flag.FlagSet, *flagValues) {
	fs := flag.NewFlagSet(cmd.word, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	v := &flagValues{strs: map[string]*string{}, bools: map[string]*bool{}, lists: map[string]*[]string{}}
	for _, def := range cmd.defs {
		switch def.kind {
		case kindBool:
			v.bools[def.name] = fs.Bool(def.name, false, def.help)
		case kindStrings:
			repeated := &stringSlice{}
			fs.Var(repeated, def.name, def.help)
			v.lists[def.name] = &repeated.values
		default:
			v.strs[def.name] = fs.String(def.name, "", def.help)
		}
	}
	return fs, v
}

// stringSlice is a repeatable string flag: --on a --on b collects [a b].
type stringSlice struct{ values []string }

func (s *stringSlice) String() string { return strings.Join(s.values, ",") }

func (s *stringSlice) Set(value string) error {
	s.values = append(s.values, value)
	return nil
}

// boardAt answers the board to run on: resolved from the explicit path, the
// environment or the working directory, with the world's clock and its files
// bound — a root holding kanban.db reads the database. The caller closes the
// board.
func boardAt(ctx context.Context, w World, explicit string) (board.Board, func() error, error) {
	cwd := w.Cwd()
	b, err := board.Resolve(explicit, w.Getenv, cwd)
	if err != nil {
		return b, nil, err
	}
	b, closeBoard, err := board.Open(ctx, b)
	if err != nil {
		return b, nil, err
	}
	b.Now = w.Now
	return b, closeBoard, nil
}

// initRoot answers where init builds a board: the explicit path, the
// environment, a board found above the working directory, else .agents/kanban
// right there.
func initRoot(ctx context.Context, w World, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v, ok := w.Getenv("KANBAN_BOARD"); ok && v != "" {
		return v
	}
	if b, err := board.Resolve("", w.Getenv, w.Cwd()); err == nil {
		if _, closeBoard, err := board.Open(ctx, b); err == nil {
			_ = closeBoard()
		}
		return b.Root
	}
	return filepath.Join(w.Cwd(), ".agents", "kanban")
}

// isColumn says whether the word names a column.
func isColumn(word string) bool {
	for _, col := range board.Columns {
		if col == word {
			return true
		}
	}
	return false
}

// listLine renders one card as one line: the column, the id, the badges, the
// title, then the dependency states and the parked state.
func listLine(c board.Card) string {
	parts := []string{fmt.Sprintf("%-11s", c.Column), fmt.Sprintf("%-14s", c.ID)}
	for _, badge := range c.Badges {
		parts = append(parts, badge.Label)
	}
	parts = append(parts, c.Title)
	if len(c.Deps) > 0 {
		states := make([]string, len(c.Deps))
		for i, d := range c.Deps {
			states[i] = d.ID + "(" + d.State + ")"
		}
		parts = append(parts, "deps="+strings.Join(states, ","))
	}
	if c.Blocked != nil {
		blocked := "blocked: " + c.Blocked.Reason
		if len(c.Blocked.On) > 0 {
			blocked += " | on: " + strings.Join(c.Blocked.On, ", ")
		}
		if c.Blocked.Until != "" {
			blocked += " | until: " + c.Blocked.Until
		}
		if c.Blocked.From != "" {
			blocked += " | from " + c.Blocked.From
		}
		parts = append(parts, blocked)
	}
	return strings.Join(parts, " ")
}

// statusLines renders the status answer: the epoch and stop state, the expedite
// lane, then one count per column.
func statusLines(st board.Status) []string {
	first := fmt.Sprintf("epoch=%d running", st.Epoch)
	if st.Stopped != nil {
		first = fmt.Sprintf("epoch=%d STOPPED by %s: %s", st.Epoch, st.Stopped.Actor, st.Stopped.Note)
	}
	lines := []string{first}
	if len(st.Expedite) > 0 {
		hot := make([]string, len(st.Expedite))
		for i, e := range st.Expedite {
			hot[i] = fmt.Sprintf("%s (%s)", e.ID, e.Column)
			if e.WaitsOn != "" {
				hot[i] = fmt.Sprintf("%s (%s, waits on %s)", e.ID, e.Column, e.WaitsOn)
			}
		}
		lines = append(lines, "EXPEDITE (fast track): "+strings.Join(hot, ", "))
	}
	for _, c := range st.Counts {
		lines = append(lines, fmt.Sprintf("%s: %d", c.Column, c.Count))
	}
	return lines
}

// lintLines renders the lint report: one line per flagged ticket, then the
// summary. It also answers the failure count, which flips the exit code;
// warnings show and never fail.
func lintLines(report board.LintReport) ([]string, int) {
	var lines []string
	failures, warnings := 0, 0
	for _, lt := range report.Tickets {
		parts := make([]string, len(lt.Problems))
		for i, p := range lt.Problems {
			if p.Severity == "failure" {
				failures++
				parts[i] = p.Message
			} else {
				warnings++
				parts[i] = "warning: " + p.Message
			}
		}
		lines = append(lines, fmt.Sprintf("%s: %s", lt.ID, strings.Join(parts, "; ")))
	}
	if len(report.Tickets) == 0 {
		return []string{"lint: every ticket clears the bar (front matter, sections, dependencies, blocked fields, UTF-8)"}, 0
	}
	summary := fmt.Sprintf("lint: %d ticket(s) flagged, %d failure(s), %d warning(s)",
		len(report.Tickets), failures, warnings)
	return append(lines, summary), failures
}

// writeJSON prints one document: two-space indent, a trailing newline, and no
// HTML escaping (ticket text keeps its <, > and & raw).
func writeJSON(w io.Writer, doc any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}

// showJSON is the show command's document.
type showJSON struct {
	Card   board.Card        `json:"card"`
	Fields map[string]string `json:"fields"`
	Body   string            `json:"body"`
}

// statusJSON is the status command's document.
type statusJSON struct {
	Epoch    int            `json:"epoch"`
	StopLine *stopLineJSON  `json:"stopLine"`
	Columns  []columnJSON   `json:"columns"`
	Expedite []expediteJSON `json:"expedite"`
}

// stopLineJSON is the stop-line record inside the status document.
type stopLineJSON struct {
	Epoch int    `json:"epoch"`
	Actor string `json:"actor"`
	Note  string `json:"note"`
	TS    string `json:"ts"`
}

// columnJSON is one column's id, display title and count.
type columnJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Count int    `json:"count"`
}

// expediteJSON is one open expedite ticket in the fast track.
type expediteJSON struct {
	ID      string `json:"id"`
	Column  string `json:"column"`
	WaitsOn string `json:"waitsOn"`
}

// columnTitles is each column's display title.
var columnTitles = map[string]string{
	"backlog": "Backlog", "ready": "Ready", "in-progress": "In Progress",
	"review": "Review", "testing": "Testing", "blocked": "Blocked", "done": "Done",
}

// statusJSONOf builds the status document from the board's answer.
func statusJSONOf(st board.Status) statusJSON {
	doc := statusJSON{Epoch: st.Epoch, Columns: []columnJSON{}, Expedite: []expediteJSON{}}
	if st.Stopped != nil {
		doc.StopLine = &stopLineJSON{
			Epoch: st.Stopped.Epoch, Actor: st.Stopped.Actor,
			Note: st.Stopped.Note, TS: st.Stopped.TS,
		}
	}
	for _, c := range st.Counts {
		doc.Columns = append(doc.Columns, columnJSON{ID: c.Column, Title: columnTitles[c.Column], Count: c.Count})
	}
	for _, e := range st.Expedite {
		doc.Expedite = append(doc.Expedite, expediteJSON{ID: e.ID, Column: e.Column, WaitsOn: e.WaitsOn})
	}
	return doc
}

// lintJSON is the lint command's document.
type lintJSON struct {
	Failures int              `json:"failures"`
	Warnings int              `json:"warnings"`
	Tickets  []lintTicketJSON `json:"tickets"`
}

// lintTicketJSON is one flagged ticket with its problems.
type lintTicketJSON struct {
	ID       string        `json:"id"`
	Column   string        `json:"column"`
	Problems []problemJSON `json:"problems"`
}

// problemJSON is one bar finding.
type problemJSON struct {
	Severity string `json:"severity"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}

// lintJSONOf builds the lint document from the board's report.
func lintJSONOf(report board.LintReport) lintJSON {
	doc := lintJSON{Tickets: []lintTicketJSON{}}
	for _, lt := range report.Tickets {
		entry := lintTicketJSON{ID: lt.ID, Column: lt.Column, Problems: []problemJSON{}}
		for _, p := range lt.Problems {
			if p.Severity == "failure" {
				doc.Failures++
			} else {
				doc.Warnings++
			}
			entry.Problems = append(entry.Problems, problemJSON{Severity: p.Severity, Rule: p.Rule, Message: p.Message})
		}
		doc.Tickets = append(doc.Tickets, entry)
	}
	return doc
}

// sortedDefs answers the command's flags in name order, the order help prints.
func (cmd *command) sortedDefs() []flagDef {
	defs := make([]flagDef, len(cmd.defs))
	copy(defs, cmd.defs)
	sort.Slice(defs, func(i, j int) bool { return defs[i].name < defs[j].name })
	return defs
}
