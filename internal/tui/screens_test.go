package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

func defaultStart() StartOptions {
	return StartOptions{Folder: "/Users/me/Code/storefront", MinSize: 10 * MB, GitHub: true}
}

func TestStartScreen(t *testing.T) {
	h := newHarness(t, &fakeBackend{report: fixtureReport()}, defaultStart(), 140, 30)
	view := h.view()
	for _, want := range []string{
		"reclaim", "v0.3.0", "What to look at",
		"> Whole machine   ~/Code, ~/Downloads, worktrees, package caches, Docker, app caches",
		"This folder     ~/Code/storefront",
		"Worktrees       linked git worktrees of the repositories under ~/Code, ~/Downloads",
		"Scanners   [x] Project artifacts  [x] Git worktrees  [x] Package caches  [x] Docker  [x] App caches  [x] Downloads",
		"Hide findings under 10.0 MB", "Stale projects only: off", "Ask GitHub about pull requests: on",
		"No report yet.",
		"Never removed: your home folder, ~/Documents, ~/Desktop, project roots, .git folders, anything outside the scanned roots.",
		"enter scan", "q quit",
	} {
		require.Contains(t, view, want)
	}
	require.Contains(t, h.flat(), "anything outside the scanned roots. P shows the full list")
	require.Empty(t, h.calls(), "the Start screen reaches the backend only when asked")
	for l := range strings.SplitSeq(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(l), 140, l)
	}
	require.Len(t, strings.Split(view, "\n"), 30, "the frame fills the window")
}

func TestStartScreenRequest(t *testing.T) {
	be := &fakeBackend{report: fixtureReport()}
	h := newHarness(t, be, defaultStart(), 140, 30)
	// Right along the scanner row to Docker, untick it; then cycle the
	// minimum size, the stale filter and the GitHub lookup.
	h.press("right", "right", "right", "space", "m", "s", "s", "g")
	require.Contains(t, h.view(), "[ ] Docker")
	require.Contains(t, h.view(), "Hide findings under 50.0 MB")
	require.Contains(t, h.view(), "Stale projects only: 90 days")
	require.Contains(t, h.view(), "Ask GitHub about pull requests: off")
	h.press("enter")
	require.Equal(t, []string{"Scan"}, h.calls())
	require.Equal(t, ScanRequest{
		Mode:       ModeMachine,
		Categories: []finding.Category{finding.CategoryProject, finding.CategoryWorktree, finding.CategoryPackageCache, finding.CategoryAppCache, finding.CategoryDownloads},
		MinSize:    50 * MB,
		Stale:      90 * 24 * time.Hour,
	}, be.requests[0])
}

func TestStartScreenFolderAndWorktrees(t *testing.T) {
	be := &fakeBackend{report: fixtureReport()}
	h := newHarness(t, be, defaultStart(), 140, 30)
	h.press("p")
	require.True(t, h.m.start.editing)
	require.Contains(t, h.view(), "folder: ~/Code/storefront")
	// q and ? are typed into the path, not acted on.
	h.press("ctrl+u")
	for _, r := range "~/Code/q?" {
		h.press(string(r))
	}
	h.press("enter")
	require.False(t, h.quit)
	require.Contains(t, h.view(), "> This folder     ~/Code/q?")
	require.Contains(t, h.view(), "Scanners   [x] Project artifacts  [x] Git worktrees  [ ] Package caches")
	h.press("enter")
	require.Equal(t, ScanRequest{Mode: ModeFolder, Paths: []string{"~/Code/q?"},
		Categories: []finding.Category{finding.CategoryProject, finding.CategoryWorktree}, MinSize: 10 * MB, GitHub: true}, be.requests[0])

	// Worktrees runs the worktree scanners alone, whatever their size.
	h = newHarness(t, be, defaultStart(), 140, 30)
	h.press("down", "down")
	require.Contains(t, h.view(), "> Worktrees")
	require.Contains(t, h.view(), "Scanners   [x] Git worktrees")
	h.press("enter")
	require.Equal(t, ScanRequest{Mode: ModeWorktrees, Categories: []finding.Category{finding.CategoryWorktree}, GitHub: true}, be.requests[1])
}

func TestStartScreenDisabledAndPaths(t *testing.T) {
	start := defaultStart()
	start.Paths = []string{"/Users/me/Code/a", "/Users/me/Code/b"}
	start.All = true
	cats := categories()
	cats[3] = CategoryChoice{Category: finding.CategoryDocker, Note: "disabled in config"}
	start.Categories = cats
	be := &fakeBackend{report: fixtureReport()}
	h := newHarness(t, be, start, 160, 30)
	view := h.view()
	require.Contains(t, view, "> This folder     ~/Code/a, ~/Code/b")
	require.Contains(t, view, "[ ] Docker (disabled in config)")
	require.Contains(t, view, "[x] Package caches", "--all ticks the categories outside the folder")
	h.press("enter")
	require.True(t, be.requests[0].All)
	require.NotContains(t, be.requests[0].Categories, finding.CategoryDocker)

	// Nothing ticked is refused on the screen.
	h = newHarness(t, be, StartOptions{Categories: []CategoryChoice{{Category: finding.CategoryProject}}}, 140, 30)
	h.press("enter")
	require.Contains(t, h.view(), "Tick at least one scanner category first.")
	require.Len(t, be.requests, 1)
}

func TestScanningScreenAfterEvents(t *testing.T) {
	h := newHarness(t, &fakeBackend{report: fixtureReport()}, defaultStart(), 120, 24)
	h.m.scanning = newScanning(h.m, ScanRequest{Mode: ModeMachine, Categories: finding.Categories()})
	h.m.screen = screenScanning
	h.m.scanning.began = now.Add(-7 * time.Second)
	for _, ev := range []scan.Event{
		{Kind: scan.Planned, Scanners: []scan.PlannedScanner{
			{Name: "node_modules", Category: finding.CategoryProject}, {Name: "rust-target", Category: finding.CategoryProject},
			{Name: "git-worktrees", Category: finding.CategoryWorktree},
			{Name: "npm-cache", Category: finding.CategoryPackageCache},
			{Name: "user-caches", Category: finding.CategoryAppCache}, {Name: "ollama", Category: finding.CategoryAppCache},
			{Name: "installers", Category: finding.CategoryDownloads},
		}},
		{Kind: scan.Step, Category: finding.CategoryProject, Folders: 3412, Projects: 57},
		{Kind: scan.ScannerStarted, Scanner: "node_modules", Category: finding.CategoryProject},
		{Kind: scan.ScannerStarted, Scanner: "rust-target", Category: finding.CategoryProject},
		{Kind: scan.ScannerFinished, Scanner: "node_modules", Category: finding.CategoryProject, Findings: 5, Size: 4_800 * MB},
		{Kind: scan.ScannerFinished, Scanner: "rust-target", Category: finding.CategoryProject, Findings: 4, Size: 3_900 * MB},
		{Kind: scan.ScannerStarted, Scanner: "git-worktrees", Category: finding.CategoryWorktree},
		{Kind: scan.Step, Scanner: "git-worktrees", Category: finding.CategoryWorktree, Text: "asking GitHub about 4 branches"},
		{Kind: scan.ScannerStarted, Scanner: "npm-cache", Category: finding.CategoryPackageCache},
		{Kind: scan.ScannerFinished, Scanner: "npm-cache", Category: finding.CategoryPackageCache, Findings: 4, Size: 5_800 * MB},
		{Kind: scan.ScannerFinished, Scanner: "docker", Category: finding.CategoryDocker, Err: errors.New("not available: no socket")},
		{Kind: scan.ScannerStarted, Scanner: "user-caches", Category: finding.CategoryAppCache},
		{Kind: scan.ScannerFinished, Scanner: "user-caches", Category: finding.CategoryAppCache, Findings: 5, Size: 5_200 * MB},
	} {
		h.update(scanEventMsg{seq: h.m.seq, event: ev})
	}
	view := h.view()
	for _, want := range []string{
		"Scanning the whole machine", "0:07 elapsed",
		"done      Project artifacts   57 projects in 3,412 folders",
		"9 findings     8.7 GB",
		"running   Git worktrees       asking GitHub about 4 branches",
		"done      Package caches",
		"warning   Docker              docker: not available: no socket",
		"running   App caches",
		"waiting   Downloads",
		"Found so far: 18 findings, 19.7 GB reclaimable",
		"esc stops the scan and shows what was found so far",
	} {
		require.Contains(t, view, want)
	}
	// A scanner that has not started yet keeps its category running.
	require.Contains(t, view, "running   App caches")

	// Events of an older scan are ignored.
	h.update(scanEventMsg{seq: h.m.seq + 5, event: scan.Event{Kind: scan.ScannerFinished, Scanner: "installers", Category: finding.CategoryDownloads, Findings: 3}})
	require.Contains(t, h.view(), "waiting   Downloads")
}

func TestScanFlowOpensReview(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), events: []scan.Event{
		{Kind: scan.Planned, Scanners: []scan.PlannedScanner{{Name: "rust-target", Category: finding.CategoryProject}}},
		{Kind: scan.ScannerStarted, Scanner: "rust-target", Category: finding.CategoryProject},
		{Kind: scan.ScannerFinished, Scanner: "rust-target", Category: finding.CategoryProject, Findings: 1, Size: 3_900 * MB},
	}}
	h := newHarness(t, be, defaultStart(), 140, 40)
	h.press("enter")
	require.Equal(t, screenReview, h.m.screen)
	require.True(t, h.m.result.Scanned)
	require.Contains(t, h.view(), "Review")
	require.Contains(t, h.view(), "reclaim scan --min-size=10MB, just now: 11 findings and 4 for attention only")

	// The last report opens again from Start with o; here it is reached by
	// quitting to a fresh model holding it.
	h2 := newHarness(t, be, defaultStart(), 140, 40)
	h2.m.lastReport = h.m.lastReport
	require.Contains(t, h2.view(), "Last report: reclaim scan --min-size=10MB, just now")
	h2.press("o")
	require.Equal(t, screenReview, h2.m.screen)
	require.Equal(t, []string{"Scan"}, h.calls(), "o opens the report without scanning")
}

func TestScanFailureReturnsToStart(t *testing.T) {
	be := &fakeBackend{scanErr: errScan}
	h := newHarness(t, be, defaultStart(), 140, 30)
	h.press("enter")
	require.Equal(t, screenStart, h.m.screen)
	require.Contains(t, h.view(), "Scan failed: no such folder: /Users/me/nope")
	require.False(t, h.m.result.Scanned)
}

func TestScanStoppedWithEsc(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), wait: true, events: []scan.Event{
		{Kind: scan.Planned, Scanners: []scan.PlannedScanner{{Name: "rust-target", Category: finding.CategoryProject}}},
	}}
	h := newHarness(t, be, defaultStart(), 140, 40)
	h.background = true
	h.press("enter")
	require.Equal(t, screenScanning, h.m.screen)
	h.press("esc")
	require.Contains(t, h.view(), "Stopping")
	h.settle()
	require.Equal(t, screenReview, h.m.screen)
	require.False(t, h.m.result.Scanned, "a stopped scan is not the last report")
	require.Nil(t, h.m.lastReport)
	flat := h.flat()
	require.Contains(t, flat, "The scan was stopped before it finished: these are the findings so far")
	require.Contains(t, flat, "Warnings and notes (3)")
	h.moveTo("Warnings and notes")
	h.press("enter")
	require.Contains(t, h.view(), "scan interrupted: context canceled")
}

func TestQuitStopsTheScan(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), wait: true}
	h := newHarness(t, be, defaultStart(), 140, 40)
	h.background = true
	h.press("enter")
	h.press("q")
	require.True(t, h.quit)
	h.settle()
	require.Equal(t, OutcomeQuit, h.m.Result().Outcome)
}

// TestNarrowWindow checks that every screen fits 80 columns and still shows
// all of what it says, wrapping instead of cutting.
func TestNarrowWindow(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), wait: true}
	h := newHarness(t, be, defaultStart(), 80, 30)
	fits := func() {
		t.Helper()
		for l := range strings.SplitSeq(h.view(), "\n") {
			require.LessOrEqual(t, lipgloss.Width(l), 80, l)
		}
	}
	fits()
	flat := h.flat()
	require.Contains(t, flat, "Whole machine ~/Code, ~/Downloads, worktrees, package caches, Docker, app caches")
	require.Contains(t, flat, "anything outside the scanned roots. P shows the full list")
	require.Contains(t, flat, "o open last report P protected ? help q quit", "the footer wraps instead of losing keys")

	h.background = true
	h.press("enter")
	h.update(scanEventMsg{seq: h.m.seq, event: scan.Event{Kind: scan.Step, Category: finding.CategoryProject, Folders: 29970, Projects: 130}})
	fits()
	require.Contains(t, h.flat(), "130 projects in 29,970 folders")

	h.press("esc")
	h.settle()
	fits()
	require.True(t, strings.HasPrefix(h.view(), "Review  "), "the title keeps its space before the facts")
	require.Contains(t, h.flat(), "The scan was stopped before it finished: these are the findings so far, and the warnings say what was not scanned.")
}
