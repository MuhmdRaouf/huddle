package main

// The fresh-install e2e: the committed plugin, copied to a temp dir the way Claude Code installs it,
// on a PATH holding no JavaScript runtime — the plugin's only dependency is the huddle binary its own
// launcher downloads. An httptest server stands in for the release, serving the four binaries the
// release build produces and their SHA256SUMS, and the copy's launcher is re-pinned to those builds'
// digests, so the launcher's checksum table decides what installs on any commit. The kanban word
// downloads the binary, verifies it against its pinned checksum and runs; the radar word answers over
// the same install, and a tampered asset is refused. Every home and state dir is the test's own;
// nothing here touches the network.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// freshRoot is the repository root, two levels above this package's directory.
const freshRoot = "../.."

// freshAsset is one entry of the committed release manifest.
type freshAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// freshRelease is the release the launcher pins: its version, tag, and every target's asset and digest.
type freshRelease struct {
	version string
	tag     string
	assets  map[string]freshAsset
}

// freshReleaseOf reads the root VERSION and the committed manifest that pins it.
func freshReleaseOf(t *testing.T) freshRelease {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(freshRoot, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		t.Fatal("VERSION is empty")
	}
	path := filepath.Join(freshRoot, "release", "v"+version+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the release manifest: %v", err)
	}
	var manifest struct {
		Version string                `json:"version"`
		Tag     string                `json:"tag"`
		Assets  map[string]freshAsset `json:"assets"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if manifest.Version != version {
		t.Fatalf("the manifest pins version %q, want %q", manifest.Version, version)
	}
	if manifest.Tag != "v"+version {
		t.Fatalf("the manifest tags %q, want v%s", manifest.Tag, version)
	}
	if len(manifest.Assets) != 4 {
		t.Fatalf("the manifest pins %d assets, want the four release targets", len(manifest.Assets))
	}
	return freshRelease{version: version, tag: manifest.Tag, assets: manifest.Assets}
}

// freshBuild builds the release binary for one target with the release build's exact flags and
// environment: the bytes a release of these sources would ship. The test pins the launcher's
// checksum table to these builds, so the install flow is exercised on any commit and a stale
// manifest never fails it; `go run ./scripts/build -manifest -check` is the check that the
// committed manifest still matches the sources.
func freshBuild(t *testing.T, release freshRelease, key string) string {
	t.Helper()
	asset, ok := release.assets[key]
	if !ok {
		t.Fatalf("the committed manifest pins no %s asset", key)
	}
	goos, goarch, _ := strings.Cut(key, "-")
	out := filepath.Join(t.TempDir(), asset.Name)
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-s -w -buildid= -X main.version="+release.version,
		"-o", out, "./cmd/huddle")
	build.Dir = freshRoot
	// Build-only environment: the toolchain's own variables (HOME, GOCACHE, GOPATH) stay, so the
	// release bytes match the committed manifest; this env never runs huddle itself.
	build.Env = freshBuildEnv(os.Environ(), goos, goarch)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the release binary for %s: %v\n%s", key, err, output)
	}
	return out
}

// freshPinBuilds replaces every committed target digest in the copied launcher's checksum table
// with the digest of the test's build for that target, so the launcher verifies what this test
// serves. Each committed digest must appear in the launcher exactly once.
func freshPinBuilds(t *testing.T, release freshRelease, plugin string, built map[string]string) {
	t.Helper()
	path := filepath.Join(plugin, "dist", "run")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the copied launcher: %v", err)
	}
	script := string(data)
	for _, key := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"} {
		committed := release.assets[key].SHA256
		if n := strings.Count(script, committed); n != 1 {
			t.Fatalf("the committed %s digest appears %d times in the launcher, want once", key, n)
		}
		script = strings.Replace(script, committed, freshSHA256(t, built[key]), 1)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("re-pin the copied launcher: %v", err)
	}
}

// freshBuildEnv pins the release build's environment, so the built bytes depend on nothing
// but the sources, the version and the target.
func freshBuildEnv(environ []string, goos, goarch string) []string {
	pinned := []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch, "GOFLAGS="}
	env := make([]string, 0, len(environ)+len(pinned))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "CGO_ENABLED", "GOOS", "GOARCH", "GOFLAGS":
		default:
			env = append(env, entry)
		}
	}
	return append(env, pinned...)
}

// freshSHA256 answers the file's sha256 as lowercase hex.
func freshSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// freshSums renders the checksum file in the sha256sum format — "<hex>  <asset>" per line — sorted
// by asset name, the way the release build writes it.
func freshSums(t *testing.T, release freshRelease, built map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(built))
	for key := range built {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s  %s\n", freshSHA256(t, built[key]), release.assets[key].Name))
	}
	return strings.Join(lines, "")
}

// freshCopyPlugin copies the committed plugin dir to a temp one, modes intact: the
// self-contained copy Claude Code installs.
func freshCopyPlugin(t *testing.T) string {
	t.Helper()
	src := filepath.Join(freshRoot, "plugin")
	dst := filepath.Join(t.TempDir(), "plugin")
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy the plugin: %v", err)
	}
	return dst
}

// freshWorld is one temp machine: a home, the plugin copy, a home for the binary and a PATH holding
// only the POSIX tools the launcher needs — never a JavaScript runtime, which the plugin must not
// depend on.
type freshWorld struct {
	home    string
	plugin  string
	binHome string
	path    string
}

// newFreshWorld builds the world.
func newFreshWorld(t *testing.T, plugin string) *freshWorld {
	t.Helper()
	home := t.TempDir()
	world := &freshWorld{
		home:    home,
		plugin:  plugin,
		binHome: filepath.Join(home, "huddle-bin"),
	}
	// the launcher downloads with the first of these the machine holds
	checksums := []string{"sha256sum", "shasum", "openssl"}
	base := []string{"sh", "uname", "dirname", "mkdir", "rm", "mv", "chmod", "date", "kill", "sleep", "curl"}
	bin := t.TempDir()
	linked := 0
	for _, name := range append(append([]string{}, base...), checksums...) {
		resolved, err := exec.LookPath(name)
		if err != nil {
			if freshHas(base, name) {
				t.Fatalf("the launcher's PATH needs %s: %v", name, err)
			}
			continue
		}
		if err := os.Symlink(resolved, filepath.Join(bin, name)); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
		if freshHas(checksums, name) {
			linked++
		}
	}
	if linked == 0 {
		t.Fatal("the launcher's PATH needs one of sha256sum, shasum or openssl to verify the download")
	}
	world.path = bin
	return world
}

// freshHas answers whether names holds one name.
func freshHas(names []string, name string) bool {
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}

// env is the child's whole environment, built from scratch so nothing of the surrounding
// shell — no PATH, no real home, no real plugin root — leaks into the run.
func (w *freshWorld) env(overrides ...string) []string {
	vars := []string{
		"HOME=" + w.home,
		"PATH=" + w.path,
		"TMPDIR=" + w.home,
		"XDG_CONFIG_HOME=" + filepath.Join(w.home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(w.home, ".local", "state"),
		"XDG_DATA_HOME=" + filepath.Join(w.home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(w.home, ".cache"),
		"AGENTS_HUDDLE_HOME=" + w.binHome,
		"CLAUDE_PLUGIN_ROOT=" + w.plugin,
	}
	if user := os.Getenv("USER"); user != "" {
		vars = append(vars, "USER="+user)
	}
	return append(vars, overrides...)
}

// run runs one command line through /bin/sh the way Claude Code runs a hook's or a command's line,
// and answers its exit code and both outputs.
func (w *freshWorld) run(t *testing.T, command string, overrides ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = w.env(overrides...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %q: %v", command, err)
		}
		code = exit.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// freshReleaseServer serves the built assets under the release tag's path, as the GitHub release does,
// and records every path it was asked for.
func freshReleaseServer(t *testing.T, tag string, files map[string][]byte) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	asked := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/"+tag+"/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestFreshInstall installs the plugin on a machine that has nothing: the launcher downloads the
// binary from the fake release, verifies it against its checksum table — re-pinned above to the
// test's own builds — and runs the word: the kanban word first, the radar word over the same
// install. A tampered asset fails its checksum and installs nothing.
func TestFreshInstall(t *testing.T) {
	release := freshReleaseOf(t)

	// the four release binaries, built the way release.yml builds them; the copy's launcher is
	// re-pinned to their digests, so the checksum the launcher verifies against is always this
	// commit's build and a stale committed manifest cannot fail the install flow
	built := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, key := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			path := freshBuild(t, release, key)
			mu.Lock()
			built[key] = path
			mu.Unlock()
		}(key)
	}
	wg.Wait()

	files := map[string][]byte{"SHA256SUMS": []byte(freshSums(t, release, built))}
	for key, path := range built {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the built binary: %v", err)
		}
		files[release.assets[key].Name] = data
	}
	releases := freshReleaseServer(t, release.tag, files)

	world := newFreshWorld(t, freshCopyPlugin(t))
	freshPinBuilds(t, release, world.plugin, built)
	for _, name := range []string{"node", "bun", "python", "python3"} {
		if code, _, _ := world.run(t, "command -v "+name); code == 0 {
			t.Fatalf("%s is on the world's PATH; the fresh install must not depend on it", name)
		}
	}
	host := runtime.GOOS + "-" + runtime.GOARCH
	binPath := filepath.Join(world.binHome, release.version, "huddle")
	if _, err := os.Stat(binPath); err == nil {
		t.Fatal("the binary home already holds the huddle binary; the world is not fresh")
	}
	downloadBase := "HUDDLE_DOWNLOAD_BASE=" + releases.URL

	t.Run("the kanban word downloads, verifies and runs", func(t *testing.T) {
		code, stdout, stderr := world.run(t, `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban --version`, downloadBase)
		if code != 0 || stdout != fmt.Sprintf("kanban %s\n", release.version) || stderr != "" {
			t.Fatalf("kanban --version = %d, stdout %q, stderr %q, want 0 and the version line",
				code, stdout, stderr)
		}
		info, err := os.Stat(binPath)
		if err != nil {
			t.Fatalf("the launcher never installed %s: %v", binPath, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("the installed binary at %s is not executable", binPath)
		}
		if got := freshSHA256(t, binPath); got != freshSHA256(t, built[host]) {
			t.Errorf("the installed binary hashes to %s, want the served build's %s",
				got, freshSHA256(t, built[host]))
		}
	})

	t.Run("the radar word runs the same install", func(t *testing.T) {
		code, stdout, stderr := world.run(t, `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" radar --version`, downloadBase)
		if code != 0 || stdout != fmt.Sprintf("radar %s\n", release.version) || stderr != "" {
			t.Fatalf("radar --version = %d, stdout %q, stderr %q, want 0 and the version line",
				code, stdout, stderr)
		}
	})

	t.Run("a tampered asset fails its checksum and installs nothing", func(t *testing.T) {
		tampered := make(map[string][]byte, len(files))
		for name, data := range files {
			tampered[name] = data
		}
		body := append([]byte{}, files[release.assets[host].Name]...)
		body[len(body)/2] ^= 0xff
		tampered[release.assets[host].Name] = body
		bad := freshReleaseServer(t, release.tag, tampered)

		// a fresh world: its binary home starts empty, so any install would show
		tainted := newFreshWorld(t, world.plugin)
		code, _, stderr := tainted.run(t,
			`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" kanban --version`, "HUDDLE_DOWNLOAD_BASE="+bad.URL)
		if code == 0 {
			t.Fatal("the launcher ran a word over a tampered asset")
		}
		if !strings.Contains(stderr, "checksum") {
			t.Errorf("stderr = %q, want the checksum failure", stderr)
		}
		if _, err := os.Stat(filepath.Join(tainted.binHome, release.version, "huddle")); err == nil {
			t.Error("the launcher installed a binary that failed its checksum")
		}
	})

	t.Run("a word the launcher does not know exits 2 naming it", func(t *testing.T) {
		code, _, stderr := world.run(t, `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" bogus`, downloadBase)
		if code != 2 || stderr != "huddle: unknown command bogus\n" {
			t.Fatalf("bogus = %d, stderr %q, want 2 and the unknown-command line", code, stderr)
		}
	})
}
