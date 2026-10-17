package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTouchOfOnlyEditToolsThePathRelativeToTheRepoRootWorktreesOfOneRepoAreOneRepo(t *testing.T) {
	top := realDir(t, t.TempDir())
	for _, dir := range []string{
		filepath.Join(top, "main", ".git", "worktrees", "feat"),
		filepath.Join(top, "main", "src"),
		filepath.Join(top, "feat", "src"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(top, "feat", ".git"), "gitdir: "+filepath.Join(top, "main", ".git", "worktrees", "feat")+"\n")
	writeFile(t, filepath.Join(top, "main", ".git", "worktrees", "feat", "commondir"), "../..\n")
	touch := func(tool, path string) *Touch {
		input := TouchInput{ToolName: tool}
		input.ToolInput.FilePath = path
		return TouchOf(input)
	}
	if got := touch("Edit", filepath.Join(top, "main", "src", "cart.ts")); *got != (Touch{Repo: filepath.Join(top, "main"), Path: "src/cart.ts"}) {
		t.Errorf("the main checkout: %+v", got)
	}
	if got := touch("Write", filepath.Join(top, "feat", "src", "cart.ts")); *got != (Touch{Repo: filepath.Join(top, "main"), Path: "src/cart.ts"}) {
		t.Errorf("a worktree is its repo: %+v", got)
	}
	notebook := TouchInput{ToolName: "NotebookEdit"}
	notebook.ToolInput.NotebookPath = filepath.Join(top, "main", "n.ipynb")
	if got := TouchOf(notebook); *got != (Touch{Repo: filepath.Join(top, "main"), Path: "n.ipynb"}) {
		t.Errorf("a notebook: %+v", got)
	}
	if got := touch("Read", filepath.Join(top, "main", "src", "cart.ts")); got != nil {
		t.Errorf("a read tool: %+v", got)
	}
	if got := touch("Bash", ""); got != nil {
		t.Errorf("a shell tool: %+v", got)
	}
	// no git: within the session's cwd, else nothing
	plain := realDir(t, t.TempDir())
	inside := TouchInput{ToolName: "Edit", Cwd: plain}
	inside.ToolInput.FilePath = filepath.Join(plain, "notes.md")
	if got := TouchOf(inside); *got != (Touch{Repo: plain, Path: "notes.md"}) {
		t.Errorf("outside any repo: %+v", got)
	}
	outside := TouchInput{ToolName: "Edit", Cwd: plain}
	outside.ToolInput.FilePath = "/somewhere/else.md"
	if got := TouchOf(outside); got != nil {
		t.Errorf("outside the cwd too: %+v", got)
	}
	// a relative path resolves against the cwd; a file outside it counts for nothing, as does an empty one
	rel := TouchInput{ToolName: "Edit", Cwd: plain}
	rel.ToolInput.FilePath = "notes.md"
	if got := TouchOf(rel); *got != (Touch{Repo: plain, Path: "notes.md"}) {
		t.Errorf("a relative path: %+v", got)
	}
	outsideCwd := TouchInput{ToolName: "Edit", Cwd: filepath.Join(plain, "sub")}
	outsideCwd.ToolInput.FilePath = filepath.Join(plain, "notes.md")
	if got := TouchOf(outsideCwd); got != nil {
		t.Errorf("outside the cwd: %+v", got)
	}
	if got := touch("Edit", ""); got != nil {
		t.Errorf("no path: %+v", got)
	}
}

// TestAbsoluteForNamesEveryRootsSpelling pins the pure half of the touch's path resolution: a
// leading slash names from the root, everything else resolves against the session's cwd.
func TestAbsoluteForNamesEveryRootsSpelling(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"a root", "/somewhere/else.md", true},
		{"a relative", "src/a.ts", false},
		{"backslashes are plain characters", `\somewhere\else.md`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := absoluteFor(tc.path); got != tc.want {
				t.Errorf("absoluteFor(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestReportTouchReturnsTheWarnLineOnly pins the hook's half: the touch goes to the channel's
// touched op and the one-line warning for the session comes back; nothing else ever surfaces.
func TestReportTouchReturnsTheWarnLineOnly(t *testing.T) {
	var gotBody map[string]any
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.String()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":{"warn":"Huddle: api edited src/cart.ts just now — coordinate with it"}}`))
	}))
	defer srv.Close()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_TOKEN": "root"})
	id := Identity{URL: srv.URL, Channel: "shop", As: "web", Detail: "digest"}
	got := ReportTouch(context.Background(), w, id, Touch{Repo: "/r/shop", Path: "src/cart.ts"}, 1500)
	if got != "Huddle: api edited src/cart.ts just now — coordinate with it" {
		t.Errorf("ReportTouch = %q", got)
	}
	if want := "/api/c/shop/op/touched?as=web"; gotQuery != want {
		t.Errorf("the op's url = %q, want %q", gotQuery, want)
	}
	if !reflect.DeepEqual(gotBody, map[string]any{"repo": "/r/shop", "path": "src/cart.ts"}) {
		t.Errorf("the op's body = %v", gotBody)
	}
	// a server that refuses, answers nothing or cannot be reached: no warning, no error
	for name, handler := range map[string]http.HandlerFunc{
		"refused": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"shapeless": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"result":{}}`))
		},
	} {
		srv := httptest.NewServer(handler)
		got := ReportTouch(context.Background(), w, Identity{URL: srv.URL, Channel: "shop", As: "web"}, Touch{Repo: "/r", Path: "a"}, 1500)
		srv.Close()
		if got != "" {
			t.Errorf("%s: ReportTouch = %q, want nothing", name, got)
		}
	}
	if got := ReportTouch(context.Background(), w, Identity{URL: "http://127.0.0.1:1", Channel: "shop", As: "web"}, Touch{Repo: "/r", Path: "a"}, 50); got != "" {
		t.Errorf("unreachable: ReportTouch = %q, want nothing", got)
	}
}
