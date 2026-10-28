package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/config"
	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
	"github.com/mandloideep/reclaim/internal/tui"
	"github.com/mandloideep/reclaim/internal/units"
)

// uiFlags are the flags of ui: the scan flags, which prefill the Start
// screen, and the plan file w writes.
type uiFlags struct {
	minSize      string
	categories   []string
	stale        string
	depth        int
	all          bool
	dockerLabels []string
	out          string
}

// notTerminal is the refusal of ui when standard input or output is not a
// terminal.
const notTerminal = "ui needs a terminal for both input and output; " +
	"use reclaim scan, reclaim select and reclaim apply instead, which work in scripts and pipes"

func newUICmd(a *app) *cobra.Command {
	var f uiFlags
	cmd := &cobra.Command{
		Use:     "ui [path...]",
		Short:   "The interactive way through scan, select and apply, one screen; reclaim alone opens it",
		GroupID: groupInteractive,
		Long: "ui opens one screen that walks through scan, select and apply: choose what to look at, watch the\n" +
			"scan, review what it found with everything reclaim knows about each item, and save a plan.\n" +
			"It takes the scan flags with the same meaning, which prefill the start screen: with paths it\n" +
			"looks at the project artifacts and worktrees under them, and --all adds the caches, Docker and\n" +
			"Downloads. Nothing is removed by the screen: c saves a plan for reclaim apply, and w writes it\n" +
			"to --out to read or edit first. reclaim with no arguments opens the same screen in a terminal.",
		ValidArgsFunction: folders,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.uiCommand(cmd, args, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.minSize, "min-size", defaultMinSize, "hide findings smaller than this, such as 50MB or 1GiB")
	fl.StringSliceVar(&f.categories, "category", nil, "only run these categories, ecosystems or scanners, such as docker,node")
	fl.StringVar(&f.stale, "stale", "", "only show project findings with no activity for this long, such as 90d")
	fl.IntVar(&f.depth, "depth", 0, "how many levels below each root to look for projects, 0 for no limit")
	fl.BoolVar(&f.all, "all", false, "with paths, also check package caches, Docker, app caches and Downloads")
	fl.StringArrayVar(&f.dockerLabels, "docker-label", nil, "only report Docker objects with this label, key=value or key; repeatable")
	fl.StringVar(&f.out, "out", "plan.json", "plan file the w key writes")
	completeFlag(cmd, "min-size", oneOf(sizeChoices))
	completeFlag(cmd, "category", listOf(categoryChoices))
	completeFlag(cmd, "stale", oneOf(staleChoices))
	completeFlag(cmd, "depth", cobra.NoFileCompletions)
	completeFlag(cmd, "docker-label", cobra.NoFileCompletions)
	completeFlag(cmd, "out", filesWithExt("json"))
	return cmd
}

// uiCommand checks the flags, opens the screen and, once it closes, prints
// a plain summary so the terminal keeps a record after the alternate screen
// is gone.
func (a *app) uiCommand(cmd *cobra.Command, args []string, f uiFlags) error {
	if a.interactive == nil || !a.interactive() {
		return errors.New(notTerminal)
	}
	if f.out == "" {
		f.out = "plan.json"
	}
	// Canceled when the screen closes, however it closes, so a run still
	// going stops after the action it is in.
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	changed := func(name string) bool {
		fl := cmd.Flags().Lookup(name)
		return fl != nil && fl.Changed
	}
	var prog *tea.Program
	opts, err := a.uiOptions(ctx, args, f, changed, func(msg tea.Msg) { prog.Send(msg) })
	if err != nil {
		return err
	}
	be := opts.Backend.(*uiBackend)
	m := tui.New(opts)
	// The screen never captures the mouse, so text such as a printed
	// command can still be selected and copied.
	prog = tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(a.stdin), tea.WithOutput(a.stdout), tea.WithAltScreen())
	_, progErr := prog.Run()
	return a.uiFinish(ctx, cancel, be, m.Result(), progErr, f.out)
}

// uiFinish ends the command once the screen has closed. A signal or a
// failure of the screen can end the program while a run goes on: it is
// stopped after the action it is in, waited for, and reported like any
// run before the command returns.
func (a *app) uiFinish(ctx context.Context, cancel context.CancelFunc, be *uiBackend, res tui.Result, progErr error, out string) error {
	cancel()
	be.close()
	if res.Pending != nil {
		res.Runs = append(res.Runs, be.runOf(*res.Pending))
	}
	if progErr != nil {
		a.printRuns(res.Runs)
		return errors.Join(fmt.Errorf("interactive screen: %w", progErr), runErrors(res.Runs))
	}
	return a.uiSummary(ctx, res, out)
}

// uiOptions are the options of the screen over the real backend.
func (a *app) uiOptions(ctx context.Context, args []string, f uiFlags, changed func(string) bool, send func(tea.Msg)) (tui.Options, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return tui.Options{}, err
	}
	start, err := a.uiStart(cfg, args, f, changed)
	if err != nil {
		return tui.Options{}, err
	}
	var last *finding.Report
	if r, err := loadReport(a.lastReportPath()); err == nil {
		last = r
	}
	return tui.Options{
		Context:    ctx,
		Backend:    &uiBackend{a: a, cfg: cfg, categories: f.categories},
		Send:       send,
		Home:       a.home,
		Version:    buildVersion(),
		Now:        func() time.Time { return a.now() },
		Renderer:   lipgloss.NewRenderer(a.stdout),
		TickEvery:  time.Second,
		Start:      start,
		LastReport: last,
		OutPath:    f.out,
	}, nil
}

// uiSummary prints what the screen did once it has closed: every plan it
// applied, then the plan it saved or wrote last. It returns the errors of
// the runs, so a run that failed or stopped ends reclaim with exit 1.
func (a *app) uiSummary(ctx context.Context, res tui.Result, out string) error {
	a.printRuns(res.Runs)
	runErr := runErrors(res.Runs)
	switch res.Outcome {
	case tui.OutcomeWrite:
		return errors.Join(runErr, a.writePlan(ctx, res.Report, res.Selected, out))
	case tui.OutcomeApplied:
	case tui.OutcomePlanSaved:
		p := res.Plan
		for _, msg := range p.LeftOut {
			a.sayf("Left out of the plan: %s\n", msg)
		}
		a.sayf("Plan written to %s: %d actions, %s. Nothing was removed.\n", p.Path, len(p.Plan.Actions), units.FormatSize(p.Plan.TotalSize()))
		a.sayf("Review it, then run: reclaim apply %s\n", p.Path)
	default:
		if len(res.Runs) == 0 {
			line := "Nothing was removed."
			if res.Scanned {
				line += " The report is saved, so reclaim select picks from it."
			}
			a.sayf("%s\n", line)
		}
	}
	return runErr
}

// printRuns prints the summary of every plan the screen applied.
func (a *app) printRuns(runs []tui.Run) {
	for _, run := range runs {
		a.appliedSummary(run)
	}
}

// runErrors joins the errors of the runs, each naming its plan.
func runErrors(runs []tui.Run) error {
	var errs []error
	for _, run := range runs {
		if run.Err != nil {
			errs = append(errs, fmt.Errorf("apply %s: %w", run.Plan.Path, run.Err))
		}
	}
	return errors.Join(errs...)
}

// uiStart prefills the Start screen from the flags, the configuration and
// the paths, checking the flags as scan does.
func (a *app) uiStart(cfg *config.Config, args []string, f uiFlags, changed func(string) bool) (tui.StartOptions, error) {
	var o tui.StartOptions
	minSize := cfg.MinSize
	if changed("min-size") || !cfg.HasMinSize {
		v, err := units.ParseSize(f.minSize)
		if err != nil {
			return o, fmt.Errorf("--min-size: %w", err)
		}
		minSize = v
	}
	stale := cfg.Stale
	if f.stale != "" {
		v, err := units.ParseAge(f.stale)
		if err != nil {
			return o, fmt.Errorf("--stale: %w", err)
		}
		stale = v
	}
	for _, l := range f.dockerLabels {
		if _, err := dockerx.ParseLabel(l); err != nil {
			return o, fmt.Errorf("--docker-label: %w", err)
		}
	}
	if f.depth < 0 {
		return o, errors.New("--depth must not be negative")
	}
	all := scanners.New().All()
	enabled, err := enabledScanners(all, f.categories, cfg.Disable)
	if err != nil {
		return o, err
	}
	var paths []string
	for _, p := range args {
		r, err := resolveDir(p)
		if err != nil {
			return o, err
		}
		paths = append(paths, r)
	}
	o = tui.StartOptions{
		Mode: tui.ModeMachine, Paths: paths, Roots: a.uiRoots(cfg), MinSize: minSize, Stale: stale,
		GitHub: cfg.GitHub(), Depth: f.depth, All: f.all, DockerLabels: f.dockerLabels,
	}
	if len(paths) > 0 {
		o.Mode = tui.ModeFolder
	}
	if wd, err := a.getwd(); err == nil {
		o.Folder = wd
	}
	for _, c := range finding.Categories() {
		choice := tui.CategoryChoice{Category: c, On: len(scan.OnlyCategory(enabled, c)) > 0}
		if !choice.On && len(f.categories) == 0 {
			choice.Note = "disabled in config"
		}
		o.Categories = append(o.Categories, choice)
	}
	return o, nil
}

// uiRoots are the roots of a whole machine scan: the configured ones, else
// the default ones that exist.
func (a *app) uiRoots(cfg *config.Config) []string {
	if len(cfg.Roots) > 0 {
		return a.configRoots(cfg)
	}
	return a.defaultRoots()
}

// uiBackend is the backend of the interactive screen over the scan, plan
// and apply code the commands use. None of its methods removes anything.
type uiBackend struct {
	a   *app
	cfg *config.Config
	// categories are the --category selectors, which pick the scanners of
	// the categories they touch.
	categories []string
	// save writes a plan file, plan.Save when nil; tests make it fail.
	save func(path string, p *plan.Plan) error

	// mu guards the fields below, which the screen's commands reach from
	// their own goroutines.
	mu sync.Mutex
	// saved holds the SHA-256 of every plan file SavePlan wrote, so Apply
	// runs only those, and only as they were confirmed.
	saved map[string][sha256.Size]byte
	// runs counts the runs in progress, and closed refuses new ones once
	// the screen has closed, so the command waits for the last before it
	// reports and exits.
	runs   sync.WaitGroup
	closed bool
	// done holds how every real run ended, by plan file.
	done map[string]tui.Run
}

var _ tui.Backend = (*uiBackend)(nil)

// Scan runs the scan the Start screen describes, with the code scan and
// worktrees use, and saves a scan that finished as the last report.
func (b *uiBackend) Scan(ctx context.Context, req tui.ScanRequest, progress func(scan.Event)) (*finding.Report, error) {
	a := b.a
	reg := scanners.New()
	selected, err := b.scanners(reg.All(), req)
	if err != nil {
		return nil, err
	}
	var labels []dockerx.Label
	for _, l := range req.DockerLabels {
		label, err := dockerx.ParseLabel(l)
		if err != nil {
			return nil, fmt.Errorf("docker label: %w", err)
		}
		labels = append(labels, label)
	}
	var roots, paths []string
	switch req.Mode {
	case tui.ModeFolder:
		for _, p := range req.Paths {
			r, err := b.resolve(p)
			if err != nil {
				return nil, err
			}
			roots = append(roots, r)
		}
		paths = roots
		if !req.All {
			selected = scan.UnderRoots(selected)
		}
	default:
		roots = a.uiRoots(b.cfg)
	}
	if len(selected) == 0 {
		return nil, errors.New("no scanners selected: the ticked categories have no scanner to run")
	}
	command, depth := "scan", req.Depth
	if req.Mode == tui.ModeWorktrees {
		// As reclaim worktrees, which takes no --depth.
		command, depth = "worktrees", 0
	}
	// What scan runs by default with the configuration, which the scope
	// leaves unsaid.
	byDefault, err := enabledScanners(reg.All(), nil, b.cfg.Disable)
	if err != nil {
		return nil, err
	}
	if req.Mode == tui.ModeFolder && !req.All {
		byDefault = scan.UnderRoots(byDefault)
	}
	report := a.runScan(ctx, reg, scanRequest{
		scanners: selected, roots: roots, exclude: b.cfg.Exclude, depth: depth, labels: labels,
		scope:     &finding.Scope{Command: command, Args: scopeArgs(req, paths, selected, reg.All(), byDefault), Paths: paths, Roots: roots},
		worktrees: scan.WorktreeOptions{GitHub: req.GitHub, InUse: req.Mode == tui.ModeWorktrees},
		progress:  progress,
		shown:     scan.Filter{MinSize: req.MinSize, Stale: req.Stale, Now: a.now(), Exclude: b.cfg.Exclude},
	})
	report.Findings = scan.Filter{MinSize: req.MinSize, Stale: req.Stale, Now: a.now()}.Apply(report.Findings)
	// A scan that was stopped returns what it found so far, which is not
	// what select should read next.
	if !scan.Interrupted(report.Warnings) {
		a.saveLastReport(report)
	}
	return report, nil
}

// scanners picks the scanners of the ticked categories: the ones the
// --category flag selected, else the ones the configuration leaves enabled,
// else, for a category whose scanners are all disabled but which the user
// ticked, all of them, as --category runs a disabled scanner it names. In
// worktrees mode only the worktree scanners run.
func (b *uiBackend) scanners(all []scan.Scanner, req tui.ScanRequest) ([]scan.Scanner, error) {
	enabled, err := enabledScanners(all, nil, b.cfg.Disable)
	if err != nil {
		return nil, err
	}
	var flagged []scan.Scanner
	if len(b.categories) > 0 {
		if flagged, err = scan.Select(all, b.categories); err != nil {
			return nil, fmt.Errorf("--category: %w", err)
		}
	}
	cats := req.Categories
	if req.Mode == tui.ModeWorktrees {
		cats = []finding.Category{finding.CategoryWorktree}
	}
	var out []scan.Scanner
	for _, c := range cats {
		pick := scan.OnlyCategory(flagged, c)
		if len(pick) == 0 {
			pick = scan.OnlyCategory(enabled, c)
		}
		if len(pick) == 0 {
			pick = scan.OnlyCategory(all, c)
		}
		out = append(out, pick...)
	}
	return out, nil
}

// resolve turns a folder the user typed, which may start with ~, into a
// clean absolute path without symbolic links.
func (b *uiBackend) resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		expanded, err := config.Expand(p, b.a.home)
		if err != nil {
			return "", err
		}
		p = expanded
	}
	return resolveDir(p)
}

// scopeArgs writes the scan the screen ran as the flags of the command it
// matches, so select and the provenance line say what it covered.
// The --category of the args names exactly the scanners that ran, whole
// categories where all of a category ran, whenever they differ from what
// the command runs by default with the configuration.
func scopeArgs(req tui.ScanRequest, paths []string, ran, all, byDefault []scan.Scanner) []string {
	args := slices.Clone(paths)
	if req.Mode == tui.ModeWorktrees {
		return args
	}
	if req.All {
		args = append(args, "--all")
	}
	if !sameScanners(ran, byDefault) {
		var selectors []string
		for _, c := range finding.Categories() {
			got := scan.OnlyCategory(ran, c)
			switch {
			case len(got) == 0:
			case len(got) == len(scan.OnlyCategory(all, c)):
				selectors = append(selectors, string(c))
			default:
				for _, s := range got {
					selectors = append(selectors, s.Name())
				}
			}
		}
		args = append(args, "--category="+strings.Join(selectors, ","))
	}
	if req.Depth > 0 {
		args = append(args, "--depth="+strconv.Itoa(req.Depth))
	}
	for _, l := range req.DockerLabels {
		args = append(args, "--docker-label="+l)
	}
	args = append(args, "--min-size="+flagSize(req.MinSize))
	if req.Stale > 0 {
		args = append(args, "--stale="+strconv.Itoa(int(req.Stale/(24*time.Hour)))+"d")
	}
	return args
}

// sameScanners reports whether two lists hold the same scanners.
func sameScanners(a, b []scan.Scanner) bool {
	names := func(ss []scan.Scanner) []string {
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = s.Name()
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(names(a), names(b))
}

// flagSize writes a size the way --min-size takes it.
func flagSize(n int64) string {
	switch {
	case n > 0 && n%1_000_000_000 == 0:
		return strconv.FormatInt(n/1_000_000_000, 10) + "GB"
	case n > 0 && n%1_000_000 == 0:
		return strconv.FormatInt(n/1_000_000, 10) + "MB"
	default:
		return strconv.FormatInt(n, 10)
	}
}

// Protected renders the protected list for the roots with the
// configuration's exclude entries.
func (b *uiBackend) Protected(roots []string) []apply.ProtectedEntry {
	return apply.ProtectedList(apply.ProtectedOptions{Home: b.a.home, Roots: roots, Exclude: b.cfg.Exclude, ConfigPath: b.cfg.Path})
}

// SavePlan builds the plan with the code select uses, proves the selected
// worktrees as select does, and saves the plan as
// plans/plan-<timestamp>.json under the reclaim folder.
func (b *uiBackend) SavePlan(ctx context.Context, r *finding.Report, selected []finding.Finding) (tui.SavedPlan, error) {
	a := b.a
	pl, err := plan.New(r, selected, a.host, a.now())
	if err != nil {
		return tui.SavedPlan{}, err
	}
	leftOut := a.proveWorktrees(ctx, pl)
	if len(pl.Actions) == 0 {
		return tui.SavedPlan{LeftOut: leftOut}, nil
	}
	path, err := a.newPlanPath()
	if err != nil {
		return tui.SavedPlan{}, err
	}
	save := b.save
	if save == nil {
		save = plan.Save
	}
	if err := save(path, pl); err != nil {
		releasePlanPath(path)
		return tui.SavedPlan{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tui.SavedPlan{}, fmt.Errorf("read back plan %s: %w", path, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.saved == nil {
		b.saved = map[string][sha256.Size]byte{}
	}
	b.saved[path] = sha256.Sum256(data)
	return tui.SavedPlan{Path: path, Plan: pl, LeftOut: leftOut}, nil
}

// releasePlanPath removes the name newPlanPath reserved when the plan could
// not be written into it: the file, only while it is still the empty
// regular file the reservation created, so nothing else is ever removed.
func releasePlanPath(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return
	}
	_ = os.Remove(path)
}

// Apply runs the plan file at path as reclaim apply --keep-going does,
// with the configuration's exclude entries refused, a new log in the
// reclaim folder, and progress at both edges of every action. With dryRun
// it runs every check and removes nothing. It runs only a plan file
// SavePlan wrote, unchanged since, so what runs is what the Confirm screen
// showed. Canceling ctx stops the run after the action in progress, which
// always finishes.
func (b *uiBackend) Apply(ctx context.Context, path string, dryRun bool, progress apply.Progress) (sum apply.Summary, err error) {
	if err := b.begin(); err != nil {
		return apply.Summary{}, err
	}
	defer b.runs.Done()
	if !dryRun {
		defer func() { b.record(path, sum, err) }()
	}
	a := b.a
	pl, err := b.load(path)
	if err != nil {
		return apply.Summary{}, err
	}
	if err := pl.CheckAge(a.now(), false); err != nil {
		return apply.Summary{}, err
	}
	logFile, logPath, err := a.openLog("", dryRun)
	if err != nil {
		return apply.Summary{}, err
	}
	defer func() { _ = logFile.Close() }()
	opts := a.applyOptions(b.cfg, path, logFile, logPath)
	opts.KeepGoing, opts.DryRun, opts.Progress = true, dryRun, progress
	// The actions run under a context that is never canceled, so a stop
	// never cuts off a command or a Docker call halfway; the stop signal
	// is checked between actions instead.
	opts.Stop = ctx.Done()
	return apply.Run(context.WithoutCancel(ctx), pl, opts)
}

// load reads the plan file at path, refusing one SavePlan did not write
// or that changed since.
func (b *uiBackend) load(path string) (*plan.Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan %s: %w", path, err)
	}
	b.mu.Lock()
	want, ok := b.saved[path]
	b.mu.Unlock()
	switch {
	case !ok:
		return nil, fmt.Errorf("refusing plan %s: the screen did not save it", path)
	case sha256.Sum256(data) != want:
		return nil, fmt.Errorf("refusing plan %s: it changed after the screen saved it; review it with reclaim apply instead", path)
	}
	return plan.Parse(path, data)
}

// begin counts a run in, unless the screen has closed.
func (b *uiBackend) begin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errNotStarted
	}
	b.runs.Add(1)
	return nil
}

// errNotStarted is the error of a run the screen started as it closed.
var errNotStarted = errors.New("the screen closed before the run started; nothing was removed")

// record keeps how a real run ended.
func (b *uiBackend) record(path string, sum apply.Summary, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done == nil {
		b.done = map[string]tui.Run{}
	}
	b.done[path] = tui.Run{Plan: tui.SavedPlan{Path: path}, Summary: sum, Err: err}
}

// close refuses new runs and waits for the ones in progress to end.
func (b *uiBackend) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.runs.Wait()
}

// runOf is how the run of a saved plan ended, once close has returned: as
// recorded, or never started.
func (b *uiBackend) runOf(saved tui.SavedPlan) tui.Run {
	b.mu.Lock()
	defer b.mu.Unlock()
	run, ok := b.done[saved.Path]
	if !ok {
		return tui.Run{Plan: saved, Err: errNotStarted}
	}
	run.Plan = saved
	return run
}

// FreeSpace reports the free bytes of the volume holding path.
func (b *uiBackend) FreeSpace(path string) (int64, error) {
	return fsx.FreeSpace(path)
}

// newPlanPath reserves a plan file name under the reclaim folder that no
// plan uses yet, plans/plan-<timestamp>.json, by creating it empty and
// exclusively, so two screens saving in the same second never pick the same
// name. plan.Save then replaces the empty file.
func (a *app) newPlanPath() (string, error) {
	if a.stateDir == "" {
		return "", errors.New("no folder to save the plan in")
	}
	dir := filepath.Join(a.stateDir, "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	stamp := a.now().UTC().Format("20060102T150405Z")
	for i := 1; ; i++ {
		name := "plan-" + stamp + ".json"
		if i > 1 {
			name = fmt.Sprintf("plan-%s-%d.json", stamp, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		switch {
		case err == nil:
			if err := f.Close(); err != nil {
				return "", fmt.Errorf("reserve %s: %w", path, err)
			}
			return path, nil
		case !errors.Is(err, os.ErrExist):
			return "", fmt.Errorf("reserve %s: %w", path, err)
		}
	}
}

// appliedSummary prints what the screen applied: the freed total, the
// count of each outcome, the commands that need administrator rights in
// full, the log and the plan.
func (a *app) appliedSummary(run tui.Run) {
	sum := run.Summary
	if len(sum.Outcomes) == 0 {
		a.sayf("Nothing was applied from %s.\n", run.Plan.Path)
		return
	}
	counts := map[apply.Status]int{}
	var sudo []string
	for _, o := range sum.Outcomes {
		counts[o.Status]++
		if o.Status == apply.StatusManual {
			sudo = append(sudo, "sudo "+strings.Join(o.Action.Command, " "))
		}
	}
	a.sayf("Freed %s: %d removed, %d already gone, %d failed, %d not run, %d for you to run.\n",
		units.FormatSize(sum.Freed), counts[apply.StatusDone], counts[apply.StatusGone], counts[apply.StatusFailed],
		counts[apply.StatusNotRun], counts[apply.StatusManual])
	if len(sudo) > 0 {
		a.sayf("%s\n", "These need administrator rights. Run them yourself if you want them:")
		for _, c := range sudo {
			a.sayf("  %s\n", c)
		}
	}
	if sum.LogPath != "" {
		a.sayf("Log: %s\n", sum.LogPath)
	}
	a.sayf("Plan: %s\n", run.Plan.Path)
}
