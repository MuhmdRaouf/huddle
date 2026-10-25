// Package board is the kanban board on disk: where it lives, how tickets move
// between column folders and which moves the gates refuse. It prints nothing;
// the cli package owns the wording of every exit code.
package board

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
	"github.com/MuhmdRaouf/huddle/internal/kanban/ticket"
)

// Columns is the board's column order, everywhere.
var Columns = []string{"backlog", "ready", "in-progress", "review", "testing", "blocked", "done"}

// openColumns lists the columns an expedite lane entry can come from, furthest
// along first.
var openColumns = []string{"testing", "review", "in-progress", "ready", "blocked", "backlog"}

// ErrNoBoard answers a resolution that found no board: the cli prints its own
// line and exits 4.
var ErrNoBoard = errors.New("no board")

var idRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)*$`)

var actorRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// yamlWords read back as bool or null when bare, so an actor may be none of
// them: claimed_by and friends must read back as strings.
var yamlWords = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true,
	"on": true, "off": true, "null": true, "~": true,
}

// Board is one board at Root. Now stamps every field and log line; a test pins
// it. Store, when set, holds the board's kanban.db and answers every file
// touch; without it the board reads and writes the folder.
type Board struct {
	Root  string
	Now   func() time.Time
	Store *store.Store
}

// Refused is an expected refusal: the cli prints Message and exits 1. Check
// prints its lines and exits 1 while the line is stopped or the ticket is
// missing; its refusal message is among those lines.
type Refused struct{ Message string }

func (r *Refused) Error() string { return r.Message }

// Args carries one flow command's flags.
type Args struct {
	Actor, ID, Note, Branch, Worktree, Until, Column string
	On                                               []string
	Force                                            bool
	Checks, Merge                                    string
}

// NewTicket carries the new command's flags; Type, Priority and Est default
// when empty.
type NewTicket struct {
	Actor, ID, Title, Cluster, Summary, Type, Priority, Est, Repos string
	Tags, DependsOn                                                []string
	Expedite, Ready, Force                                         bool
	BodyFile                                                       string
}

func refused(format string, args ...any) *Refused {
	return &Refused{Message: fmt.Sprintf(format, args...)}
}

func (b Board) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// fs answers where the board's files live: the database when the board was
// opened on a root holding kanban.db, the folder otherwise.
func (b Board) fs() files {
	if b.Store != nil {
		return dbFiles{root: b.Root, st: b.Store, now: b.now}
	}
	return dirFiles{}
}

// Open binds the board's files: a root holding kanban.db opens the database and
// answers the board with Store set and the store's Close; any other root
// answers the board unchanged and a close that does nothing.
func Open(ctx context.Context, b Board) (Board, func() error, error) {
	file := filepath.Join(b.Root, "kanban.db")
	if _, err := os.Stat(file); err != nil {
		return b, func() error { return nil }, nil
	}
	st, err := store.Open(ctx, file)
	if err != nil {
		return Board{}, nil, err
	}
	b.Store = st
	return b, st.Close, nil
}

// atomic runs one writing command as one transaction: two sessions on one
// kanban.db board serialise, so the second sees the first one's claim instead
// of deciding on a board the first is about to change, and any error from fn —
// a refusal included — writes nothing. A folder board has no store, so fn
// runs on it as is.
func (b Board) atomic(ctx context.Context, fn func(b Board) ([]string, error)) ([]string, error) {
	if b.Store == nil {
		return fn(b)
	}
	var lines []string
	err := b.Store.Tx(ctx, func(tx *store.Store) error {
		inner := b
		inner.Store = tx
		var err error
		lines, err = fn(inner)
		return err
	})
	return lines, err
}

// Resolve answers the board the command runs on: the explicit path, else
// $KANBAN_BOARD, else the first directory from cwd upwards holding
// .agents/kanban, stopping after a directory that holds .git or at the
// filesystem root. A worktree's .git file sends the walk on to its
// repository's main checkout, so every worktree without a board of its own
// works the repository's board.
func Resolve(explicit string, env func(string) (string, bool), cwd string) (Board, error) {
	pick := func(dir string) (Board, bool) {
		root := filepath.Join(dir, ".agents", "kanban")
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return Board{Root: root}, true
		}
		return Board{}, false
	}
	if explicit != "" {
		return Board{Root: explicit}, nil
	}
	if v, ok := env("KANBAN_BOARD"); ok && v != "" {
		return Board{Root: v}, nil
	}
	dir := cwd
	for {
		if b, ok := pick(dir); ok {
			return b, nil
		}
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			main, ok := "", false
			if !info.IsDir() {
				main, ok = commonRepo(filepath.Join(dir, ".git"))
			}
			if !ok {
				return Board{}, ErrNoBoard // the board never sits above the repo root
			}
			dir = main
			continue
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Board{}, ErrNoBoard
		}
		dir = parent
	}
}

// gitdirOf reads the "gitdir: <path>" line of a .git file.
var gitdirOf = regexp.MustCompile(`(?m)^gitdir:\s*(.+)$`)

// commonRepo answers the main checkout a worktree's .git file stands for: the
// file names the worktree's gitdir (<repo>/.git/worktrees/<name>), whose
// commondir names <repo>/.git. ok is false for any other .git file — a
// submodule's, a bare repository's, one whose gitdir is gone.
func commonRepo(gitFile string) (string, bool) {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	match := gitdirOf.FindStringSubmatch(string(raw))
	if match == nil {
		return "", false
	}
	gitdir := resolveIn(filepath.Dir(gitFile), strings.TrimSpace(match[1]))
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return "", false
	}
	shared := resolveIn(gitdir, strings.TrimSpace(string(common)))
	if filepath.Base(shared) != ".git" {
		return "", false
	}
	if info, err := os.Stat(shared); err != nil || !info.IsDir() {
		return "", false
	}
	return filepath.Dir(shared), true
}

// resolveIn answers p against dir when p is relative, cleaned either way.
func resolveIn(dir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(dir, p))
}

// Find answers the ticket's path and column wherever it sits.
func (b Board) Find(id string) (path, column string, ok bool) {
	f := b.fs()
	for _, col := range Columns {
		p := b.ticketPath(col, id)
		if _, ok := f.stat(p); ok {
			return p, col, true
		}
	}
	return "", "", false
}

func (b Board) ticketPath(col, id string) string {
	return filepath.Join(b.Root, col, id+".md")
}

// names answers a column's ticket file names, sorted.
func (b Board) names(col string) []string {
	return b.fs().readDir(filepath.Join(b.Root, col))
}

// Entry is one file a column holds: its name, its path and what a stat
// answers for it.
type Entry struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

// ColumnEntries answers the ticket files one column holds, sorted by name,
// whatever kind of board the root carries: real files, or rows.
func (b Board) ColumnEntries(col string) []Entry {
	f := b.fs()
	names := f.readDir(filepath.Join(b.Root, col))
	if len(names) == 0 {
		return nil
	}
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		path := filepath.Join(b.Root, col, name)
		info, ok := f.stat(path)
		if !ok {
			continue // the file left between the walk and the stat; the next pass sees it
		}
		entries = append(entries, Entry{Name: name, Path: path, Size: info.Size, ModTime: info.ModTime})
	}
	return entries
}

// ReadPath answers one path's bytes through the board's files: a real file, a
// row or a meta row.
func (b Board) ReadPath(path string) ([]byte, error) {
	return b.fs().readFile(path)
}

// StatPath answers what a stat answers for one path through the board's
// files; ok is false when nothing answers there.
func (b Board) StatPath(path string) (FileInfo, bool) {
	return b.fs().stat(path)
}

// loadCard parses one ticket file leniently: bytes that are not UTF-8 read
// back with replacement characters so list, lint and check still answer, with
// the problem attached.
func (b Board) loadCard(col, id string) (ticket.Ticket, []ticket.Problem) {
	data, err := b.fs().readFile(b.ticketPath(col, id))
	if err != nil {
		return ticket.Ticket{Fields: map[string]string{}}, nil
	}
	return parseFile(id+".md", data)
}

func parseFile(fileName string, data []byte) (ticket.Ticket, []ticket.Problem) {
	if utf8.Valid(data) {
		return ticket.Parse(fileName, data)
	}
	t, _ := ticket.Parse(fileName, []byte(strings.ToValidUTF8(string(data), "�")))
	return t, []ticket.Problem{{
		Severity: "failure", Rule: "utf-8",
		Message: "not UTF-8 (re-save the card as UTF-8; the flow commands refuse it)",
	}}
}

// fieldOf answers one front-matter value of a ticket file, leniently.
func (b Board) fieldOf(path, key string) string {
	data, err := b.fs().readFile(path)
	if err != nil {
		return ""
	}
	front, _, _ := ticket.Split(string(data))
	v, _ := ticket.Field(front, key)
	return v
}

// readCardText answers the ticket's bytes, refusing a file that is not UTF-8
// before any move: a card never changes column half-stamped.
func (b Board) readCardText(path, id string) (string, error) {
	data, err := b.fs().readFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", refused("%s: not UTF-8; re-save the card as UTF-8, then retry", id)
	}
	return string(data), nil
}

// readFMText also refuses a card with no front-matter block: the commands that
// clear fields have none to clear on such a card.
func (b Board) readFMText(path, id string) (string, error) {
	text, err := b.readCardText(path, id)
	if err != nil {
		return "", err
	}
	if _, _, ok := ticket.Split(text); !ok {
		return "", refused("%s: no front matter (the card must start with a --- block); "+
			"re-create it with new, or add the block by hand, then retry", id)
	}
	return text, nil
}

// writeCardText lands the whole text at once: a reader sees the old card or
// the new one, never a truncated one.
func (b Board) writeCardText(ctx context.Context, path, text string) error {
	return b.fs().writeFile(ctx, path, []byte(text))
}

// moveTicket stamps the ticket in place, then renames it: a failed rename
// leaves the ticket where it was.
func (b Board) moveTicket(ctx context.Context, path, dst string, stamp func(string) string) error {
	f := b.fs()
	data, err := f.readFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	if stamp != nil {
		text = stamp(text)
	}
	if text != string(data) {
		if err := f.writeFile(ctx, path, []byte(text)); err != nil {
			return err
		}
	}
	return f.rename(ctx, path, dst)
}

// clearHand drops the hand-move mark: every flow command clears it on the
// ticket it touches.
func clearHand(text string) string {
	return ticket.ClearFields(text, "moved_by_hand")
}

// appendLogEntry adds "- <RFC 3339> <actor>: <text>" at the end of the body's
// ## Log section, creating the section at the end when the body has none.
func appendLogEntry(src, entry string) string {
	front, body, ok := ticket.Split(src)
	if !ok {
		return src
	}
	lines := strings.Split(body, "\n")
	at, end := -1, len(lines)
	for i, line := range lines {
		if at < 0 && line == "## Log" {
			at = i
			continue
		}
		if at >= 0 && strings.HasPrefix(line, "## ") {
			end = i
			break
		}
	}
	var out string
	if at < 0 {
		text := strings.TrimRight(body, "\n")
		if text == "" {
			text = "## Log"
		} else {
			text += "\n\n## Log"
		}
		out = text + "\n" + entry + "\n"
	} else {
		head := lines[:end]
		for len(head) > at+1 && strings.TrimSpace(head[len(head)-1]) == "" {
			head = head[:len(head)-1]
		}
		out = strings.Join(append(head, entry), "\n") + "\n"
		if rest := strings.Join(lines[end:], "\n"); rest != "" {
			out += "\n" + rest
		}
	}
	return "---\n" + front + "\n---\n" + out
}

func normType(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "fix", "feature", "test", "docs", "ops", "decision", "chore":
		return v
	}
	return "fix" // an unknown or missing type counts as implementation
}

func isImplementation(rawType string) bool {
	switch normType(rawType) {
	case "fix", "feature", "test":
		return true
	}
	return false
}

// authorIs answers whether the actor is the ticket's current holder, its
// original author or any past holder, case-insensitively.
func authorIs(t ticket.Ticket, actor string) bool {
	me := strings.ToLower(strings.TrimSpace(actor))
	if me == "" {
		return false
	}
	for _, key := range []string{"claimed_by", "authored_by"} {
		if strings.ToLower(strings.TrimSpace(t.Fields[key])) == me {
			return true
		}
	}
	for _, h := range ticket.ParseList(t.Fields["held_by"]) {
		if strings.ToLower(strings.TrimSpace(h)) == me {
			return true
		}
	}
	return false
}

// whoHolds names the people a gate compared against, for refusal messages.
func whoHolds(t ticket.Ticket) string {
	or := func(key string) string {
		if v := t.Fields[key]; v != "" {
			return v
		}
		return "-"
	}
	return fmt.Sprintf("authored_by=%s, claimed_by=%s, held_by=%s",
		or("authored_by"), or("claimed_by"), or("held_by"))
}

// whereIs answers " (it is in <col>/)" for a ticket on the board, else "".
func (b Board) whereIs(id string) string {
	if _, col, ok := b.Find(id); ok {
		return fmt.Sprintf(" (it is in %s/)", col)
	}
	return ""
}

// missingHint is the final clause for a ticket that is nowhere on the board.
func (b Board) missingHint(id string) string {
	if _, _, ok := b.Find(id); ok {
		return ""
	}
	return "; not on the board: run list to see ids, or new to create it"
}

// actorRefusal answers the actor validation every command runs.
func actorRefusal(actor string) error {
	if actorRe.MatchString(actor) && !yamlWords[strings.ToLower(actor)] {
		return nil
	}
	return refused("--actor '%s': a short id made of letters, digits, '.', '_' or '-' (fix1, lead, proxbeam.lead)", actor)
}

// scanArgs refuses a free-text argument the front matter cannot store: one
// that mixes quote kinds, or a backslash next to a single quote. hintBodyFile
// adds the new command's alternative.
func scanArgs(hintBodyFile bool, values ...[2]string) *Refused {
	hint := ""
	if hintBodyFile {
		hint = " (or use --body-file)"
	}
	for _, v := range values {
		if strings.Contains(v[1], `"`) && strings.Contains(v[1], `'`) {
			return refused("%s: a value may hold one quote kind, not both ' and \"; rewrite the value with one kind%s", v[0], hint)
		}
		if strings.Contains(v[1], `\`) && strings.Contains(v[1], `'`) {
			return refused("%s: a value may not hold both a backslash and a single quote; drop one of them", v[0])
		}
	}
	return nil
}

// checkDep answers (problem, warning) for one dependency: an id must be on the
// board; text that is an id in the wrong case is refused with the right one;
// other text is accepted with a warning.
func (b Board) checkDep(dep, flag string) (string, string) {
	dep = strings.TrimSpace(dep)
	if idRe.MatchString(dep) {
		if _, _, ok := b.Find(dep); ok {
			return "", ""
		}
		return fmt.Sprintf("%s %s: no such card on the board (create it first, or spell it as plain text)", flag, dep), ""
	}
	if upper := strings.ToUpper(dep); idRe.MatchString(upper) {
		if _, _, ok := b.Find(upper); ok {
			return fmt.Sprintf("%s '%s': did you mean %s? (ids are upper-case)", flag, dep, upper), ""
		}
	}
	return "", fmt.Sprintf("warning: %s '%s' is plain text, not a card id; the board cannot track its state", flag, dep)
}

// openDep answers the first depends_on id that is on the board but not done,
// else "".
func (b Board) openDep(t ticket.Ticket) string {
	for _, dep := range t.DependsOn {
		if !idRe.MatchString(dep) {
			continue
		}
		if _, col, ok := b.Find(dep); ok && col != "done" {
			return dep
		}
	}
	return ""
}

// idCounts answers how many ticket files hold each id on the board, minus the
// one at exclude.
func (b Board) idCounts(exclude string) map[string]int {
	counts := map[string]int{}
	for _, col := range Columns {
		for _, name := range b.names(col) {
			p := filepath.Join(b.Root, col, name)
			if exclude != "" && p == exclude {
				continue
			}
			counts[strings.TrimSuffix(name, ".md")]++
		}
	}
	return counts
}

// knownExcept answers ticket.Lint's Known: whether an id names a file other
// than the one being checked, so a true answer for the ticket's own id means a
// duplicate.
func (b Board) knownExcept(path string) func(string) bool {
	counts := b.idCounts(path)
	return func(id string) bool { return counts[id] > 0 }
}

func validColumn(col string) bool {
	for _, c := range Columns {
		if c == col {
			return true
		}
	}
	return false
}

// collapse squeezes runs of whitespace to single spaces and trims the ends,
// the way every stamped value is written.
func collapse(v string) string {
	return strings.Join(strings.FieldsFunc(v, unicode.IsSpace), " ")
}
