package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/units"
)

// worktreeColumns are the columns of the worktrees table.
func worktreeColumns() []string {
	return []string{"TIER", "TOOL", "BRANCH", "UNCOMMITTED", "UNPUSHED", "MERGED", "PR", "LOCK", "SIZE", "PATH"}
}

// WorktreeCells are the plain text cells of one worktree's row, in the
// order of the columns after TIER.
func WorktreeCells(s *finding.WorktreeState) []string {
	unknown := func(part string) bool { return slices.Contains(s.Unknown, part) }
	branch := s.Branch
	if branch == "" {
		branch = "detached " + shortCommit(s.Head)
	}
	var changed []string
	switch {
	case unknown("modified"):
		changed = append(changed, "?")
	default:
		if s.Modified > 0 {
			changed = append(changed, strconv.Itoa(s.Modified)+" modified")
		}
		if s.Untracked > 0 {
			changed = append(changed, strconv.Itoa(s.Untracked)+" untracked")
		}
	}
	if unknown("ignored") {
		changed = append(changed, "ignored ?")
	} else if s.Ignored > 0 {
		changed = append(changed, strconv.Itoa(s.Ignored)+" ignored")
	}
	unpushed := "-"
	switch {
	case unknown("unpushed"), unknown("loose"):
		unpushed = "?"
	case s.Loose > 0:
		unpushed = fmt.Sprintf("%d, %d loose", s.Unpushed, s.Loose)
	case s.Unpushed > 0:
		unpushed = strconv.Itoa(s.Unpushed)
	}
	pr := "-"
	if s.PR != nil {
		pr = fmt.Sprintf("#%d %s", s.PR.Number, strings.ToLower(s.PR.State))
	}
	lock := "-"
	if s.Locked {
		pid := lockPID(s.LockReason)
		switch {
		case s.LockLive && pid != "":
			lock = "live, pid " + pid
		case s.LockLive:
			lock = "locked"
		case pid != "":
			lock = "stale, pid " + pid + " gone"
		default:
			lock = "stale"
		}
	}
	return []string{s.Tool, branch, orDash(strings.Join(changed, ", ")), unpushed, string(s.Merged), pr, lock}
}

// lockPID returns the process id a lock reason names, if any.
func lockPID(reason string) string {
	_, rest, ok := strings.Cut(reason, "pid ")
	if !ok {
		return ""
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// worktreeTier is the tier cell: the tier for a worktree reclaim offers and
// a dash for one it only lists.
func worktreeTier(f *finding.Finding) string {
	if !f.Actionable() {
		return "-"
	}
	return string(f.Tier)
}

// Worktrees prints every linked worktree of every repository in the report,
// one table per repository, with the state of each, and under the ones
// that hold work the commands that save it.
func (p *Printer) Worktrees(r *finding.Report) {
	sections := Sections(r.Findings, p.Path)
	var groups []Group
	for _, s := range sections {
		if s.Category == finding.CategoryWorktree {
			groups = s.Groups
		}
	}
	if len(groups) == 0 {
		p.println("No linked git worktrees found.")
		p.Warnings(r.Warnings, r.Notes)
		return
	}
	cols := worktreeColumns()
	for gi := range groups {
		g := &groups[gi]
		p.println("")
		p.println(p.header.Render(g.Title))
		if line := p.RepoLine(g.Repo); line != "" {
			p.println(p.dim.Render(line))
		}
		var rows [][]string
		var states []*finding.Finding
		var others []*finding.Finding
		for i := range g.Findings {
			f := &g.Findings[i]
			if f.Worktree == nil {
				others = append(others, f)
				continue
			}
			row := append([]string{worktreeTier(f)}, WorktreeCells(f.Worktree)...)
			row = append(row, units.FormatSize(f.Size), p.relPath(g, f.Path))
			rows = append(rows, row)
			states = append(states, f)
		}
		if len(rows) > 0 {
			widths := make([]int, len(cols))
			for i, c := range cols {
				widths[i] = len(c)
			}
			for _, row := range rows {
				for i, c := range row {
					widths[i] = max(widths[i], len(c))
				}
			}
			p.println(p.dim.Render(joinRow(cols, widths)))
			for ri, row := range rows {
				f := states[ri]
				line := joinRow(row, widths)
				if f.Actionable() {
					// Color the tier letter without breaking the alignment.
					line = p.Tier(f.Tier) + line[1:]
				}
				p.println(line)
				p.worktreeDetails(f, "     ")
			}
		}
		for _, f := range others {
			line := worktreeTier(f) + "  " + units.FormatSize(f.Size) + "  " + p.label(g, f)
			if f.Actionable() {
				line = p.Tier(f.Tier) + line[1:]
			}
			if f.Action == finding.ActionGitWorktreePrune {
				line += p.dim.Render("  via " + strings.Join(f.Command, " "))
			}
			p.println(line)
			if f.Warning != "" && !f.Actionable() {
				p.println("     " + p.warn.Render("! "+f.Warning))
			}
		}
	}
	p.println("")
	p.println(p.worktreeTotals(r.Findings))
	p.println(p.dim.Render("A is committed, pushed and merged; B is pushed but not merged; - is listed only. Nothing was removed."))
	p.Warnings(r.Warnings, r.Notes)
}

// worktreeDetails prints, under a worktree that is not offered, why, the
// commands that save its work and the command that removes it anyway.
func (p *Printer) worktreeDetails(f *finding.Finding, indent string) {
	s := f.Worktree
	if f.Actionable() {
		if f.Tier == finding.TierB && f.Warning != "" {
			p.println(indent + p.dim.Render(f.Warning))
		}
		return
	}
	if f.Warning != "" {
		p.println(indent + p.warn.Render("! "+f.Warning))
	}
	for _, h := range s.Hints {
		p.println(indent + p.dim.Render("save: ") + h)
	}
	if s.ForceCommand != "" {
		p.println(indent + p.dim.Render("remove anyway, losing it: ") + s.ForceCommand)
	}
}

// worktreeTotals counts the worktrees in each state and what tier A and B
// would free.
func (p *Printer) worktreeTotals(fs []finding.Finding) string {
	var n int
	counts := map[finding.Verdict]int{}
	for i := range fs {
		if s := fs[i].Worktree; s != nil {
			n++
			counts[s.Verdict]++
		}
	}
	parts := []string{p.bold.Render(plural(n, "linked worktree", "linked worktrees"))}
	for _, v := range []struct {
		verdict finding.Verdict
		words   string
	}{{finding.VerdictRemovable, "removable"}, {finding.VerdictPushed, "pushed, not merged"},
		{finding.VerdictAttention, "holding work that exists nowhere else or could not be checked"}, {finding.VerdictInUse, "in use"}} {
		if counts[v.verdict] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[v.verdict], v.words))
		}
	}
	line := strings.Join(parts, ", ")
	var worktreeFindings []finding.Finding
	for i := range fs {
		if fs[i].Category == finding.CategoryWorktree {
			worktreeFindings = append(worktreeFindings, fs[i])
		}
	}
	if size := finding.ReclaimableSize(worktreeFindings); size > 0 {
		line += "; " + p.bold.Render(units.FormatSize(size)) + " reclaimable (" + tierTotals(worktreeFindings) + ")"
	}
	return line
}

// joinRow pads every cell but the last to its column width.
func joinRow(cells []string, widths []int) string {
	var b strings.Builder
	for i, c := range cells {
		if i == len(cells)-1 {
			b.WriteString(c)
			break
		}
		b.WriteString(c)
		b.WriteString(strings.Repeat(" ", widths[i]-len(c)+2))
	}
	return strings.TrimRight(b.String(), " ")
}

// WorktreesMarkdown writes the worktrees report as Markdown: a heading and a
// table per repository and, under the table, what each worktree that is not
// offered holds and how to save it.
func (p *Printer) WorktreesMarkdown(r *finding.Report) {
	p.println("# Git worktrees")
	p.println("")
	intro := "Scanned " + r.Created.UTC().Format("2006-01-02 15:04 UTC")
	if r.Scope != nil {
		intro += " by " + mdCode(commandLine(r.Scope))
	}
	var groups []Group
	for _, s := range Sections(r.Findings, p.Path) {
		if s.Category == finding.CategoryWorktree {
			groups = s.Groups
		}
	}
	if len(groups) == 0 {
		p.println(intro + ". No linked git worktrees found.")
		p.markdownWarnings(r.Warnings, r.Notes)
		return
	}
	p.println(intro + ".")
	for gi := range groups {
		g := &groups[gi]
		p.println("")
		p.printf("## %s\n", mdCode(g.Title))
		p.println("")
		if line := p.RepoLine(g.Repo); line != "" {
			p.println(mdText(line) + ".")
			p.println("")
		}
		p.println("| Tier | Tool | Branch | Uncommitted | Unpushed | Merged | PR | Lock | Size | Path |")
		p.println("| --- | --- | --- | --- | --- | --- | --- | --- | ---: | --- |")
		var notes []string
		for i := range g.Findings {
			f := &g.Findings[i]
			if f.Worktree == nil {
				p.printf("| %s | | %s | | | | | | %s | %s |\n", mdTier(f), mdCell(f.Name), units.FormatSize(f.Size), mdCodeCell(p.relPath(g, orDash(f.Path))))
				if f.Warning != "" && !f.Actionable() {
					notes = append(notes, mdCode(p.relPath(g, f.Path))+": "+mdText(f.Warning))
				}
				continue
			}
			cells := WorktreeCells(f.Worktree)
			tier := worktreeTier(f)
			row := []string{tier}
			for _, c := range cells {
				row = append(row, mdCell(c))
			}
			row = append(row, units.FormatSize(f.Size), mdCodeCell(p.relPath(g, f.Path)))
			p.println("| " + strings.Join(row, " | ") + " |")
			if !f.Actionable() {
				var n strings.Builder
				n.WriteString(mdCode(p.relPath(g, f.Path)) + ": " + mdText(f.Warning))
				for _, h := range f.Worktree.Hints {
					n.WriteString("\n  - save: " + mdCode(h))
				}
				if f.Worktree.ForceCommand != "" {
					n.WriteString("\n  - remove anyway, losing it: " + mdCode(f.Worktree.ForceCommand))
				}
				notes = append(notes, n.String())
			}
		}
		if len(notes) > 0 {
			p.println("")
			for _, n := range notes {
				p.println("- " + n)
			}
		}
	}
	p.println("")
	p.println(p.worktreeTotals(r.Findings) + ".")
	p.println("")
	p.println("A is committed, pushed and merged; B is pushed but not merged; a dash is listed only. Nothing was removed.")
	p.markdownWarnings(r.Warnings, r.Notes)
}
