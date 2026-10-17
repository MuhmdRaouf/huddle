package home

// The import's edges (SPEC 4.3): the walks that find a legacy home read only what the files
// name, names are claimed until they fit a channel's shape, the merges answer what moved in and
// refuse a write that cannot land, and a source whose channel is broken is rolled back and
// named in the report. The legacy folders are only ever read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

func TestLedgerErrorNamesItsFile(t *testing.T) {
	msg := LedgerError{Path: "/one/imports.json"}.Error()
	if !strings.Contains(msg, "/one/imports.json") || !strings.Contains(msg, "cannot be read") {
		t.Errorf("the refusal = %q", msg)
	}
}

func TestSettingsHomeReadsOnlyTheHomeKey(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")
	if got := settingsHome(missing); got != "" {
		t.Errorf("a missing file named %q", got)
	}
	torn := filepath.Join(dir, "torn.json")
	if err := os.WriteFile(torn, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsHome(torn); got != "" {
		t.Errorf("a torn file named %q", got)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"channel":"main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsHome(empty); got != "" {
		t.Errorf("a file with no home key named %q", got)
	}
	plain := filepath.Join(dir, "plain.json")
	if err := os.WriteFile(plain, []byte(`{"home":"/one/shared"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsHome(plain); got != "/one/shared" {
		t.Errorf("an absolute home = %q, want /one/shared", got)
	}
	tilde := filepath.Join(dir, "tilde.json")
	t.Setenv("HOME", "/Users/a")
	if err := os.WriteFile(tilde, []byte(`{"home":"~/shared"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsHome(tilde); got != filepath.Join("/Users/a", "shared") {
		t.Errorf("a tilde home = %q, want it expanded", got)
	}
	bare := filepath.Join(dir, "bare.json")
	if err := os.WriteFile(bare, []byte(`{"home":"~"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := settingsHome(bare); got != "/Users/a" {
		t.Errorf("a bare tilde = %q, want the user's home", got)
	}
}

func TestMainCheckoutFollowsTheGitFiles(t *testing.T) {
	// a .git directory marks the main checkout itself
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := mainCheckout(deep); got != repo {
		t.Errorf("mainCheckout = %q, want the repo with the .git directory", got)
	}
	// a worktree's .git file names the main checkout through its gitdir line
	worktree := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	gitfile := filepath.Join(worktree, ".git")
	main := filepath.Join(t.TempDir(), "main")
	line := fmt.Sprintf("gitdir: %s/.git/worktrees/wt\n", main)
	if err := os.WriteFile(gitfile, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mainCheckout(worktree); got != main {
		t.Errorf("mainCheckout = %q, want the main checkout %s", got, main)
	}
	// a .git file that names no worktree is the checkout itself
	if err := os.WriteFile(gitfile, []byte("gitdir: /elsewhere/repo/.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mainCheckout(worktree); got != worktree {
		t.Errorf("mainCheckout = %q, want the directory holding the .git file", got)
	}
	// no .git anywhere walks to the root and answers none
	if got := mainCheckout(filepath.Join(t.TempDir(), "nowhere")); got != "" {
		t.Errorf("mainCheckout = %q, want none", got)
	}
}

func TestSlugAndFitChannelSpellChannelSafeNames(t *testing.T) {
	cases := []struct{ label, want string }{
		{"Acme Repo", "acme-repo"},
		{"  Messy__Name!!  ", "messy-name"},
		{"", "x"},
		{"---", "x"},
		{"éè", "x"},
	}
	for _, tc := range cases {
		if got := slug(tc.label, 40); got != tc.want {
			t.Errorf("slug(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
	if got := slug("abcdefgh", 3); got != "abc" {
		t.Errorf("a cut slug = %q, want abc", got)
	}
	if got := slug("abcd--", 4); got != "abcd" {
		t.Errorf("a cut slug = %q, want its trailing dashes gone", got)
	}
	if got := slug("ab", 0); got != "a" {
		t.Errorf("a slug with no room = %q, want one character", got)
	}
	// a candidate is cut to 40 and carries no dash at either end
	if got := fitChannel(strings.Repeat("a", 50)); got != strings.Repeat("a", 40) {
		t.Errorf("a long candidate = %d characters, want 40", len(got))
	}
	if got := fitChannel("---name---"); got != "name" {
		t.Errorf("a dashed candidate = %q, want its ends trimmed", got)
	}
	if got := fitChannel("---"); got != "x" {
		t.Errorf("an empty candidate = %q, want x", got)
	}
}

func TestClaimNameSuffixesAndCountsUntilAFreeName(t *testing.T) {
	taken := map[string]bool{}
	if got := claimName("main", "acme", taken); got != "main" {
		t.Errorf("a free name = %q, want main", got)
	}
	if got := claimName("main", "acme repo", taken); got != "main-acme-repo" {
		t.Errorf("a taken name = %q, want the label's slug as a suffix", got)
	}
	if got := claimName("main", "acme", taken); got != "main-acme" {
		t.Errorf("a name whose suffix is free = %q, want the short suffix", got)
	}
	if got := claimName("main", "acme", taken); got != "main-acme-2" {
		t.Errorf("a twice-taken name = %q, want a counter", got)
	}
	// a suffix too long for a channel's name is cut, then counted
	crowded := map[string]bool{"main": true}
	long := strings.Repeat("a", 45)
	for i := 0; i < 3; i++ {
		got := claimName("main", long, crowded)
		if len(got) > 40 {
			t.Fatalf("the claimed name %q runs past a channel's shape", got)
		}
	}
	// every name claimed is marked taken, so the next source sees it
	if !taken["main"] {
		t.Error("the first claim never marked its name taken")
	}
}

func TestChannelNamesReadsSortedValidNames(t *testing.T) {
	source := filepath.Join(t.TempDir(), "legacy")
	channels := filepath.Join(source, "data", "channels")
	if err := os.MkdirAll(channels, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta.db", "alpha.db", "Bad.db", "notes.txt", "shop.db"} {
		if err := os.WriteFile(filepath.Join(channels, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(channels, "adir.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	names, err := channelNames(source)
	if err != nil {
		t.Fatalf("channelNames: %v", err)
	}
	if strings.Join(names, ",") != "alpha,shop,zeta" {
		t.Errorf("names = %v, want alpha,shop,zeta", names)
	}
	// a source with no channels holds none
	if names, err = channelNames(filepath.Join(t.TempDir(), "other")); err != nil || names != nil {
		t.Errorf("channelNames of a bare source = %v (%v), want none", names, err)
	}
	// a channels directory that cannot be read is named
	other := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(filepath.Join(other, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "data", "channels"), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := channelNames(other); err == nil {
		t.Error("a channels file read as a directory succeeded")
	}
}

func TestTakenNamesReadsTheHome(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if taken := takenNames(h); len(taken) != 0 {
		t.Errorf("a home with no channels = %v, want none", taken)
	}
	if err := os.MkdirAll(h.Channels(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shop.db", "Bad.db", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(h.Channels(), name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	taken := takenNames(h)
	if len(taken) != 1 || !taken["shop"] {
		t.Errorf("taken = %v, want shop alone", taken)
	}
}

func TestLegacySessionsDirFollowsTheStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/one/state")
	t.Setenv("HOME", "/Users/a")
	if got := legacySessionsDir(); got != filepath.Join("/one/state", "huddle", "sessions") {
		t.Errorf("with a state home = %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	if got := legacySessionsDir(); got != filepath.Join("/Users/a", ".local", "state", "huddle", "sessions") {
		t.Errorf("without a state home = %q, want it under the user's home", got)
	}
}

func TestLegacyCredentialForPrefersTheRootCredential(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if got := legacyCredentialFor("http://127.0.0.1:1"); got != "" {
		t.Errorf("with no sessions dir = %q, want none", got)
	}
	dir := legacySessionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, url, cred string, root bool) {
		t.Helper()
		body := fmt.Sprintf(`{"url":%q,"credential":%q,"root":%v}`+"\n", url, cred, root)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const addr = "http://127.0.0.1:18860"
	write("zz.json", addr, "hcred_plain", false)
	write("aa.json", addr, "hcred_root", true)
	write("torn.json", addr, "", false)
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("elsewhere.json", "http://127.0.0.1:2", "hcred_other", true)
	if err := os.MkdirAll(filepath.Join(dir, "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := legacyCredentialFor(addr); got != "hcred_root" {
		t.Errorf("the credential = %q, want the root one", got)
	}
	// the root gone, the first matching one answers
	if err := os.Remove(filepath.Join(dir, "aa.json")); err != nil {
		t.Fatal(err)
	}
	if got := legacyCredentialFor(addr); got != "hcred_plain" {
		t.Errorf("without the root = %q, want the first matching credential", got)
	}
	if got := legacyCredentialFor("http://127.0.0.1:3"); got != "" {
		t.Errorf("an address no session names = %q, want none", got)
	}
}

func TestReadAuthStateAnswersWholeWhenTheFileIsNot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	state := readAuthState(missing)
	if state.V != 1 || state.Roots == nil || state.Invites == nil || state.Members == nil || state.Browsers == nil || state.Issued == nil {
		t.Errorf("a missing file read as %+v, want the empty state", state)
	}
	torn := filepath.Join(t.TempDir(), "torn.json")
	if err := os.WriteFile(torn, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state = readAuthState(torn); state.Roots == nil {
		t.Errorf("a torn file read as %+v, want the empty state", state)
	}
	bare := filepath.Join(t.TempDir(), "bare.json")
	if err := os.WriteFile(bare, []byte(`{"v":1,"note":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if state = readAuthState(bare); state.Note != "kept" || state.Roots == nil || len(state.Roots) != 0 {
		t.Errorf("a file with empty lists read as %+v, want the note and empty lists", state)
	}
}

func TestMergeAuthRefusesAWriteItCannotLand(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	// no legacy file: nothing moves
	if added, err := mergeAuth(h, filepath.Join(t.TempDir(), "absent.json")); err != nil || added != 0 {
		t.Errorf("a merge without a legacy file = (%d, %v), want none", added, err)
	}
	// the home's auth is a directory: a merge with something to add cannot land
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.Auth(), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "legacy-auth.json")
	if err := os.WriteFile(legacy, []byte(`{"v":1,"roots":["hcred_old"],"note":"old note"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeAuth(h, legacy); err == nil {
		t.Error("a merge onto a directory succeeded")
	}
}

func TestMergeKnowledgeMergesByIdAndRefusesABrokenLegacy(t *testing.T) {
	ctx := context.Background()
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// no legacy store: nothing moves
	if added, err := mergeKnowledge(ctx, h, filepath.Join(h.Root, "absent.db")); err != nil || added != 0 {
		t.Errorf("a merge without a legacy store = (%d, %v), want none", added, err)
	}
	// a legacy file that is no database is named
	junk := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(junk, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeKnowledge(ctx, h, junk); err == nil {
		t.Error("a legacy file that is no database merged")
	}
	// a legacy store whose knowledge table holds nothing adds nothing
	bare := filepath.Join(t.TempDir(), "bare.db")
	db, err := store.Open(ctx, bare)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if added, err := mergeKnowledge(ctx, h, bare); err != nil || added != 0 {
		t.Errorf("a legacy store with no knowledge table = (%d, %v), want none", added, err)
	}
	// rows merge by id: a row the home already holds is left as it is
	legacy := filepath.Join(t.TempDir(), "legacy.db")
	seedKnowledgeRow(t, legacy, 11, "from the legacy home", []string{"routing"})
	seedKnowledgeRow(t, filepath.Join(h.Root, "knowledge.db"), 11, "already here", nil)
	seedKnowledgeRow(t, filepath.Join(h.Root, "knowledge.db"), 12, "kept", nil)
	added, err := mergeKnowledge(ctx, h, legacy)
	if err != nil || added != 0 {
		t.Fatalf("the merge = (%d, %v), want nothing new", added, err)
	}
	if title := knowledgeTitle(t, h.Knowledge(), 11); title != "already here" {
		t.Errorf("row 11 = %q, want the home's own kept", title)
	}
}

// seedKnowledgeRow lays one knowledge row with the legacy schema's own columns.
func seedKnowledgeRow(t *testing.T, file string, id int64, title string, tags []string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx,
		"INSERT INTO knowledge (id, by, kind, title, body, tags, created_at) VALUES (?,?,?,?,?,?,?)",
		id, "atlas", "fact", title, "the row's body", string(raw), "2026-10-16T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
}

// knowledgeTitle answers one row's title by id, empty when it holds none.
func knowledgeTitle(t *testing.T, file string, id int64) string {
	t.Helper()
	rows, err := store.ReadAll(context.Background(), file, "SELECT title FROM knowledge WHERE id = ?", id)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	if len(rows) == 0 {
		return ""
	}
	title, _ := rows[0]["title"].(string)
	return title
}

func TestTagWordsReadsTheTagsColumn(t *testing.T) {
	if got := tagWords(nil); got != nil {
		t.Errorf("no tags column = %v, want none", got)
	}
	if got := tagWords(7); got != nil {
		t.Errorf("a number in the tags column = %v, want none", got)
	}
	if got := tagWords(""); got != nil {
		t.Errorf("an empty tags column = %v, want none", got)
	}
	if got := tagWords("{not json"); got != nil {
		t.Errorf("a torn tags column = %v, want none", got)
	}
	if got := tagWords(`["routing","ops"]`); len(got) != 2 || got[0] != "routing" || got[1] != "ops" {
		t.Errorf("tags = %v, want routing and ops", got)
	}
}

func TestCopyCredentialsCopiesOnceAndNamesAFailedWrite(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// no state dir: nothing to copy
	if copied, err := copyCredentials(h); err != nil || copied != nil {
		t.Errorf("a copy with no state dir = %v (%v), want none", copied, err)
	}
	dir := legacySessionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one.json"), []byte(credBody("http://127.0.0.1:1", "hcred_one", false)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	copied, err := copyCredentials(h)
	if err != nil {
		t.Fatalf("copyCredentials: %v", err)
	}
	if len(copied) != 1 || filepath.Base(copied[0].To) != "one.json" {
		t.Fatalf("copied = %+v, want one.json alone", copied)
	}
	// a second copy keeps the target as it is
	if err := os.WriteFile(filepath.Join(h.Sessions(), "one.json"), []byte("the home's own"), 0o600); err != nil {
		t.Fatal(err)
	}
	if copied, err = copyCredentials(h); err != nil || len(copied) != 0 {
		t.Fatalf("a second copy = %v (%v), want nothing", copied, err)
	}
	if body := readFile(t, filepath.Join(h.Sessions(), "one.json")); string(body) != "the home's own" {
		t.Errorf("the target was overwritten with %q", body)
	}
	// a target that is a directory cannot take the copy
	other := Home{Root: filepath.Join(t.TempDir(), "huddle2")}
	if err := os.MkdirAll(filepath.Join(other.Sessions(), "one.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := copyCredentials(other); err == nil {
		t.Error("a copy onto a directory succeeded")
	}
}

func TestReadLedgerRefusesWhatItCannotRead(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// a missing ledger is none yet
	ledger, err := readLedger(h)
	if err != nil || len(ledger.Imports) != 0 || ledger.V != 1 {
		t.Fatalf("a missing ledger = %+v (%v), want empty", ledger, err)
	}
	// an empty file (what Ensure writes) is none yet
	if err := os.WriteFile(h.Imports(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readLedger(h); err != nil {
		t.Fatalf("an empty ledger = %v", err)
	}
	// a torn ledger, a wrong version and an unreadable file all refuse
	for name, body := range map[string]string{
		"torn":   "{",
		"wrongv": `{"v":2,"imports":[]}`,
	} {
		if err := os.WriteFile(h.Imports(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = readLedger(h); err == nil {
			t.Errorf("the %s ledger read", name)
		} else if !errors.As(err, new(LedgerError)) {
			t.Errorf("the %s ledger's error = %v, want a LedgerError", name, err)
		}
	}
	if err := os.Remove(h.Imports()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.Imports(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = readLedger(h); err == nil {
		t.Error("a ledger that is a directory read")
	}
}

func TestWriteLedgerRefusesAWriteItCannotLand(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	// a nil imports list writes an empty ledger
	if err := writeLedger(h, ledgerFile{}); err != nil {
		t.Fatalf("writeLedger: %v", err)
	}
	entries := readLedgerEntries(t, h)
	if entries == nil || len(entries) != 0 {
		t.Errorf("the empty ledger = %v, want an empty list", entries)
	}
	// the file is a directory: the write refuses
	if err := os.MkdirAll(filepath.Join(h.Root, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := Home{Root: filepath.Join(h.Root, "other")}
	if err := os.MkdirAll(blocked.Imports(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeLedger(blocked, ledgerFile{}); err == nil {
		t.Error("a ledger written over a directory")
	}
}

func TestAppendLineAddsLinesAndSilencesItsFailures(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	noteImportLog(h, &Imported{Source: "/one", Channels: make([]CopiedFile, 2), Auth: 1, Knowledge: 3, Credentials: make([]CopiedFile, 4)})
	log := string(readFile(t, h.LogFile()))
	if strings.Count(log, "\n") != 1 || !strings.Contains(log, "2 channels from /one") {
		t.Errorf("the log = %q, want one line about two channels", log)
	}
	// a line under a file is silence, never an error
	if err := os.WriteFile(h.LogFile(), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := Home{Root: filepath.Join(h.Root, "x")}
	if err := os.MkdirAll(blocked.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked.Run(), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendLine(filepath.Join(blocked.Run(), "huddle.log"), "never lands")
}

func TestWriteNoticeSaysTheHomeTheWayAPersonReadsIt(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	h := Home{Root: filepath.Join(user, ".config", "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeNotice(h, &Imported{Source: "/legacy/one", Channels: make([]CopiedFile, 3)})
	notice := string(readFile(t, h.ImportNotice()))
	if !strings.Contains(notice, "3 channels") || !strings.Contains(notice, "/legacy/one") {
		t.Errorf("the notice = %q", notice)
	}
	if !strings.Contains(notice, "~/.config/huddle") {
		t.Errorf("the notice = %q, want the home spelled with a tilde", notice)
	}
}

func TestDisplayHomeSpellsTheUserHomeWithATilde(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	if got := displayHome(user); got != "~" {
		t.Errorf("the user's home = %q, want ~", got)
	}
	under := filepath.Join(user, ".config", "huddle")
	if got := displayHome(under); got != "~"+string(filepath.Separator)+filepath.Join(".config", "huddle") {
		t.Errorf("a home under the user's = %q, want a tilde spelling", got)
	}
	if got := displayHome(t.TempDir()); strings.HasPrefix(got, "~") {
		t.Errorf("an unrelated home = %q, want it as it is", got)
	}
}

func TestImportRefusesATornLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Imports(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := makeLegacyChannel(t, "acme", "main")
	if _, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}}); !errors.As(err, new(LedgerError)) {
		t.Errorf("an import on a torn ledger = %v, want a LedgerError", err)
	}
}

func TestImportNamesABrokenChannelAndRollsItBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	source := filepath.Join(repo, ".agents", "huddle")
	if err := os.MkdirAll(filepath.Join(source, "data", "channels"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a channel file that is no database cannot be copied
	if err := os.WriteFile(filepath.Join(source, "data", "channels", "broken.db"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "repo"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 0 || len(report.Failed) != 1 {
		t.Fatalf("the report = %+v, want the source failed", report)
	}
	if !strings.Contains(report.Failed[0], source) {
		t.Errorf("the failure = %q, want it to name the source", report.Failed[0])
	}
	if entries := readLedgerEntries(t, h); len(entries) != 0 {
		t.Errorf("the ledger recorded %v, want nothing", entries)
	}
	// the channels already copied by an earlier source in the same run stay: only the failed
	// source's own copies roll back
	good := makeLegacyChannel(t, "other", "main")
	report, err = Import(context.Background(), h, []Source{{Path: good, Label: "other"}})
	if err != nil || len(report.Imported) != 1 {
		t.Fatalf("the retry = %+v (%v), want the good source in", report, err)
	}
}

func TestImportHoldsEverySourceWhenTheBudgetIsGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := makeLegacyChannel(t, "acme", "main")
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Import(ctx, h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Waiting) != 1 || len(report.Imported) != 0 {
		t.Fatalf("the report = %+v, want the source waiting", report)
	}
	if entries := readLedgerEntries(t, h); len(entries) != 0 {
		t.Errorf("the ledger recorded %v, want nothing", entries)
	}
}

func TestImportBringsASourceWithNoChannelsAndNoStateIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "repo", ".agents", "huddle")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "repo"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 1 || report.Imported[0].Channels != nil {
		t.Fatalf("the report = %+v, want the bare source imported with no channels", report)
	}
	if entries := readLedgerEntries(t, h); len(entries) != 1 {
		t.Fatalf("the ledger holds %d entries, want the bare source", len(entries))
	}
}
