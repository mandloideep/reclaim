package ui

import (
	"path/filepath"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/units"
)

// ReportMarkdown writes a report as Markdown for pasting into an issue or a
// document, grouped exactly like the table: a heading per category, a
// heading per group with its subtotal, and a table of findings.
func (p *Printer) ReportMarkdown(r *finding.Report) {
	p.println("# Reclaimable space")
	p.println("")
	intro := "Scanned " + r.Created.UTC().Format("2006-01-02 15:04 UTC")
	if r.Scope != nil {
		intro += " by " + mdCode(commandLine(r.Scope))
	}
	if len(r.Findings) == 0 {
		p.println(intro + ". Nothing reclaimable found.")
		p.markdownWarnings(r.Warnings, r.Notes)
		return
	}
	p.printf("%s: **%s** reclaimable in %s (%s).\n", intro, units.FormatSize(r.TotalSize()),
		plural(actionable(r.Findings), "item", "items"), tierTotals(r.Findings))
	for _, s := range Sections(r.Findings, p.Path) {
		p.println("")
		p.printf("## %s (%s, %s)\n", s.Category.Title(), units.FormatSize(s.Size), plural(s.Count, "item", "items"))
		if s.Flat() {
			p.println("")
			p.markdownTable(&s.Groups[0])
			continue
		}
		for _, g := range s.Groups {
			title := g.Title
			if g.Project != "" {
				title = mdCode(title)
			}
			counted := ""
			if g.Attention {
				counted = ", not counted, reclaim never removes these"
			}
			p.println("")
			p.printf("### %s (%s, %s%s)\n", title, units.FormatSize(g.Size), plural(len(g.Findings), "item", "items"), counted)
			p.println("")
			p.markdownTable(&g)
		}
	}
	p.println("")
	p.println("Tier A rebuilds itself, tier B costs a download or a rebuild, tier C may be data. Nothing was removed.")
	p.markdownWarnings(r.Warnings, r.Notes)
}

func (p *Printer) markdownTable(g *Group) {
	p.println("| Tier | Size | Scanner | Item | Note |")
	p.println("| --- | ---: | --- | --- | --- |")
	for i := range g.Findings {
		f := &g.Findings[i]
		p.printf("| %s | %s | %s | %s | %s |\n", mdTier(f), units.FormatSize(f.Size), mdCell(f.Scanner),
			mdItem(p.label(g, f), f), mdCell(note(f)))
	}
}

// mdTier is the tier cell of a finding, blank for findings listed for
// attention only, which carry no action.
func mdTier(f *finding.Finding) string {
	if f.Action == finding.ActionNone {
		return ""
	}
	return string(f.Tier)
}

// mdItem shows paths as code and other names, such as image tags, as text.
func mdItem(label string, f *finding.Finding) string {
	if f.Name == "" {
		return mdCodeCell(label)
	}
	return mdCell(label)
}

// note is the extra information shown next to a finding: the command that
// cleans it, and its warning when it may be data.
func note(f *finding.Finding) string {
	var parts []string
	if f.Action == finding.ActionRunCommand {
		cmd := strings.Join(f.Command, " ")
		if f.NeedsSudo {
			cmd = "sudo " + cmd
		}
		parts = append(parts, "via "+cmd)
	}
	if (f.Tier == finding.TierC || !f.Actionable()) && f.Warning != "" {
		parts = append(parts, "warning: "+f.Warning)
	}
	return strings.Join(parts, "; ")
}

func (p *Printer) markdownWarnings(ws []finding.Warning, notes []string) {
	if len(ws) > 0 {
		p.println("")
		p.printf("## Warnings (%d)\n", len(ws))
		p.println("")
		for _, w := range ws {
			w.Path = p.Path(w.Path)
			p.println("- " + mdText(w.String()))
		}
	}
	if len(notes) > 0 {
		p.println("")
		p.println("## Notes")
		p.println("")
		for _, n := range notes {
			p.println("- " + mdText(n))
		}
	}
}

// HereMarkdown writes the breakdown of a folder as Markdown: its entries with
// their sizes and reclaimable bytes, then every reclaimable finding.
func (p *Printer) HereMarkdown(root *fsx.Node, fs []finding.Finding, warnings []finding.Warning, notes []string) {
	reclaim := finding.ReclaimableSize(fs)
	p.printf("# %s\n", mdCode(p.Path(root.Path)))
	p.println("")
	p.printf("%s total, **%s** reclaimable.\n", units.FormatSize(root.Size), units.FormatSize(reclaim))
	p.println("")
	p.println("| Size | Reclaimable | Tier | Entry |")
	p.println("| ---: | ---: | --- | --- |")
	p.hereMarkdownRows(root, root, fs)
	p.println("")
	if len(fs) == 0 {
		p.println("Nothing reclaimable found in this folder.")
	} else {
		p.printf("## Reclaimable (%s, %s)\n", plural(len(fs), "item", "items"), units.FormatSize(reclaim))
		p.println("")
		p.println("| Tier | Size | Scanner | Path | Note |")
		p.println("| --- | ---: | --- | --- | --- |")
		for i := range fs {
			f := &fs[i]
			n := note(f)
			if f.Restore != "" {
				n = strings.TrimPrefix(n+"; restore: "+f.Restore, "; ")
			}
			p.printf("| %s | %s | %s | %s | %s |\n", mdTier(f), units.FormatSize(f.Size), mdCell(f.Scanner), mdCodeCell(p.Path(f.Path)), mdCell(n))
		}
	}
	p.markdownWarnings(warnings, notes)
}

func (p *Printer) hereMarkdownRows(top, n *fsx.Node, fs []finding.Finding) {
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
		rec, tier := "-", ""
		if inside > 0 {
			rec = units.FormatSize(inside)
		}
		if own != nil {
			tier = strings.TrimSpace(mdTier(own) + " " + own.Scanner)
		}
		name, err := filepath.Rel(top.Path, c.Path)
		if err != nil {
			name = c.Name
		}
		if c.IsDir {
			name += "/"
		}
		p.printf("| %s | %s | %s | %s |\n", units.FormatSize(c.Size), rec, mdCell(tier), mdCodeCell(name))
		if c.IsDir && own == nil {
			p.hereMarkdownRows(top, c, fs)
		}
	}
}

// commandLine renders the command that produced a report.
func commandLine(s *finding.Scope) string {
	return strings.TrimSpace("reclaim " + s.Command + " " + strings.Join(s.Args, " "))
}

// mdCell makes text safe inside a table cell.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(mdText(s), "|", `\|`)
}

// mdText escapes the characters that would start Markdown formatting.
func mdText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "<", "&lt;", ">", "&gt;", "[", `\[`, "]", `\]`)
	return r.Replace(s)
}

// mdCodeCell renders s as inline code inside a table cell, where a pipe must
// be escaped even in code.
func mdCodeCell(s string) string {
	return mdCode(strings.ReplaceAll(s, "|", `\|`))
}

// mdCode renders s as inline code, using a longer fence when s contains
// backticks.
func mdCode(s string) string {
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return fence + pad + s + pad + fence
}
