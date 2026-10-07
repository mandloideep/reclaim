package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
	"github.com/mandloideep/reclaim/internal/ui"
	"github.com/mandloideep/reclaim/internal/units"
)

type scanFlags struct {
	json         bool
	md           bool
	minSize      string
	tiers        []string
	categories   []string
	stale        string
	out          string
	all          bool
	depth        int
	dockerLabels []string
}

// defaultMinSize is the --min-size default when neither the flag nor the
// configuration file sets one.
const defaultMinSize = "10MB"

func newScanCmd(a *app) *cobra.Command {
	var f scanFlags
	cmd := &cobra.Command{
		Use:   "scan [path...]",
		Short: "Report reclaimable space, read only",
		Long: "scan reports everything it can reclaim, grouped by category and project, largest first.\n" +
			"With no path it walks the roots from the config file, or ~/Code, ~/Developer, ~/Projects, ~/src,\n" +
			"~/work and ~/Downloads, whichever exist, and checks package caches, Docker, app caches and Downloads.\n" +
			"With paths it only looks for project artifacts under them, unless --all is given.\n" +
			"A scan never modifies anything.",
		ValidArgsFunction: folders,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.scanCommand(cmd, args, f)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.json, "json", false, "print the full report as JSON")
	fl.BoolVar(&f.md, "md", false, "print a Markdown summary for pasting into an issue")
	fl.StringVar(&f.minSize, "min-size", defaultMinSize, "hide findings smaller than this, such as 50MB or 1GiB")
	fl.StringSliceVar(&f.tiers, "tier", nil, "only show these tiers, such as A,B")
	fl.StringSliceVar(&f.categories, "category", nil, "only run these categories, ecosystems or scanners, such as docker,node")
	fl.StringVar(&f.stale, "stale", "", "only show project findings with no activity for this long, such as 90d")
	fl.StringVar(&f.out, "out", "", "also save the JSON report to this file for select")
	fl.BoolVar(&f.all, "all", false, "with paths, also check package caches, Docker, app caches and Downloads")
	fl.IntVar(&f.depth, "depth", 0, "how many levels below each root to look for projects, 0 for no limit")
	fl.StringArrayVar(&f.dockerLabels, "docker-label", nil, "only report Docker objects with this label, key=value or key; repeatable")
	completeFlag(cmd, "min-size", oneOf(sizeChoices))
	completeFlag(cmd, "tier", listOf(tierChoices))
	completeFlag(cmd, "category", listOf(categoryChoices))
	completeFlag(cmd, "stale", oneOf(staleChoices))
	completeFlag(cmd, "out", filesWithExt("json"))
	completeFlag(cmd, "depth", cobra.NoFileCompletions)
	completeFlag(cmd, "docker-label", cobra.NoFileCompletions)
	return cmd
}

func (a *app) scanCommand(cmd *cobra.Command, args []string, f scanFlags) error {
	if f.json && f.md {
		return errors.New("--json and --md cannot be used together")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	minSize := cfg.MinSize
	if cmd.Flags().Changed("min-size") || !cfg.HasMinSize {
		if minSize, err = units.ParseSize(f.minSize); err != nil {
			return fmt.Errorf("--min-size: %w", err)
		}
	}
	var tiers []finding.Tier
	for _, t := range f.tiers {
		tier, err := finding.ParseTier(t)
		if err != nil {
			return fmt.Errorf("--tier: %w", err)
		}
		tiers = append(tiers, tier)
	}
	stale := cfg.Stale
	if f.stale != "" {
		if stale, err = units.ParseAge(f.stale); err != nil {
			return fmt.Errorf("--stale: %w", err)
		}
	}
	var labels []dockerx.Label
	for _, l := range f.dockerLabels {
		label, err := dockerx.ParseLabel(l)
		if err != nil {
			return fmt.Errorf("--docker-label: %w", err)
		}
		labels = append(labels, label)
	}
	if f.depth < 0 {
		return errors.New("--depth must not be negative")
	}

	reg := scanners.New()
	selected, err := enabledScanners(reg.All(), f.categories, cfg.Disable)
	if err != nil {
		return err
	}
	var roots, paths []string
	switch {
	case len(args) > 0:
		for _, p := range args {
			r, err := resolveDir(p)
			if err != nil {
				return err
			}
			roots = append(roots, r)
		}
		paths = roots
		if !f.all {
			selected = scan.OnlyCategory(selected, finding.CategoryProject)
		}
	case len(cfg.Roots) > 0:
		roots = a.configRoots(cfg)
	default:
		roots = a.defaultRoots()
	}
	if len(selected) == 0 {
		return errors.New("no scanners selected: with paths only project scanners run unless --all is given, and the config file may disable some")
	}
	if !f.json && !f.md && isTerminal(a.stderr) {
		_, _ = fmt.Fprintf(a.stderr, "Scanning %s with %d scanners...\n", describeRoots(a, roots), len(selected))
	}

	scope := scopeOf(cmd, args, paths, roots,
		"all", "category", "depth", "docker-label", "json", "md", "min-size", "out", "stale", "tier")
	report := a.runScan(cmd.Context(), reg, scanRequest{
		scanners: selected, roots: roots, exclude: cfg.Exclude, depth: f.depth, labels: labels, scope: scope,
	})
	if err := cmd.Context().Err(); err != nil {
		return fmt.Errorf("scan interrupted: %w", err)
	}
	filter := scan.Filter{MinSize: minSize, Tiers: tiers, Stale: stale, Now: a.now()}
	report.Findings = filter.Apply(report.Findings)

	if f.out != "" {
		if err := saveReport(f.out, report); err != nil {
			return err
		}
	}
	a.saveLastReport(report)
	switch {
	case f.json:
		return writeJSON(a.stdout, report)
	case f.md:
		a.markdownPrinter().ReportMarkdown(report)
		return nil
	}
	a.printer().Report(report)
	if f.out != "" {
		a.sayf("\nReport saved to %s. Next: reclaim select --report %s\n", f.out, f.out)
	} else if len(report.Findings) > 0 {
		a.sayf("%s\n", "\nNext: reclaim select to pick what goes, or reclaim select --preset safe.")
	}
	return nil
}

func describeRoots(a *app, roots []string) string {
	if len(roots) == 0 {
		return "caches"
	}
	p := a.printer()
	short := make([]string, len(roots))
	for i, r := range roots {
		short[i] = p.Path(r)
	}
	return strings.Join(short, ", ")
}

// markdownPrinter writes plain text for Markdown output, never colors, even
// on a terminal.
func (a *app) markdownPrinter() *ui.Printer {
	return ui.NewPlain(a.stdout, a.home, a.now())
}
