package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/units"
	"github.com/mandloideep/reclaim/internal/worktree"
)

// The exit codes of done, which agents read.
const (
	// doneRemovable: committed, pushed and merged, or removed with --remove.
	doneRemovable = 0
	// donePushed: pushed but not merged, or a removable worktree that
	// --remove could not remove; the work is safe and the worktree stays.
	donePushed = 1
	// doneHoldsWork: work that exists nowhere else.
	doneHoldsWork = 2
	// doneUnknown: not a linked worktree, or a state git could not report.
	doneUnknown = 3
)

type doneFlags struct {
	json          bool
	remove        bool
	allowUnmerged bool
}

func newDoneCmd(a *app) *cobra.Command {
	var f doneFlags
	cmd := &cobra.Command{
		Use:   "done [path]",
		Short: "Say whether a worktree's work is committed, pushed and merged, and remove it when it is",
		Long: "done is the command an agent, or a person, runs at the end of a task in a linked git worktree.\n" +
			"It classifies the worktree at path, default the current folder, exactly as the worktree scanner\n" +
			"does, prints its state in plain words or with --json as the scanner's state object, and exits:\n\n" +
			"  0  committed, pushed and merged: safe to remove\n" +
			"  1  pushed but not merged: the work is safe on the remote; open or finish the pull request\n" +
			"  2  work that exists nowhere else: the hints say how to save it\n" +
			"  3  not a linked worktree, or a state git could not report\n\n" +
			"With --remove it removes a worktree that exits 0, through a plan and apply like everything\n" +
			"else, and exits 0 only when it is gone; --allow-unmerged extends that to one that exits 1.\n" +
			"A worktree with work that exists nowhere else is never removed, whatever the flags: done prints\n" +
			"the commands that save it and the git worktree remove --force command for you to run yourself.\n" +
			"Run it from the main checkout, because done never removes the folder it runs in. It never\n" +
			"deletes branches and never pushes.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
				a.warnf("%v", err)
				return &exitError{code: doneUnknown}
			}
			return nil
		},
		ValidArgsFunction: firstArg(folders),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return a.doneCommand(cmd.Context(), path, f)
		},
	}
	// Agents read the exit code, so a mistake on the command line exits 3,
	// never 1, which means the work is safe.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		a.warnf("%v", err)
		return &exitError{code: doneUnknown}
	})
	cmd.Flags().BoolVar(&f.json, "json", false, "print the state as JSON, the object the worktree scanner writes")
	cmd.Flags().BoolVar(&f.remove, "remove", false, "remove the worktree when its work is committed, pushed and merged")
	cmd.Flags().BoolVar(&f.allowUnmerged, "allow-unmerged", false, "with --remove, also remove a worktree that is pushed but not merged")
	return cmd
}

// exitError ends the process with a code after the command has printed
// everything it had to say.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// exit returns nil for code 0 and an exitError otherwise.
func exit(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code}
}

func (a *app) doneCommand(ctx context.Context, path string, f doneFlags) error {
	if f.allowUnmerged && !f.remove {
		a.warnf("%s", "--allow-unmerged only makes sense with --remove")
		return exit(doneUnknown)
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	out := a.stdout
	if f.json {
		// The state goes to standard output, everything else to standard
		// error, so the output parses.
		out = a.stderr
	}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }

	wd, err := a.getwd()
	if err != nil {
		return fmt.Errorf("find the working directory: %w", err)
	}
	if path == "" {
		path = wd
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(wd, path)
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	senv := scan.Env{Home: a.home, Getenv: a.getenv}
	wenv := worktree.Env{Exec: a.exec, CodexHome: senv.CodexHome()}
	root, ok := worktree.FindRoot(path)
	if !ok {
		say("%s is not in a git repository, so it is not a linked worktree.\n", a.printer().Path(path))
		return exit(doneUnknown)
	}
	state, err := worktree.Classify(ctx, wenv, root)
	if errors.Is(err, worktree.ErrNotLinked) {
		say("%s is not a linked worktree: %s.\n", a.printer().Path(root), notLinkedReason(err))
		return exit(doneUnknown)
	}
	if err != nil {
		say("Could not classify %s: %v\n", a.printer().Path(root), err)
		return exit(doneUnknown)
	}
	if cfg.GitHub() {
		if err := worktree.LookupPullRequests(ctx, wenv, []*finding.WorktreeState{state}); err != nil {
			a.warnf("pull request state is not shown: %v", err)
		}
	}
	size, _ := fsx.NewWalker(0).Size(ctx, state.Path)
	code := doneCode(state)

	if f.json {
		if err := writeJSON(a.stdout, state); err != nil {
			return err
		}
	} else {
		a.printDone(out, state, size.Size, code, f.remove)
	}
	if !f.remove {
		return exit(code)
	}

	switch {
	case code == doneRemovable:
	case code == donePushed && f.allowUnmerged:
	case code == donePushed:
		say("\nNot removed: it is not merged. Add --allow-unmerged to remove it anyway; its commits stay on the remote.\n")
		return exit(code)
	default:
		say("\nNot removed: reclaim never removes a worktree whose work exists nowhere else or could not be checked, whatever the flags.\n")
		return exit(code)
	}
	roots := a.configRoots(cfg)
	if len(cfg.Roots) == 0 {
		roots = a.defaultRoots()
	}
	if err := a.doneRemove(ctx, out, roots, state, size.Size); err != nil {
		say("\n%v\n", err)
		return exit(refusalCode(err))
	}
	return nil
}

// refusalCode is done's exit code when --remove did not remove a worktree,
// from the check that refused it: 1 when its work is safe and only the
// removal was refused, as for a lock, the working directory or a path
// outside the roots; the code of the state computed during the refusal when
// the work changed since done classified it; 2 when git itself found files
// to keep; and 3 when it is no longer a linked worktree or the failure is of
// another kind.
func refusalCode(err error) int {
	var ref *apply.Refusal
	if !errors.As(err, &ref) {
		return doneUnknown
	}
	switch ref.Check {
	case apply.CheckPath, apply.CheckWorkdir:
		return donePushed
	case apply.CheckState:
		if ref.State == nil {
			return doneUnknown
		}
		// A lock refuses a worktree whose work is safe.
		return max(doneCode(ref.State), donePushed)
	case apply.CheckRemove:
		return doneHoldsWork
	default:
		return doneUnknown
	}
}

// doneCode turns a state into done's exit code. It judges the work, not the
// lock: an agent's own session holds the lock of its worktree while it runs,
// and --remove refuses any lock anyway.
func doneCode(s *finding.WorktreeState) int {
	verdict, _ := worktree.WorkVerdict(s)
	concrete := s.Modified > 0 || s.Untracked > 0 || s.Ignored > 0 || s.Loose > 0 || s.UnpushedBlocks()
	switch {
	case concrete:
		return doneHoldsWork
	case len(s.Unknown) > 0 || verdict == finding.VerdictAttention:
		return doneUnknown
	case verdict == finding.VerdictPushed:
		return donePushed
	default:
		return doneRemovable
	}
}

// notLinkedReason is the explanation in a not linked error, without the
// sentinel's own words.
func notLinkedReason(err error) string {
	return strings.TrimSuffix(err.Error(), ": "+worktree.ErrNotLinked.Error())
}

// printDone prints the state in plain words and what to do next.
func (a *app) printDone(w io.Writer, s *finding.WorktreeState, size int64, code int, removing bool) {
	p := a.printer()
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	say("%s: %s, %s\n", p.Path(s.Path), worktree.Words(s), units.FormatSize(size))
	if s.FetchedAt.IsZero() {
		say("Merge state is checked against %s, which was never fetched.\n", orUnknown(s.DefaultBranch))
	}
	switch code {
	case doneRemovable:
		if s.Locked {
			say("Its work is safe, but it is locked, so it cannot be removed now: %s\n", s.LockReason)
			if !s.LockLive {
				say("The process that locked it is gone; unlock it with: git -C %s worktree unlock %s\n", worktree.Quote(s.Repo), worktree.Quote(s.Path))
			}
		} else if !removing {
			say("Safe to remove. From the main checkout, run: reclaim done --remove %s\n", worktree.Quote(s.Path))
		}
	case donePushed:
		if s.PR != nil && s.PR.State == "OPEN" {
			say("The work is pushed but not merged. Review and merge %s first.\n", s.PR.URL)
		} else {
			say("The work is pushed but not merged into %s. Open or finish the pull request first.\n", orUnknown(s.DefaultBranch))
		}
	default:
		if len(s.Hints) > 0 {
			say("This worktree holds work that exists nowhere else. To keep it, first run:\n")
			for _, h := range s.Hints {
				say("  %s\n", h)
			}
		}
		if s.ForceCommand != "" {
			say("To remove it anyway, losing that work, run it yourself:\n  %s\n", s.ForceCommand)
		}
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "the default branch"
	}
	return s
}

// doneRemove removes one worktree through the same plan, proof and apply as
// everything else, writing the plan and the log to the reclaim cache folder.
func (a *app) doneRemove(ctx context.Context, w io.Writer, roots []string, s *finding.WorktreeState, size int64) error {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	tier := finding.TierA
	if !s.Merged.Proven() {
		tier = finding.TierB
	}
	f := finding.Finding{
		ID: finding.MakeID("git-worktrees", s.Path), Scanner: "git-worktrees", Category: finding.CategoryWorktree,
		Tier: tier, Path: s.Path, Kind: finding.KindDir, Target: s.Path, Name: s.Tool + " " + s.Branch,
		Size: size, Project: s.Repo, Repo: s.Repo, Worktree: s, Action: finding.ActionGitWorktreeRemove,
		Command: plan.WorktreeRemoveCommand(s.Repo, s.Path),
	}
	// The roots are those a full scan records: the configured or default
	// roots and the home folder.
	now := a.now()
	report := &finding.Report{Version: finding.ReportVersion, Created: now.UTC(), Host: a.host, OS: a.goos,
		Roots: uniqueRoots(append(roots, a.home)), Findings: []finding.Finding{f}}
	pl, err := plan.New(report, report.Findings, a.host, now)
	if err != nil {
		return err
	}
	if err := pl.Validate(); err != nil {
		return fmt.Errorf("not removed: %w", err)
	}
	opts := apply.Options{Home: a.home, Walker: fsx.NewWalker(0), Exec: a.exec, Getenv: a.getenv, Now: a.now, Getwd: a.getwd}
	// The proof runs once before the plan is written and again inside
	// apply, immediately before git acts.
	if _, err := apply.ProveWorktree(ctx, opts, pl.Roots, &pl.Actions[0]); err != nil {
		return fmt.Errorf("not removed: %w", err)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	planPath := filepath.Join(a.stateDir, "plans", "done-"+stamp+".json")
	logPath := filepath.Join(a.stateDir, "logs", "done-"+stamp+".log")
	for _, dir := range []string{filepath.Dir(planPath), filepath.Dir(logPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := plan.Save(planPath, pl); err != nil {
		return err
	}
	loaded, err := plan.Load(planPath)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()
	opts.Log, opts.PlanPath = logFile, planPath
	sum, err := apply.Run(ctx, loaded, opts)
	o := sum.Outcomes[0]
	switch {
	case err != nil && o.Err != nil:
		return fmt.Errorf("not removed: %w\nPlan: %s\nLog: %s", o.Err, planPath, logPath)
	case err != nil:
		return fmt.Errorf("not removed: %w\nPlan: %s\nLog: %s", err, planPath, logPath)
	case o.Status == apply.StatusGone:
		say("\n%s was already gone.\n", a.printer().Path(s.Path))
		return nil
	}
	say("\nRemoved %s, %s freed.\n", a.printer().Path(s.Path), units.FormatSize(o.Freed))
	if s.Branch != "" {
		say("The branch %s stays; delete it yourself with: git -C %s branch -d %s\n", s.Branch, worktree.Quote(s.Repo), worktree.Quote(s.Branch))
	}
	say("Plan: %s\nLog: %s\n", planPath, logPath)
	return nil
}
