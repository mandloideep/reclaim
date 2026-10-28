package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	refusedChecks := []apply.Check{{Name: apply.CheckLinked, OK: true}, {Name: apply.CheckState, Detail: "1 modified file"}}
	for _, msg := range []any{
		applyBeginMsg{seq: 7, logPath: "/Users/me/Library/Caches/reclaim/logs/apply-20261027T153045Z.log"},
		applyStartingMsg{seq: 7, index: 0},
		applyFinishedMsg{seq: 7, index: 0, outcome: apply.Outcome{Action: pl.Actions[0], Status: apply.StatusDone, Freed: 3_200 * MB}},
		applyStartingMsg{seq: 7, index: 1},
		applyFinishedMsg{seq: 7, index: 1, outcome: apply.Outcome{Action: pl.Actions[1], Status: apply.StatusFailed, Err: refusal, Refusal: refusal, Checks: refusedChecks}},
		applyStartingMsg{seq: 7, index: 2},
	} {
		h.update(msg)
	}
	flat := h.flat()
	for _, want := range []string{
		"Removing 3 of 5", "freed 3.2 GB of 9.6 GB", "33%",
		"done 3.2 GB docker prune build cache",
		"failed - git -C ~/Code/storefront worktree remove",
		"refused at work exists elsewhere",
		"refusing to remove the worktree ~/Code/storefront/.claude/worktrees/agent-3f9c1e2a: work exists elsewhere: 1 modified file",
		"git -C ~/Code/storefront worktree prune 2 stale registrations, 800 B expected",
		"2 more waiting",
		"Disk free on the volume of ~: 41.3 GB when this started, 41.5 GB now",
		"ctrl+c stops after the current action", "? help",
		"log: ~/Library/Caches/reclaim/logs/apply-20261027T153045Z.log",
	} {
		require.Contains(t, flat, want)
	}
	require.Contains(t, h.view(), "██████")

	// q does nothing here; ? and P open their overlays, which q and esc
	// close; ctrl+c stops after the current action.
	h.press("q")
	require.False(t, h.quit)
	require.Equal(t, screenRemoving, h.m.screen)
	h.press("?")
	require.Equal(t, overlayHelp, h.m.overlay)
	help := h.flat()
	require.Contains(t, help, "stop after the current action; the rest are marked not run")
	require.NotContains(t, help, "q quit", "q does nothing while a plan is removed")
	h.press("q")
	require.Equal(t, overlayNone, h.m.overlay)
	require.False(t, h.quit)
	h.press("P")
	require.Equal(t, overlayProtected, h.m.overlay)
	h.press("esc")
	require.Equal(t, overlayNone, h.m.overlay)
	require.False(t, canceled, "no overlay key stops the run")
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
	require.Len(t, res.Runs, 1)
	require.Equal(t, int64(9_400_000_000), res.Runs[0].Summary.Freed)
	require.Equal(t, savedPath, res.Runs[0].Plan.Path)
	require.NoError(t, res.Runs[0].Err)
	require.Nil(t, res.Pending)
	require.Equal(t, savedPath, res.Plan.Path)
}

// TestDoneShowsWorktreeChecks checks that a worktree that was removed lists
// the checks of its proof by name under its row.
func TestDoneShowsWorktreeChecks(t *testing.T) {
	be := &fakeBackend{outcome: func(a plan.Action, _ bool) apply.Outcome {
		o := apply.Outcome{Action: a, Status: apply.StatusDone, Freed: a.Size}
		if a.Action == finding.ActionGitWorktreeRemove {
			o.Checks = []apply.Check{{Name: apply.CheckLinked, OK: true}, {Name: apply.CheckState, OK: true}}
		}
		return o
	}}
	h := confirmHarness(t, be, 160, 50)
	typeWord(h, "yes")
	h.press("enter")
	require.Equal(t, screenDone, h.m.screen)
	require.Contains(t, h.m.removing.view().body, "linked worktree ok, work exists elsewhere ok")
}

// TestDoneShowsARunThatNeverStarted checks that an Apply error before the
// first action, such as a plan apply refuses as stale, is shown on Done
// and carried in the result.
func TestDoneShowsARunThatNeverStarted(t *testing.T) {
	be := &fakeBackend{applyErr: errors.New("plan is 25h old, older than 24h; scan again")}
	h := confirmHarness(t, be, 140, 40)
	typeWord(h, "yes")
	h.press("enter")
	require.Equal(t, screenDone, h.m.screen)
	flat := h.flat()
	require.Contains(t, flat, "Nothing to do.")
	require.Contains(t, flat, "Nothing was applied:")
	require.Contains(t, flat, "plan is 25h old, older than 24h; scan again")

	// An error that no failed action explains is shown too.
	h.m.removing.done(apply.Summary{Outcomes: []apply.Outcome{{Status: apply.StatusDone}}}, errors.New("write log: disk full"))
	require.Contains(t, h.flat(), "The run ended with an error: write log: disk full")
	h.press("q")
	res := h.m.Result()
	require.Len(t, res.Runs, 1)
	require.EqualError(t, res.Runs[0].Err, "plan is 25h old, older than 24h; scan again")
}

// TestResultKeepsEveryRun checks that a second pass after r adds its run to
// the result instead of replacing the first, and that a run in progress is
// pending until its end arrives.
func TestResultKeepsEveryRun(t *testing.T) {
	be := &fakeBackend{}
	h := confirmHarness(t, be, 140, 40)
	typeWord(h, "yes")
	h.press("enter")
	require.Equal(t, screenDone, h.m.screen)
	h.press("r")
	require.Equal(t, screenStart, h.m.screen)
	h.press("enter", "c")
	require.Equal(t, screenConfirm, h.m.screen)
	require.Equal(t, OutcomePlanSaved, h.m.Result().Outcome)
	typeWord(h, "yes")
	h.press("enter")
	require.Equal(t, screenDone, h.m.screen)
	res := h.m.Result()
	require.Equal(t, OutcomeApplied, res.Outcome)
	require.Len(t, res.Runs, 2, "both runs are kept")
	require.Equal(t, []string{savedPath, savedPath}, be.applied)

	// A run whose end the screen never sees is pending.
	m := New(Options{Backend: &fakeBackend{}, Now: func() time.Time { return now }})
	m.startRemoving(SavedPlan{Path: savedPath, Plan: &plan.Plan{}})
	require.NotNil(t, m.Result().Pending)
	require.Equal(t, savedPath, m.Result().Pending.Path)
}

// TestConfirmKeepsThePromptInAShortWindow checks the layout of Confirm in a
// window too short for everything: the prompt and the result of the dry
// run stay, the log has its own line, and the plan scrolls.
func TestConfirmKeepsThePromptInAShortWindow(t *testing.T) {
	be := &fakeBackend{}
	h := confirmHarness(t, be, 100, 16)
	h.press("d")
	view := h.view()
	require.Contains(t, view, "Type yes to remove these 5 items (9.6 GB):")
	require.Contains(t, view, "Checked without removing anything: 5 ok")
	var logLine string
	for l := range strings.SplitSeq(view, "\n") {
		if strings.Contains(l, "Log: ") {
			logLine = strings.TrimSpace(l)
		}
	}
	require.Equal(t, "Log: ~/Library/Caches/reclaim/logs/apply-20261027T153045Z.log", logLine, "the log has a line of its own")
	require.Contains(t, view, "5. A    2.3 GB", "the plan shows whole, the explanations under it give way")
	require.NotContains(t, view, "By tier")
	require.LessOrEqual(t, strings.Count(view, "\n")+1, 16, "the view fits the window")
	for _, height := range []int{6, 8, 10, 12} {
		h.update(tea.WindowSizeMsg{Width: 100, Height: height})
		view := h.view()
		require.Contains(t, view, "Type yes to remove these", height)
		require.Contains(t, view, "up and down scroll", height)
		require.Contains(t, view, "1. A    3.2 GB", height)
		require.LessOrEqual(t, strings.Count(view, "\n")+1, height, "the view fits the window")
	}
}

// TestReviewRefusesAStaleReport checks that c on a report older than apply
// accepts says so and saves no plan.
func TestReviewRefusesAStaleReport(t *testing.T) {
	r := fixtureReport()
	r.Created = now.Add(-25 * time.Hour)
	be := &fakeBackend{report: r}
	h := newHarness(t, be, defaultStart(), 140, 40)
	h.press("enter", "c")
	require.Equal(t, screenReview, h.m.screen)
	require.Contains(t, h.flat(), "This scan is 1d old and apply refuses plans from scans older than 1d; scan again from Start.")
	require.NotContains(t, h.calls(), "SavePlan")
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
