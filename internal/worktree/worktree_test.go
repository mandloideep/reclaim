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
	var out []string
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
