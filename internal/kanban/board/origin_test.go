package board

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hermeticGit points git at an empty home and no system config, so the only
// config a case sees is the one it writes, and answers the real git binary.
func hermeticGit(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_GLOBAL", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	return realGit
}

func runGit(t *testing.T, realGit, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command(realGit, full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a repository with one commit and appends configText to its
// config.
func newRepo(t *testing.T, realGit, configText string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, realGit, dir, "init", "-q")
	runGit(t, realGit, dir, "commit", "-q", "--allow-empty", "-m", "first")
	appendFile(t, filepath.Join(dir, ".git", "config"), configText)
	return dir
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

const origin = "https://example.com/team/x.git"

func originSection(url string) string {
	return "[remote \"origin\"]\n\turl = " + url + "\n"
}

// gitSpy puts a git on PATH that records each call in a marker file and runs
// the real git, so a case shows whether OriginOf asked git.
func gitSpy(t *testing.T, realGit string) (called func() bool) {
	t.Helper()
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	script := "#!/bin/sh\ntouch '" + marker + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() bool { _, err := os.Stat(marker); return err == nil }
}

// Each case builds a real repository and compares OriginOf with the real
// `git -C <root> config --get remote.origin.url`. A case marked fallback has a
// shape the config read leaves to git: it must ask git and still agree.
func TestOriginOfEqualsGitConfigGet(t *testing.T) {
	realGit := hermeticGit(t)

	type tc struct {
		name     string
		build    func(t *testing.T) string
		want     string
		fallback bool
	}
	plain := func(config string) func(t *testing.T) string {
		return func(t *testing.T) string { return newRepo(t, realGit, config) }
	}
	cases := []tc{
		{name: "plain repo with origin", build: plain(originSection(origin)), want: origin},
		{name: "no origin", build: plain("")},
		{name: "another remote only", build: plain("[remote \"upstream\"]\n\turl = https://example.com/up.git\n")},
		{name: "several url lines answer the last", build: plain(
			"[remote \"origin\"]\n\turl = https://example.com/first.git\n\turl = " + origin + "\n"), want: origin},
		{name: "url lines in two sections answer the last", build: plain(
			originSection("https://example.com/first.git") + "[core]\n\tbare = false\n" + originSection(origin)), want: origin},
		{name: "quoted value", build: plain("[remote \"origin\"]\n\turl = \"" + origin + "\"\n"), want: origin},
		{name: "quoted value keeps comment characters", build: plain(
			"[remote \"origin\"]\n\turl = \"https://example.com/a;b#c.git\"\n"), want: "https://example.com/a;b#c.git"},
		{name: "escapes", build: plain(
			"[remote \"origin\"]\n\turl = \"https://example.com/a\\\"b\\\\c\\td\\ne\"\n"), want: "https://example.com/a\"b\\c\td\ne"},
		{name: "full-line and inline comments", build: plain(
			"# one\n; two\n[remote \"origin\"] # header\n\t# inside\n\turl = " + origin + " # inline\n\t; more\n"), want: origin},
		{name: "semicolon inline comment", build: plain("[remote \"origin\"]\n\turl = " + origin + " ; inline\n"), want: origin},
		{name: "whitespace around the value", build: plain("[remote \"origin\"]\n\turl =\t  " + origin + "  \t\n"), want: origin},
		{name: "inner whitespace", build: plain("[remote \"origin\"]\n\turl = https://example.com/a \t b.git\n"), want: "https://example.com/a \t b.git"},
		{name: "no spaces around equals", build: plain("[remote \"origin\"]\nurl=" + origin + "\n"), want: origin},
		{name: "windows line endings", build: plain("[remote \"origin\"]\r\n\turl = " + origin + "\r\n"), want: origin},
		{name: "empty url", build: plain("[remote \"origin\"]\n\turl =\n")},
		{name: "mixed-case section and key", build: plain("[Remote \"origin\"]\n\tURL = " + origin + "\n"), want: origin},
		{name: "upper-case section and key", build: plain("[REMOTE \"origin\"]\n\tUrl = " + origin + "\n"), want: origin},
		{name: "subsection Origin does not match", build: plain("[remote \"Origin\"]\n\turl = " + origin + "\n")},
		{name: "key on the header line", build: plain("[remote \"origin\"] url = " + origin + "\n"), want: origin},
		{name: "line continuation", build: plain(
			"[remote \"origin\"]\n\turl = https://example.com/\\\nteam/x.git\n"), want: "https://example.com/team/x.git"},
		{name: "insteadOf is not applied", build: plain(
			"[url \"ssh://git@example.com/\"]\n\tinsteadOf = https://example.com/\n\tpushInsteadOf = https://example.com/\n" + originSection(origin)), want: origin},
		{name: "root is a subdirectory", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			sub := filepath.Join(repo, "deep", "er")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			return sub
		}, want: origin},
		{name: "linked worktree reads the common config", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			linked := filepath.Join(t.TempDir(), "linked")
			runGit(t, realGit, repo, "worktree", "add", "-q", "-b", "other", linked)
			return linked
		}, want: origin},
		{name: "linked worktree of a repo without origin", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, "")
			linked := filepath.Join(t.TempDir(), "linked")
			runGit(t, realGit, repo, "worktree", "add", "-q", "-b", "other", linked)
			return linked
		}},
		{name: "gitdir file with an absolute path", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(repo, ".git")+"\n")
			return dir
		}, want: origin},
		{name: "gitdir file with a relative path", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			dir := filepath.Join(repo, "nested")
			writeFile(t, filepath.Join(dir, ".git"), "gitdir: ../.git\n")
			return dir
		}, want: origin},
		{name: "submodule-style gitdir without commondir", build: func(t *testing.T) string {
			gitdir := filepath.Join(t.TempDir(), "modules", "sub")
			if err := os.MkdirAll(filepath.Dir(gitdir), 0o755); err != nil {
				t.Fatal(err)
			}
			work := t.TempDir()
			runGit(t, realGit, work, "init", "-q", "--separate-git-dir", gitdir)
			appendFile(t, filepath.Join(gitdir, "config"), originSection(origin))
			return work
		}, want: origin},
		{name: "include falls back to git", fallback: true, want: origin, build: func(t *testing.T) string {
			repo := newRepo(t, realGit, "[include]\n\tpath = extra.cfg\n")
			writeFile(t, filepath.Join(repo, ".git", "extra.cfg"), originSection(origin))
			return repo
		}},
		{name: "includeIf falls back to git", fallback: true, want: origin, build: func(t *testing.T) string {
			repo := newRepo(t, realGit, "")
			appendFile(t, filepath.Join(repo, ".git", "config"), "[includeIf \"gitdir:**/.git\"]\n\tpath = extra.cfg\n")
			writeFile(t, filepath.Join(repo, ".git", "extra.cfg"), originSection(origin))
			return repo
		}},
		{name: "include in another-case section falls back to git", fallback: true, want: origin, build: func(t *testing.T) string {
			repo := newRepo(t, realGit, "[INCLUDE]\n\tPath = extra.cfg\n")
			writeFile(t, filepath.Join(repo, ".git", "extra.cfg"), originSection(origin))
			return repo
		}},
		{name: "dotted section header falls back to git", fallback: true, want: origin, build: plain("[remote.origin]\n\turl = " + origin + "\n")},
		{name: "unterminated quote falls back to git", fallback: true, build: plain("[remote \"origin\"]\n\turl = \"" + origin + "\n")},
		{name: "unknown escape falls back to git", fallback: true, build: plain("[remote \"origin\"]\n\turl = a\\qb\n")},
		{name: "url without a value falls back to git", fallback: true, build: plain("[remote \"origin\"]\n\turl\n")},
		{name: "per-worktree config falls back to git", fallback: true, want: origin, build: func(t *testing.T) string {
			repo := newRepo(t, realGit, "[extensions]\n\tworktreeConfig = true\n")
			writeFile(t, filepath.Join(repo, ".git", "config.worktree"), originSection(origin))
			return repo
		}},
		{name: "GIT_CONFIG falls back to git", fallback: true, want: "https://file.example.com/other.git", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			other := filepath.Join(t.TempDir(), "other.cfg")
			writeFile(t, other, originSection("https://file.example.com/other.git"))
			t.Setenv("GIT_CONFIG", other)
			return repo
		}},
		{name: "GIT_CONFIG_COUNT falls back to git", fallback: true, want: "https://env.example.com/y.git", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
			t.Setenv("GIT_CONFIG_VALUE_0", "https://env.example.com/y.git")
			return repo
		}},
		{name: "GIT_CONFIG_PARAMETERS falls back to git", fallback: true, want: "https://param.example.com/z.git", build: func(t *testing.T) string {
			repo := newRepo(t, realGit, originSection(origin))
			t.Setenv("GIT_CONFIG_PARAMETERS", "'remote.origin.url=https://param.example.com/z.git'")
			return repo
		}},
		{name: "not a repository falls back to git", fallback: true, build: func(t *testing.T) string { return t.TempDir() }},
		{name: "gitdir file pointing nowhere falls back to git", fallback: true, build: func(t *testing.T) string {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(dir, "missing")+"\n")
			return dir
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := c.build(t)

			out, _ := exec.Command(realGit, "-C", root, "config", "--get", "remote.origin.url").Output()
			if gitSays := strings.TrimSpace(string(out)); gitSays != c.want {
				t.Fatalf("git answers %q, the case expects %q", gitSays, c.want)
			}

			called := gitSpy(t, realGit)
			if got := OriginOf(context.Background(), root); got != c.want {
				t.Errorf("OriginOf = %q, want %q", got, c.want)
			}
			if called() != c.fallback {
				t.Errorf("git asked = %v, want %v", called(), c.fallback)
			}
		})
	}
}

// A git that hangs cannot change the answer for a repo whose config file
// answers it.
func TestOriginOfAnswersFromTheConfigFileWhileGitHangs(t *testing.T) {
	realGit := hermeticGit(t)
	repo := newRepo(t, realGit, originSection(origin))
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 6\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	got := OriginOf(context.Background(), repo)

	if got != origin {
		t.Errorf("OriginOf = %q, want %q", got, origin)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("OriginOf took %s, want it back at once", elapsed)
	}
}

// The session hook asks for the origin inside a part deadline of 2.5 s, so the
// git read must end near its one-second bound even when git leaves a child
// behind that keeps the output pipe open after git itself is killed.

func TestOriginOfEndsAtItsBoundWhenGitLeavesAChildHoldingThePipe(t *testing.T) {
	realGit := hermeticGit(t)
	repo := newRepo(t, realGit, "[include]\n\tpath = extra.cfg\n")
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 6\necho https://example.com/team/x.git\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	got := OriginOf(context.Background(), repo)

	if got != "" {
		t.Errorf("OriginOf = %q, want empty: git never answered inside the bound", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("OriginOf took %s, want it back within 2s", elapsed)
	}
}
