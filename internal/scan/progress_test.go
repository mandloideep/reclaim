package scan

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

// recorder collects events from several goroutines.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) record(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// of returns the events about one scanner, in the order they arrived.
func (r *recorder) of(scanner string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, ev := range r.events {
		if ev.Scanner == scanner {
			out = append(out, ev)
		}
	}
	return out
}

func TestRunReportsProgress(t *testing.T) {
	stepper := &fakeScanner{name: "stepper", category: finding.CategoryWorktree, scan: func(_ context.Context, env Env) ([]finding.Finding, error) {
		env.Step("checking 2 worktrees")
		env.Step("asking GitHub about 2 branches")
		return []finding.Finding{
			{Path: "/h/Code/a/.claude/worktrees/x", Tier: finding.TierA, Action: finding.ActionRemovePath, Size: 300},
			{Path: "/h/Code/a/.claude/worktrees/x/node_modules", Tier: finding.TierA, Action: finding.ActionRemovePath, Size: 100},
			{Path: "/h/Code/a/.claude/worktrees/y", Tier: finding.TierC, Action: finding.ActionNone, Size: 50},
		}, nil
	}}
	failing := &fakeScanner{name: "failing", category: finding.CategoryDocker, scan: func(context.Context, Env) ([]finding.Finding, error) {
		return nil, errors.New("Docker is not running")
	}}
	crashing := &fakeScanner{name: "crashing", category: finding.CategoryAppCache, scan: func(context.Context, Env) ([]finding.Finding, error) {
		panic("boom")
	}}
	rec := &recorder{}
	res := Run(context.Background(), []Scanner{stepper, failing, crashing}, Env{Progress: rec.record}, 2)
	require.Len(t, res.Findings, 3)

	require.Equal(t, Planned, rec.events[0].Kind, "the plan comes before any scanner starts")
	require.Equal(t, []PlannedScanner{
		{Name: "stepper", Category: finding.CategoryWorktree},
		{Name: "failing", Category: finding.CategoryDocker},
		{Name: "crashing", Category: finding.CategoryAppCache},
	}, rec.events[0].Scanners)

	got := rec.of("stepper")
	kinds := make([]EventKind, len(got))
	for i, ev := range got {
		kinds[i] = ev.Kind
		require.Equal(t, finding.CategoryWorktree, ev.Category, "the runner fills in the category")
	}
	require.Equal(t, []EventKind{ScannerStarted, Step, Step, ScannerFinished}, kinds)
	require.Equal(t, "checking 2 worktrees", got[1].Text)
	require.Equal(t, "asking GitHub about 2 branches", got[2].Text)
	done := got[3]
	require.NoError(t, done.Err)
	require.Equal(t, 2, done.Findings, "attention findings are not counted")
	require.Equal(t, int64(300), done.Size, "a finding inside another is counted once")

	failed := rec.of("failing")
	require.Len(t, failed, 2)
	require.EqualError(t, failed[1].Err, "scanner failed: Docker is not running")
	crashed := rec.of("crashing")
	require.Len(t, crashed, 2)
	require.ErrorContains(t, crashed[1].Err, "scanner crashed and was skipped: boom")
	require.True(t, slices.ContainsFunc(res.Warnings, func(w finding.Warning) bool {
		return w.Scanner == "failing" && w.Message == "scanner failed: Docker is not running"
	}), "a failure still lands in the warnings")
}

func TestRunWithoutProgress(t *testing.T) {
	s := &fakeScanner{name: "quiet", category: finding.CategoryProject, scan: func(_ context.Context, env Env) ([]finding.Finding, error) {
		env.Step("no one listens")
		return nil, nil
	}}
	res := Run(context.Background(), []Scanner{s}, Env{}, 1)
	require.Empty(t, res.Findings)
}

func TestRunCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &recorder{}
	s := &fakeScanner{name: "never", category: finding.CategoryProject, scan: returns()}
	res := Run(ctx, []Scanner{s}, Env{Progress: rec.record}, 1)
	require.Empty(t, res.Findings)
	require.Len(t, rec.events, 1, "only the plan: a scanner that never started sends nothing")
	require.True(t, slices.ContainsFunc(res.Warnings, func(w finding.Warning) bool { return w.Message == "scan interrupted: context canceled" }))
}

func TestRunCountsWhatTheReportShows(t *testing.T) {
	s := &fakeScanner{name: "caches", category: finding.CategoryPackageCache, scan: returns(
		finding.Finding{Path: "/h/big", Tier: finding.TierB, Action: finding.ActionRemovePath, Size: 50_000_000},
		finding.Finding{Path: "/h/small", Tier: finding.TierB, Action: finding.ActionRemovePath, Size: 1_000},
		finding.Finding{Path: "/h/excluded", Tier: finding.TierB, Action: finding.ActionRemovePath, Size: 90_000_000},
	)}
	rec := &recorder{}
	res := Run(context.Background(), []Scanner{s}, Env{Progress: rec.record, Shown: Filter{MinSize: 10_000_000, Exclude: []string{"/h/excluded"}}}, 1)
	require.Len(t, res.Findings, 3, "the runner itself filters nothing")
	done := rec.of("caches")[1]
	require.Equal(t, ScannerFinished, done.Kind)
	require.Equal(t, 1, done.Findings)
	require.Equal(t, int64(50_000_000), done.Size)
}
