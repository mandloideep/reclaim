package tui

import (
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// rowState is what one category of a scan is doing.
type rowState int

const (
	rowWaiting rowState = iota
	rowRunning
	rowDone
	rowWarning
)

func (s rowState) String() string {
	switch s {
	case rowRunning:
		return "running"
	case rowDone:
		return "done"
	case rowWarning:
		return "warning"
	default:
		return "waiting"
	}
}

// categoryRow follows the scanners of one category through a scan.
type categoryRow struct {
	category finding.Category
	planned  []string
	started  map[string]bool
	finished map[string]bool
	errs     []string
	findings int
	size     int64
	// steps hold what each scanner last said it was doing, and stepped
	// the scanners in the order they last said something.
	steps   map[string]string
	stepped []string
	// walked is set once the shared project walk has reported on this row.
	walked            bool
	folders, projects int
	// measured says how far the measuring walk of "This folder" got, which
	// runs before the scanners.
	measured string
}

func (r *categoryRow) running() []string {
	var out []string
	for _, name := range r.planned {
		if r.started[name] && !r.finished[name] {
			out = append(out, name)
		}
	}
	for name := range r.started {
		if !r.finished[name] && !slices.Contains(r.planned, name) {
			out = append(out, name)
		}
	}
	return out
}

func (r *categoryRow) state() rowState {
	allDone := len(r.finished) > 0 && !slices.ContainsFunc(r.planned, func(name string) bool { return !r.finished[name] })
	switch {
	case allDone && len(r.running()) == 0 && len(r.errs) > 0:
		return rowWarning
	case allDone && len(r.running()) == 0:
		return rowDone
	case len(r.started) > 0 || len(r.finished) > 0 || r.walked || r.measured != "":
		return rowRunning
	default:
		return rowWaiting
	}
}

// scanningScreen shows one row per category while the scan runs, with what
// each is doing and what it has found so far.
type scanningScreen struct {
	m        *Model
	req      ScanRequest
	began    time.Time
	rows     map[finding.Category]*categoryRow
	stopping bool
}

func newScanning(m *Model, req ScanRequest) *scanningScreen {
	s := &scanningScreen{m: m, req: req, began: m.opts.Now(), rows: map[finding.Category]*categoryRow{}}
	for _, c := range req.Categories {
		s.row(c)
	}
	return s
}

func scanningHelp() [][2]string {
	return [][2]string{{"esc", "stop the scan and review what was found so far"}}
}

func (s *scanningScreen) row(c finding.Category) *categoryRow {
	r, ok := s.rows[c]
	if !ok {
		r = &categoryRow{category: c, started: map[string]bool{}, finished: map[string]bool{}, steps: map[string]string{}}
		s.rows[c] = r
	}
	return r
}

// event records one progress event of the scan.
func (s *scanningScreen) event(ev scan.Event) {
	switch ev.Kind {
	case scan.Planned:
		for _, p := range ev.Scanners {
			r := s.row(p.Category)
			if !slices.Contains(r.planned, p.Name) {
				r.planned = append(r.planned, p.Name)
			}
		}
	case scan.ScannerStarted:
		s.row(ev.Category).started[ev.Scanner] = true
	case scan.Step:
		r := s.row(ev.Category)
		switch {
		case ev.Scanner == "" && ev.Text != "":
			r.measured = ev.Text
			return
		case ev.Scanner == "":
			r.walked, r.folders, r.projects = true, ev.Folders, ev.Projects
			return
		}
		r.steps[ev.Scanner] = ev.Text
		r.stepped = append(slices.DeleteFunc(r.stepped, func(s string) bool { return s == ev.Scanner }), ev.Scanner)
	case scan.ScannerFinished:
		r := s.row(ev.Category)
		r.finished[ev.Scanner] = true
		r.findings += ev.Findings
		r.size += ev.Size
		if ev.Err != nil {
			r.errs = append(r.errs, ev.Scanner+": "+ev.Err.Error())
		}
	}
}

func (s *scanningScreen) key(msg tea.KeyMsg) tea.Cmd {
	if msg.String() == "esc" && !s.stopping {
		s.stopping = true
		// The scan returns what it found so far, which opens the Review
		// screen with the warning that the scan was interrupted.
		if s.m.cancel != nil {
			s.m.cancel()
		}
	}
	return nil
}

func (s *scanningScreen) title() string {
	switch s.req.Mode {
	case ModeFolder:
		short := make([]string, len(s.req.Paths))
		for i, p := range s.req.Paths {
			short[i] = s.m.path(p)
		}
		return "Scanning " + strings.Join(short, ", ")
	case ModeWorktrees:
		return "Scanning git worktrees"
	default:
		return "Scanning the whole machine"
	}
}

func (s *scanningScreen) view() frame {
	st := s.m.st
	var b strings.Builder
	var total int
	var size int64
	middle := max(s.m.width-2-10-20-26, 16)
	for _, c := range finding.Categories() {
		r, ok := s.rows[c]
		if !ok {
			continue
		}
		state := r.state()
		word := pad(state.String(), 10)
		switch state {
		case rowDone:
			word = st.good.Render(word)
		case rowWarning:
			word = st.warn.Render(word)
		case rowWaiting:
			word = st.dim.Render(word)
		}
		style := st.dim
		if state == rowWarning {
			style = st.warn
		}
		found := ""
		if r.findings > 0 || state == rowDone || state == rowWarning {
			found = padLeft(plural(r.findings, "finding", "findings"), 13) + padLeft(sizeText(r.size), 11)
		}
		// What the row is doing wraps under itself in a narrow window, up
		// to three lines, so the walk's counts are never cut.
		what := wrapText(s.rowText(r, state), middle)
		if len(what) > 3 {
			what = what[:3]
			what[2] = cutTo(what[2]+" ...", middle)
		}
		for k, l := range what {
			if k == 0 {
				b.WriteString("  " + word + pad(c.Title(), 20) + style.Render(pad(l, middle+2)) + found + "\n")
				continue
			}
			b.WriteString(strings.Repeat(" ", 32) + style.Render(l) + "\n")
		}
		total += r.findings
		size += r.size
	}
	b.WriteString("\n")
	b.WriteString("  " + st.bold.Render("Found so far: "+plural(total, "finding", "findings")+", "+sizeText(size)+" reclaimable"))
	footer := s.m.footer(st.keys("esc", "stops the scan and shows what was found so far"))
	if s.stopping {
		footer = []string{st.warn.Render("Stopping; the findings so far open as soon as the running scanners notice.")}
	}
	return frame{title: s.title(), facts: clock(s.m.opts.Now().Sub(s.began)) + " elapsed", body: b.String(), footer: footer}
}

// rowText says what a category is doing: the folders and projects the
// shared walk has covered, the step of a running scanner, the scanners
// running, or why it failed.
func (s *scanningScreen) rowText(r *categoryRow, state rowState) string {
	var parts []string
	switch {
	case r.walked:
		parts = append(parts, plural(r.projects, "project", "projects")+" in "+plural(r.folders, "folder", "folders"))
	case r.measured != "":
		parts = append(parts, r.measured)
	}
	switch state {
	case rowWarning:
		return strings.Join(append(parts, r.errs...), "; ")
	case rowRunning:
		running := r.running()
		// The step of the running scanner that spoke last.
		step := ""
		for _, name := range slices.Backward(r.stepped) {
			if slices.Contains(running, name) {
				step = r.steps[name]
				break
			}
		}
		switch {
		case step != "":
			parts = append(parts, step)
		case len(running) > 0 && !r.walked:
			slices.Sort(running)
			parts = append(parts, strings.Join(running, ", "))
		}
	}
	return strings.Join(parts, ", ")
}

// cutTo shortens s to width visible columns, ending with "...".
func cutTo(s string, width int) string {
	if len([]rune(s)) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:max(width-3, 0)]) + "..."
}
