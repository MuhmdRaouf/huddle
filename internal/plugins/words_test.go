package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The channel's words are message, ask and reply. A user-facing string that says broadcast or
// publish teaches an older vocabulary, so these checks read the skill, the slash commands and the
// dashboard's strings.

var bannedWords = regexp.MustCompile(`(?i)\b(publish\w*|broadcast\w*)`)

// otherMeaning lists the files where publish means putting a page on the Artifact service.
var otherMeaning = map[string]string{
	"plugin/commands/kanban-export.md": "publishes the exported board as an Artifact",
}

func TestNoUserFacingStringSaysBroadcastOrPublish(t *testing.T) {
	var files []string
	for _, root := range []string{"plugin/skills/huddle", "plugin/commands", "web/hub/src"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			switch filepath.Ext(path) {
			case ".md", ".ts", ".tsx":
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) < 10 {
		t.Fatalf("found %d files to read, want the skill, the commands and the dashboard", len(files))
	}
	for _, path := range files {
		rel := filepath.ToSlash(strings.TrimPrefix(path, repoRoot+"/"))
		if _, skip := otherMeaning[rel]; skip {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if strings.HasPrefix(rel, "web/") {
			text = withoutComments(text)
		}
		for i, line := range strings.Split(text, "\n") {
			if w := bannedWords.FindString(line); w != "" {
				t.Errorf("%s:%d says %q: %s", rel, i+1, w, strings.TrimSpace(line))
			}
		}
	}
}

// withoutComments blanks the // and /* */ comments of TypeScript source and keeps every string
// literal, so a line count stays true and a "//" inside a string is no comment.
func withoutComments(src string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			i--
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			out.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+end], "\n")))
			i += end + 3
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
