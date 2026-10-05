package ui

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/units"
)

// Report prints a report grouped by category and then by project, Docker
// object kind or scanner, with subtotals, largest groups first.
func (p *Printer) Report(r *finding.Report) {
	if len(r.Findings) == 0 {
		p.println("Nothing reclaimable found.")
		p.Warnings(r.Warnings, r.Notes)
		return
	}
	nameWidth := 0
	for i := range r.Findings {
		nameWidth = max(nameWidth, len(r.Findings[i].Scanner))
	}
	for _, s := range Sections(r.Findings, p.Path) {
		p.println("")
		p.printf("%s  %s  %s\n", p.header.Render(s.Category.Title()), p.bold.Render(units.FormatSize(s.Size)), p.dim.Render(plural(s.Count, "item", "items")))
		if s.Flat() {
			for _, f := range s.Groups[0].Findings {
				p.findingRow(&f, nameWidth, "  ", p.Path(f.DisplayName()))
			}
			continue
		}
		for _, g := range s.Groups {
			p.groupHeader(&g, "  ")
			for _, f := range g.Findings {
				p.findingRow(&f, nameWidth, "    ", p.label(&g, &f))
			}
		}
	}
	p.println("")
	p.printf("%s %s in %s (%s)\n", p.bold.Render("Total reclaimable:"), p.bold.Render(units.FormatSize(r.TotalSize())),
		plural(actionable(r.Findings), "item", "items"), tierTotals(r.Findings))
	p.println(p.dim.Render("Tier A rebuilds itself, B costs a download or rebuild, C may be data. Nothing was removed."))
	p.Warnings(r.Warnings, r.Notes)
}

// groupHeader prints the heading of a group with its subtotal.
func (p *Printer) groupHeader(g *Group, indent string) {
	extra := ""
	if g.Attention {
		extra = p.dim.Render("  not counted, reclaim never removes these")
	} else if g.Project != "" && len(g.Findings) > 0 && !g.Findings[0].LastUsed.IsZero() {
		extra = p.dim.Render("  active " + p.age(g.Findings[0].LastUsed))
	}
	p.printf("%s%s  %s  %s%s\n", indent, p.bold.Render(g.Title), units.FormatSize(g.Size),
		p.dim.Render(plural(len(g.Findings), "item", "items")), extra)
}

// label is how a finding is named inside its group: relative to its project
// for project groups, otherwise its display name with the home directory
// shortened.
func (p *Printer) label(g *Group, f *finding.Finding) string {
	if g.Project != "" && f.Path != "" {
		if rel, err := filepath.Rel(g.Project, f.Path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return p.Path(f.DisplayName())
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func actionable(fs []finding.Finding) int {
	n := 0
	for i := range fs {
		if fs[i].Actionable() {
			n++
		}
	}
	return n
}

func (p *Printer) findingRow(f *finding.Finding, nameWidth int, indent, label string) {
	line := indent + p.Tier(f.Tier) + " " + size(f.Size) + "  " + pad(f.Scanner, nameWidth) + "  " + label
	if f.Action == finding.ActionRunCommand {
		cmd := strings.Join(f.Command, " ")
		if f.NeedsSudo {
			cmd = "sudo " + cmd
		}
		line += p.dim.Render("  via " + cmd)
	}
	if f.Tier == finding.TierC && f.Warning != "" {
		line += "\n" + indent + strings.Repeat(" ", 15+nameWidth) + p.warn.Render("! "+f.Warning)
	}
	p.println(line)
}

// Numbered prints the findings grouped like the report, numbering the ones
// that can be selected, and returns them in numbering order: the finding
// numbered 1 comes first. Findings listed for attention only are shown
// without a number, so they can never be selected.
func (p *Printer) Numbered(fs []finding.Finding) []finding.Finding {
	sections := Sections(fs, p.Path)
	var numbered []finding.Finding
	for _, f := range Ordered(sections) {
		if f.Actionable() {
			numbered = append(numbered, f)
		}
	}
	nameWidth := len("SCANNER")
	for i := range fs {
		nameWidth = max(nameWidth, len(fs[i].Scanner))
	}
	idxWidth := len(strconv.Itoa(len(numbered))) + 2
	p.println(p.dim.Render(pad("#", idxWidth) + "T " + pad("     SIZE", 9) + "  " + pad("SCANNER", nameWidth) + "  ITEM"))
	n := 0
	for _, s := range sections {
		p.printf("%s  %s\n", p.header.Render(s.Category.Title()), p.dim.Render(units.FormatSize(s.Size)+", "+plural(s.Count, "item", "items")))
		for _, g := range s.Groups {
			if !s.Flat() {
				p.printf("%s%s  %s\n", strings.Repeat(" ", idxWidth), p.bold.Render(g.Title),
					p.dim.Render(units.FormatSize(g.Size)+", "+plural(len(g.Findings), "item", "items")+attentionNote(&g)))
			}
			for _, f := range g.Findings {
				idx := "-"
				if f.Actionable() {
					n++
					idx = strconv.Itoa(n) + "."
				}
				line := pad(idx, idxWidth) + p.Tier(f.Tier) + " " + size(f.Size) + "  " + pad(f.Scanner, nameWidth) + "  " + p.label(&g, &f)
				if (f.Tier == finding.TierC || !f.Actionable()) && f.Warning != "" {
					line += "\n" + strings.Repeat(" ", idxWidth+15+nameWidth) + p.warn.Render("! "+f.Warning)
				}
				p.println(line)
			}
		}
	}
	return numbered
}

func attentionNote(g *Group) string {
	if g.Attention {
		return ", not counted, cannot be selected"
	}
	return ""
}
