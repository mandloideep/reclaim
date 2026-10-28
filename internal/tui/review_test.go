package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

// reviewHarness opens the Review screen on the fixture report.
func reviewHarness(t *testing.T, width, height int) *harness {
	t.Helper()
	be := &fakeBackend{report: fixtureReport()}
	h := newHarness(t, be, defaultStart(), width, height)
	h.press("enter")
	require.Equal(t, screenReview, h.m.screen)
	return h
}

func selectedTargets(h *harness) []string {
	out := make([]string, 0, len(h.m.review.tree.Selected()))
	for _, f := range h.m.review.tree.Selected() {
		out = append(out, f.Target)
	}
	return out
}

func TestReviewTreeAndWorktrees(t *testing.T) {
	h := reviewHarness(t, 140, 60)
	h.moveTo("~/Code/storefront")
	h.press("enter")
	view := h.view()
	for _, want := range []string{
		"[-] ▾ Git worktrees",
		"[-] ▾ ~/Code/storefront  1 removable, 1 pushed, 1 work, 1 in use, 2 selected",
		"[x] A  184.0 MB  claude-code  fix/checkout-rounding  squash, PR #41 merged",
		"[ ] B   96.0 MB  claude-code  feat/gift-cards  pushed, PR #44 open",
		" -      88.0 MB  claude-code  chore/upgrade-next  in use, pid 4242",
		" -     231.0 MB  codex  detached a5e1d0c9  12 modified, 3 untracked",
		"[x] A     800 B  2 stale worktree registrations",
		" -            -  ~/Code/personal/storefront  another clone of this origin",
		"▸ Warnings and notes (2)",
		"Selected: 5 items, 9.6 GB",
	} {
		require.Contains(t, view, want)
	}

	// Rows listed only cannot be ticked.
	h.moveTo("chore/upgrade-next")
	h.press("space")
	require.NotContains(t, selectedTargets(h), "/Users/me/Code/storefront/.claude/worktrees/agent-c0ffee12")
	require.Contains(t, h.view(), "listed for attention only")

	// n clears the selection and a selects tier A again.
	h.press("n")
	require.Empty(t, selectedTargets(h))
	require.Contains(t, h.view(), "cleared 5 items")
	require.Contains(t, h.view(), "Selected: 0 items, 0 B")
	h.press("a")
	require.Len(t, selectedTargets(h), 5)
}

func TestReviewDetails(t *testing.T) {
	h := reviewHarness(t, 140, 60)
	h.moveTo("~/Code/ripgrep-fork")
	h.press("enter", "down")
	flat := h.details()
	for _, want := range []string{
		"~/Code/ripgrep-fork/target",
		"3.9 GB, tier A: rebuilds itself the next time the project builds or installs",
		"Scanner: rust-target", "Restore: cargo build", "Last used: 21w ago",
		"In root ~/Code.", "Project ~/Code/ripgrep-fork, which apply never removes.",
		"Apply checks before removing: inside the root, not a protected folder, not the project root",
		"Apply will: remove ~/Code/ripgrep-fork/target",
	} {
		require.Contains(t, flat, want)
	}

	// A worktree that holds work: the verdict, what it holds, the hints and
	// the force command, all from the scanner.
	h.moveTo("~/Code/storefront")
	h.press("enter")
	h.moveTo("detached a5e1d0c9")
	flat = h.details()
	for _, want := range []string{
		"Verdict: holds work that exists nowhere else", "12 modified and 3 untracked files", "Repository ~/Code/storefront, made by codex",
		"Detached at a5e1d0c9b8a7", "Uncommitted: 12 modified, 3 untracked, 0 ignored outside artifact folders, no hidden changes", "Merged: ancestor into origin/main", "Lock: none",
		"Fetched 3h ago; merge state is as of that fetch.",
		"Save the work first:", "git -C ~/.codex/worktrees/5e1d/storefront add -A",
		"Remove anyway, losing it:", "worktree remove --force ~/.codex/worktrees/5e1d/storefront",
		"Listed for attention only: reclaim never removes it",
	} {
		require.Contains(t, flat, want)
	}

	// A removable worktree: its pull request and the git command apply runs.
	h.moveTo("fix/checkout-rounding")
	flat = h.details()
	require.Contains(t, flat, "Pull request: #41 merged https://github.com/acme/storefront/pull/41")
	require.Contains(t, flat, "Apply checks before removing: linked worktree, the path rules, the state computed again")
	require.Contains(t, flat, "Apply will: git -C ~/Code/storefront worktree remove ~/Code/storefront/.claude/worktrees/agent-3f9c1e2a")
	require.Contains(t, flat, "tier A: committed, pushed and merged, so the work is in the default branch")

	// The repository row: origin, default branch, fetch and counts.
	h.moveTo("~/Code/storefront")
	flat = h.details()
	for _, want := range []string{
		"Origin: git@github.com:acme/storefront.git", "Default branch: origin/main",
		"Fetched 3h ago; merge state is as of that fetch.", "1 removable, 1 pushed, 1 holding work, 1 in use, 2 stale registrations, 1 other clone",
	} {
		require.Contains(t, flat, want)
	}

	// Docker objects, Claude sessions and what needs sudo.
	h.moveTo("Unused images")
	h.press("enter", "down")
	flat = h.details()
	require.Contains(t, flat, "Kind: unused images")
	require.Contains(t, flat, "Tags: node:20")
	require.Contains(t, flat, "Created: 12w ago")
	require.Contains(t, flat, "Apply will: docker remove image node:20")

	h.moveTo("claude-sessions")
	h.press("enter", "down")
	flat = h.details()
	require.Contains(t, flat, "Claude Code cannot resume these sessions.")
	require.Contains(t, flat, "Project folder gone: ~/Code/old")

	h.moveTo("simulator-runtimes")
	h.press("enter", "down")
	flat = h.details()
	require.Contains(t, flat, "Apply will: you run: sudo xcrun simctl runtime delete iOS-17-0, printed, never run")
	require.Contains(t, flat, "nothing runs: it needs administrator rights")

	// Warnings and notes are the last node, closed, never selectable.
	h.moveTo("Warnings and notes")
	h.press("space")
	require.Contains(t, h.view(), "warnings and notes cannot be selected")
	h.press("enter", "down")
	require.Contains(t, h.flat(), "projects: ~/Code/locked: not scanned: permission denied")
}

func TestReviewLayout(t *testing.T) {
	wide := reviewHarness(t, 140, 40)
	wide.moveTo("~/Code/ripgrep-fork")
	view := wide.view()
	require.Contains(t, view, "│ ~/Code/ripgrep-fork", "the details sit on the right at 140 columns")
	for l := range strings.SplitSeq(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(l), 140, l)
	}
	require.Len(t, strings.Split(view, "\n"), 40)

	narrow := reviewHarness(t, 80, 30)
	narrow.moveTo("~/Code/ripgrep-fork")
	view = narrow.view()
	require.NotContains(t, view, "│", "the details sit below the list at 80 columns")
	require.Contains(t, view, strings.Repeat("─", 80))
	for l := range strings.SplitSeq(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(l), 80, l)
	}
	require.Len(t, strings.Split(view, "\n"), 30)

	// i hides the details and gives the list the whole window.
	narrow.press("i")
	require.NotContains(t, narrow.view(), strings.Repeat("─", 80))
	narrow.press("i")
	require.Contains(t, narrow.view(), strings.Repeat("─", 80))
}

func TestReviewFilterTakesKeys(t *testing.T) {
	h := reviewHarness(t, 140, 40)
	h.press("/", "q", "?", "P")
	require.False(t, h.quit, "q is typed into the filter")
	require.Equal(t, overlayNone, h.m.overlay)
	require.Contains(t, h.view(), "/q?P")
	h.press("esc", "esc")
	require.Contains(t, h.view(), "sort: size")
}

// TestReviewContinueSavesAPlan proves that the Review screen reaches the
// backend only to save a plan of what is selected, and then ends.
func TestReviewContinueSavesAPlan(t *testing.T) {
	h := reviewHarness(t, 140, 40)
	h.press("n", "c")
	require.Contains(t, h.view(), "Nothing is selected; tick what should go first.")
	require.Equal(t, []string{"Scan"}, h.calls())

	h.press("a", "c")
	require.True(t, h.quit)
	require.Equal(t, []string{"Scan", "SavePlan"}, h.calls(), "the screen scans and saves a plan, and calls nothing else")
	ids := make([]string, 0, len(h.be.selected[0]))
	for _, f := range h.be.selected[0] {
		require.Equal(t, finding.TierA, f.Tier)
		ids = append(ids, f.Target)
	}
	require.ElementsMatch(t, []string{"/Users/me/Code/ripgrep-fork/target", "/Users/me/Code/ml-pipeline/.venv",
		"/Users/me/Code/storefront/.claude/worktrees/agent-3f9c1e2a", "git-worktree-prune:/Users/me/Code/storefront", "docker-build-cache"}, ids)
	res := h.m.Result()
	require.Equal(t, OutcomePlanSaved, res.Outcome)
	require.Equal(t, "/Users/me/Library/Caches/reclaim/plans/plan-20261027T153000Z.json", res.Plan.Path)
	require.Len(t, res.Plan.Plan.Actions, 5)
}

func TestReviewNothingLeftAfterProof(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), saved: SavedPlan{LeftOut: []string{"refusing a worktree: check 3 failed"}}}
	be.saved.Path = ""
	h := newHarness(t, be, defaultStart(), 140, 40)
	h.press("enter")
	// The fake saves an empty plan when Path is empty, so make it return
	// only the refusal.
	h.m.review.planSaved(planSavedMsg{plan: SavedPlan{LeftOut: []string{"refusing a worktree: check 3 failed"}}})
	require.False(t, h.quit)
	require.Contains(t, h.view(), "Nothing is left to remove: refusing a worktree: check 3 failed")
}

func TestReviewWrite(t *testing.T) {
	h := reviewHarness(t, 140, 40)
	h.press("w")
	require.True(t, h.quit)
	res := h.m.Result()
	require.Equal(t, OutcomeWrite, res.Outcome)
	require.Len(t, res.Selected, 5)
	require.Equal(t, []string{"Scan"}, h.calls(), "w leaves the writing to select's code after the screen closes")
}

func TestHelpOnEveryScreen(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), wait: true}
	h := newHarness(t, be, defaultStart(), 140, 40)
	check := func(keys ...string) {
		t.Helper()
		h.press("?")
		flat := h.flat()
		require.Contains(t, flat, closing)
		for _, k := range keys {
			require.Contains(t, flat, k)
		}
		h.press("esc")
		require.Equal(t, overlayNone, h.m.overlay)
	}
	check("cycle the minimum size", "open the last report")
	h.background = true
	h.press("enter")
	check("stop the scan and review what was found so far")
	h.press("esc")
	h.settle()
	check("select nothing", "reclaim skill", "save the plan for reclaim apply")
}

func TestProtectedList(t *testing.T) {
	h := newHarness(t, &fakeBackend{report: fixtureReport()}, defaultStart(), 140, 200)
	h.press("P")
	require.Equal(t, overlayProtected, h.m.overlay)
	view := h.view()
	for _, want := range []string{
		"Never removed", "System paths (built in)", "/Users", "/System",
		"Folders of your home folder (built in)", "~/Documents", "~/Desktop", "~/Library",
		"Rules (built in)", "nothing inside a .git directory",
		"Scanned roots", "~/Code", "Your exclude entries (config file), which the scan skips", "~/Code/archive",
	} {
		require.Contains(t, view, want)
	}
	require.Equal(t, [][]string{{"/Users/me/Code", "/Users/me/Downloads", "/Users/me"}}, h.be.roots,
		"the Start screen asks for the roots the scan would record")
	h.press("esc")

	// On Review the roots are the report's.
	h.press("enter", "P")
	require.Equal(t, []string{"/Users/me", "/Users/me/Code"}, h.be.roots[1])
}

// TestQuitWaitsForThePlan checks that no key ends the screen while the plan
// is being saved, so a plan is never written without the screen saying so.
func TestQuitWaitsForThePlan(t *testing.T) {
	h := reviewHarness(t, 140, 40)
	h.background = true
	h.press("c")
	require.True(t, h.m.review.saving)
	h.press("q", "ctrl+c")
	require.False(t, h.quit)
	h.settle()
	require.True(t, h.quit)
	require.Equal(t, OutcomePlanSaved, h.m.Result().Outcome)
}
