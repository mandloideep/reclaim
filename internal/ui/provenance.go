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
	what := "an older reclaim that did not record its command"
	if r.Scope != nil {
		what = commandLine(r.Scope)
	}
	when := "at an unknown time"
	if !r.Created.IsZero() {
		when = p.age(r.Created) + " (" + r.Created.In(p.now.Location()).Format("2006-01-02 15:04") + ")"
	}
	count := plural(actionable(r.Findings), "finding", "findings")
	if n := len(r.Findings) - actionable(r.Findings); n > 0 {
		count += fmt.Sprintf(" and %d for attention only", n)
	}
	line := fmt.Sprintf("Report from %s, %s: %s, %s reclaimable.", what, when, count, units.FormatSize(r.TotalSize()))
	if r.Scope.Partial() {
		paths := make([]string, len(r.Scope.Paths))
		for i, path := range r.Scope.Paths {
			paths[i] = p.Path(path)
		}
		line += fmt.Sprintf(" It covers only %s, not the whole machine; run reclaim scan for everything.", strings.Join(paths, ", "))
	}
	return line
}
