// The project's settings file, .agents/huddle/huddle.json, as the commands and the hub write it:
// one place sets the channel and the session's name and rewrites the file with every other key
// where it was.
package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// setChannel names the channel the project's sessions join and the name they join as.
func (s *settings) setChannel(channel, as string) {
	s.set("channel", jsfmt.String(channel))
	s.set("as", jsfmt.String(as))
}

// writeSettings saves the settings to file, making its folder.
func writeSettings(file string, s *settings) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(indentJSON(s.value())+"\n"), 0o644)
}

// WriteProjectChannel saves channel and as into the settings file of the project at root and
// answers that file.
func WriteProjectChannel(root, channel, as string) (string, error) {
	file := filepath.Join(root, FileRel)
	next := readSettings(file)
	next.setChannel(channel, as)
	return file, writeSettings(file, next)
}

// ProjectFiles is the hub's way into the settings files of the projects its sessions work in.
type ProjectFiles struct {
	W *World
}

// projectRoot answers the project around dir: where its settings file is, else its repository's
// main checkout (a worktree shares the main checkout's file), else dir itself.
func (p ProjectFiles) projectRoot(ctx context.Context, dir string) string {
	if found := FindConfig(ctx, p.W, dir); found != nil {
		return found.Root
	}
	if main := MainCheckout(ctx, p.W, dir); main != "" {
		return main
	}
	return dir
}

// MemberName answers the name a session working in cwd joins as: asked when given, else the name
// the project's settings file holds, else the project's own name.
func (p ProjectFiles) MemberName(ctx context.Context, cwd, asked string) string {
	if asked != "" {
		return asked
	}
	if found := FindConfig(ctx, p.W, cwd); found != nil {
		if as := cfgString(found.Cfg, "as"); as != "" {
			return as
		}
	}
	return Slug(p.projectRoot(ctx, cwd), "huddle")
}

// SaveChannel writes channel and name into the settings file of the project around cwd, keeps the
// folder out of git, and answers that file.
func (p ProjectFiles) SaveChannel(ctx context.Context, cwd, channel, name string) (string, error) {
	root := p.projectRoot(ctx, cwd)
	file, err := WriteProjectChannel(root, channel, name)
	if err != nil {
		return "", err
	}
	KeepOutOfGit(ctx, p.W, filepath.Dir(file))
	return file, nil
}
