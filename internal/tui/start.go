package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/ui"
	"github.com/mandloideep/reclaim/internal/units"
)

// MinSizeSteps are the minimum sizes m cycles through on the Start screen,
// the same values the shell completes for --min-size, so the screen and the
// shell agree.
func MinSizeSteps() []int64 {
	const mb, gb = 1_000_000, 1_000_000_000
	return []int64{0, 10 * mb, 50 * mb, 100 * mb, gb}
}

// StaleSteps are the stale windows s cycles through on the Start screen,
// off and the values the shell completes for --stale.
func StaleSteps() []time.Duration {
	day := 24 * time.Hour
	return []time.Duration{0, 30 * day, 90 * day, 180 * day, 365 * day}
}

// rootCategories are the categories a folder scan looks at without --all.
func rootCategories() []finding.Category {
	return []finding.Category{finding.CategoryProject, finding.CategoryWorktree}
}

// startScreen chooses what to scan: the whole machine, a folder or the
// worktrees, which scanner categories run, the minimum size, the stale
// window and whether GitHub is asked about pull requests.
type startScreen struct {
	m *Model

	mode      Mode
	catCursor int
	cats      []CategoryChoice
	// on holds the ticked categories of the whole machine and of the
	// folder mode, which start differently: a folder scan looks at project
	// artifacts and worktrees unless --all is given.
	on      map[Mode]map[finding.Category]bool
	paths   []string
	minSize int64
	stale   time.Duration
	github  bool

	editing bool
	// edited is the line the folder input started with.
	edited string
	input  textinput.Model
	status string

	// listing shows the ignore list, with listCursor on an entry; asking
	// is the entry d would take off while its confirmation waits for y,
	// and writing is set while the list is written.
	listing    bool
	listCursor int
	// listOffset is the first entry shown when the list is longer than
	// the window.
	listOffset int
	asking     string
	writing    bool
}

func newStart(m *Model) *startScreen {
	o := m.opts.Start
	s := &startScreen{
		m:       m,
		mode:    o.Mode,
		cats:    o.Categories,
		minSize: o.MinSize,
		stale:   o.Stale,
		github:  o.GitHub,
		paths:   o.Paths,
		on:      map[Mode]map[finding.Category]bool{ModeMachine: {}, ModeFolder: {}},
	}
	if len(s.paths) > 0 {
		s.mode = ModeFolder
	}
	if len(s.paths) == 0 && o.Folder != "" {
		s.paths = []string{o.Folder}
	}
	for _, c := range s.cats {
		s.on[ModeMachine][c.Category] = c.On
		s.on[ModeFolder][c.Category] = c.On && (o.All || slices.Contains(rootCategories(), c.Category))
	}
	in := textinput.New()
	in.Prompt = "folder: "
	in.Placeholder = "~/Code/app"
	ui.StyleInput(&in, m.opts.Renderer)
	s.input = in
	return s
}

func startHelp() [][2]string {
	return [][2]string{
		{"up/down", "choose what to look at"},
		{"left/right", "move along the scanner row"},
		{"space", "tick or untick a scanner category"},
		{"enter", "scan"},
		{"m", "cycle the minimum size: 0, 10 MB, 50 MB, 100 MB, 1 GB"},
		{"s", "cycle the stale filter: off, 30 days, 90 days, 180 days, a year"},
		{"g", "ask GitHub about pull requests, or keep the scan offline"},
		{"p", "type the folder This folder scans"},
		{"o", "open the last report without scanning"},
		{"x", "show the ignore list: what is never offered again and apply refuses; d takes an entry off"},
	}
}

// ticked returns the categories the current mode will scan.
func (s *startScreen) ticked() []finding.Category {
	if s.mode == ModeWorktrees {
		return []finding.Category{finding.CategoryWorktree}
	}
	var out []finding.Category
	for _, c := range s.cats {
		if s.on[s.mode][c.Category] {
			out = append(out, c.Category)
		}
	}
	return out
}

// request is the scan the screen describes.
func (s *startScreen) request() ScanRequest {
	o := s.m.opts.Start
	req := ScanRequest{
		Mode: s.mode, Categories: s.ticked(), MinSize: s.minSize, Stale: s.stale, Depth: o.Depth,
		DockerLabels: o.DockerLabels, GitHub: s.github,
	}
	if s.mode == ModeFolder {
		req.Paths = s.paths
		req.All = slices.ContainsFunc(req.Categories, func(c finding.Category) bool { return !slices.Contains(rootCategories(), c) })
	}
	if s.mode == ModeWorktrees {
		// Worktrees are listed whatever their size, as reclaim worktrees
		// lists them.
		req.MinSize = 0
	}
	return req
}

// roots are the roots the scan would record, for the protected list.
func (s *startScreen) roots() []string {
	req := s.request()
	roots := s.m.opts.Start.Roots
	if s.mode == ModeFolder {
		roots = s.paths
	}
	out := slices.Clone(roots)
	if s.m.opts.Home != "" && (s.mode != ModeFolder || req.All) {
		out = append(out, s.m.opts.Home)
	}
	return out
}

func (s *startScreen) key(msg tea.KeyMsg) tea.Cmd {
	if s.editing {
		return s.editKey(msg)
	}
	if s.listing {
		return s.listKey(msg)
	}
	s.status = ""
	switch msg.String() {
	case "up", "k":
		s.moveCursor(-1)
	case "down", "j":
		s.moveCursor(1)
	case "left", "h":
		s.catCursor = max(s.catCursor-1, 0)
	case "right", "l":
		s.catCursor = max(min(s.catCursor+1, len(s.cats)-1), 0)
	case " ":
		s.toggle()
	case "enter":
		return s.scan()
	case "m":
		s.minSize = nextStep(MinSizeSteps(), s.minSize)
	case "s":
		s.stale = nextStep(StaleSteps(), s.stale)
	case "g":
		s.github = !s.github
	case "p":
		s.mode = ModeFolder
		s.editing = true
		// Several folders from the command line show as one line; enter
		// keeps them unless the line was changed to one folder.
		s.edited = ""
		if len(s.paths) > 0 {
			s.edited = s.folderText()
		}
		s.input.SetValue(s.edited)
		s.input.CursorEnd()
		return s.input.Focus()
	case "x":
		s.listing, s.listCursor = true, 0
	case "o":
		if s.m.lastReport == nil {
			s.status = "There is no last report yet; enter scans."
			return nil
		}
		s.m.openReview(s.m.lastReport, s.m.lastFolder, false)
	}
	return nil
}

// listKey handles the keys of the ignore list: moving, d with its one
// line confirmation, and esc or x back to the Start screen.
func (s *startScreen) listKey(msg tea.KeyMsg) tea.Cmd {
	if s.writing {
		return nil
	}
	if s.asking != "" {
		entry := s.asking
		s.asking = ""
		if msg.String() != "y" {
			s.status = "Kept on the ignore list."
			return nil
		}
		s.writing = true
		s.status = "Taking it off the ignore list..."
		return s.m.changeIgnore(nil, []string{entry})
	}
	s.status = ""
	switch msg.String() {
	case "up", "k":
		s.listCursor = max(s.listCursor-1, 0)
	case "down", "j":
		s.listCursor = max(min(s.listCursor+1, len(s.m.ignore)-1), 0)
	case "d":
		if len(s.m.ignore) == 0 {
			s.status = "There is nothing to take off; x on the Review screen adds to the list."
			return nil
		}
		s.asking = s.m.ignore[s.listCursor]
		s.status = "Take " + s.m.path(s.asking) + " off the ignore list, so scans offer it again? y takes it off, any other key keeps it."
	case "esc", "x":
		s.listing = false
	}
	return nil
}

// ignored ends a change of the ignore list made from it.
func (s *startScreen) ignored(msg ignoredMsg) {
	s.writing = false
	if msg.err != nil {
		s.status = "The ignore list was not written: " + msg.err.Error()
		return
	}
	s.listCursor = max(min(s.listCursor, len(s.m.ignore)-1), 0)
	if len(msg.dropped) > 0 {
		s.status = "Took " + s.m.path(msg.dropped[0]) + " off the ignore list; the next scan offers it again."
	}
}

// listView shows the ignore list with the cursor on an entry.
func (s *startScreen) listView() frame {
	st := s.m.st
	var b []string
	b = append(b, "  "+st.bold.Render("Ignore list")+"   "+st.dim.Render(s.m.path(s.m.opts.IgnorePath)))
	for _, l := range wrapText("Never offered again, and apply refuses them whatever a plan says. x on the Review screen adds to it; "+
		"the file can be edited by hand too.", s.m.width-2) {
		b = append(b, "  "+st.dim.Render(l))
	}
	b = append(b, "")
	var tail []string
	if s.status != "" {
		tail = append(tail, "")
		for _, l := range wrapText(s.status, s.m.width-2) {
			tail = append(tail, "  "+st.warn.Render(l))
		}
	}
	footer := s.m.footer(st.keys("up/down", "move", "d", "take off the list", "esc", "back", "?", "help", "q", "quit"))
	if len(s.m.ignore) == 0 {
		b = append(b, "  The ignore list is empty.")
	}
	// A long list scrolls with the cursor; the last line says where it is.
	entries := s.m.ignore
	room := max(s.m.bodyHeight(len(footer))-len(b)-len(tail), 1)
	scrolls := len(entries) > room
	if scrolls {
		room = max(room-1, 1)
		s.listOffset = min(max(s.listOffset, s.listCursor-room+1), s.listCursor, len(entries)-room)
	} else {
		s.listOffset = 0
	}
	shown := entries[s.listOffset:min(s.listOffset+room, len(entries))]
	for i, e := range shown {
		marker := "    "
		if s.listOffset+i == s.listCursor {
			marker = "  " + st.cursor.Render(">") + " "
		}
		b = append(b, marker+s.m.path(e))
	}
	if scrolls {
		b = append(b, st.dim.Render(fmt.Sprintf("    entries %d to %d of %d; up and down scroll", s.listOffset+1, s.listOffset+len(shown), len(entries))))
	}
	b = append(b, tail...)
	return frame{title: "reclaim", facts: s.m.opts.Version, body: strings.Join(b, "\n"), footer: footer}
}

func (s *startScreen) editKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEnter:
		s.editing = false
		s.input.Blur()
		if v := strings.TrimSpace(s.input.Value()); v != "" && v != s.edited {
			s.paths = []string{v}
		}
		return nil
	case tea.KeyEsc:
		s.editing = false
		s.input.Blur()
		return nil
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return cmd
}

// moveCursor chooses the mode above or below.
func (s *startScreen) moveCursor(delta int) {
	s.mode = Mode(min(max(int(s.mode)+delta, 0), int(ModeWorktrees)))
}

// toggle ticks or unticks the category under the cursor of the scanner
// row, which left and right move. In worktrees mode the row is fixed.
func (s *startScreen) toggle() {
	if len(s.cats) == 0 {
		return
	}
	if s.mode == ModeWorktrees {
		s.status = "Worktrees runs the worktree scanners alone."
		return
	}
	c := s.cats[s.catCursor].Category
	s.on[s.mode][c] = !s.on[s.mode][c]
}

func (s *startScreen) scan() tea.Cmd {
	req := s.request()
	if len(req.Categories) == 0 {
		s.status = "Tick at least one scanner category first."
		return nil
	}
	if s.mode == ModeFolder && len(req.Paths) == 0 {
		s.status = "Press p to type the folder to scan."
		return nil
	}
	return s.m.startScan(req)
}

// nextStep returns the first step larger than v, or the first step after
// the last, so a value from a flag that is not a step continues from where
// it fits.
func nextStep[T int64 | time.Duration](steps []T, v T) T {
	for _, s := range steps {
		if s > v {
			return s
		}
	}
	return steps[0]
}

func (s *startScreen) view() frame {
	if s.listing {
		return s.listView()
	}
	st := s.m.st
	var b strings.Builder
	line := func(l string) { b.WriteString(l + "\n") }

	line("  " + st.bold.Render("What to look at"))
	roots := s.shortRoots()
	modes := []struct{ name, what string }{
		{"Whole machine", strings.Join(append(slices.Clone(roots), s.machineExtras()...), ", ")},
		{"This folder", s.folderText()},
		{"Worktrees", "linked git worktrees of the repositories under " + orNone(strings.Join(roots, ", ")) + ", and Codex worktrees"},
	}
	for i, md := range modes {
		marker := "    "
		if Mode(i) == s.mode {
			marker = "  > "
		}
		name := pad(md.name, 16)
		if Mode(i) == s.mode {
			name = st.bold.Render(md.name) + strings.Repeat(" ", 16-len(md.name))
		}
		if i == int(ModeFolder) && s.editing {
			line(marker + name + s.input.View())
			continue
		}
		// A long description wraps under itself, so a narrow window
		// shows all of it.
		for k, l := range wrapText(md.what, s.m.width-20) {
			if k > 0 {
				marker, name = "    ", strings.Repeat(" ", 16)
			}
			line(marker + name + st.dim.Render(l))
		}
	}
	line("")
	indent := strings.Repeat(" ", 13)
	for k, l := range ui.Pack(s.scannerRow(), "  ", s.m.width-13) {
		if k == 0 {
			line("  " + pad("Scanners", 11) + l)
		} else {
			line(indent + l)
		}
	}
	if note := s.scannerNote(); note != "" {
		for _, l := range wrapText(note, s.m.width-13) {
			line(indent + st.dim.Render(l))
		}
	}
	for _, l := range ui.Pack(s.settings(), "     ", s.m.width-2) {
		line("  " + l)
	}
	line("")
	last := "No report yet."
	if r := s.m.lastReport; r != nil {
		last = "Last report: " + s.m.printer().Summary(r) + ".  o opens it"
	}
	for _, l := range wrapText(last, s.m.width-2) {
		line("  " + st.dim.Render(l))
	}
	for _, l := range wrapText("Never removed: your home folder, ~/Documents, ~/Desktop, project roots, .git folders, anything outside the scanned roots.  P shows the full list", s.m.width-2) {
		line("  " + l)
	}
	if s.status != "" {
		line("")
		for _, l := range wrapText(s.status, s.m.width-2) {
			line("  " + st.warn.Render(l))
		}
	}
	footer := s.m.footer(st.keys("enter", "scan", "space", "toggle", "m", "min size", "s", "stale", "g", "github", "p", "path",
		"o", "open last report", "x", "ignored", "P", "protected", "?", "help", "q", "quit"))
	if s.editing {
		footer = s.m.footer(st.keys("enter", "use this folder", "esc", "keep the old one"))
	}
	return frame{title: "reclaim", facts: s.m.opts.Version, body: strings.TrimSuffix(b.String(), "\n"), footer: footer}
}

func orNone(s string) string {
	if s == "" {
		return "the default roots, none of which exist"
	}
	return s
}

func (s *startScreen) shortRoots() []string {
	out := make([]string, 0, len(s.m.opts.Start.Roots))
	for _, r := range s.m.opts.Start.Roots {
		out = append(out, s.m.path(r))
	}
	return out
}

// machineExtras names what a whole machine scan looks at besides the roots.
func (s *startScreen) machineExtras() []string {
	var out []string
	for _, c := range s.cats {
		if !s.on[ModeMachine][c.Category] {
			continue
		}
		switch c.Category {
		case finding.CategoryWorktree:
			out = append(out, "worktrees")
		case finding.CategoryPackageCache:
			out = append(out, "package caches")
		case finding.CategoryDocker:
			out = append(out, "Docker")
		case finding.CategoryAppCache:
			out = append(out, "app caches")
		case finding.CategoryDownloads:
			// ~/Downloads is usually a root already, named first.
			if !slices.Contains(s.shortRoots(), "~/Downloads") {
				out = append(out, "~/Downloads")
			}
		}
	}
	return out
}

func (s *startScreen) folderText() string {
	if len(s.paths) == 0 {
		return "p types the folder"
	}
	short := make([]string, len(s.paths))
	for i, p := range s.paths {
		short[i] = s.m.path(p)
	}
	return strings.Join(short, ", ")
}

// scannerRow ticks the categories of the current mode, one item each, with
// the one under the cursor of the row highlighted.
func (s *startScreen) scannerRow() []string {
	st := s.m.st
	if s.mode == ModeWorktrees {
		return []string{"[x] " + finding.CategoryWorktree.Title()}
	}
	parts := make([]string, 0, len(s.cats))
	for i, c := range s.cats {
		box := "[ ]"
		if s.on[s.mode][c.Category] {
			box = "[x]"
		}
		label := c.Category.Title()
		if i == s.catCursor {
			label = st.cursor.Render(label)
		}
		item := box + " " + label
		if c.Note != "" {
			item += st.dim.Render(" (" + c.Note + ")")
		}
		parts = append(parts, item)
	}
	return parts
}

func (s *startScreen) scannerNote() string {
	switch s.mode {
	case ModeWorktrees:
		return "the worktree scanners alone, every size, with the worktrees in use listed too"
	case ModeFolder:
		return "project artifacts and worktrees in the folder; ticking more looks outside it too, as --all does"
	}
	return ""
}

// settings are the minimum size, the stale filter and the GitHub lookup.
func (s *startScreen) settings() []string {
	size := "Hide findings under " + units.FormatSize(s.minSize)
	if s.minSize == 0 {
		size = "Show findings of every size"
	}
	if s.mode == ModeWorktrees {
		size = "Show worktrees of every size"
	}
	stale := "off"
	if s.stale > 0 {
		stale = staleWords(s.stale)
	}
	github := "off"
	if s.github {
		github = "on"
	}
	return []string{size, "Stale projects only: " + stale, "Ask GitHub about pull requests: " + github,
		"Ignored: " + strconv.Itoa(len(s.m.ignore))}
}

func staleWords(d time.Duration) string {
	day := 24 * time.Hour
	switch {
	case d == 365*day:
		return "a year"
	case d%day == 0:
		return plural(int(d/day), "day", "days")
	default:
		return d.String()
	}
}
