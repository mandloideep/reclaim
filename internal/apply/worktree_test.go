package apply

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/worktree"
	"github.com/mandloideep/reclaim/internal/worktree/worktreetest"
)

// worktreeAction classifies the worktree at path the way the scanner does
// and turns it into a plan action of the given tier.
func worktreeAction(t *testing.T, path string, tier finding.Tier) plan.Action {
	t.Helper()
	s, err := worktree.Classify(context.Background(), worktree.Env{Exec: execx.OS{}}, path)
	require.NoError(t, err)
	f := finding.Finding{
		ID: finding.MakeID("git-worktrees", path), Scanner: "git-worktrees", Category: finding.CategoryWorktree,
		Tier: tier, Path: path, Kind: finding.KindDir, Target: path, Repo: s.Repo, Worktree: s,
		Action: finding.ActionGitWorktreeRemove, Command: plan.WorktreeRemoveCommand(s.Repo, path),
	}
	return plan.FromFinding(&f)
}

// gitRun runs git for real, so apply's own command runs against the fixture.
func gitRun(mod func(*Options)) func(*Options) {
	return func(o *Options) {
		o.Exec = execx.OS{}
		if mod != nil {
			mod(o)
		}
	}
}

// snapshot lists every file and folder under dir, except git's object
// store, which the squash merge check adds a dangling object to.
func snapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "objects" && filepath.Base(filepath.Dir(p)) == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	}))
	sort.Strings(out)
	return out
}

// without returns the entries of before that are not the given folders or
// inside them.
func without(before []string, dirs ...string) []string {
	return slices.DeleteFunc(slices.Clone(before), func(p string) bool {
		return slices.ContainsFunc(dirs, func(d string) bool { return p == d || strings.HasPrefix(p, d+string(filepath.Separator)) })
	})
}

// TestApplyRemovesOnlyTheWorktree removes one merged worktree and proves
// that only its folder and its registration are gone: every other worktree,
// every branch, the main checkout and a stale registration of another folder
// are untouched.
func TestApplyRemovesOnlyTheWorktree(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}

	target := f.Worktree("target")
	f.Commit(target, "t.txt", "t\n")
	f.Write(target, "node_modules/x/index.js", "x\n")
	f.Push(target, "target")
	f.MergeSquash("target")

	other := f.Worktree("other")
	f.Commit(other, "o.txt", "o\n")
	f.Push(other, "other")
	dirty := f.Worktree("dirty")
	f.Write(dirty, "README.md", "dirty\n")
	gone := f.Worktree("gone")
	require.NoError(t, os.RemoveAll(gone))

	a := worktreeAction(t, target, finding.TierA)
	branches := f.Git(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes")
	before := snapshot(t, f.Dir)

	sum, log, err := run(t, e, newPlan([]string{f.Dir}, a), gitRun(nil))
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status, "%v", sum.Outcomes[0].Err)
	require.Positive(t, sum.Freed)

	relTarget, _ := filepath.Rel(f.Dir, target)
	relReg := filepath.Join("repo", ".git", "worktrees", "target")
	after := snapshot(t, f.Dir)
	require.Equal(t, without(before, relTarget, relReg), withoutGitBookkeeping(after, before))
	require.Equal(t, branches, f.Git(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes"),
		"no branch is deleted or moved")
	require.True(t, exists(filepath.Join(f.Repo, ".git", "worktrees", "gone")), "a stale registration the plan does not cover stays")

	// The log records the six checks, all passed.
	var checks []any
	for _, entry := range log {
		if entry["event"] == "action" {
			checks = entry["checks"].([]any)
		}
	}
	require.Len(t, checks, 6)
	for i, name := range []string{CheckLinked, CheckPath, CheckState, CheckHead, CheckWorkdir, CheckRemove} {
		c := checks[i].(map[string]any)
		require.Equal(t, name, c["name"])
		require.Equal(t, true, c["ok"])
	}
	require.Contains(t, sum.Outcomes[0].Output, "git worktree prune not run")
}

// withoutGitBookkeeping keeps the entries of after that existed before or
// that git creates on its own while reading, such as a refreshed index lock
// or reflog folder, so the comparison is about what apply removed.
func withoutGitBookkeeping(after, before []string) []string {
	return slices.DeleteFunc(slices.Clone(after), func(p string) bool {
		return !slices.Contains(before, p)
	})
}

// TestApplyNeverPrunesAfterRemove checks that after a removal apply only
// asks git, with a dry run, what a prune would remove, and never prunes.
func TestApplyNeverPrunesAfterRemove(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	target := f.Worktree("target")
	f.Commit(target, "t.txt", "t\n")
	f.Push(target, "target")
	f.MergeFF("target")
	a := worktreeAction(t, target, finding.TierA)
	var ran [][]string
	sum, _, err := run(t, e, newPlan([]string{f.Dir}, a), func(o *Options) {
		o.Exec = recorder{Runner: execx.OS{}, ran: &ran}
	})
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.Equal(t, [][]string{plan.WorktreeRemoveCommand(f.Repo, target),
		{"git", "-C", f.Repo, "worktree", "prune", "--dry-run", "--verbose"}}, ran)
	require.False(t, exists(target))
}

// recorder runs commands with a real runner and records Run calls. before,
// when set, runs first, to change the fixture right before git acts.
type recorder struct {
	execx.Runner
	ran    *[][]string
	before func()
}

func (r recorder) Run(ctx context.Context, argv []string) ([]byte, error) {
	if r.before != nil {
		r.before()
	}
	*r.ran = append(*r.ran, slices.Clone(argv))
	return r.Runner.Run(ctx, argv)
}

// TestApplyWorktreeRefusals changes the fixture after the plan is written
// and checks that apply refuses, names the failed check, gives the hints
// when work would be lost, and leaves the worktree and everything else in
// place.
func TestApplyWorktreeRefusals(t *testing.T) {
	type setup struct {
		f      *worktreetest.Fixture
		path   string
		action plan.Action
	}
	cases := []struct {
		name string
		// tier is the tier the action is planned at, A when empty.
		tier finding.Tier
		// prepare makes the worktree, which starts merged and clean.
		prepare func(f *worktreetest.Fixture, path string)
		// change runs after the plan is written.
		change func(t *testing.T, s *setup)
		opts   func(o *Options, s *setup)
		check  string
		detail string
		hint   string
	}{
		{name: "modified after the plan was written", check: CheckState, detail: "1 modified files",
			change: func(_ *testing.T, s *setup) { s.f.Write(s.path, "README.md", "changed\n") },
			hint:   "add -A"},
		{name: "untracked file after the plan was written", check: CheckState, detail: "1 untracked files",
			change: func(_ *testing.T, s *setup) { s.f.Write(s.path, "notes.txt", "mine\n") }},
		{name: "ignored .env after the plan was written", check: CheckState, detail: "ignored files",
			change: func(_ *testing.T, s *setup) { s.f.Write(s.path, ".env", "SECRET=1\n") }},
		{name: "received a commit after the plan was written", check: CheckLinked, detail: "the plan was made at",
			change: func(_ *testing.T, s *setup) { s.f.Commit(s.path, "late.txt", "late\n") }},
		{name: "received a pushed and merged commit", check: CheckLinked, detail: "the plan was made at",
			change: func(_ *testing.T, s *setup) {
				s.f.Commit(s.path, "late.txt", "late\n")
				s.f.Push(s.path, filepath.Base(s.path))
				s.f.MergeFF(filepath.Base(s.path))
			}},
		{name: "lost its remote after the plan was written", check: CheckState, detail: "commits on no remote",
			change: func(_ *testing.T, s *setup) { s.f.Git(s.f.Repo, "remote", "remove", "origin") }},
		{name: "locked by this process", check: CheckState, detail: "it is locked",
			change: func(_ *testing.T, s *setup) {
				s.f.Git(s.f.Repo, "worktree", "lock", "--reason", "in use", s.path)
			}},
		{name: "unmerged at tier A, with a forged state", check: CheckState, detail: "not ancestor, rebase or squash",
			prepare: func(f *worktreetest.Fixture, path string) {
				f.Commit(path, "p.txt", "p\n")
				f.Push(path, filepath.Base(path))
			},
			change: func(_ *testing.T, s *setup) {
				// A hand edit that claims the merge passes plan validation;
				// apply computes the state again and refuses.
				s.action.Worktree.Merged = finding.MergedSquash
			}},
		{name: "remote branch deleted at tier B", tier: finding.TierB, check: CheckState, detail: "commits on no remote",
			prepare: func(f *worktreetest.Fixture, path string) {
				f.Commit(path, "p.txt", "p\n")
				f.Push(path, filepath.Base(path))
			},
			change: func(_ *testing.T, s *setup) { s.f.Git(s.f.Repo, "push", "-q", "origin", "--delete", "wt") }, hint: "push -u origin"},
		{name: "working directory inside it", check: CheckWorkdir, detail: "run it from the main checkout",
			opts: func(o *Options, s *setup) {
				o.Getwd = func() (string, error) { return filepath.Join(s.path, "sub"), nil }
			}},
		{name: "outside the recorded roots", check: CheckPath, detail: "outside the roots",
			opts: func(*Options, *setup) {}, change: func(*testing.T, *setup) {}},
		{name: "replaced by a symbolic link", check: CheckLinked,
			change: func(t *testing.T, s *setup) {
				moved := s.path + "-moved"
				require.NoError(t, os.Rename(s.path, moved))
				require.NoError(t, os.Symlink(moved, s.path))
			}},
		{name: "a .git directory instead of a .git file", check: CheckLinked, detail: "main checkout",
			change: func(t *testing.T, s *setup) {
				require.NoError(t, os.Remove(filepath.Join(s.path, ".git")))
				require.NoError(t, os.MkdirAll(filepath.Join(s.path, ".git"), 0o755))
			}},
		{name: "git refuses a file that appears at the last moment", check: CheckRemove, detail: "git refused",
			opts: func(o *Options, s *setup) {
				var ran [][]string
				o.Exec = recorder{Runner: execx.OS{}, ran: &ran, before: func() {
					s.f.Write(s.path, "late.txt", "written while apply ran\n")
				}}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := worktreetest.New(t)
			e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
			name := "wt"
			path := f.Worktree(name)
			if tc.prepare != nil {
				tc.prepare(f, path)
			} else {
				f.Commit(path, "w.txt", "w\n")
				f.Push(path, name)
				f.MergeFF(name)
			}
			tier := tc.tier
			if tier == "" {
				tier = finding.TierA
			}
			s := &setup{f: f, path: path}
			s.action = worktreeAction(t, path, tier)
			if tc.change != nil {
				tc.change(t, s)
			}
			roots := []string{f.Dir}
			if tc.name == "outside the recorded roots" {
				roots = []string{filepath.Join(worktreetest.RealTempDir(t), "elsewhere")}
			}
			sum, log, err := run(t, e, newPlan(roots, s.action), gitRun(func(o *Options) {
				if tc.opts != nil {
					tc.opts(o, s)
				}
			}))
			require.Error(t, err)
			o := sum.Outcomes[0]
			require.Equal(t, StatusFailed, o.Status)
			require.NotNil(t, o.Refusal, "%v", o.Err)
			require.Equal(t, tc.check, o.Refusal.Check, "%v", o.Err)
			require.Contains(t, o.Err.Error(), tc.detail)
			if tc.hint != "" {
				require.Contains(t, strings.Join(o.Refusal.Hints, "\n"), tc.hint)
				require.Contains(t, o.Refusal.ForceCommand, "worktree remove --force")
				require.Contains(t, o.Err.Error(), o.Refusal.ForceCommand)
			}
			require.True(t, exists(path), "the worktree stays")
			require.True(t, exists(filepath.Join(f.Repo, ".git", "worktrees", name)), "its registration stays")
			// The last check in the log is the one that failed.
			for _, entry := range log {
				if entry["event"] == "action" {
					checks := entry["checks"].([]any)
					last := checks[len(checks)-1].(map[string]any)
					require.Equal(t, tc.check, last["name"])
					require.Equal(t, false, last["ok"])
				}
			}
		})
	}
}

// TestApplyRemovesTierBWorktree removes a pushed, unmerged worktree planned
// at tier B, as select does when the user ticks it.
func TestApplyRemovesTierBWorktree(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	path := f.Worktree("pushed")
	f.Commit(path, "p.txt", "p\n")
	f.Push(path, "pushed")
	sum, _, err := run(t, e, newPlan([]string{f.Dir}, worktreeAction(t, path, finding.TierB)), gitRun(nil))
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.False(t, exists(path))
	require.Equal(t, "pushed", f.Git(f.Repo, "branch", "--list", "pushed", "--format=%(refname:short)"), "the branch stays")
}

// TestApplyWorktreeAlreadyGone reports a removed worktree as already gone.
func TestApplyWorktreeAlreadyGone(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	path := f.Worktree("gone")
	a := worktreeAction(t, path, finding.TierA)
	f.Git(f.Repo, "worktree", "remove", path)
	sum, _, err := run(t, e, newPlan([]string{f.Dir}, a), gitRun(nil))
	require.NoError(t, err)
	require.Equal(t, StatusGone, sum.Outcomes[0].Status)
}

// TestProveWorktreeRefusesTheMainCheckout drives the proof directly with an
// action whose path is the main checkout, which plan validation already
// refuses, to show the proof refuses it on its own.
func TestProveWorktreeRefusesTheMainCheckout(t *testing.T) {
	f := worktreetest.New(t)
	path := f.Worktree("x")
	a := worktreeAction(t, path, finding.TierA)
	a.Path, a.Target, a.Worktree.Path = f.Repo, f.Repo, f.Repo
	_, err := ProveWorktree(context.Background(), Options{Exec: execx.OS{}}, []string{f.Dir}, &a)
	var ref *Refusal
	require.True(t, errors.As(err, &ref))
	require.Equal(t, CheckLinked, ref.Check)
	require.True(t, exists(filepath.Join(f.Repo, "README.md")))
}

func pruneAction(repo string, regs ...string) plan.Action {
	target := plan.WorktreePruneTarget(repo)
	return plan.Action{
		ID: finding.MakeID("worktree-registrations", target), Action: finding.ActionGitWorktreePrune, Target: target,
		Tier: finding.TierA, Scanner: "worktree-registrations", Repo: repo, Registrations: regs,
		Command: plan.WorktreePruneCommand(repo),
	}
}

// TestApplyPrunesOnlyStaleRegistrations proves that stale registrations go
// and the registrations of worktrees that exist, locked or not, stay.
func TestApplyPrunesOnlyStaleRegistrations(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	live := f.Worktree("live")
	locked := f.Worktree("locked")
	f.Git(f.Repo, "worktree", "lock", locked)
	stale1 := f.Worktree("stale1")
	stale2 := f.Worktree("stale2")
	require.NoError(t, os.RemoveAll(stale1))
	require.NoError(t, os.RemoveAll(stale2))

	sum, _, err := run(t, e, newPlan([]string{f.Dir}, pruneAction(f.Repo, stale1, stale2)), gitRun(nil))
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	regs := filepath.Join(f.Repo, ".git", "worktrees")
	require.False(t, exists(filepath.Join(regs, "stale1")))
	require.False(t, exists(filepath.Join(regs, "stale2")))
	require.True(t, exists(filepath.Join(regs, "live")))
	require.True(t, exists(filepath.Join(regs, "locked")))
	require.True(t, exists(live))
	require.True(t, exists(locked))

	// Nothing stale is left, so a second run reports it as already gone.
	sum, _, err = run(t, e, newPlan([]string{f.Dir}, pruneAction(f.Repo, stale1, stale2)), gitRun(nil))
	require.NoError(t, err)
	require.Equal(t, StatusGone, sum.Outcomes[0].Status)
}

// TestApplyRefusesToPruneUnlistedRegistrations refuses a prune when git
// would also prune a registration that went stale after the scan.
func TestApplyRefusesToPruneUnlistedRegistrations(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	stale := f.Worktree("stale")
	later := f.Worktree("later")
	require.NoError(t, os.RemoveAll(stale))
	require.NoError(t, os.RemoveAll(later))
	sum, _, err := run(t, e, newPlan([]string{f.Dir}, pruneAction(f.Repo, stale)), gitRun(nil))
	require.Error(t, err)
	require.ErrorContains(t, sum.Outcomes[0].Err, "which the plan does not list")
	require.True(t, exists(filepath.Join(f.Repo, ".git", "worktrees", "stale")))
	require.True(t, exists(filepath.Join(f.Repo, ".git", "worktrees", "later")))
}

// TestApplyRefusesToPruneAFolderThatStillExists refuses a prune when git
// would remove a registration whose gitdir file is missing while its folder,
// with its work, still exists; git worktree list does not show that one.
func TestApplyRefusesToPruneAFolderThatStillExists(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	stale := f.Worktree("stale")
	require.NoError(t, os.RemoveAll(stale))
	orphan := f.Worktree("orphan")
	f.Write(orphan, "work.txt", "uncommitted work\n")
	require.NoError(t, os.Remove(filepath.Join(f.Repo, ".git", "worktrees", "orphan", "gitdir")))
	require.NotContains(t, f.Git(f.Repo, "worktree", "list"), orphan)

	reg := filepath.Join(f.Repo, ".git", "worktrees", "orphan")
	for _, regs := range [][]string{{stale}, {stale, reg}} {
		sum, _, err := run(t, e, newPlan([]string{f.Dir}, pruneAction(f.Repo, regs...)), gitRun(nil))
		require.Error(t, err)
		require.ErrorContains(t, sum.Outcomes[0].Err, "refusing to prune")
		require.DirExists(t, reg, "the registration of a folder that still exists stays")
		require.FileExists(t, filepath.Join(orphan, "work.txt"))
	}
}

// TestRemoveEmptyDir removes a folder planned to be removed once empty, and
// refuses one that still holds anything, such as a worktree whose removal
// failed earlier in the run.
func TestRemoveEmptyDir(t *testing.T) {
	e := newEnv(t)
	empty := filepath.Join(e.root, "tasks", "a1")
	full := filepath.Join(e.root, "tasks", "b2")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	write(t, filepath.Join(full, "repo", ".git"), 10)
	write(t, filepath.Join(full, "repo", "work.txt"), 10)

	a, b := removeAction(empty, finding.KindEmptyDir), removeAction(full, finding.KindEmptyDir)
	sum, _, err := run(t, e, newPlan([]string{e.root}, a, b), func(o *Options) { o.KeepGoing = true })
	require.Error(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.False(t, exists(empty))
	require.Equal(t, StatusFailed, sum.Outcomes[1].Status)
	require.True(t, exists(filepath.Join(full, "repo", "work.txt")))
}

// TestApplyRemovesSquashMergedWorktreeWithDeletedBranch removes a worktree
// whose branch the remote deleted after a squash merge: its commits are on
// no remote, but its change is exactly in origin/main.
func TestApplyRemovesSquashMergedWorktreeWithDeletedBranch(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	path := f.Worktree("deleted")
	f.Commit(path, "a.txt", "a\n")
	f.Commit(path, "b.txt", "b\n")
	f.Push(path, "deleted")
	f.MergeSquash("deleted")
	f.Git(f.Repo, "push", "-q", "origin", "--delete", "deleted")
	a := worktreeAction(t, path, finding.TierA)
	require.Equal(t, 2, a.Worktree.Unpushed)
	sum, _, err := run(t, e, newPlan([]string{f.Dir}, a), gitRun(nil))
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.False(t, exists(path))
	require.Equal(t, "deleted", f.Git(f.Repo, "branch", "--list", "deleted", "--format=%(refname:short)"), "the local branch stays")
}

// TestApplyRefusesToPruneAFolderWithoutItsGitFile refuses a prune whose
// registration points at a folder that lost its .git file but still holds
// files, which git reports as stale.
func TestApplyRefusesToPruneAFolderWithoutItsGitFile(t *testing.T) {
	f := worktreetest.New(t)
	e := env{root: f.Dir, home: filepath.Join(worktreetest.RealTempDir(t), "home", "me")}
	kept := f.Worktree("kept")
	f.Write(kept, "notes.md", "mine\n")
	require.NoError(t, os.Remove(filepath.Join(kept, ".git")))
	sum, _, err := run(t, e, newPlan([]string{f.Dir}, pruneAction(f.Repo, kept)), gitRun(nil))
	require.Error(t, err)
	require.ErrorContains(t, sum.Outcomes[0].Err, "whose folder may still hold work")
	require.DirExists(t, filepath.Join(f.Repo, ".git", "worktrees", "kept"))
	require.FileExists(t, filepath.Join(kept, "notes.md"))
}
