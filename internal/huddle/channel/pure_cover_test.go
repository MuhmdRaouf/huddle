package channel

// The channel's small pure rules, each by what it answers: topic globs, who a send addresses,
// where a shell word may point, how a reservation path and a touched file are cleaned, how an
// answer reads as text, and how a task and an untyped argument render.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestGlobMatchTakesAnyOfItsGlobsAndNoGlobsMeansEverything(t *testing.T) {
	if !GlobMatch("task.created", nil) {
		t.Error("no globs must match every topic")
	}
	if !GlobMatch("task.created", []string{"note.*", "task.*"}) {
		t.Error("a topic matching the second glob must match")
	}
	if GlobMatch("task.created", []string{"note.*", "ask.*"}) {
		t.Error("a topic matching none of the globs must not match")
	}
	if GlobMatch("task.created", []string{"task.created.extra"}) {
		t.Error("a glob longer than the topic must not match")
	}
	if GlobMatch("a.b.c", []string{"a.*.d"}) {
		t.Error("a middle segment that never appears must not match")
	}
}

func TestIsBroadcastReadsWhoASendAddresses(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want bool
	}{
		{"no to", `{"msg":"hi"}`, true},
		{"an empty list", `{"to":[]}`, true},
		{"all", `{"to":"all"}`, true},
		{"all in a list", `{"to":["all"]}`, true},
		{"one name", `{"to":"b"}`, false},
		{"two names", `{"to":["a","b"]}`, false},
		{"all among names is refused, so no broadcast", `{"to":["all","b"]}`, false},
		{"an empty name is refused, so no broadcast", `{"to":[""]}`, false},
	} {
		if got := IsBroadcast(vv(t, c.body)); got != c.want {
			t.Errorf("%s: IsBroadcast(%s) = %v, want %v", c.name, c.body, got, c.want)
		}
	}
	if !IsBroadcast(nil) {
		t.Error("a call with no body addresses everyone")
	}
}

func TestLeavesProjectCountsAnAssignmentValueAsAPathOnlyForAVariableName(t *testing.T) {
	const project = "/work/app"
	for _, c := range []struct {
		segment string
		want    bool
	}{
		{"OUT=/etc/passwd cat", true},
		{"OUT_DIR=build cat", false},
		{"OUT=../elsewhere make", true},
		{"1OUT=/etc/passwd cat", false},
		{"=/etc/passwd cat", false},
		{"A-B=/etc/passwd cat", false},
		{"--out=/etc/passwd cat", true},
		{"cat $HOME/notes", true},
		{"cat ~/notes", true},
		{"cat src/main.go", false},
		{"cd ..", true},
	} {
		if got := leavesProject(c.segment, project); got != c.want {
			t.Errorf("leavesProject(%q) = %v, want %v", c.segment, got, c.want)
		}
	}
}

func TestGitTagListingStopsAtAFlagsWordBoundary(t *testing.T) {
	for _, c := range []struct {
		after string
		want  bool
	}{
		{"", true},
		{"; echo", true},
		{" -l", true},
		{" -l 'v1.*'", true},
		{" --list;", true},
		{" --contains=abc", true},
		{" -n5", true},
		{" -lx", false},
		{" --listing", false},
		{" v1.2.0", false},
		{" -a v1 -m x", false},
	} {
		if got := isTagListing(c.after); got != c.want {
			t.Errorf("isTagListing(%q) = %v, want %v", c.after, got, c.want)
		}
	}
}

func TestEffectiveLeavesTheDefaultsAloneForAnythingButAnObject(t *testing.T) {
	defaults := Defaults()
	for _, stored := range []*jsfmt.Value{nil, vv(t, `[]`), vv(t, `"on"`)} {
		got := Effective(stored)
		if len(got) != len(defaults) {
			t.Fatalf("Effective(%v) = %v, want the %d defaults", stored, got, len(defaults))
		}
		for i := range defaults {
			if got[i] != defaults[i] {
				t.Errorf("Effective(%v)[%d] = %+v, want %+v", stored, i, got[i], defaults[i])
			}
		}
	}
}

func TestCleanReservePathAcceptsOnlyAnAbsolutePathAndNormalisesIt(t *testing.T) {
	for in, want := range map[string]string{
		"/w/app/src/../main.go": "/w/app/main.go",
		"  /w/app//a.go  ":      "/w/app/a.go",
		`\w\app\a.go`:           "/w/app/a.go",
	} {
		got, err := cleanReservePath(in)
		if err != nil || got != want {
			t.Errorf("cleanReservePath(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", ".", "src/a.go", "/w/\x00x", "/" + strings.Repeat("a", 500)} {
		if _, err := cleanReservePath(in); errStatus(err) != 400 {
			t.Errorf("cleanReservePath(%q) = %v, want a 400", in, err)
		}
	}
}

func TestRelOfAnswersThePathInsideTheRoot(t *testing.T) {
	for _, c := range []struct{ p, root, want string }{
		{"/w/app/src/a.go", "/w/app", "src/a.go"},
		{"/w/app", "/w/app", ""},
		{"/w/application/a.go", "/w/app", ""},
		{"/elsewhere/a.go", "/w/app", ""},
		{"/w/app/a.go", "", ""},
	} {
		if got := relOf(c.p, c.root); got != c.want {
			t.Errorf("relOf(%q, %q) = %q, want %q", c.p, c.root, got, c.want)
		}
	}
}

func TestReservationAnswersReadAsTheLinesAnAgentSees(t *testing.T) {
	holder, until := "b", "2026-10-16T10:00:00Z"
	got := reserveText(ReserveResult{Paths: []ReservePath{
		{Path: "/w/a.go", Held: true},
		{Path: "/w/b.go", Holder: &holder, Until: &until},
	}}, opCall{})
	if want := "/w/a.go held\n/w/b.go taken by b until 2026-10-16T10:00:00Z"; got != want {
		t.Errorf("reserveText = %q, want %q", got, want)
	}
	if got := reserveText("not a result", opCall{}); got != "" {
		t.Errorf("reserveText of a foreign answer = %q, want empty", got)
	}

	if got := releaseText(ReleaseResult{Released: []string{"/w/a.go", "/w/b.go"}}, opCall{}); got != "released /w/a.go, /w/b.go" {
		t.Errorf("releaseText = %q", got)
	}
	if got := releaseText(ReleaseResult{}, opCall{}); got != "nothing released" {
		t.Errorf("releaseText of none = %q", got)
	}
	if got := releaseText(42, opCall{}); got != "nothing released" {
		t.Errorf("releaseText of a foreign answer = %q", got)
	}

	note := "refactor"
	list := ReservationsView{Reservations: []ReservationView{
		{Path: "/w/a.go", Holder: "a", Expires: "T1", Note: &note},
		{Path: "/w/b.go", Holder: "b", Expires: "T2"},
	}}
	if got, want := reservationsText(list, opCall{}), "/w/a.go — a until T1 (refactor)\n/w/b.go — b until T2"; got != want {
		t.Errorf("reservationsText = %q, want %q", got, want)
	}
	if got := reservationsText(ReservationsView{}, opCall{}); got != "(no reservations)" {
		t.Errorf("reservationsText of none = %q", got)
	}
	if got := reservedText(ReservedResult{Path: "/w/a.go"}, opCall{}); got != "" {
		t.Errorf("reservedText of a free path = %q, want nothing", got)
	}
}

func TestCleanTouchKeepsAFileRelativeToItsRepo(t *testing.T) {
	path, repo, err := cleanTouch(`.\src\a.go`, "/w/app")
	if err != nil || path != "src/a.go" || repo != "/w/app" {
		t.Errorf("cleanTouch = (%q, %q, %v), want src/a.go in /w/app", path, repo, err)
	}
	for _, c := range []struct{ path, repo any }{
		{"/abs/a.go", "/w/app"},
		{"../a.go", "/w/app"},
		{"src/../../a.go", "/w/app"},
		{"a\nb.go", "/w/app"},
		{"", "/w/app"},
		{nil, "/w/app"},
		{42, "/w/app"},
		{"a.go", ""},
		{"a.go", nil},
		{"a.go", "/w/\nx"},
		{strings.Repeat("a", 501), "/w/app"},
		{"a.go", strings.Repeat("r", 501)},
	} {
		if _, _, err := cleanTouch(c.path, c.repo); errStatus(err) != 400 {
			t.Errorf("cleanTouch(%v, %v) = %v, want a 400", c.path, c.repo, err)
		}
	}
}

func TestRepoNameIsTheLastPartOfTheKey(t *testing.T) {
	for in, want := range map[string]string{"/w/app": "app", "/w/app/": "app", "app": "app", "/": "/", "": ""} {
		if got := RepoName(in); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRowReadersTakeEveryNumberTheStoreCanReturn(t *testing.T) {
	row := Row{"i64": int64(7), "f64": float64(8), "int": 9, "text": "10", "nil": nil}
	for key, want := range map[string]int64{"i64": 7, "f64": 8, "int": 9, "text": 0, "nil": 0, "absent": 0} {
		if got := rInt(row, key); got != want {
			t.Errorf("rInt(%s) = %d, want %d", key, got, want)
		}
	}
	if got := rIntPtr(row, "nil"); got != nil {
		t.Errorf("rIntPtr of a null = %v, want nil", *got)
	}
	if got := rIntPtr(row, "i64"); got == nil || *got != 7 {
		t.Errorf("rIntPtr(i64) = %v, want 7", got)
	}
}

func TestUtf16CutPtrCutsOnUnitsAndKeepsANilNil(t *testing.T) {
	if utf16CutPtr(nil, 3) != nil {
		t.Error("a nil string must stay nil")
	}
	long := "abcdef"
	if got := utf16CutPtr(&long, 3); got == nil || *got != "abc" {
		t.Errorf("utf16CutPtr = %v, want abc", got)
	}
	if long != "abcdef" {
		t.Errorf("the original was cut to %q", long)
	}
}

func TestJsAnyToStringRendersLikeStringOfValueOrEmpty(t *testing.T) {
	var nilValue *jsfmt.Value
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"text", "text"},
		{true, "true"},
		{false, "false"},
		{nilValue, ""},
		{vv(t, `null`), ""},
		{vv(t, `"quoted"`), "quoted"},
		{vv(t, `{"a":1}`), `{"a":1}`},
		{vv(t, `12`), "12"},
		{42, "42"},
		{1.5, "1.5"},
	} {
		if got := jsAnyToString(c.in); got != c.want {
			t.Errorf("jsAnyToString(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFieldNumReadsOnlyAWholeNumberField(t *testing.T) {
	data := vv(t, `{"n":12,"neg":-3,"frac":1.5,"text":"12","none":null}`)
	for key, want := range map[string]int64{"n": 12, "neg": -3, "frac": 0, "text": 0, "none": 0, "absent": 0} {
		if got := fieldNum(data, key); got != want {
			t.Errorf("fieldNum(%s) = %d, want %d", key, got, want)
		}
	}
	if fieldNum(nil, "n") != 0 {
		t.Error("fieldNum of no data must be 0")
	}
}

func TestCmpFOrdersAscending(t *testing.T) {
	if cmpF(1, 2) != -1 || cmpF(2, 1) != 1 || cmpF(2, 2) != 0 {
		t.Error("cmpF must answer -1, 1 and 0 for less, greater and equal")
	}
}

func TestATaskViewEncodesItsBodyThenTheReviewAnswers(t *testing.T) {
	task := &TaskView{
		Fields:   vv(t, `{"id":"t-1","title":"Ship it"}`),
		NeededBy: []string{"t-2"},
		Ready:    true,
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("the encoding %s is not JSON: %v", encoded, err)
	}
	if back["id"] != "t-1" || back["title"] != "Ship it" || back["ready"] != true {
		t.Errorf("the encoding = %s, want the body and the answers together", encoded)
	}
	if !strings.HasPrefix(string(encoded), `{"id":"t-1","title":"Ship it","comments"`) {
		t.Errorf("the encoding = %s, want the body's own order first", encoded)
	}

	bare, err := json.Marshal(&TaskView{})
	if err != nil {
		t.Fatalf("marshal a bare task: %v", err)
	}
	if !strings.HasPrefix(string(bare), `{"comments"`) {
		t.Errorf("a task with no body = %s, want the answers alone", bare)
	}
}
