package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/ui"
)

// confirmScreen shows the plan SavePlan wrote, the plan printer's lines,
// the checks apply runs, and asks for the typed word yes. d runs every
// check without removing anything and marks each row with its result.
type confirmScreen struct {
	m      *Model
	saved  SavedPlan
	input  textinput.Model
	offset int
	status string

	// checking is set while the dry run runs; results are its outcomes by
	// action, and dryLog its log.
	checking bool
	results  map[int]apply.Outcome
	dryLog   string
}

func newConfirm(m *Model, saved SavedPlan) *confirmScreen {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 16
	in.Width = 12
	ui.StyleInput(&in, m.opts.Renderer)
	in.Focus()
	return &confirmScreen{m: m, saved: saved, input: in}
}

func confirmHelp() [][2]string {
	return [][2]string{
		{"yes, enter", "remove what the plan lists; the word must be typed in full"},
		{"d", "run every check of every action without removing anything, and mark each row with its result"},
		{"up/down", "scroll the plan; pgup and pgdown jump"},
		{"esc", "back to the Review screen with the selection as it was; the plan file stays on disk"},
	}
}

// typing reports whether the confirmation is being typed, when every key
// belongs to it.
func (s *confirmScreen) typing() bool { return s.input.Value() != "" }

func (s *confirmScreen) key(msg tea.KeyMsg) tea.Cmd {
	if s.checking {
		s.status = "The checks are running; wait for them to finish."
		return nil
	}
	s.status = ""
	switch msg.String() {
	case "up":
		s.offset = max(s.offset-1, 0)
		return nil
	case "down":
		s.offset++
		return nil
	case "pgup":
		s.offset = max(s.offset-10, 0)
		return nil
	case "pgdown":
		s.offset += 10
		return nil
	case "enter":
		if strings.TrimSpace(s.input.Value()) != "yes" {
			s.input.SetValue("")
			s.status = "Type yes, the whole word, to remove these; anything else removes nothing."
			return nil
		}
		return s.m.startRemoving(s.saved)
	case "esc":
		if s.input.Value() != "" {
			s.input.SetValue("")
			return nil
		}
		s.m.screen = screenReview
		return nil
	case "d":
		if s.input.Value() == "" {
			s.checking = true
			s.results = map[int]apply.Outcome{}
			return s.m.startApply(s.saved.Path, true)
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return cmd
}

// finished records the outcome of one action of the dry run.
func (s *confirmScreen) finished(i int, o apply.Outcome) {
	if s.results == nil {
		s.results = map[int]apply.Outcome{}
	}
	s.results[i] = o
}

// dryRunDone ends the dry run.
func (s *confirmScreen) dryRunDone(sum apply.Summary, err error) {
	s.checking = false
	s.dryLog = sum.LogPath
	for i, o := range sum.Outcomes {
		s.results[i] = o
	}
	counts := map[apply.Status]int{}
	for _, o := range sum.Outcomes {
		counts[o.Status]++
	}
	s.status = fmt.Sprintf("Checked without removing anything: %d ok, %d gone, %d for you to run, %d refused.",
		counts[apply.StatusChecked], counts[apply.StatusGone], counts[apply.StatusManual], counts[apply.StatusRefused])
	if err != nil && counts[apply.StatusRefused] == 0 {
		s.status = "The checks stopped: " + err.Error()
	}
}

// planLines are the plan printer's lines, with the warnings of tier C and
// worktree actions under their rows, and the result of the dry run on
// every row once it ran.
func (s *confirmScreen) planLines() []string {
	st := s.m.st
	p := ui.NewWithRenderer(nil, s.m.opts.Renderer, s.m.opts.Home, s.m.opts.Now())
	pl := s.saved.Plan
	total := len(pl.Actions)
	indent := ui.PlanIndent(total)
	if len(s.results) > 0 {
		indent = strings.Repeat(" ", 9) + indent
	}
	var out []string
	for i := range pl.Actions {
		a := &pl.Actions[i]
		line := p.PlanLine(i, total, a)
		if a.NeedsSudo {
			line += st.dim.Render("  printed, never run")
		}
		// After the dry run every row starts with its result, so a long
		// row never cuts it off.
		o, ran := s.results[i]
		if len(s.results) > 0 {
			word := ""
			if ran {
				word = resultWord(st, o.Status)
			}
			line = pad(word, 9) + line
		}
		out = append(out, line)
		if a.Warning != "" && (a.Tier == finding.TierC || a.Action == finding.ActionGitWorktreeRemove) {
			out = append(out, indent+st.warn.Render("! "+a.Warning))
		}
		if !ran {
			continue
		}
		if o.Err != nil {
			out = append(out, s.m.errorLines(o.Err, indent)...)
		}
		if a.Action == finding.ActionGitWorktreeRemove && len(o.Checks) > 0 {
			for _, l := range wrapText(checkNames(o.Checks), s.m.width-len(indent)-2) {
				out = append(out, indent+st.dim.Render(l))
			}
		}
	}
	return out
}

// resultWord is the mark the dry run leaves on a row: ok, gone, manual or
// refused.
func resultWord(st styles, s apply.Status) string {
	switch s {
	case apply.StatusChecked:
		return st.good.Render("ok")
	case apply.StatusGone:
		return st.dim.Render("gone")
	case apply.StatusManual:
		return st.warn.Render("manual")
	case apply.StatusRefused:
		return st.bad.Render("refused")
	default:
		return st.dim.Render(string(s))
	}
}

// checkNames lists the checks of an outcome by name with their results.
func checkNames(cs []apply.Check) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		mark := "ok"
		if !c.OK {
			mark = "failed"
		}
		parts[i] = c.Name + " " + mark
	}
	return strings.Join(parts, ", ")
}

// tierLine sums the plan by tier, "By tier: A 14.2 GB in 13 items, B 3.9 GB
// in 3 items.", counting an action inside another once.
func tierLine(pl *plan.Plan) string {
	var parts []string
	for _, t := range finding.Tiers() {
		sub := &plan.Plan{}
		for _, a := range pl.Actions {
			if a.Tier == t {
				sub.Actions = append(sub.Actions, a)
			}
		}
		if len(sub.Actions) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s in %s", t, sizeText(sub.TotalSize()), plural(len(sub.Actions), "item", "items")))
		}
	}
	return "By tier: " + strings.Join(parts, ", ") + "."
}

func (s *confirmScreen) view() frame {
	st := s.m.st
	pl := s.saved.Plan
	width := s.m.width
	var head []string
	head = append(head, "Plan written to "+s.m.path(s.saved.Path))
	for _, l := range s.saved.LeftOut {
		for _, w := range wrapText("Left out of the plan: "+l, width) {
			head = append(head, st.warn.Render(w))
		}
	}
	head = append(head, "")
	tail := make([]string, 0, 4)
	tail = append(tail, "", tierLine(pl))
	for _, l := range wrapText("Every target is checked again right before it is removed: inside a scanned root, not a protected folder, "+
		"not a project root, no symbolic link, still the same thing. Nothing is deleted until you confirm.", width) {
		tail = append(tail, st.dim.Render(l))
	}
	status := s.status
	if s.checking {
		status = fmt.Sprintf("Checking %d of %d without removing anything...", len(s.results), len(pl.Actions))
	}
	var notes []string
	for _, l := range wrapText(status, width) {
		notes = append(notes, st.warn.Render(l))
	}
	if s.dryLog != "" && !s.checking {
		// On its own line, so the path can be selected in one piece.
		notes = append(notes, st.dim.Render("Log: "+s.m.path(s.dryLog)))
	}
	prompt := st.bold.Render(fmt.Sprintf("Type yes to remove these %s (%s): ", plural(len(pl.Actions), "item", "items"), sizeText(pl.TotalSize()))) + s.input.View()

	footer := s.m.footer(st.keys("yes enter", "remove", "d", "checks everything without removing", "up/down", "scroll", "esc", "back", "?", "help"))
	// The prompt always shows, under the plan with at least two lines of
	// it. In a short window the explanations below the plan give way
	// first, when the plan would show fewer than six lines with them, then
	// the lines above it, then the notes over the prompt.
	height := s.m.bodyHeight(len(footer))
	bottom := append(append([]string{""}, notes...), prompt)
	lines := s.planLines()
	const minRoom = 2
	if len(head)+len(tail)+len(bottom)+min(len(lines), 6) > height {
		tail = nil
	}
	if len(head)+len(bottom)+minRoom > height {
		head = nil
	}
	for len(bottom) > 1 && len(bottom)+minRoom > height {
		bottom = bottom[1:]
	}
	room := max(height-len(head)-len(tail)-len(bottom), 1)
	scrolls := len(lines) > room
	if scrolls && room >= minRoom {
		// The last line of the room says where the list is.
		room--
	}
	if scrolls {
		s.offset = min(s.offset, len(lines)-room)
	} else {
		s.offset = 0
	}
	shown := append([]string(nil), lines[s.offset:min(s.offset+room, len(lines))]...)
	if scrolls && len(shown) < height-len(head)-len(tail)-len(bottom) {
		shown = append(shown, st.dim.Render(fmt.Sprintf("  lines %d to %d of %d; up and down scroll", s.offset+1, s.offset+len(shown), len(lines))))
	}
	body := append(append(head, shown...), tail...)
	body = append(body, bottom...)
	return frame{title: "Confirm", facts: plural(len(pl.Actions), "item", "items") + ", " + sizeText(pl.TotalSize()),
		body: strings.Join(body, "\n"), footer: footer}
}
