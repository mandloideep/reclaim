package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/units"
)

// Plan prints every action of a plan with its size, the way apply announces
// what it is about to do.
func (p *Printer) Plan(pl *plan.Plan) {
	p.printf("%s %s in %s, scanned %s\n", p.bold.Render("Plan:"), p.bold.Render(units.FormatSize(pl.TotalSize())),
		plural(len(pl.Actions), "action", "actions"), p.age(pl.Scanned))
	idxWidth := len(strconv.Itoa(len(pl.Actions)))
	for i := range pl.Actions {
		a := &pl.Actions[i]
		p.println(pad(strconv.Itoa(i+1)+".", idxWidth+2) + p.Tier(a.Tier) + " " + size(a.Size) + "  " + p.ActionLine(a))
		indent := strings.Repeat(" ", idxWidth+2+13)
		if a.Warning != "" {
			p.println(indent + p.warn.Render("! "+a.Warning))
		}
	}
}

// ActionLine describes what an action does in one line.
func (p *Printer) ActionLine(a *plan.Action) string {
	switch a.Action {
	case finding.ActionRemovePath:
		return "remove " + p.Path(a.Path) + p.dim.Render("  "+a.Scanner)
	case finding.ActionRunCommand:
		line := "run " + strings.Join(a.Command, " ")
		if a.Path != "" {
			line += p.dim.Render("  cleans " + p.Path(a.Path))
		}
		if a.NeedsSudo {
			line = "you run: sudo " + strings.Join(a.Command, " ")
		}
		return line
	case finding.ActionDockerRemoveContainer, finding.ActionDockerRemoveImage, finding.ActionDockerRemoveVolume:
		return "docker remove " + a.Label()
	case finding.ActionDockerPruneBuildCache:
		return "docker prune " + a.Label()
	default:
		return string(a.Action) + " " + a.Label()
	}
}

// Outcome prints one line of apply progress: the position, the result, the
// bytes freed and what the action did, with the error below a failure. The
// result is done, gone (the target no longer existed), failed, manual (needs
// sudo, printed instead of run) or skipped (an earlier failure stopped the run).
func (p *Printer) Outcome(index, total int, o apply.Outcome) {
	width := len(strconv.Itoa(total))
	prefix := fmt.Sprintf("[%*d/%d] ", width, index+1, total)
	var status, freed string
	switch o.Status {
	case apply.StatusDone:
		status = p.tierA.Render("done")
		freed = units.FormatSize(o.Freed)
		if o.Estimated {
			freed = "~" + freed
		}
	case apply.StatusGone:
		status = p.dim.Render("gone")
	case apply.StatusManual:
		status = p.warn.Render("manual")
	case apply.StatusFailed:
		status = p.tierC.Render("failed")
	default:
		status = p.dim.Render("skipped")
	}
	p.println(prefix + pad(status, 7) + fmt.Sprintf(" %10s", freed) + "  " + p.ActionLine(&o.Action))
	if o.Err != nil {
		p.println(strings.Repeat(" ", len(prefix)) + p.tierC.Render(o.Err.Error()))
	}
}

// Dim renders secondary text.
func (p *Printer) Dim(s string) string { return p.dim.Render(s) }

// Bold renders emphasized text.
func (p *Printer) Bold(s string) string { return p.bold.Render(s) }

// Warn renders warning text.
func (p *Printer) Warn(s string) string { return p.warn.Render(s) }
