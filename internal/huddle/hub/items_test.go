package hub

// The permission flow on the needs-you items: a tool call with its input cut to 8 KiB, a
// decision the first answerer wins, expiry at ten minutes of age or seventy-five seconds
// without a poll, withdrawal when the terminal answers first, and the machine's remote-allow
// floor and list every remote allow passes. The store is exercised over a real SQLite file in
// a temp home.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// permNow is one fixed moment the cases build timestamps around.
var permNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// isoPerm renders one moment the way the items keep their timestamps.
func isoPerm(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// growPermission records one permission item through the flow's own door and answers its id.
func growPermission(t *testing.T, db *DB, session, tool, input string, now time.Time) int64 {
	t.Helper()
	id, err := db.CreatePermissionItem(context.Background(), session, tool, []byte(input), now)
	if err != nil {
		t.Fatalf("create the permission item: %v", err)
	}
	if id <= 0 {
		t.Fatalf("the item's id = %d, want a row's number", id)
	}
	return id
}

// readPermission answers one item with its state computed at now.
func readPermission(t *testing.T, db *DB, id int64, now time.Time) PermissionItem {
	t.Helper()
	item, err := db.PermissionItemByID(context.Background(), id, now)
	if err != nil {
		t.Fatalf("read the item: %v", err)
	}
	if item == nil {
		t.Fatalf("no permission item %d", id)
	}
	return *item
}

func TestCreatePermissionItemCutsTheInputToEightKiB(t *testing.T) {
	db := openForTest(t)
	id := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, permNow)
	item := readPermission(t, db, id, permNow.Add(time.Second))
	if item.Kind != KindPermission || item.SessionID != "s-1" || item.Tool != "Bash" {
		t.Errorf("the item = %+v, want a permission item of s-1 running Bash", item)
	}
	if item.Input != `{"command":"npm test"}` {
		t.Errorf("input = %q, want the compact JSON as it came", item.Input)
	}
	if item.Truncated {
		t.Error("an input under the limit is marked truncated")
	}
	if item.State != StateOpen || item.CreatedAt != isoPerm(permNow) {
		t.Errorf("state/created = %q/%q, want open at the given moment", item.State, item.CreatedAt)
	}
	// the POST that creates the item is its first poll: without it, a fresh item would read
	// expired before its hook ever polled
	if item.LastPollAt != isoPerm(permNow) {
		t.Errorf("last poll = %q, want the creation's own time", item.LastPollAt)
	}

	big := growPermission(t, db, "s-1", "Read", `{"file_path":"/tmp/`+strings.Repeat("x", 9000)+`"}`, permNow)
	item = readPermission(t, db, big, permNow.Add(time.Second))
	if !item.Truncated {
		t.Error("an input past 8 KiB is not marked truncated")
	}
	if len(item.Input) > InputLimit {
		t.Errorf("the kept input is %d bytes, want at most %d", len(item.Input), InputLimit)
	}
	// a cut that would split a multi-byte rune backs off to the rune's start
	runes := growPermission(t, db, "s-1", "Bash", `{"command":"`+strings.Repeat("é", 5000)+`"}`, permNow)
	item = readPermission(t, db, runes, permNow.Add(time.Second))
	if !item.Truncated || !validUTF8(item.Input) {
		t.Errorf("a cut through a multi-byte rune: truncated %v, valid %v", item.Truncated, validUTF8(item.Input))
	}
	// text that is not JSON stays as it came: the floor reads it as one string either way
	raw := growPermission(t, db, "s-1", "Bash", `npm test`, permNow)
	item = readPermission(t, db, raw, permNow.Add(time.Second))
	if item.Input != "npm test" || item.Truncated {
		t.Errorf("a bare text input = %q (%v), want it kept whole", item.Input, item.Truncated)
	}
}

// validUTF8 says whether s is whole runes.
func validUTF8(s string) bool { return utf8.ValidString(s) }

func TestThePermissionSummaryIsOneCleanLine(t *testing.T) {
	db := openForTest(t)
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"a plain command", `{"command":"npm test"}`, "Bash: npm test"},
		{"a newline becomes one space", `{"command":"rm\n-rf /tmp"}`, "Bash: rm -rf /tmp"},
		{"a control character becomes one space", `{"command":"a\u0000b"}`, "Bash: a b"},
		{"an escape becomes one space", `{"command":"a\u001bb"}`, "Bash: a b"},
		{"bidi overrides become one space", `{"command":"a\u202eb"}`, "Bash: a b"},
		{"bidi isolates become one space", `{"command":"a\u2066b\u2069"}`, "Bash: a b"},
		{"a run of spaces collapses", `{"command":"a  b"}`, "Bash: a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := growPermission(t, db, "s-1", "Bash", tc.input, permNow)
			if summary := readPermission(t, db, id, permNow.Add(time.Second)).Summary; summary != tc.want {
				t.Errorf("summary = %q, want %q", summary, tc.want)
			}
		})
	}
	// a long command is cut to one calm line
	id := growPermission(t, db, "s-1", "Bash", `{"command":"`+strings.Repeat("w", 400)+`"}`, permNow)
	if got := len([]rune(readPermission(t, db, id, permNow.Add(time.Second)).Summary)); got > 200 {
		t.Errorf("the summary runs %d runes, want at most 200", got)
	}
}

func TestPermissionStateComputesTheTwoExpiries(t *testing.T) {
	fresh := PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow), SnoozedUntil: ""},
		LastPollAt: isoPerm(permNow)}
	cases := []struct {
		name string
		item PermissionItem
		now  time.Time
		want string
	}{
		{"a young polled item", fresh, permNow.Add(30 * time.Second), StateOpen},
		{"ten minutes of age", fresh, permNow.Add(10 * time.Minute), StateExpired},
		{
			"just under ten minutes, its poll alive",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow)},
				LastPollAt: isoPerm(permNow.Add(9 * time.Minute))},
			permNow.Add(10*time.Minute - time.Second),
			StateOpen,
		},
		{
			"the poll went silent for seventy-five seconds",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow)},
				LastPollAt: isoPerm(permNow.Add(time.Minute))},
			permNow.Add(time.Minute + 75*time.Second),
			StateExpired,
		},
		{
			"a live poll keeps a silent item open",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow)},
				LastPollAt: isoPerm(permNow.Add(2 * time.Minute))},
			permNow.Add(2*time.Minute + 74*time.Second),
			StateOpen,
		},
		{
			"a decided item stays decided",
			PermissionItem{Item: Item{State: StateAnswered, CreatedAt: isoPerm(permNow)}},
			permNow.Add(time.Hour),
			StateAnswered,
		},
		{
			"a withdrawn item stays withdrawn",
			PermissionItem{Item: Item{State: StateWithdrawn, CreatedAt: isoPerm(permNow)}},
			permNow.Add(time.Hour),
			StateWithdrawn,
		},
		{
			"a dismissed item stays dismissed",
			PermissionItem{Item: Item{State: StateDismissed, CreatedAt: isoPerm(permNow)}},
			permNow.Add(time.Hour),
			StateDismissed,
		},
		{
			"a snooze holds until its until",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow),
				SnoozedUntil: isoPerm(permNow.Add(15 * time.Minute))}},
			permNow.Add(time.Minute),
			StateSnoozed,
		},
		{
			"a snooze past its until with a live poll reads open",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow),
				SnoozedUntil: isoPerm(permNow.Add(time.Minute))}},
			permNow.Add(time.Minute + 10*time.Second),
			StateOpen,
		},
		{
			"a snooze past its until with a silent poll reads expired",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: isoPerm(permNow),
				SnoozedUntil: isoPerm(permNow.Add(time.Minute))}},
			permNow.Add(30 * time.Minute),
			StateExpired,
		},
		{
			"a timestamp that parses as no time reads expired",
			PermissionItem{Item: Item{State: StateOpen, CreatedAt: "not a time"}},
			permNow,
			StateExpired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PermissionState(tc.item, tc.now); got != tc.want {
				t.Errorf("PermissionState = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecidePermissionLetsTheFirstAnswerWin(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	id := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, permNow)
	now := permNow.Add(5 * time.Second)
	won, err := db.DecidePermission(ctx, id, "allow", "", isoPerm(now), now)
	if err != nil || !won {
		t.Fatalf("the first decision = (%v, %v), want it to win", won, err)
	}
	// the second answer — the app's or the terminal's — changes nothing
	won, err = db.DecidePermission(ctx, id, "deny", "no", isoPerm(now), now)
	if err != nil {
		t.Fatalf("the second decision: %v", err)
	}
	if won {
		t.Error("a second decision won; the first answer must hold")
	}
	item := readPermission(t, db, id, now)
	if item.State != StateAnswered || item.Decision != "allow" {
		t.Errorf("the item = %+v, want answered allow by the first answer", item)
	}
	// a denied item keeps its message for Claude, cleaned to one line
	other := growPermission(t, db, "s-1", "Bash", `{"command":"npm run build"}`, permNow)
	if won, err = db.DecidePermission(ctx, other, "deny", "not\ntonight", isoPerm(now), now); err != nil || !won {
		t.Fatalf("deny: (%v, %v)", won, err)
	}
	item = readPermission(t, db, other, now)
	if item.State != StateAnswered || item.Decision != "deny" || item.Message != "not tonight" {
		t.Errorf("the denied item = %+v, want its decision and a one-line message kept", item)
	}
	// an unknown id wins nothing, and neither does another kind's item
	if won, err = db.DecidePermission(ctx, 987654, "allow", "", isoPerm(now), now); err != nil || won {
		t.Errorf("an unknown id = (%v, %v), want (false, nil)", won, err)
	}
}

func TestSnoozeHoldsAndDismissDropsAPermissionItem(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	id := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, permNow)
	// the shared snooze and dismiss work on a permission item like on any other
	if err := db.SnoozeItem(ctx, id, permNow.Add(15*time.Minute)); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	item := readPermission(t, db, id, permNow.Add(time.Minute))
	if item.State != StateSnoozed || item.SnoozedUntil != isoPerm(permNow.Add(15*time.Minute)) {
		t.Errorf("the snoozed item = %+v, want snoozed a quarter hour", item)
	}
	// a snoozed item refuses a decision: it is not waiting
	if won, err := db.DecidePermission(ctx, id, "allow", "", isoPerm(permNow.Add(time.Minute)), permNow.Add(time.Minute)); err != nil || won {
		t.Errorf("deciding a snoozed item = (%v, %v), want refused", won, err)
	}
	if err := db.DismissItem(ctx, id); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if item = readPermission(t, db, id, permNow.Add(2*time.Minute)); item.State != StateDismissed {
		t.Errorf("the dismissed item reads %q, want dismissed", item.State)
	}
	// a decided item refuses the shared dismiss: its answer already happened
	other := growPermission(t, db, "s-1", "Bash", `{"command":"npm run build"}`, permNow)
	if _, err := db.DecidePermission(ctx, other, "deny", "", isoPerm(permNow), permNow); err != nil {
		t.Fatalf("decide: %v", err)
	}
	err := db.DismissItem(ctx, other)
	var missing *MissingItemError
	if !errors.As(err, &missing) {
		t.Errorf("dismissing a decided item = %v, want the not-open refusal", err)
	}
}

func TestTouchPollKeepsAnItemAlive(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	// an item created long ago whose hook still polls
	id := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, permNow)
	now := permNow.Add(9 * time.Minute)
	if err := db.TouchPoll(ctx, id, isoPerm(now)); err != nil {
		t.Fatalf("touch the poll: %v", err)
	}
	if got := readPermission(t, db, id, now.Add(30*time.Second)).State; got != StateOpen {
		t.Errorf("a polled nine-minute-old item reads %q, want open", got)
	}
	if got := readPermission(t, db, id, now.Add(76*time.Second)).State; got != StateExpired {
		t.Errorf("a touchless item reads %q after 76 s, want expired", got)
	}
}

func TestWithdrawPermissionsAnswersTheTerminalFirst(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	one := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, permNow)
	two := growPermission(t, db, "s-1", "Read", `{"file_path":"/tmp/a"}`, permNow)
	other := growPermission(t, db, "s-2", "Bash", `{"command":"npm run build"}`, permNow)
	at := permNow.Add(3 * time.Second)
	withdrawn, err := db.WithdrawPermissions(ctx, "s-1", isoPerm(at))
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if len(withdrawn) != 2 || withdrawn[0] != one || withdrawn[1] != two {
		t.Fatalf("withdrawn %v, want the session's two %d %d", withdrawn, one, two)
	}
	for _, id := range withdrawn {
		if item := readPermission(t, db, id, at); item.State != StateWithdrawn {
			t.Errorf("the withdrawn item %d reads %q, want withdrawn", id, item.State)
		}
	}
	// another session's item waits on
	if item := readPermission(t, db, other, at); item.State != StateOpen {
		t.Errorf("the other session's item reads %q, want open", item.State)
	}
	// a second withdrawal finds nothing open
	if again, err := db.WithdrawPermissions(ctx, "s-1", isoPerm(at)); err != nil || len(again) != 0 {
		t.Errorf("a second withdrawal = (%v, %v), want none", again, err)
	}
}

func TestExpirePermissionsWithdrawsTheDeadPrompts(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	// a hook that died a minute after asking: its poll went silent past the 75 s limit
	dead := growPermission(t, db, "s-dead", "Bash", `{"command":"npm test"}`, permNow.Add(-3*time.Minute))
	if err := db.TouchPoll(ctx, dead, isoPerm(permNow.Add(-3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	// a hook still polling: its item lives
	live := growPermission(t, db, "s-live", "Bash", `{"command":"npm test"}`, permNow.Add(-time.Minute))
	if err := db.TouchPoll(ctx, live, isoPerm(permNow.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	sessions, err := db.ExpirePermissions(ctx, permNow)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != "s-dead" {
		t.Fatalf("the expired sessions = %v, want s-dead alone", sessions)
	}
	if item := readPermission(t, db, dead, permNow); item.State != StateWithdrawn {
		t.Errorf("the dead prompt reads %q, want withdrawn", item.State)
	}
	if item := readPermission(t, db, live, permNow); item.State != StateOpen {
		t.Errorf("the live prompt reads %q, want open", item.State)
	}
}

func TestAnOpenPermissionPutsItsSessionInNeedsYou(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-1", Machine: "box",
		LastEvent: "UserPromptSubmit", LastEventAt: isoPerm(permNow)}); err != nil {
		t.Fatalf("report: %v", err)
	}
	row, err := db.SessionRow(ctx, "s-1", permNow.Add(time.Second))
	if err != nil || row == nil {
		t.Fatalf("the session row: %v %v", row, err)
	}
	if row.State != StateWorking {
		t.Fatalf("a session with no item reads %q, want working", row.State)
	}
	id := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, permNow.Add(2*time.Second))
	row, err = db.SessionRow(ctx, "s-1", permNow.Add(3*time.Second))
	if err != nil || row == nil {
		t.Fatalf("the session row: %v %v", row, err)
	}
	if row.State != StateNeedsYou {
		t.Errorf("a session with an open item reads %q, want needs-you", row.State)
	}
	// the shared list carries it with its tool call, and a decided item returns the session
	// to its events
	items, err := db.Items(ctx, permNow.Add(3*time.Second))
	if err != nil || len(items) != 1 {
		t.Fatalf("the shared items = %v (%v), want the one permission item", items, err)
	}
	if items[0].Kind != KindPermission || items[0].Tool != "Bash" || items[0].Input != `{"command":"npm test"}` {
		t.Errorf("the shared row = %+v, want it to carry the tool call", items[0])
	}
	if _, err := db.DecidePermission(ctx, id, "allow", "", isoPerm(permNow.Add(4*time.Second)), permNow.Add(4*time.Second)); err != nil {
		t.Fatalf("decide: %v", err)
	}
	row, _ = db.SessionRow(ctx, "s-1", permNow.Add(5*time.Second))
	if row.State != StateWorking {
		t.Errorf("a decided item leaves the session reading %q, want working", row.State)
	}
}

func TestRemoteAllowEnforcesTheFloorWhateverTheListSays(t *testing.T) {
	everything := []string{"*"}
	cases := []struct {
		name     string
		patterns []string
		tool     string
		input    string
	}{
		{"rm at the start", everything, "Bash", `{"command":"rm -rf /tmp/x"}`},
		{"rm after sudo", everything, "Bash", `{"command":"sudo rm /var/log/a"}`},
		{"rm after a separator", everything, "Bash", `{"command":"cd /tmp && rm x"}`},
		{"git rm", everything, "Bash", `{"command":"git rm old.go"}`},
		{"rmdir", everything, "Bash", `{"command":"rmdir empty"}`},
		{"git push --force", everything, "Bash", `{"command":"git push --force origin main"}`},
		{"git push -f", everything, "Bash", `{"command":"git push -f"}`},
		{"git push --force-with-lease", everything, "Bash", `{"command":"git push --force-with-lease"}`},
		{"a secrets path in the command", everything, "Bash", `{"command":"cat app/secrets/prod.env"}`},
		{"the bare word secrets in the command", everything, "Bash", `{"command":"tar cf x secrets"}`},
		{"a .ssh path in another tool's input", everything, "Read", `{"file_path":"/Users/u/.ssh/config"}`},
		{"a tilde .gnupg path", everything, "Read", `{"file_path":"~/.gnupg/private.asc"}`},
		{"a secrets directory in another tool's input", everything, "Edit", `{"file_path":"secrets/token.txt"}`},
		{"an expanded .ssh path over env", everything, "Read", `{"file_path":"$HOME/.ssh/id_rsa"}`},
		{"a secrets path in a nested input string", everything, "Write", `{"content":"key from secrets/prod","file_path":"/tmp/out"}`},
		{"a secrets path in an array input", everything, "Read", `["/tmp/a","secrets/prod.env"]`},
		{"a relative .ssh path", everything, "Bash", `{"command":"cat .ssh/id_rsa"}`},
		{"a deleting command against a narrow list", []string{"Bash(npm test*)"}, "Bash", `{"command":"rm x"}`},
		{"a forced push against a narrow list", []string{"Bash(npm test*)"}, "Bash", `{"command":"git push --force"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := RemoteAllow(tc.patterns, tc.tool, tc.input)
			if ok {
				t.Fatalf("the floor allowed %s %s", tc.tool, tc.input)
			}
			if reason == "" {
				t.Error("a refusal carries no reason")
			}
		})
	}
}

func TestRemoteAllowReadsTheList(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		tool     string
		input    string
		want     bool
	}{
		{"the bare star admits everything", []string{"*"}, "Bash", `{"command":"npm test"}`, true},
		{"the bare star admits other tools", []string{"*"}, "Read", `{"file_path":"/tmp/a"}`, true},
		{"a matching command pattern", []string{"Bash(npm test*)"}, "Bash", `{"command":"npm test -- -f"}`, true},
		{"a command pattern matches the command alone", []string{"Bash(npm test*)"}, "Bash", `{"command":"npm test","description":"run the suite"}`, true},
		{"a pattern for another command", []string{"Bash(npm test*)"}, "Bash", `{"command":"npm run build"}`, false},
		{"a pattern for another tool", []string{"Bash(npm test*)"}, "Read", `{"file_path":"/tmp/a"}`, false},
		{"a path pattern", []string{"Read(/tmp/*)"}, "Read", `{"file_path":"/tmp/a.txt"}`, true},
		{"an empty list allows nothing", nil, "Bash", `{"command":"npm test"}`, false},
		{"a torn pattern allows nothing", []string{"Bash(npm test"}, "Bash", `{"command":"npm test"}`, false},
		{"a pattern with no glob allows nothing", []string{"Bash()"}, "Bash", `{"command":"npm test"}`, false},
		{"the wrong tool name", []string{"Read(/tmp/*)"}, "Edit", `{"file_path":"/tmp/a"}`, false},
		{"two patterns, the second matching", []string{"Read(/tmp/*)", "Bash(git status)"}, "Bash", `{"command":"git status"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := RemoteAllow(tc.patterns, tc.tool, tc.input)
			if ok != tc.want {
				t.Errorf("RemoteAllow(%q, %s, …) = %v, want %v", tc.patterns, tc.tool, ok, tc.want)
			}
		})
	}
}

func TestJoinedInput(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"bash keeps its command alone", "Bash", `{"description":"run it","command":"npm test --watch"}`, "npm test --watch"},
		{"other tools join their values in order", "Read", `{"file_path":"/tmp/a","limit":10}`, "/tmp/a 10"},
		{"a bare string input is itself", "WebFetch", `"https://example.com"`, "https://example.com"},
		{"an array joins its items", "Bash", `["npm","test"]`, "npm test"},
		{"a null member joins as null", "Read", `{"file_path":"/tmp/a","limit":null}`, "/tmp/a null"},
		{"a bash command that is not a string joins the rest", "Bash", `{"command":7,"description":"odd"}`, `7 odd`},
		{"a number alone", "Bash", `42`, "42"},
		{"an empty input is empty", "Bash", ``, ""},
		{"text that is not JSON is itself", "Bash", `npm test`, "npm test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := JoinedInput(tc.tool, tc.input); got != tc.want {
				t.Errorf("JoinedInput = %q, want %q", got, tc.want)
			}
		})
	}
}
