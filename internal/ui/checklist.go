package ui

import (
	"fmt"
	"strings"
	"time"

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

// Checklist is the interactive selection screen of select. It is a
// bubbletea model around the findings tree, Tree, with a header that says
// where the report came from, the warning or path of the finding under the
// cursor, and a footer with the selection and the plan path. w writes the
// plan and q quits without writing; every other key belongs to the tree.
type Checklist struct {
	opts    ChecklistOptions
	tree    *Tree
	width   int
	height  int
	outcome ChecklistOutcome

	bold, dim, warn, header lipgloss.Style
}

var _ tea.Model = (*Checklist)(nil)

// NewChecklist returns a checklist over the report's findings with every
// tier A finding selected.
func NewChecklist(r *finding.Report, opts ChecklistOptions) *Checklist {
	re := opts.Renderer
	if re == nil {
		re = lipgloss.DefaultRenderer()
	}
	m := &Checklist{
		opts:   opts,
		tree:   NewTree(r, TreeOptions{Home: opts.Home, Renderer: re, Now: opts.Now}),
		width:  100,
		height: 24,
		bold:   re.NewStyle().Bold(true),
		dim:    re.NewStyle().Faint(true),
		warn:   re.NewStyle().Foreground(lipgloss.Color("3")),
		header: re.NewStyle().Bold(true).Underline(true),
	}
	m.layout()
	return m
}

// Init implements tea.Model.
func (m *Checklist) Init() tea.Cmd { return nil }

// Outcome reports how the checklist ended.
func (m *Checklist) Outcome() ChecklistOutcome { return m.outcome }

// Selected returns the selected findings in report order.
func (m *Checklist) Selected() []finding.Finding { return m.tree.Selected() }

// SelectedSize returns the number and total size of the selected findings.
func (m *Checklist) SelectedSize() (int, int64) { return m.tree.SelectedSize() }

// Update implements tea.Model.
func (m *Checklist) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		if handled, cmd := m.tree.HandleKey(msg); handled {
			return m, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.outcome = ChecklistQuit
			return m, tea.Quit
		case "w":
			m.outcome = ChecklistWrite
			return m, tea.Quit
		}
	}
	return m, nil
}

// layout gives the tree the rows between the header and the footer.
func (m *Checklist) layout() {
	m.tree.SetSize(m.width, max(m.height-len(m.provenanceLines())-7, 1))
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
// finding, which a narrow window cuts off in its row, or else its path.
func (m *Checklist) detailText() []string {
	it := m.tree.Current()
	if it.Kind != TreeFinding {
		return nil
	}
	if it.Finding.Warning != "" {
		return wrap("! "+it.Finding.Warning, m.width, detailLines)
	}
	return wrap(shortener(m.opts.Home)(it.Finding.DisplayName()), m.width, detailLines)
}

// View implements tea.Model. Only the rows in the window are rendered.
func (m *Checklist) View() string {
	var b strings.Builder
	line := func(s string) {
		b.WriteString(lipgloss.NewStyle().MaxWidth(m.width).Render(s))
		b.WriteByte('\n')
	}
	line(m.header.Render("reclaim select"))
	for _, l := range m.provenanceLines() {
		line(m.dim.Render(l))
	}
	line(m.tree.FilterLine())
	b.WriteString(m.tree.View())
	b.WriteByte('\n')
	detail := m.detailText()
	for i := range detailLines {
		if i < len(detail) {
			line(m.warn.Render(detail[i]))
		} else {
			line("")
		}
	}
	n, size := m.SelectedSize()
	line(m.bold.Render(fmt.Sprintf("Selected: %s, %s", plural(n, "item", "items"), units.FormatSize(size))) +
		"   w writes " + m.opts.PlanPath + "   q quits without writing")
	line(m.dim.Render("space toggle  enter expand  / filter  s sort  t tier  a select tier A  arrows move"))
	if s := m.tree.Status(); s != "" {
		line(m.warn.Render(s))
	} else {
		line("")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
