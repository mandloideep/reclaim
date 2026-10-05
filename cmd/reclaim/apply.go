package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/units"
)

type applyFlags struct {
	yes       bool
	log       string
	keepGoing bool
	staleOK   bool
}

func newApplyCmd(a *app) *cobra.Command {
	var f applyFlags
	cmd := &cobra.Command{
		Use:   "apply plan.json",
		Short: "Remove exactly what a plan lists, after confirmation",
		Long: "apply prints every action in the plan with its size and asks you to type yes. It then runs the\n" +
			"actions in order, checking each target again right before acting on it, and logs every result.\n" +
			"It stops at the first failure unless --keep-going is given. Plans whose scan is older than 24\n" +
			"hours are refused unless --stale-ok is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.applyCommand(cmd, args[0], f)
		},
	}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation, for scripts")
	cmd.Flags().StringVar(&f.log, "log", "", "log file to append to, default a new file in the reclaim state folder")
	cmd.Flags().BoolVar(&f.keepGoing, "keep-going", false, "continue after a failed action")
	cmd.Flags().BoolVar(&f.staleOK, "stale-ok", false, "accept a plan whose scan is older than 24 hours")
	return cmd
}

func (a *app) applyCommand(cmd *cobra.Command, path string, f applyFlags) error {
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
	p.Plan(pl)
	if !f.yes {
		a.sayf("\nType yes to remove these %d items (%s): ", len(pl.Actions), units.FormatSize(pl.TotalSize()))
		line, err := a.readLine()
		if err != nil || strings.TrimSpace(line) != "yes" {
			return errors.New("not confirmed, nothing was removed")
		}
	}

	logPath := f.log
	if logPath == "" {
		logPath = filepath.Join(a.stateDir, "logs", "apply-"+a.now().UTC().Format("20060102T150405Z")+".log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create log folder for %s: %w", logPath, err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	a.sayf("\n")
	total := len(pl.Actions)
	sum, runErr := apply.Run(cmd.Context(), pl, apply.Options{
		Home:      a.home,
		Walker:    fsx.NewWalker(0),
		Exec:      a.exec,
		Docker:    a.docker,
		Log:       logFile,
		PlanPath:  path,
		KeepGoing: f.keepGoing,
		Now:       a.now,
		Progress: func(i int, o apply.Outcome) {
			a.printOutcome(i, total, o)
		},
	})

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
	a.sayf("\n%s %s. Log: %s\n", p.Bold("Freed"), p.Bold(units.FormatSize(sum.Freed)), logPath)
	return runErr
}

func (a *app) printOutcome(i, total int, o apply.Outcome) {
	a.printer().Outcome(i, total, o)
}
