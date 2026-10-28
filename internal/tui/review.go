package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/ui"
	"github.com/mandloideep/reclaim/internal/units"
)

// wideDetails is the narrowest window that shows the details pane to the
// right of the list; narrower windows show it below.
const wideDetails = 120

// reviewScreen is the findings tree of select with the details pane: the
// user ticks what goes, reads everything reclaim knows about the row under
// the cursor, and continues to a plan.
type reviewScreen struct {
	m           *Model
	report      *finding.Report
	tree        *ui.Tree
	interrupted bool
	showDetails bool
	// detailOffset is how far J has scrolled the details pane; it starts
	// again at the top when the cursor moves.
	detailOffset int
	status       string
	saving       bool
}

func newReview(m *Model, r *finding.Report, interrupted bool) *reviewScreen {
	return &reviewScreen{
		m:           m,
		report:      r,
		tree:        ui.NewTree(r, ui.TreeOptions{Home: m.opts.Home, Renderer: m.opts.Renderer, Now: m.opts.Now(), Warnings: true}),
		interrupted: interrupted,
		showDetails: true,
	}
}

func reviewHelp() [][2]string {
	return [][2]string{
		{"up/down", "move; pgup, pgdown, home and end jump"},
		{"space", "tick or untick; a group ticks its tier A and B items, never tier C"},
		{"enter", "open or close a group; right and left too"},
		{"/", "filter by name, path, scanner or project; esc clears it"},
		{"t", "cycle the tier filter: all, A, B, C"},
		{"s", "cycle the sort: size, name, oldest first"},
		{"a", "select every visible tier A item"},
		{"n", "select nothing"},
		{"i", "show or hide the details"},
		{"J/K", "scroll the details down and up, one line each"},
		{"w", "write the plan to the --out file and quit, to read or edit it first"},
		{"c", "continue to Confirm: save the plan, read it, check it and type yes"},
	}
}

func (s *reviewScreen) key(msg tea.KeyMsg) tea.Cmd {
	if s.saving {
		return nil
	}
	s.status = ""
	if handled, cmd := s.tree.HandleKey(msg); handled {
		s.detailOffset = 0
		return cmd
	}
	switch msg.String() {
	case "n":
		n := s.tree.SelectNone()
		s.status = "cleared " + plural(n, "item", "items")
	case "J":
		s.detailOffset++
	case "K":
		s.detailOffset = max(s.detailOffset-1, 0)
	case "i":
		s.showDetails = !s.showDetails
	case "w":
		sel := s.tree.Selected()
		if len(sel) == 0 {
			s.status = "Nothing is selected, so there is no plan to write."
			return nil
		}
		s.m.result = Result{Outcome: OutcomeWrite, Report: s.report, Selected: sel, Scanned: s.m.result.Scanned}
		return s.m.quit()
	case "c":
		sel := s.tree.Selected()
		if len(sel) == 0 {
			s.status = "Nothing is selected; tick what should go first."
			return nil
		}
		if age := s.m.opts.Now().Sub(s.report.Created); age > plan.MaxAge {
			// apply refuses a plan whose scan is this old, because the disk
			// may have changed, so the screen says so before anything else.
			s.status = "This scan is " + units.FormatAge(age) + " old and apply refuses plans from scans older than " + units.FormatAge(plan.MaxAge) + "; scan again from Start."
			return nil
		}
		s.saving = true
		s.status = "Saving the plan and proving the selected worktrees..."
		backend, ctx, report := s.m.opts.Backend, s.m.opts.Context, s.report
		return func() tea.Msg {
			p, err := backend.SavePlan(ctx, report, sel)
			return planSavedMsg{plan: p, err: err}
		}
	}
	return nil
}

// planSaved ends the screen once the plan is saved, or says why it was not.
func (s *reviewScreen) planSaved(msg planSavedMsg) {
	s.saving = false
	switch {
	case msg.err != nil:
		s.status = "The plan was not saved: " + msg.err.Error()
		return
	case msg.plan.Path == "":
		s.status = "Nothing is left to remove: " + strings.Join(msg.plan.LeftOut, "; ")
		return
	}
	s.m.result.Outcome, s.m.result.Report = OutcomePlanSaved, s.report
	s.m.openConfirm(msg.plan)
}

// layout returns the size of the list and of the details pane, which sits
// to the right in a wide window and below in a narrow one.
func (s *reviewScreen) layout(listHeight int) (listW, listH, detailW, detailH int, beside bool) {
	w := s.m.width
	if !s.showDetails {
		return w, listHeight, 0, 0, false
	}
	if w >= wideDetails {
		detailW = min(max(w/3, 36), 56)
		return w - detailW - 3, listHeight, detailW, listHeight, true
	}
	detailH = min(max(listHeight/3, 4), 10)
	return w, max(listHeight-detailH-1, 3), w - 2, detailH, false
}

func (s *reviewScreen) view() frame {
	st := s.m.st
	var head []string
	head = append(head, s.tree.FilterLine())
	if s.interrupted {
		for _, l := range wrapText("The scan was stopped before it finished: these are the findings so far, and the warnings say what was not scanned.", s.m.width) {
			head = append(head, st.warn.Render(l))
		}
	}
	if c := s.m.plain.Coverage(s.report); c != "" {
		for _, l := range wrapText(c, s.m.width) {
			head = append(head, st.dim.Render(l))
		}
	}
	footer := s.m.footer(st.keys("space", "toggle", "enter", "open", "/", "filter", "t", "tier", "s", "sort", "a", "all tier A",
		"n", "none", "i", "details", "w", "save plan", "c", "continue", "?", "help"))
	if s.tree.Filtering() {
		footer = s.m.footer(st.keys("enter", "keep the filter", "esc", "clear it"))
	}
	// The body is the header, a blank line, the list and details, a status
	// line and the selection line.
	listHeight := max(s.m.bodyHeight(len(footer))-len(head)-3, 3)
	listW, listH, detailW, detailH, beside := s.layout(listHeight)
	s.tree.SetSize(listW, listH)
	list := strings.Split(s.tree.View(), "\n")

	var body []string
	body = append(body, head...)
	body = append(body, "")
	switch {
	case !s.showDetails:
		body = append(body, list...)
	case beside:
		pane := s.pane(detailW, detailH)
		for i := range listH {
			body = append(body, pad(lipgloss.NewStyle().MaxWidth(listW).Render(list[i]), listW)+" "+st.dim.Render("│")+" "+pane[i])
		}
	default:
		body = append(body, list...)
		body = append(body, st.dim.Render(strings.Repeat("─", s.m.width)))
		body = append(body, s.pane(detailW, detailH)...)
	}
	status := s.status
	if status == "" {
		status = s.tree.Status()
	}
	if status != "" {
		status = st.warn.Render(status)
	}
	body = append(body, status)
	n, size := s.tree.SelectedSize()
	out := s.m.opts.OutPath
	if out == "" {
		out = "plan.json"
	}
	body = append(body, st.bold.Render(fmt.Sprintf("Selected: %s, %s", plural(n, "item", "items"), sizeText(size)))+
		st.dim.Render("   w writes "+out+"   c continues"))
	return frame{title: "Review", facts: s.m.printer().Summary(s.report), body: strings.Join(body, "\n"), footer: footer}
}

// pane renders the details of the row under the cursor, wrapped to width
// and cut or padded to height lines.
func (s *reviewScreen) pane(width, height int) []string {
	st := s.m.st
	var out []string
	for _, l := range s.m.details(s.report, s.tree.Current()) {
		for _, w := range wrapText(l.text, width) {
			switch l.style {
			case boldLine:
				w = st.bold.Render(w)
			case dimLine:
				w = st.dim.Render(w)
			case warnLine:
				w = st.warn.Render(w)
			}
			out = append(out, w)
		}
	}
	// J and K scroll; the last line says there is more and how to see it.
	s.detailOffset = min(s.detailOffset, max(len(out)-height, 0))
	out = out[s.detailOffset:]
	if len(out) > height {
		out = out[:height]
		if height > 0 {
			out[height-1] = st.dim.Render("... J scrolls down")
		}
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}
