// Package ui renders reports, folder breakdowns and plans for the terminal.
//
// Colors are used only when the output is a terminal and NO_COLOR is unset;
// lipgloss detects both from the writer. Sizes use decimal units.
package ui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/units"
)

// Printer writes styled text to one writer.
type Printer struct {
	w    io.Writer
	home string
	now  time.Time

	bold   lipgloss.Style
	dim    lipgloss.Style
	warn   lipgloss.Style
	tierA  lipgloss.Style
	tierB  lipgloss.Style
	tierC  lipgloss.Style
	header lipgloss.Style
}

// New returns a Printer for w. Paths under home are shortened to "~". It
// uses colors only when w is a terminal and NO_COLOR is unset.
func New(w io.Writer, home string, now time.Time) *Printer {
	return newPrinter(w, lipgloss.NewRenderer(w), home, now)
}

// NewPlain returns a Printer for w that never styles its output, for
// formats such as Markdown that are read as text.
func NewPlain(w io.Writer, home string, now time.Time) *Printer {
	return newPrinter(w, lipgloss.NewRenderer(io.Discard), home, now)
}

func newPrinter(w io.Writer, r *lipgloss.Renderer, home string, now time.Time) *Printer {
	return &Printer{
		w:      w,
		home:   home,
		now:    now,
		bold:   r.NewStyle().Bold(true),
		dim:    r.NewStyle().Faint(true),
		warn:   r.NewStyle().Foreground(lipgloss.Color("3")),
		tierA:  r.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
		tierB:  r.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
		tierC:  r.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		header: r.NewStyle().Bold(true).Underline(true),
	}
}

// Path shortens a path under the home directory to start with "~".
func (p *Printer) Path(path string) string {
	if p.home == "" || path == "" {
		return path
	}
	if path == p.home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, p.home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// Tier renders a tier letter in its color.
func (p *Printer) Tier(t finding.Tier) string {
	switch t {
	case finding.TierA:
		return p.tierA.Render(string(t))
	case finding.TierB:
		return p.tierB.Render(string(t))
	case finding.TierC:
		return p.tierC.Render(string(t))
	default:
		return string(t)
	}
}

// TierCell renders the tier column of a finding. Findings listed for
// attention only carry no action, so their tier column is blank: the tier
// letter would suggest that they could be selected.
func (p *Printer) TierCell(f *finding.Finding) string {
	if f.Action == finding.ActionNone {
		return " "
	}
	return p.Tier(f.Tier)
}

func (p *Printer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.w, format, args...)
}

func (p *Printer) println(s string) {
	_, _ = io.WriteString(p.w, s+"\n")
}

// size renders a size right aligned in a fixed width column.
func size(n int64) string {
	return fmt.Sprintf("%9s", units.FormatSize(n))
}

// pad pads s with spaces to width visible columns.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

func (p *Printer) age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	age := units.FormatAge(p.now.Sub(t))
	if age == "now" {
		return "just now"
	}
	return age + " ago"
}

// tierTotals formats the per tier totals, such as "A 1.2 GB, B 300 MB".
func tierTotals(fs []finding.Finding) string {
	sums := map[finding.Tier]int64{}
	for i := range fs {
		if fs[i].Actionable() {
			sums[fs[i].Tier] += fs[i].Size
		}
	}
	var parts []string
	for _, t := range finding.Tiers() {
		if n, ok := sums[t]; ok {
			parts = append(parts, string(t)+" "+units.FormatSize(n))
		}
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Warnings prints the report warnings and notes.
func (p *Printer) Warnings(ws []finding.Warning, notes []string) {
	if len(ws) > 0 {
		p.println("")
		p.println(p.warn.Render(fmt.Sprintf("Warnings (%d)", len(ws))))
		for _, w := range ws {
			w.Path = p.Path(w.Path)
			p.println("  " + w.String())
		}
	}
	if len(notes) > 0 {
		p.println("")
		for _, n := range notes {
			p.println(p.dim.Render("Note: ") + n)
		}
	}
}
