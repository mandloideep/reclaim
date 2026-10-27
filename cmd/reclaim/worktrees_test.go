package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/worktree"
	"github.com/mandloideep/reclaim/internal/worktree/worktreetest"
	"github.com/mandloideep/reclaim/skills"
)

// worktreeApp returns a test app that runs git for real, whose home folder
// holds a fixture repository with one worktree in each state done reports.
func worktreeApp(t *testing.T) (*app, *worktreetest.Fixture, map[string]string) {
	t.Helper()
	f := worktreetest.New(t)
	a, _ := testApp(t, "")
	a.home = f.Dir
	a.exec = execx.OS{}
	a.getwd = func() (string, error) { return f.Dir, nil }
	w := map[string]string{}
	w["merged"] = f.Worktree("merged")
	f.Commit(w["merged"], "m.txt", "m\n")
	f.Push(w["merged"], "merged")
	f.MergeFF("merged")
	w["pushed"] = f.Worktree("pushed")
	f.Commit(w["pushed"], "p.txt", "p\n")
	f.Push(w["pushed"], "pushed")
	w["dirty"] = f.Worktree("dirty")
	f.Write(w["dirty"], "README.md", "changed\n")
	w["unpushed"] = f.Worktree("unpushed")
	f.Commit(w["unpushed"], "u.txt", "u\n")
	return a, f, w
}

// exitCode returns the code an error from a command ends the process with.
func exitCode(err error) int {
	var ee *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	default:
		return 1
	}
}

func output(a *app) string {
	return a.stdout.(*strings.Builder).String()
}

func withOutput(a *app) {
	a.stdout, a.stderr = &strings.Builder{}, &strings.Builder{}
}

func TestDoneExitCodes(t *testing.T) {
	a, f, w := worktreeApp(t)
	plain := filepath.Join(f.Dir, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	cases := []struct {
		name string
		path string
		code int
		says []string
	}{
		{"merged", w["merged"], 0, []string{"committed, pushed, merged", "Safe to remove", "reclaim done --remove"}},
		{"from a subfolder", filepath.Join(w["merged"], ".git", ".."), 0, []string{"committed, pushed, merged"}},
		{"pushed, not merged", w["pushed"], 1, []string{"committed, pushed, not merged", "Open or finish the pull request"}},
		{"modified", w["dirty"], 2, []string{"1 modified file", "add -A", "worktree remove --force " + w["dirty"]}},
		{"unpushed", w["unpushed"], 2, []string{"1 commit on no remote", "push -u origin unpushed"}},
		{"main checkout", f.Repo, 3, []string{"not a linked worktree", "main checkout"}},
		{"not in a repository", plain, 3, []string{"not in a git repository"}},
		{"missing", filepath.Join(f.Dir, "gone"), 3, []string{"not in a git repository"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withOutput(a)
			err := execute(a, "done", tc.path)
			require.Equal(t, tc.code, exitCode(err), "%v\n%s", err, output(a))
			for _, s := range tc.says {
				require.Contains(t, output(a), s)
			}
		})
	}

	t.Run("json is the scanner's state object", func(t *testing.T) {
		withOutput(a)
		require.Equal(t, 1, exitCode(execute(a, "done", "--json", w["pushed"])))
		var s finding.WorktreeState
		require.NoError(t, json.Unmarshal([]byte(output(a)), &s))
		require.Equal(t, w["pushed"], s.Path)
		require.Equal(t, finding.VerdictPushed, s.Verdict)
		require.Equal(t, finding.MergedNo, s.Merged)
	})

	t.Run("the current folder by default", func(t *testing.T) {
		withOutput(a)
		a.getwd = func() (string, error) { return w["merged"], nil }
		defer func() { a.getwd = func() (string, error) { return f.Dir, nil } }()
		require.Equal(t, 0, exitCode(execute(a, "done")))
		require.Contains(t, output(a), "merged")
	})

	t.Run("command line mistakes exit 3", func(t *testing.T) {
		for _, args := range [][]string{{"--allow-unmerged", w["pushed"]}, {"--bogus"}, {w["pushed"], w["merged"]}} {
			withOutput(a)
			require.Equal(t, 3, exitCode(execute(a, append([]string{"done"}, args...)...)), "%v", args)
			require.Contains(t, a.stderr.(*strings.Builder).String(), "reclaim: warning:")
		}
	})
}

func TestRefusalCode(t *testing.T) {
	merged := &finding.WorktreeState{Exists: true, Merged: finding.MergedSquash, Locked: true}
	dirty := &finding.WorktreeState{Exists: true, Merged: finding.MergedSquash, Modified: 2}
	cases := []struct {
		err  error
		want int
	}{
		{&apply.Refusal{Check: apply.CheckLinked}, 3},
		{&apply.Refusal{Check: apply.CheckPath}, 1},
		{&apply.Refusal{Check: apply.CheckPath, HoldsWork: true}, 2},
		{&apply.Refusal{Check: apply.CheckState, State: merged}, 1},
		{&apply.Refusal{Check: apply.CheckState, State: dirty}, 2},
		{&apply.Refusal{Check: apply.CheckState}, 3},
		{&apply.Refusal{Check: apply.CheckWorkdir}, 1},
		{&apply.Refusal{Check: apply.CheckHead, State: merged}, 1},
		{&apply.Refusal{Check: apply.CheckHead, State: dirty}, 2},
		{&apply.Refusal{Check: apply.CheckRemove}, 2},
		{fmt.Errorf("not removed: %w", &apply.Refusal{Check: apply.CheckWorkdir}), 1},
		{errors.New("disk full"), 3},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, refusalCode(tc.err), "%v", tc.err)
	}
}

// TestDoneRemove proves that --remove removes a tier A worktree and nothing
// else, refuses tier B without --allow-unmerged, and that no flag removes a
// worktree with work that exists nowhere else or the folder it runs in.
func TestDoneRemove(t *testing.T) {
	a, f, w := worktreeApp(t)
	branches := f.Git(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	before := snapshot(t, f.Repo)

	withOutput(a)
	require.Equal(t, 0, exitCode(execute(a, "done", "--remove", w["merged"])), output(a))
	require.Contains(t, output(a), "Removed")
	require.Contains(t, output(a), "branch -d merged")
	require.NoDirExists(t, w["merged"])
	rel, err := filepath.Rel(f.Repo, w["merged"])
	require.NoError(t, err)
	gone := func(p string) bool {
		return p == rel || strings.HasPrefix(p, rel+"/") || p == ".git/worktrees/merged" || strings.HasPrefix(p, ".git/worktrees/merged/")
	}
	after := snapshot(t, f.Repo)
	for _, p := range before {
		if !gone(p) && !strings.HasPrefix(p, ".git/objects") {
			require.Contains(t, after, p, "only the worktree and its registration are removed")
		}
	}
	require.Equal(t, branches, f.Git(f.Repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), "no branch is deleted")
	plans, err := filepath.Glob(filepath.Join(a.stateDir, "plans", "done-*.json"))
	require.NoError(t, err)
	require.Len(t, plans, 1)
	logs, err := filepath.Glob(filepath.Join(a.stateDir, "logs", "done-*.log"))
	require.NoError(t, err)
	require.Len(t, logs, 1)
	log, err := os.ReadFile(logs[0])
	require.NoError(t, err)
	require.Contains(t, string(log), `"name":"git worktree remove","ok":true`)

	// Tier B is refused without --allow-unmerged, removed with it.
	withOutput(a)
	require.Equal(t, 1, exitCode(execute(a, "done", "--remove", w["pushed"])))
	require.Contains(t, output(a), "Add --allow-unmerged")
	require.DirExists(t, w["pushed"])

	// Work that exists nowhere else is never removed, whatever the flags.
	for _, name := range []string{"dirty", "unpushed"} {
		withOutput(a)
		require.Equal(t, 2, exitCode(execute(a, "done", "--remove", "--allow-unmerged", w[name])))
		require.Contains(t, output(a), "never removes a worktree whose work exists nowhere else")
		require.Contains(t, output(a), "worktree remove --force")
		require.DirExists(t, w[name])
	}

	// done never removes the folder it runs in.
	withOutput(a)
	a.getwd = func() (string, error) { return w["pushed"], nil }
	require.Equal(t, 1, exitCode(execute(a, "done", "--remove", "--allow-unmerged", w["pushed"])))
	require.Contains(t, output(a), "run it from the main checkout")
	require.DirExists(t, w["pushed"])

	a.getwd = func() (string, error) { return f.Repo, nil }
	withOutput(a)
	require.Equal(t, 0, exitCode(execute(a, "done", "--remove", "--allow-unmerged", w["pushed"])), output(a))
	require.NoDirExists(t, w["pushed"])
	require.DirExists(t, w["dirty"])
	require.DirExists(t, w["unpushed"])
	require.FileExists(t, filepath.Join(f.Repo, "README.md"))
}

// TestWorktreesThenSelectThenApply runs the documented flow: worktrees
// saves a report, select --preset safe plans the tier A worktrees, apply
// removes exactly those.
func TestWorktreesThenSelectThenApply(t *testing.T) {
	a, f, w := worktreeApp(t)
	withOutput(a)
	require.NoError(t, execute(a, "worktrees", f.Dir))
	out := output(a)
	for _, s := range []string{"TIER  TOOL", "claude-code", "merged", "pushed", "1 modified", "save: git -C ~/repo/.claude/worktrees/unpushed push -u origin unpushed",
		"remove anyway, losing it: git -C ~/repo worktree remove --force ~/repo/.claude/worktrees/dirty"} {
		require.Contains(t, out, s)
	}

	withOutput(a)
	require.NoError(t, execute(a, "worktrees", "--json", f.Dir))
	var r finding.Report
	require.NoError(t, json.Unmarshal([]byte(output(a)), &r))
	require.Len(t, r.Findings, 4)
	for _, fd := range r.Findings {
		require.NotNil(t, fd.Worktree, fd.Path)
	}

	withOutput(a)
	require.NoError(t, execute(a, "worktrees", "--md", f.Dir))
	require.Contains(t, output(a), "| Tier | Tool | Branch | Uncommitted | Unpushed | Merged | PR | Lock | Size | Path |")

	planPath := filepath.Join(t.TempDir(), "plan.json")
	withOutput(a)
	require.NoError(t, execute(a, "select", "--preset", "safe", "--out", planPath))
	require.Contains(t, output(a), "It covers only git worktrees")
	withOutput(a)
	require.NoError(t, execute(a, "apply", "--yes", planPath), output(a))
	require.NoDirExists(t, w["merged"])
	for _, kept := range []string{"pushed", "dirty", "unpushed"} {
		require.DirExists(t, w[kept])
	}
}

func TestSkillCommand(t *testing.T) {
	a, _ := testApp(t, "")
	withOutput(a)
	require.NoError(t, execute(a, "skill"))
	require.Equal(t, skills.Reclaim, output(a))
	require.True(t, strings.HasPrefix(output(a), "---\nname: reclaim\n"))
	for _, s := range []string{"reclaim done --json", "reclaim done --remove", "git worktree remove --force"} {
		require.Contains(t, output(a), s)
	}
	agents, err := os.ReadFile(filepath.Join("..", "..", "docs", "AGENTS.md"))
	require.NoError(t, err)
	for _, s := range []string{"reclaim done --json", "reclaim done --remove", "--allow-unmerged", "git worktree remove --force"} {
		require.Contains(t, string(agents), s, "docs/AGENTS.md holds the same instructions")
	}
}

// TestSelectProvesWorktrees leaves a worktree out of the plan when it fails
// the proof between the report and the plan, here because it was modified.
func TestSelectProvesWorktrees(t *testing.T) {
	a, f, w := worktreeApp(t)
	withOutput(a)
	require.NoError(t, execute(a, "worktrees", f.Dir))
	f.Write(w["merged"], "late.txt", "written after the report\n")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	withOutput(a)
	require.NoError(t, execute(a, "select", "--preset", "aggressive", "--out", planPath))
	require.Contains(t, output(a), "Left out of the plan: refusing to remove the worktree "+w["merged"])
	data, err := os.ReadFile(planPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), w["merged"])
	require.Contains(t, string(data), w["pushed"])
}

// TestDoneExitCodesForBlockers exits 2 for a nested repository and for a
// submodule, which git reported and the hints say how to save, and 3 only
// when git could not report the state.
func TestDoneExitCodesForBlockers(t *testing.T) {
	a, f, _ := worktreeApp(t)
	nested := f.Worktree("nested")
	f.Commit(nested, "n.txt", "n\n")
	f.Push(nested, "nested")
	f.MergeFF("nested")
	dep := filepath.Join(nested, "node_modules", "dep")
	f.Write(dep, "index.js", "local\n")
	f.Git(dep, "init", "-q")
	f.Git(dep, "add", "-A")
	f.Git(dep, "commit", "-q", "-m", "local only")

	lib := filepath.Join(f.Dir, "lib")
	f.Git(f.Dir, "init", "-q", "-b", "main", lib)
	f.Commit(lib, "lib.txt", "lib\n")
	sub := f.Worktree("sub")
	f.Git(sub, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "lib")
	f.Git(sub, "commit", "-q", "-m", "add lib")
	f.Push(sub, "sub")
	f.MergeFF("sub")

	for path, says := range map[string]string{nested: "nested repositories: node_modules/dep", sub: "submodule foreach git push"} {
		for _, args := range [][]string{{path}, {"--remove", "--allow-unmerged", path}} {
			withOutput(a)
			require.Equal(t, 2, exitCode(execute(a, append([]string{"done"}, args...)...)), output(a))
			require.Contains(t, output(a), "holds work that exists nowhere else")
			require.Contains(t, output(a), says)
			require.DirExists(t, path)
		}
	}

	// A state git could not report: git fails for this worktree.
	broken := &finding.WorktreeState{Exists: true, Merged: finding.MergedSquash, Unknown: []string{"modified", "untracked"}, Problem: "git status failed"}
	require.Equal(t, 3, doneCode(broken))
	worktree.Judge(broken)
	var out strings.Builder
	a.printDone(&out, broken, 0, doneCode(broken), false)
	require.Contains(t, out.String(), "reclaim could not tell whether its work exists elsewhere: git could not report modified, untracked: git status failed")
	require.NotContains(t, out.String(), "holds work that exists nowhere else")
}
