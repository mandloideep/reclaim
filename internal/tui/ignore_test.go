package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

// ignoreHarness scans the fixture with be and opens the Review screen.
func ignoreHarness(t *testing.T, be *fakeBackend, edit func(*Options)) *harness {
	t.Helper()
	if be.report == nil {
		be.report = fixtureReport()
	}
	h := newHarnessWith(t, be, defaultStart(), 140, 40, edit)
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
	h := ignoreHarness(t, be, nil)
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
	h := ignoreHarness(t, be, nil)
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

// TestReviewIgnoreFails checks that a list that cannot be written changes
// nothing on the screen and says why.
func TestReviewIgnoreFails(t *testing.T) {
	be := &fakeBackend{ignoreErr: errors.New("read-only file system")}
	h := ignoreHarness(t, be, nil)
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

// TestStartIgnoreListEmpty checks the empty list and d on it.
func TestStartIgnoreListEmpty(t *testing.T) {
	h := newHarness(t, &fakeBackend{}, defaultStart(), 140, 40)
	h.press("x")
	require.Contains(t, h.flat(), "The ignore list is empty.")
	h.press("d")
	require.Contains(t, h.flat(), "The ignore list is empty.")
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
