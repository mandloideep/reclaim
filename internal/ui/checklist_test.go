package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

func newTestChecklist(r *finding.Report) *Checklist {
	m := NewChecklist(r, ChecklistOptions{Home: "/Users/me", PlanPath: "plan.json", Provenance: "Report from reclaim scan, 2h ago", Now: now})
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return m
}

// press feeds keys to the model the way bubbletea does. Named keys are
// "space", "enter", "esc", "up", "down", "left" and "right"; anything else is
// typed as runes.
func press(t *testing.T, m *Checklist, keys ...string) tea.Cmd {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var model tea.Model
		model, cmd = m.Update(msg)
		require.Same(t, m, model)
	}
	return cmd
}

// moveTo moves the cursor down to the first row whose title contains s.
func moveTo(t *testing.T, m *Checklist, s string) {
	t.Helper()
	press(t, m, "home")
	for range len(m.rows) {
		if strings.Contains(m.current().title, s) {
			return
		}
		press(t, m, "down")
	}
	t.Fatalf("no row %q in\n%s", s, m.View())
}

func selectedTargets(m *Checklist) []string {
	sel := m.Selected()
	out := make([]string, 0, len(sel))
	for _, f := range sel {
		out = append(out, f.Target)
	}
	return out
}

func TestChecklistStartsWithTierASelected(t *testing.T) {
	m := newTestChecklist(report())
	require.ElementsMatch(t, []string{
		"/Users/me/Code/app/node_modules", "/tmp/x/__pycache__", "docker-build-cache", "c1", "sha256:abc", "/Users/me/Library/Caches/Google",
	}, selectedTargets(m))
	view := m.View()
	require.NotContains(t, view, "\x1b[", "no colors without a terminal")
	for _, want := range []string{
		"reclaim select\nReport from reclaim scan, 2h ago",
		"sort: size  tier: all",
		"> [-] ▾ Project artifacts  2.9 GB  3 items, 2 selected",
		"  [x] ▸ ~/Code/app  2.9 GB  1 item, 1 selected",
		"  [ ] ▸ ~/Code/lib  5.0 MB  1 item",
		"[-] ▾ Docker  1.5 GB  5 items, 3 selected",
		"[ ] ▾ Downloads  200.0 MB  2 items",
		"Selected: 6 items, 4.0 GB   w writes plan.json   q quits without writing",
		"space toggle  enter expand  / filter  s sort  t tier  a select tier A",
	} {
		require.Contains(t, view, want)
	}
	// Groups start collapsed, so their findings are not rows yet.
	require.NotContains(t, view, "node_modules  node_modules")
}

func TestChecklistGroupToggleNeverTouchesTierC(t *testing.T) {
	m := newTestChecklist(report())
	moveTo(t, m, "Project artifacts")
	press(t, m, "space")
	require.NotContains(t, selectedTargets(m), "/Users/me/Code/app/node_modules", "all tier A and B were selected, so the toggle clears them")
	require.NotContains(t, selectedTargets(m), "/Users/me/Code/lib/dist")
	require.Contains(t, m.View(), "1 tier C item left as they were; tick tier C items one by one")

	press(t, m, "space")
	require.Contains(t, selectedTargets(m), "/Users/me/Code/app/node_modules")
	require.Contains(t, selectedTargets(m), "/tmp/x/__pycache__")
	require.NotContains(t, selectedTargets(m), "/Users/me/Code/lib/dist", "a group never selects tier C")

	// A group of only tier C has nothing a toggle may change.
	moveTo(t, m, "~/Code/lib")
	press(t, m, "space")
	require.NotContains(t, selectedTargets(m), "/Users/me/Code/lib/dist")

	// Tier C is selected when ticked on its own.
	press(t, m, "enter", "down")
	require.Equal(t, "/Users/me/Code/lib/dist", m.findings[m.current().leaf].Target)
	require.Contains(t, m.View(), "[ ] C    5.0 MB  dist  dist")
	require.Contains(t, m.View(), "! may be the only copy", "tier C rows show their warning")
	press(t, m, "space")
	require.Contains(t, selectedTargets(m), "/Users/me/Code/lib/dist")
	require.Contains(t, m.View(), "[x] C    5.0 MB  dist")
}

func TestChecklistGroupToggleSelectsTierB(t *testing.T) {
	m := newTestChecklist(report())
	moveTo(t, m, "Docker")
	press(t, m, "space")
	for _, target := range []string{"sha256:img", "vol", "sha256:abc", "docker-build-cache", "c1"} {
		require.Contains(t, selectedTargets(m), target)
	}
	press(t, m, "space")
	for _, target := range []string{"sha256:img", "vol", "sha256:abc", "docker-build-cache", "c1"} {
		require.NotContains(t, selectedTargets(m), target)
	}
}

func TestChecklistAttentionCannotBeSelected(t *testing.T) {
	m := newTestChecklist(report())
	moveTo(t, m, "Attention")
	press(t, m, "space")
	require.Contains(t, m.View(), " -  ▸ Attention, never removed  4.0 GB  1 item, not counted, never removed")
	require.Contains(t, m.View(), "[ ] ▾ Downloads  200.0 MB  2 items", "attention is not reclaimable, so it adds nothing")
	press(t, m, "enter", "down")
	require.Contains(t, m.View(), " -       4.0 GB  ~/Downloads/talk.mov  old-downloads")
	require.Contains(t, m.View(), "cannot be selected  ! not touched for 6mo", "the row says it cannot be ticked")
	press(t, m, "space")
	require.Contains(t, m.View(), "listed for attention only")
	require.NotContains(t, selectedTargets(m), "/Users/me/Downloads/talk.mov")

	moveTo(t, m, "Downloads")
	press(t, m, "space")
	require.Contains(t, selectedTargets(m), "/Users/me/Downloads/Zen.dmg")
	require.NotContains(t, selectedTargets(m), "/Users/me/Downloads/talk.mov")
}

func TestChecklistFilterAndTier(t *testing.T) {
	m := newTestChecklist(report())
	press(t, m, "/", "h", "o", "m", "e")
	require.True(t, m.filtering)
	view := m.View()
	require.Contains(t, view, "/home")
	require.Contains(t, view, "Package caches")
	require.Contains(t, view, "~/Library/Caches/Homebrew")
	require.NotContains(t, view, "Project artifacts")
	press(t, m, "enter")
	require.False(t, m.filtering)
	require.Contains(t, m.View(), "filter: home")

	// A group toggle under a filter changes only what the filter shows.
	moveTo(t, m, "Package caches")
	press(t, m, "space")
	require.Contains(t, selectedTargets(m), "/Users/me/Library/Caches/Homebrew")
	press(t, m, "esc")
	require.Contains(t, m.View(), "Project artifacts")
	require.Contains(t, m.View(), "Selected: 7 items")

	// The text filter matches group titles, so a project shows all its findings.
	press(t, m, "/", "C", "o", "d", "e", "/", "l", "i", "b", "enter")
	require.Contains(t, m.View(), "dist")
	require.NotContains(t, m.View(), "node_modules")
	press(t, m, "esc")

	press(t, m, "t")
	require.Contains(t, m.View(), "tier: A")
	require.NotContains(t, m.View(), "Package caches", "no tier A package caches")
	press(t, m, "t", "t")
	require.Contains(t, m.View(), "tier: C")
	require.Contains(t, m.View(), "talk.mov")
	press(t, m, "t")
	require.Contains(t, m.View(), "tier: all")

	press(t, m, "/", "z", "z", "z", "enter")
	require.Contains(t, m.View(), "nothing matches the filters")
	press(t, m, "/", "esc")
	require.NotContains(t, m.View(), "nothing matches")
}

func TestChecklistSelectAllTierA(t *testing.T) {
	m := newTestChecklist(report())
	moveTo(t, m, "Project artifacts")
	press(t, m, "space")
	require.NotContains(t, selectedTargets(m), "/tmp/x/__pycache__")
	press(t, m, "a")
	require.Contains(t, selectedTargets(m), "/tmp/x/__pycache__")
	require.Contains(t, m.View(), "selected 2 more tier A items")
	require.Len(t, m.Selected(), 6)
}

func TestChecklistSortAndExpand(t *testing.T) {
	m := newTestChecklist(report())
	require.Contains(t, m.View(), "sort: size")
	press(t, m, "s")
	require.Contains(t, m.View(), "sort: name")
	view := m.View()
	require.Less(t, strings.Index(view, "(not in a project)"), strings.Index(view, "~/Code/app"), "by name")
	press(t, m, "s")
	require.Contains(t, m.View(), "sort: oldest first")
	press(t, m, "s")
	require.Contains(t, m.View(), "sort: size")

	moveTo(t, m, "Project artifacts")
	press(t, m, "enter")
	require.NotContains(t, m.View(), "~/Code/app", "collapsed")
	press(t, m, "right")
	require.Contains(t, m.View(), "~/Code/app")
	press(t, m, "left")
	require.NotContains(t, m.View(), "~/Code/app")
}

// TestChecklistNarrowWindow checks that a narrow window still shows the end
// of the provenance line, which says when a report covers only some folders,
// and the whole warning of the finding under the cursor.
func TestChecklistNarrowWindow(t *testing.T) {
	prov := "Report from reclaim here ~/Code/web, 5m ago (2026-10-05 11:55): 3 findings, 3.2 GB reclaimable. It covers only ~/Code/web, not the whole machine; run reclaim scan for everything."
	m := NewChecklist(report(), ChecklistOptions{Home: "/Users/me", PlanPath: "plan.json", Provenance: prov, Now: now})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	view := m.View()
	require.LessOrEqual(t, strings.Count(view, "\n")+1, 24)
	require.Contains(t, strings.Join(strings.Fields(view), " "), "It covers only ~/Code/web, not the whole machine; run reclaim scan for everything.")
	for l := range strings.SplitSeq(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(l), 60, l)
	}

	moveTo(t, m, "Attention")
	press(t, m, "enter", "down")
	require.Contains(t, m.View(), "! not touched for 6mo")
	moveTo(t, m, "~/Code/lib")
	press(t, m, "enter", "down")
	require.Contains(t, strings.Join(strings.Fields(m.View()), " "), "! may be the only copy")
}

func TestChecklistWriteAndQuit(t *testing.T) {
	m := newTestChecklist(report())
	cmd := press(t, m, "w")
	require.Equal(t, ChecklistWrite, m.Outcome())
	require.NotNil(t, cmd)
	require.IsType(t, tea.QuitMsg{}, cmd())
	require.Len(t, m.Selected(), 6)

	m = newTestChecklist(report())
	cmd = press(t, m, "q")
	require.Equal(t, ChecklistQuit, m.Outcome())
	require.IsType(t, tea.QuitMsg{}, cmd())
}

// TestChecklistLongList checks that a few thousand findings render only the
// rows that fit, and that the cursor scrolls the window.
func TestChecklistLongList(t *testing.T) {
	r := &finding.Report{Version: 1}
	for i := range 3000 {
		path := fmt.Sprintf("/Users/me/Code/p%04d/node_modules", i)
		r.Findings = append(r.Findings, finding.Finding{
			Scanner: "node_modules", Category: finding.CategoryProject, Tier: finding.TierA, Path: path, Target: path,
			Project: fmt.Sprintf("/Users/me/Code/p%04d", i), Size: int64(3000 - i), Action: finding.ActionRemovePath, LastUsed: now.Add(-time.Hour),
		})
	}
	m := newTestChecklist(r)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	view := m.View()
	require.LessOrEqual(t, strings.Count(view, "\n")+1, 20, "the view never exceeds the window")
	require.Contains(t, view, "Selected: 3000 items")
	require.Contains(t, view, "~/Code/p0000")
	require.NotContains(t, view, "~/Code/p2999")

	press(t, m, "G")
	view = m.View()
	require.Contains(t, view, ">   [x] ▸ ~/Code/p2999")
	require.NotContains(t, view, "~/Code/p0000")
	require.LessOrEqual(t, strings.Count(view, "\n")+1, 20)
	press(t, m, "right", "down")
	require.Contains(t, m.View(), ">     [x] A       1 B  node_modules  node_modules  used 1h ago")
	press(t, m, "up", "left")

	start := time.Now()
	for range 200 {
		press(t, m, "up")
		_ = m.View()
	}
	require.Less(t, time.Since(start), 5*time.Second, "moving through a long list stays responsive")

	moveTo(t, m, "Project artifacts")
	press(t, m, "space")
	require.Empty(t, m.Selected())
}
