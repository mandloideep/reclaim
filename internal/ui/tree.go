package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/units"
)

// sortMode is the order of groups and findings in the tree.
type sortMode int

const (
	sortSize sortMode = iota
	sortName
	sortAge
	sortModes
)

func (s sortMode) String() string {
	switch s {
	case sortName:
		return "name"
	case sortAge:
		return "oldest first"
	default:
		return "size"
	}
}

// TreeOptions configure a findings tree.
type TreeOptions struct {
	// Home shortens paths to start with "~".
	Home string
	// Renderer styles the output. Nil uses lipgloss's default renderer.
	Renderer *lipgloss.Renderer
	// Now is the reference time for ages.
	Now time.Time
	// Warnings adds the report's warnings and notes as the last node of the
	// tree, closed at first and never selectable.
	Warnings bool
}

// nodeKind says what a node of the tree is.
type nodeKind int

const (
	kindCategory nodeKind = iota
	kindGroup
	kindFinding
	kindWarnings
	kindWarning
)

// node is one entry of the tree: a category, a group, a finding, the
// warnings and notes node or one warning or note.
type node struct {
	kind     nodeKind
	key      string
	title    string
	children []*node
	// leaf is the index of the finding for findings, -1 for other nodes.
	leaf int
	// group is the group a finding or a group node shows.
	group *Group
	// category is the category the node lies in.
	category finding.Category
	// text is the full text of a warning or note.
	text string
	// size and newest summarize every finding below the node, for sorting.
	size   int64
	newest time.Time
}

// stats summarize the visible findings below a node.
type stats struct {
	visible int
	// size is the reclaimable size; attention is the size of findings listed
	// for attention only, which is never reclaimable.
	size       int64
	attention  int64
	selectable int
	selected   int
}

func (s *stats) add(o stats) {
	s.visible += o.visible
	s.size += o.size
	s.attention += o.attention
	s.selectable += o.selectable
	s.selected += o.selected
}

// row is one visible line of the tree.
type row struct {
	node  *node
	depth int
	stats stats
}

// TreeItemKind says what the row under the cursor is.
type TreeItemKind int

// Kinds of tree items.
const (
	// TreeNone means there is no row under the cursor.
	TreeNone TreeItemKind = iota
	// TreeCategory is a category such as "Project artifacts".
	TreeCategory
	// TreeGroup is a group: a project, a repository, a Docker kind or a
	// scanner.
	TreeGroup
	// TreeFinding is one finding.
	TreeFinding
	// TreeWarnings is the node that holds the warnings and notes.
	TreeWarnings
	// TreeWarning is one warning or note.
	TreeWarning
)

// TreeItem describes the row under the cursor.
type TreeItem struct {
	// Kind says what the row is.
	Kind TreeItemKind
	// Category is the category the row lies in.
	Category finding.Category
	// Group is the group of a group row or of a finding, nil for a
	// category.
	Group *Group
	// Finding is the finding of a finding row.
	Finding *finding.Finding
	// Selected reports whether the finding is selected.
	Selected bool
	// Text is the full text of a warning or a note.
	Text string
	// Count is the number of visible findings below a category or group,
	// SelectedCount how many of them are selected, Size their reclaimable
	// size and Attention the size of those listed for attention only.
	Count, SelectedCount int
	Size, Attention      int64
}

// Tree is the findings tree that the checklist of select and the review
// screen of the interactive mode embed: categories, then groups, then
// findings, with the selection, the expansion state, the filters, the sort
// and the cursor.
//
// Tier A findings start selected. Toggling a group or category selects or
// clears its visible tier A and B findings and never touches tier C, which
// must be ticked one by one. Findings listed for attention only cannot be
// selected. Only the rows that fit the window are rendered, so thousands of
// findings stay responsive.
type Tree struct {
	opts     TreeOptions
	findings []finding.Finding
	selected []bool
	// dropped marks the findings Drop took out of the tree.
	dropped  []bool
	tree     []*node
	expanded map[string]bool

	rows      []row
	matched   int
	cursor    int
	cursorKey string
	offset    int
	width     int
	height    int

	filter    string
	filtering bool
	input     textinput.Model
	tier      finding.Tier
	sort      sortMode
	status    string
	// changed is set when the selection changed since selCount and selSize
	// were computed; selCount is -1 before the first time.
	changed  bool
	selCount int
	selSize  int64

	styles treeStyles
}

type treeStyles struct {
	bold, dim, warn, cursor lipgloss.Style
	tierA, tierB, tierC     lipgloss.Style
}

// NewTree returns a tree over the report's findings with every tier A
// finding selected.
func NewTree(r *finding.Report, opts TreeOptions) *Tree {
	re := opts.Renderer
	if re == nil {
		re = lipgloss.DefaultRenderer()
	}
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "filter by name, path, scanner or project"
	StyleInput(&in, re)
	t := &Tree{
		opts:     opts,
		findings: Ordered(Sections(r.Findings, shortener(opts.Home))),
		expanded: map[string]bool{},
		selCount: -1,
		width:    100,
		height:   20,
		input:    in,
		styles: treeStyles{
			bold:   re.NewStyle().Bold(true),
			dim:    re.NewStyle().Faint(true),
			warn:   re.NewStyle().Foreground(lipgloss.Color("3")),
			cursor: re.NewStyle().Reverse(true),
			tierA:  re.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
			tierB:  re.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
			tierC:  re.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		},
	}
	t.selected = make([]bool, len(t.findings))
	t.dropped = make([]bool, len(t.findings))
	for i := range t.findings {
		t.selected[i] = t.findings[i].Tier == finding.TierA && t.findings[i].Actionable()
	}
	t.buildTree()
	if opts.Warnings {
		t.addWarnings(r.Warnings, r.Notes)
	}
	t.rebuild()
	return t
}

func shortener(home string) func(string) string {
	p := &Printer{home: home}
	return p.Path
}

// buildTree makes the category, group and finding nodes. Findings are
// addressed by their index in t.findings.
func (t *Tree) buildTree() {
	short := shortener(t.opts.Home)
	idx := 0
	for _, s := range Sections(t.findings, short) {
		cat := &node{kind: kindCategory, key: "c\x00" + string(s.Category), title: s.Category.Title(), leaf: -1, category: s.Category}
		t.expanded[cat.key] = true
		for gi := range s.Groups {
			g := &s.Groups[gi]
			parent := cat
			if !s.Flat() {
				parent = &node{kind: kindGroup, key: "g\x00" + string(s.Category) + "\x00" + g.Key, title: g.Title, leaf: -1,
					group: g, category: s.Category}
				cat.children = append(cat.children, parent)
			}
			for range g.Findings {
				f := &t.findings[idx]
				leaf := &node{kind: kindFinding, key: "f\x00" + f.ID, title: f.DisplayName(), leaf: idx, group: g,
					category: s.Category, size: f.Size, newest: f.LastUsed}
				parent.children = append(parent.children, leaf)
				idx++
			}
			if parent != cat {
				summarize(parent)
			}
		}
		summarize(cat)
		t.tree = append(t.tree, cat)
	}
}

// addWarnings adds the warnings and notes node, closed, as the last node.
func (t *Tree) addWarnings(ws []finding.Warning, notes []string) {
	if len(ws)+len(notes) == 0 {
		return
	}
	short := shortener(t.opts.Home)
	n := &node{kind: kindWarnings, key: "w", title: fmt.Sprintf("Warnings and notes (%d)", len(ws)+len(notes)), leaf: -1}
	for i, w := range ws {
		w.Path = short(w.Path)
		n.children = append(n.children, &node{kind: kindWarning, key: fmt.Sprintf("w\x00%d", i), title: w.String(), text: w.String(), leaf: -1})
	}
	for i, note := range notes {
		n.children = append(n.children, &node{kind: kindWarning, key: fmt.Sprintf("n\x00%d", i), title: "Note: " + note, text: "Note: " + note, leaf: -1})
	}
	t.tree = append(t.tree, n)
}

func summarize(n *node) {
	for _, c := range n.children {
		n.size += c.size
		if c.newest.After(n.newest) {
			n.newest = c.newest
		}
	}
}

// SetSize sets the width of the rows and the number of rows the tree shows.
func (t *Tree) SetSize(width, height int) {
	t.width, t.height = max(width, 20), max(height, 1)
	t.input.Width = max(width-4, 10)
	t.scroll()
}

// Selected returns the selected findings in tree order.
func (t *Tree) Selected() []finding.Finding {
	var out []finding.Finding
	for i := range t.findings {
		if t.selected[i] {
			out = append(out, t.findings[i])
		}
	}
	return out
}

// SelectedSize returns the number of selected findings and their total
// size, counting a finding that lies inside another selected finding once,
// as report totals do.
// It is computed again only after the selection changed, because every
// frame shows it.
func (t *Tree) SelectedSize() (int, int64) {
	if t.changed || t.selCount < 0 {
		sel := t.Selected()
		t.selCount, t.selSize = len(sel), finding.ReclaimableSize(sel)
		t.changed = false
	}
	return t.selCount, t.selSize
}

// SelectNone clears the selection, the counterpart of selecting every tier
// A finding, and returns how many findings it cleared.
func (t *Tree) SelectNone() int {
	n := 0
	for i := range t.selected {
		if t.selected[i] {
			t.selected[i] = false
			t.changed = true
			n++
		}
	}
	t.rebuild()
	return n
}

// Drop takes the findings drop reports out of the tree, such as the ones
// the user just put on the ignore list, and clears their selection. It
// returns how many it took out.
func (t *Tree) Drop(drop func(*finding.Finding) bool) int {
	key, at := t.cursorKey, t.cursor
	n := 0
	for i := range t.findings {
		if !t.dropped[i] && drop(&t.findings[i]) {
			t.dropped[i], t.selected[i] = true, false
			t.changed = true
			n++
		}
	}
	t.rebuild()
	// When the row under the cursor went, the cursor stays where it was
	// rather than jumping to the top.
	if len(t.rows) > 0 && t.rows[t.cursor].node.key != key {
		t.cursor = min(at, len(t.rows)-1)
		t.cursorKey = t.rows[t.cursor].node.key
		t.scroll()
	}
	return n
}

// CurrentFindings returns the findings the row under the cursor stands
// for: its finding, or the findings below its group or category that the
// filters show. A warning stands for none.
func (t *Tree) CurrentFindings() []finding.Finding {
	n := t.current()
	if n == nil {
		return nil
	}
	var out []finding.Finding
	for _, i := range t.visibleLeaves([]*node{n}) {
		out = append(out, t.findings[i])
	}
	return out
}

// Findings returns the findings the tree holds, in tree order, without the
// ones Drop took out.
func (t *Tree) Findings() []finding.Finding {
	var out []finding.Finding
	for i := range t.findings {
		if !t.dropped[i] {
			out = append(out, t.findings[i])
		}
	}
	return out
}

// IsSelected reports whether the finding with this id is selected.
func (t *Tree) IsSelected(id string) bool {
	i := slices.IndexFunc(t.findings, func(f finding.Finding) bool { return f.ID == id })
	return i >= 0 && t.selected[i]
}

// SetSelected ticks or unticks the finding with this id, for a view that
// shares the selection, such as the folder tab of the review screen. It
// returns why it could not, for a finding listed for attention only or one
// the tree does not hold.
func (t *Tree) SetSelected(id string, on bool) string {
	i := slices.IndexFunc(t.findings, func(f finding.Finding) bool { return f.ID == id })
	switch {
	case i < 0 || t.dropped[i]:
		return "this is not among the findings"
	case !t.findings[i].Actionable():
		return "this is listed for attention only; reclaim never removes it"
	}
	if t.selected[i] != on {
		t.selected[i] = on
		t.changed = true
		t.rebuild()
	}
	return ""
}

// Filtering reports whether the text filter is being typed, when every key
// belongs to it.
func (t *Tree) Filtering() bool { return t.filtering }

// Status is the message of the last key, such as why a row cannot be ticked.
func (t *Tree) Status() string { return t.status }

// SetStatus replaces the status message.
func (t *Tree) SetStatus(s string) { t.status = s }

// Current describes the row under the cursor.
func (t *Tree) Current() TreeItem {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return TreeItem{}
	}
	r := t.rows[t.cursor]
	n := r.node
	it := TreeItem{Category: n.category, Group: n.group, Text: n.text,
		Count: r.stats.visible, SelectedCount: r.stats.selected, Size: r.stats.size, Attention: r.stats.attention}
	switch n.kind {
	case kindCategory:
		it.Kind = TreeCategory
	case kindGroup:
		it.Kind = TreeGroup
	case kindFinding:
		it.Kind = TreeFinding
		it.Finding = &t.findings[n.leaf]
		it.Selected = t.selected[n.leaf]
	case kindWarnings:
		it.Kind = TreeWarnings
	case kindWarning:
		it.Kind = TreeWarning
	}
	return it
}

// HandleKey handles the keys of the tree: moving, ticking, opening, the
// text and tier filters, the sort and selecting every tier A finding. While
// the text filter is typed every key but ctrl+c belongs to it. It reports
// whether it used the key, so the screen around it can handle the rest.
func (t *Tree) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return false, nil
	}
	if t.filtering {
		return true, t.updateFilter(msg)
	}
	t.status = ""
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
	case " ":
		t.toggle()
	case "enter":
		t.setExpanded(!t.isExpanded(t.current()))
	case "right", "l":
		t.setExpanded(true)
	case "left", "h":
		t.setExpanded(false)
	case "/":
		t.filtering = true
		t.input.SetValue(t.filter)
		t.input.CursorEnd()
		return true, t.input.Focus()
	case "esc":
		if t.filter == "" {
			return false, nil
		}
		t.filter = ""
		t.input.SetValue("")
		t.rebuild()
	case "s":
		t.sort = (t.sort + 1) % sortModes
		t.rebuild()
	case "t":
		t.tier = nextTier(t.tier)
		t.rebuild()
	case "a":
		n := 0
		for _, i := range t.visibleLeaves(t.tree) {
			if t.findings[i].Tier == finding.TierA && t.findings[i].Actionable() && !t.selected[i] {
				t.selected[i] = true
				t.changed = true
				n++
			}
		}
		t.status = fmt.Sprintf("selected %s", plural(n, "more tier A item", "more tier A items"))
		t.rebuild()
	default:
		return false, nil
	}
	return true, nil
}

func (t *Tree) updateFilter(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEnter:
		t.filtering = false
		t.input.Blur()
		return nil
	case tea.KeyEsc:
		t.filtering = false
		t.input.Blur()
		t.input.SetValue("")
		t.filter = ""
		t.rebuild()
		return nil
	}
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	if v := strings.TrimSpace(t.input.Value()); v != t.filter {
		t.filter = v
		t.rebuild()
	}
	return cmd
}

func nextTier(t finding.Tier) finding.Tier {
	switch t {
	case "":
		return finding.TierA
	case finding.TierA:
		return finding.TierB
	case finding.TierB:
		return finding.TierC
	default:
		return ""
	}
}

func (t *Tree) current() *node {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return nil
	}
	return t.rows[t.cursor].node
}

func (t *Tree) move(delta int) {
	if len(t.rows) == 0 {
		return
	}
	t.cursor = min(max(t.cursor+delta, 0), len(t.rows)-1)
	t.cursorKey = t.rows[t.cursor].node.key
	t.scroll()
}

func (t *Tree) setExpanded(open bool) {
	n := t.current()
	if n == nil || n.kind == kindFinding || n.kind == kindWarning {
		return
	}
	if t.filtered() && n.kind != kindWarnings {
		t.status = "groups stay open while a filter is active"
		return
	}
	t.expanded[n.key] = open
	t.rebuild()
}

// toggle flips the selection of the finding under the cursor, or of the
// tier A and B findings below the group under the cursor.
func (t *Tree) toggle() {
	n := t.current()
	if n == nil {
		return
	}
	switch n.kind {
	case kindWarnings, kindWarning:
		t.status = "warnings and notes cannot be selected"
		return
	case kindFinding:
		f := &t.findings[n.leaf]
		if !f.Actionable() {
			t.status = "this is listed for attention only; reclaim never removes it"
			return
		}
		t.selected[n.leaf] = !t.selected[n.leaf]
		t.changed = true
		t.rebuild()
		return
	}
	var targets []int
	skippedC := 0
	for _, i := range t.visibleLeaves(n.children) {
		switch {
		case !t.findings[i].Actionable():
		case t.findings[i].Tier == finding.TierC:
			skippedC++
		default:
			targets = append(targets, i)
		}
	}
	all := len(targets) > 0 && !slices.ContainsFunc(targets, func(i int) bool { return !t.selected[i] })
	for _, i := range targets {
		t.selected[i] = !all
		t.changed = true
	}
	if skippedC > 0 {
		t.status = fmt.Sprintf("%s left as they were; tick tier C items one by one", plural(skippedC, "tier C item", "tier C items"))
	}
	t.rebuild()
}

// visibleLeaves returns the findings below the nodes that match the filters,
// whether or not their groups are expanded.
func (t *Tree) visibleLeaves(nodes []*node) []int {
	var out []int
	for _, n := range nodes {
		switch n.kind {
		case kindFinding:
			if t.matches(n) {
				out = append(out, n.leaf)
			}
		case kindCategory, kindGroup:
			out = append(out, t.visibleLeaves(n.children)...)
		}
	}
	return out
}

func (t *Tree) filtered() bool { return t.filter != "" || t.tier != "" }

func (t *Tree) isExpanded(n *node) bool {
	switch {
	case n == nil || n.kind == kindFinding || n.kind == kindWarning:
		return false
	case n.kind == kindWarnings:
		return t.expanded[n.key]
	default:
		return t.filtered() || t.expanded[n.key]
	}
}

// matches reports whether a finding passes the text and tier filters. The
// text matches the name, path, scanner, project and group title.
func (t *Tree) matches(n *node) bool {
	if t.dropped[n.leaf] {
		return false
	}
	f := &t.findings[n.leaf]
	if t.tier != "" && f.Tier != t.tier {
		return false
	}
	if t.filter == "" {
		return true
	}
	needle := strings.ToLower(t.filter)
	for _, s := range []string{f.DisplayName(), f.Path, f.Scanner, f.Project, n.group.Title} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	return false
}

// rebuild recomputes the visible rows after any change, keeping the cursor
// on the same node when it is still visible.
func (t *Tree) rebuild() {
	t.rows = t.rows[:0]
	t.matched = 0
	for _, n := range t.tree {
		if n.kind == kindWarnings {
			t.rows = append(t.rows, row{node: n})
			if t.isExpanded(n) {
				for _, c := range n.children {
					t.rows = append(t.rows, row{node: c, depth: 1})
				}
			}
			continue
		}
		t.matched += t.walk(n, 0, true).visible
	}
	t.cursor = 0
	if i := slices.IndexFunc(t.rows, func(r row) bool { return r.node.key == t.cursorKey }); i >= 0 {
		t.cursor = i
	}
	if len(t.rows) > 0 {
		t.cursorKey = t.rows[t.cursor].node.key
	}
	t.scroll()
}

func (t *Tree) walk(n *node, depth int, emit bool) stats {
	if n.kind == kindFinding {
		if !t.matches(n) {
			return stats{}
		}
		f := &t.findings[n.leaf]
		st := stats{visible: 1}
		if f.Actionable() {
			st.size = f.Size
			st.selectable = 1
			if t.selected[n.leaf] {
				st.selected = 1
			}
		} else {
			st.attention = f.Size
		}
		if emit {
			t.rows = append(t.rows, row{node: n, depth: depth, stats: st})
		}
		return st
	}
	pos := len(t.rows)
	if emit {
		t.rows = append(t.rows, row{node: n, depth: depth})
	}
	var total stats
	childEmit := emit && t.isExpanded(n)
	for _, c := range t.sorted(n.children) {
		total.add(t.walk(c, depth+1, childEmit))
	}
	if emit {
		if total.visible == 0 {
			t.rows = t.rows[:pos]
		} else {
			t.rows[pos].stats = total
		}
	}
	return total
}

func (t *Tree) sorted(nodes []*node) []*node {
	if t.sort == sortSize {
		return nodes
	}
	out := slices.Clone(nodes)
	slices.SortStableFunc(out, func(a, b *node) int {
		if t.sort == sortName {
			return strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
		}
		// Oldest first, unknown ages last.
		if a.newest.IsZero() != b.newest.IsZero() {
			return cmp.Compare(boolInt(a.newest.IsZero()), boolInt(b.newest.IsZero()))
		}
		return a.newest.Compare(b.newest)
	})
	return out
}

func (t *Tree) scroll() {
	h := t.height
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = max(min(t.offset, len(t.rows)-h), 0)
}

// FilterLine shows the text filter, the sort and the tier filter.
func (t *Tree) FilterLine() string {
	tier := "all"
	if t.tier != "" {
		tier = string(t.tier)
	}
	state := fmt.Sprintf("sort: %s  tier: %s", t.sort, tier)
	if t.filtering {
		return t.input.View() + "  " + t.styles.dim.Render(state)
	}
	if t.filter != "" {
		return "filter: " + t.filter + "  " + t.styles.dim.Render(state+"  esc clears")
	}
	return t.styles.dim.Render(state)
}

// View renders the rows in the window, exactly as many lines as the height
// SetSize gave, each cut to the width.
func (t *Tree) View() string {
	lines := make([]string, 0, t.height)
	cut := lipgloss.NewStyle().MaxWidth(t.width)
	end := min(t.offset+t.height, len(t.rows))
	for i := t.offset; i < end; i++ {
		lines = append(lines, cut.Render(t.renderRow(i)))
	}
	if t.matched == 0 && len(lines) < t.height {
		empty := "  nothing matches the filters; esc clears the text filter, t cycles the tier filter"
		if len(t.findings) == 0 {
			empty = "  nothing reclaimable was found"
		}
		lines = append(lines, cut.Render(t.styles.dim.Render(empty)))
	}
	for len(lines) < t.height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (t *Tree) renderRow(i int) string {
	r := t.rows[i]
	n := r.node
	indent := strings.Repeat("  ", r.depth)
	var s string
	switch n.kind {
	case kindFinding:
		s = indent + t.leafText(n)
	case kindWarnings:
		arrow := "▸"
		if t.isExpanded(n) {
			arrow = "▾"
		}
		s = indent + "    " + arrow + " " + t.styles.warn.Render(n.title)
	case kindWarning:
		text := t.styles.warn.Render("! " + n.title)
		if strings.HasPrefix(n.title, "Note: ") {
			text = t.styles.dim.Render(n.title)
		}
		s = indent + "    " + text
	default:
		s = indent + t.nodeText(r)
	}
	if i == t.cursor {
		return t.styles.cursor.Render(">") + " " + s
	}
	return "  " + s
}

// nodeText renders a category or group row: the box, the arrow, the title,
// the size, the count and the selected count. A repository of worktrees
// counts its worktrees by verdict.
func (t *Tree) nodeText(r row) string {
	n := r.node
	arrow := "▸"
	if t.isExpanded(n) {
		arrow = "▾"
	}
	title := n.title
	if r.depth == 0 {
		title = t.styles.bold.Render(title)
	}
	size, extra := r.stats.size, ""
	if r.stats.selectable == 0 {
		size, extra = r.stats.attention, ", not counted, never removed"
	}
	head := title + "  " + units.FormatSize(size) + "  " + t.styles.dim.Render(plural(r.stats.visible, "item", "items")+extra)
	if n.group != nil && n.group.Repo != nil {
		// A repository counts its worktrees by verdict instead, which says
		// more than a size in a row that is already long; the details
		// pane has the size.
		if words := VerdictCounts(n.group.Repo); words != "" {
			head = title + "  " + t.styles.dim.Render(words)
		}
	}
	s := t.box(r.stats) + " " + arrow + " " + head
	if r.stats.selected > 0 {
		s += t.styles.dim.Render(fmt.Sprintf(", %d selected", r.stats.selected))
	}
	return s
}

func (t *Tree) box(st stats) string {
	switch {
	case st.selectable > 0 && st.selected == st.selectable:
		return "[x]"
	case st.selected > 0:
		return "[-]"
	case st.selectable == 0:
		return " - "
	default:
		return "[ ]"
	}
}

func (t *Tree) leafText(n *node) string {
	f := &t.findings[n.leaf]
	box := "[ ]"
	switch {
	case !f.Actionable():
		box = " - "
	case t.selected[n.leaf]:
		box = "[x]"
	}
	tier := " "
	if f.Actionable() {
		tier = t.tierText(f.Tier)
	}
	sizeCell := size(f.Size)
	if f.Size == 0 && !f.Actionable() {
		// Such as another clone of a repository, which reclaim does not
		// measure because it never offers it.
		sizeCell = fmt.Sprintf("%9s", "-")
	}
	s := box + " " + tier + " " + sizeCell + "  "
	if f.Category == finding.CategoryWorktree {
		return s + t.worktreeLabel(n, f)
	}
	label := f.DisplayName()
	if n.group != nil && n.group.Project != "" {
		label = (&Printer{home: t.opts.Home}).label(n.group, f)
	} else {
		label = shortener(t.opts.Home)(label)
	}
	s += label + t.styles.dim.Render("  "+f.Scanner)
	if !f.LastUsed.IsZero() && !t.opts.Now.IsZero() {
		s += t.styles.dim.Render("  used " + (&Printer{now: t.opts.Now}).age(f.LastUsed))
	}
	if !f.Actionable() {
		s += "  " + t.styles.warn.Render("cannot be selected")
	}
	if (f.Tier == finding.TierC || !f.Actionable()) && f.Warning != "" {
		s += "  " + t.styles.warn.Render("! "+f.Warning)
	}
	return s
}

// worktreeLabel names a worktree finding by its tool, its branch and its
// verdict in words, and other findings of a repository, such as stale
// registrations and other clones, by their name.
func (t *Tree) worktreeLabel(n *node, f *finding.Finding) string {
	words := WorktreeWords(f)
	style := t.styles.dim
	if !f.Actionable() {
		style = t.styles.warn
	}
	var label string
	if s := f.Worktree; s != nil {
		ref := s.Branch
		if ref == "" {
			ref = "detached " + shortCommit(s.Head)
		}
		label = s.Tool + "  " + ref
	} else {
		label = f.DisplayName()
		if f.Path != "" && f.Kind != finding.KindEmptyDir && f.Action == finding.ActionNone {
			label = (&Printer{home: t.opts.Home}).relPath(n.group, f.Path) + "  " + label
		}
	}
	if words != "" {
		label += "  " + style.Render(words)
	}
	return label
}

func (t *Tree) tierText(tier finding.Tier) string {
	switch tier {
	case finding.TierA:
		return t.styles.tierA.Render("A")
	case finding.TierB:
		return t.styles.tierB.Render("B")
	default:
		return t.styles.tierC.Render(string(tier))
	}
}

// StyleInput gives a text input styles from the renderer of the screen
// it is drawn on, instead of the default renderer, so its cursor and
// placeholder follow the same color rules as everything around them.
func StyleInput(in *textinput.Model, re *lipgloss.Renderer) {
	in.PromptStyle = re.NewStyle()
	in.TextStyle = re.NewStyle()
	in.PlaceholderStyle = re.NewStyle().Faint(true)
	in.CompletionStyle = re.NewStyle().Faint(true)
	in.Cursor.Style = re.NewStyle()
	in.Cursor.TextStyle = re.NewStyle()
}
