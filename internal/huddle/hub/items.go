// The permission flow on the needs-you items (SPEC 6.2, 6.6): one tool call the terminal is
// being asked about grows an items row of kind permission — needs.go holds the list it joins —
// carrying the tool and its input cut to 8 KiB, and a decision the first answerer wins. The
// asking hook's long poll is what keeps the item alive: an item whose hook stops polling
// expires, and the server's sweeper withdraws it so the shared list never waits on a dead
// prompt. The machine's remote-allow floor and list every remote allow passes live here too;
// a decision covers the one prompt and nothing here writes a permission rule.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/bmatcuk/doublestar/v4"
)

// KindPermission is the permission flow's kind on the shared items list.
const KindPermission = "permission"

// The two expiry limits of SPEC 6.2.6: an open permission item lives ten minutes, and
// seventy-five seconds once its hook stops polling (the hook is gone).
const (
	PermissionExpiresAfter    = 10 * time.Minute
	PermissionSilentPollAfter = 75 * time.Second
)

// InputLimit is the most of a tool's input an item keeps: past it the input is cut and the
// truncated flag set, and a cut input cannot be allowed remotely (SPEC 6.2.4).
const InputLimit = 8 * 1024

// StateExpired is what a read answers for an open permission item past either expiry limit.
// It is computed, never stored: the sweeper moves the row itself once it notices.
const StateExpired = "expired"

// messageLimit is the most of a denial's message, in UTF-16 units — the permission prompt's
// own reason cap.
const messageLimit = 300

// PermissionItem is one permission item as its routes answer it: the shared item's fields
// plus the tool call it holds and the decision one answer wrote. LastPollAt stays off the
// answer; it is the expiry's own bookkeeping.
type PermissionItem struct {
	Item
	Tool       string `json:"tool,omitempty"`
	Input      string `json:"input,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Decision   string `json:"decision,omitempty"`
	Message    string `json:"message,omitempty"`
	LastPollAt string `json:"-"`
}

// permissionColumns is the permission flow's own columns on items, after the shared ones.
const permissionColumns = `tool, input, truncated, last_poll_at, decided_at, decision, message`

// CreatePermissionItem records one permission item (SPEC 6.2.1) and answers its id. The item
// is born polled — the POST that creates it is its hook's hello — and its summary is the
// shared cleaner's one line over the tool and its joined input.
func (d *DB) CreatePermissionItem(ctx context.Context, sessionID, tool string, input []byte, now time.Time) (int64, error) {
	kept, truncated := cutInput(normalizeInput(input))
	at := jsfmt.ISOTime(now)
	// a fresh key per prompt: the hook posts one item per permission prompt, and no replay of
	// a post there is (the hook never retries a prompt it already asked about)
	noise := make([]byte, 8)
	_, _ = rand.Read(noise)
	key := fmt.Sprintf("permission:%s:%s", sessionID, hex.EncodeToString(noise))
	if err := d.st.Run(ctx,
		"INSERT INTO items (key, session_id, kind, summary, created_at, state, "+permissionColumns+") "+
			"VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?, ?, ?, ?, ?)",
		key, sessionID, KindPermission, cleanSummary(tool+": "+JoinedInput(tool, kept)), at,
		tool, kept, truncated, at, nil, "", ""); err != nil {
		return 0, err
	}
	row, err := d.st.Get(ctx, "SELECT id FROM items WHERE key = ?", key)
	if err != nil || row == nil {
		return 0, err
	}
	return rowID(row), nil
}

// PermissionItemByID answers one permission item with its state computed at now, nil when no
// such item or the item is another kind.
func (d *DB) PermissionItemByID(ctx context.Context, id int64, now time.Time) (*PermissionItem, error) {
	row, err := d.st.Get(ctx,
		"SELECT id, session_id, kind, summary, created_at, state, snoozed_until, "+permissionColumns+
			" FROM items WHERE id = ?", id)
	if err != nil || row == nil {
		return nil, err
	}
	if textOfRow(row, "kind") != KindPermission {
		return nil, nil
	}
	item := permissionOfRow(row)
	item.State = PermissionState(item, now)
	return &item, nil
}

// PermissionState answers the permission item's state at now. A decision, a withdrawal or a
// dismissal stays as stored; an open item behind its snooze answers snoozed; an open item past
// either expiry limit answers expired. A timestamp that parses as no time reads as long ago,
// so a torn row never looks alive.
func PermissionState(item PermissionItem, now time.Time) string {
	if item.State != StateOpen {
		return item.State
	}
	if item.SnoozedUntil != "" && parseEventTime(item.SnoozedUntil).After(now) {
		return StateSnoozed
	}
	silent := item.LastPollAt
	if silent == "" {
		silent = item.CreatedAt
	}
	if now.Sub(parseEventTime(item.CreatedAt)) >= PermissionExpiresAfter ||
		now.Sub(parseEventTime(silent)) >= PermissionSilentPollAfter {
		return StateExpired
	}
	return StateOpen
}

// DecidePermission records one decision — allow or deny — on one item and answers whether
// this call made it (SPEC 6.2.2: whichever answers first wins). An item whose state at now is
// not open refuses: the other answer, the app's or the terminal's, already holds. The write is
// guarded by the state its read saw and verified by a read back, so two answers in the same
// instant on the store's one connection still leave exactly one winner. The denial's message
// is cleaned here, where it enters storage: whatever reads it back — the hook's answer, the
// dashboard — shows one calm line.
func (d *DB) DecidePermission(ctx context.Context, id int64, decision, message, at string, now time.Time) (bool, error) {
	message = jsfmt.UTF16Slice(cleanSummary(message), 0, messageLimit)
	won := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx,
			"SELECT kind, state, snoozed_until, created_at, last_poll_at FROM items WHERE id = ?", id)
		if err != nil {
			return err
		}
		if row == nil || textOfRow(row, "kind") != KindPermission {
			return nil
		}
		stored := textOfRow(row, "state")
		if PermissionState(permissionOfRow(row), now) != StateOpen {
			return nil
		}
		if err := q.Run(ctx,
			"UPDATE items SET state = 'answered', decision = ?, message = ?, decided_at = ? "+
				"WHERE id = ? AND state = ?", decision, message, at, id, stored); err != nil {
			return err
		}
		after, err := q.Get(ctx, "SELECT state, decision, decided_at FROM items WHERE id = ?", id)
		if err != nil {
			return err
		}
		won = after != nil && textOfRow(after, "state") == StateAnswered &&
			textOfRow(after, "decision") == decision && textOfRow(after, "decided_at") == at
		return nil
	})
	return won, err
}

// TouchPoll marks one item polled at at (SPEC 6.2.6): the poll is what proves its hook lives.
func (d *DB) TouchPoll(ctx context.Context, id int64, at string) error {
	return d.st.Run(ctx, "UPDATE items SET last_poll_at = ? WHERE id = ? AND kind = ?", at, id, KindPermission)
}

// WithdrawPermissions marks the session's open permission items withdrawn (SPEC 6.2.2: the
// terminal answered its own prompt first) and answers their ids. The guarded write keeps a
// decision that lands in the same instant: it already holds.
func (d *DB) WithdrawPermissions(ctx context.Context, sessionID, at string) ([]int64, error) {
	rows, err := d.st.All(ctx,
		"SELECT id FROM items WHERE session_id = ? AND kind = ? AND state = 'open' ORDER BY id",
		sessionID, KindPermission)
	if err != nil {
		return nil, err
	}
	withdrawn := []int64{}
	for _, row := range rows {
		id := rowID(row)
		if err := d.st.Run(ctx,
			"UPDATE items SET state = 'withdrawn', decided_at = ? WHERE id = ? AND state = 'open'", at, id); err != nil {
			return withdrawn, err
		}
		after, err := d.st.Get(ctx, "SELECT state FROM items WHERE id = ?", id)
		if err != nil {
			return withdrawn, err
		}
		if after != nil && textOfRow(after, "state") == StateWithdrawn {
			withdrawn = append(withdrawn, id)
		}
	}
	return withdrawn, nil
}

// ExpirePermissions withdraws every open permission item past either expiry limit and answers
// the sessions that held one, so the needs-you list and the sessions' states drop a dead
// prompt without waiting for the next read. The expiry itself stays computed on read; this is
// only the sweeper that keeps the stored list honest.
func (d *DB) ExpirePermissions(ctx context.Context, now time.Time) ([]string, error) {
	at := jsfmt.ISOTime(now)
	rows, err := d.st.All(ctx,
		"SELECT id, session_id FROM items WHERE kind = ? AND state = 'open' AND ("+
			"created_at < ? OR COALESCE(last_poll_at, created_at) < ?)",
		KindPermission, jsfmt.ISOTime(now.Add(-PermissionExpiresAfter)),
		jsfmt.ISOTime(now.Add(-PermissionSilentPollAfter)))
	if err != nil {
		return nil, err
	}
	sessions := []string{}
	for _, row := range rows {
		if err := d.st.Run(ctx,
			"UPDATE items SET state = 'withdrawn', decided_at = ? WHERE id = ? AND state = 'open'",
			at, rowID(row)); err != nil {
			return sessions, err
		}
		sessions = append(sessions, textOfRow(row, "session_id"))
	}
	return sessions, nil
}

// permissionOfRow builds one permission item from its stored columns; its State holds what is
// stored, for PermissionState to compute over.
func permissionOfRow(row store.Row) PermissionItem {
	return PermissionItem{
		Item: Item{
			ID:           rowID(row),
			SessionID:    textOfRow(row, "session_id"),
			Kind:         KindPermission,
			Summary:      textOfRow(row, "summary"),
			CreatedAt:    textOfRow(row, "created_at"),
			State:        textOfRow(row, "state"),
			SnoozedUntil: textOfRow(row, "snoozed_until"),
		},
		Tool:       textOfRow(row, "tool"),
		Input:      textOfRow(row, "input"),
		Truncated:  row["truncated"] == int64(1),
		Decision:   textOfRow(row, "decision"),
		Message:    textOfRow(row, "message"),
		LastPollAt: textOfRow(row, "last_poll_at"),
	}
}

// normalizeInput answers the input as compact JSON, key order kept. Text that is not JSON
// stays as it came: the floor checks read it as one string either way.
func normalizeInput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if value, err := jsfmt.DecodeValue(raw); err == nil && value != nil {
		return value.EncodeJSON()
	}
	return strings.TrimSpace(string(raw))
}

// cutInput answers the input cut to InputLimit bytes, on a rune boundary so the kept text is
// whole UTF-8, and whether anything was lost.
func cutInput(norm string) (string, bool) {
	if len(norm) <= InputLimit {
		return norm, false
	}
	at := InputLimit
	for at > 0 && !utf8.RuneStart(norm[at]) {
		at--
	}
	return norm[:at], true
}

// The floor's refusal reasons (SPEC 6.6): the machine holds them whatever the allow list says.
const (
	floorDeleting = "refused: a deleting command; allow it in the terminal"
	floorForced   = "refused: a forced push; allow it in the terminal"
	floorPath     = "refused: the input names a .ssh, .gnupg or secrets path; allow it in the terminal"
	listMiss      = "not in this machine's remote allow list; allow it in the terminal"
)

// RemoteAllow answers whether the app may allow one tool call on this machine (SPEC 6.6), and
// when not, the reason the dashboard reads. The floor comes first, whatever the list says,
// because it is not a pattern but a refusal: a Bash command the channel's approval rules call
// deleting or force-pushing, and any input string naming a path with a .ssh, .gnupg or
// secrets segment. Then the list: "*" alone admits every call, Name(glob) admits the named
// tool with doublestar over the joined input; a pattern of any other shape matches nothing, so
// a typo refuses rather than allows.
func RemoteAllow(patterns []string, tool, input string) (string, bool) {
	if tool == "Bash" {
		for _, rule := range channel.Matching(JoinedInput(tool, input), "") {
			switch rule.ID {
			case "delete":
				return floorDeleting, false
			case "force_push":
				return floorForced, false
			}
		}
	}
	for _, s := range inputStrings(input) {
		if guardedText(s) {
			return floorPath, false
		}
	}
	joined := JoinedInput(tool, input)
	for _, pattern := range patterns {
		if patternAllows(pattern, tool, joined) {
			return "", true
		}
	}
	return listMiss, false
}

// patternAllows matches one allow-list pattern: "*" alone admits every call, Name(glob)
// admits the named tool with doublestar over the joined input. Any other shape matches
// nothing, and a glob doublestar refuses matches nothing: both fail closed.
func patternAllows(pattern, tool, joined string) bool {
	if pattern == "*" {
		return true
	}
	name, rest, ok := strings.Cut(pattern, "(")
	if !ok || name == "" || name != tool || !strings.HasSuffix(rest, ")") {
		return false
	}
	glob := rest[:len(rest)-1]
	if glob == "" {
		return false
	}
	matched, err := doublestar.Match(glob, joined)
	return err == nil && matched
}

// JoinedInput answers the one string an allow-list pattern matches against: the input's
// member values in order — Bash's command alone, so its description never hides it — joined
// by single spaces. A pattern like Bash(npm test*) or Read(/tmp/*) then reads the way its
// owner wrote it. Text that is not JSON is itself.
func JoinedInput(tool, input string) string {
	if input == "" {
		return ""
	}
	value, err := jsfmt.DecodeValue([]byte(input))
	if err != nil || value == nil {
		return strings.TrimSpace(input)
	}
	if value.Kind() == jsfmt.KindString {
		return value.Text()
	}
	if tool == "Bash" {
		if command, ok := fieldText(value, "command"); ok {
			return command
		}
	}
	switch value.Kind() {
	case jsfmt.KindObject, jsfmt.KindArray:
		members := value.Fields()
		if value.Kind() == jsfmt.KindArray {
			members = value.Items()
		}
		parts := []string{}
		for _, member := range members {
			if member == nil {
				continue
			}
			if member.Kind() == jsfmt.KindString {
				parts = append(parts, member.Text())
				continue
			}
			parts = append(parts, member.EncodeJSON())
		}
		return strings.Join(parts, " ")
	}
	return value.EncodeJSON()
}

// fieldText answers one object member's text and whether it is a string.
func fieldText(value *jsfmt.Value, key string) (string, bool) {
	if value == nil || value.Kind() != jsfmt.KindObject {
		return "", false
	}
	for i, name := range value.Keys() {
		if name != key {
			continue
		}
		field := value.Fields()[i]
		if field != nil && field.Kind() == jsfmt.KindString {
			return field.Text(), true
		}
	}
	return "", false
}

// guardedText says whether s names a path under ~/.ssh, ~/.gnupg or a secrets directory
// (SPEC 6.6): one of its whitespace words carries a slash segment named .ssh, .gnupg or
// secrets. Word-then-segment, so $HOME/.ssh, a relative .ssh/, "tar cf x secrets" (the floor's
// Bash(* secrets*)) and a path named inside prose all match, whatever the shell would expand
// them to.
func guardedText(s string) bool {
	for _, word := range strings.Fields(s) {
		for _, segment := range strings.Split(word, "/") {
			switch segment {
			case ".ssh", ".gnupg", "secrets":
				return true
			}
		}
	}
	return false
}

// inputStrings answers every string value the input carries, walked recursively through its
// objects and arrays. Text that is not JSON answers as itself, so the floor reads a torn
// input too.
func inputStrings(input string) []string {
	if input == "" {
		return nil
	}
	value, err := jsfmt.DecodeValue([]byte(input))
	if err != nil || value == nil {
		return []string{strings.TrimSpace(input)}
	}
	var out []string
	var walk func(v *jsfmt.Value)
	walk = func(v *jsfmt.Value) {
		if v == nil {
			return
		}
		switch v.Kind() {
		case jsfmt.KindString:
			out = append(out, v.Text())
		case jsfmt.KindObject:
			for _, member := range v.Fields() {
				walk(member)
			}
		case jsfmt.KindArray:
			for _, item := range v.Items() {
				walk(item)
			}
		}
	}
	walk(value)
	return out
}
