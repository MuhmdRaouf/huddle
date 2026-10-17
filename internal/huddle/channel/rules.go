// The approval rules: shell commands the owner wants to be asked about before a session in their
// channel runs them. Pure: the PreToolUse hook loads them on every Bash call and matches locally,
// so a command that matches no rule costs no network at all. The server keeps which rules are on
// per channel; the hook only asks the server when a command could match one.
//
// A rule never denies and never waits: a match makes Claude Code show its own permission prompt
// ("ask"), the user answers there, and the owner sees the request in the dashboard's Inbox.
package channel

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Rule is one approval rule.
type Rule struct {
	ID    string
	Label string
	Help  string
	On    bool
	pats  []pattern
}

// jsS and jsNotS are one whitespace and one non-whitespace of the source's set: the
// JavaScript class jsfmt carries, which RE2's own \s (ASCII, no \v) is not.
const (
	jsS    = jsfmt.SpaceClass
	jsNotS = `[^` + jsfmt.SpaceSet + `]`
)

// wRe is a command word's position: at the start of the line or after a shell separator
// (; & | ( ` $( sudo … exec … then … do …).
const wRe = `(?:^|[;&|(\x60]|\$\(|\bsudo` + jsS + `+|\bexec` + jsS + `+|\bthen` + jsS + `+|\bdo` + jsS + `+)` + jsS + `*`

// gitOpts matches "git" plus any options before its subcommand.
const gitOpts = `git(?:` + jsS + `+(?:-[Cc]` + jsS + `+` + jsNotS + `+|--?[\w-]+(?:=` + jsNotS + `+)?))*` + jsS + `+`

// pattern is one rule's shape. The regex stops where the command word ends; endWord applies the
// end-of-word check there ("push" but not "push-helper"). The two flags carry the checks a plain
// expression cannot: notAssign (the word must not begin an assignment) and notTagList (git tag's
// listing flags read a tag, they do not make one).
type pattern struct {
	re         *regexp.Regexp
	endWord    bool
	rest       *regexp.Regexp
	notAssign  bool
	notTagList bool
}

func (p pattern) matches(c string) bool {
	for _, loc := range p.re.FindAllStringIndex(c, -1) {
		end := loc[1]
		if p.endWord && end < len(c) && isWordDash(c[end]) {
			continue
		}
		after := c[end:]
		if p.notAssign {
			j := skipJSSpace(after)
			if strings.HasPrefix(after[j:], "=") {
				continue
			}
		}
		if p.notTagList && isTagListing(after) {
			continue
		}
		if p.rest == nil || p.rest.MatchString(after) {
			return true
		}
	}
	return false
}

func isWordDash(b byte) bool {
	return b == '_' || b == '-' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func isWord(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

// skipJSSpace answers how many bytes of s are whitespace in the source's set.
func skipJSSpace(s string) int {
	j := 0
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if !jsfmt.IsSpace(r) {
			break
		}
		j += size
	}
	return j
}

// boundaryAt reports whether a word boundary sits at r[k]: the character before it is a word
// character and the one at it is not (or the text ends).
func boundaryAt(r string, k int) bool {
	if k == 0 {
		return false
	}
	if !isWord(r[k-1]) {
		return false
	}
	return k == len(r) || !isWord(r[k])
}

var tagListFlags = []string{"-l", "--list", "--contains", "--points-at", "--merged", "--no-merged", "--sort", "--format", "-v", "--verify"}

// isTagListing reports whether what follows "git tag" only reads tags: the end of the command, a
// separator, or a listing flag.
func isTagListing(after string) bool {
	r := after[skipJSSpace(after):]
	if r == "" || r[0] == ';' || r[0] == '&' || r[0] == '|' || r[0] == ')' {
		return true
	}
	for _, flag := range tagListFlags {
		if strings.HasPrefix(r, flag) && boundaryAt(r, len(flag)) {
			return true
		}
	}
	if strings.HasPrefix(r, "-n") {
		k := 2
		for k < len(r) && r[k] >= '0' && r[k] <= '9' {
			k++
		}
		if boundaryAt(r, k) {
			return true
		}
	}
	return false
}

func mustCompile(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// Rules lists the approval rules in their fixed order.
var Rules = []Rule{
	{
		ID: "force_push", Label: "Force push", Help: "git push --force, -f, --force-with-lease or a +ref", On: true,
		pats: []pattern{{
			re:      mustCompile(wRe + gitOpts + `push`),
			endWord: true,
			rest:    mustCompile(`^[^;&|\n]*?(?:` + jsS + `--force(?:-with-lease)?\b|` + jsS + `-[a-zA-Z]*f[a-zA-Z]*\b|` + jsS + `\+` + jsNotS + `)`),
		}},
	},
	{
		ID: "delete", Label: "Delete files or branches", Help: "rm, git rm, git clean, git branch -d/-D, git push --delete, find -delete, rmdir, unlink", On: true,
		pats: []pattern{
			{re: mustCompile(wRe + `(?:rmdir|unlink|shred|trash|rm)`), endWord: true, notAssign: true},
			{re: mustCompile(wRe + gitOpts + `(?:rm|clean)`), endWord: true},
			{re: mustCompile(wRe + gitOpts + `branch`), endWord: true, rest: mustCompile(`^[^;&|\n]*` + jsS + `(?:-[a-zA-Z]*[dD]\b|--delete\b)`)},
			{re: mustCompile(wRe + gitOpts + `push`), endWord: true, rest: mustCompile(`^[^;&|\n]*(?:` + jsS + `--delete\b|` + jsS + `-d\b|` + jsS + `:` + jsNotS + `)`)},
			{re: mustCompile(wRe + `find\b[^;&|\n]*` + jsS + `-delete\b`)},
		},
	},
	{
		ID: "git_push", Label: "Git push", Help: "every git push", On: false,
		pats: []pattern{{re: mustCompile(wRe + gitOpts + `push`), endWord: true}},
	},
	{
		ID: "git_tag", Label: "Git tag", Help: "creating, moving or deleting a tag (listing is fine)", On: false,
		pats: []pattern{{re: mustCompile(wRe + gitOpts + `tag`), endWord: true, notTagList: true}},
	},
	{
		ID: "publish", Label: "Publish a package", Help: "npm/pnpm/yarn/bun publish, cargo publish, twine upload, gem push, poetry/uv/flit publish, docker push, gh release create", On: false,
		pats: []pattern{
			{re: mustCompile(wRe + `(?:npm|pnpm|yarn|bun|cargo|poetry|uv|flit|vsce|ovsx|hatch)` + jsS + `+(?:[\w-]+` + jsS + `+)?publish`), endWord: true},
			{re: mustCompile(wRe + `(?:python3?` + jsS + `+-m` + jsS + `+)?twine` + jsS + `+upload`), endWord: true},
			{re: mustCompile(wRe + `gem` + jsS + `+push`), endWord: true},
			{re: mustCompile(wRe + `docker` + jsS + `+(?:image` + jsS + `+)?push`), endWord: true},
			{re: mustCompile(wRe + `gh` + jsS + `+release` + jsS + `+create`), endWord: true},
		},
	},
}

// Switch is one rule's on/off state.
type Switch struct {
	ID string
	On bool
}

// Matching answers the rules a command matches, in their order (it may match several: a force
// push is a push).
func Matching(command string) []Rule {
	c := command
	if jsfmt.Trim(c) == "" || jsfmt.UTF16Len(c) > 100000 {
		return nil
	}
	var out []Rule
	for _, r := range Rules {
		for _, p := range r.pats {
			if p.matches(c) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// Defaults is every rule's default switch (force push and deleting on, the rest off).
func Defaults() []Switch {
	out := make([]Switch, len(Rules))
	for i, r := range Rules {
		out[i] = Switch{ID: r.ID, On: r.On}
	}
	return out
}

// Effective is a channel's switches: its stored ones over the defaults; unknown ids are dropped.
func Effective(stored *jsfmt.Value) []Switch {
	out := Defaults()
	if stored == nil || stored.Kind() != jsfmt.KindObject {
		return out
	}
	over := map[string]bool{}
	for i, k := range stored.Keys() {
		f := stored.Fields()[i]
		if f != nil && f.Kind() == jsfmt.KindBool {
			over[k] = f.EncodeJSON() == "true"
		}
	}
	for i, r := range Rules {
		if v, ok := over[r.ID]; ok {
			out[i].On = v
		}
	}
	return out
}
