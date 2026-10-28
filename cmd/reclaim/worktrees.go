package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
)

type worktreesFlags struct {
	json bool
	md   bool
	out  string
}

func newWorktreesCmd(a *app) *cobra.Command {
	var f worktreesFlags
	cmd := &cobra.Command{
		Use:   "worktrees [path...]",
		Short: "Show every linked git worktree and whether its work exists elsewhere, read only",
		Long: "worktrees runs only the worktree scanners, over the repositories under the given paths or the\n" +
			"configured roots, and the Codex worktrees, and prints one table per repository with every linked\n" +
			"worktree: whether its changes are committed, its commits pushed and merged, its pull request and\n" +
			"its lock. Tier A is committed, pushed and merged; tier B is pushed but not merged; the others are\n" +
			"listed with the commands that save their work and are never removed by reclaim.\n" +
			"Like scan, it saves the report for select, so reclaim worktrees then reclaim select cleans up\n" +
			"worktrees alone. Merge state is as of the last fetch; worktrees never fetches.",
		ValidArgsFunction: folders,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.worktreesCommand(cmd, args, f)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.json, "json", false, "print the report, with the full state of every worktree, as JSON")
	fl.BoolVar(&f.md, "md", false, "print the tables as Markdown")
	fl.StringVar(&f.out, "out", "", "also save the JSON report to this file for select")
	completeFlag(cmd, "out", filesWithExt("json"))
	return cmd
}

func (a *app) worktreesCommand(cmd *cobra.Command, args []string, f worktreesFlags) error {
	if f.json && f.md {
		return errors.New("--json and --md cannot be used together")
	}
	cfg, err := a.loadConfig()
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
	case len(cfg.Roots) > 0:
		roots = a.configRoots(cfg)
	default:
		roots = a.defaultRoots()
	}
	reg := scanners.New()
	enabled, err := enabledScanners(reg.All(), nil, cfg.Disable)
	if err != nil {
		return err
	}
	selected := scan.OnlyCategory(enabled, finding.CategoryWorktree)
	if len(selected) == 0 {
		return errors.New("the config file disables every worktree scanner")
	}
	report := a.runScan(cmd.Context(), reg, scanRequest{
		scanners:       selected,
		roots:          roots,
		exclude:        cfg.ExcludedPaths(),
		excludeTargets: cfg.ExcludedTargets(),
		scope:          scopeOf(cmd, args, paths, roots, "json", "md", "out"),
		// Every linked worktree is shown, the ones a running process
		// holds too, which a scan never lists.
		worktrees: scan.WorktreeOptions{GitHub: cfg.GitHub(), InUse: true},
	})
	if err := cmd.Context().Err(); err != nil {
		return fmt.Errorf("worktrees interrupted: %w", err)
	}
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
		a.markdownPrinter().WorktreesMarkdown(report)
		return nil
	}
	a.printer().Worktrees(report)
	if report.TotalSize() > 0 {
		if f.out != "" {
			a.sayf("\nReport saved to %s. Next: reclaim select --report %s\n", f.out, f.out)
		} else {
			a.sayf("%s\n", "\nNext: reclaim select to pick the worktrees that go, or reclaim select --preset safe.")
		}
	}
	return nil
}
