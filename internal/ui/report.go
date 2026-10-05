package ui

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/units"
)

// Report prints a report grouped by category and, for project artifacts, by
// project, with the largest groups first.
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
	for _, cat := range categoriesIn(r.Findings) {
		fs := filterCategory(r.Findings, cat)
		var total int64
		for i := range fs {
			total += fs[i].Size
		}
		p.println("")
		p.printf("%s  %s  %s\n", p.header.Render(cat.Title()), p.bold.Render(units.FormatSize(total)), p.dim.Render(plural(len(fs), "item", "items")))
		if cat == finding.CategoryProject {
			p.projectGroups(fs, nameWidth)
			continue
		}
		for i := range fs {
			p.findingRow(&fs[i], nameWidth, "  ", p.Path(fs[i].DisplayName()))
		}
	}
	p.println("")
	p.printf("%s %s in %s (%s)\n", p.bold.Render("Total reclaimable:"), p.bold.Render(units.FormatSize(r.TotalSize())),
		plural(len(r.Findings), "item", "items"), tierTotals(r.Findings))
	p.println(p.dim.Render("Tier A rebuilds itself, B costs a download or rebuild, C may be data. Nothing was removed."))
	p.Warnings(r.Warnings, r.Notes)
}

type projectGroup struct {
	root     string
	total    int64
	findings []finding.Finding
}

func (p *Printer) projectGroups(fs []finding.Finding, nameWidth int) {
	byRoot := map[string]*projectGroup{}
	var groups []*projectGroup
	for i := range fs {
		g, ok := byRoot[fs[i].Project]
		if !ok {
			g = &projectGroup{root: fs[i].Project}
			byRoot[fs[i].Project] = g
			groups = append(groups, g)
		}
		g.total += fs[i].Size
		g.findings = append(g.findings, fs[i])
	}
	slices.SortStableFunc(groups, func(a, b *projectGroup) int {
		if a.root == "" || b.root == "" {
			return cmp.Compare(boolInt(a.root == ""), boolInt(b.root == ""))
		}
		if a.total != b.total {
			return cmp.Compare(b.total, a.total)
		}
		return strings.Compare(a.root, b.root)
	})
	for _, g := range groups {
		title := p.Path(g.root)
		if g.root == "" {
			title = "(not in a project)"
		}
		var last string
		if len(g.findings) > 0 && !g.findings[0].LastUsed.IsZero() {
			last = p.dim.Render("  active " + p.age(g.findings[0].LastUsed))
		}
		p.printf("  %s  %s%s\n", p.bold.Render(title), units.FormatSize(g.total), last)
		for i := range g.findings {
			f := &g.findings[i]
			label := p.Path(f.Path)
			if g.root != "" {
				if rel, err := filepath.Rel(g.root, f.Path); err == nil && !strings.HasPrefix(rel, "..") {
					label = rel
				}
			}
			p.findingRow(f, nameWidth, "    ", label)
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (p *Printer) findingRow(f *finding.Finding, nameWidth int, indent, label string) {
	line := indent + p.Tier(f.Tier) + " " + size(f.Size) + "  " + pad(f.Scanner, nameWidth) + "  " + label
	if f.Action == finding.ActionRunCommand {
		line += p.dim.Render("  via " + strings.Join(f.Command, " "))
	}
	if f.Tier == finding.TierC && f.Warning != "" {
		line += "\n" + indent + strings.Repeat(" ", 15+nameWidth) + p.warn.Render("! "+f.Warning)
	}
	p.println(line)
}

func categoriesIn(fs []finding.Finding) []finding.Category {
	var out []finding.Category
	for i := range fs {
		if !slices.Contains(out, fs[i].Category) {
			out = append(out, fs[i].Category)
		}
	}
	return out
}

func filterCategory(fs []finding.Finding, c finding.Category) []finding.Finding {
	var out []finding.Finding
	for i := range fs {
		if fs[i].Category == c {
			out = append(out, fs[i])
		}
	}
	return out
}

// Numbered prints findings with 1 based indices for selection from stdin.
func (p *Printer) Numbered(fs []finding.Finding) {
	nameWidth := len("SCANNER")
	for i := range fs {
		nameWidth = max(nameWidth, len(fs[i].Scanner))
	}
	idxWidth := len(strconv.Itoa(len(fs)))
	p.println(p.dim.Render(pad("#", idxWidth+2) + "T " + pad("     SIZE", 9) + "  " + pad("SCANNER", nameWidth) + "  ITEM"))
	for i := range fs {
		f := &fs[i]
		line := pad(strconv.Itoa(i+1)+".", idxWidth+2) + p.Tier(f.Tier) + " " + size(f.Size) + "  " + pad(f.Scanner, nameWidth) + "  " + p.Path(f.DisplayName())
		if f.Tier == finding.TierC && f.Warning != "" {
			line += "\n" + strings.Repeat(" ", idxWidth+2+15+nameWidth) + p.warn.Render("! "+f.Warning)
		}
		p.println(line)
	}
}
