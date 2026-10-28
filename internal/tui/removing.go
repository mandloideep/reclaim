package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/ui"
)

// removingScreen follows apply.Run on the plan file, one row per action,
// with the running action, the freed total, the bar and the disk line.
type removingScreen struct {
	m       *Model
	saved   SavedPlan
	began   time.Time
	ended   time.Time
	logPath string
	// outcomes are the finished actions by index, running the action that
	// runs now, -1 between actions.
	outcomes map[int]apply.Outcome
	running  int
	freed    int64
	// freeBefore and freeNow are the free bytes of the volume of the first
	// root, -1 when they cannot be read.
	freeBefore, freeNow int64
	stopping            bool
	spinner             spinner.Model
	summary             apply.Summary
	err                 error
}

func newRemoving(m *Model, saved SavedPlan) *removingScreen {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	s := &removingScreen{m: m, saved: saved, began: m.opts.Now(), outcomes: map[int]apply.Outcome{}, running: -1,
		spinner: sp}
	s.freeBefore = s.readFree()
	s.freeNow = s.freeBefore
	return s
}

func removingHelp() [][2]string {
	return [][2]string{{"ctrl+c", "stop after the current action; the rest are marked not run"}}
}

func doneHelp() [][2]string {
	return [][2]string{{"r", "scan again with the same options: a second pass often finds more"}}
}

// volume is the path whose volume the disk line reports: the first root.
func (s *removingScreen) volume() string {
	if len(s.saved.Plan.Roots) == 0 {
		return ""
	}
	return s.saved.Plan.Roots[0]
}

func (s *removingScreen) readFree() int64 {
	v := s.volume()
	if v == "" {
		return -1
	}
	n, err := s.m.opts.Backend.FreeSpace(v)
	if err != nil {
		return -1
	}
	return n
}

func (s *removingScreen) key(msg tea.KeyMsg) tea.Cmd {
	if msg.String() == "ctrl+c" && !s.stopping {
		// apply.Run checks the context between actions, so the current
		// action finishes and the rest are marked not run.
		s.stopping = true
		s.m.stopRun()
	}
	return nil
}

func (s *removingScreen) starting(i int) { s.running = i }

func (s *removingScreen) finished(i int, o apply.Outcome) {
	s.outcomes[i] = o
	s.freed += o.Freed
	if s.running == i {
		s.running = -1
	}
	s.freeNow = s.readFree()
}

func (s *removingScreen) done(sum apply.Summary, err error) {
	s.summary, s.err = sum, err
	s.ended = s.m.opts.Now()
	for i, o := range sum.Outcomes {
		s.outcomes[i] = o
	}
	s.freed = sum.Freed
	s.running = -1
	if sum.LogPath != "" {
		s.logPath = sum.LogPath
	}
	s.freeNow = s.readFree()
}

// statusWord is the word a row of the Removing screen shows for an outcome.
func statusWord(st styles, s apply.Status) string {
	word := pad(string(s), 9)
	switch s {
	case apply.StatusDone:
		return st.good.Render(word)
	case apply.StatusFailed:
		return st.bad.Render(word)
	case apply.StatusManual:
		return st.warn.Render(word)
	case apply.StatusNotRun:
		return st.dim.Render(pad("not run", 9))
	default:
		return st.dim.Render(word)
	}
}

// freedText renders bytes freed, with ~ for an estimate, as the outcome
// printer marks Docker sizes.
func freedText(o apply.Outcome) string {
	if o.Status != apply.StatusDone || o.Freed == 0 {
		return "-"
	}
	if o.Estimated {
		return "~" + sizeText(o.Freed)
	}
	return sizeText(o.Freed)
}

// diskLine compares the free space of the first root's volume before the
// run and now, and says when OrbStack returns Docker space later.
func (s *removingScreen) diskLine(before string) string {
	if s.freeBefore < 0 || s.freeNow < 0 {
		return ""
	}
	line := fmt.Sprintf("Disk free on the volume of %s: %s %s, %s now", s.m.path(s.volume()), sizeText(s.freeBefore), before, sizeText(s.freeNow))
	if s.orbStack() {
		line += "; OrbStack returns Docker's space to the disk later"
	}
	return line
}

func (s *removingScreen) orbStack() bool {
	r := s.m.lastReviewed()
	if r == nil {
		return false
	}
	for _, n := range r.Notes {
		if strings.Contains(n, "OrbStack") {
			return true
		}
	}
	return false
}

func (s *removingScreen) view() frame {
	st := s.m.st
	pl := s.saved.Plan
	p := ui.NewWithRenderer(nil, s.m.opts.Renderer, s.m.opts.Home, s.m.opts.Now())
	total := len(pl.Actions)
	planned := pl.TotalSize()
	pct := 0.0
	if planned > 0 {
		pct = min(float64(s.freed)/float64(planned), 1)
	}
	at := len(s.outcomes)
	if s.running >= 0 {
		at++
	}
	facts := fmt.Sprintf("freed %s of %s   %s %d%%   %s", sizeText(s.freed), sizeText(planned), bar(st, pct, 20), int(pct*100),
		clock(s.m.opts.Now().Sub(s.began)))
	var rows []string
	for i := range pl.Actions {
		o, ok := s.outcomes[i]
		if !ok {
			continue
		}
		rows = append(rows, "  "+statusWord(st, o.Status)+padLeft(freedText(o), 9)+"  "+p.ActionLine(&o.Action))
		if o.Err != nil && o.Status != apply.StatusNotRun {
			err := o.Err
			if o.Refusal != nil {
				err = errors.New("refused at " + o.Refusal.Check + ": " + o.Refusal.Detail)
			}
			rows = append(rows, s.m.errorLines(err, strings.Repeat(" ", 22))...)
		}
	}
	if s.running >= 0 && s.running < total {
		a := &pl.Actions[s.running]
		rows = append(rows, "  "+s.spinner.View()+" "+runningWords(p, a))
	}
	if waiting := total - at; waiting > 0 && s.err == nil {
		rows = append(rows, strings.Repeat(" ", 12)+st.dim.Render(plural(waiting, "more waiting", "more waiting")))
	}
	var tail []string
	if d := s.diskLine("when this started"); d != "" {
		tail = append(tail, "", d)
	}
	var footer []string
	switch {
	case s.stopping:
		footer = []string{st.warn.Render("Stopping after the current action; the rest are marked not run.")}
	default:
		footer = s.m.footer(st.keys("ctrl+c", "stops after the current action"))
	}
	if s.logPath != "" {
		footer = append(footer, st.dim.Render("log: "+s.m.path(s.logPath)))
	}
	room := max(s.m.bodyHeight(len(footer))-len(tail), 3)
	if len(rows) > room {
		// The newest rows matter most while it runs.
		rows = rows[len(rows)-room:]
	}
	rows = append(rows, tail...)
	body := rows
	return frame{title: fmt.Sprintf("Removing   %d of %d", max(at, 1), total), facts: facts, body: strings.Join(body, "\n"), footer: footer}
}

// runningWords describe the action that runs now and what it is expected
// to free.
func runningWords(p *ui.Printer, a *plan.Action) string {
	line := p.ActionLine(a)
	if a.Size > 0 {
		line += ", " + sizeText(a.Size) + " expected"
	}
	return line
}

// doneView is the Done screen: what was freed, what happened to every
// action, the disk, the commands that need administrator rights in full,
// the failures with their reasons, the log and the plan.
func (s *removingScreen) doneView() frame {
	st := s.m.st
	p := ui.NewWithRenderer(nil, s.m.opts.Renderer, s.m.opts.Home, s.m.opts.Now())
	var body []string
	body = append(body, countsLine(s.summary))
	if d := s.diskLine("before"); d != "" {
		body = append(body, d)
	}
	if sudo := sudoCommands(s.summary); len(sudo) > 0 {
		body = append(body, "", st.bold.Render("Needs administrator rights, run it yourself if you want it:"))
		for _, c := range sudo {
			body = append(body, "  "+c)
		}
	}
	var failed []string
	for _, o := range s.summary.Outcomes {
		if o.Status != apply.StatusFailed {
			continue
		}
		failed = append(failed, "  "+p.ActionLine(&o.Action))
		failed = append(failed, s.m.errorLines(o.Err, "    ")...)
	}
	if len(failed) > 0 {
		body = append(body, "", st.bold.Render("Failed:"))
		body = append(body, failed...)
	}
	if s.err != nil && strings.Contains(s.err.Error(), "interrupted") {
		body = append(body, "", st.warn.Render("Stopped with ctrl+c: the actions not run are still in the plan file."))
	}
	body = append(body, "")
	if s.summary.LogPath != "" {
		body = append(body, "Log: "+s.m.path(s.summary.LogPath))
	}
	body = append(body, "Plan: "+s.m.path(s.saved.Path))
	return frame{title: "Done", facts: "freed " + sizeText(s.summary.Freed) + " in " + clock(s.ended.Sub(s.began)),
		body: strings.Join(body, "\n"), footer: s.m.footer(st.keys("r", "scan again", "?", "help", "q", "quit"))}
}

// countsLine counts the outcomes in words: "14 removed, 1 already gone, 1
// failed, 1 needs you".
func countsLine(sum apply.Summary) string {
	counts := map[apply.Status]int{}
	for _, o := range sum.Outcomes {
		counts[o.Status]++
	}
	var parts []string
	for _, c := range []struct {
		status apply.Status
		words  string
	}{{apply.StatusDone, "removed"}, {apply.StatusGone, "already gone"}, {apply.StatusFailed, "failed"},
		{apply.StatusManual, "needs you"}, {apply.StatusNotRun, "not run"}} {
		if n := counts[c.status]; n > 0 {
			words := c.words
			if c.status == apply.StatusManual && n > 1 {
				words = "need you"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, words))
		}
	}
	if len(parts) == 0 {
		return "Nothing to do."
	}
	return strings.Join(parts, ", ")
}

// sudoCommands are the commands of the actions that need administrator
// rights, in full, for the user to run.
func sudoCommands(sum apply.Summary) []string {
	var out []string
	for _, o := range sum.Outcomes {
		if o.Status == apply.StatusManual {
			out = append(out, "sudo "+strings.Join(o.Action.Command, " "))
		}
	}
	return out
}

// errorLines renders an error under its row: every line of it, with the
// home folder shortened to ~, wrapped to the window after indent, in the
// warning color.
func (m *Model) errorLines(err error, indent string) []string {
	var out []string
	for l := range strings.SplitSeq(m.plain.ShortCommand(err.Error()), "\n") {
		for _, w := range wrapText(strings.TrimSpace(l), m.width-len(indent)-2) {
			out = append(out, indent+m.st.warn.Render(w))
		}
	}
	return out
}

// lastReviewed is the report on the Review screen.
func (m *Model) lastReviewed() *finding.Report {
	if m.review == nil {
		return nil
	}
	return m.review.report
}

// bar draws freed over planned in width cells.
func bar(st styles, pct float64, width int) string {
	filled := min(max(int(pct*float64(width)+0.5), 0), width)
	return st.good.Render(strings.Repeat("█", filled)) + st.dim.Render(strings.Repeat("░", width-filled))
}
