package ui

import (
	"fmt"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/units"
)

// Here prints the breakdown of a folder: its entries sorted by size down to
// the depth of the tree, each with the reclaimable bytes inside it and the
// tier of entries that are themselves findings, then the full list of
// reclaimable findings at any depth.
func (p *Printer) Here(root *fsx.Node, fs []finding.Finding, warnings []finding.Warning, notes []string) {
	var reclaim int64
	for i := range fs {
		reclaim += fs[i].Size
	}
	p.printf("%s  %s total, %s reclaimable\n", p.bold.Render(p.Path(root.Path)),
		units.FormatSize(root.Size), p.bold.Render(units.FormatSize(reclaim)))
	p.println("")
	p.println(p.dim.Render("     SIZE  RECLAIMABLE  NAME"))
	p.hereChildren(root, fs, "")

	p.println("")
	if len(fs) == 0 {
		p.println("Nothing reclaimable found in this folder.")
	} else {
		p.printf("%s %s\n", p.header.Render("Reclaimable"), p.dim.Render("("+plural(len(fs), "item", "items")+", "+units.FormatSize(reclaim)+")"))
		nameWidth := 0
		for i := range fs {
			nameWidth = max(nameWidth, len(fs[i].Scanner))
		}
		for i := range fs {
			f := &fs[i]
			line := "  " + p.Tier(f.Tier) + " " + size(f.Size) + "  " + pad(f.Scanner, nameWidth) + "  " + p.Path(f.Path)
			if f.Restore != "" {
				line += p.dim.Render("  restore: " + f.Restore)
			}
			if f.Tier == finding.TierC && f.Warning != "" {
				line += "\n  " + strings.Repeat(" ", 15+nameWidth) + p.warn.Render("! "+f.Warning)
			}
			p.println(line)
		}
	}
	p.Warnings(warnings, notes)
}

func (p *Printer) hereChildren(n *fsx.Node, fs []finding.Finding, indent string) {
	for _, c := range n.Children {
		var inside int64
		var own *finding.Finding
		for i := range fs {
			if project.IsWithin(fs[i].Path, c.Path) {
				inside += fs[i].Size
			}
			if fs[i].Path == c.Path {
				own = &fs[i]
			}
		}
		recSize, tier := "-", " "
		if inside > 0 {
			recSize = units.FormatSize(inside)
		}
		if own != nil {
			tier = p.Tier(own.Tier)
		}
		rec := fmt.Sprintf("%9s", recSize) + " " + tier
		if inside == 0 {
			rec = p.dim.Render(rec)
		}
		name := c.Name
		if c.IsDir {
			name += "/"
		}
		if own != nil {
			name += p.dim.Render("  " + own.Scanner)
		}
		p.println(size(c.Size) + "  " + rec + "  " + indent + name)
		if c.IsDir && own == nil {
			p.hereChildren(c, fs, indent+"  ")
		}
	}
}
