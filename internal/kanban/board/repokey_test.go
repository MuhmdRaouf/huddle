package board

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The repository identity a board's key rides on: every spelling of one
// repository's remote — https, ssh, scp-like — answers one normalised form,
// and RepoKey folds it into the key the tab and the clones of one repository
// share, leaving the fallback when a remote names no repository.

func TestNormalizeRemoteAnswersOneIdentityPerRepository(t *testing.T) {
	cases := []struct{ name, remote, want string }{
		{"scp-like with a user and .git", "git@github.com:MuhmdRaouf/x.git", "github.com/MuhmdRaouf/x"},
		{"scp-like without a user", "github.com:MuhmdRaouf/x", "github.com/MuhmdRaouf/x"},
		{"host lower-cased, path case kept", "git@GitHub.com:MuhmdRaouf/X.git", "github.com/MuhmdRaouf/X"},
		{"ssh with the default port spelled", "ssh://git@github.com:22/MuhmdRaouf/x.git", "github.com/MuhmdRaouf/x"},
		{"ssh with a port", "ssh://git@git.example.com:2222/team/x.git", "git.example.com/team/x"},
		{"ssh without a user", "ssh://github.com/MuhmdRaouf/x.git", "github.com/MuhmdRaouf/x"},
		{"https plain", "https://github.com/MuhmdRaouf/x", "github.com/MuhmdRaouf/x"},
		{"https with a user", "https://raouf@github.com/MuhmdRaouf/x.git", "github.com/MuhmdRaouf/x"},
		{"https with a user and a password", "https://raouf:tok@github.com/MuhmdRaouf/x.git", "github.com/MuhmdRaouf/x"},
		{"https with a port", "https://git.example.com:8443/team/x.git", "git.example.com/team/x"},
		{"a path deeper than owner and repo", "ssh://git@git.example.com/group/team/x.git", "git.example.com/group/team/x"},
		{"trailing slashes dropped", "https://github.com/MuhmdRaouf/x//", "github.com/MuhmdRaouf/x"},
		{"spaces trimmed", "  git@github.com:MuhmdRaouf/x.git\n", "github.com/MuhmdRaouf/x"},
		{"empty", "", ""},
		{"spaces only", "   ", ""},
		{"a relative local path", "../x", ""},
		{"an absolute local path", "/srv/git/x.git", ""},
		{"a file url", "file:///srv/git/x", ""},
		{"a host with no path", "https://github.com", ""},
		{"free text", "not a remote", ""},
	}
	for _, c := range cases {
		if got := NormalizeRemote(c.remote); got != c.want {
			t.Errorf("%s: NormalizeRemote(%q) = %q, want %q", c.name, c.remote, got, c.want)
		}
	}
}

func TestRepoKeyAnswersOneKeyPerRepositoryAndKeepsTheFallback(t *testing.T) {
	spellings := []string{
		"git@github.com:MuhmdRaouf/x.git",
		"ssh://git@github.com:22/MuhmdRaouf/x.git",
		"https://raouf@github.com/MuhmdRaouf/x",
	}
	keys := map[string]bool{}
	for _, remote := range spellings {
		keys[RepoKey(remote, "fallback")] = true
	}
	if len(keys) != 1 {
		t.Errorf("one repository's spellings answered %d keys %v, want one", len(keys), keys)
	}
	// the key is the first 12 hex of sha256 of "git:" plus the identity, the
	// same shape the path key has
	sum := sha256.Sum256([]byte("git:github.com/MuhmdRaouf/x"))
	want := hex.EncodeToString(sum[:])[:12]
	for _, remote := range spellings {
		if got := RepoKey(remote, "fallback"); got != want {
			t.Errorf("RepoKey(%q) = %s, want %s", remote, got, want)
		}
	}
	for _, remote := range []string{"", "   ", "../x", "file:///srv/git/x"} {
		if got := RepoKey(remote, "path-key"); got != "path-key" {
			t.Errorf("RepoKey(%q) = %s, want the fallback path key", remote, got)
		}
	}
}
