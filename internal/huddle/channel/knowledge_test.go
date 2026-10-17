package channel

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// asType reads one of remember's answers as the shape the case expects.
func asType[T any](t *testing.T, v any) T {
	t.Helper()
	out, ok := v.(T)
	if !ok {
		t.Fatalf("result = %#v, want a %T", v, out)
	}
	return out
}

// knowledgeWorld is two channels over one shared knowledge store: alpha with x and y, beta with z.
func knowledgeWorld(t *testing.T) (*Channel, *Channel, *Shared) {
	t.Helper()
	dir := t.TempDir()
	shared, err := OpenShared(bg, filepath.Join(dir, "shared.db"))
	if err != nil {
		t.Fatalf("open shared knowledge: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	a := openChannel(t, "alpha")
	b := openChannel(t, "beta")
	AttachShared(a, shared)
	AttachShared(b, shared)
	for _, n := range []string{"x", "y"} {
		if _, err := a.Join(bg, n, JoinOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Join(bg, "z", JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	return a, b, shared
}

// Knowledge that lasts: verified first, age and "may be stale", duplicates offered for supersede,
// server-wide entries every channel recalls, and the Markdown export for CLAUDE.md.
func TestKnowledge(t *testing.T) {
	world := knowledgeWorld
	agoISO := func(days float64) string {
		return time.Now().Add(-time.Duration(days * float64(24*time.Hour))).UTC().Format("2006-01-02T15:04:05Z")
	}
	ids := func(items []KBShape) []int64 {
		out := make([]int64, len(items))
		for i, k := range items {
			out[i] = k.ID
		}
		return out
	}
	first := func(t *testing.T, ch *Channel, q string) KBShape {
		t.Helper()
		r, err := Recall(bg, ch, q, RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r) == 0 {
			t.Fatalf("recall %q found nothing", q)
		}
		return r[0]
	}

	t.Run("verified entries rank first in recall, and anyone can verify or take it back", func(t *testing.T) {
		a, _, _ := world(t)
		k1, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "orders table indexed by created_at", Body: "orders paginate by created_at"})
		if err != nil {
			t.Fatal(err)
		}
		k2, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "orders use cursor pagination", Body: "the API pages orders by cursor"})
		if err != nil {
			t.Fatal(err)
		}
		if first(t, a, "orders").Title == "" {
			t.Fatal("recall answered nothing")
		}
		v, err := Verify(bg, a, "y", k1.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if deref(v.VerifiedBy) != "y" {
			t.Errorf("verified_by = %q, want y", deref(v.VerifiedBy))
		}
		r, err := Recall(bg, a, "orders", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids(r), []int64{k1.ID, k2.ID}) {
			t.Errorf("recall = %v, want [%d %d]", ids(r), k1.ID, k2.ID)
		}
		if r[0].VerifiedAt == nil {
			t.Error("first recall entry is not verified")
		}
		if _, err := Verify(bg, a, Owner, k2.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(bg, a, "y", k1.ID, true); err != nil {
			t.Fatal(err)
		}
		if got := first(t, a, "orders").ID; got != k2.ID {
			t.Errorf("recall first = %d, want %d", got, k2.ID)
		}
		evs, err := a.Events(bg, EventsFilter{Topic: "kb.verified"})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 3 {
			t.Errorf("kb.verified events = %d, want 3", len(evs))
		}
		op, err := RunOp(bg, "recall", a, "x", vv(t, `{"q": "orders"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "verified (owner)") {
			t.Errorf("recall text = %q, want verified (owner)", deref(op.Text))
		}
	})

	t.Run("staleness: age, older than the default without a verify, or a referenced file edited since", func(t *testing.T) {
		a, _, _ := world(t)
		clock := time.Now()
		a.Now = func() time.Time { return clock }
		k, err := Remember(bg, a, "x", RememberArgs{
			Kind: "howto", Title: "build the cart", Body: "make cart",
			Refs: []string{"src/cart.ts:10-20", "https://example.com/doc"},
		})
		if err != nil {
			t.Fatal(err)
		}
		r := first(t, a, "cart")
		if r.AgeDays != 0 || r.Stale != nil {
			t.Errorf("fresh entry = %+v, want no age and no stale", r)
		}
		// another session edits the file the entry cites (a second passes, so the stamp moves)
		clock = clock.Add(1100 * time.Millisecond)
		if _, err := Touched(bg, a, "y", TouchArgs{Repo: "/r/shop", Path: "src/cart.ts"}); err != nil {
			t.Fatal(err)
		}
		r = first(t, a, "cart")
		if r.Stale == nil || !strings.Contains(*r.Stale, "src/cart.ts was edited since") {
			t.Errorf("stale = %v, want src/cart.ts was edited since", r.Stale)
		}
		// verifying it after the edit: fresh again
		clock = clock.Add(1100 * time.Millisecond)
		if _, err := Verify(bg, a, "x", asType[KBEntry](t, k).ID, false); err != nil {
			t.Fatal(err)
		}
		if s := first(t, a, "cart").Stale; s != nil {
			t.Errorf("stale after verify = %q, want none", *s)
		}
		// an old entry nobody verified
		id := asType[KBEntry](t, k).ID
		if err := a.Store.Run(bg, "UPDATE knowledge SET created_at=?, verified_at=NULL WHERE id=?", agoISO(StaleDays()+3), id); err != nil {
			t.Fatal(err)
		}
		r = first(t, a, "cart")
		if r.AgeDays != int64(StaleDays()+3) {
			t.Errorf("age_days = %d, want %d", r.AgeDays, int64(StaleDays()+3))
		}
		if r.Stale == nil || !strings.Contains(*r.Stale, "not verified for over 30 days") {
			t.Errorf("stale = %v, want not verified for over 30 days", r.Stale)
		}
		op, err := RunOp(bg, "recall", a, "x", vv(t, `{"q": "cart"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`33d old.*MAY BE STALE`).MatchString(deref(op.Text)) {
			t.Errorf("recall text = %q, want 33d old then MAY BE STALE", deref(op.Text))
		}
	})

	t.Run("staleness: a referenced file on disk (in the channel's repo) that changed since", func(t *testing.T) {
		a, _, _ := world(t)
		repo := t.TempDir()
		schema := filepath.Join(repo, "schema.sql")
		if err := os.WriteFile(schema, []byte("create table t();"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Configure(bg, ConfigureArgs{Repo: sp(repo)}); err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "context", Title: "the schema", Body: "one table", Refs: []string{"schema.sql"}}); err != nil {
			t.Fatal(err)
		}
		if s := first(t, a, "schema").Stale; s != nil {
			t.Errorf("stale = %q, want none", *s)
		}
		later := time.Now().Add(60 * time.Second)
		if err := os.Chtimes(schema, later, later); err != nil {
			t.Fatal(err)
		}
		s := first(t, a, "schema").Stale
		if s == nil || !strings.Contains(*s, "schema.sql changed since") {
			t.Errorf("stale = %v, want schema.sql changed since", s)
		}
	})

	t.Run("duplicates: remember returns the existing entry and adds nothing; supersede or force adds", func(t *testing.T) {
		a, _, _ := world(t)
		k, err := a.Remember(bg, "x", RememberInput{Kind: "lesson", Title: "ugrep alias recurses in pipes", Body: "use /usr/bin/grep in pipes, the alias recurses"})
		if err != nil {
			t.Fatal(err)
		}
		d, err := Remember(bg, a, "y", RememberArgs{Kind: "lesson", Title: "ugrep alias recurses in pipes!", Body: "the alias recurses; use /usr/bin/grep in pipes"})
		if err != nil {
			t.Fatal(err)
		}
		dup, ok := d.(DuplicateResult)
		if !ok || !dup.Duplicate {
			t.Fatalf("remember = %#v, want a duplicate answer", d)
		}
		if dup.Existing.ID != k.ID {
			t.Errorf("existing = %d, want %d", dup.Existing.ID, k.ID)
		}
		if !strings.Contains(dup.Hint, fmt.Sprintf("supersedes=%d", k.ID)) {
			t.Errorf("hint = %q, want supersedes=%d", dup.Hint, k.ID)
		}
		mine, err := a.Recall(bg, "", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(mine) != 1 {
			t.Errorf("channel recall = %d entries, want 1", len(mine))
		}
		op, err := RunOp(bg, "remember", a, "y", vv(t, `{"kind": "fact", "title": "ugrep alias recurses in pipes", "body": "use /usr/bin/grep in pipes, the alias recurses"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^not added: #\d+ .*supersedes=`).MatchString(deref(op.Text)) {
			t.Errorf("remember text = %q, want not added with supersedes", deref(op.Text))
		}
		s, err := Remember(bg, a, "y", RememberArgs{Kind: "lesson", Title: "ugrep alias recurses in pipes", Body: "use /usr/bin/grep", Supersedes: ip(k.ID)})
		if err != nil {
			t.Fatal(err)
		}
		if asType[KBEntry](t, s).ID <= k.ID {
			t.Errorf("superseding id = %d, want above %d", asType[KBEntry](t, s).ID, k.ID)
		}
		r, err := Recall(bg, a, "ugrep", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids(r), []int64{asType[KBEntry](t, s).ID}) {
			t.Errorf("recall = %v, want the superseding entry only", ids(r))
		}
		f, err := Remember(bg, a, "y", RememberArgs{Kind: "lesson", Title: "ugrep alias recurses in pipes", Body: "use /usr/bin/grep", Force: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.(DuplicateResult); ok {
			t.Error("force added nothing")
		}
		other, err := Remember(bg, a, "y", RememberArgs{Kind: "fact", Title: "valkey runs on 6379", Body: "container cache-1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := other.(DuplicateResult); ok {
			t.Error("something different is never a duplicate")
		}
	})

	t.Run("scope server: every channel recalls it, by its own id range; share moves a channel entry there", func(t *testing.T) {
		a, b, _ := world(t)
		g, err := Remember(bg, a, "x", RememberArgs{Kind: "lesson", Title: "bun test needs --preload for env", Body: "set it in bunfig.toml", Scope: "server"})
		if err != nil {
			t.Fatal(err)
		}
		gs := asType[RememberShared](t, g)
		if gs.ID < SharedBase {
			t.Errorf("server-wide id = %d, want at or above %d", gs.ID, SharedBase)
		}
		if gs.Scope != "server" {
			t.Errorf("scope = %q, want server", gs.Scope)
		}
		fromB, err := Recall(bg, b, "preload", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids(fromB), []int64{gs.ID}) {
			t.Errorf("recall from beta = %v, want the server-wide entry", ids(fromB))
		}
		if deref(fromB[0].Origin) != "alpha" {
			t.Errorf("origin = %q, want alpha", deref(fromB[0].Origin))
		}
		full, err := KB(bg, b, gs.ID)
		if err != nil {
			t.Fatal(err)
		}
		if asType[KBShared](t, full).Body != "set it in bunfig.toml" {
			t.Errorf("body = %q", asType[KBShared](t, full).Body)
		}
		// and a duplicate of it, written in another channel, is caught
		d, err := Remember(bg, b, "z", RememberArgs{Kind: "lesson", Title: "bun test needs --preload for env", Body: "bunfig.toml sets it"})
		if err != nil {
			t.Fatal(err)
		}
		if dup, ok := d.(DuplicateResult); !ok || !dup.Duplicate {
			t.Errorf("remember = %#v, want a duplicate answer", d)
		}
		// share: a channel entry goes server-wide; the channel's copy leaves recall and points at it
		k, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "the CI runner is arm64", Body: "macos-14"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(bg, a, "y", k.ID, false); err != nil {
			t.Fatal(err)
		}
		s, err := Share(bg, a, "x", k.ID)
		if err != nil {
			t.Fatal(err)
		}
		sh := asType[ShareResult](t, s)
		if sh.ID < SharedBase {
			t.Errorf("shared id = %d, want at or above %d", sh.ID, SharedBase)
		}
		if deref(sh.VerifiedBy) != "y" {
			t.Errorf("verified_by = %q, want y", deref(sh.VerifiedBy))
		}
		ra, err := Recall(bg, a, "runner", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids(ra), []int64{sh.ID}) {
			t.Errorf("recall alpha = %v", ids(ra))
		}
		rb, err := Recall(bg, b, "runner", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids(rb), []int64{sh.ID}) {
			t.Errorf("recall beta = %v", ids(rb))
		}
		moved, err := KB(bg, a, k.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m := asType[KBFull](t, moved).MovedTo; m == nil || *m != sh.ID {
			t.Errorf("moved_to = %v, want %d", m, sh.ID)
		}
		chanRecall, err := a.Recall(bg, "runner", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(chanRecall) != 0 {
			t.Errorf("channel recall = %d, want 0", len(chanRecall))
		}
		// verify works on a server-wide entry from any channel
		vg, err := Verify(bg, b, "z", gs.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if deref(vg.VerifiedBy) != "z" {
			t.Errorf("verified_by = %q, want z", deref(vg.VerifiedBy))
		}
		// empty recall: the newest of both stores
		empty, err := Recall(bg, b, "", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(ids(empty), gs.ID) {
			t.Errorf("empty recall = %v, want the server-wide entry among them", ids(empty))
		}
	})

	t.Run("export: Markdown grouped by kind for CLAUDE.md, verified only on request, server-wide included", func(t *testing.T) {
		a, _, _ := world(t)
		k, err := a.Remember(bg, "x", RememberInput{Kind: "lesson", Title: "never run migrations twice", Body: "line one\nline two", Refs: []string{"db/migrate.ts"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Remember(bg, "x", RememberInput{Kind: "howto", Title: "run the tests", Body: "bun test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "the box has 64 GB", Body: "plenty", Scope: "server"}); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(bg, a, "y", k.ID, false); err != nil {
			t.Fatal(err)
		}
		md, err := ExportMarkdown(bg, a, ExportOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(md, `## Team knowledge (Huddle channel "alpha")`) {
			t.Errorf("markdown = %q, want the alpha header", md)
		}
		if strings.Index(md, "### Lessons") >= strings.Index(md, "### How-tos") {
			t.Errorf("markdown = %q, want Lessons before How-tos", md)
		}
		for _, want := range []string{
			"- **never run migrations twice** (see `db/migrate.ts`)\n  line one\n  line two",
			"- **run the tests**: bun test",
			"- **the box has 64 GB**: plenty (every channel)",
		} {
			if !strings.Contains(md, want) {
				t.Errorf("markdown misses %q", want)
			}
		}
		v, err := ExportMarkdown(bg, a, ExportOpts{Verified: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(v, "never run migrations twice") {
			t.Errorf("verified export = %q, want the verified lesson", v)
		}
		if strings.Contains(v, "run the tests") || strings.Contains(v, "### How-tos") {
			t.Errorf("verified export = %q, want the unverified entries left out", v)
		}
	})

	t.Run("an empty recall breaks created_at ties the way localeCompare does, not by bytes", func(t *testing.T) {
		a, _, _ := world(t)
		glm, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "first entry", Body: "one"})
		if err != nil {
			t.Fatal(err)
		}
		GLM, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "second entry", Body: "two"})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Store.Run(bg, "UPDATE knowledge SET created_at='glm' WHERE id=?", glm.ID); err != nil {
			t.Fatal(err)
		}
		if err := a.Store.Run(bg, "UPDATE knowledge SET created_at='GLM' WHERE id=?", GLM.ID); err != nil {
			t.Fatal(err)
		}
		r, err := Recall(bg, a, "", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		// lowercase sorts before uppercase in the locale, so a descending order answers GLM first;
		// a byte order would answer glm first
		if !slices.Equal(ids(r), []int64{GLM.ID, glm.ID}) {
			t.Errorf("recall = %v, want the GLM stamp first", ids(r))
		}
	})
}

// fileRefVectors is fileRef's verdict table; an empty want is a null answer.
func TestFileRefVectors(t *testing.T) {
	for _, v := range []struct {
		id   string
		ref  string
		want string
	}{
		{"ref001", "src/cart.ts", "src/cart.ts"},
		{"ref002", "src/cart.ts:10", "src/cart.ts"},
		{"ref003", "src/cart.ts:10-20", "src/cart.ts"},
		{"ref004", "./pkg/a.go", "pkg/a.go"},
		{"ref005", ".gitignore", ".gitignore"},
		{"ref006", "a:b.ts", "a:b.ts"},
		{"ref007", "https://example.com/doc", ""},
		{"ref008", "HTTP://EXAMPLE.COM/X", ""},
		{"ref009", "custom+scheme://x", ""},
		{"ref010", "mailto:a@b.c", "mailto:a@b.c"},
		{"ref011", "#12", ""},
		{"ref012", "42", ""},
		{"ref013", "", ""},
		{"ref014", "   ", ""},
		{"ref015", "\t x", "x"},
		{"ref016", "two words", ""},
		{"ref017", "lead nbsp", ""},
		{"ref018", "trail ls", ""},
		{"ref019", "vtab\vcheck", ""},
		{"ref020", "src/a b.ts", ""},
		{"ref021", ":12", ""},
		{"ref022", "x:12-34:56", "x:12-34"},
		{"ref023", "./", ""},
		{"ref024", "a//b", "a//b"},
		{"ref025", " ", ""},
		{"ref026", "café.md", "café.md"},
		{"ref027", "dir/ünï.txt", "dir/ünï.txt"},
	} {
		if got := fileRef(v.ref); got != v.want {
			t.Errorf("%s fileRef(%q) = %q, want %q", v.id, v.ref, got, v.want)
		}
	}
}

// The answers marshal with the wire's key order, whichever store wrote them.
func TestKnowledgeKeyOrder(t *testing.T) {
	a, b, _ := knowledgeWorld(t)
	keys := func(t *testing.T, v any) []string {
		t.Helper()
		data, err := domain.MarshalCompactJSON(v)
		if err != nil {
			t.Fatal(err)
		}
		d, err := jsfmt.DecodeValue(data)
		if err != nil {
			t.Fatal(err)
		}
		return d.Keys()
	}
	g, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "t", Body: "b", Scope: "server"})
	if err != nil {
		t.Fatal(err)
	}
	want := "id kind title by tags refs task created_at hit scope origin verified_by verified_at age_days stale body hits supersedes"
	if got := strings.Join(keys(t, g), " "); got != want {
		t.Errorf("remember(server) keys = %q, want %q", got, want)
	}
	k, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "u", Body: "v"})
	if err != nil {
		t.Fatal(err)
	}
	full, err := KB(bg, a, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	want = "id by kind title body tags refs task supersedes created_at hits superseded_by hit scope origin verified_by verified_at age_days stale moved_to"
	if got := strings.Join(keys(t, full), " "); got != want {
		t.Errorf("kb(channel) keys = %q, want %q", got, want)
	}
	shared, err := KB(bg, b, asType[RememberShared](t, g).ID)
	if err != nil {
		t.Fatal(err)
	}
	want = "id kind title by tags refs task created_at hit scope origin verified_by verified_at age_days stale body hits supersedes superseded_by moved_to"
	if got := strings.Join(keys(t, shared), " "); got != want {
		t.Errorf("kb(server) keys = %q, want %q", got, want)
	}
	d, err := Digest(bg, a, "24h", DigestOpts{SkipCost: true})
	if err != nil {
		t.Fatal(err)
	}
	want = "channel title since until totals sessions blocked questions cost"
	if got := strings.Join(keys(t, d), " "); got != want {
		t.Errorf("digest keys = %q, want %q", got, want)
	}
	want = "done notes knowledge events blocked questions approvals"
	if got := strings.Join(keys(t, d.Totals), " "); got != want {
		t.Errorf("digest totals keys = %q, want %q", got, want)
	}
	if len(d.Sessions) > 0 {
		want = "name state role done notes knowledge approvals asked events cost"
		if got := strings.Join(keys(t, d.Sessions[0]), " "); got != want {
			t.Errorf("digest session keys = %q, want %q", got, want)
		}
	}
}

// The knowledge ops as the operation table serves them: the same run the HTTP API and the MCP
// tools make, with each rendering and each refusal.
func TestKnowledgeOps(t *testing.T) {
	t.Run("kb, verify, share and digest run through the table, with the agent's renderings", func(t *testing.T) {
		a, b, _ := knowledgeWorld(t)
		k, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "orders table", Body: "indexed by created_at"})
		if err != nil {
			t.Fatal(err)
		}
		op, err := RunOp(bg, "kb", a, "y", vv(t, fmt.Sprintf(`{"id": %d}`, k.ID)))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			fmt.Sprintf("#%d [fact] orders table", k.ID), "by x · ", "indexed by created_at",
		} {
			if !strings.Contains(deref(op.Text), want) {
				t.Errorf("kb text = %q, want %q", deref(op.Text), want)
			}
		}
		// remember through the op, with tags, refs and force
		op, err = RunOp(bg, "remember", b, "z", vv(t, `{"kind": "howto", "title": "run the tests", "body": "bun test", "tags": ["ci"], "refs": ["bunfig.toml"], "force": true}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "remembered: run the tests") {
			t.Errorf("remember text = %q", deref(op.Text))
		}
		// verify through the op, and its undo rendering
		kRun, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "runner", Body: "arm64"})
		if err != nil {
			t.Fatal(err)
		}
		id := kRun.ID
		op, err = RunOp(bg, "verify", a, "y", vv(t, fmt.Sprintf(`{"id": %d}`, id)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "verified by y") {
			t.Errorf("verify text = %q", deref(op.Text))
		}
		op, err = RunOp(bg, "verify", a, "y", vv(t, fmt.Sprintf(`{"id": %d, "undo": true}`, id)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "no longer verified") {
			t.Errorf("unverify text = %q", deref(op.Text))
		}
		// share through the op
		op, err = RunOp(bg, "share", a, "x", vv(t, fmt.Sprintf(`{"id": %d}`, id)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), fmt.Sprintf("#%d is for every channel now (was #%d)", sharedID(t, op.Result), id)) {
			t.Errorf("share text = %q", deref(op.Text))
		}
		// sharing again answers the entry it already moved to
		op, err = RunOp(bg, "share", a, "x", vv(t, fmt.Sprintf(`{"id": %d}`, id)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "is for every channel now: runner") {
			t.Errorf("share-again text = %q", deref(op.Text))
		}
		// the digest op renders the channel's day
		op, err = RunOp(bg, "digest", a, "x", vv(t, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "digest: alpha · since") {
			t.Errorf("digest text = %q", deref(op.Text))
		}
		// recall through the op with a filter, and its empty answer
		op, err = RunOp(bg, "recall", a, "x", vv(t, `{"q": "nothing matches this", "kind": "howto"}`))
		if err != nil {
			t.Fatal(err)
		}
		if deref(op.Text) != "(nothing yet: remember what you learn)" {
			t.Errorf("empty recall text = %q", deref(op.Text))
		}
	})

	t.Run("the refusals: scope, supersedes across stores, missing entries, no shared knowledge", func(t *testing.T) {
		a, b, _ := knowledgeWorld(t)
		k, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "t", Body: "b"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RunOp(bg, "remember", a, "x", vv(t, `{"kind": "fact", "title": "t", "body": "b", "scope": "everywhere"}`)); errStatus(err) != 400 {
			t.Errorf("bad scope = %v", err)
		}
		// a channel entry cannot be superseded from the server store, nor a shared one from a channel
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "t", Body: "b", Scope: "server", Supersedes: ip(k.ID)}); errStatus(err) != 400 || !strings.Contains(err.Error(), "supersede it without scope=server") {
			t.Errorf("channel supersedes from server = %v", err)
		}
		g := asType[RememberShared](t, mustRemember(t, a, "x", RememberArgs{Kind: "fact", Title: "s", Body: "b", Scope: "server"}))
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "s2", Body: "b", Supersedes: ip(g.ID)}); errStatus(err) != 400 || !strings.Contains(err.Error(), "supersede it with scope=server") {
			t.Errorf("server supersedes from channel = %v", err)
		}
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "s3", Body: "b", Scope: "server", Supersedes: ip(999999)}); errStatus(err) != 404 {
			t.Errorf("missing supersedes = %v", err)
		}
		if _, err := Remember(bg, a, "ghost", RememberArgs{Kind: "fact", Title: "s4", Body: "b", Scope: "server"}); errStatus(err) != 404 || !strings.Contains(err.Error(), "has not joined") {
			t.Errorf("unjoined remember = %v", err)
		}
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "bogus", Title: "s5", Body: "b", Scope: "server"}); errStatus(err) != 400 {
			t.Errorf("bad kind = %v", err)
		}
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "  ", Body: "b", Scope: "server", Force: true}); errStatus(err) != 400 {
			t.Errorf("blank title = %v", err)
		}
		if _, err := KB(bg, a, 424242); errStatus(err) != 404 {
			t.Errorf("missing channel entry = %v", err)
		}
		if _, err := KB(bg, b, 242424); errStatus(err) != 404 {
			t.Errorf("missing shared entry = %v", err)
		}
		if _, err := Verify(bg, a, "x", 424242, false); errStatus(err) != 404 {
			t.Errorf("verify missing = %v", err)
		}
		if _, err := Verify(bg, b, "z", 242424, false); errStatus(err) != 404 {
			t.Errorf("verify missing shared = %v", err)
		}
		if _, err := Share(bg, b, "z", g.ID); errStatus(err) != 400 || !strings.Contains(err.Error(), "already for every channel") {
			t.Errorf("share shared = %v", err)
		}
		if _, err := Share(bg, a, "x", 42); errStatus(err) != 404 {
			t.Errorf("share missing = %v", err)
		}
		old, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "old", Body: "superseded soon"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, a, "y", RememberArgs{Kind: "fact", Title: "new", Body: "replaces it", Supersedes: ip(old.ID), Force: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := Share(bg, a, "x", old.ID); errStatus(err) != 400 || !strings.Contains(err.Error(), "was superseded") {
			t.Errorf("share superseded = %v", err)
		}
		// a channel without the shared store keeps its own knowledge and refuses the server scope
		solo := openChannel(t, "solo")
		if _, err := solo.Join(bg, "s", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, solo, "s", RememberArgs{Kind: "fact", Title: "t", Body: "b", Scope: "server"}); errStatus(err) != 400 || !strings.Contains(err.Error(), "no server-wide knowledge") {
			t.Errorf("remember without shared = %v", err)
		}
		if _, err := Share(bg, solo, "s", 1); errStatus(err) != 400 {
			t.Errorf("share without shared = %v", err)
		}
		if r, err := Recall(bg, solo, "", RecallOpts{}); err != nil || len(r) != 0 {
			t.Errorf("recall without shared = %v (%v)", r, err)
		}
		// a directory chain that cannot be created answers its error, and no database stays open
		// behind it
		blocked := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocked, []byte("a file stands in the chain's way"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenShared(bg, filepath.Join(blocked, "sub", "shared.db")); err == nil {
			t.Fatal("open shared through a file in the path answered no error")
		}
		_ = b
	})

	t.Run("a shared store without its index falls back to LIKE over the words", func(t *testing.T) {
		a, b, shared := knowledgeWorld(t)
		shared.Store.FTS = false
		shared.FTS = false
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "cursor pagination", Body: "keyset pages", Scope: "server"}); err != nil {
			t.Fatal(err)
		}
		r, err := Recall(bg, b, "keyset pages", RecallOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r) != 1 || r[0].Title != "cursor pagination" {
			t.Errorf("LIKE recall = %+v", r)
		}
		r, err = Recall(bg, b, "", RecallOpts{})
		if err != nil || len(r) != 1 {
			t.Errorf("newest recall = %+v (%v)", r, err)
		}
	})

	t.Run("recall keeps the filters the channel's own recall applies", func(t *testing.T) {
		a, _, shared := knowledgeWorld(t)
		shared.Store.FTS = false
		shared.FTS = false
		if _, err := Remember(bg, a, "x", RememberArgs{Kind: "lesson", Title: "cache wars", Body: "valkey first", Tags: []string{"infra"}, Scope: "server"}); err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, a, "y", RememberArgs{Kind: "fact", Title: "cache facts", Body: "valkey second", Scope: "server"}); err != nil {
			t.Fatal(err)
		}
		byKind, err := Recall(bg, a, "valkey", RecallOpts{Kind: "lesson"})
		if err != nil {
			t.Fatal(err)
		}
		if len(byKind) != 1 || byKind[0].Kind != "lesson" {
			t.Errorf("kind filter = %+v", byKind)
		}
		byBy, err := Recall(bg, a, "valkey", RecallOpts{By: "y"})
		if err != nil {
			t.Fatal(err)
		}
		if len(byBy) != 1 || byBy[0].By != "y" {
			t.Errorf("by filter = %+v", byBy)
		}
		byTag, err := Recall(bg, a, "valkey", RecallOpts{Tag: "infra"})
		if err != nil {
			t.Fatal(err)
		}
		if len(byTag) != 1 || byTag[0].Title != "cache wars" {
			t.Errorf("tag filter = %+v", byTag)
		}
		if limited, err := Recall(bg, a, "valkey", RecallOpts{Limit: 1}); err != nil || len(limited) != 1 {
			t.Errorf("limit = %+v (%v)", limited, err)
		}
	})
}

func mustRemember(t *testing.T, ch *Channel, me string, k RememberArgs) any {
	t.Helper()
	v, err := Remember(bg, ch, me, k)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sharedID(t *testing.T, v any) int64 {
	t.Helper()
	if s, ok := v.(ShareResult); ok {
		return s.ID
	}
	if s, ok := v.(KBShared); ok {
		return s.ID
	}
	t.Fatalf("share result = %#v, want a shared entry", v)
	return 0
}

// The staleness threshold reads $HUDDLE_KB_STALE_DAYS, and a shared file that cannot open answers
// its error.
func TestStaleDaysSetting(t *testing.T) {
	t.Setenv("HUDDLE_KB_STALE_DAYS", "7")
	if StaleDays() != 7 {
		t.Errorf("stale days = %v, want 7", StaleDays())
	}
	a, _, _ := knowledgeWorld(t)
	k, err := a.Remember(bg, "x", RememberInput{Kind: "fact", Title: "old one", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	if err := a.Store.Run(bg, "UPDATE knowledge SET created_at=? WHERE id=?", old, k.ID); err != nil {
		t.Fatal(err)
	}
	r, err := Recall(bg, a, "old", RecallOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 1 || r[0].Stale == nil || !strings.Contains(*r[0].Stale, "not verified for over 7 days") {
		t.Errorf("stale = %+v, want over 7 days", r)
	}
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenShared(bg, filepath.Join(notDir, "shared.db")); err == nil {
		t.Error("open shared through a file answered no error")
	}
}

// The renderings' other arms: costs of every size, an entry's every mark, and a digest with more
// than its happy path in it.
func TestKnowledgeRenderArms(t *testing.T) {
	t.Run("the cost renders at every size, and the digest text keeps its edge lines", func(t *testing.T) {
		ch := openChannel(t, "dg")
		for _, n := range []string{"api", "web", "quiet"} {
			if _, err := ch.Join(bg, n, JoinOpts{}); err != nil {
				t.Fatal(err)
			}
		}
		Link(bg, ch, "api", "claude-api")
		Link(bg, ch, "web", "claude-web")
		obs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			fmt.Fprint(w, `{"rows": [{"key": "claude-api", "label": "api", "requests": 1, "tokens": {}, "costUsd": 250}, {"key": "claude-web", "label": "web", "requests": 1, "tokens": {}, "costUsd": 0.004}]}`)
		}))
		defer obs.Close()
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "port"), []byte(strconv.Itoa(obs.Listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
			t.Fatal(err)
		}
		radar := outside.NewRadar()
		radar.Env = func(name string) string {
			if name == "RADAR_HOME" {
				return home
			}
			return ""
		}
		radar.Client = obs.Client()
		ch.Radar = radar
		if _, err := ch.CreateTask(bg, "api", CreateTaskInput{ID: "t1", Title: "One"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "api", CreateTaskInput{ID: "t0", Title: "Zero"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "api", CreateTaskInput{ID: "t2", Title: "Two", Owner: sp("api"), After: []string{"t0"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "t1", "skipped", "not needed"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "t2", "blocked", "waits"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 9; i++ {
			if _, err := ch.Note(bg, "web", "t1", "question", fmt.Sprintf("note %d", i)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.Send(bg, "web", nil, "to everyone", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		d, err := Digest(bg, ch, vv(t, `"24h"`), DigestOpts{})
		if err != nil {
			t.Fatal(err)
		}
		txt := DigestText(d)
		for _, want := range []string{
			"est. cost $250 (radar, this day)",
			"est. $250",
			"est. $0.004",
			"skipped t1 One",
			"… 1 more notes",
			"t2 Two @api — waits (waits on t0)",
			"→ everyone:",
			"nothing finished or shared",
		} {
			if !strings.Contains(txt, want) {
				t.Errorf("digest text misses %q:\n%s", want, txt)
			}
		}
	})

	t.Run("an entry's every mark renders: tags, refs, superseded, moved", func(t *testing.T) {
		a, _, _ := knowledgeWorld(t)
		old, err := a.Remember(bg, "x", RememberInput{Kind: "howto", Title: "old way", Body: "b", Tags: []string{"t"}, Refs: []string{"r.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Remember(bg, a, "y", RememberArgs{Kind: "howto", Title: "new way", Body: "c", Supersedes: ip(old.ID), Force: true}); err != nil {
			t.Fatal(err)
		}
		op, err := RunOp(bg, "kb", a, "x", vv(t, fmt.Sprintf(`{"id": %d}`, old.ID)))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"tags: t", "refs: r.txt", "SUPERSEDED by #"} {
			if !strings.Contains(deref(op.Text), want) {
				t.Errorf("kb text = %q, want %q", deref(op.Text), want)
			}
		}
		g, err := Remember(bg, a, "x", RememberArgs{Kind: "fact", Title: "shared one", Body: "b", Tags: []string{"s"}, Refs: []string{"s.txt"}, Scope: "server"})
		if err != nil {
			t.Fatal(err)
		}
		gs := asType[RememberShared](t, g)
		if _, err := Remember(bg, a, "y", RememberArgs{Kind: "fact", Title: "shared two", Body: "c", Supersedes: ip(gs.ID), Scope: "server", Force: true}); err != nil {
			t.Fatal(err)
		}
		op, err = RunOp(bg, "kb", a, "x", vv(t, fmt.Sprintf(`{"id": %d}`, gs.ID)))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"tags: s", "refs: s.txt", "SUPERSEDED by #", "every channel"} {
			if !strings.Contains(deref(op.Text), want) {
				t.Errorf("shared kb text = %q, want %q", deref(op.Text), want)
			}
		}
		// an id passed as a string still reads, one that is not a number reads nothing, and force
		// arrives in more shapes than a boolean
		op, err = RunOp(bg, "kb", a, "x", vv(t, fmt.Sprintf(`{"id": "%d"}`, old.ID)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(op.Text), "old way") {
			t.Errorf("kb by string id text = %q", deref(op.Text))
		}
		if _, err := RunOp(bg, "kb", a, "x", vv(t, `{"id": "not a number"}`)); errStatus(err) != 404 {
			t.Errorf("kb by a non-number = %v, want 404", err)
		}
		if _, err := RunOp(bg, "remember", a, "x", vv(t, `{"kind": "fact", "title": "array force", "body": "b", "force": ["x"]}`)); err != nil {
			t.Errorf("array force = %v", err)
		}
		if _, err := RunOp(bg, "remember", a, "x", vv(t, `{"kind": "fact", "title": "str force", "body": "b", "force": "yes"}`)); err != nil {
			t.Errorf("string force = %v", err)
		}
		if _, err := RunOp(bg, "remember", a, "x", vv(t, `{"kind": "fact", "title": "num force", "body": "b", "force": 0}`)); err != nil {
			t.Errorf("zero force = %v", err)
		}
		if _, err := RunOp(bg, "remember", a, "x", vv(t, `{"kind": "fact", "title": "should dup", "body": "num force body"}`)); err != nil {
			t.Fatal(err)
		}
		op, err = RunOp(bg, "remember", a, "x", vv(t, `{"kind": "fact", "title": "should dup", "body": "num force body", "force": false}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(deref(op.Text), "not added:") {
			t.Errorf("falsy force text = %q", deref(op.Text))
		}
		if _, err := RunOp(bg, "digest", a, "x", vv(t, `{"since": 45}`)); err != nil {
			t.Errorf("numeric since = %v", err)
		}
		if _, err := RunOp(bg, "digest", a, "x", vv(t, `{"since": true}`)); errStatus(err) != 400 || !strings.Contains(err.Error(), "not true") {
			t.Errorf("boolean since = %v", err)
		}
	})
}
