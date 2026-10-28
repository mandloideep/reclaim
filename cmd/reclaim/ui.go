package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/config"
	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
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
	ctx := cmd.Context()
	changed := func(name string) bool {
		fl := cmd.Flags().Lookup(name)
		return fl != nil && fl.Changed
	}
	var prog *tea.Program
	m, err := a.newUIModel(ctx, args, f, changed, func(msg tea.Msg) { prog.Send(msg) })
	if err != nil {
		return err
	}
	// The screen never captures the mouse, so text such as a printed
	// command can still be selected and copied.
	prog = tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(a.stdin), tea.WithOutput(a.stdout), tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("interactive screen: %w", err)
	}
	return a.uiSummary(ctx, m.Result(), f.out)
}

// newUIModel builds the screen over the real backend, prefilled from the
// flags and the configuration. send delivers the scan's progress to the
// running program.
func (a *app) newUIModel(ctx context.Context, args []string, f uiFlags, changed func(string) bool, send func(tea.Msg)) (*tui.Model, error) {
	opts, err := a.uiOptions(ctx, args, f, changed, send)
	if err != nil {
		return nil, err
	}
	return tui.New(opts), nil
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

// uiSummary prints what the screen did once it has closed.
func (a *app) uiSummary(ctx context.Context, res tui.Result, out string) error {
	switch res.Outcome {
	case tui.OutcomeWrite:
		return a.writePlan(ctx, res.Report, res.Selected, out)
	case tui.OutcomePlanSaved:
		p := res.Plan
		for _, msg := range p.LeftOut {
			a.sayf("Left out of the plan: %s\n", msg)
		}
		a.sayf("Plan written to %s: %d actions, %s. Nothing was removed.\n", p.Path, len(p.Plan.Actions), units.FormatSize(p.Plan.TotalSize()))
		a.sayf("Review it, then run: reclaim apply %s\n", p.Path)
	default:
		line := "Nothing was removed."
		if res.Scanned {
			line += " The report is saved, so reclaim select picks from it."
		}
		a.sayf("%s\n", line)
	}
	return nil
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
	command := "scan"
	if req.Mode == tui.ModeWorktrees {
		command = "worktrees"
	}
	report := a.runScan(ctx, reg, scanRequest{
		scanners: selected, roots: roots, exclude: b.cfg.Exclude, depth: req.Depth, labels: labels,
		scope:     &finding.Scope{Command: command, Args: scopeArgs(req, paths), Paths: paths, Roots: roots},
		worktrees: scan.WorktreeOptions{GitHub: req.GitHub, InUse: req.Mode == tui.ModeWorktrees},
		progress:  progress,
		shown:     scan.Filter{MinSize: req.MinSize, Stale: req.Stale, Now: a.now(), Exclude: b.cfg.Exclude},
	})
	report.Findings = scan.Filter{MinSize: req.MinSize, Stale: req.Stale, Now: a.now()}.Apply(report.Findings)
	// A scan that was stopped returns what it found so far, which is not
	// what select should read next.
	if ctx.Err() == nil {
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
func scopeArgs(req tui.ScanRequest, paths []string) []string {
	args := slices.Clone(paths)
	if req.Mode == tui.ModeWorktrees {
		return args
	}
	if req.All {
		args = append(args, "--all")
	}
	if len(req.Categories) < len(finding.Categories()) {
		names := make([]string, len(req.Categories))
		for i, c := range req.Categories {
			names[i] = string(c)
		}
		args = append(args, "--category="+strings.Join(names, ","))
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
	if err := plan.Save(path, pl); err != nil {
		return tui.SavedPlan{}, err
	}
	return tui.SavedPlan{Path: path, Plan: pl, LeftOut: leftOut}, nil
}

// newPlanPath returns a plan file name under the reclaim folder that no
// plan uses yet, plans/plan-<timestamp>.json.
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
		_, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return path, nil
		case err != nil:
			return "", fmt.Errorf("inspect %s: %w", path, err)
		}
	}
}
