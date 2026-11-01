// The project's settings file as the hub's "Add to channel" writes it: the channel and the
// session's name land in .agents/huddle/huddle.json of the project's main checkout, every other
// key stays, and the folder stays out of git.
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectWithGit makes a repository root with a git directory and a source folder under it.
func projectWithGit(t *testing.T, name string) (root, sub string) {
	t.Helper()
	root = filepath.Join(realDir(t, t.TempDir()), name)
	sub = filepath.Join(root, "src", "api")
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

func settingsOf(t *testing.T, root string) string {
	t.Helper()
	return readFile(t, filepath.Join(root, FileRel))
}

func TestWriteProjectChannelKeepsEveryOtherKeyInOrder(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, FileRel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, `{"autostart":false,"role":"docs","listen":["ops"]}`)
	got, err := WriteProjectChannel(root, "homelab", "docs")
	if err != nil || got != file {
		t.Fatalf("WriteProjectChannel = %q, %v, want %q", got, err, file)
	}
	want := "{\n  \"autostart\": false,\n  \"role\": \"docs\",\n  \"listen\": [\n    \"ops\"\n  ],\n  \"channel\": \"homelab\",\n  \"as\": \"docs\"\n}\n"
	if text := readFile(t, file); text != want {
		t.Errorf("the settings = %q, want %q", text, want)
	}
}

func TestWriteProjectChannelMakesTheFileOfAProjectWithNone(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteProjectChannel(root, "homelab", "proxbeam"); err != nil {
		t.Fatal(err)
	}
	if text := settingsOf(t, root); !strings.Contains(text, `"channel": "homelab"`) || !strings.Contains(text, `"as": "proxbeam"`) {
		t.Errorf("the settings = %q, want the channel and the name", text)
	}
}

func TestProjectFilesNameTheMemberAfterTheProject(t *testing.T) {
	root, sub := projectWithGit(t, "proxbeam")
	files := ProjectFiles{W: world(t, sub, nil)}
	ctx := context.Background()
	if got := files.MemberName(ctx, sub, ""); got != "proxbeam" {
		t.Errorf("the name from a folder inside the project = %q, want proxbeam", got)
	}
	if got := files.MemberName(ctx, sub, "backup"); got != "backup" {
		t.Errorf("the name asked for = %q, want backup", got)
	}
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(FileRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, FileRel), `{"channel":"old","as":"api"}`)
	if got := files.MemberName(ctx, sub, ""); got != "api" {
		t.Errorf("the name the project's file holds = %q, want api", got)
	}
}

func TestProjectFilesSaveTheChannelInTheMainCheckoutKeptOutOfGit(t *testing.T) {
	root, sub := projectWithGit(t, "homelab")
	worktree := filepath.Join(realDir(t, t.TempDir()), "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+root+"/.git/worktrees/wt\n")
	files := ProjectFiles{W: world(t, worktree, nil)}
	ctx := context.Background()
	for _, cwd := range []string{sub, worktree} {
		got, err := files.SaveChannel(ctx, cwd, "homelab", "homelab")
		if want := filepath.Join(root, FileRel); err != nil || got != want {
			t.Fatalf("SaveChannel from %s = %q, %v, want %q", cwd, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(worktree, FileRel)); err == nil {
		t.Error("the worktree got a settings file of its own")
	}
	if exclude := readFile(t, filepath.Join(root, ".git", "info", "exclude")); !strings.Contains(exclude, ".agents/huddle/") {
		t.Errorf("the exclude file = %q, want .agents/huddle/ in it", exclude)
	}
}

func TestProjectFilesSaveInAFolderOutsideAnyRepositoryUsesTheFolder(t *testing.T) {
	dir := realDir(t, t.TempDir())
	files := ProjectFiles{W: world(t, dir, nil)}
	got, err := files.SaveChannel(context.Background(), dir, "homelab", "scratch")
	if want := filepath.Join(dir, FileRel); err != nil || got != want {
		t.Fatalf("SaveChannel = %q, %v, want %q", got, err, want)
	}
}
