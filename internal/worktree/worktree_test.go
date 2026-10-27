package worktree_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/worktree"
	"github.com/mandloideep/reclaim/internal/worktree/worktreetest"
)

func osEnv() worktree.Env { return worktree.Env{Exec: execx.OS{}} }

// ownStart returns this process's start time as ps prints it, in UTC, the
// way Claude Code records it in its locks.
func ownStart(t *testing.T) string {
	t.Helper()
	out, err := execx.OS{}.Output(context.Background(), "ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Skip("ps cannot report this process: " + err.Error())
	}
	local, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(out), " "), time.Local)
	require.NoError(t, err)
	return local.UTC().Format("Mon Jan _2 15:04:05 2006")
}

// TestClassifyStates builds one worktree per state DESIGN.md lists, with
// real git, and checks every part of the state and the verdict.
func TestClassifyStates(t *testing.T) {
	f := worktreetest.New(t)
	ctx := context.Background()

	ancestor := f.Worktree("ancestor")
	f.Commit(ancestor, "a.txt", "a\n")
	f.Push(ancestor, "ancestor")
	f.MergeFF("ancestor")

	squash := f.Worktree("squash")
	f.Commit(squash, "s1.txt", "1\n")
	f.Commit(squash, "s2.txt", "2\n")
	f.Push(squash, "squash")
	f.MergeSquash("squash")

	rebase := f.Worktree("rebase")
	f.Commit(rebase, "r1.txt", "1\n")
	f.Commit(rebase, "r2.txt", "2\n")
	f.Push(rebase, "rebase")
	f.MergeRebase("rebase")

	pushed := f.Worktree("pushed")
	f.Commit(pushed, "p.txt", "p\n")
	f.Push(pushed, "pushed")

	modified := f.Worktree("modified")
	f.Write(modified, "README.md", "changed\n")
	f.Write(modified, "new.txt", "new\n")

	untracked := f.Worktree("untracked")
	f.Write(untracked, "notes/a.txt", "a\n")
	f.Write(untracked, "notes/b.txt", "b\n")

	ignored := f.Worktree("ignored")
	f.Write(ignored, ".env", "SECRET=1\n")
	f.Write(ignored, "node_modules/left-pad/index.js", "x\n")

	artifacts := f.Worktree("artifacts")
	f.Write(artifacts, "node_modules/left-pad/index.js", "x\n")

	detached := f.WorktreeAt(filepath.Join(f.Repo, ".claude", "worktrees", "detached"), "")
	f.Commit(detached, "d1.txt", "1\n")
	f.Commit(detached, "d2.txt", "2\n")

	unpushed := f.Worktree("unpushed")
	f.Commit(unpushed, "u.txt", "u\n")

	deadLock := f.Worktree("dead-lock")
	f.Git(f.Repo, "worktree", "lock", "--reason", "claude agent dead (pid 2147480000 start Mon Oct  5 11:42:02 2026)", deadLock)

	ownLock := f.Worktree("own-lock")
	f.Git(f.Repo, "worktree", "lock", "--reason", "claude agent own (pid "+strconv.Itoa(os.Getpid())+" start "+ownStart(t)+")", ownLock)

	reusedPID := f.Worktree("reused-pid")
	f.Git(f.Repo, "worktree", "lock", "--reason", "claude agent old (pid "+strconv.Itoa(os.Getpid())+" start Mon Jan  1 00:00:00 2024)", reusedPID)

	stale := f.Worktree("stale")
	require.NoError(t, os.RemoveAll(stale))

	f.Fetch()

	cases := []struct {
		name    string
		path    string
		check   func(t *testing.T, s *finding.WorktreeState)
		verdict finding.Verdict
		reason  string
	}{
		{"clean merged", ancestor, func(t *testing.T, s *finding.WorktreeState) {
			require.Equal(t, finding.MergedAncestor, s.Merged)
			require.Equal(t, "origin/ancestor", s.Upstream)
			require.Equal(t, "origin/main", s.DefaultBranch)
			require.False(t, s.FetchedAt.IsZero())
		}, finding.VerdictRemovable, ""},
		{"clean squash merged", squash, func(t *testing.T, s *finding.WorktreeState) {
			require.Equal(t, finding.MergedSquash, s.Merged)
		}, finding.VerdictRemovable, ""},
		{"clean rebase merged", rebase, func(t *testing.T, s *finding.WorktreeState) {
			require.Equal(t, finding.MergedRebase, s.Merged)
		}, finding.VerdictRemovable, ""},
		{"clean pushed unmerged", pushed, func(t *testing.T, s *finding.WorktreeState) {
			require.Equal(t, finding.MergedNo, s.Merged)
			require.Zero(t, s.Unpushed)
			require.Contains(t, s.Hints, "gh pr create --head pushed")
		}, finding.VerdictPushed, ""},
		{"modified", modified, func(t *testing.T, s *finding.WorktreeState) {
			require.Equal(t, 1, s.Modified)
			require.Equal(t, 1, s.Untracked)
			require.Contains(t, s.Hints, "git -C "+modified+" add -A && git -C "+modified+" commit")
			require.Equal(t, "git -C "+f.Repo+" worktree remove --force "+modified, s.ForceCommand)
		}, finding.VerdictAttention, "1 modified and 1 untracked files"},
		{"untracked only", untracked, func(t *testing.T, s *finding.WorktreeState) {
			require.Zero(t, s.Modified)
			require.Equal(t, 2, s.Untracked)
		}, finding.VerdictAttention, "2 untracked files"},
		{"ignored .env only", ignored, func(t *testing.T, s *finding.WorktreeState) {
			require.Equal(t, 1, s.Ignored)
			require.Equal(t, []string{".env"}, s.IgnoredSample)
			require.Zero(t, s.Untracked)
		}, finding.VerdictAttention, "1 ignored file: .env"},
		{"ignored artifact folders do not count", artifacts, func(t *testing.T, s *finding.WorktreeState) {
			require.Zero(t, s.Ignored)
		}, finding.VerdictRemovable, ""},
		{"detached with loose commits", detached, func(t *testing.T, s *finding.WorktreeState) {
			require.True(t, s.Detached)
			require.Empty(t, s.Branch)
			require.Equal(t, 2, s.Loose)
			require.Equal(t, 2, s.Unpushed)
			require.Contains(t, s.Hints, "git -C "+detached+" branch rescue-"+s.Head[:12])
		}, finding.VerdictAttention, "detached with 2 commits no branch holds"},
		{"unpushed on a branch", unpushed, func(t *testing.T, s *finding.WorktreeState) {
			require.Zero(t, s.Loose)
			require.Equal(t, 1, s.Unpushed)
			require.Contains(t, s.Hints, "git -C "+unpushed+" push -u origin unpushed")
		}, finding.VerdictAttention, "1 commit on no remote"},
		{"locked by a dead pid", deadLock, func(t *testing.T, s *finding.WorktreeState) {
			require.True(t, s.Locked)
			require.False(t, s.LockLive)
			require.Equal(t, "claude-code", s.Tool)
			require.Contains(t, s.Hints, "git -C "+f.Repo+" worktree unlock "+deadLock)
			require.Equal(t, "git -C "+f.Repo+" worktree remove --force --force "+deadLock, s.ForceCommand)
		}, finding.VerdictAttention, "locked by a process that is no longer running"},
		{"locked by the test's own pid", ownLock, func(t *testing.T, s *finding.WorktreeState) {
			require.True(t, s.LockLive)
			require.Empty(t, s.ForceCommand)
		}, finding.VerdictInUse, "locked by a running process"},
		{"locked by a reused pid", reusedPID, func(t *testing.T, s *finding.WorktreeState) {
			require.False(t, s.LockLive, "the process with this id started at another time")
		}, finding.VerdictAttention, "no longer running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := worktree.Classify(ctx, osEnv(), tc.path)
			require.NoError(t, err)
			require.Equal(t, f.Repo, s.Repo)
			require.Equal(t, tc.path, s.Path)
			require.True(t, s.Exists)
			require.NotEmpty(t, s.Head)
			require.Empty(t, s.Unknown, s.Problem)
			require.Equal(t, tc.verdict, s.Verdict, "reasons: %v", s.Reasons)
			if tc.reason != "" {
				require.Contains(t, strings.Join(s.Reasons, "; "), tc.reason)
			} else {
				require.Empty(t, s.Reasons)
			}
			if tc.check != nil {
				tc.check(t, s)
			}
		})
	}

	t.Run("stale registration", func(t *testing.T) {
		_, err := worktree.Classify(ctx, osEnv(), stale)
		require.ErrorIs(t, err, worktree.ErrNotLinked)
		repo, err := worktree.OpenRepo(ctx, osEnv(), f.Repo)
		require.NoError(t, err)
		require.Len(t, repo.Stale(), 1)
		require.Equal(t, stale, repo.Stale()[0].Path)
		require.NotContains(t, paths(repo.Linked()), stale)
	})

	t.Run("main checkout is not a linked worktree", func(t *testing.T) {
		_, err := worktree.Classify(ctx, osEnv(), f.Repo)
		require.ErrorIs(t, err, worktree.ErrNotLinked)
		require.ErrorContains(t, err, "main checkout")
	})
}

func paths(es []worktree.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Path)
	}
	return out
}

// TestClassifyNoRemote checks a repository without a remote: every commit is
// on no remote, so a worktree with its own commits is never removable, and
// the merge state is checked against the local main branch.
func TestClassifyNoRemote(t *testing.T) {
	f := worktreetest.NewLocal(t)
	w := f.Worktree("local")
	f.Commit(w, "x.txt", "x\n")
	clean := f.Worktree("clean")

	s, err := worktree.Classify(context.Background(), osEnv(), w)
	require.NoError(t, err)
	require.Equal(t, 2, s.Unpushed)
	require.Equal(t, "main", s.DefaultBranch)
	require.Empty(t, s.Origin)
	require.Equal(t, finding.VerdictAttention, s.Verdict)

	s, err = worktree.Classify(context.Background(), osEnv(), clean)
	require.NoError(t, err)
	require.Equal(t, finding.MergedAncestor, s.Merged)
	require.Equal(t, 1, s.Unpushed, "the first commit is on no remote either")
	require.Equal(t, finding.VerdictAttention, s.Verdict)
}

// TestClassifyFromSubfolder checks that FindRoot finds the worktree from a
// folder inside it, as done does when run in a subfolder.
func TestClassifyFromSubfolder(t *testing.T) {
	f := worktreetest.New(t)
	w := f.Worktree("sub")
	f.Write(w, "pkg/a/a.txt", "a\n")
	f.Git(w, "add", "-A")
	f.Git(w, "commit", "-q", "-m", "a")
	root, ok := worktree.FindRoot(filepath.Join(w, "pkg", "a"))
	require.True(t, ok)
	require.Equal(t, w, root)
}

func TestLocate(t *testing.T) {
	f := worktreetest.New(t)
	w := f.Worktree("loc")
	loc, err := worktree.Locate(w)
	require.NoError(t, err)
	require.Equal(t, f.Repo, loc.Repo)
	require.Equal(t, filepath.Join(f.Repo, ".git", "worktrees", "loc"), loc.GitDir)

	_, err = worktree.Locate(f.Repo)
	require.ErrorIs(t, err, worktree.ErrNotLinked)

	plain := filepath.Join(f.Dir, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	_, err = worktree.Locate(plain)
	require.ErrorIs(t, err, worktree.ErrNotLinked)

	// A .git file that names a folder outside .git/worktrees is refused.
	odd := filepath.Join(f.Dir, "odd")
	require.NoError(t, os.MkdirAll(odd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(odd, ".git"), []byte("gitdir: "+filepath.Join(f.Repo, ".git")+"\n"), 0o644))
	_, err = worktree.Locate(odd)
	require.ErrorIs(t, err, worktree.ErrNotLinked)
}

// TestSquashMergedExactly covers the squash merge after the remote deleted
// the branch, as GitHub does: tier A only when the change is exactly in a
// default branch on a remote. A whitespace only difference, which git
// cherry's patch ids ignore, is not merged, and a local only default branch
// never makes commits on no remote safe.
func TestSquashMergedExactly(t *testing.T) {
	ctx := context.Background()

	t.Run("remote branch deleted after the squash merge", func(t *testing.T) {
		f := worktreetest.New(t)
		w := f.Worktree("gone-upstream")
		f.Commit(w, "a.txt", "a\n")
		f.Commit(w, "b.txt", "b\n")
		f.Push(w, "gone-upstream")
		f.MergeSquash("gone-upstream")
		f.Git(f.Repo, "push", "-q", "origin", "--delete", "gone-upstream")
		s, err := worktree.Classify(ctx, osEnv(), w)
		require.NoError(t, err)
		require.Equal(t, 2, s.Unpushed)
		require.Equal(t, finding.MergedSquash, s.Merged)
		require.True(t, s.MergedRemotely())
		require.Equal(t, finding.VerdictRemovable, s.Verdict, "%v", s.Reasons)
		require.Contains(t, worktree.Words(s), "merged by squash into origin/main; the branch is not on the remote")
		require.NotContains(t, strings.Join(s.Hints, "\n"), "push")
	})

	t.Run("a whitespace only change after the squash merge", func(t *testing.T) {
		f := worktreetest.New(t)
		w := f.Worktree("spacing")
		f.Commit(w, "a.txt", "one\ntwo\n")
		f.Commit(w, "b.txt", "b\n")
		f.Push(w, "spacing")
		f.MergeSquash("spacing")
		f.Commit(w, "a.txt", "one\n    two\n")
		f.Push(w, "spacing")
		s, err := worktree.Classify(ctx, osEnv(), w)
		require.NoError(t, err)
		require.Equal(t, finding.MergedNo, s.Merged)
		require.Equal(t, "the branch's changes are not exactly in origin/main", s.MergeNote)
		require.NotEqual(t, finding.VerdictRemovable, s.Verdict)
		require.Contains(t, worktree.Words(s), "not exactly in origin/main")
	})

	t.Run("squash merged into a local only default branch", func(t *testing.T) {
		f := worktreetest.NewLocal(t)
		w := f.Worktree("local-squash")
		f.Commit(w, "a.txt", "a\n")
		f.Commit(w, "b.txt", "b\n")
		f.Git(f.Repo, "merge", "-q", "--squash", "local-squash")
		f.Git(f.Repo, "commit", "-q", "-m", "squash local-squash")
		s, err := worktree.Classify(ctx, osEnv(), w)
		require.NoError(t, err)
		require.Equal(t, finding.MergedSquash, s.Merged)
		require.Equal(t, "refs/heads/main", s.DefaultRef)
		require.False(t, s.MergedRemotely())
		require.Equal(t, finding.VerdictAttention, s.Verdict)
		require.Contains(t, strings.Join(s.Reasons, "; "), "commits on no remote")
	})
}

// TestClassifyIgnoresInheritedGitEnvironment classifies a dirty worktree
// while GIT_DIR and GIT_WORK_TREE point at the clean main checkout, as they
// do inside a git hook: the worktree must still be judged on its own state.
func TestClassifyIgnoresInheritedGitEnvironment(t *testing.T) {
	f := worktreetest.New(t)
	w := f.Worktree("hooked")
	f.Commit(w, "a.txt", "a\n")
	f.Push(w, "hooked")
	f.MergeFF("hooked")
	f.Write(w, "a.txt", "changed\n")
	f.Write(w, ".env", "SECRET=1\n")
	clean := f.Worktree("clean")
	cleanHead := f.Git(clean, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(f.Repo, ".git"))
	t.Setenv("GIT_WORK_TREE", f.Repo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(f.Repo, ".git", "index"))
	s, err := worktree.Classify(context.Background(), osEnv(), w)
	require.NoError(t, err)
	require.Equal(t, 1, s.Modified)
	require.Equal(t, 1, s.Ignored)
	require.Equal(t, finding.VerdictAttention, s.Verdict)
	require.NoError(t, worktree.Recheck(context.Background(), osEnv(), clean, cleanHead), "a clean worktree passes the recheck")
	require.Error(t, worktree.Recheck(context.Background(), osEnv(), w, s.Head))
}

// TestClassifyHiddenChanges finds changes that git status cannot see,
// because the file is marked assume-unchanged or skip-worktree, which git
// worktree remove would delete without complaint.
func TestClassifyHiddenChanges(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			f := worktreetest.New(t)
			w := f.Worktree("hidden")
			f.Commit(w, "config.json", "{}\n")
			f.Push(w, "hidden")
			f.MergeFF("hidden")
			f.Write(w, "config.json", `{"token":"local"}`+"\n")
			f.Git(w, "update-index", flag, "config.json")
			require.Empty(t, f.Git(w, "status", "--porcelain"), "git status cannot see the change")
			s, err := worktree.Classify(context.Background(), osEnv(), w)
			require.NoError(t, err)
			require.Contains(t, s.Unknown, "hidden-changes")
			require.Contains(t, strings.Join(s.Blockers, "; "), "assume-unchanged or skip-worktree")
			require.Contains(t, strings.Join(s.Reasons, "; "), "assume-unchanged or skip-worktree")
			require.Equal(t, finding.VerdictAttention, s.Verdict)
			require.Error(t, worktree.Recheck(context.Background(), osEnv(), w, s.Head))
		})
	}
}

// TestClassifyBlockers covers what git worktree remove would delete although
// git status shows nothing: a repository inside an ignored artifact folder
// with its own commit, change and untracked file, and a submodule, whose
// repository git keeps inside the worktree's registration.
func TestClassifyBlockers(t *testing.T) {
	ctx := context.Background()
	merged := func(f *worktreetest.Fixture, name string) string {
		w := f.Worktree(name)
		f.Commit(w, name+".txt", name+"\n")
		f.Push(w, name)
		f.MergeFF(name)
		return w
	}

	t.Run("nested repository in node_modules", func(t *testing.T) {
		f := worktreetest.New(t)
		w := merged(f, "nested")
		dep := filepath.Join(w, "node_modules", "dep")
		f.Write(dep, "index.js", "v1\n")
		f.Git(dep, "init", "-q")
		f.Git(dep, "add", "-A")
		f.Git(dep, "commit", "-q", "-m", "local only")
		f.Write(dep, "index.js", "v2\n")
		f.Write(dep, "new.js", "untracked\n")
		s, err := worktree.Classify(ctx, osEnv(), w)
		require.NoError(t, err)
		require.Zero(t, s.Modified+s.Untracked+s.Ignored, "git status sees nothing")
		require.Contains(t, s.Unknown, "nested-repositories")
		require.Equal(t, []string{"nested repositories: node_modules/dep"}, s.Blockers)
		require.Equal(t, finding.VerdictAttention, s.Verdict)
		require.Contains(t, s.Reasons, "nested repositories: node_modules/dep")
		require.Contains(t, worktree.Words(s), "nested repositories: node_modules/dep")
		require.NotEmpty(t, s.ForceCommand)
		require.Error(t, worktree.Recheck(ctx, osEnv(), w, s.Head))
	})

	t.Run("submodule", func(t *testing.T) {
		f := worktreetest.New(t)
		lib := filepath.Join(f.Dir, "lib")
		f.Git(f.Dir, "init", "-q", "-b", "main", lib)
		f.Commit(lib, "lib.txt", "lib\n")
		w := f.Worktree("sub")
		f.Git(w, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "lib")
		f.Git(w, "commit", "-q", "-m", "add lib")
		f.Push(w, "sub")
		f.MergeFF("sub")
		s, err := worktree.Classify(ctx, osEnv(), w)
		require.NoError(t, err)
		require.Contains(t, s.Unknown, "submodules")
		require.Contains(t, s.Blockers, "holds 1 submodule; git worktree remove cannot remove it without --force")
		require.Equal(t, finding.VerdictAttention, s.Verdict)
		require.Contains(t, s.Hints, "git -C "+w+" submodule foreach git push")
		require.Error(t, worktree.Recheck(ctx, osEnv(), w, s.Head))
	})

	t.Run("commits that undo each other", func(t *testing.T) {
		f := worktreetest.New(t)
		w := f.Worktree("revert")
		f.Commit(w, "x.txt", "x\n")
		f.Git(w, "rm", "-q", "x.txt")
		f.Git(w, "commit", "-q", "-m", "undo")
		f.Push(w, "revert")
		s, err := worktree.Classify(ctx, osEnv(), w)
		require.NoError(t, err)
		require.Equal(t, finding.MergedNo, s.Merged)
		require.Equal(t, "its commits add up to no change", s.MergeNote)
		require.Equal(t, finding.VerdictPushed, s.Verdict)
	})
}
