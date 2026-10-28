package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/ui"
	"github.com/mandloideep/reclaim/internal/units"
)

// Options configure the interactive screen.
type Options struct {
	// Context is the parent of every scan the screen starts; canceling it
	// stops them.
	Context context.Context
	// Backend reaches the machine.
	Backend Backend
	// Send delivers a message to the running program from another
	// goroutine. cmd/reclaim passes the program's Send, which the scan uses
	// to report progress while it runs.
	Send func(tea.Msg)
	// Home shortens paths to start with "~".
	Home string
	// Version is shown on the Start screen.
	Version string
	// Now returns the current time. Nil uses time.Now.
	Now func() time.Time
	// Renderer styles the output. Nil uses lipgloss's default renderer.
	Renderer *lipgloss.Renderer
	// TickEvery is how often the Scanning screen refreshes its elapsed
	// time. Zero refreshes it only when the scan reports progress.
	TickEvery time.Duration
	// Start prefills the Start screen.
	Start StartOptions
	// LastReport is the report the last scan saved, nil when there is none.
	// o on the Start screen opens it.
	LastReport *finding.Report
	// OutPath is the plan file w writes, shown on the Review screen.
	OutPath string
}

// StartOptions prefill the Start screen from the command line and the
// configuration.
type StartOptions struct {
	// Mode is the mode selected at first.
	Mode Mode
	// Folder is the folder "This folder" scans when no paths were given,
	// the working directory.
	Folder string
	// Paths are the folders given on the command line, which select "This
	// folder".
	Paths []string
	// Roots are the roots of a whole machine or worktrees scan.
	Roots []string
	// Categories are the scanner categories, in report order, with whether
	// they start ticked.
	Categories []CategoryChoice
	// MinSize, Stale, GitHub, Depth, All and DockerLabels are the values of
	// the scan flags or the configuration.
	MinSize      int64
	Stale        time.Duration
	GitHub       bool
	Depth        int
	All          bool
	DockerLabels []string
}

// CategoryChoice is one category of the scanner row of the Start screen.
type CategoryChoice struct {
	// Category is the category.
	Category finding.Category
	// On reports whether it starts ticked.
	On bool
	// Note says why it starts unticked, such as "disabled in config".
	Note string
}

// Outcome says how the screen ended.
type Outcome int

// Outcomes.
const (
	// OutcomeQuit means the user quit without writing a plan.
	OutcomeQuit Outcome = iota
	// OutcomeWrite means the user asked for the plan to be written to the
	// --out path, as select writes it, which the caller does after the
	// screen closes.
	OutcomeWrite
	// OutcomePlanSaved means the screen saved a plan under the reclaim
	// folder and nothing was applied.
	OutcomePlanSaved
	// OutcomeApplied means the plan was applied after yes was typed.
	OutcomeApplied
)

// Result is what the screen leaves for the caller to print once the
// alternate screen has closed.
type Result struct {
	// Outcome says how the screen ended.
	Outcome Outcome
	// Report is the report on the Review screen, nil when none was opened.
	Report *finding.Report
	// Selected are the selected findings, for OutcomeWrite.
	Selected []finding.Finding
	// Plan is the last plan the screen saved, for OutcomePlanSaved and
	// OutcomeApplied.
	Plan SavedPlan
	// Summary is the result of applying the plan, for OutcomeApplied.
	Summary apply.Summary
	// Scanned reports whether a scan finished and was saved as the last
	// report.
	Scanned bool
}

type screenID int

const (
	screenStart screenID = iota
	screenScanning
	screenReview
	screenConfirm
	screenRemoving
	screenDone
)

type overlayID int

const (
	overlayNone overlayID = iota
	overlayHelp
	overlayProtected
)

// Messages the screens exchange with the goroutines of the program. seq
// ties a message to the scan that sent it, so the messages of a stopped
// scan do not reach the next one.
type (
	scanEventMsg struct {
		seq   int
		event scan.Event
	}
	scanDoneMsg struct {
		seq    int
		report *finding.Report
		err    error
	}
	tickMsg struct {
		seq int
	}
	planSavedMsg struct {
		plan SavedPlan
		err  error
	}
	applyBeginMsg struct {
		seq     int
		logPath string
	}
	applyStartingMsg struct {
		seq, index int
	}
	applyFinishedMsg struct {
		seq, index int
		outcome    apply.Outcome
	}
	applyDoneMsg struct {
		seq     int
		dryRun  bool
		summary apply.Summary
		err     error
	}
)

// frame is what a screen shows inside the common frame: a title with the
// screen name, the key facts on the right, the content and the footer with
// the keys that matter on that screen.
type frame struct {
	title, facts, body string
	footer             []string
}

// Model is the root model of the interactive screen. It owns the screens
// and switches between them: Start, Scanning, Review, Confirm, Removing and
// Done. It removes nothing itself: the scan goes through Backend.Scan, the
// plan through Backend.SavePlan, and the plan file SavePlan wrote through
// Backend.Apply, only after yes is typed on the Confirm screen.
type Model struct {
	opts   Options
	plain  *ui.Printer
	st     styles
	width  int
	height int

	screen   screenID
	start    *startScreen
	scanning *scanningScreen
	review   *reviewScreen
	confirm  *confirmScreen
	removing *removingScreen

	overlay   overlayID
	viewport  viewport.Model
	protected []apply.ProtectedEntry

	lastReport *finding.Report
	seq        int
	cancel     context.CancelFunc
	result     Result
}

var _ tea.Model = (*Model)(nil)

// New returns the root model on the Start screen.
func New(opts Options) *Model {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Renderer == nil {
		opts.Renderer = lipgloss.DefaultRenderer()
	}
	if opts.Send == nil {
		opts.Send = func(tea.Msg) {}
	}
	m := &Model{
		opts:       opts,
		st:         newStyles(opts.Renderer),
		width:      100,
		height:     30,
		lastReport: opts.LastReport,
		plain:      ui.NewPlain(nil, opts.Home, opts.Now()),
	}
	m.start = newStart(m)
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Result reports how the screen ended, for the caller to print once the
// alternate screen has closed.
func (m *Model) Result() Result { return m.result }

// printer formats paths, ages and plan actions without colors, with ages
// measured from now.
func (m *Model) printer() *ui.Printer {
	return ui.NewPlain(nil, m.opts.Home, m.opts.Now())
}

// path shortens a path under the home directory to start with "~".
func (m *Model) path(p string) string { return m.plain.Path(p) }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width, m.viewport.Height = m.width, m.bodyHeight(1)
		return m, nil
	case tea.KeyMsg:
		return m, m.key(msg)
	case scanEventMsg:
		if m.screen == screenScanning && msg.seq == m.seq {
			m.scanning.event(msg.event)
		}
		return m, nil
	case tickMsg:
		if (m.screen == screenScanning || m.screen == screenRemoving) && msg.seq == m.seq {
			return m, m.tick()
		}
		return m, nil
	case scanDoneMsg:
		if msg.seq == m.seq {
			m.scanDone(msg)
		}
		return m, nil
	case planSavedMsg:
		if m.screen == screenReview {
			m.review.planSaved(msg)
		}
		return m, nil
	case applyBeginMsg, applyStartingMsg, applyFinishedMsg, applyDoneMsg:
		m.applyMsg(msg)
		return m, nil
	case spinner.TickMsg:
		if m.screen == screenRemoving {
			var cmd tea.Cmd
			m.removing.spinner, cmd = m.removing.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

// applyMsg follows a run of the plan: the dry run on the Confirm screen or
// the real one on the Removing screen.
func (m *Model) applyMsg(msg tea.Msg) {
	switch msg := msg.(type) {
	case applyBeginMsg:
		if msg.seq == m.seq && m.removing != nil && m.screen == screenRemoving {
			m.removing.logPath = msg.logPath
		}
	case applyStartingMsg:
		if msg.seq == m.seq && m.screen == screenRemoving {
			m.removing.starting(msg.index)
		}
	case applyFinishedMsg:
		switch {
		case msg.seq != m.seq:
		case m.screen == screenRemoving:
			m.removing.finished(msg.index, msg.outcome)
		case m.screen == screenConfirm:
			m.confirm.finished(msg.index, msg.outcome)
		}
	case applyDoneMsg:
		if msg.seq != m.seq {
			return
		}
		m.cancel = nil
		if msg.dryRun {
			m.confirm.dryRunDone(msg.summary, msg.err)
			return
		}
		m.removing.done(msg.summary, msg.err)
		m.result.Outcome, m.result.Summary = OutcomeApplied, msg.summary
		m.screen = screenDone
	}
}

// key handles a key: the overlays first, then a text input that wants every
// key, then the keys of every screen, then the screen's own.
func (m *Model) key(msg tea.KeyMsg) tea.Cmd {
	// While the plan is being saved no key acts, not even q, so the screen
	// never ends with a plan written that nobody was told about.
	if m.screen == screenReview && m.review.saving {
		return nil
	}
	// While the plan is removed, q does nothing and ctrl+c stops after the
	// current action, so no action is ever cut off half done.
	if m.screen == screenRemoving {
		return m.removing.key(msg)
	}
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if m.overlay != overlayNone {
		switch msg.String() {
		case "esc", "q", "?", "P":
			m.overlay = overlayNone
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return cmd
		}
		return nil
	}
	if m.typing() {
		return m.screenKey(msg)
	}
	switch msg.String() {
	case "?":
		m.viewport = viewport.New(m.width, m.bodyHeight(1))
		m.viewport.SetContent(m.helpText())
		m.overlay = overlayHelp
		return nil
	case "P":
		m.showProtected()
		return nil
	case "q":
		return m.quit()
	}
	return m.screenKey(msg)
}

// typing reports whether a text input on the current screen takes every
// key, so that "q" or "?" are typed rather than acted on.
func (m *Model) typing() bool {
	switch m.screen {
	case screenStart:
		return m.start.editing
	case screenReview:
		return m.review.tree.Filtering()
	case screenConfirm:
		return m.confirm.typing()
	}
	return false
}

func (m *Model) screenKey(msg tea.KeyMsg) tea.Cmd {
	switch m.screen {
	case screenStart:
		return m.start.key(msg)
	case screenScanning:
		return m.scanning.key(msg)
	case screenReview:
		return m.review.key(msg)
	case screenConfirm:
		return m.confirm.key(msg)
	case screenDone:
		if msg.String() == "r" {
			// A second pass often finds more: images freed by a removed
			// container, or a worktree whose pull request merged.
			m.start.status = ""
			m.screen = screenStart
		}
	}
	return nil
}

// quit stops a running scan or dry run and ends the program.
func (m *Model) quit() tea.Cmd {
	m.stopRun()
	if m.review != nil && m.result.Outcome == OutcomeQuit {
		m.result.Report = m.review.report
	}
	return tea.Quit
}

// stopRun cancels the scan or apply run in progress.
func (m *Model) stopRun() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// startScan opens the Scanning screen and runs the scan in the background.
// The scan reports progress through Options.Send and ends with a
// scanDoneMsg.
func (m *Model) startScan(req ScanRequest) tea.Cmd {
	m.stopRun()
	m.seq++
	seq := m.seq
	ctx, cancel := context.WithCancel(m.opts.Context)
	m.cancel = cancel
	m.scanning = newScanning(m, req)
	m.screen = screenScanning
	backend, send := m.opts.Backend, m.opts.Send
	run := func() tea.Msg {
		r, err := backend.Scan(ctx, req, func(ev scan.Event) { send(scanEventMsg{seq: seq, event: ev}) })
		return scanDoneMsg{seq: seq, report: r, err: err}
	}
	return tea.Batch(run, m.tick())
}

// tick refreshes the elapsed time of the Scanning screen.
func (m *Model) tick() tea.Cmd {
	if m.opts.TickEvery <= 0 {
		return nil
	}
	seq := m.seq
	return tea.Tick(m.opts.TickEvery, func(time.Time) tea.Msg { return tickMsg{seq: seq} })
}

// scanDone opens the Review screen on the report, or goes back to Start
// with the error.
func (m *Model) scanDone(msg scanDoneMsg) {
	interrupted := msg.report != nil && scan.Interrupted(msg.report.Warnings)
	m.stopRun()
	if msg.err != nil || msg.report == nil {
		m.screen = screenStart
		text := "the scan returned no report"
		if msg.err != nil {
			text = msg.err.Error()
		}
		m.start.status = "Scan failed: " + text
		return
	}
	if !interrupted {
		m.lastReport = msg.report
		m.result.Scanned = true
	}
	m.openReview(msg.report, interrupted)
}

// openReview shows the Review screen on a report.
func (m *Model) openReview(r *finding.Report, interrupted bool) {
	m.review = newReview(m, r, interrupted)
	m.screen = screenReview
}

// showProtected opens the protected list for the roots of the current
// screen: the roots a scan would use on Start, the report's elsewhere.
func (m *Model) showProtected() {
	var roots []string
	switch m.screen {
	case screenStart, screenScanning:
		roots = m.start.roots()
	case screenReview:
		roots = m.review.report.Roots
	default:
		roots = m.confirm.saved.Plan.Roots
	}
	m.protected = m.opts.Backend.Protected(roots)
	m.viewport = viewport.New(m.width, m.bodyHeight(1))
	m.viewport.SetContent(m.protectedText())
	m.overlay = overlayProtected
}

// bodyHeight is the number of lines between the title, with the blank line
// under it, and a footer of that many lines.
func (m *Model) bodyHeight(footer int) int { return max(m.height-2-footer, 3) }

// View implements tea.Model.
func (m *Model) View() string {
	var f frame
	switch m.screen {
	case screenStart:
		f = m.start.view()
	case screenScanning:
		f = m.scanning.view()
	case screenReview:
		f = m.review.view()
	case screenConfirm:
		f = m.confirm.view()
	case screenRemoving:
		f = m.removing.view()
	case screenDone:
		f = m.removing.doneView()
	}
	switch m.overlay {
	case overlayHelp:
		f.body = m.viewport.View()
		f.footer = m.footer(m.st.keys("up/down", "scroll", "esc", "close"))
	case overlayProtected:
		f.title = "Never removed"
		f.facts = "rendered by apply from the tables its checks use"
		f.body = m.viewport.View()
		f.footer = m.footer(m.st.keys("up/down", "scroll", "esc", "close"))
	}
	return m.frame(f)
}

// frame lays out a screen: the title with the facts on the right, a blank
// line, the body cut or padded to fit, and the footer.
func (m *Model) frame(f frame) string {
	cut := lipgloss.NewStyle().MaxWidth(m.width)
	title := m.st.title.Render(f.title)
	if f.facts != "" {
		// The facts sit on the right, cut to the room the title leaves.
		room := m.width - lipgloss.Width(title) - 3
		if room > 3 {
			title += padLeft(m.st.dim.Render(cutTo(f.facts, room)), m.width-lipgloss.Width(title))
		}
	}
	lines := []string{cut.Render(title), ""}
	body := strings.Split(f.body, "\n")
	h := m.bodyHeight(len(f.footer))
	if len(body) > h {
		body = body[:h]
	}
	for _, l := range body {
		lines = append(lines, cut.Render(l))
	}
	for len(lines) < m.height-len(f.footer) {
		lines = append(lines, "")
	}
	for _, l := range f.footer {
		lines = append(lines, cut.Render(l))
	}
	return strings.Join(lines, "\n")
}

// footer lays out the keys of a screen in as many lines as the window
// needs, so a narrow window still shows every key.
func (m *Model) footer(parts []string) []string {
	return ui.Pack(parts, "  ", m.width)
}

// closing is the sentence every help overlay ends with.
const closing = "Nothing is removed until you type yes on the Confirm screen, and P shows what reclaim never removes."

// helpText lists every key of the current screen.
func (m *Model) helpText() string {
	var keys [][2]string
	switch m.screen {
	case screenStart:
		keys = startHelp()
	case screenScanning:
		keys = scanningHelp()
	case screenReview:
		keys = reviewHelp()
	case screenConfirm:
		keys = confirmHelp()
	case screenRemoving:
		keys = removingHelp()
	case screenDone:
		keys = doneHelp()
	}
	keys = append(keys, [2]string{"P", "what reclaim never removes"}, [2]string{"?", "this help"}, [2]string{"q", "quit"})
	var b strings.Builder
	b.WriteString(m.st.bold.Render("Keys") + "\n\n")
	for _, k := range keys {
		// The description wraps under itself, so a narrow window cuts
		// nothing.
		for i, l := range wrapText(k[1], m.width-16) {
			key := strings.Repeat(" ", 14)
			if i == 0 {
				key = pad(m.st.key.Render(k[0]), 14)
			}
			b.WriteString("  " + key + l + "\n")
		}
	}
	b.WriteString("\n")
	if m.screen == screenReview {
		for _, l := range wrapText("For an agent at the end of a task: reclaim skill prints the skill that tells it to run reclaim done; done and skill stay on the command line.", m.width-2) {
			b.WriteString("  " + l + "\n")
		}
		b.WriteString("\n")
	}
	for _, l := range wrapText(closing, m.width-2) {
		b.WriteString("  " + m.st.bold.Render(l) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// sizeText formats a size.
func sizeText(n int64) string { return units.FormatSize(n) }

// ago says how long ago t was, such as "3h ago" or "just now".
func (m *Model) ago(t time.Time) string {
	age := units.FormatAge(m.opts.Now().Sub(t))
	if age == "now" {
		return "just now"
	}
	return age + " ago"
}

// protectedText renders the protected list with the same code reclaim
// apply --dry-run prints it with.
func (m *Model) protectedText() string {
	p := ui.NewWithRenderer(nil, m.opts.Renderer, m.opts.Home, m.opts.Now())
	return strings.Join(p.ProtectedLines(m.protected, m.width), "\n")
}

// openConfirm shows the Confirm screen on the plan SavePlan wrote.
func (m *Model) openConfirm(saved SavedPlan) {
	m.result.Plan = saved
	m.confirm = newConfirm(m, saved)
	m.screen = screenConfirm
}

// startApply runs the plan file at path through Backend.Apply in the
// background, a dry run or the real one, reporting progress through
// Options.Send and ending with an applyDoneMsg.
func (m *Model) startApply(path string, dryRun bool) tea.Cmd {
	m.stopRun()
	m.seq++
	seq := m.seq
	ctx, cancel := context.WithCancel(m.opts.Context)
	m.cancel = cancel
	backend, send := m.opts.Backend, m.opts.Send
	progress := apply.Progress{
		Begin:    func(logPath string) { send(applyBeginMsg{seq: seq, logPath: logPath}) },
		Starting: func(i int, _ plan.Action) { send(applyStartingMsg{seq: seq, index: i}) },
		Finished: func(i int, o apply.Outcome) { send(applyFinishedMsg{seq: seq, index: i, outcome: o}) },
	}
	run := func() tea.Msg {
		sum, err := backend.Apply(ctx, path, dryRun, progress)
		return applyDoneMsg{seq: seq, dryRun: dryRun, summary: sum, err: err}
	}
	return run
}

// startRemoving opens the Removing screen and applies the plan file, after
// yes was typed on the Confirm screen.
func (m *Model) startRemoving(saved SavedPlan) tea.Cmd {
	m.removing = newRemoving(m, saved)
	m.screen = screenRemoving
	run := m.startApply(saved.Path, false)
	return tea.Batch(run, m.tick(), m.removing.spinner.Tick)
}
