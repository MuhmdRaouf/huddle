package home

// The legacy reads at their edges (SPEC 4.3): where an old server answered and which credential
// it holds, the auth merge's windows and its refusals, and the import's ledger and rollbacks
// when a write cannot land. The legacy folders are only ever read.

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

func TestLegacyAddressReadsTheURLThenFallsBackToThePort(t *testing.T) {
	source := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "huddle.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	// no settings file names no address
	if got := legacyAddress(filepath.Join(t.TempDir(), "none")); got != "" {
		t.Errorf("a source with no settings = %q, want none", got)
	}
	// a torn file names none
	if got := legacyAddress(source(t, "{")); got != "" {
		t.Errorf("a torn settings = %q, want none", got)
	}
	// a url wins over the port, its trailing slash trimmed
	if got := legacyAddress(source(t, `{"url":"https://huddle.example/","port":18860}`)); got != "https://huddle.example" {
		t.Errorf("a url = %q, want it without the slash", got)
	}
	// a whole port on its own answers loopback
	if got := legacyAddress(source(t, `{"port":18860}`)); got != "http://127.0.0.1:18860" {
		t.Errorf("a port = %q, want loopback", got)
	}
	// a port that is no whole number in range names none
	for _, body := range []string{`{"port":0}`, `{"port":-5}`, `{"port":70000}`, `{"port":"18860"}`} {
		if got := legacyAddress(source(t, body)); got != "" {
			t.Errorf("settings %s = %q, want none", body, got)
		}
	}
}

func TestLegacyCredentialForRefusesWhatItCannotRead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	// the sessions dir is a file: no credential, never an error
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacySessionsDir(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := legacyCredentialFor("http://127.0.0.1:1"); got != "" {
		t.Errorf("with an unreadable sessions dir = %q, want none", got)
	}
	// a session file that cannot be read is skipped, and an unparseable one too
	if err := os.Remove(legacySessionsDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacySessionsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	const addr = "http://127.0.0.1:18861"
	// locked.json carries a credential for another address, so a caller that can read it anyway
	// (a root run) still sees it skipped
	if err := os.WriteFile(filepath.Join(legacySessionsDir(), "locked.json"), []byte(`{"url":"http://127.0.0.1:9","credential":"hcred_locked"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(legacySessionsDir(), "locked.json"), 0o600) })
	if err := os.WriteFile(filepath.Join(legacySessionsDir(), "torn.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacySessionsDir(), "answer.json"), []byte(fmt.Sprintf(`{"url":%q,"credential":"hcred_answer"}`, addr)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := legacyCredentialFor(addr); got != "hcred_answer" {
		t.Errorf("the credential = %q, want the one readable file's", got)
	}
}

func TestLegacySessionsDirFallsBackWithoutAStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if got := legacySessionsDir(); !strings.HasSuffix(got, filepath.Join(".local", "state", "huddle", "sessions")) {
		t.Errorf("with no home at all = %q, want the relative state path", got)
	}
}

func TestReadAuthStateFillsTheListsAFileNamesNull(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nulls.json")
	body := `{"v":1,"roots":null,"invites":null,"issued":null,"members":null,"browsers":null}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	state := readAuthState(file)
	if state.Roots == nil || state.Invites == nil || state.Issued == nil || state.Members == nil || state.Browsers == nil {
		t.Errorf("null lists read as %+v, want every list empty", state)
	}
}

func TestMergeAuthTakesTheNoteAndKeepsTheWindows(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "legacy-auth.json")
	// nine roots (the window holds eight), a note, an invite and a member
	roots := make([]string, 9)
	for i := range roots {
		roots[i] = fmt.Sprintf("hcred_root_%d", i)
	}
	body := fmt.Sprintf(`{"v":1,"note":"the legacy note","roots":[%q,%q,%q,%q,%q,%q,%q,%q,%q],"invites":[{"id":"i","secret":"hsecret","by":"a"}],"members":[{"name":"ada","cred":"hcred_ada"}]}`,
		roots[0], roots[1], roots[2], roots[3], roots[4], roots[5], roots[6], roots[7], roots[8])
	if err := os.WriteFile(legacy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := mergeAuth(h, legacy)
	if err != nil {
		t.Fatalf("mergeAuth: %v", err)
	}
	if added != 10 { // eight roots, one invite, one member
		t.Fatalf("added = %d, want ten", added)
	}
	merged := readAuthState(h.Auth())
	if len(merged.Roots) != 8 {
		t.Errorf("the merged roots = %d, want the window's eight", len(merged.Roots))
	}
	if merged.Note != "the legacy note" {
		t.Errorf("the merged note = %q, want the legacy's", merged.Note)
	}
	// a merge that adds nothing and a home that already has an auth file writes nothing
	before := string(readFile(t, h.Auth()))
	if added, err = mergeAuth(h, legacy); err != nil || added != 0 {
		t.Errorf("the second merge = (%d, %v), want nothing", added, err)
	}
	if after := string(readFile(t, h.Auth())); after != before {
		t.Error("an empty merge rewrote the auth file")
	}
}

func TestMergeAuthStopsAtTheBrowsersWindow(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "legacy-auth.json")
	browsers := make([]string, 66)
	for i := range browsers {
		browsers[i] = fmt.Sprintf(`{"cred":"hcred_browser_%02d"}`, i)
	}
	body := `{"v":1,"browsers":[` + strings.Join(browsers, ",") + `]}`
	if err := os.WriteFile(legacy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := mergeAuth(h, legacy)
	if err != nil {
		t.Fatalf("mergeAuth: %v", err)
	}
	if added != 64 {
		t.Fatalf("added = %d, want the window's sixty-four", added)
	}
	if merged := readAuthState(h.Auth()); len(merged.Browsers) != 64 {
		t.Errorf("the merged browsers = %d, want sixty-four", len(merged.Browsers))
	}
}

func TestDiscoverStopsAtASettingsFileWhateverItNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".agents", "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a settings file with no home key is the project's answer, whole: the walk stops here and
	// finds nothing, because the repo's own home holds no channels
	if err := os.WriteFile(filepath.Join(repo, ".agents", "huddle", "huddle.json"), []byte(`{"port":18860}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Discover(context.Background(), repo); len(got) != 0 {
		t.Fatalf("Discover = %+v, want none: the settings file answers even when it names no home", got)
	}
	// a settings file naming a home that holds no channels names nothing either
	named := filepath.Join(t.TempDir(), "empty-home")
	if err := os.WriteFile(filepath.Join(repo, ".agents", "huddle", "huddle.json"), []byte(fmt.Sprintf(`{"home":%q}`, named)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Discover(context.Background(), repo); len(got) != 0 {
		t.Fatalf("Discover = %+v, want none: the named home holds no channels", got)
	}
	// a tilde home the walk cannot expand names nothing
	if err := os.WriteFile(filepath.Join(repo, ".agents", "huddle", "huddle.json"), []byte(`{"home":"~/.agents/huddle"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	if got := Discover(context.Background(), repo); len(got) != 0 {
		t.Fatalf("Discover = %+v, want none without a home to expand against", got)
	}
}

func TestDiscoverChecksTheMainCheckoutAWorktreeNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	main := filepath.Join(t.TempDir(), "main")
	inner := filepath.Join(main, "more")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	// a .git file that names no worktree marks its own directory the main checkout
	if err := os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: /elsewhere/.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(inner, "deep")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Discover(context.Background(), deep); len(got) != 0 {
		t.Fatalf("Discover = %+v, want none: the main checkout holds no legacy home either", got)
	}
}

func TestImportRefusesAHomeItCannotLayOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "huddle")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// the ledger reads (Ensure made it), but the tree cannot grow: the import stops
	if err := os.WriteFile(filepath.Join(root, "imports.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	source := makeLegacyChannel(t, "acme", "main")
	if _, err := Import(context.Background(), Home{Root: root}, []Source{{Path: source, Label: "acme"}}); err == nil {
		t.Fatal("an import into a home that cannot grow succeeded")
	}
}

func TestImportNamesALedgerItCannotWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := h.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// the temp file the ledger's atomic write picks is taken by a directory
	if err := os.MkdirAll(fmt.Sprintf("%s.%d.tmp", h.Imports(), os.Getpid()), 0o700); err != nil {
		t.Fatal(err)
	}
	source := makeLegacyChannel(t, "acme", "main")
	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 0 || len(report.Failed) != 1 {
		t.Fatalf("the report = %+v, want the source failed", report)
	}
	if entries := readLedgerEntries(t, h); len(entries) != 0 {
		t.Errorf("the ledger recorded %v, want nothing", entries)
	}
}

func TestImportRollsBackOnlyTheFailedSourcesOwnCopies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	source := filepath.Join(repo, ".agents", "huddle")
	channels := filepath.Join(source, "data", "channels")
	if err := os.MkdirAll(channels, 0o700); err != nil {
		t.Fatal(err)
	}
	// the first channel copies, the second is no database: the failure rolls the first's copy
	// back out and names the source, and the legacy folder never moved
	if err := os.WriteFile(filepath.Join(channels, "zzz.db"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedChannelFile(t, filepath.Join(channels, "aaa.db"))
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "repo"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Failed) != 1 || len(report.Imported) != 0 {
		t.Fatalf("the report = %+v, want the source failed", report)
	}
	if _, err := os.Stat(h.ChannelDB("aaa")); !os.IsNotExist(err) {
		t.Errorf("the copy the failure rolled back stayed in the home: %v", err)
	}
	if entries := readLedgerEntries(t, h); len(entries) != 0 {
		t.Errorf("the ledger recorded %v, want nothing", entries)
	}
	if body, err := os.ReadFile(filepath.Join(channels, "zzz.db")); err != nil || string(body) != "junk" {
		t.Errorf("the legacy file changed: %q (%v)", body, err)
	}
}

// seedChannelFile lays one valid channel database at path, closed.
func seedChannelFile(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx, "INSERT INTO tasks (id, title, created_at) VALUES (?,?,?)", "t1", "one task", "2026-10-16T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImportHoldsASourceWhoseBudgetRunsOutMidCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// the legacy server's shutdown ask cancels the entry's budget: the copy stops and the
	// source waits for a later entry
	var cancel context.CancelFunc
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(200)
	}))
	defer fake.Close()
	_, source := legacyRepoHome(t)
	writeLegacyPort(t, source, portOf(t, fake.URL))
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	report, err := Import(ctx, h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	// the budget ran out before the first copy: the source is named as failed and nothing landed
	if len(report.Failed) != 1 || len(report.Imported) != 0 || len(report.Waiting) != 0 {
		t.Fatalf("the report = %+v, want the source failed", report)
	}
	if entries := readLedgerEntries(t, h); len(entries) != 0 {
		t.Errorf("the ledger recorded %v, want nothing", entries)
	}
	names, _ := filepath.Glob(filepath.Join(h.Channels(), "*.db"))
	if len(names) != 0 {
		t.Errorf("channels landed after the budget ran out: %v", names)
	}
}

func TestAskToStopRefusesAnAddressItCannotName(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	source := filepath.Join(repo, ".agents", "huddle")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	// a settings url that is no address at all: the ask gives up and the source imports
	if err := os.WriteFile(filepath.Join(source, "huddle.json"), []byte(`{"url":"://bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !askToStop(context.Background(), source) {
		t.Error("an unnameable address held its source")
	}
}

func TestAppendLineSilencesAFileItCannotOpen(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Channels(), 0o700); err != nil {
		t.Fatal(err)
	}
	// the path is a directory: the open fails and the line is silence
	appendLine(h.Channels(), "never lands")
}

func TestPendingImportNoticesAnswersNoneWithoutAHome(t *testing.T) {
	if got := PendingImportNotices(""); got != nil {
		t.Errorf("notices of no home = %q, want none", got)
	}
}

func TestTheLedgersEmptyImportsListIsFilled(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Imports(), []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := readLedger(h)
	if err != nil {
		t.Fatalf("readLedger: %v", err)
	}
	if ledger.Imports == nil {
		t.Error("a ledger with no imports read as nil, want an empty list")
	}
}

func TestResolveTakesTheEnvironmentWhenGivenNone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HUDDLE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	got, err := Resolve(nil)
	if err != nil || got.Root != home {
		t.Errorf("Resolve(nil) = (%+v, %v), want the environment's home", got, err)
	}
}

func TestEnsureConfigRefusesAConfigItCannotStat(t *testing.T) {
	root := filepath.Join(t.TempDir(), "huddle")
	if err := os.WriteFile(root, []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureConfig(Home{Root: root}); err == nil {
		t.Error("ensureConfig laid a config under a file")
	}
}

func TestLoadConfigFillsAFileThatNamesEmptyKeys(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), "role = \"\"\nmachine = \"\"\n")
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != "solo" || cfg.Machine == "" {
		t.Errorf("the empty keys read = %+v, want the defaults filled in", cfg)
	}
}

func TestSaveCertificateRefusesAPairItCannotLay(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(filepath.Join(h.TLS(), "cert.pem"), 0o700); err != nil {
		t.Fatal(err)
	}
	pair := mintPair(t, time.Hour)
	if err := SaveCertificate(h, pair.certPEM, pair.keyPEM); err == nil {
		t.Error("a certificate laid over a directory")
	}
}

func TestLeafExpiryNamesACertificateItCannotParse(t *testing.T) {
	torn := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")})
	if _, err := leafExpiry(torn); err == nil {
		t.Error("a torn leaf answered an expiry")
	}
}

func TestRenewalTakesItsRealClockAndSleepWhenGivenNone(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	// no certificate: the loop says so and ends before it ever sleeps
	RenewCertificates(context.Background(), h, func(context.Context) (ACME, error) {
		return nil, errors.New("unused")
	}, "hub.test", RenewalOptions{})
	// the real sleep runs its short stretch and answers
	if !sleepStretches(context.Background(), 10*time.Millisecond) {
		t.Error("a sleep the context outlived answered stop")
	}
	// a wait longer than one stretch is cut to the stretch
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepStretches(ctx, 25*time.Hour) {
		t.Error("a sleep over a done context answered keep-going")
	}
}
