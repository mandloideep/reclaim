package ui

import (
	"fmt"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/units"
)

// Provenance describes in one line where a report came from: the command
// that wrote it, when, and how many findings it holds. A report from here or
// from a scan given paths says that it covers only those folders, because
// select reads the most recent report of any kind. The time is shown in the
// zone of the printer's clock, the local zone in normal use.
func (p *Printer) Provenance(r *finding.Report) string {
	when := "at an unknown time"
	if !r.Created.IsZero() {
		when = p.age(r.Created) + " (" + r.Created.In(p.now.Location()).Format("2006-01-02 15:04") + ")"
	}
	line := fmt.Sprintf("Report from %s, %s: %s, %s reclaimable.", reportCommand(r), when, findingCount(r), units.FormatSize(r.TotalSize()))
	if c := p.Coverage(r); c != "" {
		line += " " + c
	}
	return line
}

// Summary describes a report in a few words for a title line, such as
// "reclaim scan, just now: 25 findings and 1 for attention, 25.9 GB
// reclaimable".
func (p *Printer) Summary(r *finding.Report) string {
	when := "at an unknown time"
	if !r.Created.IsZero() {
		when = p.age(r.Created)
	}
	return fmt.Sprintf("%s, %s: %s, %s reclaimable", reportCommand(r), when, findingCount(r), units.FormatSize(r.TotalSize()))
}

// Coverage says, for a report from here, from worktrees or from a scan
// given paths, that it covers only those folders. It is empty for a report
// of the whole machine.
func (p *Printer) Coverage(r *finding.Report) string {
	if !r.Scope.Partial() {
		return ""
	}
	paths := make([]string, len(r.Scope.Paths))
	for i, path := range r.Scope.Paths {
		paths[i] = p.Path(path)
	}
	covers := strings.Join(paths, ", ")
	if r.Scope.Command == "worktrees" {
		covers = "git worktrees"
		if len(paths) > 0 {
			covers += " under " + strings.Join(paths, ", ")
		}
	}
	return fmt.Sprintf("It covers only %s, not the whole machine; run reclaim scan for everything.", covers)
}

// reportCommand is the command that wrote a report.
func reportCommand(r *finding.Report) string {
	if r.Scope == nil {
		return "an older reclaim that did not record its command"
	}
	return commandLine(r.Scope)
}

// findingCount counts the findings that can be selected and the ones listed
// for attention only.
func findingCount(r *finding.Report) string {
	count := plural(actionable(r.Findings), "finding", "findings")
	if n := len(r.Findings) - actionable(r.Findings); n > 0 {
		count += fmt.Sprintf(" and %d for attention only", n)
	}
	return count
}
