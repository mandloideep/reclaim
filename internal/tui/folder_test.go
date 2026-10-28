package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// fixtureFolder is the breakdown of ~/Code as the measuring walk of "This
// folder" gives it, for the fixture report.
func fixtureFolder() *Folder {
	code := "/Users/me/Code"
	wt := code + "/storefront/.claude/worktrees/agent-3f9c1e2a"
	return &Folder{Path: code, Size: 8_000 * MB, Entries: []FolderEntry{
		{Name: "ripgrep-fork", Path: code + "/ripgrep-fork", Dir: true, Size: 4_000 * MB, Entries: []FolderEntry{
			{Name: "target", Path: code + "/ripgrep-fork/target", Dir: true, Size: 3_900 * MB},
			{Name: "src", Path: code + "/ripgrep-fork/src", Dir: true, Size: 100 * MB},
		}},
		{Name: "ml-pipeline", Path: code + "/ml-pipeline", Dir: true, Size: 2_400 * MB, Entries: []FolderEntry{
			{Name: ".venv", Path: code + "/ml-pipeline/.venv", Dir: true, Size: 2_300 * MB},
		}},
		{Name: "storefront", Path: code + "/storefront", Dir: true, Size: 1_000 * MB, Entries: []FolderEntry{
			{Name: ".claude", Path: code + "/storefront/.claude", Dir: true, Size: 184 * MB, Entries: []FolderEntry{
				{Name: "worktrees", Path: code + "/storefront/.claude/worktrees", Dir: true, Size: 184 * MB, Entries: []FolderEntry{
					{Name: "agent-3f9c1e2a", Path: wt, Dir: true, Size: 184 * MB, Worktree: "squash, PR #41 merged"},
				}},
			}},
		}},
		{Name: "blog", Path: code + "/blog", Dir: true, Size: 30 * MB, Entries: []FolderEntry{
			{Name: "dist", Path: code + "/blog/dist", Dir: true, Size: 24 * MB},
		}},
		{Name: "notes.md", Path: code + "/notes.md", Size: 2_000},
	}}
}

// folderHarness scans "This folder" with a backend that returns the
// breakdown and opens its folder tab.
func folderHarness(t *testing.T, be *fakeBackend) *harness {
	t.Helper()
	be.report, be.folder = fixtureReport(), fixtureFolder()
	start := defaultStart()
	start.Paths = []string{"/Users/me/Code"}
	h := newHarness(t, be, start, 140, 40)
	h.press("enter")
	require.Equal(t, screenReview, h.m.screen)
	require.Contains(t, h.flat(), "Findings Folder ~/Code tab switches")
	h.press("tab")
	require.True(t, h.m.review.onFolder)
	return h
}

// TestFolderTab checks the folder breakdown of "This folder": the entries
// by size with the reclaimable bytes inside, the tier of findings, the
// worktree state words, opening folders, and the details of the entry
// under the cursor.
func TestFolderTab(t *testing.T) {
	h := folderHarness(t, &fakeBackend{})
	flat := h.flat()
	for _, want := range []string{
		"8.0 GB in all, 6.5 GB reclaimable inside",
		"4.0 GB ▸ ripgrep-fork/ 3.9 GB reclaimable inside",
		"2.4 GB ▸ ml-pipeline/ 2.3 GB reclaimable inside",
		"30.0 MB ▸ blog/ 24.0 MB reclaimable inside",
		"2.0 kB notes.md",
		"space toggle enter open x ignore tab findings",
	} {
		require.Contains(t, flat, want)
	}
	require.Contains(t, h.details(), "~/Code/ripgrep-fork 4.0 GB in all 3.9 GB of it reclaimable, in the findings below it")

	// enter opens a folder; a finding shows its box and tier and its
	// details are the findings tab's.
	h.press("enter", "down")
	flat = h.flat()
	require.Contains(t, flat, "▾ ripgrep-fork/")
	require.Contains(t, flat, "> [x] A 3.9 GB target/")
	require.Contains(t, h.details(), "Restore: cargo build")

	// Deep folders open one by one down to a worktree, with its words.
	for _, name := range []string{"storefront/", ".claude/", "worktrees/"} {
		for range 10 {
			if !containsCursor(h, name) {
				h.press("down")
			}
		}
		h.press("right")
	}
	require.Contains(t, h.flat(), "[x] A 184.0 MB agent-3f9c1e2a/ squash, PR #41 merged")

	// left closes a folder or goes up to the one that holds the entry.
	h.press("down", "left")
	require.True(t, containsCursor(h, "worktrees/"))
}

// containsCursor reports whether the row under the cursor shows s.
func containsCursor(h *harness, s string) bool {
	return strings.Contains(h.cursorRow(), s)
}

// TestFolderTabSharesTheSelection checks that space on the folder tab ticks
// the finding in the selection the findings tab shows, and that an entry
// that is no finding cannot be ticked.
func TestFolderTabSharesTheSelection(t *testing.T) {
	h := folderHarness(t, &fakeBackend{})
	id := finding.MakeID("rust-target", "/Users/me/Code/ripgrep-fork/target")
	h.press("space")
	require.Contains(t, h.flat(), "only an entry that is a finding can be ticked")
	h.press("enter", "down", "space")
	require.False(t, h.m.review.tree.IsSelected(id))
	require.Contains(t, h.flat(), "[ ] A 3.9 GB target/")
	h.press("tab")
	require.Contains(t, h.flat(), "Selected: 4 items")
	h.press("tab", "space")
	require.True(t, h.m.review.tree.IsSelected(id))

	// The tier C dist is ticked one by one here too, and the attention
	// rows cannot be.
	n, _ := h.m.review.tree.SelectedSize()
	require.Equal(t, 5, n)
}

// TestFolderTabIgnores checks x on the folder tab: the entry's path goes on
// the ignore list, and what lies in it leaves both tabs.
func TestFolderTabIgnores(t *testing.T) {
	be := &fakeBackend{}
	h := folderHarness(t, be)
	for range 10 {
		if !containsCursor(h, "blog/") {
			h.press("down")
		}
	}
	h.press("x")
	require.Contains(t, h.flat(), "Ignore ~/Code/blog from now on?")
	h.press("y")
	require.Equal(t, []string{"+/Users/me/Code/blog"}, be.ignores)
	require.NotContains(t, h.flat(), "24.0 MB reclaimable inside")
	require.Contains(t, h.flat(), "30.0 MB ▸ blog/ │")
	h.press("tab")
	require.NotContains(t, h.view(), "~/Code/blog")
}

// TestScanningShowsTheMeasuringWalk checks the project row while the
// measuring walk of "This folder" runs, before any scanner starts, and
// that the walk of the scanners replaces it once it reports.
func TestScanningShowsTheMeasuringWalk(t *testing.T) {
	h := newHarness(t, &fakeBackend{report: fixtureReport()}, defaultStart(), 120, 24)
	h.m.scanning = newScanning(h.m, ScanRequest{Mode: ModeFolder, Paths: []string{"/Users/me/Code"},
		Categories: []finding.Category{finding.CategoryProject, finding.CategoryWorktree}})
	h.m.screen = screenScanning
	h.update(scanEventMsg{seq: h.m.seq, event: scan.Event{Kind: scan.Step, Category: finding.CategoryProject,
		Text: "measuring the folder, 1500 folders"}})
	flat := h.flat()
	require.Contains(t, flat, "Scanning ~/Code")
	require.Contains(t, flat, "running Project artifacts measuring the folder, 1500 folders")
	require.Contains(t, flat, "waiting Git worktrees")
	h.update(scanEventMsg{seq: h.m.seq, event: scan.Event{Kind: scan.Step, Category: finding.CategoryProject, Folders: 1500, Projects: 4}})
	require.Contains(t, h.flat(), "4 projects in 1,500 folders")
}

// TestFolderTabSharedFolder checks a folder several findings share, such
// as Ollama's models folder: no tick box, and what they hold counts
// inside it.
func TestFolderTabSharedFolder(t *testing.T) {
	r := fixtureReport()
	models := "/Users/me/Code/models"
	for _, m := range []string{"llama3:latest", "qwen:7b"} {
		f := finding.Finding{Scanner: "ollama", Category: finding.CategoryAppCache, Tier: finding.TierB, Path: models,
			Target: "ollama:" + m, Name: "model " + m, Size: 1_000 * MB, Action: finding.ActionRunCommand, Command: []string{"ollama", "rm", m}}
		f.ID = finding.MakeID(f.Scanner, f.Target)
		r.Findings = append(r.Findings, f)
	}
	be := &fakeBackend{report: r, folder: fixtureFolder()}
	be.folder.Entries = append(be.folder.Entries, FolderEntry{Name: "models", Path: models, Dir: true, Size: 2_000 * MB})
	start := defaultStart()
	start.Paths = []string{"/Users/me/Code"}
	h := newHarness(t, be, start, 140, 40)
	h.press("enter", "tab")
	for range 10 {
		if !containsCursor(h, "models/") {
			h.press("down")
		}
	}
	require.Contains(t, h.cursorRow(), "2.0 GB    models/  2.0 GB reclaimable inside")
	require.NotContains(t, h.cursorRow(), "[")
	h.press("space")
	require.Contains(t, h.flat(), "only an entry that is a finding can be ticked")
}

// TestScanningIgnoresLateWalkProgress checks that a count the measuring
// walk reports out of order never moves the row backwards.
func TestScanningIgnoresLateWalkProgress(t *testing.T) {
	h := newHarness(t, &fakeBackend{report: fixtureReport()}, defaultStart(), 120, 24)
	h.m.scanning = newScanning(h.m, ScanRequest{Mode: ModeFolder, Paths: []string{"/Users/me/Code"},
		Categories: []finding.Category{finding.CategoryProject}})
	h.m.screen = screenScanning
	for _, n := range []int{1000, 500} {
		h.update(scanEventMsg{seq: h.m.seq, event: scan.Event{Kind: scan.Step, Category: finding.CategoryProject, Folders: n,
			Text: fmt.Sprintf("measuring the folder, %d folders", n)}})
	}
	require.Contains(t, h.flat(), "measuring the folder, 1000 folders")
}

// TestNoFolderTabForAWholeMachineScan checks that tab does nothing when the
// scan measured no folder.
func TestNoFolderTabForAWholeMachineScan(t *testing.T) {
	h := newHarness(t, &fakeBackend{report: fixtureReport()}, defaultStart(), 140, 40)
	h.press("enter", "tab")
	require.False(t, h.m.review.onFolder)
	require.NotContains(t, h.flat(), "tab switches")
}
