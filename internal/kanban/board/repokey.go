package board

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// NormalizeRemote answers one remote URL's repository identity: the host and
// the path, with the scheme, the user, the port and a trailing .git dropped
// and the host lower-cased, so the spellings one repository answers (https,
// ssh, scp-like) fold into one. A URL that names no host and path — empty,
// local, unparsable — answers "".
func NormalizeRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var host, path string
	if _, _, ok := strings.Cut(s, "://"); ok {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like: [user@]host:path, the colon the separator; a remote with
		// no scheme and no colon is a local path, which names no repository
		// two machines share
		var ok bool
		host, path, ok = strings.Cut(s, ":")
		if !ok {
			return ""
		}
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + path
}

// RepoKey answers a repository's board key: the first 12 hex of sha256 of
// "git:" plus the normalised origin, so the clones of one repository answer
// one key wherever each sits. A remote that normalises to nothing — no origin,
// a local path — leaves the fallback, the key the board's own path decides.
func RepoKey(remote, fallback string) string {
	identity := NormalizeRemote(remote)
	if identity == "" {
		return fallback
	}
	sum := sha256.Sum256([]byte("git:" + identity))
	return hex.EncodeToString(sum[:])[:12]
}
