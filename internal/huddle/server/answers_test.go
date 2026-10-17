package server

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestShortLineShapes walks the one-line answers' own edges: the shapes an op never returns over
// HTTP (a zero seq, a missing task) and the rich finishes and waits.
func TestShortLineShapes(t *testing.T) {
	if got := shortLine("send", channel.Ev{}); got != "" {
		t.Fatalf("a send without a seq = %q", got)
	}
	if got := shortLine("remember", channel.KBShape{}); got != "" {
		t.Fatalf("a remember without an id = %q", got)
	}
	if got := shortLine("task_create", channel.CreateTaskResult{}); got != "" {
		t.Fatalf("a task_create without a task = %q", got)
	}
	if got := shortLine("finish", channel.FinishResult{}); got != "" {
		t.Fatalf("a finish without a task = %q", got)
	}
	if got := shortLine("wait", struct{}{}); got != "" {
		t.Fatalf("a wait that is not a hit = %q", got)
	}
	if got := shortLine("wait", channel.WaitHit{Kind: ""}); got != "" {
		t.Fatalf("a wait without a kind = %q", got)
	}
	if got := shortLine("wait", channel.WaitHit{Kind: "timeout"}); got != "timeout: call wait again" {
		t.Fatalf("a wait timeout = %q", got)
	}
	ask, old := "answer me", "old"
	hit := channel.WaitHit{Kind: "msg", Seq: 7, From: "b", Topic: "ask", Msg: &ask, NeedsReply: true, More: 2,
		Skipped: []channel.SkippedEvent{{Seq: 3, From: "a", Topic: "note", Msg: &old}}, SkippedTotal: 4}
	want := "msg: #7 b → all (ask): answer me [answer: reply seq=7] (+2 more waiting)" +
		" — ack 7 once handled\n  skipped #3 a → all (note): old\n  (3 older skipped: events)"
	if got := shortLine("wait", hit); got != want {
		t.Fatalf("a wait hit = %q, want %q", got, want)
	}
	to, task, know := "b", "tail", int64(9)
	finish := channel.FinishResult{Done: "first", Released: []channel.ReleasedItem{{To: &to}, {Task: &task}},
		Knowledge: &know, Next: &channel.FinishNext{ID: "tail", Title: "Tail", Unmet: []string{"first"}}}
	want = "done first; released: →b, tail→?; result #9; next: tail Tail (waits on first)"
	if got := shortLine("finish", finish); got != want {
		t.Fatalf("a rich finish = %q, want %q", got, want)
	}
	ready := channel.FinishResult{Done: "ship", Next: &channel.FinishNext{ID: "n", Title: "N", Ready: true}}
	if got := shortLine("finish", ready); got != "done ship; released: none; next: n N" {
		t.Fatalf("a ready finish = %q", got)
	}
	if got := shortLine("nope", nil); got != "" {
		t.Fatalf("an unknown op = %q", got)
	}
}

// TestStringifyIndent1 nests JSON.stringify(v, null, 1) one space deep at every level.
func TestStringifyIndent1(t *testing.T) {
	v := vv(t, `{"a": [{"b": 1}, 2], "c": null, "d": true, "e": "x y", "f": []}`)
	want := "{\n" +
		" \"a\": [\n" +
		"  {\n" +
		"   \"b\": 1\n" +
		"  },\n" +
		"  2\n" +
		" ],\n" +
		" \"c\": null,\n" +
		" \"d\": true,\n" +
		" \"e\": \"x y\",\n" +
		" \"f\": []\n" +
		"}"
	if got := stringifyIndent1(v); got != want {
		t.Fatalf("stringifyIndent1 = %q, want %q", got, want)
	}
	if got := stringifyIndent1(vv(t, `"plain"`)); got != "\"plain\"" {
		t.Fatalf("a string stays itself: %q", got)
	}
}

// TestValueReaders walks the JavaScript-shaped readers' every kind.
func TestValueReaders(t *testing.T) {
	cases := []struct {
		src        string
		truthyWant bool
		textWant   string
	}{
		{"", false, ""}, {"null", false, ""}, {"false", false, "false"}, {"true", true, "true"},
		{"0", false, "0"}, {"1", true, "1"}, {"2.5", true, "2.5"}, {"\"\"", false, ""},
		{"\"x\"", true, "x"}, {"{}", true, ""}, {"[]", true, ""},
	}
	for _, c := range cases {
		var v *jsfmt.Value
		if c.src != "" {
			v = vv(t, c.src)
		}
		if got := truthy(v); got != c.truthyWant {
			t.Fatalf("truthy(%s) = %v, want %v", c.src, got, c.truthyWant)
		}
		if got := textOf(v); got != c.textWant {
			t.Fatalf("textOf(%s) = %q, want %q", c.src, got, c.textWant)
		}
	}
	if got := fieldOf(nil, "a"); got != nil {
		t.Fatalf("fieldOf(nil) = %v", got)
	}
	if got := fieldOf(vv(t, `"s"`), "a"); got != nil {
		t.Fatalf("fieldOf of a string = %v", got)
	}
}

// TestURLWithoutCode strips the redeem code from a query string, keeping the rest.
func TestURLWithoutCode(t *testing.T) {
	for _, probe := range []struct{ in, want string }{
		{"", ""},
		{"code=x", ""},
		{"a=1&code=x&b=2", "a=1&b=2"},
		{"a=1&&b=2", "a=1&b=2"},
	} {
		if got := urlWithoutCode(probe.in); got != probe.want {
			t.Fatalf("urlWithoutCode(%q) = %q, want %q", probe.in, got, probe.want)
		}
	}
}

// TestSkippedQuote walks the placeholder lines drift steps over.
func TestSkippedQuote(t *testing.T) {
	for _, line := range []string{"…", "...", "# …", "//  …", "// …"} {
		if !skippedQuote(line) {
			t.Fatalf("skippedQuote(%q) = false", line)
		}
	}
	for _, line := range []string{"real code", "# comment", "// … more", ""} {
		if skippedQuote(line) {
			t.Fatalf("skippedQuote(%q) = true", line)
		}
	}
}

// TestMustBool answers the two JSON booleans.
func TestMustBool(t *testing.T) {
	if got := mustBool(true).EncodeJSON(); got != "true" {
		t.Fatalf("mustBool(true) = %s", got)
	}
	if got := mustBool(false).EncodeJSON(); got != "false" {
		t.Fatalf("mustBool(false) = %s", got)
	}
}

// TestURLEscape keeps the cookie value percent-encoded except the unreserved set.
func TestURLEscape(t *testing.T) {
	if got := urlEscape(`aZ09-_.~ "x"`); got != "aZ09-_.~%20%22x%22" {
		t.Fatalf("urlEscape = %q", got)
	}
}

// TestIsTerminal says only a character device is one.
func TestIsTerminal(t *testing.T) {
	if isTerminal(io.Discard) {
		t.Fatal("a buffer is not a terminal")
	}
	if isTerminal(&brokenFile{}) {
		t.Fatal("a file that cannot stat is not a terminal")
	}
}

type brokenFile struct{ os.File }

func (b *brokenFile) Stat() (os.FileInfo, error) { return nil, os.ErrClosed }

func (b *brokenFile) Write([]byte) (int, error) { return 0, nil }

// TestMcpHandleEdges covers the messages the endpoint drops: not an object at all.
func TestMcpHandleEdges(t *testing.T) {
	m := NewMcp("0.0.0")
	if got := m.Handle(bg, nil, "c", "s", nil); got != nil {
		t.Fatalf("a missing message = %v", got)
	}
	if got := m.Handle(bg, nil, "c", "s", vv(t, `"str"`)); got != nil {
		t.Fatalf("a message that is not an object = %v", got)
	}
	if got := m.Handle(bg, nil, "c", "s", vv(t, `{"jsonrpc":"2.0","method":"x"}`)); got != nil {
		t.Fatalf("a notification without an id = %v", got)
	}
}

// TestRepoToolsSafe walks the path guard: escapes, secrets by name and by resolution, and the
// scratch tree beside the repo.
func TestRepoToolsSafe(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	repo := filepath.Join(base, "repo")
	for _, p := range []string{"a.go", "secrets/k", "deep/secrets/k"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "real-secret"), filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "real-secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratchTarget := filepath.Join(outside, "scratch")
	if err := os.MkdirAll(scratchTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratchTarget, "note.md"), []byte("quote"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(scratchTarget, filepath.Join(repo, ".agents", "scratch")); err != nil {
		t.Fatal(err)
	}
	r := NewRepoTools(repo)
	if r == nil {
		t.Fatal("NewRepoTools = nil for a live repo")
	}
	for _, bad := range []string{"../out", "a/../../out", "/abs", "secrets/k", "deep/secrets/k", "nope", "link"} {
		if got := r.Safe(bad); got != "" {
			t.Fatalf("safe(%q) = %q, want refused", bad, got)
		}
	}
	if got := r.Safe("a.go"); got != filepath.Join(repo, "a.go") {
		t.Fatalf("safe(a.go) = %q", got)
	}
	if got := r.Safe(""); got != repo {
		t.Fatalf("safe(\"\") = %q", got)
	}
	t.Run("refuses what can never name a repo file", func(t *testing.T) {
		cases := []struct {
			rel     string
			refused bool
		}{
			{"a.go", false},
			{"sub/deep/x.md", false},
			{"../out", true},
			{"a/../../out", true},
			{"/abs", true},
			{`\abs`, false}, // a backslash is a plain filename character
			{"C:\\x", false},
		}
		for _, c := range cases {
			if got := relRefusedFor(c.rel); got != c.refused {
				t.Errorf("relRefusedFor(%q) = %t, want %t", c.rel, got, c.refused)
			}
		}
	})
	realScratch, err := filepath.EvalSymlinks(scratchTarget)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Safe("note.md"); got != filepath.Join(realScratch, "note.md") {
		t.Fatalf("safe(note.md) = %q, want the scratch tree %q", got, realScratch)
	}
	if NewRepoTools(filepath.Join(base, "missing")) != nil {
		t.Fatal("a missing repo answers nil")
	}
}

// TestRepoToolsFile answers the whole-file guard: directories, the cap and a missing name.
func TestRepoToolsFile(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(repo, "big.bin")
	if err := os.WriteFile(big, make([]byte, 2_000_001), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRepoTools(repo)
	answer, err := r.File(bg, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := answer.(NotFoundAnswer); !ok {
		t.Fatalf("a directory = %#v", answer)
	}
	answer, err = r.File(bg, "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	f, ok := answer.(FileAnswer)
	if !ok {
		t.Fatalf("the capped file = %#v", answer)
	}
	if f.Size != 2_000_001 || f.Text != "(file larger than 2 MB)" {
		t.Fatalf("the capped file = %#v", f)
	}
	if err := os.WriteFile(filepath.Join(repo, "bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	answer, err = r.File(bg, "bin")
	if err != nil {
		t.Fatal(err)
	}
	bin, ok := answer.(FileBinary)
	if !ok || !bin.Binary {
		t.Fatalf("a NUL file = %#v", answer)
	}
	late := make([]byte, 2100)
	for i := range late {
		late[i] = 'a'
	}
	late[10] = 0
	if err := os.WriteFile(filepath.Join(repo, "late.bin"), late, 0o600); err != nil {
		t.Fatal(err)
	}
	answer, err = r.File(bg, "late.bin")
	if err != nil {
		t.Fatal(err)
	}
	lateBin, ok := answer.(FileBinary)
	if !ok || !lateBin.Binary || lateBin.Size != 2100 {
		t.Fatalf("a NUL inside a head longer than the cut = %#v", answer)
	}
	answer, err = r.File(bg, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := answer.(NotFoundAnswer); !ok {
		t.Fatalf("a missing file = %#v", answer)
	}
}

// TestViewsRouteEdges walks the route's own misses: an unreachable repo, an anchored excerpt and
// the references view over files and snippets.
func TestViewsRouteEdges(t *testing.T) {
	ch := openChannel(t, "edge")
	if _, err := ch.Join(bg, "dev", channel.JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "not-there")
	if _, err := ch.Configure(bg, channel.ConfigureArgs{Repo: &missing}); err != nil {
		t.Fatal(err)
	}
	views := NewViews()
	answer, err := views.Route(bg, ch, "tree", queryOf(""))
	if err != nil {
		t.Fatal(err)
	}
	if n := answer.(NoRepoAnswer); !strings.Contains(n.Error, "not reachable") {
		t.Fatalf("an unreachable repo = %#v", n)
	}

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("line0\nline1\nthe anchor\nline3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "b.go"), []byte("package b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Configure(bg, channel.ConfigureArgs{Repo: &repo}); err != nil {
		t.Fatal(err)
	}
	answer, err = views.Route(bg, ch, "code", queryOf("path=a.go&anchor=the anchor"))
	if err != nil {
		t.Fatal(err)
	}
	if e := answer.(CodeExcerptAnswer); e.From != 1 || !strings.Contains(e.Excerpt, "the anchor") {
		t.Fatalf("the anchored excerpt = %#v", e)
	}
	answer, err = views.Route(bg, ch, "code", queryOf("path=secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if !answer.(CodeMissingAnswer).Missing {
		t.Fatalf("a refused path = %#v", answer)
	}
	answer, err = views.Route(bg, ch, "refs", queryOf(""))
	if err != nil {
		t.Fatal(err)
	}
	if rows := answer.([]RefRow); len(rows) != 0 {
		t.Fatalf("no references yet = %#v", rows)
	}
	if _, err := ch.CreateTask(bg, "dev", channel.CreateTaskInput{ID: "t1", Title: "Touch", Body: vv(t,
		`{"files": ["a.go:12", "sub/b.go"], "snippets": [{"title": "s", "path": "sub/b.go:3", "code": "x"}]}`)}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"a.go", "sub", "sub/b.go"} {
		answer, err = views.Route(bg, ch, "refs", queryOf("path="+q))
		if err != nil {
			t.Fatal(err)
		}
		if rows := answer.([]RefRow); len(rows) != 1 || rows[0].ID != "t1" {
			t.Fatalf("refs(%s) = %#v", q, rows)
		}
	}
	answer, err = views.Route(bg, ch, "nope", queryOf(""))
	if err != nil {
		t.Fatal(err)
	}
	if n := answer.(NoRepoAnswer); !strings.Contains(n.Error, "no repo view") {
		t.Fatalf("an unknown view = %#v", n)
	}
}
