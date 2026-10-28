package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/tui"
)

// TestUIEndToEndRemovesOnlyTheTickedPaths drives the screens with the real
// backend over the end to end fixture, through Start, Scanning, Review,
// Confirm with its dry run, Removing and Done, and proves that only the
// ticked paths are gone: the assertion the --preset safe test makes,
// reached through the screen.
func TestUIEndToEndRemovesOnlyTheTickedPaths(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	decoy := filepath.Join(a.home, "elsewhere", "app")
	writeTree(t, decoy, map[string]string{"package.json": "{}", "node_modules/x/index.js": "decoy"})
	before := snapshot(t, root)
	homeBefore := snapshot(t, a.home)

	d := newDriver(t, a, []string{root}, uiFlags{minSize: "0", out: "plan.json"})
	d.key("enter")
	require.Contains(t, d.m.View(), "Review")
	d.key("c")
	view := d.m.View()
	require.Contains(t, view, "Confirm")
	require.Contains(t, view, "Type yes to remove these 6 items")

	// d runs every check and removes nothing.
	d.key("d")
	require.Contains(t, d.m.View(), "Checked without removing anything: 6 ok, 0 gone, 0 for you to run, 0 refused.")
	require.Equal(t, homeBefore, snapshot(t, a.home), "the dry run removed nothing")

	// Anything but yes removes nothing either.
	d.key("y", "e", "enter")
	require.Contains(t, d.m.View(), "Type yes, the whole word")
	require.Equal(t, homeBefore, snapshot(t, a.home))

	d.key("y", "e", "s", "enter")
	view = d.m.View()
	require.Contains(t, view, "Done")
	require.Contains(t, view, "6 removed")

	removed := []string{"web/node_modules", "web/dist", "web/.next", "py/.venv", "py/pkg/__pycache__", "crate/target"}
	var want []string
	for _, rel := range before {
		if !slices.ContainsFunc(removed, func(r string) bool { return rel == r || strings.HasPrefix(rel, r+"/") }) {
			want = append(want, rel)
		}
	}
	require.Equal(t, want, snapshot(t, root), "only the ticked tier A paths are gone")
	require.FileExists(t, filepath.Join(root, "lib", "dist", "only-copy.js"))
	require.FileExists(t, filepath.Join(root, "stray", "node_modules", "z", "index.js"))
	require.FileExists(t, filepath.Join(decoy, "node_modules", "x", "index.js"))

	// The dry run and the real run each wrote a log; the dry run's says so.
	dryLogs, err := filepath.Glob(filepath.Join(a.stateDir, "logs", "dry-run-*.log"))
	require.NoError(t, err)
	require.Len(t, dryLogs, 1)
	data, err := os.ReadFile(dryLogs[0])
	require.NoError(t, err)
	var start map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &start))
	require.Equal(t, true, start["dry_run"])

	d.key("q")
	require.True(t, d.quit)
	res := d.m.Result()
	require.Equal(t, tui.OutcomeApplied, res.Outcome)
	out.Reset()
	require.NoError(t, a.uiSummary(context.Background(), res, "plan.json"))
	summary := out.String()
	require.Contains(t, summary, "Freed ")
	require.Contains(t, summary, ": 6 removed, 0 already gone, 0 failed, 0 not run, 0 for you to run.")
	require.Contains(t, summary, "Log: "+filepath.Join(a.stateDir, "logs", "apply-"))
	require.Contains(t, summary, "Plan: "+res.Runs[0].Plan.Path)
}

// TestUISavePlanReleasesItsReservedName checks that a plan that cannot be
// written leaves no empty reserved file behind, and that releasing a name
// removes nothing but that empty file.
func TestUISavePlanReleasesItsReservedName(t *testing.T) {
	a, _ := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	r, err := be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{root},
		Categories: []finding.Category{finding.CategoryProject}}, nil)
	require.NoError(t, err)
	selected := plan.SelectTiers(r.Findings, []finding.Tier{finding.TierA})
	other := filepath.Join(a.stateDir, "plans", "plan-other.json")
	writeTree(t, filepath.Dir(other), map[string]string{"plan-other.json": "{}"})
	plansBefore := snapshot(t, filepath.Dir(other))

	be.save = func(string, *plan.Plan) error { return errors.New("disk full") }
	_, err = be.SavePlan(context.Background(), r, selected)
	require.EqualError(t, err, "disk full")
	require.Equal(t, plansBefore, snapshot(t, filepath.Dir(other)), "the reserved name is released and nothing else changes")

	// A file that is not the empty reservation is never removed.
	full := filepath.Join(filepath.Dir(other), "plan-full.json")
	require.NoError(t, os.WriteFile(full, []byte("{}"), 0o644))
	releasePlanPath(full)
	require.FileExists(t, full)
	require.FileExists(t, other)
}

// savedFixturePlan scans the end to end fixture with the real backend and
// saves a plan of its tier A findings, the six paths the end to end test
// removes.
func savedFixturePlan(t *testing.T, a *app) (*uiBackend, tui.SavedPlan, string) {
	t.Helper()
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	r, err := be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{root},
		Categories: []finding.Category{finding.CategoryProject}}, nil)
	require.NoError(t, err)
	saved, err := be.SavePlan(context.Background(), r, plan.SelectTiers(r.Findings, []finding.Tier{finding.TierA}))
	require.NoError(t, err)
	require.Len(t, saved.Plan.Actions, 6)
	return be, saved, root
}

// TestUIApplyRunsOnlyThePlansItSaved checks that the backend applies only a
// plan file SavePlan wrote, and only as it was written: a plan it did not
// save, or one changed on disk since, is refused and nothing is removed.
func TestUIApplyRunsOnlyThePlansItSaved(t *testing.T) {
	a, _ := testApp(t, "")
	be, saved, _ := savedFixturePlan(t, a)
	homeBefore := snapshot(t, a.home)

	other := filepath.Join(a.stateDir, "plans", "plan-by-hand.json")
	data, err := os.ReadFile(saved.Path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(other, data, 0o644))
	_, err = be.Apply(context.Background(), other, false, apply.Progress{})
	require.EqualError(t, err, "refusing plan "+other+": the screen did not save it")

	// The same plan with its last action dropped, as an edit by hand would.
	var edited plan.Plan
	require.NoError(t, json.Unmarshal(data, &edited))
	edited.Actions = edited.Actions[:5]
	require.NoError(t, plan.Save(saved.Path, &edited))
	for _, dry := range []bool{true, false} {
		_, err = be.Apply(context.Background(), saved.Path, dry, apply.Progress{})
		require.EqualError(t, err, "refusing plan "+saved.Path+": it changed after the screen saved it; review it with reclaim apply instead")
	}
	require.NoError(t, os.Remove(other))
	require.Equal(t, homeBefore, snapshot(t, a.home), "nothing was removed")
}

// TestUIApplyStopFinishesTheRunningAction checks that canceling the context
// of a run, as ctrl+c on the Removing screen does, lets the action that
// runs finish whole and marks the rest not run, with nothing of them
// removed.
func TestUIApplyStopFinishesTheRunningAction(t *testing.T) {
	a, _ := testApp(t, "")
	be, saved, root := savedFixturePlan(t, a)
	first := saved.Plan.Actions[0].Path
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sum, err := be.Apply(ctx, saved.Path, false, apply.Progress{Starting: func(i int, _ plan.Action) {
		if i == 0 {
			cancel()
		}
	}})
	require.EqualError(t, err, "apply interrupted: stopped after the action that was running")
	require.Len(t, sum.Outcomes, 6)
	require.Equal(t, apply.StatusDone, sum.Outcomes[0].Status, "the running action finishes")
	require.NoDirExists(t, first)
	for i, o := range sum.Outcomes[1:] {
		require.Equal(t, apply.StatusNotRun, o.Status, i+1)
		require.DirExists(t, o.Action.Path, "an action not run removes nothing")
	}
	require.DirExists(t, filepath.Join(root, "lib", "dist"))
}

// TestUIFinishWaitsForTheRun checks the end of the command when the
// program ends while a plan is removed, as on a signal: the run stops
// after the action in progress, the command waits for it, prints what it
// did and returns its error with the program's.
func TestUIFinishWaitsForTheRun(t *testing.T) {
	a, out := testApp(t, "")
	be, saved, _ := savedFixturePlan(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_, _ = be.Apply(ctx, saved.Path, false, apply.Progress{Starting: func(i int, _ plan.Action) {
			if i == 0 {
				close(started)
				// The action waits for the program to end, then runs.
				<-ctx.Done()
			}
		}})
	}()
	<-started
	out.Reset()
	err := a.uiFinish(ctx, cancel, be, tui.Result{Outcome: tui.OutcomeApplied, Pending: &saved}, tea.ErrInterrupted, "plan.json")
	// The summary holds the run's outcomes, which exist only once it has
	// ended, so uiFinish waited for it.
	<-ran
	require.ErrorIs(t, err, tea.ErrInterrupted)
	require.ErrorContains(t, err, "apply "+saved.Path+": apply interrupted: stopped after the action that was running")
	summary := out.String()
	require.Contains(t, summary, ": 1 removed, 0 already gone, 0 failed, 5 not run, 0 for you to run.")
	require.Contains(t, summary, "Plan: "+saved.Path)
	require.NoDirExists(t, saved.Plan.Actions[0].Path)
	for _, act := range saved.Plan.Actions[1:] {
		require.DirExists(t, act.Path)
	}

	// A run the screen started as it closed never starts.
	_, err = be.Apply(context.Background(), saved.Path, false, apply.Progress{})
	require.ErrorIs(t, err, errNotStarted)
	for _, act := range saved.Plan.Actions[1:] {
		require.DirExists(t, act.Path)
	}
}

// TestUIFinishReportsARunThatNeverStarted checks a run whose plan the
// screen passed on as the program ended, before Apply was reached.
func TestUIFinishReportsARunThatNeverStarted(t *testing.T) {
	a, out := testApp(t, "")
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	saved := tui.SavedPlan{Path: filepath.Join(a.stateDir, "plans", "plan-x.json"), Plan: &plan.Plan{}}
	ctx, cancel := context.WithCancel(context.Background())
	err = a.uiFinish(ctx, cancel, be, tui.Result{Outcome: tui.OutcomeApplied, Pending: &saved}, nil, "plan.json")
	require.ErrorIs(t, err, errNotStarted)
	require.Equal(t, "Nothing was applied from "+saved.Path+".\n", out.String())
}

// TestUISummaryPrintsEveryRun checks that every run of the screen is
// printed, and that a run that failed makes the command fail.
func TestUISummaryPrintsEveryRun(t *testing.T) {
	a, out := testApp(t, "")
	done := apply.Outcome{Status: apply.StatusDone, Freed: 1_000_000}
	failed := apply.Outcome{Status: apply.StatusFailed, Err: errors.New("boom")}
	res := tui.Result{Outcome: tui.OutcomeQuit, Scanned: true, Runs: []tui.Run{
		{Plan: tui.SavedPlan{Path: "/state/plans/one.json"}, Summary: apply.Summary{Freed: 1_000_000, Outcomes: []apply.Outcome{done}}},
		{Plan: tui.SavedPlan{Path: "/state/plans/two.json"}, Summary: apply.Summary{Outcomes: []apply.Outcome{failed}, Failed: 1},
			Err: errors.New("1 of 1 actions failed")},
	}}
	err := a.uiSummary(context.Background(), res, "plan.json")
	require.EqualError(t, err, "apply /state/plans/two.json: 1 of 1 actions failed")
	summary := out.String()
	require.Contains(t, summary, "Plan: /state/plans/one.json")
	require.Contains(t, summary, "Plan: /state/plans/two.json")
	require.Contains(t, summary, ": 1 removed, 0 already gone, 0 failed")
	require.Contains(t, summary, ": 0 removed, 0 already gone, 1 failed")
	require.NotContains(t, summary, "Nothing was removed", "a screen that applied a plan never says so")
}
