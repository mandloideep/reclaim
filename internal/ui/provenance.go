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
// select reads the most recent report of any kind.
func (p *Printer) Provenance(r *finding.Report) string {
	what := "an older reclaim that did not record its command"
	if r.Scope != nil {
		what = commandLine(r.Scope)
	}
	when := "at an unknown time"
	if !r.Created.IsZero() {
		when = p.age(r.Created) + " (" + r.Created.Local().Format("2006-01-02 15:04") + ")"
	}
	line := fmt.Sprintf("Report from %s, %s: %s, %s reclaimable.", what, when,
		plural(len(r.Findings), "finding", "findings"), units.FormatSize(r.TotalSize()))
	if r.Scope.Partial() {
		paths := make([]string, len(r.Scope.Paths))
		for i, path := range r.Scope.Paths {
			paths[i] = p.Path(path)
		}
		line += fmt.Sprintf(" It covers only %s, not the whole machine; run reclaim scan for everything.", strings.Join(paths, ", "))
	}
	return line
}
