package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// ignoredMsg ends a change to the ignore list: the entries added and
// dropped, the list as written, or why it was not written.
type ignoredMsg struct {
	added, dropped []string
	list           []string
	err            error
}

// changeIgnore writes the ignore list through the backend, which writes
// that file and nothing else.
func (m *Model) changeIgnore(add, drop []string) tea.Cmd {
	backend := m.opts.Backend
	return func() tea.Msg {
		list, err := backend.Ignore(add, drop)
		return ignoredMsg{added: add, dropped: drop, list: list, err: err}
	}
}

// ignored takes the list as written and tells the screen that changed it.
func (m *Model) ignored(msg ignoredMsg) {
	if msg.err == nil {
		m.ignore = msg.list
	}
	switch m.screen {
	case screenReview:
		m.review.ignored(msg)
	case screenStart:
		m.start.ignored(msg)
	}
}

// excludedBy reports whether entries, clean absolute paths and target
// entries, name the finding: a path entry it is, lies under or holds, as
// for exclude, or a target entry naming its target.
func excludedBy(entries []string, f *finding.Finding) bool {
	if len(entries) == 0 {
		return false
	}
	targets := slices.DeleteFunc(slices.Clone(entries), func(e string) bool { return !finding.IsTargetEntry(e) })
	paths := slices.DeleteFunc(slices.Clone(entries), finding.IsTargetEntry)
	return len(scan.Filter{Exclude: paths, ExcludeTargets: targets}.Apply([]finding.Finding{*f})) == 0
}

// withoutIgnored returns the report without the findings the ignore list
// names, such as a report saved before an entry was added; the report
// itself is left as it is.
func (m *Model) withoutIgnored(r *finding.Report) *finding.Report {
	if r == nil || !slices.ContainsFunc(r.Findings, func(f finding.Finding) bool { return excludedBy(m.ignore, &f) }) {
		return r
	}
	c := *r
	c.Findings = slices.DeleteFunc(slices.Clone(r.Findings), func(f finding.Finding) bool { return excludedBy(m.ignore, &f) })
	return &c
}

// ignoreEntries are the entries x adds for findings: each one's path or
// target entry, without duplicates, and the number of findings that have
// neither, such as stale worktree registrations.
func ignoreEntries(fs []finding.Finding) ([]string, int) {
	var out []string
	skipped := 0
	for i := range fs {
		e, ok := finding.EntryFor(&fs[i])
		switch {
		case !ok:
			skipped++
		case !slices.Contains(out, e):
			out = append(out, e)
		}
	}
	return out, skipped
}
