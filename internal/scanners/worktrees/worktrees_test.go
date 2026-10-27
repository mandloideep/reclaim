package worktrees_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
	"github.com/mandloideep/reclaim/internal/worktree/worktreetest"
)

// mixed runs git and ps for real and gh through a fake, so the tests never
// reach GitHub.
type mixed struct {
	execx.OS
	gh *execx.Fake
}

func (m mixed) LookPath(name string) (string, error) {
	if name == "gh" {
		return m.gh.LookPath(name)
	}
	return m.OS.LookPath(name)
}

func (m mixed) Output(ctx context.Context, name string, args ...string) (string, error) {
	if name == "gh" {
		return m.gh.Output(ctx, name, args...)
	}
	return m.OS.Output(ctx, name, args...)
}

func envFor(home string, roots []string, runner execx.Runner) scan.Env {
	reg := scanners.New()
	return scan.Env{
		Roots: roots, Home: home, GOOS: "darwin", Now: time.Now(), Exec: runner,
		Getenv: func(string) string { return "" }, Walker: fsx.NewWalker(2),
		Projects: project.NewSource(roots, reg.ProjectMatchers(), project.Options{Home: home}),
	}
}

// runWorktrees runs the worktree scanners and returns their findings by
// scanner, failing on any warning.
func runWorktrees(t *testing.T, env scan.Env) map[string][]finding.Finding {
	t.Helper()
	var ss []scan.Scanner
	for _, s := range scanners.New().All() {
		if s.Category() == finding.CategoryWorktree {
			ss = append(ss, s)
		}
	}
	res := scan.Run(context.Background(), ss, env, 4)
	require.Empty(t, res.Warnings)
	out := map[string][]finding.Finding{}
	for _, f := range res.Findings {
		out[f.Scanner] = append(out[f.Scanner], f)
	}
	return out
}

func byPath(fs []finding.Finding) map[string]finding.Finding {
	out := map[string]finding.Finding{}
	for _, f := range fs {
		out[f.Target] = f
	}
	return out
}

func TestGitWorktrees(t *testing.T) {
	f := worktreetest.New(t)
	home := filepath.Join(worktreetest.RealTempDir(t), "Users", "me")
	require.NoError(t, os.MkdirAll(home, 0o755))

	merged := f.Worktree("merged")
	f.Commit(merged, "m1.txt", "m\n")
	f.Commit(merged, "m2.txt", "m\n")
	f.Push(merged, "merged")
	f.MergeSquash("merged")
	pushed := f.Worktree("pushed")
	f.Commit(pushed, "p.txt", "p\n")
	f.Push(pushed, "pushed")
	dirty := f.Worktree("dirty")
	f.Write(dirty, "README.md", "changed\n")
	inUse := f.Worktree("in-use")
	f.Git(f.Repo, "worktree", "lock", "--reason", "claude agent x (pid "+strconv.Itoa(os.Getpid())+")", inUse)
	gone := f.Worktree("gone")
	require.NoError(t, os.RemoveAll(gone))
	// A worktree outside the roots and the home folder is never offered.
	outside := f.WorktreeAt(filepath.Join(worktreetest.RealTempDir(t), "outside"), "outside")
	f.Commit(outside, "x.txt", "x\n")
	f.Push(outside, "outside")
	f.MergeFF("outside")
	// A second clone of the same origin.
	clone := filepath.Join(f.Dir, "zz-clone")
	f.Git(f.Dir, "clone", "-q", f.Remote, clone)
	// The origin looks like GitHub to reclaim; git still uses the local
	// bare repository.
	f.Git(f.Repo, "config", "url."+f.Dir+"/.insteadOf", "git@github.com:o/")
	f.Git(f.Repo, "remote", "set-url", "origin", "git@github.com:o/origin.git")
	f.Git(clone, "remote", "set-url", "origin", "https://github.com/o/origin")
	f.Fetch()

	gh := &execx.Fake{Tools: map[string]string{"gh": "/usr/bin/gh"}, Outputs: map[string]string{
		"gh pr list --repo o/origin --head merged --state all --json number,state,url":  `[{"number":3,"state":"MERGED","url":"https://github.com/o/origin/pull/3"}]`,
		"gh pr list --repo o/origin --head pushed --state all --json number,state,url":  `[{"number":4,"state":"OPEN","url":"https://github.com/o/origin/pull/4"}]`,
		"gh pr list --repo o/origin --head dirty --state all --json number,state,url":   `[]`,
		"gh pr list --repo o/origin --head in-use --state all --json number,state,url":  `[]`,
		"gh pr list --repo o/origin --head outside --state all --json number,state,url": `[]`,
	}}
	env := envFor(home, []string{f.Dir}, mixed{gh: gh})
	env.Worktrees.GitHub = true

	got := runWorktrees(t, env)
	wts := byPath(got["git-worktrees"])

	m := wts[merged]
	require.Equal(t, finding.TierA, m.Tier)
	require.Equal(t, finding.ActionGitWorktreeRemove, m.Action)
	require.Equal(t, plan.WorktreeRemoveCommand(f.Repo, merged), m.Command)
	require.Equal(t, f.Repo, m.Repo)
	require.Equal(t, "claude-code merged", m.Name)
	require.Equal(t, finding.MergedSquash, m.Worktree.Merged)
	require.Equal(t, 3, m.Worktree.PR.Number)
	require.Contains(t, m.Restore, "git -C "+f.Repo+" worktree add "+merged+" merged")
	require.Contains(t, m.Restore, "branch -d merged")
	require.Positive(t, m.Size)

	p := wts[pushed]
	require.Equal(t, finding.TierB, p.Tier)
	require.Equal(t, finding.ActionGitWorktreeRemove, p.Action)
	require.Contains(t, p.Warning, "PR #4 is open")

	d := wts[dirty]
	require.Equal(t, finding.ActionNone, d.Action)
	require.False(t, d.Actionable())
	require.Contains(t, d.Warning, "1 modified file")
	require.Contains(t, d.Worktree.ForceCommand, "worktree remove --force "+dirty)

	require.NotContains(t, wts, inUse, "a worktree a running process holds is never a finding")
	require.NotContains(t, wts, gone)

	o := wts[outside]
	require.Equal(t, finding.ActionNone, o.Action, "outside the roots and home")
	require.Contains(t, o.Warning, "outside the scanned folders")

	c := wts[clone]
	require.Equal(t, finding.ActionNone, c.Action)
	require.Equal(t, f.Repo, c.Repo, "listed under the first clone")
	require.Contains(t, c.Warning, "another clone of")

	regs := got["worktree-registrations"]
	require.Len(t, regs, 1)
	require.Equal(t, finding.ActionGitWorktreePrune, regs[0].Action)
	require.Equal(t, []string{gone}, regs[0].Registrations)
	require.Equal(t, "1 stale worktree registration", regs[0].Name)
	require.Equal(t, plan.WorktreePruneCommand(f.Repo), regs[0].Command)

	// The worktrees command asks for the ones in use too.
	env.Worktrees.InUse = true
	env.Projects = project.NewSource(env.Roots, scanners.New().ProjectMatchers(), project.Options{Home: home})
	all := byPath(runWorktrees(t, env)["git-worktrees"])
	require.Equal(t, finding.ActionNone, all[inUse].Action)
	require.Contains(t, all[inUse].Warning, "in use")
	require.Equal(t, finding.VerdictInUse, all[inUse].Worktree.Verdict)
}

// TestGitWorktreesLookupFailureIsOneWarning checks that a failing gh adds
// one warning and changes no finding.
// TestRegistrationsOfFoldersThatExistAreNotOffered lists for attention,
// never offers, a prune that would also remove the registration of a folder
// that still exists, because its gitdir file is missing.
func TestRegistrationsOfFoldersThatExistAreNotOffered(t *testing.T) {
	f := worktreetest.New(t)
	home := filepath.Join(worktreetest.RealTempDir(t), "Users", "me")
	gone := f.Worktree("gone")
	require.NoError(t, os.RemoveAll(gone))
	orphan := f.Worktree("orphan")
	require.NoError(t, os.Remove(filepath.Join(f.Repo, ".git", "worktrees", "orphan", "gitdir")))
	regs := runWorktrees(t, envFor(home, []string{f.Dir}, execx.OS{}))["worktree-registrations"]
	require.Len(t, regs, 1)
	require.Equal(t, finding.ActionNone, regs[0].Action)
	require.Contains(t, regs[0].Warning, "gitdir file does not exist")
	require.Contains(t, regs[0].Warning, "may still hold work")
	require.Contains(t, regs[0].Registrations, gone)
	require.Contains(t, regs[0].Registrations, filepath.Join(f.Repo, ".git", "worktrees", "orphan"))
	require.DirExists(t, orphan)

	// A folder that lost its .git file but still holds files.
	f2 := worktreetest.New(t)
	kept := f2.Worktree("kept")
	f2.Write(kept, "notes.md", "mine\n")
	require.NoError(t, os.Remove(filepath.Join(kept, ".git")))
	regs = runWorktrees(t, envFor(home, []string{f2.Dir}, execx.OS{}))["worktree-registrations"]
	require.Len(t, regs, 1)
	require.Equal(t, finding.ActionNone, regs[0].Action)
	require.Contains(t, regs[0].Warning, kept+" (the folder still exists without its .git file)")
}

func TestGitWorktreesLookupFailureIsOneWarning(t *testing.T) {
	f := worktreetest.New(t)
	home := filepath.Join(worktreetest.RealTempDir(t), "Users", "me")
	for _, name := range []string{"a", "b"} {
		w := f.Worktree(name)
		f.Commit(w, name+".txt", name+"\n")
		f.Push(w, name)
	}
	f.Git(f.Repo, "config", "url."+f.Dir+"/.insteadOf", "git@github.com:o/")
	f.Git(f.Repo, "remote", "set-url", "origin", "git@github.com:o/origin.git")
	env := envFor(home, []string{f.Dir}, mixed{gh: &execx.Fake{Tools: map[string]string{"gh": "/usr/bin/gh"}}})
	env.Worktrees.GitHub = true
	var ss []scan.Scanner
	for _, s := range scanners.New().All() {
		if s.Name() == "git-worktrees" {
			ss = append(ss, s)
		}
	}
	res := scan.Run(context.Background(), ss, env, 4)
	require.Len(t, res.Warnings, 1)
	require.Contains(t, res.Warnings[0].Message, "pull request state is not shown")
	require.Len(t, res.Findings, 2)
	for _, fd := range res.Findings {
		require.Equal(t, finding.TierB, fd.Tier)
		require.Nil(t, fd.Worktree.PR)
	}
}

// codexFixture makes Codex task folders in a fake home, each holding a
// worktree of the fixture's repository.
func codexFixture(t *testing.T) (*worktreetest.Fixture, string, string) {
	f := worktreetest.New(t)
	home := filepath.Join(worktreetest.RealTempDir(t), "Users", "me")
	tasks := filepath.Join(home, ".codex", "worktrees")
	clean := f.WorktreeAt(filepath.Join(tasks, "a1", "repo"), "codex/clean")
	f.Commit(clean, "c.txt", "c\n")
	f.Push(clean, "codex/clean")
	f.MergeFF("codex/clean")
	f.Write(clean, "node_modules/x/index.js", "x\n")
	pushed := f.WorktreeAt(filepath.Join(tasks, "b2", "repo"), "codex/pushed")
	f.Commit(pushed, "p.txt", "p\n")
	f.Push(pushed, "codex/pushed")
	dirty := f.WorktreeAt(filepath.Join(tasks, "c3", "repo"), "codex/dirty")
	f.Write(dirty, "README.md", "changed\n")
	f.Write(filepath.Join(tasks, "d4"), "notes/todo.txt", "not a worktree\n")
	half := f.WorktreeAt(filepath.Join(tasks, "e5", "repo"), "codex/half")
	f.Write(filepath.Join(tasks, "e5"), "loose.txt", "a file next to the worktree\n")
	_ = half
	f.Fetch()
	return f, home, tasks
}

func TestCodexWorktrees(t *testing.T) {
	f, home, tasks := codexFixture(t)
	env := envFor(home, []string{f.Dir}, execx.OS{})
	got := runWorktrees(t, env)
	codex := byPath(got["codex-worktrees"])

	clean := codex[filepath.Join(tasks, "a1", "repo")]
	require.Equal(t, finding.TierA, clean.Tier)
	require.Equal(t, "codex", clean.Worktree.Tool)
	require.Equal(t, f.Repo, clean.Repo)
	task := codex[filepath.Join(tasks, "a1")]
	require.Equal(t, finding.KindEmptyDir, task.Kind)
	require.Equal(t, finding.TierA, task.Tier)

	require.Equal(t, finding.TierB, codex[filepath.Join(tasks, "b2", "repo")].Tier)
	require.Equal(t, finding.TierB, codex[filepath.Join(tasks, "b2")].Tier, "a task folder takes the tier of its least safe worktree")

	require.Equal(t, finding.ActionNone, codex[filepath.Join(tasks, "c3", "repo")].Action)
	require.NotContains(t, codex, filepath.Join(tasks, "c3"), "a task holding work stays")

	require.Equal(t, finding.ActionNone, codex[filepath.Join(tasks, "d4", "notes")].Action)
	require.Contains(t, codex[filepath.Join(tasks, "d4", "notes")].Warning, "not a linked git worktree")
	require.NotContains(t, codex, filepath.Join(tasks, "d4"))

	require.Equal(t, finding.TierA, codex[filepath.Join(tasks, "e5", "repo")].Tier)
	require.NotContains(t, codex, filepath.Join(tasks, "e5"), "a task folder holding a file is not emptied")

	for _, g := range got["git-worktrees"] {
		require.False(t, strings.HasPrefix(g.Path, tasks), "Codex worktrees are reported once, by codex-worktrees: %s", g.Path)
	}
}

// TestApplyCodexWorktrees scans the Codex fixture, applies every finding
// that can be applied, and checks that exactly the removable worktrees and
// their emptied task folders are gone, while the dirty worktree, the folder
// that is not a worktree, the task folder holding a file, the main checkout
// and every branch remain.
func TestApplyCodexWorktrees(t *testing.T) {
	f, home, tasks := codexFixture(t)
	env := envFor(home, []string{f.Dir}, execx.OS{})
	got := runWorktrees(t, env)
	r := &finding.Report{Version: finding.ReportVersion, Created: time.Now(), Roots: []string{f.Dir, home}}
	for _, fs := range got {
		r.Findings = append(r.Findings, fs...)
	}
	p, err := plan.New(r, plan.SelectTiers(r.Findings, []finding.Tier{finding.TierA, finding.TierB}), "host", time.Now())
	require.NoError(t, err)
	require.NoError(t, p.Validate())
	branches := f.Git(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")

	sum, err := apply.Run(context.Background(), p, apply.Options{Home: home, Walker: fsx.NewWalker(2), Exec: execx.OS{},
		Getwd: func() (string, error) { return f.Dir, nil }})
	require.NoError(t, err)
	for _, o := range sum.Outcomes {
		require.Equal(t, apply.StatusDone, o.Status, "%s: %v", o.Action.Label(), o.Err)
	}

	for _, gone := range []string{"a1", "b2"} {
		require.NoDirExists(t, filepath.Join(tasks, gone))
	}
	require.NoDirExists(t, filepath.Join(tasks, "e5", "repo"))
	for _, kept := range []string{"c3/repo/README.md", "d4/notes/todo.txt", "e5/loose.txt"} {
		require.FileExists(t, filepath.Join(tasks, kept))
	}
	require.FileExists(t, filepath.Join(f.Repo, "README.md"))
	require.Equal(t, branches, f.Git(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"))
	regs, err := os.ReadDir(filepath.Join(f.Repo, ".git", "worktrees"))
	require.NoError(t, err)
	require.Len(t, regs, 1, "only the dirty worktree stays registered")
	gitdir, err := os.ReadFile(filepath.Join(f.Repo, ".git", "worktrees", regs[0].Name(), "gitdir"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(tasks, "c3", "repo", ".git"), strings.TrimSpace(string(gitdir)))
}
