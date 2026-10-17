// The one-time import (SPEC 4.3): today's users have homes at <repo>/.agents/huddle (or a
// shared one the settings file's home key names) and credentials under
// ${XDG_STATE_HOME:-~/.local/state}/huddle. Every huddle entry brings them into the user's home
// before doing anything else, inside its budget. The import copies, never moves: each channel
// database through SQLite's backup API (a name taken becomes <name>-<repo basename>), auth
// entries merge by digest, server-wide knowledge merges by id, the state dir's credentials copy
// into sessions/. imports.json records each source — path, targets, sha256 of each copied file,
// the time — so it happens once, and the legacy folders are left untouched. A legacy server
// still running on a source is asked to stop through its own /api/shutdown path first; one that
// never answers holds its source for a later entry.

package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Source is one legacy home to import: its path (the directory data/channels sits under) and
// the label a channel name taken by an earlier source takes as its suffix — the repo's basename.
type Source struct {
	Path  string
	Label string
}

// CopiedFile is one file the import copied: its name in the source, its target path in the home,
// and the target's sha256 after the copy.
type CopiedFile struct {
	From   string `json:"from"`
	To     string `json:"to"`
	SHA256 string `json:"sha256"`
}

// Imported is one source's record in the ledger: when it moved in, which channels landed under
// which names, how many auth entries and knowledge rows merged, and the credentials copied.
type Imported struct {
	Source      string       `json:"source"`
	At          string       `json:"at"`
	Channels    []CopiedFile `json:"channels"`
	Auth        int          `json:"auth"`
	Knowledge   int          `json:"knowledge"`
	Credentials []CopiedFile `json:"credentials"`
}

// Report is what one Import did: the sources that moved in, the ones a legacy server still
// holds for a later entry, and the ones that failed and why.
type Report struct {
	Imported []Imported `json:"imported"`
	Waiting  []string   `json:"waiting,omitempty"`
	Failed   []string   `json:"failed,omitempty"`
}

// LedgerError names an import ledger that cannot be read: a torn ledger never re-imports what
// it already recorded, so the error stops the import instead.
type LedgerError struct{ Path string }

func (e LedgerError) Error() string {
	return "huddle: the import ledger " + e.Path + " cannot be read"
}

// channelNameRe is a channel's name, the channel package's own shape: a lowercase letter or
// digit, then lowercase letters, digits and dashes, 40 characters at most. The pattern lives
// here too because the import cannot import the channel package.
var channelNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// settingsRels are the settings file's spellings, nearest first.
var settingsRels = []string{filepath.Join(".agents", "huddle", "huddle.json"), filepath.Join(".agents", ".huddle.json")}

// gitdirLine reads the "gitdir: <path>" line of a worktree's .git file.
var gitdirLine = regexp.MustCompile(`(?m)^gitdir:\s*(.+)$`)

// Discover answers the legacy homes projectDir names: the settings file's home key when it
// names one, else the repo's own .agents/huddle while it still holds channels. The walk mirrors
// the CLI's settings walk — nearest file up from the project dir, then the main checkout a
// worktree's .git names — because the same file decides both.
func Discover(ctx context.Context, projectDir string) []Source {
	out := []Source{}
	seen := map[string]bool{}
	add := func(path, label string) {
		if path == "" || seen[path] {
			return
		}
		if !dirExists(filepath.Join(path, "data", "channels")) {
			return
		}
		seen[path] = true
		out = append(out, Source{Path: path, Label: label})
	}
	check := func(dir string) bool {
		add(filepath.Join(dir, ".agents", "huddle"), filepath.Base(dir))
		for _, rel := range settingsRels {
			file := filepath.Join(dir, rel)
			if _, err := os.Stat(file); err != nil {
				continue
			}
			if shared := settingsHome(file); shared != "" {
				add(shared, filepath.Base(shared))
			}
			return true // a settings file here is the project's answer, whole or torn
		}
		return false
	}
	for dir := projectDir; ; dir = filepath.Dir(dir) {
		if check(dir) {
			return out
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if main := mainCheckout(projectDir); main != "" && main != projectDir {
		check(main)
	}
	return out
}

// settingsHome answers the home key a settings file names, ~ expanded; empty when it names none.
func settingsHome(file string) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var parsed struct {
		Home string `json:"home"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	given := strings.TrimSpace(parsed.Home)
	if given == "" {
		return ""
	}
	if given == "~" || strings.HasPrefix(given, "~/") {
		user, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		given = filepath.Join(user, strings.TrimPrefix(given, "~"))
	}
	return filepath.Clean(given)
}

// mainCheckout answers the main checkout of the git repo around dir, from the files: a .git
// directory marks the main checkout, a .git file names it. The walk is the CLI's own, spelled
// here because the import cannot import it.
func mainCheckout(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		info, err := os.Stat(filepath.Join(d, ".git"))
		if err == nil {
			if info.IsDir() {
				return d
			}
			if raw, err := os.ReadFile(filepath.Join(d, ".git")); err == nil {
				if match := gitdirLine.FindStringSubmatch(string(raw)); match != nil {
					if at := strings.LastIndex(match[1], "/.git/worktrees/"); at > 0 {
						return match[1][:at]
					}
				}
			}
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// Import brings the sources into h once, in order: each source still missing from imports.json
// moves in whole (a legacy server that never answers holds it for a later entry), the ledger
// grows by one entry per source, and one log line and one notice line note it. Nothing pending
// answers at once and writes nothing. The legacy folders are only ever read.
func Import(ctx context.Context, h Home, sources []Source) (Report, error) {
	ledger, err := readLedger(h)
	if err != nil {
		return Report{}, err
	}
	done := map[string]bool{}
	for _, rec := range ledger.Imports {
		done[rec.Source] = true
	}
	var pending []Source
	for _, src := range sources {
		if src.Path != "" && !done[src.Path] {
			pending = append(pending, src)
		}
	}
	if len(pending) == 0 {
		return Report{}, nil
	}
	if err := h.Ensure(ctx); err != nil {
		return Report{}, err
	}
	report := Report{}
	taken := takenNames(h)
	for _, src := range pending {
		if ctx.Err() != nil {
			report.Waiting = append(report.Waiting, src.Path)
			continue
		}
		if !askToStop(ctx, src.Path) {
			report.Waiting = append(report.Waiting, src.Path)
			continue
		}
		rec, failure := importOne(ctx, h, src, taken)
		if failure != "" {
			report.Failed = append(report.Failed, src.Path+": "+failure)
			continue
		}
		ledger.Imports = append(ledger.Imports, rec)
		if err := writeLedger(h, ledger); err != nil {
			report.Failed = append(report.Failed, src.Path+": "+err.Error())
			continue
		}
		report.Imported = append(report.Imported, rec)
		noteImportLog(h, &rec)
		writeNotice(h, &rec)
	}
	return report, nil
}

// importOne brings one source in. A failure rolls the channels it just copied back out and
// answers the reason, so a later entry retries the source from its start; the merges and the
// credential copies are idempotent (digest, id, file name), so a retry converges.
func importOne(ctx context.Context, h Home, src Source, taken map[string]bool) (rec Imported, failure string) {
	rec = Imported{Source: src.Path, At: jsfmt.ISOTime(time.Now())}
	created := []string{}
	rollback := func(reason string) (Imported, string) {
		for _, name := range created {
			_ = os.Remove(h.ChannelDB(name))
		}
		return Imported{}, reason
	}
	names, err := channelNames(src.Path)
	if err != nil {
		return rollback(err.Error())
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return rollback("the entry's budget ran out")
		}
		target := claimName(name, src.Label, taken)
		if err := store.BackupFile(ctx, filepath.Join(src.Path, "data", "channels", name+".db"), h.ChannelDB(target)); err != nil {
			return rollback(err.Error())
		}
		created = append(created, target)
		sum, err := shaOfFile(h.ChannelDB(target))
		if err != nil {
			return rollback(err.Error())
		}
		rec.Channels = append(rec.Channels, CopiedFile{From: name, To: h.ChannelDB(target), SHA256: sum})
	}
	added, err := mergeAuth(h, filepath.Join(src.Path, "data", "auth.json"))
	if err != nil {
		return rollback(err.Error())
	}
	rec.Auth = added
	added, err = mergeKnowledge(ctx, h, filepath.Join(src.Path, "data", "shared.db"))
	if err != nil {
		return rollback(err.Error())
	}
	rec.Knowledge = added
	creds, err := copyCredentials(h)
	if err != nil {
		return rollback(err.Error())
	}
	rec.Credentials = creds
	return rec, ""
}

// channelNames lists a source's channel databases, sorted, in the channel name's own shape.
func channelNames(source string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(source, "data", "channels"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		if name := strings.TrimSuffix(entry.Name(), ".db"); channelNameRe.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// takenNames answers the channel names the home already holds.
func takenNames(h Home) map[string]bool {
	taken := map[string]bool{}
	entries, err := os.ReadDir(h.Channels())
	if err != nil {
		return taken
	}
	for _, entry := range entries {
		if name := strings.TrimSuffix(entry.Name(), ".db"); strings.HasSuffix(entry.Name(), ".db") && channelNameRe.MatchString(name) {
			taken[name] = true
		}
	}
	return taken
}

// slugNoise is everything a channel name's suffix may not carry.
var slugNoise = regexp.MustCompile(`[^a-z0-9]+`)

// slug answers a channel-safe spelling of label, at most max characters; "x" when nothing is
// left (the replacement leaves ascii only, so the cut is on a character boundary).
func slug(label string, max int) string {
	if max < 1 {
		max = 1
	}
	out := strings.Trim(slugNoise.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if out == "" {
		return "x"
	}
	if len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	if out == "" {
		return "x"
	}
	return out
}

// claimName answers the target name for one legacy channel: its own when free, else the label
// and then a counter appended until the name is free and still a channel's name.
func claimName(name, label string, taken map[string]bool) string {
	if !taken[name] {
		taken[name] = true
		return name
	}
	base := fitChannel(name + "-" + slug(label, 34))
	if !taken[base] {
		taken[base] = true
		return base
	}
	for i := 2; ; i++ {
		digits := strconv.Itoa(i)
		head := base
		if room := 40 - len(digits) - 1; len(head) > room {
			head = strings.TrimRight(head[:room], "-")
		}
		if head == "" {
			head = "x"
		}
		if candidate := head + "-" + digits; !taken[candidate] {
			taken[candidate] = true
			return candidate
		}
	}
}

// fitChannel cuts one candidate to a channel's name shape: at most 40 characters, no dash at
// either end, never empty.
func fitChannel(candidate string) string {
	if len(candidate) > 40 {
		candidate = candidate[:40]
	}
	candidate = strings.Trim(candidate, "-")
	if candidate == "" {
		return "x"
	}
	return candidate
}

// askToStop asks a legacy server still running on the source to stop through its own
// /api/shutdown path, with the credential the legacy sessions hold for its address. False
// means it never answered within its second: the source waits for a later entry. An address
// nothing answers is fine — nothing runs there.
func askToStop(ctx context.Context, source string) bool {
	addr := legacyAddress(source)
	if addr == "" {
		return true
	}
	askCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(askCtx, http.MethodPost, addr+"/api/shutdown", strings.NewReader("{}"))
	if err != nil {
		return true
	}
	// a guarded server takes only JSON writes that name their action
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-huddle-action", "/api/shutdown")
	if token := legacyCredentialFor(addr); token != "" {
		req.Header.Set("x-huddle-token", token)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return false // it accepted and never answered
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		return true // refused or unreachable: no server holds the source
	}
	_ = resp.Body.Close()
	return true // answered, whatever it said: the backup copy is consistent either way
}

// legacyAddress answers where a legacy home's server answered, from its own settings file: the
// url it names, else its saved port on loopback. Empty when it names neither.
func legacyAddress(source string) string {
	raw, err := os.ReadFile(filepath.Join(source, "huddle.json"))
	if err != nil {
		return ""
	}
	var parsed struct {
		URL  string `json:"url"`
		Port any    `json:"port"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	if url := strings.TrimSuffix(strings.TrimSpace(parsed.URL), "/"); url != "" {
		return url
	}
	if port, ok := parsed.Port.(float64); ok && port == float64(int(port)) && port > 0 && port < 65536 {
		return "http://127.0.0.1:" + strconv.Itoa(int(port))
	}
	return ""
}

// legacyCredentialFor answers the credential the legacy sessions hold for addr, the root one
// when there is it. Empty when no session file names the address.
func legacyCredentialFor(addr string) string {
	entries, err := os.ReadDir(legacySessionsDir())
	if err != nil {
		return ""
	}
	first, root := "", ""
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !entry.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(legacySessionsDir(), entry.Name()))
		if err != nil {
			continue
		}
		var cred struct {
			URL        string `json:"url"`
			Credential string `json:"credential"`
			Root       bool   `json:"root"`
		}
		if json.Unmarshal(raw, &cred) != nil || cred.Credential == "" {
			continue
		}
		if strings.TrimSuffix(cred.URL, "/") != addr {
			continue
		}
		if cred.Root && root == "" {
			root = cred.Credential
		}
		if first == "" {
			first = cred.Credential
		}
	}
	if root != "" {
		return root
	}
	return first
}

// legacySessionsDir is where the legacy releases kept the sessions' credentials:
// ${XDG_STATE_HOME:-~/.local/state}/huddle/sessions.
func legacySessionsDir() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		user := os.Getenv("HOME")
		if user == "" {
			if home, err := os.UserHomeDir(); err == nil {
				user = home
			}
		}
		state = filepath.Join(user, ".local", "state")
	}
	return filepath.Join(state, "huddle", "sessions")
}

// mergeAuth merges a legacy auth state into the home's by digest: roots, invites, members and
// browsers whose digest is not already there move in, keeping the home's own entries first and
// the auth package's own windows (8 roots, 64 browsers). It answers how many entries moved in.
func mergeAuth(h Home, legacy string) (int, error) {
	if !fileExists(legacy) {
		return 0, nil
	}
	state := readAuthState(h.Auth())
	add := readAuthState(legacy)
	merged := authState{V: 1, Note: state.Note, Roots: state.Roots, Invites: state.Invites,
		Issued: state.Issued, Members: state.Members, Browsers: state.Browsers}
	if merged.Note == "" {
		merged.Note = add.Note
	}
	added := 0
	known := map[string]bool{}
	for _, r := range merged.Roots {
		known[r] = true
	}
	for _, r := range add.Roots {
		if len(merged.Roots) >= 8 {
			break
		}
		if !known[r] {
			known[r] = true
			merged.Roots = append(merged.Roots, r)
			added++
		}
	}
	known = map[string]bool{}
	for _, i := range merged.Invites {
		known[i.Secret] = true
	}
	for _, i := range add.Invites {
		if !known[i.Secret] {
			known[i.Secret] = true
			merged.Invites = append(merged.Invites, i)
			added++
		}
	}
	known = map[string]bool{}
	for _, i := range merged.Issued {
		known[i] = true
	}
	for _, i := range add.Issued {
		if len(merged.Issued) >= 1000 {
			break
		}
		if !known[i] {
			known[i] = true
			merged.Issued = append(merged.Issued, i)
			added++
		}
	}
	known = map[string]bool{}
	for _, m := range merged.Members {
		known[m.Cred] = true
	}
	for _, m := range add.Members {
		if !known[m.Cred] {
			known[m.Cred] = true
			merged.Members = append(merged.Members, m)
			added++
		}
	}
	known = map[string]bool{}
	for _, b := range merged.Browsers {
		known[b.Cred] = true
	}
	for _, b := range add.Browsers {
		if len(merged.Browsers) >= 64 {
			break
		}
		if !known[b.Cred] {
			known[b.Cred] = true
			merged.Browsers = append(merged.Browsers, b)
			added++
		}
	}
	if added == 0 && fileExists(h.Auth()) {
		return 0, nil
	}
	body, err := domain.MarshalCompactJSON(&merged)
	if err != nil {
		return 0, err
	}
	return added, writeFile0600(h.Auth(), body)
}

// authState mirrors the auth package's state file: the field order is the JSON's key order, so
// a merged file reads the way the server's own does.
type authState struct {
	V        int           `json:"v"`
	Note     string        `json:"note"`
	Roots    []string      `json:"roots"`
	Invites  []authInvite  `json:"invites"`
	Issued   []string      `json:"issued"`
	Members  []authMember  `json:"members"`
	Browsers []authBrowser `json:"browsers"`
}

type authInvite struct {
	ID          string `json:"id"`
	Secret      string `json:"secret"`
	Created     int64  `json:"created"`
	Expires     *int64 `json:"expires"`
	Single      bool   `json:"single"`
	Uses        int    `json:"uses"`
	Invite      bool   `json:"invite"`
	Channel     string `json:"channel,omitempty"`
	Description string `json:"description,omitempty"`
	By          string `json:"by"`
}

type authMember struct {
	Name   string `json:"name"`
	Cred   string `json:"cred"`
	Since  int64  `json:"since"`
	Seen   int64  `json:"seen"`
	Invite bool   `json:"invite"`
	Via    string `json:"via"`
}

type authBrowser struct {
	Cred string  `json:"cred"`
	By   *string `json:"by"`
}

// readAuthState reads one auth state file; anything unreadable is an empty state, so a torn
// legacy file keeps the rest of the import going.
func readAuthState(path string) authState {
	state := authState{V: 1, Roots: []string{}, Invites: []authInvite{}, Issued: []string{}, Members: []authMember{}, Browsers: []authBrowser{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(raw, &state)
	if state.Roots == nil {
		state.Roots = []string{}
	}
	if state.Invites == nil {
		state.Invites = []authInvite{}
	}
	if state.Issued == nil {
		state.Issued = []string{}
	}
	if state.Members == nil {
		state.Members = []authMember{}
	}
	if state.Browsers == nil {
		state.Browsers = []authBrowser{}
	}
	return state
}

// knowledgeColumns is the knowledge table's column list, the store's own spelling.
var knowledgeColumns = []string{
	"id", "by", "kind", "title", "body", "tags", "refs", "task_id", "supersedes",
	"created_at", "hits", "verified_by", "verified_at", "moved_to", "origin",
}

// mergeKnowledge merges a legacy shared store's server-wide knowledge into the home's
// knowledge.db by id, through the target's own search index. It answers how many rows moved in.
func mergeKnowledge(ctx context.Context, h Home, legacy string) (int, error) {
	if !fileExists(legacy) {
		return 0, nil
	}
	info, err := store.ReadAll(ctx, legacy, "PRAGMA table_info(knowledge)")
	if err != nil {
		return 0, err
	}
	columns := map[string]bool{}
	for _, r := range info {
		if name, ok := r["name"].(string); ok {
			columns[name] = true
		}
	}
	kept := []string{}
	for _, c := range knowledgeColumns {
		if columns[c] {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return 0, nil // the legacy store holds no knowledge table
	}
	rows, err := store.ReadAll(ctx, legacy, "SELECT "+strings.Join(kept, ",")+" FROM knowledge ORDER BY id")
	if err != nil {
		return 0, err
	}
	target, err := store.Open(ctx, KnowledgeFile(h.Root))
	if err != nil {
		return 0, err
	}
	defer target.Close()
	if err := target.Init(ctx); err != nil {
		return 0, err
	}
	added := 0
	for _, row := range rows {
		id, _ := row["id"].(int64)
		held, err := target.Get(ctx, "SELECT id FROM knowledge WHERE id=?", id)
		if err != nil {
			return 0, err
		}
		if held != nil {
			continue
		}
		names := make([]string, 0, len(kept))
		values := make([]any, 0, len(kept))
		for _, c := range kept {
			names = append(names, c)
			values = append(values, row[c])
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
		if err := target.Run(ctx, "INSERT INTO knowledge ("+strings.Join(names, ",")+") VALUES ("+placeholders+")", values...); err != nil {
			return 0, err
		}
		if err := target.IndexKnowledge(ctx, target, row, tagWords(row["tags"])); err != nil {
			return 0, err
		}
		added++
	}
	return added, nil
}

// tagWords answers one row's tags as words, for the search index.
func tagWords(v any) []string {
	raw, ok := v.(string)
	if !ok || raw == "" {
		return nil
	}
	var tags []string
	if json.Unmarshal([]byte(raw), &tags) != nil {
		return nil
	}
	return tags
}

// copyCredentials copies the state dir's session credentials into the home's sessions
// directory, a file the home already holds stays as it is. It answers what it copied.
func copyCredentials(h Home) ([]CopiedFile, error) {
	dir := legacySessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []CopiedFile{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
			continue
		}
		target := filepath.Join(h.Sessions(), name)
		if fileExists(target) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if err := writeFile0600(target, body); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out = append(out, CopiedFile{From: name, To: target, SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// ledgerFile is imports.json's shape.
type ledgerFile struct {
	V       int        `json:"v"`
	Imports []Imported `json:"imports"`
}

// readLedger answers the home's import ledger; an empty file (what Ensure writes) is none yet.
func readLedger(h Home) (ledgerFile, error) {
	raw, err := os.ReadFile(h.Imports())
	if err != nil {
		if os.IsNotExist(err) {
			return ledgerFile{V: 1, Imports: []Imported{}}, nil
		}
		return ledgerFile{}, LedgerError{Path: h.Imports()}
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return ledgerFile{V: 1, Imports: []Imported{}}, nil
	}
	var parsed ledgerFile
	if json.Unmarshal(raw, &parsed) != nil || parsed.V != 1 {
		return ledgerFile{}, LedgerError{Path: h.Imports()}
	}
	if parsed.Imports == nil {
		parsed.Imports = []Imported{}
	}
	return parsed, nil
}

// writeLedger writes the ledger back atomically and private.
func writeLedger(h Home, ledger ledgerFile) error {
	ledger.V = 1
	if ledger.Imports == nil {
		ledger.Imports = []Imported{}
	}
	body, err := domain.MarshalIndentJSON(ledger)
	if err != nil {
		return err
	}
	return writeFile0600(h.Imports(), body)
}

// noteImportLog appends one line to the home's run log for a source that moved in.
func noteImportLog(h Home, rec *Imported) {
	appendLine(h.LogFile(), fmt.Sprintf("%s imported %d channels from %s (auth %d, knowledge %d, credentials %d)",
		jsfmt.ISOTime(time.Now()), len(rec.Channels), rec.Source, rec.Auth, rec.Knowledge, len(rec.Credentials)))
}

// writeNotice appends the line the next session start says once.
func writeNotice(h Home, rec *Imported) {
	appendLine(h.ImportNotice(), fmt.Sprintf("Huddle moved %d channels from %s into %s; the old folder is untouched.",
		len(rec.Channels), rec.Source, displayHome(h.Root)))
}

// appendLine adds one line to a run file, creating its directory, private; a failure is silence.
func appendLine(path, line string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintln(file, line)
}

// PendingImportNotices answers the import notices waiting in a home and drops the file they
// waited in: each is said once, by the next session start. A home that cannot be named has
// none — an empty root would read a path relative to the working directory.
func PendingImportNotices(root string) []string {
	if root == "" {
		return nil
	}
	file := Home{Root: root}.ImportNotice()
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	_ = os.Remove(file)
	out := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// displayHome spells a home the way a person reads it: under the user's home, with ~.
func displayHome(root string) string {
	user := os.Getenv("HOME")
	if user != "" && (root == user || strings.HasPrefix(root, user+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(root, user)
	}
	return root
}

// shaOfFile answers one file's sha256 as hex.
func shaOfFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// fileExists says a regular file is there.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// dirExists says a directory is there.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
