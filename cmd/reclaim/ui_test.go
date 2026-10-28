package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
	"github.com/mandloideep/reclaim/internal/tui"
	"github.com/mandloideep/reclaim/internal/units"
)

// driver runs the interactive screen without a terminal: keys go to
// Update, the commands it returns run in order, and what the backend sent
// through Send reaches Update before the message of the command that sent
// it, as it does in a running program.
type driver struct {
	t    *testing.T
	m    *tui.Model
	mu   sync.Mutex
	sent []tea.Msg
	quit bool
	// beforeDone runs once, after the messages the backend sent have
	// arrived and before the message of the command that sent them, which
	// is when the Scanning screen shows a scan's progress.
	beforeDone func()
}

func newDriver(t *testing.T, a *app, args []string, f uiFlags) *driver {
	t.Helper()
	return newDriverWith(t, a, args, f, 140, 40, func(*tui.Options) {})
}

// newDriverWith builds the screen over the real backend, lets edit change
// its options, and sizes the window.
func newDriverWith(t *testing.T, a *app, args []string, f uiFlags, width, height int, edit func(*tui.Options)) *driver {
	t.Helper()
	d := &driver{t: t}
	opts, err := a.uiOptions(context.Background(), args, f, func(string) bool { return false }, d.send)
	require.NoError(t, err)
	edit(&opts)
	d.m = tui.New(opts)
	d.m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return d
}

func (d *driver) send(msg tea.Msg) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, msg)
}

func (d *driver) key(keys ...string) {
	d.t.Helper()
	for _, k := range keys {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "home":
			msg = tea.KeyMsg{Type: tea.KeyHome}
		}
		d.update(msg)
	}
}

// moveTo moves the cursor of the Review screen to the first row containing
// s.
func (d *driver) moveTo(s string) {
	d.t.Helper()
	d.key("home")
	for range 200 {
		if strings.Contains(d.cursorRow(), s) {
			return
		}
		d.key("down")
	}
	d.t.Fatalf("no row %q in\n%s", s, d.m.View())
}

// sgr matches the color codes of a styled view.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// cursorRow is the line of the view that holds the cursor, without colors.
func (d *driver) cursorRow() string {
	for l := range strings.SplitSeq(sgr.ReplaceAllString(d.m.View(), ""), "\n") {
		if strings.HasPrefix(l, "> ") {
			return l
		}
	}
	return ""
}

func (d *driver) update(msg tea.Msg) {
	d.t.Helper()
	_, cmd := d.m.Update(msg)
	d.run(cmd)
}

func (d *driver) run(cmd tea.Cmd) {
	d.t.Helper()
	if cmd == nil || d.quit {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
	case tea.QuitMsg:
		d.quit = true
	default:
		// The cursor's blink, the spinner and the clock tick forever in a
		// running program; here they are dropped so the drive ends.
		if name := fmt.Sprintf("%T", msg); strings.HasPrefix(name, "cursor.") || strings.HasPrefix(name, "spinner.") || name == "tui.tickMsg" {
			return
		}
		d.mu.Lock()
		sent := d.sent
		d.sent = nil
		d.mu.Unlock()
		for _, s := range sent {
			d.update(s)
		}
		if d.beforeDone != nil {
			d.beforeDone()
			d.beforeDone = nil
		}
		d.update(msg)
	}
}

func TestUIRefusesWithoutATerminal(t *testing.T) {
	a, out := testApp(t, "")
	err := execute(a, "ui")
	require.EqualError(t, err, notTerminal)
	require.Contains(t, err.Error(), "use reclaim scan, reclaim select and reclaim apply instead")
	require.Empty(t, out.String())

	// Bare reclaim prints the help instead, with ui listed first.
	require.NoError(t, execute(a))
	help := out.String()
	require.Contains(t, help, "reclaim                   in a terminal, the interactive screen")
	ui := strings.Index(help, "Interactive:\n  ui ")
	require.Positive(t, ui)
	require.Less(t, ui, strings.Index(help, "  scan "), "ui comes before every other command")
	require.Less(t, ui, strings.Index(help, "  apply "))
}

func TestUIFlagsAreChecked(t *testing.T) {
	a, _ := testApp(t, "")
	a.interactive = func() bool { return true }
	for _, args := range [][]string{
		{"ui", "--min-size", "lots"},
		{"ui", "--stale", "soon"},
		{"ui", "--depth", "-1"},
		{"ui", "--category", "nope"},
		{"ui", "--docker-label", "=x"},
		{"ui", filepath.Join(a.home, "missing")},
	} {
		require.Error(t, execute(a, args...), args)
	}
}

// TestUIProgramQuits runs the real program over a reader instead of a
// terminal: q quits from the Start screen, nothing is scanned, and the
// summary says nothing was removed.
func TestUIProgramQuits(t *testing.T) {
	a, out := testApp(t, "q")
	a.interactive = func() bool { return true }
	require.NoError(t, execute(a, "ui"))
	require.True(t, strings.HasSuffix(out.String(), "Nothing was removed.\n"), out.String())
	_, err := os.Stat(a.lastReportPath())
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestUIScreenOverTheFixtureMachine drives the screen with the real backend
// over the screenshot machine: it scans with progress, reviews, and saves a
// plan under the reclaim folder with c, and nothing on the machine changes.
func TestUIScreenOverTheFixtureMachine(t *testing.T) {
	a, out := screenshotApp(t)
	before := snapshot(t, a.home)
	d := newDriver(t, a, nil, uiFlags{minSize: defaultMinSize, out: "plan.json"})
	view := d.m.View()
	require.Contains(t, view, "> Whole machine   ~/Code, ~/Downloads")
	d.key("enter")

	require.Contains(t, d.m.View(), "Review")
	// Open the repository of the worktrees.
	d.moveTo("~/Code/storefront  1 removable")
	d.key("enter")
	view = d.m.View()
	for _, want := range []string{
		"[-] ▾ Project artifacts",
		"~/Code/storefront  1 removable, 1 pushed, 1 work",
		"claude-code  fix/checkout-rounding  squash, PR #41 merged",
		"claude-code  feat/gift-cards  pushed, PR #44 open",
		"codex  detached a5e1d0c9  12 modified, 3 untracked",
	} {
		require.Contains(t, view, want)
	}
	// The totals are the ones scan reports.
	scanned, err := loadReport(a.lastReportPath())
	require.NoError(t, err)
	require.Contains(t, view, units.FormatSize(scanned.TotalSize())+" reclaimable")
	require.Equal(t, "scan", scanned.Scope.Command)
	require.Contains(t, scanned.Scope.Args, "--min-size=10MB")

	d.key("c")
	require.Contains(t, d.m.View(), "Type yes to remove these")
	d.key("q")
	require.True(t, d.quit, "quitting on Confirm removes nothing")

	res := d.m.Result()
	require.Equal(t, tui.OutcomePlanSaved, res.Outcome)
	require.Equal(t, filepath.Join(a.stateDir, "plans", "plan-20261005T093000Z.json"), res.Plan.Path)
	saved, err := plan.Load(res.Plan.Path)
	require.NoError(t, err)
	want := make([]string, 0, len(scanned.Findings))
	for _, f := range plan.SelectTiers(scanned.Findings, []finding.Tier{finding.TierA}) {
		want = append(want, f.Target)
	}
	got := make([]string, 0, len(saved.Actions))
	for _, act := range saved.Actions {
		got = append(got, act.Target)
	}
	require.ElementsMatch(t, want, got, "the plan holds the preselected tier A findings")
	require.Empty(t, res.Plan.LeftOut, "the scripted git answers pass checks 1 to 5 of the proof")

	require.NoError(t, a.uiSummary(context.Background(), res, "plan.json"))
	summary := out.String()
	require.Contains(t, summary, "Plan written to "+res.Plan.Path+": ")
	require.Contains(t, summary, "Nothing was removed.")
	require.Contains(t, summary, "Review it, then run: reclaim apply "+res.Plan.Path)

	after := snapshot(t, a.home)
	require.Equal(t, before, after, "the screen changed nothing on the machine")

	// select still reads the report the screen saved.
	out.Reset()
	planPath := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, execute(a, "select", "--preset", "safe", "--out", planPath))
	require.Contains(t, out.String(), "Report from reclaim scan --min-size=10MB")
	require.FileExists(t, planPath)
}

// TestUIBackendScan checks the scans the Start screen asks for: progress
// events, the modes, a folder typed with ~, the ticked categories, and a
// stopped scan that returns what it found without saving it.
func TestUIBackendScan(t *testing.T) {
	a, _ := screenshotApp(t)
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	var mu sync.Mutex
	var events []scan.Event
	record := func(ev scan.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}

	r, err := be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeMachine, Categories: finding.Categories(), MinSize: 10_000_000, GitHub: true}, record)
	require.NoError(t, err)
	require.NotEmpty(t, r.Findings)
	require.Equal(t, scan.Planned, events[0].Kind, "Docker is available in the fixture, so the plan comes first")
	kinds := map[scan.EventKind]int{}
	walked := false
	for _, ev := range events {
		kinds[ev.Kind]++
		if ev.Kind == scan.Step && ev.Scanner == "" {
			walked = true
			require.Equal(t, finding.CategoryProject, ev.Category)
		}
	}
	require.True(t, walked, "the project walk reports its progress")
	require.Equal(t, kinds[scan.ScannerStarted], kinds[scan.ScannerFinished])
	require.True(t, slices.ContainsFunc(events, func(ev scan.Event) bool {
		return ev.Kind == scan.Step && ev.Category == finding.CategoryWorktree && strings.HasPrefix(ev.Text, "asking GitHub about ")
	}), "the worktree scanner says it asks GitHub")
	require.True(t, slices.ContainsFunc(events, func(ev scan.Event) bool {
		return ev.Kind == scan.Step && ev.Scanner == "docker" && ev.Text == "listing containers"
	}))
	require.FileExists(t, a.lastReportPath())

	// A folder typed with ~ scans project artifacts and worktrees under it.
	r, err = be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{"~/Code/storefront"},
		Categories: []finding.Category{finding.CategoryProject, finding.CategoryWorktree}, MinSize: 0}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(a.home, "Code", "storefront")}, r.Roots)
	for _, f := range r.Findings {
		require.Contains(t, []finding.Category{finding.CategoryProject, finding.CategoryWorktree}, f.Category)
	}
	require.True(t, r.Scope.Partial())

	// Worktrees lists the ones in use too, whatever their size.
	r, err = be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeWorktrees}, nil)
	require.NoError(t, err)
	require.Equal(t, "worktrees", r.Scope.Command)
	require.True(t, slices.ContainsFunc(r.Findings, func(f finding.Finding) bool {
		return f.Worktree != nil && f.Worktree.Verdict == finding.VerdictInUse
	}))

	// A stopped scan returns what it has, says so, and is not saved.
	require.NoError(t, os.Remove(a.lastReportPath()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err = be.Scan(ctx, tui.ScanRequest{Mode: tui.ModeMachine, Categories: finding.Categories()}, nil)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(r.Warnings, func(w finding.Warning) bool { return strings.HasPrefix(w.Message, "scan interrupted") }))
	_, err = os.Stat(a.lastReportPath())
	require.ErrorIs(t, err, os.ErrNotExist)

	// A folder that does not exist is an error the Start screen shows.
	_, err = be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{"~/nope"}, Categories: finding.Categories()}, nil)
	require.Error(t, err)
}

// TestUIBackendScannersRespectTheConfig checks that a ticked category runs
// only the scanners the configuration leaves enabled, and all of them when
// every one is disabled and the user ticked it anyway.
func TestUIBackendScannersRespectTheConfig(t *testing.T) {
	a, _ := testApp(t, "")
	writeTree(t, filepath.Dir(a.configPath), map[string]string{"config.toml": "[scanners]\ndisable = [\"ollama\", \"docker\"]\n"})
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	all := scanners.New().All()
	got, err := be.scanners(all, tui.ScanRequest{Categories: []finding.Category{finding.CategoryAppCache, finding.CategoryDocker}})
	require.NoError(t, err)
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name())
	}
	require.NotContains(t, names, "ollama", "a disabled scanner of a ticked category stays off")
	require.Contains(t, names, "user-caches")
	require.Contains(t, names, "docker", "a category whose scanners are all disabled runs when ticked")

	be.categories = []string{"node"}
	got, err = be.scanners(all, tui.ScanRequest{Categories: []finding.Category{finding.CategoryProject}})
	require.NoError(t, err)
	for _, s := range got {
		require.Equal(t, "node", s.Ecosystem(), "--category picks the scanners of the categories it touches")
	}
}

func TestUIStartPrefill(t *testing.T) {
	a, _ := testApp(t, "")
	writeTree(t, filepath.Dir(a.configPath), map[string]string{"config.toml": "min_size = \"50MB\"\nstale = \"30d\"\n[scanners]\ndisable = [\"docker\"]\n[worktrees]\ngithub = false\n"})
	writeTree(t, a.home, map[string]string{"Code/app/package.json": "{}"})
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	o, err := a.uiStart(cfg, []string{filepath.Join(a.home, "Code", "app")}, uiFlags{minSize: defaultMinSize}, func(string) bool { return false })
	require.NoError(t, err)
	require.Equal(t, tui.ModeFolder, o.Mode)
	require.Equal(t, int64(50_000_000), o.MinSize, "the configuration's minimum size wins over the flag default")
	require.Equal(t, 30*24*time.Hour, o.Stale)
	require.False(t, o.GitHub)
	require.Equal(t, []string{filepath.Join(a.home, "Code")}, o.Roots)
	for _, c := range o.Categories {
		if c.Category == finding.CategoryDocker {
			require.False(t, c.On)
			require.Equal(t, "disabled in config", c.Note)
		} else {
			require.True(t, c.On, c.Category)
		}
	}
}

func TestUISavePlanNamesAndProof(t *testing.T) {
	a, f, w := worktreeApp(t)
	a.now = func() time.Time { return time.Date(2026, 10, 27, 15, 30, 12, 0, time.UTC) }
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	r, err := be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{f.Dir},
		Categories: []finding.Category{finding.CategoryWorktree}}, nil)
	require.NoError(t, err)
	before := snapshot(t, f.Dir)
	var selected []finding.Finding
	for _, fd := range r.Findings {
		if fd.Actionable() {
			selected = append(selected, fd)
		}
	}
	require.Len(t, selected, 2, "the merged and the pushed worktree")
	// A file appears in the merged worktree after the scan, so the proof
	// leaves it out of the plan and says why.
	writeTree(t, w["merged"], map[string]string{"late.txt": "written after the scan\n"})

	saved, err := be.SavePlan(context.Background(), r, selected)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(a.stateDir, "plans", "plan-20261027T153012Z.json"), saved.Path)
	require.Len(t, saved.LeftOut, 1)
	require.Contains(t, saved.LeftOut[0], w["merged"])
	loaded, err := plan.Load(saved.Path)
	require.NoError(t, err)
	require.Len(t, loaded.Actions, 1)
	require.Equal(t, w["pushed"], loaded.Actions[0].Path)

	byPath := func(path string) []finding.Finding {
		return slices.DeleteFunc(slices.Clone(selected), func(fd finding.Finding) bool { return fd.Path != path })
	}
	// A second plan in the same second gets its own name.
	again, err := be.SavePlan(context.Background(), r, byPath(w["pushed"]))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(a.stateDir, "plans", "plan-20261027T153012Z-2.json"), again.Path)

	// Only the late file is new, besides the objects the merge checks write
	// into the repositories: saving plans removes nothing.
	late := filepath.ToSlash(filepath.Join(strings.TrimPrefix(w["merged"], f.Dir+"/"), "late.txt"))
	require.Equal(t, sortedStrings(append(withoutObjects(before), late)), withoutObjects(snapshot(t, f.Dir)))

	// Nothing left once every selected worktree fails the proof.
	empty, err := be.SavePlan(context.Background(), r, byPath(w["merged"]))
	require.NoError(t, err)
	require.Empty(t, empty.Path)
	require.Len(t, empty.LeftOut, 1)
}

// withoutObjects drops the git object stores from a snapshot: the merge
// checks write dangling objects there and change nothing else.
func withoutObjects(paths []string) []string {
	return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return strings.Contains(p, ".git/objects") })
}

func sortedStrings(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

// TestUIStepsMatchTheCompletion keeps the values m and s cycle through on
// the Start screen equal to the values the shell completes, so the screen
// and the shell agree.
func TestUIStepsMatchTheCompletion(t *testing.T) {
	sizes := make([]int64, 0, len(sizeChoices()))
	for _, c := range sizeChoices() {
		n, err := units.ParseSize(c.value)
		require.NoError(t, err)
		sizes = append(sizes, n)
	}
	require.Equal(t, sizes, tui.MinSizeSteps())
	stales := append(make([]time.Duration, 0, len(staleChoices())+1), 0)
	for _, c := range staleChoices() {
		d, err := units.ParseAge(c.value)
		require.NoError(t, err)
		stales = append(stales, d)
	}
	require.Equal(t, stales, tui.StaleSteps())
}

func TestUISummaryWhenQuitting(t *testing.T) {
	a, out := testApp(t, "")
	require.NoError(t, a.uiSummary(context.Background(), tui.Result{Outcome: tui.OutcomeQuit, Scanned: true}, "plan.json"))
	require.Equal(t, "Nothing was removed. The report is saved, so reclaim select picks from it.\n", out.String())
	out.Reset()
	err := a.uiSummary(context.Background(), tui.Result{Outcome: tui.OutcomeWrite, Report: &finding.Report{Version: 1}}, "plan.json")
	require.NoError(t, err)
	require.Equal(t, "Nothing selected, no plan written.\n", out.String())
}

// TestUIScopeNamesWhatRan checks that the saved report says which scanners
// ran, so select's provenance line does not misstate what it covers.
func TestUIScopeNamesWhatRan(t *testing.T) {
	a, _ := testApp(t, "")
	writeTree(t, filepath.Dir(a.configPath), map[string]string{"config.toml": "[scanners]\ndisable = [\"ollama\"]\n"})
	writeTree(t, a.home, map[string]string{"Code/app/package.json": "{}"})
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	scopeOf := func(req tui.ScanRequest) []string {
		t.Helper()
		r, err := be.Scan(context.Background(), req, nil)
		require.NoError(t, err)
		return r.Scope.Args
	}
	require.Equal(t, []string{"--min-size=10MB"}, scopeOf(tui.ScanRequest{Categories: finding.Categories(), MinSize: 10_000_000}),
		"every category the configuration leaves enabled is a plain scan")
	require.Equal(t, []string{"--category=project", "--min-size=0"}, scopeOf(tui.ScanRequest{Categories: []finding.Category{finding.CategoryProject}}))

	be.categories = []string{"node"}
	args := scopeOf(tui.ScanRequest{Categories: []finding.Category{finding.CategoryProject}})
	require.Len(t, args, 2)
	require.True(t, strings.HasPrefix(args[0], "--category=node_modules"), args[0])
	require.NotContains(t, args[0], "project", "only the node scanners ran")

	be.categories = nil
	require.Empty(t, scopeOf(tui.ScanRequest{Mode: tui.ModeWorktrees, Depth: 2}), "worktrees takes no flags here")
}
