package board

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/kanban/ticket"
)

// evidenceRe accepts the note forms that carry proof: an exit code, a return
// code, a status, or a pass count.
var evidenceRe = regexp.MustCompile(`(?i)\b(?:exit(?:ed| code)?|rc|status)\s*[=:]?\s*\d+|\b\d+ (?:passed|ok|green)\b`)

// doneSweep is the column order done takes tickets from.
var doneSweep = []string{"testing", "review", "in-progress", "blocked", "ready", "backlog"}

// blockSweep is the column order block takes tickets from.
var blockSweep = []string{"ready", "in-progress", "review", "testing", "backlog"}

// barFailures answers the ticket-bar failures of the file at path, as the
// promote and ready-writing forms of new report them.
func (b Board) barFailures(path, column string) []string {
	data, err := b.fs().readFile(path)
	if err != nil {
		return []string{"cannot read the card"}
	}
	return b.barText(filepath.Base(path), string(data), column, path)
}

// barText answers the ticket-bar failures of a ticket's whole text. exclude is
// the file's own path when it is on disk, so its id counts as taken only when
// a second file holds it.
func (b Board) barText(fileName, text, column, exclude string) []string {
	t, problems := parseFile(fileName, []byte(text))
	problems = append(problems, ticket.Lint(t, ticket.Context{Column: column, Known: b.knownExcept(exclude)})...)
	var messages []string
	for _, p := range problems {
		if p.Severity == "failure" {
			messages = append(messages, p.Message)
		}
	}
	return messages
}

// barRefusal is the refusal a ticket under the bar answers; --force overrides
// it and the LOG line says so.
func barRefusal(id string, failures []string) *Refused {
	return refused("%s: under the ticket bar: %s; fill the card, or pass --force (logged)",
		id, strings.Join(failures, "; "))
}

// Promote moves a backlog ticket to ready, under the ticket bar.
func (b Board) Promote(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.promote(ctx, a) })
}

func (b Board) promote(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}); r != nil {
		return nil, r
	}
	f := b.fs()
	src := b.ticketPath("backlog", a.ID)
	if _, ok := f.stat(src); !ok {
		return nil, refused("%s: no card in backlog/%s; only backlog cards are promoted%s",
			a.ID, b.whereIs(a.ID), b.missingHint(a.ID))
	}
	if _, err := b.readCardText(src, a.ID); err != nil {
		return nil, err
	}
	if !a.Force {
		if failures := b.barFailures(src, "ready"); len(failures) > 0 {
			return nil, barRefusal(a.ID, failures)
		}
	}
	dst := b.ticketPath("ready", a.ID)
	if err := b.moveTicket(ctx, src, dst, clearHand); err != nil {
		return nil, err
	}
	pairs := []logPair{logStr("ticket", a.ID), logStr("actor", a.Actor)}
	if a.Force {
		pairs = append(pairs, logBool("forced", true))
	}
	if err := b.appendLog("promote", pairs...); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// pullKey ranks ready tickets: expedite first, then priority, then name.
type pullKey struct {
	expedite int
	priority int
	name     string
}

func keyOf(t ticket.Ticket) pullKey {
	prio := 9
	switch t.Priority {
	case "P0", "P1", "P2", "P3":
		prio = int(t.Priority[1] - '0')
	}
	e := 1
	if t.Expedite {
		e = 0
	}
	return pullKey{e, prio, t.ID}
}

func lessKey(a, b pullKey) bool {
	if a.expedite != b.expedite {
		return a.expedite < b.expedite
	}
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	return a.name < b.name
}

// Pull claims the best ready ticket, or the one named by --id. An expedite
// ticket with no open dependency holds the fast track; a waiting dependency
// passes it over with a note.
func (b Board) Pull(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.pull(ctx, a) })
}

func (b Board) pull(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false,
		[2]string{"<ID>", a.ID}, [2]string{"--branch", a.Branch}, [2]string{"--worktree", a.Worktree}); r != nil {
		return nil, r
	}
	if rec := b.stopped(); rec != nil {
		return nil, refused("board stopped (epoch %d) by %s: %s; wait for resume-line",
			rec.Epoch, rec.Actor, rec.Note)
	}
	actor := strings.TrimSpace(a.Actor)
	me := strings.ToLower(actor)
	var held []string
	for _, name := range b.names("in-progress") {
		t, _ := b.loadCard("in-progress", strings.TrimSuffix(name, ".md"))
		if strings.ToLower(strings.TrimSpace(t.Fields["claimed_by"])) == me {
			held = append(held, strings.TrimSuffix(name, ".md"))
		}
	}
	if len(held) > 0 {
		return nil, refused("%s already holds %s in in-progress/ (one card per actor); finish it with review, or park it with block",
			actor, strings.Join(held, ", "))
	}
	var lines []string
	best := ""
	bestSet := false
	var bestKey pullKey
	forced := false
	if a.ID != "" {
		path := b.ticketPath("ready", a.ID)
		if _, ok := b.fs().stat(path); !ok {
			_, col, ok := b.Find(a.ID)
			switch {
			case ok && col == "blocked":
				return nil, refused("%s: not in ready/ (it is in blocked/); unblock it first", a.ID)
			case ok && (col == "in-progress" || col == "review" || col == "testing"):
				claimed := b.fieldOf(b.ticketPath(col, a.ID), "claimed_by")
				if claimed == "" {
					claimed = "?"
				}
				return nil, refused("%s: not in ready/ (it is in %s/); already claimed by %s", a.ID, col, claimed)
			case ok && col == "backlog":
				return nil, refused("%s: not in ready/ (it is in backlog/); promote it first, or pull without --id", a.ID)
			case ok:
				return nil, refused("%s: not in ready/ (it is in %s/); nothing to pull", a.ID, col)
			default:
				return nil, refused("%s: not on the board; run list to see ids, or new to create it", a.ID)
			}
		}
		// the fast track: an unclaimed expedite ticket goes before any normal --id pull;
		// one whose dependency is still open cannot be worked, so it holds nothing yet
		var waiting []string
		for _, name := range b.names("ready") {
			cid := strings.TrimSuffix(name, ".md")
			if cid == a.ID {
				continue
			}
			t, _ := b.loadCard("ready", cid)
			if !t.Expedite {
				continue
			}
			if dep := b.openDep(t); dep != "" {
				_, depCol, _ := b.Find(dep)
				lines = append(lines, fmt.Sprintf("note: expedite %s waits on %s (%s/) and does not hold the fast track yet", cid, dep, depCol))
			} else {
				waiting = append(waiting, cid)
			}
		}
		target, _ := b.loadCard("ready", a.ID)
		if len(waiting) > 0 && !target.Expedite {
			if !a.Force {
				return nil, refused("expedite %s is waiting: pull it first (or --force to skip the fast track; logged)",
					strings.Join(waiting, ", "))
			}
			forced = true
		}
		best, bestSet = path, true
	}
	type wait struct{ id, dep string }
	var waitingOn []wait
	if !bestSet {
		for _, name := range b.names("ready") {
			path := filepath.Join(b.Root, "ready", name)
			t, _ := b.loadCard("ready", strings.TrimSuffix(name, ".md"))
			if dep := b.openDep(t); dep != "" {
				waitingOn = append(waitingOn, wait{strings.TrimSuffix(name, ".md"), dep})
				continue
			}
			key := keyOf(t)
			if !bestSet || lessKey(key, bestKey) {
				best, bestKey, bestSet = path, key, true
			}
		}
	}
	for _, w := range waitingOn {
		_, depCol, _ := b.Find(w.dep)
		lines = append(lines, fmt.Sprintf("skipped %s: it waits on %s (%s/); pull --id %s to take it anyway",
			w.id, w.dep, depCol, w.id))
	}
	if !bestSet {
		if len(waitingOn) > 0 {
			parts := make([]string, len(waitingOn))
			for i, w := range waitingOn {
				parts[i] = w.id + " waits on " + w.dep
			}
			return nil, refused("no ready card without an open dependency: %s", strings.Join(parts, "; "))
		}
		return nil, refused("no ready tickets; promote one from backlog/ or unblock one from blocked/")
	}
	id := strings.TrimSuffix(filepath.Base(best), ".md")
	if _, err := b.readCardText(best, id); err != nil {
		return nil, err
	}
	dst := filepath.Join(b.Root, "in-progress", filepath.Base(best))
	stamp := func(text string) string {
		text = clearHand(text)
		text = setFieldPanicFree(text, "claimed_by", actor)
		if v, ok := fieldOfText(text, "authored_by"); !ok || strings.TrimSpace(v) == "" {
			text = setFieldPanicFree(text, "authored_by", actor) // the first puller stays the author across re-pulls
		}
		held := ticket.ParseList(fieldOfTextOr(text, "held_by"))
		if !containsFold(held, actor) {
			text = setFieldPanicFree(text, "held_by", ticket.FormatList(append(held, actor)))
		}
		text = setFieldPanicFree(text, "claimed_at", b.stamp())
		if a.Branch != "" {
			text = setFieldPanicFree(text, "branch", a.Branch)
		}
		if a.Worktree != "" {
			text = setFieldPanicFree(text, "worktree", a.Worktree)
		}
		return text
	}
	if err := b.moveTicket(ctx, best, dst, stamp); err != nil {
		return nil, err
	}
	pairs := []logPair{logStr("ticket", id), logStr("actor", a.Actor)}
	if forced {
		pairs = append(pairs, logBool("forced", true))
	}
	if err := b.appendLog("pull", pairs...); err != nil {
		return nil, err
	}
	return append(lines, dst), nil
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

func fieldOfText(text, key string) (string, bool) {
	front, _, _ := ticket.Split(text)
	return ticket.Field(front, key)
}

func fieldOfTextOr(text, key string) string {
	v, _ := fieldOfText(text, key)
	return v
}

// setFieldPanicFree writes one field; every value reaching it was validated,
// so the unquotable case cannot arise.
func setFieldPanicFree(text, key, value string) string {
	out, err := ticket.SetField(text, key, value)
	if err != nil {
		return text
	}
	return out
}

// joinNote answers the note joined onto the key's value with " | ", so a
// later note never erases an earlier one; a join that would mix quote kinds is
// refused, naming the kind the earlier notes use.
func joinNote(t ticket.Ticket, key, note string) (string, error) {
	if note == "" {
		return "", nil
	}
	prev := t.Fields[key]
	joined := note
	if prev != "" {
		joined = prev + " | " + note
	}
	if strings.Contains(joined, `"`) && strings.Contains(joined, `'`) {
		kind := "single"
		if strings.Contains(prev, `"`) {
			kind = "double"
		}
		return "", refused("%s: the earlier notes use %s quotes; write this note with the same kind", key, kind)
	}
	return joined, nil
}

// Review submits an in-progress ticket to review; the note appends to
// review_note, and a pass-through note by the author or a holder is refused.
func (b Board) Review(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.review(ctx, a) })
}

func (b Board) review(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	src := b.ticketPath("in-progress", a.ID)
	if _, ok := b.fs().stat(src); !ok {
		return nil, refused("%s: not in in-progress/%s; review submits from in-progress only%s",
			a.ID, b.whereIs(a.ID), b.missingHint(a.ID))
	}
	t, _ := b.loadCard("in-progress", a.ID)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Note)), "pass-through") && authorIs(t, a.Actor) {
		return nil, refused("%s: %s is the author or a holder (%s) and does not wave their own card through; a reviewer writes the pass-through note",
			a.ID, a.Actor, whoHolds(t))
	}
	joined, err := joinNote(t, "review_note", a.Note)
	if err != nil {
		return nil, err
	}
	if _, err := b.readCardText(src, a.ID); err != nil {
		return nil, err
	}
	dst := b.ticketPath("review", a.ID)
	stamp := func(cur string) string {
		cur = clearHand(cur)
		if joined != "" {
			cur = setFieldPanicFree(cur, "review_note", joined)
		}
		cur = setFieldPanicFree(cur, "submitted_at", b.stamp())
		if a.Note != "" {
			cur = appendLogEntry(cur, b.logLine(a.Actor, a.Note))
		}
		return cur
	}
	if err := b.moveTicket(ctx, src, dst, stamp); err != nil {
		return nil, err
	}
	if err := b.appendLog("review", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// logLine renders one ## Log entry line.
func (b Board) logLine(actor, note string) string {
	return fmt.Sprintf("- %s %s: %s", b.now().Format(time.RFC3339), actor, note)
}

// Reject returns a review ticket to in-progress with the reviewer's verdict.
func (b Board) Reject(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.reject(ctx, a) })
}

func (b Board) reject(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	src := b.ticketPath("review", a.ID)
	if _, ok := b.fs().stat(src); !ok {
		return nil, refused("%s: not in review/%s; reject and test work on cards in review only%s",
			a.ID, b.whereIs(a.ID), b.missingHint(a.ID))
	}
	t, _ := b.loadCard("review", a.ID)
	if authorIs(t, a.Actor) {
		return nil, refused("%s: %s is the author or holder (%s) and does not reject their own card; a different --actor must",
			a.ID, a.Actor, whoHolds(t))
	}
	joined, err := joinNote(t, "review_note", a.Note)
	if err != nil {
		return nil, err
	}
	if _, err := b.readCardText(src, a.ID); err != nil {
		return nil, err
	}
	dst := b.ticketPath("in-progress", a.ID)
	stamp := func(cur string) string {
		cur = clearHand(cur)
		if joined != "" {
			cur = setFieldPanicFree(cur, "review_note", joined)
		}
		if a.Note != "" {
			cur = appendLogEntry(cur, b.logLine(a.Actor, a.Note))
		}
		return cur
	}
	if err := b.moveTicket(ctx, src, dst, stamp); err != nil {
		return nil, err
	}
	if err := b.appendLog("reject", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// Test moves a review ticket to testing under a verifier's evidence note; the
// author, the original author and every past holder are refused.
func (b Board) Test(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.test(ctx, a) })
}

func (b Board) test(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	src := b.ticketPath("review", a.ID)
	if _, ok := b.fs().stat(src); !ok {
		return nil, refused("%s: not in review/%s; reject and test work on cards in review only%s",
			a.ID, b.whereIs(a.ID), b.missingHint(a.ID))
	}
	t, _ := b.loadCard("review", a.ID)
	if authorIs(t, a.Actor) {
		return nil, refused("%s: %s is the author or holder (%s) and does not verify their own card; a different --actor must test it (also for a pass-through wave)",
			a.ID, a.Actor, whoHolds(t))
	}
	note := strings.TrimSpace(a.Note)
	if note == "" || !evidenceRe.MatchString(note) {
		return nil, refused("%s: the verification note must carry evidence: the commands run and their exit codes (pass --note '<cmd> exit 0; ...'; 'rc=0' and 'N passed' count too)", a.ID)
	}
	joined, err := joinNote(t, "test_note", note)
	if err != nil {
		return nil, err
	}
	if _, err := b.readCardText(src, a.ID); err != nil {
		return nil, err
	}
	dst := b.ticketPath("testing", a.ID)
	stamp := func(cur string) string {
		cur = clearHand(cur)
		cur = setFieldPanicFree(cur, "test_started_at", b.stamp())
		if joined != "" {
			cur = setFieldPanicFree(cur, "test_note", joined)
		}
		cur = appendLogEntry(cur, b.logLine(a.Actor, note))
		return cur
	}
	if err := b.moveTicket(ctx, src, dst, stamp); err != nil {
		return nil, err
	}
	if err := b.appendLog("test", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// Done finishes a ticket: implementation tickets only from testing, and never
// by their author or a holder; every blocked field is cleared.
func (b Board) Done(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.done(ctx, a) })
}

func (b Board) done(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note},
		[2]string{"--checks", a.Checks}, [2]string{"--merge", a.Merge}); r != nil {
		return nil, r
	}
	var src string
	var col string
	f := b.fs()
	for _, c := range doneSweep {
		p := b.ticketPath(c, a.ID)
		if _, ok := f.stat(p); ok {
			src, col = p, c
			break
		}
	}
	if src == "" {
		if _, found, ok := b.Find(a.ID); ok {
			return nil, refused("%s: not open (it is in %s/); nothing to do", a.ID, found)
		}
		return nil, refused("%s: not on the board; run list to see ids, or new to create it", a.ID)
	}
	t, _ := b.loadCard(col, a.ID)
	if isImplementation(t.Type) {
		if col != "testing" {
			raw := strings.ToLower(strings.TrimSpace(t.Type))
			shown := raw
			if raw != normType(raw) {
				shown = raw + " (treated as implementation)"
				if raw == "" {
					shown = "(none) (treated as implementation)"
				}
			}
			return nil, refused("%s: type %s must pass review and testing before done (card is in %s/)", a.ID, shown, col)
		}
		if authorIs(t, a.Actor) {
			return nil, refused("%s: %s is the author or holder (%s) and does not finish their own implementation card",
				a.ID, a.Actor, whoHolds(t))
		}
	}
	if _, err := b.readFMText(src, a.ID); err != nil {
		return nil, err
	}
	dst := b.ticketPath("done", a.ID)
	stamp := func(cur string) string {
		cur = clearHand(cur)
		cur = ticket.ClearFields(cur, append([]string{"blocked_from"},
			"blocked_reason", "blocked_at", "blocked_by", "blocked_on", "unblock_when")...)
		cur = setFieldPanicFree(cur, "finished_at", b.stamp())
		if a.Checks != "" {
			cur = setFieldPanicFree(cur, "checks_passed", a.Checks)
		}
		if a.Merge != "" {
			cur = setFieldPanicFree(cur, "verified_merge", a.Merge)
		}
		if a.Note != "" {
			cur = setFieldPanicFree(cur, "result", a.Note)
			cur = appendLogEntry(cur, b.logLine(a.Actor, a.Note))
		}
		return cur
	}
	if err := b.moveTicket(ctx, src, dst, stamp); err != nil {
		return nil, err
	}
	if err := b.appendLog("done", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// Block parks a ticket with its cause and unblock condition.
func (b Board) Block(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.block(ctx, a) })
}

func (b Board) block(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	values := [][2]string{{"<ID>", a.ID}, {"--note", a.Note}, {"--until", a.Until}}
	for _, on := range a.On {
		values = append(values, [2]string{"--on", on})
	}
	if r := scanArgs(false, values...); r != nil {
		return nil, r
	}
	for _, c := range blockSweep {
		src := b.ticketPath(c, a.ID)
		if _, ok := b.fs().stat(src); !ok {
			continue
		}
		if _, err := b.readCardText(src, a.ID); err != nil {
			return nil, err
		}
		dst := b.ticketPath("blocked", a.ID)
		stamp := func(cur string) string {
			cur = clearHand(cur)
			cur = setFieldPanicFree(cur, "blocked_reason", a.Note)
			cur = setFieldPanicFree(cur, "blocked_from", c)
			cur = setFieldPanicFree(cur, "blocked_at", b.stamp())
			cur = setFieldPanicFree(cur, "blocked_by", a.Actor)
			if len(a.On) > 0 {
				cur = setFieldPanicFree(cur, "blocked_on", ticket.FormatList(a.On))
			}
			if a.Until != "" {
				cur = setFieldPanicFree(cur, "unblock_when", a.Until)
			}
			if a.Note != "" {
				cur = appendLogEntry(cur, b.logLine(a.Actor, a.Note))
			}
			return cur
		}
		if err := b.moveTicket(ctx, src, dst, stamp); err != nil {
			return nil, err
		}
		if err := b.appendLog("block", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
			return nil, err
		}
		return []string{dst}, nil
	}
	_, col, ok := b.Find(a.ID)
	switch {
	case ok && col == "blocked":
		return nil, refused("%s: already blocked; unblock it first", a.ID)
	case ok:
		return nil, refused("%s: block works from backlog, ready, in-progress, review or testing (it is in %s/)", a.ID, col)
	default:
		return nil, refused("%s: not on the board; run list to see ids, or new to create it", a.ID)
	}
}

// Unblock returns a blocked ticket to ready, clearing its holder; blocked_from
// stays on purpose: it marks the phase the ticket must reach again.
func (b Board) Unblock(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.unblock(ctx, a) })
}

func (b Board) unblock(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	src := b.ticketPath("blocked", a.ID)
	if _, ok := b.fs().stat(src); !ok {
		return nil, refused("%s: not in blocked/%s; only blocked cards are unblocked%s",
			a.ID, b.whereIs(a.ID), b.missingHint(a.ID))
	}
	if _, err := b.readFMText(src, a.ID); err != nil {
		return nil, err
	}
	dst := b.ticketPath("ready", a.ID)
	stamp := func(cur string) string {
		cur = clearHand(cur)
		return ticket.ClearFields(cur,
			"blocked_reason", "blocked_at", "blocked_by", "blocked_on", "unblock_when",
			"claimed_by", "claimed_at") // nobody holds a ready card; the author stays
	}
	if err := b.moveTicket(ctx, src, dst, stamp); err != nil {
		return nil, err
	}
	if err := b.appendLog("unblock", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// Move is the lead's override for repairs: any column to any column, always
// logged as forced, except into or out of done for an implementation ticket
// with no test_note.
func (b Board) Move(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.move(ctx, a) })
}

func (b Board) move(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	if !validColumn(a.Column) {
		return nil, refused("%s: %s is not a column; use one of %s", a.ID, a.Column, strings.Join(Columns, ", "))
	}
	src, from, ok := b.Find(a.ID)
	if !ok {
		return nil, refused("%s: not on the board; run list to see ids, or new to create it", a.ID)
	}
	t, _ := b.loadCard(from, a.ID)
	if (a.Column == "done" || from == "done") && isImplementation(t.Type) &&
		strings.TrimSpace(t.Fields["test_note"]) == "" {
		return nil, refused("%s: an implementation ticket without a test_note does not move into or out of done (review and test it first)", a.ID)
	}
	if _, err := b.readCardText(src, a.ID); err != nil {
		return nil, err
	}
	dst := b.ticketPath(a.Column, a.ID)
	if err := b.moveTicket(ctx, src, dst, clearHand); err != nil {
		return nil, err
	}
	pairs := []logPair{
		logStr("ticket", a.ID), logStr("actor", a.Actor),
		logStr("from", from), logStr("to", a.Column), logBool("forced", true),
	}
	if a.Note != "" {
		pairs = append(pairs, logStr("note", a.Note))
	}
	if err := b.appendLog("move", pairs...); err != nil {
		return nil, err
	}
	return []string{dst}, nil
}

// Log appends one entry to the ticket's ## Log section.
func (b Board) Log(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.log(ctx, a) })
}

func (b Board) log(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	src, _, ok := b.Find(a.ID)
	if !ok {
		return nil, refused("%s: not on the board; run list to see ids, or new to create it", a.ID)
	}
	text, err := b.readFMText(src, a.ID)
	if err != nil {
		return nil, err
	}
	if err := b.writeCardText(ctx, src, appendLogEntry(clearHand(text), b.logLine(a.Actor, a.Note))); err != nil {
		return nil, err
	}
	if err := b.appendLog("log", logStr("ticket", a.ID), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{src}, nil
}

// RecordHandMove stamps a ticket the watcher saw move between column folders
// by anything but the cli, and leaves the LOG line.
func (b Board) RecordHandMove(ctx context.Context, id, from, to string) error {
	_, err := b.atomic(ctx, func(b Board) ([]string, error) { return nil, b.recordHandMove(ctx, id, from, to) })
	return err
}

func (b Board) recordHandMove(ctx context.Context, id, from, to string) error {
	path, _, ok := b.Find(id)
	if !ok {
		return fmt.Errorf("kanban: %s is not on the board", id)
	}
	text, err := b.readCardText(path, id)
	if err != nil {
		return err
	}
	stamped, err := ticket.SetField(text, "moved_by_hand", fmt.Sprintf("%s -> %s at %s", from, to, b.now().Format(time.RFC3339)))
	if err != nil {
		return err
	}
	if err := b.writeCardText(ctx, path, stamped); err != nil {
		return err
	}
	return b.appendLog("moved-by-hand", logStr("ticket", id), logStr("from", from), logStr("to", to))
}

// StopLine halts the board: EPOCH +1 and the STOP-LINE record. A second stop
// while stopped is a no-op.
func (b Board) StopLine(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.stopLine(ctx, a) })
}

func (b Board) stopLine(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"--note", a.Note}); r != nil {
		return nil, r
	}
	if b.stopped() != nil {
		return []string{"stop-line: already stopped; not bumping epoch again"}, nil
	}
	n := b.readEpoch() + 1
	if err := b.writeEpoch(ctx, n); err != nil {
		return nil, err
	}
	rec := &StopLine{Epoch: n, Actor: a.Actor, Note: a.Note, TS: b.stamp()}
	if err := b.writeStop(ctx, rec); err != nil {
		return nil, err
	}
	if err := b.appendLog("stop-line",
		logNum("epoch", n), logStr("actor", a.Actor), logStr("note", a.Note)); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("STOP-LINE set at epoch %d by %s: %s", n, a.Actor, a.Note)}, nil
}

// ResumeLine clears the stop-line after verified green.
func (b Board) ResumeLine(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.resumeLine(ctx, a) })
}

func (b Board) resumeLine(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	rec := b.stopped()
	if rec == nil {
		return nil, refused("no stop-line set; nothing to resume (status shows the epoch)")
	}
	if err := b.fs().remove(b.stopPath()); err != nil {
		return nil, err
	}
	if err := b.appendLog("resume-line", logStr("actor", a.Actor),
		logObj("prior", logNum("epoch", rec.Epoch), logStr("actor", rec.Actor), logStr("note", rec.Note))); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("stop-line cleared by %s (was epoch %d)", a.Actor, rec.Epoch)}, nil
}

var clusterRe = regexp.MustCompile(`^[a-z0-9]+( [a-z0-9]+)?$`)

var tagRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// New writes a full ticket into backlog, or into ready when asked, where the
// ticket bar applies. Plain-text dependencies warn; nothing is written while
// any problem stands.
func (b Board) New(ctx context.Context, n NewTicket) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.new(ctx, n) })
}

func (b Board) new(ctx context.Context, n NewTicket) ([]string, error) {
	if err := actorRefusal(n.Actor); err != nil {
		return nil, err
	}
	values := [][2]string{
		{"<ID>", n.ID}, {"--title", n.Title}, {"--cluster", n.Cluster},
		{"--summary", n.Summary}, {"--repos", n.Repos}, {"--body-file", n.BodyFile},
	}
	for _, dep := range n.DependsOn {
		values = append(values, [2]string{"--depends", dep})
	}
	for _, tag := range n.Tags {
		values = append(values, [2]string{"--tag", tag})
	}
	if r := scanArgs(true, values...); r != nil {
		return nil, r
	}
	cid := strings.TrimSpace(n.ID)
	var problems []string
	if !idRe.MatchString(cid) {
		problems = append(problems, fmt.Sprintf("'%s': an id is a letter, then A-Z and 0-9 with single dashes between, like AUTH-TOKEN", cid))
	}
	if _, col, ok := b.Find(cid); ok {
		problems = append(problems, fmt.Sprintf("%s: already on the board (in %s/)", cid, col))
	}
	title := collapse(n.Title)
	if title == "" {
		problems = append(problems, "--title is required")
	} else if len(title) > 90 {
		problems = append(problems, fmt.Sprintf("--title is %d chars; the limit is 90 (what and where, one line)", len(title)))
	}
	if !clusterRe.MatchString(strings.ToLower(collapse(n.Cluster))) {
		problems = append(problems, "--cluster: one short lowercase word or two naming the group (letters and digits)")
	}
	summary := collapse(n.Summary)
	if len(summary) < 80 {
		problems = append(problems, fmt.Sprintf("--summary is %d chars; it needs at least 80 (what changes, why, what done looks like)", len(summary)))
	}
	if n.Type == "" {
		n.Type = "feature"
	}
	n.Type = strings.ToLower(strings.TrimSpace(n.Type))
	if !oneOfType(n.Type) {
		problems = append(problems, fmt.Sprintf("--type '%s': use one of fix, feature, test, docs, ops, decision, chore", n.Type))
	}
	if n.Priority == "" {
		n.Priority = "P2"
	}
	if n.Priority != "P0" && n.Priority != "P1" && n.Priority != "P2" && n.Priority != "P3" {
		problems = append(problems, fmt.Sprintf("--priority '%s': use one of P0, P1, P2, P3", n.Priority))
	}
	if n.Est == "" {
		n.Est = "M"
	}
	if n.Est != "S" && n.Est != "M" && n.Est != "L" {
		problems = append(problems, fmt.Sprintf("--est '%s': use one of S, M, L", n.Est))
	}
	for _, tag := range n.Tags {
		if !tagRe.MatchString(tag) || len(tag) > 24 {
			problems = append(problems, fmt.Sprintf("--tag '%s': lowercase words joined by dashes, at most 24 characters", tag))
		}
	}
	if len(n.Tags) > 8 {
		problems = append(problems, fmt.Sprintf("%d --tag values; at most 8", len(n.Tags)))
	}
	var warnings []string
	for _, dep := range n.DependsOn {
		problem, warning := b.checkDep(dep, "--depends")
		if problem != "" {
			problems = append(problems, problem)
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	var body string
	if n.BodyFile != "" {
		data, err := b.fs().readFile(n.BodyFile)
		if err != nil {
			reason := "cannot read"
			if os.IsNotExist(err) {
				reason = "no such file or directory"
			}
			return nil, refused("--body-file %s: %s; fix the path, then retry", n.BodyFile, reason)
		}
		if !utf8.Valid(data) {
			return nil, refused("--body-file %s: not UTF-8; re-save it as UTF-8, then retry", n.BodyFile)
		}
		body = strings.TrimRightFunc(string(data), unicode.IsSpace) + "\n"
		if strings.TrimSpace(body) == "" {
			return nil, refused("--body-file %s: the file is empty; a worker must be able to finish the card from its body", n.BodyFile)
		}
	} else {
		body = ticket.Skeleton(cid, title)
	}
	if len(problems) > 0 {
		return nil, refused("%s", strings.Join(problems, "\n"))
	}
	dest := "backlog"
	if n.Ready || n.Expedite {
		dest = "ready"
	}
	text := "---\nid: " + cid + "\n---\n\n" + body
	text = setFieldPanicFree(text, "title", title)
	text = setFieldPanicFree(text, "cluster", strings.ToLower(collapse(n.Cluster)))
	text = setFieldPanicFree(text, "summary", foldedValue("summary", summary))
	if n.Type != "" {
		text = setFieldPanicFree(text, "type", n.Type)
	}
	if n.Repos != "" {
		text = setFieldPanicFree(text, "repos", n.Repos)
	}
	text = setFieldPanicFree(text, "priority", n.Priority)
	text = setFieldPanicFree(text, "est", n.Est)
	if len(n.Tags) > 0 {
		text = setFieldPanicFree(text, "tags", ticket.FormatList(n.Tags))
	}
	if len(n.DependsOn) > 0 {
		text = setFieldPanicFree(text, "depends_on", ticket.FormatList(n.DependsOn))
	}
	if n.Expedite {
		text = setFieldPanicFree(text, "expedite", "true")
	}
	text = setFieldPanicFree(text, "created_at", b.stamp())
	text = setFieldPanicFree(text, "created_by", n.Actor)
	if !n.Force && dest == "ready" {
		if failures := b.barText(cid+".md", text, dest, ""); len(failures) > 0 {
			return nil, barRefusal(cid, failures)
		}
	}
	if err := b.fs().mkdirAll(filepath.Join(b.Root, dest)); err != nil {
		return nil, err
	}
	dst := b.ticketPath(dest, cid)
	if err := b.writeCardText(ctx, dst, text); err != nil {
		return nil, err
	}
	pairs := []logPair{logStr("ticket", cid), logStr("actor", n.Actor), logStr("column", dest)}
	if n.Force {
		pairs = append(pairs, logBool("forced", true))
	}
	if err := b.appendLog("new", pairs...); err != nil {
		return nil, err
	}
	return append(warnings, dst), nil
}

func oneOfType(v string) bool {
	switch v {
	case "fix", "feature", "test", "docs", "ops", "decision", "chore":
		return true
	}
	return false
}

// foldedValue answers the wrapped lines of a folded block, the value
// ticket.SetField stores for a multi-line summary.
func foldedValue(key, text string) string {
	block := ticket.WrapFolded(key, text)
	var lines []string
	for _, line := range strings.Split(strings.TrimPrefix(block, key+": >-\n"), "\n") {
		lines = append(lines, strings.TrimPrefix(line, "  "))
	}
	return strings.Join(lines, "\n")
}

// settable are the descriptive fields set may write; status moves only
// through the flow commands.
var settable = []string{
	"cluster", "summary", "title", "type", "priority", "est", "branch", "worktree",
	"depends_on", "repos", "blocked_on", "unblock_when",
}

func settableList() string { return strings.Join(settable, ", ") }

// Set edits a ticket's descriptive fields, with new's validation; a refused
// key refuses the whole call, so no partial write happens.
func (b Board) Set(ctx context.Context, actor, id string, pairs []string) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.set(ctx, actor, id, pairs) })
}

func (b Board) set(ctx context.Context, actor, id string, pairs []string) ([]string, error) {
	if err := actorRefusal(actor); err != nil {
		return nil, err
	}
	values := [][2]string{{"<ID>", id}}
	for _, kv := range pairs {
		values = append(values, [2]string{"key=value", kv})
	}
	if r := scanArgs(false, values...); r != nil {
		return nil, r
	}
	for _, kv := range pairs {
		if !strings.Contains(kv, "=") {
			return nil, refused("usage: set <ID> --actor <id> key=value ...; settable: %s", settableList())
		}
	}
	type change struct{ key, value string }
	var wants []change
	for _, kv := range pairs {
		key, value, _ := strings.Cut(kv, "=")
		key = strings.TrimSpace(key)
		if !oneOfSettable(key) {
			return nil, refused("%s: not a settable field (descriptive fields only: %s)", key, settableList())
		}
		wants = append(wants, change{key, value})
	}
	src, col, ok := b.Find(id)
	if !ok {
		return nil, refused("%s: not on the board; run list to see ids, or new to create it", id)
	}
	t, _ := b.loadCard(col, id)
	if col == "done" {
		return nil, refused("%s: done cards are not edited (it is in done/)", id)
	}
	for _, w := range wants {
		switch w.key {
		case "type":
			v := strings.ToLower(strings.TrimSpace(w.value))
			if !oneOfType(v) {
				return nil, refused("type='%s': use one of fix, feature, test, docs, ops, decision, chore", w.value)
			}
		case "priority":
			if w.value != "P0" && w.value != "P1" && w.value != "P2" && w.value != "P3" {
				return nil, refused("priority='%s': use one of P0, P1, P2, P3", w.value)
			}
		case "est":
			if w.value != "S" && w.value != "M" && w.value != "L" {
				return nil, refused("est='%s': use one of S, M, L", w.value)
			}
		case "cluster":
			if !clusterRe.MatchString(strings.ToLower(collapse(w.value))) {
				return nil, refused("cluster: one short lowercase word or two naming the group (letters and digits)")
			}
		case "title":
			flat := collapse(w.value)
			if flat == "" {
				return nil, refused("title may not be empty (what and where, one line)")
			}
			if len(flat) > 90 {
				return nil, refused("title is %d chars; the limit is 90 (what and where, one line)", len(flat))
			}
		case "summary":
			if len(collapse(w.value)) < 80 {
				return nil, refused("summary is %d chars; it needs at least 80 (what changes, why, what done looks like)", len(collapse(w.value)))
			}
		}
	}
	for _, w := range wants {
		if w.key != "type" {
			continue
		}
		if authorIs(t, actor) {
			return nil, refused("%s: %s is the author or a holder (%s) and does not change the type of their own card (that would skip its gates); the lead does",
				id, actor, whoHolds(t))
		}
	}
	var warnings []string
	text, err := b.readCardText(src, id)
	if err != nil {
		return nil, err
	}
	text = clearHand(text)
	for _, w := range wants {
		switch w.key {
		case "depends_on":
			for _, dep := range ticket.ParseList(w.value) {
				problem, warning := b.checkDep(dep, "depends_on")
				if problem != "" {
					return nil, refused("%s", problem)
				}
				if warning != "" {
					warnings = append(warnings, warning)
				}
			}
			text = setFieldPanicFree(text, "depends_on", ticket.FormatList(ticket.ParseList(w.value)))
		case "blocked_on":
			text = setFieldPanicFree(text, "blocked_on", ticket.FormatList(ticket.ParseList(w.value)))
		case "cluster", "type":
			text = setFieldPanicFree(text, w.key, strings.ToLower(collapse(w.value)))
		default:
			text = setFieldPanicFree(text, w.key, w.value)
		}
	}
	if err := b.writeCardText(ctx, src, text); err != nil {
		return nil, err
	}
	logFields := make([]logPair, 0, len(wants))
	for _, w := range wants {
		logFields = append(logFields, logStr(w.key, collapse(w.value)))
	}
	if err := b.appendLog("set", logStr("ticket", id), logStr("actor", actor),
		logObj("fields", logFields...)); err != nil {
		return nil, err
	}
	return append(warnings, src), nil
}

func oneOfSettable(key string) bool {
	for _, k := range settable {
		if k == key {
			return true
		}
	}
	return false
}
