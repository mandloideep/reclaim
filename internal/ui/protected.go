package ui

import (
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/apply"
)

// NewWithRenderer returns a Printer for w that styles its output with r, for
// screens that render into strings with the renderer of their terminal.
func NewWithRenderer(w io.Writer, r *lipgloss.Renderer, home string, now time.Time) *Printer {
	return newPrinter(w, r, home, now)
}

// protectedIntro opens the protected list.
const protectedIntro = "reclaim never removes these, whatever a plan says: apply checks every target against them right before removing it."

// ProtectedLines renders the protected list apply builds from the tables
// its checks use, grouped by where each entry comes from, in lines of at
// most width columns. Paths that share a rule are listed together under it,
// and rules that name no path are listed one per line. reclaim apply
// --dry-run prints it and the interactive screen shows it, so both say the
// same.
func (p *Printer) ProtectedLines(entries []apply.ProtectedEntry, width int) []string {
	width = max(width, 24)
	out := wrapWords(protectedIntro, width)
	inner := width - 4
	for i := 0; i < len(entries); {
		src := entries[i].Source
		j := i
		for j < len(entries) && entries[j].Source == src {
			j++
		}
		out = append(out, "", p.bold.Render(protectedTitle(src)))
		var texts []string
		paths := map[string][]string{}
		for _, e := range entries[i:j] {
			if e.Path == "" {
				for k, l := range wrapWords(e.Text, inner) {
					bullet := "- "
					if k > 0 {
						bullet = "  "
					}
					out = append(out, "  "+bullet+l)
				}
				continue
			}
			if _, seen := paths[e.Text]; !seen {
				texts = append(texts, e.Text)
			}
			paths[e.Text] = append(paths[e.Text], p.Path(e.Path))
		}
		for _, text := range texts {
			for _, l := range wrapWords(text, width-2) {
				out = append(out, "  "+p.dim.Render(l))
			}
			for _, l := range Pack(paths[text], "   ", inner) {
				out = append(out, "    "+l)
			}
		}
		i = j
	}
	return out
}

// Protected prints the protected list for a terminal of the given width.
func (p *Printer) Protected(entries []apply.ProtectedEntry, width int) {
	for _, l := range p.ProtectedLines(entries, width) {
		p.println(l)
	}
}

func protectedTitle(s apply.ProtectedSource) string {
	switch s {
	case apply.ProtectedSystem:
		return "System paths (built in)"
	case apply.ProtectedHome:
		return "Folders of your home folder (built in)"
	case apply.ProtectedRule:
		return "Rules (built in)"
	case apply.ProtectedRoot:
		return "Scanned roots"
	case apply.ProtectedExclude:
		return "Your exclude entries (config file)"
	default:
		return string(s)
	}
}

// Pack lays items out in lines of at most width columns, separated by sep,
// never breaking an item, such as a path with a space in it, across lines.
func Pack(items []string, sep string, width int) []string {
	var lines []string
	line := ""
	for _, it := range items {
		switch {
		case line == "":
			line = it
		case lipgloss.Width(line+sep+it) <= width:
			line += sep + it
		default:
			lines = append(lines, line)
			line = it
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// wrapWords breaks s into lines of at most width columns.
func wrapWords(s string, width int) []string {
	if s == "" {
		return nil
	}
	text := lipgloss.NewStyle().Width(max(width, 10)).Render(s)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}
