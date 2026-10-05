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

// ChecklistOutcome says how the checklist ended.
type ChecklistOutcome int

const (
	// ChecklistQuit means the user quit without writing a plan.
	ChecklistQuit ChecklistOutcome = iota
	// ChecklistWrite means the user asked to write the plan.
	ChecklistWrite
)

// sortMode is the order of groups and findings in the checklist.
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

// ChecklistOptions configure a checklist.
type ChecklistOptions struct {
	// Home shortens paths to start with "~".
	Home string
	// PlanPath is where w writes the plan, shown in the footer.
	PlanPath string
	// Provenance is the line that says where the report came from.
	Provenance string
	// Renderer styles the output. Nil uses lipgloss's default renderer.
	Renderer *lipgloss.Renderer
	// Now is the reference time for ages.
	Now time.Time
}

// node is one entry of the checklist tree: a category, a group or a finding.
type node struct {
	key      string
	title    string
	children []*node
	// leaf is the index of the finding for leaves, -1 for other nodes.
	leaf int
	// project is the project root a leaf's path is shown relative to.
	group *Group
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

// Checklist is the interactive selection screen of select. It is a
// bubbletea model: a tree of categories, groups and findings in which the
// user ticks what goes into the plan.
//
// Tier A findings start selected. Toggling a group or category selects or
// clears its visible tier A and B findings and never touches tier C, which
// must be ticked one by one. Findings listed for attention only cannot be
// selected. Only the rows that fit the window are rendered, so thousands of
// findings stay responsive.
type Checklist struct {
	opts     ChecklistOptions
	findings []finding.Finding
	selected []bool
	tree     []*node
	expanded map[string]bool

	rows      []row
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
	outcome   ChecklistOutcome

	styles checklistStyles
}

type checklistStyles struct {
	bold, dim, warn, cursor, header lipgloss.Style
	tierA, tierB, tierC             lipgloss.Style
}

var _ tea.Model = (*Checklist)(nil)

// NewChecklist returns a checklist over the report's findings with every
// tier A finding selected.
func NewChecklist(r *finding.Report, opts ChecklistOptions) *Checklist {
	re := opts.Renderer
	if re == nil {
		re = lipgloss.DefaultRenderer()
	}
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "filter by name, path, scanner or project"
	m := &Checklist{
		opts:     opts,
		findings: Ordered(Sections(r.Findings, shortener(opts.Home))),
		expanded: map[string]bool{},
		width:    100,
		height:   24,
		input:    in,
		styles: checklistStyles{
			bold:   re.NewStyle().Bold(true),
			dim:    re.NewStyle().Faint(true),
			warn:   re.NewStyle().Foreground(lipgloss.Color("3")),
			cursor: re.NewStyle().Reverse(true),
			header: re.NewStyle().Bold(true).Underline(true),
			tierA:  re.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
			tierB:  re.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
			tierC:  re.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		},
	}
	m.selected = make([]bool, len(m.findings))
	for i := range m.findings {
		m.selected[i] = m.findings[i].Tier == finding.TierA && m.findings[i].Actionable()
	}
	m.buildTree()
	m.rebuild()
	return m
}

func shortener(home string) func(string) string {
	p := &Printer{home: home}
	return p.Path
}

// buildTree makes the category, group and finding nodes. Findings are
// addressed by their index in m.findings.
func (m *Checklist) buildTree() {
	short := shortener(m.opts.Home)
	idx := 0
	for _, s := range Sections(m.findings, short) {
		cat := &node{key: "c\x00" + string(s.Category), title: s.Category.Title(), leaf: -1}
		m.expanded[cat.key] = true
		for gi := range s.Groups {
			g := &s.Groups[gi]
			parent := cat
			if !s.Flat() {
				parent = &node{key: "g\x00" + string(s.Category) + "\x00" + g.Key, title: g.Title, leaf: -1}
				cat.children = append(cat.children, parent)
			}
			for range g.Findings {
				f := &m.findings[idx]
				leaf := &node{key: "f\x00" + f.ID, title: f.DisplayName(), leaf: idx, group: g, size: f.Size, newest: f.LastUsed}
				parent.children = append(parent.children, leaf)
				idx++
			}
			if parent != cat {
				summarize(parent)
			}
		}
		summarize(cat)
		m.tree = append(m.tree, cat)
	}
}

func summarize(n *node) {
	for _, c := range n.children {
		n.size += c.size
		if c.newest.After(n.newest) {
			n.newest = c.newest
		}
	}
}

// Init implements tea.Model.
func (m *Checklist) Init() tea.Cmd { return nil }

// Outcome reports how the checklist ended.
func (m *Checklist) Outcome() ChecklistOutcome { return m.outcome }

// Selected returns the selected findings in report order.
func (m *Checklist) Selected() []finding.Finding {
	var out []finding.Finding
	for i := range m.findings {
		if m.selected[i] {
			out = append(out, m.findings[i])
		}
	}
	return out
}

// SelectedSize returns the number and total size of the selected findings.
func (m *Checklist) SelectedSize() (int, int64) {
	var n int
	var size int64
	for i := range m.findings {
		if m.selected[i] {
			n++
			size += m.findings[i].Size
		}
	}
	return n, size
}

// Update implements tea.Model.
func (m *Checklist) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(msg.Width-4, 10)
		m.scroll()
		return m, nil
	case tea.KeyMsg:
		if m.filtering {
			return m.updateFilter(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *Checklist) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
		m.input.Blur()
		return m, nil
	case tea.KeyEsc:
		m.filtering = false
		m.input.Blur()
		m.input.SetValue("")
		m.filter = ""
		m.rebuild()
		return m, nil
	case tea.KeyCtrlC:
		m.outcome = ChecklistQuit
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if v := strings.TrimSpace(m.input.Value()); v != m.filter {
		m.filter = v
		m.rebuild()
	}
	return m, cmd
}

func (m *Checklist) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "q", "ctrl+c":
		m.outcome = ChecklistQuit
		return m, tea.Quit
	case "w":
		m.outcome = ChecklistWrite
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+b":
		m.move(-m.listHeight())
	case "pgdown", "ctrl+f":
		m.move(m.listHeight())
	case "home", "g":
		m.move(-len(m.rows))
	case "end", "G":
		m.move(len(m.rows))
	case " ":
		m.toggle()
	case "enter":
		m.setExpanded(!m.isExpanded(m.current()))
	case "right", "l":
		m.setExpanded(true)
	case "left", "h":
		m.setExpanded(false)
	case "/":
		m.filtering = true
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.input.SetValue("")
			m.rebuild()
		}
	case "s":
		m.sort = (m.sort + 1) % sortModes
		m.rebuild()
	case "t":
		m.tier = nextTier(m.tier)
		m.rebuild()
	case "a":
		n := 0
		for _, i := range m.visibleLeaves(m.tree) {
			if m.findings[i].Tier == finding.TierA && m.findings[i].Actionable() && !m.selected[i] {
				m.selected[i] = true
				n++
			}
		}
		m.status = fmt.Sprintf("selected %s", plural(n, "more tier A item", "more tier A items"))
		m.rebuild()
	}
	return m, nil
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

func (m *Checklist) current() *node {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].node
}

func (m *Checklist) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = min(max(m.cursor+delta, 0), len(m.rows)-1)
	m.cursorKey = m.rows[m.cursor].node.key
	m.scroll()
}

func (m *Checklist) setExpanded(open bool) {
	n := m.current()
	if n == nil || n.leaf >= 0 {
		return
	}
	if m.filtered() {
		m.status = "groups stay open while a filter is active"
		return
	}
	m.expanded[n.key] = open
	m.rebuild()
}

// toggle flips the selection of the finding under the cursor, or of the
// tier A and B findings below the group under the cursor.
func (m *Checklist) toggle() {
	n := m.current()
	if n == nil {
		return
	}
	if n.leaf >= 0 {
		f := &m.findings[n.leaf]
		if !f.Actionable() {
			m.status = "this file is listed for attention only; reclaim never removes it"
			return
		}
		m.selected[n.leaf] = !m.selected[n.leaf]
		m.rebuild()
		return
	}
	var targets []int
	skippedC := 0
	for _, i := range m.visibleLeaves(n.children) {
		switch {
		case !m.findings[i].Actionable():
		case m.findings[i].Tier == finding.TierC:
			skippedC++
		default:
			targets = append(targets, i)
		}
	}
	all := len(targets) > 0 && !slices.ContainsFunc(targets, func(i int) bool { return !m.selected[i] })
	for _, i := range targets {
		m.selected[i] = !all
	}
	if skippedC > 0 {
		m.status = fmt.Sprintf("%s left as they were; tick tier C items one by one", plural(skippedC, "tier C item", "tier C items"))
	}
	m.rebuild()
}

// visibleLeaves returns the findings below the nodes that match the filters,
// whether or not their groups are expanded.
func (m *Checklist) visibleLeaves(nodes []*node) []int {
	var out []int
	for _, n := range nodes {
		if n.leaf >= 0 {
			if m.matches(n) {
				out = append(out, n.leaf)
			}
			continue
		}
		out = append(out, m.visibleLeaves(n.children)...)
	}
	return out
}

func (m *Checklist) filtered() bool { return m.filter != "" || m.tier != "" }

func (m *Checklist) isExpanded(n *node) bool {
	if n == nil || n.leaf >= 0 {
		return false
	}
	return m.filtered() || m.expanded[n.key]
}

// matches reports whether a finding passes the text and tier filters. The
// text matches the name, path, scanner, project and group title.
func (m *Checklist) matches(n *node) bool {
	f := &m.findings[n.leaf]
	if m.tier != "" && f.Tier != m.tier {
		return false
	}
	if m.filter == "" {
		return true
	}
	needle := strings.ToLower(m.filter)
	for _, s := range []string{f.DisplayName(), f.Path, f.Scanner, f.Project, n.group.Title} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	return false
}

// rebuild recomputes the visible rows after any change, keeping the cursor
// on the same node when it is still visible.
func (m *Checklist) rebuild() {
	m.rows = m.rows[:0]
	for _, n := range m.tree {
		m.walk(n, 0, true)
	}
	m.cursor = 0
	if i := slices.IndexFunc(m.rows, func(r row) bool { return r.node.key == m.cursorKey }); i >= 0 {
		m.cursor = i
	}
	if len(m.rows) > 0 {
		m.cursorKey = m.rows[m.cursor].node.key
	}
	m.scroll()
}

func (m *Checklist) walk(n *node, depth int, emit bool) stats {
	if n.leaf >= 0 {
		if !m.matches(n) {
			return stats{}
		}
		f := &m.findings[n.leaf]
		st := stats{visible: 1}
		if f.Actionable() {
			st.size = f.Size
			st.selectable = 1
			if m.selected[n.leaf] {
				st.selected = 1
			}
		} else {
			st.attention = f.Size
		}
		if emit {
			m.rows = append(m.rows, row{node: n, depth: depth, stats: st})
		}
		return st
	}
	pos := len(m.rows)
	if emit {
		m.rows = append(m.rows, row{node: n, depth: depth})
	}
	var total stats
	childEmit := emit && m.isExpanded(n)
	for _, c := range m.sorted(n.children) {
		total.add(m.walk(c, depth+1, childEmit))
	}
	if emit {
		if total.visible == 0 {
			m.rows = m.rows[:pos]
		} else {
			m.rows[pos].stats = total
		}
	}
	return total
}

func (m *Checklist) sorted(nodes []*node) []*node {
	if m.sort == sortSize {
		return nodes
	}
	out := slices.Clone(nodes)
	slices.SortStableFunc(out, func(a, b *node) int {
		if m.sort == sortName {
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

// listHeight is the number of tree rows that fit between header and footer.
func (m *Checklist) listHeight() int {
	return max(m.height-len(m.provenanceLines())-7, 1)
}

// maxProvenanceLines and detailLines bound the wrapped header and the
// detail area under the list, so the list keeps most of the screen.
const (
	maxProvenanceLines = 4
	detailLines        = 2
)

// wrap breaks s into lines of at most width columns, keeping at most n.
func wrap(s string, width, n int) []string {
	if s == "" {
		return nil
	}
	text := lipgloss.NewStyle().Width(max(width, 20)).Render(s)
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		out = append(out, strings.TrimRight(l, " "))
	}
	if len(out) > n {
		out = out[:n]
		out[n-1] += "..."
	}
	return out
}

// provenanceLines is the provenance line wrapped to the window, so its end,
// which says when a report covers only some folders, is always visible.
func (m *Checklist) provenanceLines() []string {
	return wrap(m.opts.Provenance, m.width, maxProvenanceLines)
}

// detailText describes the row under the cursor in full: the warning of a
// finding, which a narrow window cuts off in the row, or else its path.
func (m *Checklist) detailText() []string {
	n := m.current()
	if n == nil || n.leaf < 0 {
		return nil
	}
	f := &m.findings[n.leaf]
	if f.Warning != "" {
		return wrap("! "+f.Warning, m.width, detailLines)
	}
	return wrap(shortener(m.opts.Home)(f.DisplayName()), m.width, detailLines)
}

func (m *Checklist) scroll() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(min(m.offset, len(m.rows)-h), 0)
}

// View implements tea.Model. Only the rows in the window are rendered.
func (m *Checklist) View() string {
	var b strings.Builder
	line := func(s string) {
		b.WriteString(lipgloss.NewStyle().MaxWidth(m.width).Render(s))
		b.WriteByte('\n')
	}
	line(m.styles.header.Render("reclaim select"))
	for _, l := range m.provenanceLines() {
		line(m.styles.dim.Render(l))
	}
	line(m.filterLine())
	h := m.listHeight()
	end := min(m.offset+h, len(m.rows))
	for i := m.offset; i < end; i++ {
		line(m.renderRow(i))
	}
	if len(m.rows) == 0 {
		line(m.styles.dim.Render("  nothing matches the filters; esc clears the text filter, t cycles the tier filter"))
		end++
	}
	for i := end - m.offset; i < h; i++ {
		b.WriteByte('\n')
	}
	detail := m.detailText()
	for i := range detailLines {
		if i < len(detail) {
			line(m.styles.warn.Render(detail[i]))
		} else {
			line("")
		}
	}
	n, size := m.SelectedSize()
	line(m.styles.bold.Render(fmt.Sprintf("Selected: %s, %s", plural(n, "item", "items"), units.FormatSize(size))) +
		"   w writes " + m.opts.PlanPath + "   q quits without writing")
	line(m.styles.dim.Render("space toggle  enter expand  / filter  s sort  t tier  a select tier A  arrows move"))
	if m.status != "" {
		line(m.styles.warn.Render(m.status))
	} else {
		line("")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (m *Checklist) filterLine() string {
	tier := "all"
	if m.tier != "" {
		tier = string(m.tier)
	}
	state := fmt.Sprintf("sort: %s  tier: %s", m.sort, tier)
	if m.filtering {
		return m.input.View() + "  " + m.styles.dim.Render(state)
	}
	if m.filter != "" {
		return "filter: " + m.filter + "  " + m.styles.dim.Render(state+"  esc clears")
	}
	return m.styles.dim.Render(state)
}

func (m *Checklist) renderRow(i int) string {
	r := m.rows[i]
	n := r.node
	indent := strings.Repeat("  ", r.depth)
	var s string
	if n.leaf >= 0 {
		s = indent + m.leafText(n)
	} else {
		arrow := "▸"
		if m.isExpanded(n) {
			arrow = "▾"
		}
		title := n.title
		if r.depth == 0 {
			title = m.styles.bold.Render(title)
		}
		size, extra := r.stats.size, ""
		if r.stats.selectable == 0 {
			size, extra = r.stats.attention, ", not counted, never removed"
		}
		s = indent + m.box(r.stats) + " " + arrow + " " + title + "  " + units.FormatSize(size) + "  " +
			m.styles.dim.Render(plural(r.stats.visible, "item", "items")+extra)
		if r.stats.selected > 0 {
			s += m.styles.dim.Render(fmt.Sprintf(", %d selected", r.stats.selected))
		}
	}
	if i == m.cursor {
		return m.styles.cursor.Render(">") + " " + s
	}
	return "  " + s
}

func (m *Checklist) box(st stats) string {
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

func (m *Checklist) leafText(n *node) string {
	f := &m.findings[n.leaf]
	box := "[ ]"
	switch {
	case !f.Actionable():
		box = " - "
	case m.selected[n.leaf]:
		box = "[x]"
	}
	label := f.DisplayName()
	if n.group != nil && n.group.Project != "" {
		label = (&Printer{home: m.opts.Home}).label(n.group, f)
	} else {
		label = shortener(m.opts.Home)(label)
	}
	s := box + " " + m.tierText(f.Tier) + " " + size(f.Size) + "  " + label + m.styles.dim.Render("  "+f.Scanner)
	if !f.LastUsed.IsZero() && !m.opts.Now.IsZero() {
		s += m.styles.dim.Render("  used " + (&Printer{now: m.opts.Now}).age(f.LastUsed))
	}
	if (f.Tier == finding.TierC || !f.Actionable()) && f.Warning != "" {
		s += "  " + m.styles.warn.Render("! "+f.Warning)
	}
	return s
}

func (m *Checklist) tierText(t finding.Tier) string {
	switch t {
	case finding.TierA:
		return m.styles.tierA.Render("A")
	case finding.TierB:
		return m.styles.tierB.Render("B")
	default:
		return m.styles.tierC.Render(string(t))
	}
}
