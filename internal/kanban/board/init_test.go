package board

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// init creates the board's layout once and never overwrites it.
func TestInitCreatesLayoutOnce(t *testing.T) {
	t.Run("test_init_creates_layout_once_and_never_overwrites", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), ".agents", "kanban")
		out := mustLines(t, initBoard(root, ""))
		for _, col := range Columns {
			if info, err := os.Stat(filepath.Join(root, col)); err != nil || !info.IsDir() {
				t.Errorf("column %s missing: %v", col, err)
			}
		}
		if data, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil {
			t.Errorf("README.md missing: %v", err)
		} else if !strings.Contains(string(data), "kanban pull") {
			t.Errorf("README.md does not carry the worker loop:\n%s", data)
		}
		epoch, err := os.ReadFile(filepath.Join(root, "EPOCH"))
		if err != nil || strings.TrimSpace(string(epoch)) != "1" {
			t.Errorf("EPOCH = %q, %v", epoch, err)
		}
		if data, err := os.ReadFile(filepath.Join(root, "LOG")); err != nil || len(data) != 0 {
			t.Errorf("LOG = %q, %v", data, err)
		}
		contains(t, strings.Join(out, "\n"), "EPOCH", "init output")

		if err := os.WriteFile(filepath.Join(root, "EPOCH"), []byte("7\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out2 := mustLines(t, initBoard(root, ""))
		contains(t, strings.Join(out2, "\n"), "nothing to create", "second init")
		epoch, _ = os.ReadFile(filepath.Join(root, "EPOCH"))
		if strings.TrimSpace(string(epoch)) != "7" {
			t.Errorf("EPOCH = %q, want 7 (init never overwrites)", epoch)
		}
	})
}

func TestInitPrintsAnAbsolutePath(t *testing.T) {
	root, err := filepath.Abs(filepath.Join(t.TempDir(), ".agents", "kanban"))
	if err != nil {
		t.Fatal(err)
	}
	out := mustLines(t, initBoard(root, ""))
	if len(out) == 0 || !filepath.IsAbs(strings.SplitN(out[0], ": ", 2)[0]) {
		t.Fatalf("init output %q does not open with an absolute path", out)
	}
	cols := make([]string, 0, len(Columns))
	for _, col := range Columns {
		cols = append(cols, col+"/")
	}
	if want := root + ": created " + strings.Join(cols, ", ") + ", README.md, EPOCH, LOG"; out[0] != want {
		t.Errorf("init line = %q, want %q", out[0], want)
	}
}

// referenceBoard builds a pre-kanban board: eight columns, a LOG, an EPOCH,
// a STOP-LINE and a README.
func referenceBoard(t *testing.T) string {
	t.Helper()
	old := filepath.Join(t.TempDir(), ".agents", "backlog")
	cols := []string{"tickets", "backlog", "blocked", "ready", "in-progress", "review", "testing", "done"}
	for _, col := range cols {
		if err := os.MkdirAll(filepath.Join(old, col), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	card := func(col, id string) {
		t.Helper()
		text := "---\nid: " + id + "\ntitle: \"" + id + "\"\ntype: docs\n---\n\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(old, col, id+".md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	card("backlog", "OLD-BACK")
	card("ready", "OLD-READY")
	card("in-progress", "OLD-PROG")
	card("tickets", "OLD-POOL")
	card("done", "OLD-DONE")
	if err := os.WriteFile(filepath.Join(old, "EPOCH"), []byte("5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "LOG"), []byte("{\"event\":\"old\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "STOP-LINE"), []byte(`{"epoch": 5, "actor": "lead", "note": "red"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "README.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestInitFromMigratesAReferenceBoard(t *testing.T) {
	old := referenceBoard(t)
	project := filepath.Dir(filepath.Dir(old)) // <tmp>/.agents/..
	root := filepath.Join(project, ".agents", "kanban")

	var before = map[string][]byte{}
	for _, p := range []string{
		filepath.Join(old, "backlog", "OLD-BACK.md"),
		filepath.Join(old, "ready", "OLD-READY.md"),
		filepath.Join(old, "in-progress", "OLD-PROG.md"),
		filepath.Join(old, "tickets", "OLD-POOL.md"),
		filepath.Join(old, "done", "OLD-DONE.md"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[filepath.Base(p)] = data
	}

	out := mustLines(t, initBoard(root, old))
	_ = out

	// tickets move byte for byte, the pool into backlog
	moves := map[string]string{
		"backlog/OLD-BACK.md":     "OLD-BACK.md",
		"ready/OLD-READY.md":      "OLD-READY.md",
		"in-progress/OLD-PROG.md": "OLD-PROG.md",
		"backlog/OLD-POOL.md":     "OLD-POOL.md",
		"done/OLD-DONE.md":        "OLD-DONE.md",
	}
	for path, name := range moves {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("%s not migrated: %v", path, err)
		}
		if string(before[name]) != string(data) {
			t.Errorf("%s was rewritten byte for byte differently", path)
		}
	}

	// LOG and EPOCH carry over, the migrate event is appended, the stop-line moves
	logData, err := os.ReadFile(filepath.Join(root, "LOG"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last["event"] != "migrate" || last["from"] != old {
		t.Errorf("migrate line = %v", last)
	}
	epoch, _ := os.ReadFile(filepath.Join(root, "EPOCH"))
	if strings.TrimSpace(string(epoch)) != "5" {
		t.Errorf("EPOCH = %q, want 5", epoch)
	}
	if _, err := os.Stat(filepath.Join(root, "STOP-LINE")); err != nil {
		t.Errorf("STOP-LINE not copied: %v", err)
	}

	// the old folder holds only a README that points at the new board
	entries, err := os.ReadDir(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "README.md" {
		t.Errorf("old board holds %d entries, want only README.md", len(entries))
	}
	pointer, err := os.ReadFile(filepath.Join(old, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pointer), root) {
		t.Errorf("old README does not point at %s:\n%s", root, pointer)
	}

	// the migrated board answers
	f := &fixture{t: t, root: root, b: Board{Root: root, Now: func() time.Time { return fixedNow }}}
	st := mustStatus(t, f)
	if st.Epoch != 5 {
		t.Errorf("epoch on the migrated board = %d", st.Epoch)
	}
}

func TestInitFromRefusesANonEmptyTarget(t *testing.T) {
	ctx := context.Background()
	old := referenceBoard(t)
	root := filepath.Join(t.TempDir(), "fresh", ".agents", "kanban")
	if err := os.MkdirAll(filepath.Join(root, "backlog"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backlog", "THERE.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Init(ctx, root, old, fixedNow)
	contains(t, refuseErr(t, err), "ticket", "refusal")
	if _, err := os.Stat(filepath.Join(old, "backlog", "OLD-BACK.md")); err != nil {
		t.Errorf("the source board was touched: %v", err)
	}
}

// A reference board missing some of its columns still migrates.
func TestInitFromAPartialBoard(t *testing.T) {
	old := filepath.Join(t.TempDir(), "partial", ".agents", "backlog")
	for _, col := range []string{"backlog", "ready"} {
		if err := os.MkdirAll(filepath.Join(old, col), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(old, "backlog", "P-1.md"), []byte("---\nid: P-1\ntitle: \"p\"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "LOG"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "new", ".agents", "kanban")
	out := mustLines(t, initBoard(root, old))
	if len(out) != 2 || !strings.Contains(out[1], "migrated 1 ticket(s)") {
		t.Errorf("init output = %q", out)
	}
	if !fileExists(filepath.Join(root, "backlog", "P-1.md")) {
		t.Error("the ticket did not move")
	}
	entries, err := os.ReadDir(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "README.md" {
		t.Errorf("old board holds %d entries, want only README.md", len(entries))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
