package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestRepoViews(t *testing.T) {
	t.Run("repo views appear only for a channel with a reachable repo", func(t *testing.T) {
		ch := openChannel(t, "v")
		views := NewViews()
		if got := views.Names(ch); len(got) != 0 {
			t.Fatalf("viewNames = %v, want none without a repo", got)
		}
		if r, err := views.Route(bg, ch, "code", queryOf("path=a")); err != nil {
			t.Fatal(err)
		} else if !strings.Contains(mustJSON(t, r), "this channel has no repo configured") {
			t.Fatalf("code view = %s, want the no-repo answer", mustJSON(t, r))
		}
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("line one\nline two\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Configure(bg, channel.ConfigureArgs{Repo: &repo}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"code", "drift", "diagrams"} {
			found := false
			for _, n := range views.Names(ch) {
				if n == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("viewNames = %v, want %s among them", views.Names(ch), want)
			}
		}
		code, err := views.Route(bg, ch, "code", queryOf("path=a.txt"))
		if err != nil {
			t.Fatal(err)
		}
		raw := mustJSON(t, code)
		if !strings.Contains(raw, `"path":"a.txt"`) || !strings.Contains(raw, `"total":3`) {
			t.Fatalf("code view = %s, want path a.txt and total 3", raw)
		}
		if r, err := views.Route(bg, ch, "nope", queryOf("")); err != nil {
			t.Fatal(err)
		} else if !strings.Contains(mustJSON(t, r), "no repo view nope") {
			t.Fatalf("unknown view = %s", mustJSON(t, r))
		}
	})

	t.Run("safe() stays inside the repo and never serves secrets", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		if err := os.MkdirAll(filepath.Join(repo, "secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "secrets", "k"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(base, "repo-other")
		if err := os.MkdirAll(other, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, filepath.Join(repo, "link")); err != nil {
			t.Fatal(err)
		}
		r := NewRepoTools(repo)
		if r == nil {
			t.Fatal("repoTools answered nil for a repo that exists")
		}
		if got := r.Safe("secrets/k"); got != "" {
			t.Fatalf("safe(secrets/k) = %q, want none", got)
		}
		if got := r.Safe("../repo-other/f"); got != "" {
			t.Fatalf("safe(../repo-other/f) = %q, want none", got)
		}
		if got := r.Safe("link/f"); got != "" { // a sibling with the repo's name as prefix
			t.Fatalf("safe(link/f) = %q, want none", got)
		}
		if err := os.WriteFile(filepath.Join(repo, "ok"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := r.Safe("ok"); got != filepath.Join(repo, "ok") {
			t.Fatalf("safe(ok) = %q, want %q", got, filepath.Join(repo, "ok"))
		}
	})

	t.Run("exportMd lists tasks outside any phase", func(t *testing.T) {
		ch := openChannel(t, "e")
		body := vv(t, `{"what": "do it"}`)
		if _, err := ch.CreateTask(bg, "owner", channel.CreateTaskInput{ID: "t1", Title: "Loose task", Body: body}); err != nil {
			t.Fatal(err)
		}
		md, err := NewViews().ExportMd(bg, ch)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(md, "## Tasks") || !strings.Contains(md, "### t1 Loose task") {
			t.Fatalf("exportMd =\n%s\nwant the loose-tasks section", md)
		}
	})
}

// TestRepoReads walks the read-only repo tools against a small tree: the directory level with
// its README, the capped file read, the drift check's verdicts, and the route's own views.
func TestRepoReads(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# the repo\n")
	write("src/README.md", "# the sources\n")
	write("src/a.go", "package a\n\nfunc A() {}\n")
	write("src/b.go", "package b\n\nfunc B() {}\n")
	write("docs/architecture/system.png", "png")
	tools := NewRepoTools(repo)
	if tools == nil {
		t.Fatal("no tools for a repo that exists")
	}

	t.Run("tree: one level, directories first, the README folded in", func(t *testing.T) {
		answer, err := tools.Tree(bg, "src")
		if err != nil {
			t.Fatal(err)
		}
		tree := answer.(TreeAnswer)
		if tree.Path != "src" || len(tree.Entries) != 3 || tree.Entries[2].Name != "README.md" || !tree.ReadyPresent() {
			t.Fatalf("tree = %+v", tree)
		}
		if file, err := tools.Tree(bg, "src/a.go"); err != nil || !file.(TreeFile).File {
			t.Fatalf("a file's tree = %+v %v", file, err)
		}
		if miss, err := tools.Tree(bg, "nope"); err != nil || miss.(NotFoundAnswer).Error != "not found" {
			t.Fatalf("a missing path's tree = %+v %v", miss, err)
		}
	})

	t.Run("file: the whole text, the binary marker, the cap", func(t *testing.T) {
		answer, err := tools.File(bg, "src/a.go")
		if err != nil {
			t.Fatal(err)
		}
		file := answer.(FileAnswer)
		if file.Path != "src/a.go" || file.Text != "package a\n\nfunc A() {}\n" || file.Size == 0 {
			t.Fatalf("file = %+v", file)
		}
		write("bin.dat", "\x00\x01\x02")
		if answer, err = tools.File(bg, "bin.dat"); err != nil {
			t.Fatal(err)
		}
		if !answer.(FileBinary).Binary {
			t.Fatalf("the binary read = %+v", answer)
		}
		if answer, err = tools.File(bg, "src"); err != nil || answer.(NotFoundAnswer).Error != "not a file" {
			t.Fatalf("a directory's file = %+v %v", answer, err)
		}
	})

	t.Run("drift: a quote that still matches, one that partly does, a missing file", func(t *testing.T) {
		tasks := []*channel.TaskView{taskWithSnippets(t, "m1", vv(t, `[{"title": "same", "path": "src/a.go", "code": "package a\n\nfunc A() {}"},
			{"title": "gone", "path": "src/old.go", "code": "package old"},
			{"title": "part", "path": "src/b.go:2", "code": "package b\nfunc X() {}\nfunc Y() {}\nfunc B() {}"}]`))}
		rows, err := tools.Drift(bg, tasks)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("rows = %+v", rows)
		}
		byTitle := map[string]DriftRow{}
		for _, r := range rows {
			byTitle[r.Path] = r
		}
		if byTitle["src/a.go"].State != "ok" || byTitle["src/a.go"].Score == nil || *byTitle["src/a.go"].Score != 100 {
			t.Fatalf("the matching quote = %+v", byTitle["src/a.go"])
		}
		if byTitle["src/old.go"].State != "missing" || byTitle["src/old.go"].Score != nil {
			t.Fatalf("the missing quote = %+v", byTitle["src/old.go"])
		}
		if byTitle["src/b.go"].State != "partial" {
			t.Fatalf("the partial quote = %+v", byTitle["src/b.go"])
		}
	})

	t.Run("the route's own views: refs, diagrams, the code directory and its anchor", func(t *testing.T) {
		ch := openChannel(t, "r")
		if _, err := ch.Join(bg, "a", channel.JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Configure(bg, channel.ConfigureArgs{Repo: &repo}); err != nil {
			t.Fatal(err)
		}
		views := NewViews()
		if answer, err := views.Route(bg, ch, "refs", queryOf("path=src")); err != nil {
			t.Fatal(err)
		} else if len(answer.([]RefRow)) != 0 {
			t.Fatalf("refs = %+v, want none yet", answer)
		}
		if _, err := ch.CreateTask(bg, "a", channel.CreateTaskInput{
			ID: "t1", Title: "touch a", Body: vv(t, `{"files": ["src/a.go"]}`),
		}); err != nil {
			t.Fatal(err)
		}
		answer, err := views.Route(bg, ch, "refs", queryOf("path=src/a.go"))
		if err != nil {
			t.Fatal(err)
		}
		refs := answer.([]RefRow)
		if len(refs) != 1 || refs[0].ID != "t1" || refs[0].Title != "touch a" {
			t.Fatalf("refs = %+v", refs)
		}
		diagrams, err := views.Route(bg, ch, "diagrams", queryOf(""))
		if err != nil {
			t.Fatal(err)
		}
		if got := diagrams.([]string); len(got) != 1 || got[0] != "system.png" {
			t.Fatalf("diagrams = %v", got)
		}
		dir, err := views.Route(bg, ch, "code", queryOf("path=src"))
		if err != nil {
			t.Fatal(err)
		}
		entries := dir.(CodeDirAnswer).Entries
		if len(entries) != 3 || entries[0] != "README.md" { // the code view's plain sort, not the collation

			t.Fatalf("the code directory = %+v", dir)
		}
		if views.DiagramFile(ch, "system.png") == "" || views.DiagramFile(ch, "../x.png") != "" {
			t.Fatal("diagramFile admits what it should not")
		}
		anchored, err := views.Route(bg, ch, "code", queryOf("path=src/a.go&anchor=func A"))
		if err != nil {
			t.Fatal(err)
		}
		if excerpt := anchored.(CodeExcerptAnswer); excerpt.From != 1 || excerpt.Total != 4 {
			t.Fatalf("the anchored excerpt = %+v", excerpt)
		}
	})
}

// taskWithSnippets builds one task view whose flattened body carries the snippets.
func taskWithSnippets(t *testing.T, id string, snippets *jsfmt.Value) *channel.TaskView {
	t.Helper()
	return &channel.TaskView{ID: id, Title: id, Fields: jsfmt.Object(
		[]string{"id", "title", "snippets"}, []*jsfmt.Value{jsfmt.String(id), jsfmt.String(id), snippets})}
}

// ReadyPresent says whether the tree's README was read.
func (a TreeAnswer) ReadyPresent() bool {
	return a.Readme != nil && strings.Contains(*a.Readme, "the sources")
}

// TestExportMdPlan walks the Markdown export over a plan with phases, rich task bodies and an
// open review note, beside the repo views the route serves.
func TestExportMdPlan(t *testing.T) {
	ch := openChannel(t, "p")
	if _, err := ch.Join(bg, "dev", channel.JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	plan := channel.Plan{Phases: []channel.PlanPhase{{
		N:     &one,
		Title: "First",
		Body: vv(t, `{"summary": "the start", "needs": "a repo",
			"make": ["lint", "test"], "value": "a green build"}`),
		Steps: []channel.PlanStep{{
			ID: "s1", Title: "Rich step",
			Body: vv(t, `{"kind": "build", "gate": "ask", "risk": "high",
				"what": "do it", "why": "because", "use": "a CLI", "value": "speed",
				"rollback": "revert", "how": ["one", "two"],
				"alternatives": [{"option": "other", "why_not": "slower"}],
				"verify": [{"cmd": "go test", "expect": "ok"}],
				"snippets": [{"title": "the shape", "path": "a.go", "lang": "go", "code": "package a", "proposed": true}]}`),
		}},
	}}}
	two := int64(2)
	plan.Phases = append(plan.Phases, channel.PlanPhase{
		N:     &two,
		Title: "Bare",
		Steps: []channel.PlanStep{{ID: "s9", Title: "Nothing said"}},
	})
	if _, err := ch.ImportPlan(bg, "dev", plan, channel.ImportOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.CreateTask(bg, "dev", channel.CreateTaskInput{ID: "s2", Title: "Before"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Note(bg, "dev", "s1", "question", "sure?"); err != nil {
		t.Fatal(err)
	}
	md, err := NewViews().ExportMd(bg, ch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Step 1: First", "the start", "- **Needs:** a repo", "`make lint`, `make test`",
		"## Step 2: Bare", "- **Needs:** -", "- **Make:** -", "- **Value:** -",
		"### s1 Rich step", "`build` · gate `ask` · risk `high` · status **todo**",
		"**What.** do it", "**Why.** because", "- *other*: slower", "1. one", "2. two",
		"*the shape* — `a.go`", "```go", "**Use.** a CLI", "- `go test` → ok",
		"**Value.** speed", "**Rollback.** revert", "**Owner review:**", "- [question] sure?",
		"**Verify:**", "**How:**", "**Use.** a CLI", "(proposed)",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("exportMd misses %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, "## Tasks") || !strings.Contains(md, "### s2 Before") {
		t.Fatalf("exportMd misses the loose task s2:\n%s", md)
	}

	t.Run("the route serves the tree, the file and the drift twice (the second from the cache)", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Configure(bg, channel.ConfigureArgs{Repo: &repo}); err != nil {
			t.Fatal(err)
		}
		views := NewViews()
		answer, err := views.Route(bg, ch, "tree", queryOf(""))
		if err != nil {
			t.Fatal(err)
		}
		if tree := answer.(TreeAnswer); len(tree.Entries) != 1 || tree.Entries[0].Name != "a.go" {
			t.Fatalf("tree = %+v", tree)
		}
		if answer, err = views.Route(bg, ch, "file", queryOf("path=a.go")); err != nil {
			t.Fatal(err)
		}
		if file := answer.(FileAnswer); file.Text != "package a\n" {
			t.Fatalf("file = %+v", file)
		}
		first, err := views.Route(bg, ch, "drift", queryOf(""))
		if err != nil {
			t.Fatal(err)
		}
		at := first.(DriftAnswer).At
		if at == "" {
			t.Fatalf("drift = %+v", first)
		}
		second, err := views.Route(bg, ch, "drift", queryOf(""))
		if err != nil {
			t.Fatal(err)
		}
		if second.(DriftAnswer).At != at {
			t.Fatalf("the second drift = %+v, want the cached one", second)
		}
		if answer, err = views.Route(bg, ch, "tree", queryOf("path=a.go")); err != nil {
			t.Fatal(err)
		}
		if !answer.(TreeFile).File {
			t.Fatalf("a file's tree = %+v", answer)
		}
		if answer, err = views.Route(bg, ch, "code", queryOf("")); err != nil {
			t.Fatal(err)
		}
		if !answer.(CodeMissingAnswer).Missing {
			t.Fatalf("an empty code path = %+v", answer)
		}
	})
}
