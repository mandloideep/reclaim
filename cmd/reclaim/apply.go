package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/config"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/units"
)

type applyFlags struct {
	yes       bool
	log       string
	keepGoing bool
	staleOK   bool
	dryRun    bool
}

func newApplyCmd(a *app) *cobra.Command {
	var f applyFlags
	cmd := &cobra.Command{
		Use:   "apply plan.json",
		Short: "Remove exactly what a plan lists, after confirmation",
		Long: "apply prints every action in the plan with its size and asks you to type yes. It then runs the\n" +
			"actions in order, checking each target again right before acting on it, and logs every result.\n" +
			"It stops at the first failure unless --keep-going is given. Plans whose scan is older than 24\n" +
			"hours are refused unless --stale-ok is given.\n\n" +
			"--dry-run runs every check of every action and removes nothing. It needs no confirmation,\n" +
			"prints what reclaim never removes, then one line per action: checked, gone, manual or refused\n" +
			"with the reason, and writes a log marked as a dry run.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: firstArg(filesWithExt("json")),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.applyCommand(cmd, args[0], f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation, for scripts")
	cmd.Flags().StringVar(&f.log, "log", "", "log file to append to, default a new file in the reclaim state folder")
	cmd.Flags().BoolVar(&f.keepGoing, "keep-going", false, "continue after a failed action")
	cmd.Flags().BoolVar(&f.staleOK, "stale-ok", false, "accept a plan whose scan is older than 24 hours")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "run every check and remove nothing")
	return cmd
}

func (a *app) applyCommand(cmd *cobra.Command, path string, f applyFlags) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	pl, err := plan.Load(path)
	if err != nil {
		return err
	}
	if err := pl.CheckAge(a.now(), f.staleOK); err != nil {
		return err
	}
	p := a.printer()
	if pl.Host != "" && a.host != "" && pl.Host != a.host {
		a.warnf("the plan was made on %q and this is %q", pl.Host, a.host)
	}
	if len(pl.Actions) == 0 {
		a.sayf("%s\n", "The plan has no actions, nothing to do.")
		return nil
	}
	if f.dryRun {
		// What the checks protect comes first, so the refusals below read
		// against it.
		p.Protected(a.protectedFor(cfg, pl.Roots), 100)
		a.sayf("\n%s %s: %s, checked and not removed.\n", p.Bold("Dry run of"), path, plural(len(pl.Actions), "action", "actions"))
	} else {
		p.Plan(pl)
		if !f.yes {
			a.sayf("\nType yes to remove these %d items (%s): ", len(pl.Actions), units.FormatSize(pl.TotalSize()))
			line, err := a.readLine()
			if err != nil || strings.TrimSpace(line) != "yes" {
				return errors.New("not confirmed, nothing was removed")
			}
		}
	}

	logFile, logPath, err := a.openLog(f.log, f.dryRun)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	a.sayf("\n")
	total := len(pl.Actions)
	opts := a.applyOptions(cfg, path, logFile, logPath)
	opts.KeepGoing, opts.DryRun = f.keepGoing, f.dryRun
	opts.Progress.Finished = func(i int, o apply.Outcome) { a.printOutcome(i, total, o) }
	sum, runErr := apply.Run(cmd.Context(), pl, opts)

	var manual []string
	for _, o := range sum.Outcomes {
		if o.Status == apply.StatusManual {
			manual = append(manual, "sudo "+strings.Join(o.Action.Command, " "))
		}
	}
	if len(manual) > 0 {
		a.sayf("%s\n", "\nThese need administrator rights. Run them yourself if you want them:")
		for _, m := range manual {
			a.sayf("%s\n", "  "+m)
		}
	}
	if f.dryRun {
		a.sayf("\n%s Log: %s\n", p.Bold(dryRunWords(sum)), logPath)
		return runErr
	}
	a.sayf("\n%s %s. Log: %s\n", p.Bold("Freed"), p.Bold(units.FormatSize(sum.Freed)), logPath)
	return runErr
}

// dryRunWords counts the results of a dry run.
func dryRunWords(sum apply.Summary) string {
	counts := map[apply.Status]int{}
	for _, o := range sum.Outcomes {
		counts[o.Status]++
	}
	var parts []string
	for _, s := range []struct {
		status apply.Status
		words  string
	}{{apply.StatusChecked, "checked"}, {apply.StatusGone, "already gone"}, {apply.StatusManual, "for you to run"},
		{apply.StatusRefused, "refused"}} {
		if n := counts[s.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s.words))
		}
	}
	return "Dry run: " + strings.Join(parts, ", ") + "; nothing was removed."
}

// applyOptions are the options every apply run passes: the screen's and
// the command's.
func (a *app) applyOptions(cfg *config.Config, planPath string, log io.Writer, logPath string) apply.Options {
	return apply.Options{
		Home:               a.home,
		Walker:             fsx.NewWalker(0),
		Exec:               a.exec,
		Getenv:             a.getenv,
		Docker:             a.docker,
		OllamaSystemModels: a.ollamaSystemModels,
		Log:                log,
		LogPath:            logPath,
		PlanPath:           planPath,
		Excluded:           exclusionsOf(cfg),
		Now:                a.now,
		Getwd:              a.getwd,
	}
}

// protectedFor is the protected list for a plan's roots with the
// configuration's exclude entries and the ignore list.
func (a *app) protectedFor(cfg *config.Config, roots []string) []apply.ProtectedEntry {
	ex := exclusionsOf(cfg)
	return apply.ProtectedList(apply.ProtectedOptions{Home: a.home, Roots: roots, Exclude: ex.Exclude, ConfigPath: cfg.Path,
		Ignore: ex.Ignore, IgnorePath: ex.IgnorePath})
}

// exclusionsOf are the entries apply refuses whatever a plan says: the
// configuration's exclude entries, paths and targets, and the ignore list.
func exclusionsOf(cfg *config.Config) apply.Exclusions {
	ex := apply.Exclusions{Exclude: append(slices.Clone(cfg.Exclude), cfg.ExcludeTargets...)}
	if cfg.Ignore != nil {
		ex.Ignore, ex.IgnorePath = slices.Clone(cfg.Ignore.Entries), cfg.Ignore.Path
	}
	return ex
}

// openLog opens the apply log: the file --log names, appended to, or a new
// one in the reclaim folder, named for a dry run when it is one.
func (a *app) openLog(path string, dryRun bool) (*os.File, string, error) {
	if path == "" {
		prefix := "apply-"
		if dryRun {
			prefix = "dry-run-"
		}
		path = filepath.Join(a.stateDir, "logs", prefix+a.now().UTC().Format("20060102T150405Z")+".log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", fmt.Errorf("create log folder for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("open log %s: %w", path, err)
	}
	return f, path, nil
}

func (a *app) printOutcome(i, total int, o apply.Outcome) {
	a.printer().Outcome(i, total, o)
}

// plural formats a count with the singular or plural noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
