package home

// The one-time import (SPEC 4.3): a legacy home — a repo's .agents/huddle with data/channels,
// built here from the recorded home in testdata/legacy_home — moves into the user's home by
// itself. Channels copy with SQLite's backup API under a name a earlier source took gets a
// suffix, auth entries merge by digest, server-wide knowledge merges by id, the state dir's
// credentials copy over, and imports.json records it all so it happens once. The legacy folder
// is only ever read: its file hashes are the same after as before. A legacy server still
// running on the source is asked to stop through its own /api/shutdown path first; one that
// never answers holds its source for a later entry.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// legacyFixture is the recorded home under testdata: its channels and its settings file's shape.
const legacyFixture = "../testdata/legacy_home"

// freePort answers a loopback port nothing holds, away from the routers' 18787-18791.
func freePort(t *testing.T) int {
	t.Helper()
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve a port: %v", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if port < 18787 || port > 18791 {
			return port
		}
	}
}

// copyTree copies one directory tree, files whole.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	info, err := os.Stat(from)
	if err != nil {
		t.Fatalf("stat %s: %v", from, err)
	}
	if err := os.MkdirAll(to, info.Mode().Perm()); err != nil {
		t.Fatalf("mkdir %s: %v", to, err)
	}
	err = filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
}

// hashTree answers every file under root by content hash, slash spelled.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash %s: %v", root, err)
	}
	return out
}

// tableCounts answers one SQLite file's row count per table, the internal tables aside.
func tableCounts(t *testing.T, file string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("list %s tables: %v", file, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan a table name: %v", err)
		}
		names = append(names, name)
	}
	rows.Close()
	sort.Strings(names)
	out := map[string]int{}
	for _, name := range names {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + name).Scan(&n); err != nil {
			t.Fatalf("count %s.%s: %v", file, name, err)
		}
		out[name] = n
	}
	return out
}

// sameCounts says two counts maps are equal, with the difference spelled for the failure.
func sameCounts(want, got map[string]int) string {
	for table, n := range want {
		if got[table] != n {
			return fmt.Sprintf("table %s: %d rows, want %d", table, got[table], n)
		}
	}
	for table := range got {
		if _, ok := want[table]; !ok {
			return fmt.Sprintf("table %s exists only in the copy", table)
		}
	}
	return ""
}

// credBody is one session credential file's body, the shape creds.go writes.
func credBody(url, credential string, root bool) string {
	raw, err := json.Marshal(map[string]any{"url": url, "credential": credential, "root": root})
	if err != nil {
		panic(err)
	}
	return string(raw) + "\n"
}

// legacyRepoHome assembles one legacy repo home from the recorded fixture: <repo>/.agents/huddle
// with the settings file at its top and data/ under it, the settings' port moved to a free port
// nothing listens on. It answers the repo and the source (the legacy home itself).
func legacyRepoHome(t *testing.T) (repo, source string) {
	t.Helper()
	repo = t.TempDir()
	source = filepath.Join(repo, ".agents", "huddle")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join(legacyFixture, "data"), filepath.Join(source, "data"))
	settings := map[string]any{
		"channel": "atelier", "as": "atlas", "autostart": true,
		"port": freePort(t), "port_auto": true,
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "huddle.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, source
}

// legacyState points the import's state dir at a throwaway and fills it with credential files;
// the import copies them into the home and reads the legacy server's credential from them.
func legacyState(t *testing.T, creds map[string]string) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "huddle", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, body := range creds {
		if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

// readLedgerEntries answers the ledger's import entries as parsed JSON; an empty ledger (what
// Ensure writes) holds none.
func readLedgerEntries(t *testing.T, h Home) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(h.Imports())
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var parsed struct {
		Imports []map[string]any `json:"imports"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse the ledger %s: %v", raw, err)
	}
	return parsed.Imports
}

// authJSON reads an auth state file's digest lists.
func authJSON(t *testing.T, path string) (roots, inviteSecrets, memberCreds, browserCreds []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var parsed struct {
		Roots   []string `json:"roots"`
		Invites []struct {
			Secret string `json:"secret"`
		} `json:"invites"`
		Members []struct {
			Cred string `json:"cred"`
		} `json:"members"`
		Browsers []struct {
			Cred string `json:"cred"`
		} `json:"browsers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	roots = append(roots, parsed.Roots...)
	for _, i := range parsed.Invites {
		inviteSecrets = append(inviteSecrets, i.Secret)
	}
	for _, m := range parsed.Members {
		memberCreds = append(memberCreds, m.Cred)
	}
	for _, b := range parsed.Browsers {
		browserCreds = append(browserCreds, b.Cred)
	}
	return
}

// TestImportBringsALegacyHomeInOnce is the card's first case: the recorded home moves in whole,
// the ledger proves it, a second run does nothing, and the legacy folder's bytes never move.
func TestImportBringsALegacyHomeInOnce(t *testing.T) {
	_, source := legacyRepoHome(t)
	legacyState(t, map[string]string{
		"sess-one": credBody("", "hcred_one", false),
	})
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	before := hashTree(t, source)

	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 1 || report.Imported[0].Source != source {
		t.Fatalf("the report = %+v, want one imported %s", report, source)
	}
	// every channel copied, row counts equal per table
	names, err := filepath.Glob(filepath.Join(source, "data", "channels", "*.db"))
	if err != nil || len(names) != 3 {
		t.Fatalf("the fixture holds three channels: %v (%v)", names, err)
	}
	for _, legacy := range names {
		name := strings.TrimSuffix(filepath.Base(legacy), ".db")
		copied := h.ChannelDB(name)
		if _, err := os.Stat(copied); err != nil {
			t.Fatalf("channel %s did not copy: %v", name, err)
		}
		if diff := sameCounts(tableCounts(t, legacy), tableCounts(t, copied)); diff != "" {
			t.Errorf("channel %s: %s", name, diff)
		}
	}
	// the recorded auth entries moved in by digest
	roots, invites, members, browsers := authJSON(t, h.Auth())
	if len(roots) != 1 || len(invites) != 1 || len(members) != 4 || len(browsers) != 2 {
		t.Fatalf("the merged auth = %d roots, %d invites, %d members, %d browsers", len(roots), len(invites), len(members), len(browsers))
	}
	legacyRoots, legacyInvites, legacyMembers, legacyBrowsers := authJSON(t, filepath.Join(source, "data", "auth.json"))
	for i, want := range [][]string{legacyRoots, legacyInvites, legacyMembers, legacyBrowsers} {
		var got []string
		switch i {
		case 0:
			got = roots
		case 1:
			got = invites
		case 2:
			got = members
		case 3:
			got = browsers
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("auth list %d = %v, want %v", i, got, want)
		}
	}
	// server-wide knowledge merged by id
	if got := knowledgeTitles(t, h.Knowledge()); len(got) != 2 {
		t.Errorf("the merged knowledge = %v, want the fixture's two entries", got)
	}
	// the state dir's credentials copied
	cred := filepath.Join(h.Sessions(), "sess-one.json")
	if body, err := os.ReadFile(cred); err != nil || !strings.Contains(string(body), "hcred_one") {
		t.Errorf("the credential copied = %q (%v)", body, err)
	}
	// the ledger records path, targets, sha256 and a time
	entries := readLedgerEntries(t, h)
	if len(entries) != 1 {
		t.Fatalf("the ledger holds %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry["source"] != source {
		t.Errorf("the ledger's source = %v, want %s", entry["source"], source)
	}
	if at, _ := entry["at"].(string); at == "" {
		t.Error("the ledger entry carries no time")
	}
	channels, _ := entry["channels"].([]any)
	if len(channels) != 3 {
		t.Fatalf("the ledger records %d channels, want 3", len(channels))
	}
	for _, one := range channels {
		channel, _ := one.(map[string]any)
		to, _ := channel["to"].(string)
		sum, _ := channel["sha256"].(string)
		gotSum, sumErr := shaOfFile(to)
		if to == "" || sumErr != nil || sum != gotSum {
			t.Errorf("a ledger channel = %v, want a target and its file's hash now", channel)
		}
	}
	// one log line and one notice line for the source
	log := string(readFile(t, h.LogFile()))
	if strings.Count(log, "\n") != 1 || !strings.Contains(log, source) {
		t.Errorf("the log = %q, want one line naming %s", log, source)
	}
	notice := readFile(t, h.ImportNotice())
	if want := "Huddle moved 3 channels from " + source; !strings.HasPrefix(string(notice), want) {
		t.Errorf("the notice = %q, want it to start with %q", notice, want)
	}
	if !strings.Contains(string(notice), "the old folder is untouched") {
		t.Errorf("the notice = %q, want it to say the old folder is untouched", notice)
	}

	// a second run does nothing: the ledger, the channels and the legacy folder all stand still
	ledgerBefore := string(readFile(t, h.Imports()))
	channelHashes := hashTree(t, h.Channels())
	again, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import again: %v", err)
	}
	if len(again.Imported) != 0 {
		t.Errorf("the second run imported %+v, want nothing", again.Imported)
	}
	if string(readFile(t, h.Imports())) != ledgerBefore {
		t.Error("the second run rewrote the ledger")
	}
	if strings.Join(sortedKeys(hashTree(t, h.Channels())), ",") != strings.Join(sortedKeys(channelHashes), ",") {
		t.Error("the second run touched the channels")
	}
	after := hashTree(t, source)
	if strings.Join(sortedKeys(before), ",") != strings.Join(sortedKeys(after), ",") {
		t.Errorf("the legacy folder changed:\nbefore %v\nafter  %v", sortedKeys(before), sortedKeys(after))
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("the legacy file %s changed", path)
		}
	}
}

// sortedKeys answers a map's keys sorted, for one-line comparisons.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// knowledgeTitles answers one knowledge store's titles by id.
func knowledgeTitles(t *testing.T, file string) map[string]string {
	t.Helper()
	if _, err := os.Stat(file); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT id, title FROM knowledge ORDER BY id")
	if err != nil {
		t.Fatalf("read the knowledge: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatalf("scan a knowledge row: %v", err)
		}
		out[strconv.FormatInt(id, 10)] = title
	}
	return out
}

// TestImportRenamesAChannelWhoseNameIsTaken is the card's clash case: two legacy homes with the
// same channel name, and the second takes the repo's basename as a suffix, recorded in the ledger.
func TestImportRenamesAChannelWhoseNameIsTaken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	one := makeLegacyChannel(t, "one-repo", "main")
	two := makeLegacyChannel(t, "two-repo", "main")
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	report, err := Import(context.Background(), h, []Source{{Path: one, Label: "one-repo"}, {Path: two, Label: "two-repo"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 2 {
		t.Fatalf("the report imported %d sources, want 2", len(report.Imported))
	}
	for _, name := range []string{"main", "main-two-repo"} {
		if _, err := os.Stat(h.ChannelDB(name)); err != nil {
			t.Errorf("channel %s did not land: %v", name, err)
		}
	}
	entries := readLedgerEntries(t, h)
	if len(entries) != 2 {
		t.Fatalf("the ledger holds %d entries, want 2", len(entries))
	}
	second, _ := entries[1]["channels"].([]any)
	if len(second) != 1 {
		t.Fatalf("the second ledger entry holds %v, want its one channel", entries[1])
	}
	channel, _ := second[0].(map[string]any)
	if channel["from"] != "main" || !strings.HasSuffix(fmt.Sprint(channel["to"]), "main-two-repo.db") {
		t.Errorf("the recorded rename = %v, want main to main-two-repo.db", channel)
	}
}

// makeLegacyChannel builds one minimal legacy home holding a channel database with one task row.
func makeLegacyChannel(t *testing.T, repo, channel string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), repo, ".agents", "huddle")
	st, err := store.Open(context.Background(), filepath.Join(source, "data", "channels", channel+".db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := st.Run(context.Background(), "INSERT INTO tasks (id, title, created_at) VALUES (?,?,?)", "t1", "one task of "+channel, "2026-10-16T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return source
}

// TestImportAsksALegacyServerToStopFirst is the card's live-server case: a legacy server still
// running on the source is asked to stop through its own /api/shutdown path, with the credential
// the legacy sessions hold, before any database is copied.
func TestImportAsksALegacyServerToStopFirst(t *testing.T) {
	var mu sync.Mutex
	var sawMethod, sawPath, sawToken string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawMethod, sawPath, sawToken = r.Method, r.URL.Path, r.Header.Get("x-huddle-token")
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer fake.Close()
	_, source := legacyRepoHome(t)
	writeLegacyPort(t, source, portOf(t, fake.URL))
	legacyState(t, map[string]string{
		"sess-one": credBody(fake.URL, "hcred_root_of_old", true),
	})
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if _, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if sawMethod != http.MethodPost || sawPath != "/api/shutdown" {
		t.Errorf("the legacy server saw %s %s, want POST /api/shutdown", sawMethod, sawPath)
	}
	if sawToken != "hcred_root_of_old" {
		t.Errorf("the shutdown ask sent %q, want the legacy sessions' root credential", sawToken)
	}
	if _, err := os.Stat(h.ChannelDB("atelier")); err != nil {
		t.Errorf("the channels did not copy after the ask: %v", err)
	}
}

// TestImportWaitsForAServerThatNeverAnswers is the card's refusal case: a legacy server that
// accepts and never answers within its second leaves the source unimported — nothing in the
// ledger — and a retry once it is gone brings the source in.
func TestImportWaitsForAServerThatNeverAnswers(t *testing.T) {
	done := make(chan struct{})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}))
	shut := func() { close(done); fake.Close() }
	defer func() {
		select {
		case <-done:
		default:
			shut()
		}
	}()
	_, source := legacyRepoHome(t)
	writeLegacyPort(t, source, portOf(t, fake.URL))
	legacyState(t, nil)
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	t0 := time.Now()
	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if elapsed := time.Since(t0); elapsed > 5*time.Second {
		t.Errorf("Import waited %s for the mute server", elapsed)
	}
	if len(report.Imported) != 0 || len(report.Waiting) != 1 {
		t.Fatalf("the report = %+v, want the source waiting", report)
	}
	if _, err := os.Stat(h.ChannelDB("atelier")); err == nil {
		t.Error("a channel copied out from under a server that never answered")
	}
	if entries := readLedgerEntries(t, h); len(entries) != 0 {
		t.Fatalf("the ledger recorded %v, want nothing", entries)
	}
	// the server gone, the next entry brings the source in
	shut()
	if _, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}}); err != nil {
		t.Fatalf("Import again: %v", err)
	}
	if _, err := os.Stat(h.ChannelDB("atelier")); err != nil {
		t.Errorf("the retry did not bring the channels in: %v", err)
	}
	if entries := readLedgerEntries(t, h); len(entries) != 1 {
		t.Fatalf("the ledger holds %d entries after the retry, want 1", len(entries))
	}
}

// writeLegacyPort rewrites the legacy settings file's port.
func writeLegacyPort(t *testing.T, source string, port int) {
	t.Helper()
	file := filepath.Join(source, "huddle.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	settings["port"] = port
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// portOf pulls the port out of a loopback url.
func portOf(t *testing.T, url string) int {
	t.Helper()
	parts := strings.Split(url, ":")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("%s carries no port: %v", url, err)
	}
	return port
}

// TestDiscoverFindsTheSettingsHomeKeyAndTheRepoHome pins discovery: the settings file's home key
// names a shared legacy home, else the repo's own .agents/huddle when it still holds channels.
func TestDiscoverFindsTheSettingsHomeKeyAndTheRepoHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	shared := makeLegacyChannel(t, "shared-home", "main")
	repo := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".agents", "huddle"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"channel":"main","as":"api","home":%q}`+"\n", shared)
	if err := os.WriteFile(filepath.Join(repo, ".agents", "huddle", "huddle.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover(context.Background(), repo)
	if len(got) != 1 || got[0].Path != shared {
		t.Fatalf("Discover = %+v, want the settings' shared home %s", got, shared)
	}

	// with no home key, the repo's own .agents/huddle is the source when it holds channels
	own := makeLegacyChannel(t, "plain-repo", "main")
	got = Discover(context.Background(), filepath.Dir(filepath.Dir(own)))
	if len(got) != 1 || got[0].Path != own {
		t.Fatalf("Discover = %+v, want the repo home %s", got, own)
	}
	if got[0].Label != "plain-repo" {
		t.Errorf("the source's label = %q, want the repo's basename", got[0].Label)
	}

	// a project with no legacy home names none
	if got := Discover(context.Background(), t.TempDir()); len(got) != 0 {
		t.Errorf("Discover = %+v, want no source", got)
	}
}

// TestImportAdoptsTheEarlierKnowledgeSpelling pins the rename: a home whose store still sits at
// shared.db keeps its rows when the import merges a legacy home's knowledge into knowledge.db.
func TestImportAdoptsTheEarlierKnowledgeSpelling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// the earlier spelling, holding one entry
	earlier, err := store.Open(context.Background(), filepath.Join(h.Root, "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := earlier.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := earlier.Run(context.Background(),
		"INSERT INTO knowledge (id, by, kind, title, body, tags, refs, created_at) VALUES (?,?,?,?,?,?,?,?)",
		int64(200000), "old", "fact", "kept by the rename", "the row the earlier spelling holds", "[]", "[]", "2026-10-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := earlier.Close(); err != nil {
		t.Fatal(err)
	}
	source := makeLegacyChannel(t, "acme", "main")
	// a legacy shared store with one entry of its own
	legacy, err := store.Open(context.Background(), filepath.Join(source, "data", "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Run(context.Background(),
		"INSERT INTO knowledge (id, by, kind, title, body, tags, refs, created_at) VALUES (?,?,?,?,?,?,?,?)",
		int64(100000), "atlas", "fact", "from the legacy home", "the row the legacy home holds", "[]", "[]", "2026-10-02T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Import(context.Background(), h, []Source{{Path: source, Label: "acme"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 1 || report.Imported[0].Knowledge != 1 {
		t.Fatalf("the report = %+v, want one merged knowledge entry", report)
	}
	if _, err := os.Stat(filepath.Join(h.Root, "shared.db")); !os.IsNotExist(err) {
		t.Errorf("shared.db still sits in the home: %v", err)
	}
	titles := knowledgeTitles(t, h.Knowledge())
	if titles["200000"] != "kept by the rename" || titles["100000"] != "from the legacy home" {
		t.Errorf("the merged knowledge = %v, want both spellings' rows", titles)
	}
}

// TestPendingImportNoticesSaysEachLineOnce pins the notice's life: it waits in run/import-notice
// until a session start reads it, and is never said twice.
func TestPendingImportNoticesSaysEachLineOnce(t *testing.T) {
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := os.MkdirAll(h.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	line := "Huddle moved 2 channels from /old into ~/.config/huddle; the old folder is untouched."
	if err := os.WriteFile(h.ImportNotice(), []byte(line+"\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := PendingImportNotices(h.Root)
	if len(got) != 2 || got[0] != line {
		t.Fatalf("the notices = %q, want the line twice", got)
	}
	if _, err := os.Stat(h.ImportNotice()); !os.IsNotExist(err) {
		t.Errorf("the notice file stayed after it was said: %v", err)
	}
	if again := PendingImportNotices(h.Root); len(again) != 0 {
		t.Errorf("the notices were said again: %q", again)
	}
}
