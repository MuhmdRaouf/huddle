// The credential this Claude session holds for a Huddle server (the auth package), kept per
// session, never the invite it was made from:
//
//	${HUDDLE_SESSIONS_DIR:-<the huddle home>/sessions}/<key>.json   (0600, dir 0700)
//	{"url": "http://127.0.0.1:<port>", "channel": "shop", "as": "api", "credential": "hcred_…", "root": false}
//
// The key is the session: the hook input's session id, else HUDDLE_SESSION, else
// CLAUDE_CODE_SESSION_ID (what Claude Code gives its tools), with an alias by the Claude process
// (CLAUDE_PID) so a /clear, a subagent or the MCP bridge of the same session find it too, and a
// key for the project (project-<sha1 of its main checkout>), which with no session id comes
// before the terminal stand-in: the next session in the same project (a restart, a second
// terminal) finds the project's credential instead of a leftover terminal file of another
// project. Same OS user, same 0600 directory: no new secret on disk.
// HUDDLE_TOKEN overrides it all (a root credential for scripts and tests). A credential is only
// ever sent to the url it was issued by. Reads never fail: no file means no credential.
package cli

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// Cred is the credential a session holds for one Huddle server.
type Cred struct {
	URL        string `json:"url"`
	Channel    string `json:"channel,omitempty"`
	As         string `json:"as,omitempty"`
	Credential string `json:"credential"`
	Root       bool   `json:"root,omitempty"`
}

// SessionsDir answers where session credentials live: HUDDLE_SESSIONS_DIR, else the sessions
// directory of this user's huddle home.
func SessionsDir(w *World) string {
	if dir := w.getenv("HUDDLE_SESSIONS_DIR"); dir != "" {
		return dir
	}
	if h, err := home.Resolve(w.getenv); err == nil {
		return h.Sessions()
	}
	return filepath.Join(userHomeDir(), ".config", "huddle", "sessions")
}

// userHomeDir is the user's home directory.
func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// keyNoise is everything a key may not carry.
var keyNoise = regexp.MustCompile(`[^\w.-]+`)

// cleanKey strips everything a filename must not carry: everything but letters, digits,
// underscore, dot and hyphen.
func cleanKey(key string) string { return keyNoise.ReplaceAllString(key, "") }

// SessionKeys answers the session's keys, most specific first: the given session id, then the
// environment's, then the Claude process's alias, then the project's. A shell outside Claude
// Code has none of those, so the project's key comes first and "terminal" stands in for both.
func SessionKeys(w *World, sid string) []string {
	keys := []string{}
	for _, key := range []string{sid, w.getenv("HUDDLE_SESSION"), w.getenv("CLAUDE_CODE_SESSION_ID")} {
		if key = cleanKey(key); key != "" {
			keys = append(keys, key)
		}
	}
	if pid := cleanKey(w.getenv("CLAUDE_PID")); pid != "" {
		keys = append(keys, "pid-"+pid)
	}
	project := ProjectKey(context.Background(), w)
	if len(keys) == 0 {
		// the project's own credential outranks the terminal's stand-in: a process in a
		// project dir must not act as another project's member against a stale server
		if project != "" {
			keys = append(keys, project)
		}
		keys = append(keys, "terminal")
	} else if project != "" {
		keys = append(keys, project)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// ProjectKey answers the project's key: its main checkout (the repo around the project dir),
// else the project dir itself, hashed; empty outside a project. The dir is resolved first: the
// same checkout reached through two spellings of one path (a symlinked /var, a bind mount, /tmp)
// must hash to one key, or a session of the project cannot see the credential another session
// of it saved.
func ProjectKey(ctx context.Context, w *World) string {
	if w.getenv("HUDDLE_NO_PROJECT_CRED") == "1" {
		return ""
	}
	project := w.getenv("CLAUDE_PROJECT_DIR")
	start := project
	if start == "" {
		start = w.dir()
	}
	var dir string
	if main := MainCheckout(ctx, w, start); main != "" {
		dir = main // every worktree of one repository is one project
	} else {
		dir = project
	}
	if dir == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return "project-" + hashKey(dir)
}

// hashKey is the first 16 hex characters of a dir's SHA-1.
func hashKey(dir string) string {
	sum := sha1.Sum([]byte(dir))
	return hex.EncodeToString(sum[:])[:16]
}

// readCred reads one credential file; nil when it is missing or holds no credential.
func readCred(path string) *Cred {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cred Cred
	if err := json.Unmarshal(raw, &cred); err != nil {
		return nil
	}
	if cred.Credential == "" || cred.URL == "" {
		return nil
	}
	return &cred
}

// LoadCred answers this session's credential, for url when one is given (empty accepts any).
func LoadCred(ctx context.Context, w *World, url, sid string) *Cred {
	for _, key := range SessionKeys(w, sid) {
		cred := readCred(filepath.Join(SessionsDir(w), key+".json"))
		if cred != nil && (url == "" || trimSlash(cred.URL) == trimSlash(url)) {
			return cred
		}
	}
	return nil
}

// SaveCred keeps the credential under every key of this session. Files idle for a week are
// forgotten; a project's stays until it is replaced.
func SaveCred(ctx context.Context, w *World, cred Cred, sid string) error {
	dir := SessionsDir(w)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	cred.URL = trimSlash(cred.URL)
	raw, err := domain.MarshalCompactJSON(cred)
	if err != nil {
		return err
	}
	for _, key := range SessionKeys(w, sid) {
		file := filepath.Join(dir, key+".json")
		tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, file); err != nil {
			return err
		}
	}
	forgetIdle(ctx, w, dir)
	return nil
}

// forgetIdle removes session files idle for a week; a project's file stays.
func forgetIdle(ctx context.Context, w *World, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "project-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if w.now().Sub(info.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// ForgetCred drops this session's credential files.
func ForgetCred(ctx context.Context, w *World, sid string) {
	for _, key := range SessionKeys(w, sid) {
		_ = os.Remove(filepath.Join(SessionsDir(w), key+".json"))
	}
}

// ProjectCred answers the credential kept under another project's key: setup joins a Huddle this
// user already runs. dir is resolved first, as ProjectKey resolves it.
func ProjectCred(ctx context.Context, w *World, dir, url string) *Cred {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	cred := readCred(filepath.Join(SessionsDir(w), "project-"+hashKey(dir)+".json"))
	if cred == nil || (url != "" && trimSlash(cred.URL) != trimSlash(url)) {
		return nil
	}
	return cred
}

// TokenFor answers the credential to send to url: HUDDLE_TOKEN, else this session's for that
// url, else this home's own server's owner key, else none.
func TokenFor(ctx context.Context, w *World, url, sid string) string {
	if token := w.getenv("HUDDLE_TOKEN"); token != "" {
		return token
	}
	if cred := LoadCred(ctx, w, url, sid); cred != nil {
		return cred.Credential
	}
	return ownerKeyFor(w, url)
}

// ownerKeyFor answers the root credential this home's own server leaves for this machine's
// owner (run/owner.key, ownerkey.go on the server side), or "": only for the loopback address
// the home itself names, so one home's key never vouches for another server and the key never
// leaves the machine. A file another user owns, or one group or others may read, is refused
// with one line saying why; a missing one is the common case and says nothing.
func ownerKeyFor(w *World, rawURL string) string {
	h, err := home.Resolve(w.getenv)
	if err != nil {
		return ""
	}
	if !ownLoopback(rawURL, h.Root) {
		return ""
	}
	file := h.OwnerKey()
	info, err := w.fileStat(file)
	if err != nil {
		return ""
	}
	if uid, ok := uidOf(info); !ok || uid != uint32(os.Getuid()) {
		sayOwnerKeyRefused(file, "another user owns it")
		return ""
	}
	if info.Mode().Perm()&0o077 != 0 {
		sayOwnerKeyRefused(file, "group or others may read it")
		return ""
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ownLoopback answers whether rawURL is the loopback address this home's own server answers
// on: the port its home names, in the run directory's port file (what `huddle up` wrote) or
// saved in its config.toml (what a serve with no port named saves itself).
func ownLoopback(rawURL, root string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "127.0.0.1", "localhost":
	default:
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port <= 0 {
		return false
	}
	if savedRunPort(root) == port {
		return true
	}
	if choice, has := SavedPort(root); has && choice.Port == port {
		return true
	}
	return false
}

// uidOf answers the user that owns info's file. A stat without a Stat_t (a test's fake) answers
// nothing, and the caller treats the file as not this user's.
func uidOf(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

// sayOwnerKeyRefused says on stderr why the owner key was not used: the machine's owner is the
// one person who can fix it.
func sayOwnerKeyRefused(file, why string) {
	fmt.Fprintf(os.Stderr, "huddle: owner key %s: %s: not used\n", file, why)
}

// HFetch does one HTTP call to a Huddle server with this session's credential in the header. A
// 401 reads the credential again and retries once (another process of this session may have
// just joined). timeout bounds the whole call; 0 leaves it open for a long-poll or a live
// stream. The caller closes the response's body.
func HFetch(ctx context.Context, w *World, method, rawURL string, headers map[string]string, body []byte, timeout time.Duration, sid string) (*http.Response, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	origin := base.Scheme + "://" + base.Host
	issue := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if token != "" {
			req.Header.Set(auth.Header, token)
		}
		// the server refuses a write that names no action: the route's path is its
		// name, so every caller gets it for free
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions &&
			req.Header.Get(server.ActionHeader) == "" {
			req.Header.Set(server.ActionHeader, base.Path)
		}
		client := &http.Client{}
		if timeout > 0 {
			client.Timeout = timeout
		}
		return client.Do(req)
	}
	token := TokenFor(ctx, w, origin, sid)
	resp, err := issue(token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	again := TokenFor(ctx, w, origin, sid)
	if again == "" || again == token {
		// the saved credential aged out (the server restarted more roots than it keeps
		// digests of): this machine's owner key is the one credential left
		again = ownerKeyFor(w, origin)
	}
	if again == "" || again == token {
		return resp, nil
	}
	resp.Body.Close() // the retry opens its own; this one is done
	return issue(again)
}
