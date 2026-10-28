package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

// ignoreHarness scans the fixture with be and opens the Review screen.
func ignoreHarness(t *testing.T, be *fakeBackend) *harness {
	t.Helper()
	if be.report == nil {
		be.report = fixtureReport()
	}
	h := newHarness(t, be, defaultStart(), 140, 40)
	h.press("enter")
	require.Equal(t, screenReview, h.m.screen)
	return h
}

// shown reports whether the Review tree still holds the finding at path.
func shown(h *harness, path string) bool {
	return slices.ContainsFunc(h.m.review.tree.Findings(), func(f finding.Finding) bool { return f.Path == path })
}

// TestReviewIgnoresAFinding checks x on a finding: the one line
// confirmation names it and says how to undo it, anything but y adds
// nothing, and y writes the entry through Ignore and drops the finding,
// selection included.
func TestReviewIgnoresAFinding(t *testing.T) {
	be := &fakeBackend{}
	h := ignoreHarness(t, be)
	const target = "/Users/me/Code/ripgrep-fork/target"
	require.True(t, h.m.review.tree.IsSelected(finding.MakeID("rust-target", target)), "tier A starts selected")
	h.moveTo("~/Code/ripgrep-fork")
	h.press("enter", "down", "x")
	require.Contains(t, h.flat(), "Ignore ~/Code/ripgrep-fork/target from now on? y adds it to the ignore list, any other key keeps it; x on the Start screen undoes it.")

	// q answers the question; it does not quit.
	h.press("q")
	require.False(t, h.quit)
	require.Contains(t, h.flat(), "Nothing was added to the ignore list.")
	require.NotContains(t, h.calls(), "Ignore")
	require.True(t, shown(h, target))

	h.press("x", "y")
	require.Equal(t, []string{"+" + target}, be.ignores)
	require.Contains(t, h.flat(), "On the ignore list now, 1 finding no longer offered")
	require.False(t, shown(h, target))
	require.False(t, h.m.review.tree.IsSelected(finding.MakeID("rust-target", target)), "its selection goes with it")
	require.NotContains(t, h.view(), "ripgrep-fork")
	require.Equal(t, []string{target}, h.m.ignore)
}

// TestReviewIgnoresAGroupAndTargets checks x on a group, which adds every
// finding below it, and on a Docker object, which adds its target entry.
func TestReviewIgnoresAGroupAndTargets(t *testing.T) {
	be := &fakeBackend{}
	h := ignoreHarness(t, be)
	h.moveTo("Unused images")
	h.press("enter", "down", "x")
	require.Contains(t, h.flat(), "Ignore image node:20 (docker:sha256:node20) from now on?")
	h.press("y")
	require.Equal(t, []string{"+docker:sha256:node20"}, be.ignores)
	require.NotContains(t, h.view(), "node:20")

	// Stale registrations have no path; the repository group still adds
	// its worktrees and the other clone.
	h.moveTo("~/Code/storefront")
	h.press("enter")
	h.moveTo("2 stale worktree registrations")
	h.press("x")
	require.Contains(t, h.flat(), "Stale worktree registrations have no path to ignore")
	h.moveTo("~/Code/storefront")
	h.press("x")
	require.Contains(t, h.flat(), "Ignore the 5 items of ~/Code/storefront from now on?")
	h.press("y")
	require.Len(t, be.ignores, 6)
	require.NotContains(t, h.view(), "fix/checkout-rounding")
	require.Contains(t, h.view(), "2 stale worktree registrations", "what has no entry stays")

	// A category is not a thing to ignore.
	h.moveTo("Package caches")
	h.press("x")
	require.Contains(t, h.flat(), "x ignores a finding, or every finding of a group; move to one first.")
}

// TestReviewIgnoreSaysWhatElseGoes checks that the question names the
// findings a path entry also stops offering, here the worktree that holds
// the node_modules being ignored, that q quits nothing while the list is
// written, and that the cursor stays where it was.
func TestReviewIgnoreSaysWhatElseGoes(t *testing.T) {
	r := fixtureReport()
	wt := "/Users/me/Code/storefront/.claude/worktrees/agent-3f9c1e2a"
	nm := finding.Finding{Scanner: "node_modules", Category: finding.CategoryProject, Tier: finding.TierA, Path: wt + "/node_modules",
		Kind: finding.KindDir, Target: wt + "/node_modules", Project: wt, Size: 50 * MB, Action: finding.ActionRemovePath}
	nm.ID = finding.MakeID(nm.Scanner, nm.Target)
	r.Findings = append(r.Findings, nm)
	finding.Sort(r.Findings)
	be := &fakeBackend{report: r}
	h := ignoreHarness(t, be)
	h.moveTo("agent-3f9c1e2a")
	h.press("enter", "down")
	before := h.cursorRow()
	require.Contains(t, before, "node_modules")
	h.press("x")
	require.Contains(t, h.flat(), "It also stops offering 1 other finding that holds it.")
	h.m.review.writing = true
	h.press("q")
	require.False(t, h.quit, "no key acts while the list is written")
	h.m.review.writing = false
	h.press("y")
	require.Contains(t, h.flat(), "2 findings no longer offered")
	require.NotEqual(t, 0, h.m.review.tree.Current().Count, "the cursor stays on a row")
	require.NotContains(t, h.cursorRow(), "Project artifacts", "and does not jump to the top")
}

// TestTabWhileFiltering checks that tab does not leave a filter half typed:
// it switches tabs only once the filter is closed.
func TestTabWhileFiltering(t *testing.T) {
	be := &fakeBackend{}
	be.report, be.folder = fixtureReport(), fixtureFolder()
	start := defaultStart()
	start.Paths = []string{"/Users/me/Code"}
	h := newHarness(t, be, start, 140, 40)
	h.press("enter", "/", "c", "a", "tab")
	require.False(t, h.m.review.onFolder)
	require.True(t, h.m.review.tree.Filtering())
	require.Equal(t, screenReview, h.m.screen, "c was typed, not a continue")
	h.press("enter", "tab")
	require.True(t, h.m.review.onFolder)
}

// TestReviewIgnoreFails checks that a list that cannot be written changes
// nothing on the screen and says why.
func TestReviewIgnoreFails(t *testing.T) {
	be := &fakeBackend{ignoreErr: errors.New("read-only file system")}
	h := ignoreHarness(t, be)
	h.moveTo("~/Code/ripgrep-fork")
	h.press("enter", "down", "x", "y")
	require.Contains(t, h.flat(), "The ignore list was not written: read-only file system")
	require.True(t, shown(h, "/Users/me/Code/ripgrep-fork/target"))
	require.Empty(t, h.m.ignore)
}

// TestStartIgnoreList checks the ignore list on the Start screen: the count,
// x opening the list, d with its confirmation taking an entry off through
// Ignore, and esc going back.
func TestStartIgnoreList(t *testing.T) {
	be := &fakeBackend{report: fixtureReport(), ignore: []string{"/Users/me/Code/blog/dist", "docker:sha256:node20"}}
	h := newHarnessWith(t, be, defaultStart(), 140, 40, func(o *Options) { o.Ignore = slices.Clone(be.ignore) })
	require.Contains(t, h.flat(), "Ignored: 2")
	require.Contains(t, h.flat(), "x ignored")
	h.press("x")
	flat := h.flat()
	for _, want := range []string{"Ignore list ~/.config/reclaim/ignore", "Never offered again, and apply refuses them",
		"> ~/Code/blog/dist", "docker:sha256:node20", "d take off the list", "esc back"} {
		require.Contains(t, flat, want)
	}
	h.press("down", "d")
	require.Contains(t, h.flat(), "Take docker:sha256:node20 off the ignore list, so scans offer it again? y takes it off, any other key keeps it.")
	h.press("n")
	require.Contains(t, h.flat(), "Kept on the ignore list.")
	require.NotContains(t, h.calls(), "Ignore")
	h.press("d", "y")
	require.Equal(t, []string{"-docker:sha256:node20"}, be.ignores)
	require.Contains(t, h.flat(), "Took docker:sha256:node20 off the ignore list; the next scan offers it again.")
	require.NotContains(t, h.flat(), "> docker")
	h.press("esc")
	require.Contains(t, h.flat(), "Ignored: 1")

	// The report a scan returns shows without what the list names, here
	// the dist folder the fake scan still reports.
	h.press("enter")
	require.Equal(t, screenReview, h.m.screen)
	require.False(t, shown(h, "/Users/me/Code/blog/dist"))
	require.True(t, slices.ContainsFunc(h.m.review.tree.Findings(), func(f finding.Finding) bool { return f.Target == "sha256:node20" }))
}

// TestStartIgnoreListScrolls checks a list longer than the window: the
// cursor stays in view and d takes off the entry under it.
func TestStartIgnoreListScrolls(t *testing.T) {
	list := make([]string, 0, 40)
	for i := range 40 {
		list = append(list, fmt.Sprintf("/Users/me/Code/app%02d/dist", i))
	}
	be := &fakeBackend{ignore: slices.Clone(list)}
	h := newHarnessWith(t, be, defaultStart(), 100, 20, func(o *Options) { o.Ignore = slices.Clone(list) })
	h.press("x")
	require.Contains(t, h.flat(), "> ~/Code/app00/dist")
	require.Contains(t, h.flat(), "up and down scroll")
	require.LessOrEqual(t, strings.Count(h.view(), "\n")+1, 20)
	for range 30 {
		h.press("down")
	}
	require.Contains(t, h.flat(), "> ~/Code/app30/dist")
	require.NotContains(t, h.flat(), "app00")
	h.press("d", "y")
	require.Equal(t, []string{"-/Users/me/Code/app30/dist"}, be.ignores)
	require.LessOrEqual(t, strings.Count(h.view(), "\n")+1, 20)
}

// TestStartIgnoreListEmpty checks the empty list and d on it.
func TestStartIgnoreListEmpty(t *testing.T) {
	h := newHarness(t, &fakeBackend{}, defaultStart(), 140, 40)
	h.press("x")
	require.Contains(t, h.flat(), "The ignore list is empty.")
	h.press("d")
	require.Contains(t, h.flat(), "There is nothing to take off; x on the Review screen adds to the list.")
	h.press("x")
	require.Contains(t, h.flat(), "What to look at")
}

// TestReportOptionOpensReview checks --report: the Review screen opens at
// once on the report, without what the ignore list names.
func TestReportOptionOpensReview(t *testing.T) {
	be := &fakeBackend{}
	h := newHarnessWith(t, be, defaultStart(), 140, 40, func(o *Options) {
		o.Report = fixtureReport()
		o.Ignore = []string{"/Users/me/Code/ml-pipeline"}
	})
	require.Equal(t, screenReview, h.m.screen)
	require.NotContains(t, h.calls(), "Scan")
	require.False(t, shown(h, "/Users/me/Code/ml-pipeline/.venv"))
	require.True(t, shown(h, "/Users/me/Code/ripgrep-fork/target"))
	require.True(t, strings.Contains(h.flat(), "Review"))
}
