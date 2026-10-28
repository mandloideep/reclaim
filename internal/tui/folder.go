package tui

import (
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/ui"
)

// folderTab is the second tab of the Review screen in "This folder" mode:
// the breakdown of reclaim here, the entries by size with the reclaimable
// bytes inside each, the tier of the entries that are findings and the
// state words of linked worktrees. A row that is a finding is ticked with
// space, and the selection is the findings tab's.
type folderTab struct {
	s        *reviewScreen
	folder   *Folder
	expanded map[string]bool
	rows     []folderRow
	cursor   int
	offset   int
	height   int
	// byPath are the findings of the report on the review screen that the
	// ignore list leaves, by path, and inside the reclaimable size below
	// every folder of the breakdown.
	byPath map[string]*finding.Finding
	inside map[string]int64
}

// folderRow is one visible line of the tab.
type folderRow struct {
	entry *FolderEntry
	depth int
}

func newFolderTab(s *reviewScreen, f *Folder) *folderTab {
	t := &folderTab{s: s, folder: f, expanded: map[string]bool{}, height: 10}
	t.index()
	t.rebuild()
	return t
}

// index maps the findings to the entries of the breakdown: the finding at
// each path, and the reclaimable bytes below every entry, a finding inside
// another counted once, as report totals do.
func (t *folderTab) index() {
	t.byPath = map[string]*finding.Finding{}
	var actionable []finding.Finding
	for i := range t.s.report.Findings {
		f := &t.s.report.Findings[i]
		if f.Path == "" || excludedBy(t.s.m.ignore, f) {
			continue
		}
		if prev, ok := t.byPath[f.Path]; !ok || (!prev.Actionable() && f.Actionable()) {
			t.byPath[f.Path] = f
		}
		if f.Actionable() {
			actionable = append(actionable, *f)
		}
	}
	t.inside = map[string]int64{}
	var walk func(es []FolderEntry)
	walk = func(es []FolderEntry) {
		for i := range es {
			e := &es[i]
			var below []finding.Finding
			for _, f := range actionable {
				if f.Path == e.Path || strings.HasPrefix(f.Path, e.Path+string(filepath.Separator)) {
					below = append(below, f)
				}
			}
			t.inside[e.Path] = finding.ReclaimableSize(below)
			walk(e.Entries)
		}
	}
	walk(t.folder.Entries)
}

// rebuild flattens the open folders into rows, keeping the cursor on the
// same entry.
func (t *folderTab) rebuild() {
	var at string
	if t.cursor < len(t.rows) {
		at = t.rows[t.cursor].entry.Path
	}
	t.rows = t.rows[:0]
	var add func(es []FolderEntry, depth int)
	add = func(es []FolderEntry, depth int) {
		for i := range es {
			t.rows = append(t.rows, folderRow{entry: &es[i], depth: depth})
			if t.expanded[es[i].Path] {
				add(es[i].Entries, depth+1)
			}
		}
	}
	add(t.folder.Entries, 0)
	t.cursor = max(slices.IndexFunc(t.rows, func(r folderRow) bool { return r.entry.Path == at }), 0)
	t.scroll()
}

// current is the entry under the cursor, nil in an empty folder.
func (t *folderTab) current() *FolderEntry {
	if t.cursor < len(t.rows) {
		return t.rows[t.cursor].entry
	}
	return nil
}

// currentFinding is the finding at the entry under the cursor, if any.
func (t *folderTab) currentFinding() *finding.Finding {
	if e := t.current(); e != nil {
		return t.byPath[e.Path]
	}
	return nil
}

// key handles a key of the tab and reports whether it used it.
func (t *folderTab) key(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup", "ctrl+b":
		t.move(-t.height)
	case "pgdown", "ctrl+f":
		t.move(t.height)
	case "home", "g":
		t.move(-len(t.rows))
	case "end", "G":
		t.move(len(t.rows))
	case "enter":
		if e := t.current(); e != nil && len(e.Entries) > 0 {
			t.expanded[e.Path] = !t.expanded[e.Path]
			t.rebuild()
		}
	case "right", "l":
		if e := t.current(); e != nil && len(e.Entries) > 0 {
			t.expanded[e.Path] = true
			t.rebuild()
		}
	case "left", "h":
		t.collapse()
	case " ":
		t.toggle()
	default:
		return false
	}
	return true
}

func (t *folderTab) move(delta int) {
	t.cursor = min(max(t.cursor+delta, 0), max(len(t.rows)-1, 0))
	t.scroll()
}

// collapse closes the open folder under the cursor, or moves to the folder
// that holds the entry.
func (t *folderTab) collapse() {
	e := t.current()
	if e == nil {
		return
	}
	if t.expanded[e.Path] {
		t.expanded[e.Path] = false
		t.rebuild()
		return
	}
	depth := t.rows[t.cursor].depth
	for i := t.cursor - 1; i >= 0; i-- {
		if t.rows[i].depth < depth {
			t.cursor = i
			t.scroll()
			return
		}
	}
}

// toggle ticks or unticks the finding under the cursor, in the selection
// both tabs share.
func (t *folderTab) toggle() {
	f := t.currentFinding()
	if f == nil {
		t.s.status = "only an entry that is a finding can be ticked; its tier shows next to its size"
		return
	}
	if why := t.s.tree.SetSelected(f.ID, !t.s.tree.IsSelected(f.ID)); why != "" {
		t.s.status = why
	}
}

func (t *folderTab) scroll() {
	h := max(t.height, 1)
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = max(min(t.offset, len(t.rows)-h), 0)
}

// view renders the rows that fit in width columns and height lines.
func (t *folderTab) view(width, height int) []string {
	t.height = max(height, 1)
	t.scroll()
	out := make([]string, 0, height)
	for i := t.offset; i < len(t.rows) && len(out) < height; i++ {
		prefix := "  "
		if i == t.cursor {
			prefix = t.s.m.st.cursor.Render(">") + " "
		}
		out = append(out, lipgloss.NewStyle().MaxWidth(width).Render(prefix+t.row(t.rows[i])))
	}
	if len(t.rows) == 0 {
		out = append(out, t.s.m.st.dim.Render("  the folder is empty"))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// row is one line: the tick box and tier of a finding, the size, the name
// indented by depth with a marker on folders that open, and then the
// reclaimable bytes inside and the state of a worktree.
func (t *folderTab) row(r folderRow) string {
	st := t.s.m.st
	e := r.entry
	box, tier := "   ", " "
	if f := t.byPath[e.Path]; f != nil {
		switch {
		case !f.Actionable():
			box = " - "
		case t.s.tree.IsSelected(f.ID):
			box = "[x]"
		default:
			box = "[ ]"
		}
		if f.Actionable() {
			tier = st.tier(f.Tier)
		}
	}
	marker := "  "
	switch {
	case len(e.Entries) > 0 && t.expanded[e.Path]:
		marker = "▾ "
	case len(e.Entries) > 0:
		marker = "▸ "
	}
	name := e.Name
	if e.Dir {
		name += "/"
	}
	line := box + " " + tier + " " + padLeft(sizeText(e.Size), 9) + "  " + strings.Repeat("  ", r.depth) + marker + name
	var notes []string
	if n := t.inside[e.Path]; n > 0 && t.byPath[e.Path] == nil {
		notes = append(notes, sizeText(n)+" reclaimable inside")
	}
	if e.Worktree != "" {
		notes = append(notes, e.Worktree)
	}
	if len(notes) > 0 {
		line += "  " + st.dim.Render(strings.Join(notes, ", "))
	}
	return line
}

// details describes the entry under the cursor: the finding's details when
// it is one, else its path, size, what is reclaimable inside and the state
// of a worktree.
func (t *folderTab) details() []detailLine {
	e := t.current()
	if e == nil {
		return nil
	}
	if f := t.byPath[e.Path]; f != nil {
		return t.s.m.details(t.s.report, ui.TreeItem{Kind: ui.TreeFinding, Finding: f, Selected: t.s.tree.IsSelected(f.ID),
			Category: f.Category})
	}
	out := []detailLine{{t.s.m.path(e.Path), boldLine}, {sizeText(e.Size) + " in all", plainLine}}
	if n := t.inside[e.Path]; n > 0 {
		out = append(out, detailLine{sizeText(n) + " of it reclaimable, in the findings below it", plainLine})
	} else {
		out = append(out, detailLine{"Nothing reclaimable was found below it.", dimLine})
	}
	if e.Worktree != "" {
		out = append(out, detailLine{"A linked worktree: " + e.Worktree + ". The findings tab has its verdict and proof.", plainLine})
	}
	if len(e.Entries) > 0 {
		out = append(out, detailLine{"enter opens it; left closes it.", dimLine})
	}
	out = append(out, detailLine{"x adds this path to the ignore list, so nothing in it is offered again.", dimLine})
	return out
}
