package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/worktree"
)

func TestWorktreeWords(t *testing.T) {
	merged := &finding.PullRequest{Number: 41, State: "MERGED"}
	open := &finding.PullRequest{Number: 44, State: "OPEN"}
	wt := func(s finding.WorktreeState, action finding.Action) *finding.Finding {
		return &finding.Finding{Worktree: &s, Action: action, Tier: finding.TierA}
	}
	for _, tc := range []struct {
		name string
		f    *finding.Finding
		want string
	}{
		{"squash with its pull request", wt(finding.WorktreeState{Verdict: finding.VerdictRemovable, Merged: finding.MergedSquash, PR: merged}, finding.ActionGitWorktreeRemove), "squash, PR #41 merged"},
		{"ancestor without a pull request", wt(finding.WorktreeState{Verdict: finding.VerdictRemovable, Merged: finding.MergedAncestor}, finding.ActionGitWorktreeRemove), "merged"},
		{"ancestor with a merged pull request", wt(finding.WorktreeState{Verdict: finding.VerdictRemovable, Merged: finding.MergedAncestor, PR: merged}, finding.ActionGitWorktreeRemove), "merged, PR #41"},
		{"pushed with an open pull request", wt(finding.WorktreeState{Verdict: finding.VerdictPushed, Merged: finding.MergedNo, PR: open}, finding.ActionGitWorktreeRemove), "pushed, PR #44 open"},
		{"pushed without one", wt(finding.WorktreeState{Verdict: finding.VerdictPushed, Merged: finding.MergedNo}, finding.ActionGitWorktreeRemove), "pushed, not merged"},
		{"in use", wt(finding.WorktreeState{Verdict: finding.VerdictInUse, Locked: true, LockLive: true, LockReason: "claude agent x (pid 4242 start Mon Oct 5 08:00:00 2026)", PR: merged}, finding.ActionNone), "in use, pid 4242, PR #41 merged"},
		{"stale lock", wt(finding.WorktreeState{Verdict: finding.VerdictAttention, Locked: true, LockReason: "claude agent x (pid 50949 start Mon)"}, finding.ActionNone), "stale lock, pid 50949 gone"},
		{"changes", wt(finding.WorktreeState{Verdict: finding.VerdictAttention, Modified: 12, Untracked: 3}, finding.ActionNone), "12 modified, 3 untracked"},
		{"commits and ignored files", wt(finding.WorktreeState{Verdict: finding.VerdictAttention, Loose: 2, Unpushed: 3, Ignored: 1}, finding.ActionNone), "2 loose commits, 3 unpushed, 1 ignored"},
		{"not checkable and blocked", wt(finding.WorktreeState{Verdict: finding.VerdictAttention, Unknown: []string{"modified", worktree.PartNested, worktree.PartSubmodules, worktree.PartHidden}}, finding.ActionNone), "not checkable, nested repositories, submodules, hidden changes"},
		{"outside the roots", wt(finding.WorktreeState{Verdict: finding.VerdictRemovable, Merged: finding.MergedSquash}, finding.ActionNone), "outside the scanned roots"},
		{"a task folder", &finding.Finding{Kind: finding.KindEmptyDir, Action: finding.ActionRemovePath}, "task folder, removed once empty"},
		{"stale registrations", &finding.Finding{Action: finding.ActionGitWorktreePrune, Name: "2 stale worktree registrations"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, WorktreeWords(tc.f))
		})
	}
}

func TestVerdictCounts(t *testing.T) {
	r := &RepoSummary{Verdicts: map[finding.Verdict]int{finding.VerdictRemovable: 1, finding.VerdictAttention: 2, finding.VerdictInUse: 1}, Stale: 3, Clones: 1}
	require.Equal(t, "1 removable, 2 work, 1 in use", VerdictCounts(r))
	require.Equal(t, "1 removable, 2 holding work, 1 in use, 3 stale registrations, 1 other clone", RepoCounts(r))
	require.Empty(t, VerdictCounts(nil))
}

// TestTreeWarningsNode checks the node the review screen adds: last, closed
// at first, never selectable, and its rows carry the full text.
func TestTreeWarningsNode(t *testing.T) {
	tr := NewTree(report(), TreeOptions{Home: "/Users/me", Now: now, Warnings: true})
	tr.SetSize(160, 60)
	view := tr.View()
	require.Contains(t, view, "▸ Warnings and notes (2)")
	require.NotContains(t, view, "permission denied", "closed at first")
	for range 40 {
		if tr.Current().Kind == TreeWarnings {
			break
		}
		tr.HandleKey(key("down"))
	}
	require.Equal(t, TreeWarnings, tr.Current().Kind)
	tr.HandleKey(key(" "))
	require.Equal(t, "warnings and notes cannot be selected", tr.Status())
	tr.HandleKey(key("enter"))
	tr.HandleKey(key("down"))
	it := tr.Current()
	require.Equal(t, TreeWarning, it.Kind)
	require.Equal(t, "projects: ~/Code/locked: not scanned: permission denied", it.Text)
	tr.HandleKey(key("down"))
	require.Equal(t, "Note: Docker runs in OrbStack.", tr.Current().Text)
	require.Contains(t, tr.View(), "Note: Docker runs in OrbStack.")

	// The checklist of select has no such node.
	require.NotContains(t, NewTree(report(), TreeOptions{Home: "/Users/me"}).View(), "Warnings and notes")
}

func TestTreeSelectNoneAndCurrent(t *testing.T) {
	tr := NewTree(report(), TreeOptions{Home: "/Users/me", Now: now})
	tr.SetSize(160, 60)
	n, _ := tr.SelectedSize()
	require.Equal(t, 6, n)
	require.Equal(t, 6, tr.SelectNone())
	n, size := tr.SelectedSize()
	require.Zero(t, n)
	require.Zero(t, size)

	it := tr.Current()
	require.Equal(t, TreeCategory, it.Kind)
	require.Equal(t, finding.CategoryProject, it.Category)
	require.Equal(t, 3, it.Count)
	tr.HandleKey(key("down"))
	it = tr.Current()
	require.Equal(t, TreeGroup, it.Kind)
	require.Equal(t, "/Users/me/Code/app", it.Group.Project)
	tr.HandleKey(key("enter"))
	tr.HandleKey(key("down"))
	it = tr.Current()
	require.Equal(t, TreeFinding, it.Kind)
	require.Equal(t, "/Users/me/Code/app/node_modules", it.Finding.Path)
	require.False(t, it.Selected)
	tr.HandleKey(key(" "))
	require.True(t, tr.Current().Selected)
}

// TestTreeSelectedSizeCountsNestedOnce checks that a selected finding inside
// another selected finding is counted once, as report totals count it.
func TestTreeSelectedSizeCountsNestedOnce(t *testing.T) {
	wt := "/Users/me/Code/app/.claude/worktrees/agent-1"
	r := &finding.Report{Version: 1, Findings: []finding.Finding{
		{ID: "1", Scanner: "git-worktrees", Category: finding.CategoryWorktree, Tier: finding.TierA, Path: wt, Target: wt, Size: 300, Action: finding.ActionGitWorktreeRemove, Repo: "/Users/me/Code/app"},
		{ID: "2", Scanner: "node_modules", Category: finding.CategoryProject, Tier: finding.TierA, Path: wt + "/node_modules", Target: wt + "/node_modules", Size: 200, Action: finding.ActionRemovePath},
	}}
	tr := NewTree(r, TreeOptions{})
	n, size := tr.SelectedSize()
	require.Equal(t, 2, n)
	require.Equal(t, int64(300), size)
}

func key(k string) tea.KeyMsg {
	switch k {
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func TestActionLineWorktrees(t *testing.T) {
	p := NewPlain(nil, "/Users/me", now)
	rm := &plan.Action{Action: finding.ActionGitWorktreeRemove, Scanner: "git-worktrees",
		Command: []string{"git", "-C", "/Users/me/Code/app", "worktree", "remove", "/Users/me/Code/app/.claude/worktrees/a"}}
	require.Equal(t, "git -C ~/Code/app worktree remove ~/Code/app/.claude/worktrees/a  git-worktrees", p.ActionLine(rm))
	prune := &plan.Action{Action: finding.ActionGitWorktreePrune, Registrations: []string{"/x", "/y"},
		Command: []string{"git", "-C", "/Users/me/Code/app", "worktree", "prune"}}
	require.Equal(t, "git -C ~/Code/app worktree prune  2 stale registrations", p.ActionLine(prune))
}
