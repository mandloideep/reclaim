package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
)

const savedPath = "/Users/me/Library/Caches/reclaim/plans/plan-20261027T153000Z.json"

// confirmHarness scans, keeps the preselected tier A findings and
// continues to the Confirm screen.
func confirmHarness(t *testing.T, be *fakeBackend, width, height int) *harness {
	t.Helper()
	if be.report == nil {
		be.report = fixtureReport()
	}
	h := newHarness(t, be, defaultStart(), width, height)
	h.press("enter", "c")
	require.Equal(t, screenConfirm, h.m.screen)
	return h
}

func typeWord(h *harness, word string) {
	for _, r := range word {
		h.press(string(r))
	}
}

func TestConfirmScreen(t *testing.T) {
	h := confirmHarness(t, &fakeBackend{}, 140, 40)
	flat := h.flat()
	for _, want := range []string{
		"Confirm", "5 items, 9.6 GB",
		"Plan written to ~/Library/Caches/reclaim/plans/plan-20261027T153000Z.json",
		"1. A 3.2 GB docker prune build cache (2 unused records)",
		"2. A 184.0 MB git -C ~/Code/storefront worktree remove ~/Code/storefront/.claude/worktrees/agent-3f9c1e2a",
		"3. A 800 B git -C ~/Code/storefront worktree prune 2 stale registrations",
		"4. A 3.9 GB remove ~/Code/ripgrep-fork/target",
		"By tier: A 9.6 GB in 5 items.",
		"Every target is checked again right before it is removed",
		"Nothing is deleted until you confirm.",
		"Type yes to remove these 5 items (9.6 GB):",
		"d checks everything without removing", "esc back",
	} {
		require.Contains(t, flat, want)
	}

	// Anything but the whole word yes removes nothing.
	for _, word := range []string{"no", "ye", "YES", "yes please", "y"} {
		typeWord(h, word)
		h.press("enter")
		require.Equal(t, screenConfirm, h.m.screen, word)
		require.Contains(t, h.flat(), "Type yes, the whole word, to remove these; anything else removes nothing.")
	}
	require.NotContains(t, h.calls(), "Apply", "nothing ran")

	// q and ? are typed into the word, not acted on, once typing began.
	typeWord(h, "yq")
	require.False(t, h.quit)
	require.Equal(t, "yq", h.m.confirm.input.Value())
	h.press("esc")
	require.Empty(t, h.m.confirm.input.Value(), "esc clears the word first")

	// esc goes back to Review with the selection as it was.
	h.press("n")
	require.Equal(t, screenConfirm, h.m.screen, "n is typed, not a Review key")
	h.press("esc", "esc")
	require.Equal(t, screenReview, h.m.screen)
	n, _ := h.m.review.tree.SelectedSize()
	require.Equal(t, 5, n)
	require.NotContains(t, h.calls(), "Apply")
}

func TestConfirmNamesTheWorktreesLeftOut(t *testing.T) {
	r := fixtureReport()
	p, err := plan.New(r, []finding.Finding{r.Findings[0]}, "laptop", now)
	require.NoError(t, err)
	be := &fakeBackend{report: r, saved: SavedPlan{Path: savedPath, Plan: p,
		LeftOut: []string{"refusing to remove the worktree ~/Code/storefront/.claude/worktrees/agent-3f9c1e2a: work exists elsewhere: 1 modified file"}}}
	h := confirmHarness(t, be, 140, 40)
	require.Contains(t, h.flat(), "Left out of the plan: refusing to remove the worktree ~/Code/storefront/.claude/worktrees/agent-3f9c1e2a: work exists elsewhere")
}

// TestConfirmDryRun checks d: every row is marked with its result, a refused
// worktree lists the checks of the proof by name, and nothing is removed.
func TestConfirmDryRun(t *testing.T) {
	be := &fakeBackend{outcome: func(a plan.Action, dry bool) apply.Outcome {
		require.True(t, dry, "only the dry run runs in this test")
		o := apply.Outcome{Action: a, Status: apply.StatusChecked, Checks: []apply.Check{{Name: apply.CheckRules, OK: true}}}
		if a.Action == finding.ActionGitWorktreeRemove {
			o.Status = apply.StatusRefused
			o.Checks = []apply.Check{{Name: apply.CheckLinked, OK: true}, {Name: apply.CheckPath, OK: true},
				{Name: apply.CheckState, Detail: "1 modified file"}}
			o.Err = errors.New("refusing to remove the worktree: work exists elsewhere: 1 modified file")
		}
		return o
	}}
	h := confirmHarness(t, be, 160, 50)
	h.press("d")
	require.Equal(t, []string{"dry " + savedPath}, be.applied)
	flat := h.flat()
	require.Contains(t, flat, "ok 1. A 3.2 GB docker prune build cache (2 unused records)")
	require.Contains(t, flat, "refused 2. A 184.0 MB")
	require.Contains(t, flat, "refusing to remove the worktree: work exists elsewhere: 1 modified file")
	require.Contains(t, flat, "linked worktree ok, path ok, work exists elsewhere failed")
	require.Contains(t, flat, "Checked without removing anything: 4 ok, 0 gone, 0 for you to run, 1 refused.")
	require.Contains(t, flat, "Log: ~/Library/Caches/reclaim/logs/apply-20261027T153045Z.log")
	require.Equal(t, screenConfirm, h.m.screen, "a dry run stays on Confirm")
}

// TestScreensCallSavePlanThenApply proves that the Review, Confirm and
// Removing screens together call exactly SavePlan and then Apply with the
// path SavePlan returned, the real run only after yes was typed, and
// nothing else that could remove anything.
func TestScreensCallSavePlanThenApply(t *testing.T) {
	be := &fakeBackend{}
	h := confirmHarness(t, be, 140, 40)
	h.press("d")
	typeWord(h, "yes")
	require.Equal(t, []string{"dry " + savedPath}, be.applied, "typing alone runs nothing")
	h.press("enter")
	require.Equal(t, screenDone, h.m.screen)
	require.Equal(t, []string{"dry " + savedPath, savedPath}, be.applied)
	readOnly := []string{"Scan", "Protected", "FreeSpace"}
	var acting []string
	for _, c := range h.calls() {
		if !slices.Contains(readOnly, c) {
			acting = append(acting, c)
		}
	}
	require.Equal(t, []string{"SavePlan", "Apply", "Apply"}, acting, "SavePlan, the dry run on d, and the run after yes")
}

func TestRemovingScreen(t *testing.T) {
	be := &fakeBackend{}
	h := confirmHarness(t, be, 140, 30)
	saved := h.m.confirm.saved
	canceled := false
	h.m.removing = newRemoving(h.m, saved)
	h.m.screen = screenRemoving
	h.m.seq = 7
	h.m.cancel = func() { canceled = true }
	pl := saved.Plan
	refusal := &apply.Refusal{Path: pl.Actions[1].Path, Check: apply.CheckState, Detail: "1 modified file"}
	for _, msg := range []any{
		applyBeginMsg{seq: 7, logPath: "/Users/me/Library/Caches/reclaim/logs/apply-20261027T153045Z.log"},
		applyStartingMsg{seq: 7, index: 0},
		applyFinishedMsg{seq: 7, index: 0, outcome: apply.Outcome{Action: pl.Actions[0], Status: apply.StatusDone, Freed: 3_200 * MB}},
		applyStartingMsg{seq: 7, index: 1},
		applyFinishedMsg{seq: 7, index: 1, outcome: apply.Outcome{Action: pl.Actions[1], Status: apply.StatusFailed, Err: refusal, Refusal: refusal}},
		applyStartingMsg{seq: 7, index: 2},
	} {
		h.update(msg)
	}
	flat := h.flat()
	for _, want := range []string{
		"Removing 3 of 5", "freed 3.2 GB of 9.6 GB", "33%",
		"done 3.2 GB docker prune build cache",
		"failed - git -C ~/Code/storefront worktree remove",
		"refused at work exists elsewhere: 1 modified file",
		"git -C ~/Code/storefront worktree prune 2 stale registrations, 800 B expected",
		"2 more waiting",
		"Disk free on the volume of ~: 41.3 GB when this started, 41.5 GB now",
		"ctrl+c stops after the current action",
		"log: ~/Library/Caches/reclaim/logs/apply-20261027T153045Z.log",
	} {
		require.Contains(t, flat, want)
	}
	require.Contains(t, h.view(), "██████")

	// q does nothing here; ctrl+c stops after the current action.
	h.press("q")
	require.False(t, h.quit)
	require.Equal(t, screenRemoving, h.m.screen)
	h.press("ctrl+c")
	require.True(t, canceled)
	require.False(t, h.quit)
	require.Contains(t, h.flat(), "Stopping after the current action; the rest are marked not run.")
}

func TestDoneScreen(t *testing.T) {
	be := &fakeBackend{outcome: func(a plan.Action, _ bool) apply.Outcome {
		o := apply.Outcome{Action: a, Status: apply.StatusDone, Freed: a.Size}
		if a.Action == finding.ActionGitWorktreeRemove {
			ref := &apply.Refusal{Path: a.Path, Check: apply.CheckState, Detail: "1 modified file",
				Hints:        []string{"git -C /Users/me/Code/storefront/.claude/worktrees/agent-3f9c1e2a add -A"},
				ForceCommand: "git -C /Users/me/Code/storefront worktree remove --force /Users/me/Code/storefront/.claude/worktrees/agent-3f9c1e2a"}
			o = apply.Outcome{Action: a, Status: apply.StatusFailed, Err: ref, Refusal: ref}
		}
		if a.Action == finding.ActionGitWorktreePrune {
			o = apply.Outcome{Action: a, Status: apply.StatusGone}
		}
		return o
	}}
	h := confirmHarness(t, be, 160, 50)
	typeWord(h, "yes")
	h.press("enter")
	require.Equal(t, screenDone, h.m.screen)
	flat := h.flat()
	for _, want := range []string{
		"Done", "freed 9.4 GB in 0:00",
		"3 removed, 1 already gone, 1 failed",
		"Disk free on the volume of ~:",
		"Failed:", "refusing to remove the worktree ~/Code/storefront/.claude/worktrees/agent-3f9c1e2a: work exists elsewhere: 1 modified file",
		"to keep the work, first run:", "add -A", "worktree remove --force",
		"Log: ~/Library/Caches/reclaim/logs/apply-20261027T153045Z.log",
		"Plan: ~/Library/Caches/reclaim/plans/plan-20261027T153000Z.json",
		"r scan again", "q quit",
	} {
		require.Contains(t, flat, want)
	}
	h.press("q")
	require.True(t, h.quit)
	res := h.m.Result()
	require.Equal(t, OutcomeApplied, res.Outcome)
	require.Equal(t, int64(9_400_000_000), res.Summary.Freed)
	require.Equal(t, savedPath, res.Plan.Path)
}

func TestDoneShowsSudoCommandsAndScansAgain(t *testing.T) {
	r := fixtureReport()
	var runtime finding.Finding
	for _, f := range r.Findings {
		if f.NeedsSudo {
			runtime = f
		}
	}
	p, err := plan.New(r, []finding.Finding{runtime}, "laptop", now)
	require.NoError(t, err)
	be := &fakeBackend{report: r, saved: SavedPlan{Path: savedPath, Plan: p}}
	h := confirmHarness(t, be, 140, 40)
	require.Contains(t, h.flat(), "you run: sudo xcrun simctl runtime delete iOS-17-0 printed, never run")
	typeWord(h, "yes")
	h.press("enter")
	flat := h.flat()
	require.Contains(t, flat, "1 needs you")
	require.Contains(t, flat, "Needs administrator rights, run it yourself if you want it:")
	require.Contains(t, flat, "sudo xcrun simctl runtime delete iOS-17-0")
	h.press("r")
	require.Equal(t, screenStart, h.m.screen, "r goes back to Start with the same options")
	require.True(t, strings.Contains(h.view(), "> Whole machine"))
}
