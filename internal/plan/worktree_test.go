package plan

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

const (
	wtRepo = "/Users/me/Code/app"
	wtPath = "/Users/me/Code/app/.claude/worktrees/agent-1"
	wtHead = "0123456789abcdef0123456789abcdef01234567"
)

func worktreeFinding(tier finding.Tier) finding.Finding {
	return finding.Finding{
		ID: finding.MakeID("git-worktrees", wtPath), Scanner: "git-worktrees", Category: finding.CategoryWorktree,
		Tier: tier, Action: finding.ActionGitWorktreeRemove, Path: wtPath, Kind: finding.KindDir, Target: wtPath,
		Repo: wtRepo, Command: WorktreeRemoveCommand(wtRepo, wtPath),
		Worktree: &finding.WorktreeState{Repo: wtRepo, Path: wtPath, Head: wtHead, Branch: "agent-1", Exists: true,
			Merged: finding.MergedSquash, Verdict: finding.VerdictRemovable},
	}
}

// TestValidateWorktreeRemove checks that every field of a GitWorktreeRemove
// action must name the same worktree, and that the recorded state must
// allow removal at the action's tier, so a hand edit cannot point the
// action elsewhere or weaken the proof.
func TestValidateWorktreeRemove(t *testing.T) {
	cases := []struct {
		name string
		edit func(a *Action)
		want string
	}{
		{"valid", func(*Action) {}, ""},
		{"valid tier B unmerged", func(a *Action) { a.Tier = finding.TierB; a.Worktree.Merged = finding.MergedNo }, ""},
		{"path differs from target", func(a *Action) { a.Path = wtPath + "-other" }, "path equal to its target"},
		{"no worktree state", func(a *Action) { a.Worktree = nil }, "needs the worktree state"},
		{"state names another path", func(a *Action) { a.Worktree.Path = "/Users/me/Code/app/x" }, "edited inconsistently"},
		{"state names another repo", func(a *Action) { a.Worktree.Repo = "/Users/me/Code/other" }, "edited inconsistently"},
		{"state names another head", func(a *Action) { a.Worktree.Head = strings.Repeat("f", 40) }, "edited inconsistently"},
		{"head is not a commit", func(a *Action) { a.Head, a.Worktree.Head = "HEAD", "HEAD" }, "needs the commit"},
		{"repo is relative", func(a *Action) { a.Repo, a.Worktree.Repo = "app", "app" }, "clean absolute path"},
		{"path is the repository", func(a *Action) {
			a.Path, a.Target, a.Worktree.Path = wtRepo, wtRepo, wtRepo
			a.ID = finding.MakeID(a.Scanner, wtRepo)
		}, "never removed"},
		{"path contains the repository", func(a *Action) {
			a.Path, a.Target, a.Worktree.Path = "/Users/me/Code", "/Users/me/Code", "/Users/me/Code"
			a.ID = finding.MakeID(a.Scanner, a.Path)
		}, "never removed"},
		{"forced command", func(a *Action) { a.Command = []string{"git", "-C", wtRepo, "worktree", "remove", "--force", wtPath} }, "edited inconsistently"},
		{"command names another path", func(a *Action) { a.Command = WorktreeRemoveCommand(wtRepo, "/Users/me/Code/app/src") }, "edited inconsistently"},
		{"no command", func(a *Action) { a.Command = nil }, "edited inconsistently"},
		{"kind file", func(a *Action) { a.Kind = finding.KindFile }, "kind dir"},
		{"modified files", func(a *Action) { a.Worktree.Modified = 3 }, "3 modified files"},
		{"untracked files", func(a *Action) { a.Worktree.Untracked = 1 }, "untracked files"},
		{"ignored files", func(a *Action) { a.Worktree.Ignored = 1 }, "ignored files"},
		{"loose commits", func(a *Action) { a.Worktree.Loose = 2 }, "commits no branch holds"},
		{"unpushed commits", func(a *Action) { a.Worktree.Unpushed = 2 }, "commits on no remote"},
		{"unpushed at tier B", func(a *Action) { a.Tier = finding.TierB; a.Worktree.Unpushed = 1 }, "commits on no remote"},
		{"unknown parts", func(a *Action) { a.Worktree.Unknown = []string{"modified"} }, "could not report"},
		{"locked", func(a *Action) { a.Worktree.Locked = true }, "locked"},
		{"unmerged at tier A", func(a *Action) { a.Worktree.Merged = finding.MergedNo }, "not ancestor, rebase or squash"},
		{"unknown merge at tier B", func(a *Action) { a.Tier = finding.TierB; a.Worktree.Merged = finding.MergedUnknown }, `merged is "unknown"`},
		{"tier C", func(a *Action) { a.Tier = finding.TierC }, "only tier A and B"},
		{"gone", func(a *Action) { a.Worktree.Exists = false }, "does not exist"},
		{"needs sudo", func(a *Action) { a.NeedsSudo = true }, "sudo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := worktreeFinding(finding.TierA)
			a := FromFinding(&f)
			require.Equal(t, wtHead, a.Head)
			require.Equal(t, wtRepo, a.Repo)
			tc.edit(&a)
			err := (&Plan{Version: Version, Created: time.Now(), Actions: []Action{a}}).Validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestValidateWorktreePrune(t *testing.T) {
	valid := func() Action {
		target := WorktreePruneTarget(wtRepo)
		return Action{ID: finding.MakeID("worktree-registrations", target), Action: finding.ActionGitWorktreePrune,
			Target: target, Tier: finding.TierA, Scanner: "worktree-registrations", Repo: wtRepo,
			Registrations: []string{"/private/tmp/gone"}, Command: WorktreePruneCommand(wtRepo)}
	}
	cases := []struct {
		name string
		edit func(a *Action)
		want string
	}{
		{"valid", func(*Action) {}, ""},
		{"target names another repo", func(a *Action) { a.Repo = "/Users/me/Code/other"; a.Command = WorktreePruneCommand(a.Repo) }, "does not name the repository"},
		{"command names another repo", func(a *Action) { a.Command = WorktreePruneCommand("/Users/me/Code/other") }, "edited inconsistently"},
		{"expire flag", func(a *Action) { a.Command = append(a.Command, "--expire", "now") }, "edited inconsistently"},
		{"no registrations", func(a *Action) { a.Registrations = nil }, "registrations it covers"},
		{"relative registration", func(a *Action) { a.Registrations = []string{"tmp/x"} }, "clean absolute path"},
		{"carries a path", func(a *Action) { a.Path = wtPath }, "must not carry a path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := valid()
			tc.edit(&a)
			err := (&Plan{Version: Version, Created: time.Now(), Actions: []Action{a}}).Validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestWorktreeCommandsAreNotCleanCommands checks that a RunCommand action
// can never run the git commands of the worktree actions, so the proof
// cannot be skipped by changing the action.
func TestWorktreeCommandsAreNotCleanCommands(t *testing.T) {
	require.False(t, AllowedCommand(WorktreeRemoveCommand(wtRepo, wtPath)))
	require.False(t, AllowedCommand(WorktreePruneCommand(wtRepo)))
	a := Action{ID: finding.MakeID("x", wtPath), Action: finding.ActionRunCommand, Path: wtPath, Target: wtPath,
		Tier: finding.TierA, Scanner: "x", Command: WorktreeRemoveCommand(wtRepo, wtPath)}
	require.ErrorContains(t, (&Plan{Version: Version, Created: time.Now(), Actions: []Action{a}}).Validate(), "not one of the clean commands")
}

// TestOtherActionsCarryNoWorktreeFields keeps worktree fields off the other
// actions.
func TestOtherActionsCarryNoWorktreeFields(t *testing.T) {
	a := Action{ID: finding.MakeID("x", wtPath), Action: finding.ActionRemovePath, Path: wtPath, Target: wtPath,
		Kind: finding.KindDir, Tier: finding.TierA, Scanner: "x", Repo: wtRepo}
	require.ErrorContains(t, (&Plan{Version: Version, Created: time.Now(), Actions: []Action{a}}).Validate(), "worktree fields")
}

// TestNewOrdersWorktreesBeforeFolders puts worktree removals before the
// RemovePath of the folder that holds them.
func TestNewOrdersWorktreesBeforeFolders(t *testing.T) {
	r := sampleReport()
	wt := worktreeFinding(finding.TierA)
	folder := mk("codex-worktrees", "/Users/me/.codex/worktrees/a1", finding.TierA, finding.ActionRemovePath, 1000)
	folder.Kind = finding.KindEmptyDir
	p, err := New(r, []finding.Finding{folder, wt}, "h", time.Now())
	require.NoError(t, err)
	require.Equal(t, finding.ActionGitWorktreeRemove, p.Actions[0].Action)
	require.Equal(t, finding.ActionRemovePath, p.Actions[1].Action)
	require.NotSame(t, wt.Worktree, p.Actions[0].Worktree, "the plan holds its own copy of the state")
}
