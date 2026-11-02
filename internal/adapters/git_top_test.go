package adapters

// Every git write refuses a folder that is not its own repository's top: a folder inside a
// repository, with no repository of its own, must never write into the repository around it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
)

// parentRepoState is what a write into the parent repository would change.
type parentRepoState struct {
	head      string
	reflog    string
	branches  string
	status    string
	worktrees string
}

func readParentRepoState(repo testkit.TestRepo) parentRepoState {
	return parentRepoState{
		head:      repo.Head(),
		reflog:    repo.Git("reflog", "--all", "--format=%H %gd %gs"),
		branches:  repo.Git("branch", "--list", "--format=%(refname) %(objectname)"),
		status:    repo.Git("status", "--porcelain", "-uall"),
		worktrees: repo.Git("worktree", "list", "--porcelain"),
	}
}

// nestedFolderRepo is a real repository on main with a folder inside it that holds an untracked
// file and has no repository of its own, plus a branch one commit ahead of main and a linked
// worktree on its own branch.
type nestedFolderRepo struct {
	repo         testkit.TestRepo
	nested       string
	ahead        string
	mainTip      string
	linked       testkit.TestRepo
	linkedBranch string
	baseSha      string
}

func makeNestedFolderRepo(t *testing.T) nestedFolderRepo {
	t.Helper()
	repo := testkit.MakeRepo(t)
	gitLfRepo(repo)
	base := repo.Head()

	repo.Git("checkout", "-q", "-b", "ahead")
	repo.Write("ahead.txt", "ahead\n")
	ahead := repo.CommitAll("ahead")
	repo.Git("checkout", "-q", "main")

	linked := gitApplyWorktree(t, repo, "zai/job", base)
	linked.Write("README.md", "# changed by the job\n")

	nested := filepath.Join(repo.Dir, "not-a-repo")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "stray.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return nestedFolderRepo{
		repo: repo, nested: nested, ahead: ahead, mainTip: base,
		linked: linked, linkedBranch: "zai/job", baseSha: base,
	}
}

func TestAGitWriteRefusesAFolderThatIsNotItsOwnRepositoryTop(t *testing.T) {
	testkit.IsolateGitConfig(t)
	git := CreateGitCli(testkit.ReferenceProvider.Name)
	ctx := context.Background()

	writes := []struct {
		name string
		call func(w nestedFolderRepo) error
	}{
		{"CommitAll", func(w nestedFolderRepo) error {
			_, err := git.CommitAll(ctx, w.nested, "stray", ports.CommitOptions{})
			return err
		}},
		{"AmendAll", func(w nestedFolderRepo) error {
			_, err := AmendAll(ctx, w.nested, ports.CommitOptions{})
			return err
		}},
		{"RebaseJobBranch", func(w nestedFolderRepo) error {
			return RebaseJobBranch(ctx, w.nested, w.ahead, nil)
		}},
		{"FastForward", func(w nestedFolderRepo) error {
			_, err := FastForward(ctx, w.nested, w.ahead, w.mainTip)
			return err
		}},
		{"AddWorktree", func(w nestedFolderRepo) error {
			return git.AddWorktree(ctx, w.nested, gitCliWorktreePath(t, "wt"), "zai/other", w.baseSha)
		}},
		{"RemoveWorktree", func(w nestedFolderRepo) error {
			return git.RemoveWorktree(ctx, w.nested, w.linked.Dir, w.linkedBranch)
		}},
		{"AddDetachedWorktree", func(w nestedFolderRepo) error {
			return AddDetachedWorktree(ctx, w.nested, gitCliWorktreePath(t, "detached"), w.ahead)
		}},
		{"RemoveDetachedWorktree", func(w nestedFolderRepo) error {
			return RemoveDetachedWorktree(ctx, w.nested, w.linked.Dir)
		}},
		{"ApplyToWorkingTree", func(w nestedFolderRepo) error {
			return git.ApplyToWorkingTree(ctx, w.nested, w.linked.Dir, w.baseSha)
		}},
	}
	for _, write := range writes {
		t.Run(write.name+" answers not_a_repo and leaves the repository around the folder untouched", func(t *testing.T) {
			w := makeNestedFolderRepo(t)
			before := readParentRepoState(w.repo)
			readmeBefore := w.repo.Read("README.md")

			err := write.call(w)

			if after := readParentRepoState(w.repo); after != before {
				t.Errorf("the repository around the folder changed:\nbefore %+v\nafter  %+v", before, after)
			}
			gitCliWantNotARepo(t, err, w.nested)
			if got := w.repo.Read("README.md"); got != readmeBefore {
				t.Fatalf("README.md = %q, want it untouched", got)
			}
			if _, statErr := os.Stat(w.linked.Dir); statErr != nil {
				t.Fatalf("the linked worktree is gone: %v", statErr)
			}
		})
	}
}

func TestAGitWriteStillWorksInARepositoryTopAndInALinkedWorktree(t *testing.T) {
	testkit.IsolateGitConfig(t)
	git := CreateGitCli(testkit.ReferenceProvider.Name)
	w := makeNestedFolderRepo(t)

	if _, err := git.CommitAll(context.Background(), w.linked.Dir, "job work", ports.CommitOptions{}); err != nil {
		t.Fatalf("CommitAll in a linked worktree = %v, want it to commit", err)
	}
	if _, err := AmendAll(context.Background(), w.linked.Dir, ports.CommitOptions{}); err != nil {
		t.Fatalf("AmendAll in a linked worktree = %v, want it to amend", err)
	}
	if _, err := git.CommitAll(context.Background(), w.repo.Dir, "top work", ports.CommitOptions{}); err != nil {
		t.Fatalf("CommitAll in the repository top = %v, want it to commit", err)
	}
}
