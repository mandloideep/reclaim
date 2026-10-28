// Package apply executes plan files.
//
// Every action is verified again immediately before it runs, because the disk
// may have changed since the scan. Filesystem removals go through
// os.RemoveAll on the exact path in the plan after the safety checks in
// safety.go. Clean commands are limited to a fixed allowlist. Docker removals
// never force and are refused when a running container uses the target.
//
// Every outcome carries the checks that ran, each with its result, and a dry
// run runs all of them and stops right before anything would change.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
)

// Status is the result of one action.
type Status string

// Statuses.
const (
	// StatusDone means the action ran and succeeded.
	StatusDone Status = "done"
	// StatusGone means the target no longer existed, so nothing was done.
	StatusGone Status = "gone"
	// StatusManual means the action needs sudo and was printed for the user.
	StatusManual Status = "manual"
	// StatusFailed means verification or execution failed.
	StatusFailed Status = "failed"
	// StatusNotRun means an earlier failure stopped the run before this action.
	StatusNotRun Status = "not-run"
	// StatusChecked means a dry run passed every check of the action and
	// stopped right before acting.
	StatusChecked Status = "checked"
	// StatusRefused means a check of a dry run failed, so a real run would
	// refuse the action.
	StatusRefused Status = "refused"
)

// Outcome is the result of one action.
type Outcome struct {
	// Action is the plan action.
	Action plan.Action
	// Status says what happened.
	Status Status
	// Freed is the number of bytes freed.
	Freed int64
	// Estimated is true when Freed is the scan time size rather than a
	// measurement, as for Docker objects.
	Estimated bool
	// Err is set when Status is StatusFailed or StatusRefused.
	Err error
	// Output is the output of a clean command, truncated.
	Output string
	// Checks are the checks that ran before acting, in order, each with
	// its result. When one failed it is the last.
	Checks []Check
	// Refusal is set when a worktree was refused because a check failed.
	Refusal *Refusal
}

// Summary is the result of a run.
type Summary struct {
	// Outcomes holds one entry per plan action, in plan order.
	Outcomes []Outcome
	// Freed is the total number of bytes freed.
	Freed int64
	// Failed counts failed actions, or refused ones in a dry run.
	Failed int
	// DryRun is set when the run removed nothing by design.
	DryRun bool
	// LogPath is the log the run wrote, as Options.LogPath named it.
	LogPath string
}

// Progress reports a run at both edges of every action, so a caller can show
// the action that is running. Either function may be nil.
type Progress struct {
	// Begin is called once before the first action with the log the run
	// writes, Options.LogPath, so a screen can name it while the run lasts.
	Begin func(logPath string)
	// Starting is called right before an action is checked and run.
	Starting func(index int, a plan.Action)
	// Finished is called with the outcome of an action, including the
	// actions an earlier failure or a canceled context kept from running.
	Finished func(index int, o Outcome)
}

// Options configure a run.
type Options struct {
	// Home is the user's home directory, used by the path denylist.
	Home string
	// Walker measures sizes before and after removal.
	Walker *fsx.Walker
	// Exec runs clean commands.
	Exec execx.Runner
	// OllamaSystemModels is the models folder of a system wide Ollama
	// service, plan.OllamaSystemModels on Linux and empty elsewhere. ollama
	// rm reaches whichever server answers, so a model of another folder is
	// removed only while this folder can be read and holds no model of the
	// same name.
	OllamaSystemModels string
	// Getenv reads the environment the commands run in, such as
	// OLLAMA_HOST. Nil reads nothing.
	Getenv func(string) string
	// Docker returns a Docker client. It is called at most once, and only when
	// the plan has Docker actions.
	Docker func() (dockerx.API, error)
	// Log receives one JSON object per line for the start, every action and
	// the end of the run.
	Log io.Writer
	// LogPath is where Log writes, recorded in the summary.
	LogPath string
	// PlanPath is recorded in the log.
	PlanPath string
	// KeepGoing continues after a failed action instead of stopping.
	KeepGoing bool
	// DryRun runs every check of every action and removes nothing: it stops
	// right before each action would change anything. It implies KeepGoing.
	DryRun bool
	// Excluded are the user's own entries, the configuration's exclude
	// entries and the ignore list. Apply refuses a target that is, lies
	// under or contains a path entry, or that a target entry names,
	// whatever the plan says.
	Excluded Exclusions
	// Now returns the current time. Nil uses time.Now.
	Now func() time.Time
	// Progress reports the run at both edges of every action.
	Progress Progress
	// Stop, when it is closed, stops the run between actions: the action
	// that runs finishes, and the rest are marked not run. Unlike canceling
	// ctx, which also cuts off the command or Docker call that runs, it
	// never leaves an action half done.
	Stop <-chan struct{}
	// Getwd returns the working directory of the process, which apply never
	// removes a worktree around. Nil uses os.Getwd.
	Getwd func() (string, error)
}

// logEntry is one line of the apply log.
type logEntry struct {
	Time      time.Time      `json:"time"`
	Event     string         `json:"event"`
	DryRun    bool           `json:"dry_run,omitempty"`
	Plan      string         `json:"plan,omitempty"`
	Actions   int            `json:"actions,omitempty"`
	ID        string         `json:"id,omitempty"`
	Action    finding.Action `json:"action,omitempty"`
	Target    string         `json:"target,omitempty"`
	Path      string         `json:"path,omitempty"`
	Command   []string       `json:"command,omitempty"`
	Status    Status         `json:"status,omitempty"`
	Freed     *int64         `json:"freed,omitempty"`
	Estimated bool           `json:"estimated,omitempty"`
	Error     string         `json:"error,omitempty"`
	Output    string         `json:"output,omitempty"`
	Checks    []Check        `json:"checks,omitempty"`
	Failed    int            `json:"failed,omitempty"`
}

// Run executes the plan's actions in order. The returned error is non nil
// when any action failed or was refused, or ctx was canceled; the summary is
// always valid. With DryRun it removes nothing.
func Run(ctx context.Context, p *plan.Plan, opts Options) (Summary, error) {
	r := &runner{opts: opts}
	if r.opts.Now == nil {
		r.opts.Now = time.Now
	}
	if r.opts.Walker == nil {
		r.opts.Walker = fsx.NewWalker(0)
	}
	if r.opts.DryRun {
		r.opts.KeepGoing = true
	}
	defer r.closeDocker()

	r.log(logEntry{Event: "start", DryRun: opts.DryRun, Plan: opts.PlanPath, Actions: len(p.Actions)})
	if opts.Progress.Begin != nil {
		opts.Progress.Begin(opts.LogPath)
	}
	sum := Summary{Outcomes: make([]Outcome, 0, len(p.Actions)), DryRun: opts.DryRun, LogPath: opts.LogPath}
	stopped := false
	for i := range p.Actions {
		a := p.Actions[i]
		var o Outcome
		switch {
		case stopped:
			o = Outcome{Action: a, Status: StatusNotRun}
		case ctx.Err() != nil || closed(opts.Stop):
			o = Outcome{Action: a, Status: StatusNotRun}
			stopped = true
		default:
			if opts.Progress.Starting != nil {
				opts.Progress.Starting(i, a)
			}
			o = r.one(ctx, p, &a)
		}
		if o.Status != StatusNotRun {
			r.log(logEntry{
				Event: "action", ID: a.ID, Action: a.Action, Target: a.Target, Path: a.Path, Command: a.Command,
				Status: o.Status, Freed: &o.Freed, Estimated: o.Estimated, Error: errString(o.Err), Output: o.Output,
				Checks: o.Checks,
			})
		}
		sum.Outcomes = append(sum.Outcomes, o)
		sum.Freed += o.Freed
		if o.Status == StatusFailed || o.Status == StatusRefused {
			sum.Failed++
			if !r.opts.KeepGoing {
				stopped = true
			}
		}
		if opts.Progress.Finished != nil {
			opts.Progress.Finished(i, o)
		}
	}
	r.log(logEntry{Event: "end", DryRun: opts.DryRun, Freed: &sum.Freed, Failed: sum.Failed})

	if err := ctx.Err(); err != nil {
		return sum, fmt.Errorf("apply interrupted: %w", err)
	}
	if closed(opts.Stop) && slices.ContainsFunc(sum.Outcomes, func(o Outcome) bool { return o.Status == StatusNotRun }) {
		return sum, errors.New("apply interrupted: stopped after the action that was running")
	}
	if sum.Failed > 0 {
		if opts.DryRun {
			return sum, fmt.Errorf("%d of %d actions would be refused", sum.Failed, len(p.Actions))
		}
		return sum, fmt.Errorf("%d of %d actions failed", sum.Failed, len(p.Actions))
	}
	return sum, nil
}

type runner struct {
	opts      Options
	docker    dockerx.API
	dockerErr error
	dockerSet bool
	// containers holds the containers a dry run found removable so far,
	// which no longer keep their images in use when the real run reaches
	// them.
	containers map[string]bool
	// checked holds the paths a dry run found removable so far. A folder
	// removed once empty, such as a Codex task folder, still holds the
	// worktrees earlier actions remove, so a dry run counts it empty when
	// all it holds is in here, as it will be in the real run.
	checked map[string]bool
}

func (r *runner) log(e logEntry) {
	if r.opts.Log == nil {
		return
	}
	e.Time = r.opts.Now().UTC()
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = r.opts.Log.Write(append(data, '\n'))
}

func (r *runner) one(ctx context.Context, p *plan.Plan, a *plan.Action) Outcome {
	var o Outcome
	switch {
	case !a.Action.Applicable():
		err := fmt.Errorf("refusing %s: action %q cannot be applied", a.Label(), a.Action)
		o = Outcome{Err: err, Checks: []Check{{Name: CheckAction, Detail: err.Error()}}}
	case a.NeedsSudo:
		return Outcome{Action: *a, Status: StatusManual,
			Checks: []Check{{Name: CheckSudo, OK: true, Detail: "needs administrator rights: printed for you, never run"}}}
	default:
		switch a.Action {
		case finding.ActionRemovePath:
			o = r.removePath(ctx, p, a)
		case finding.ActionRunCommand:
			o = r.runCommand(ctx, a)
		case finding.ActionGitWorktreeRemove:
			o = r.removeWorktree(ctx, p, a)
		case finding.ActionGitWorktreePrune:
			o = r.pruneWorktrees(ctx, a)
		case finding.ActionDockerRemoveContainer, finding.ActionDockerRemoveImage,
			finding.ActionDockerRemoveVolume, finding.ActionDockerPruneBuildCache:
			o = r.dockerAction(ctx, a)
		default:
			o = Outcome{Err: fmt.Errorf("unknown action %q", a.Action)}
		}
	}
	o.Action = *a
	switch {
	case errors.Is(o.Err, errGone):
		o.Status, o.Err = StatusGone, nil
	case o.Err != nil && r.opts.DryRun:
		o.Status = StatusRefused
	case o.Err != nil:
		o.Status = StatusFailed
	case r.opts.DryRun:
		o.Status = StatusChecked
		if a.Action == finding.ActionDockerRemoveContainer {
			if r.containers == nil {
				r.containers = map[string]bool{}
			}
			r.containers[a.Target] = true
		}
		if a.Path != "" && (a.Action == finding.ActionRemovePath || a.Action == finding.ActionGitWorktreeRemove) {
			if r.checked == nil {
				r.checked = map[string]bool{}
			}
			r.checked[a.Path] = true
		}
	default:
		o.Status = StatusDone
	}
	return o
}

func (r *runner) removePath(ctx context.Context, p *plan.Plan, a *plan.Action) Outcome {
	var c checks
	if err := checkProtected(a.Path, r.opts.Home, a.Project, p.Roots); err != nil {
		return c.fail(CheckRules, err)
	}
	root := containingRoot(a.Path, p.Roots)
	c.pass(CheckRules, "inside the root "+root+"; not protected, not a root or the project root")
	if err := checkExcluded(a.Path, a, r.opts.Excluded); err != nil {
		return c.fail(CheckExcluded, err)
	}
	c.pass(CheckExcluded, notExcluded)
	checked, err := os.Lstat(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return c.gone(CheckObject, a.Path+" no longer exists")
	}
	if err != nil {
		return c.fail(CheckObject, fmt.Errorf("inspect %s: %w", a.Path, err))
	}
	if err := checkObject(a, checked); err != nil {
		return c.fail(CheckObject, err)
	}
	c.pass(CheckObject, "still a "+kindWords(a.Kind)+", no symbolic link in its path, not a mount point, no .git entry or project manifest")
	before, err := r.opts.Walker.Size(ctx, a.Path)
	if err != nil {
		return c.fail(CheckMounts, err)
	}
	if len(before.Mounts) > 0 {
		return c.fail(CheckMounts, fmt.Errorf("refusing %s: another filesystem is mounted inside it at %s", a.Path, before.Mounts[0]))
	}
	c.pass(CheckMounts, "no other filesystem inside; "+fmt.Sprint(before.Size)+" bytes now")
	emptyOnly := a.Kind == finding.KindEmptyDir
	// A dry run removes nothing, so a folder removed once empty is checked
	// as the real run will find it: without what earlier actions remove.
	var removedFirst map[string]bool
	if r.opts.DryRun {
		removedFirst = r.checked
	}
	if err := checkInRoot(root, a.Path, checked, emptyOnly, removedFirst); err != nil {
		return c.fail(CheckLastLook, err)
	}
	detail := "the same object, no symbolic link, seen through the root"
	if emptyOnly {
		detail += "; empty"
		if r.opts.DryRun {
			detail += " once the actions before it have run"
		}
	}
	c.pass(CheckLastLook, detail)
	if r.opts.DryRun {
		return c.done(Outcome{})
	}
	if emptyOnly {
		if err := removeEmptyInRoot(root, a.Path, checked); err != nil {
			return c.done(Outcome{Err: fmt.Errorf("remove %s: %w", a.Path, err)})
		}
		return c.done(Outcome{Freed: before.Size})
	}
	err = removeInRoot(root, a.Path, checked)
	if err != nil && errors.Is(err, fs.ErrPermission) && a.Kind == finding.KindDir {
		// Read only directories, such as those in a Go module cache, cannot
		// have entries removed. Make the directories inside the target
		// writable by their owner, check again for mounts that unreadable
		// folders may have hidden, and try once more.
		if werr := makeDirsWritable(ctx, root, a.Path); werr != nil {
			return c.done(Outcome{Err: fmt.Errorf("remove %s: %w", a.Path, werr)})
		}
		again, serr := r.opts.Walker.Size(ctx, a.Path)
		if serr != nil {
			return c.done(Outcome{Err: serr})
		}
		if len(again.Mounts) > 0 {
			return c.fail(CheckMounts, fmt.Errorf("refusing %s: another filesystem is mounted inside it at %s", a.Path, again.Mounts[0]))
		}
		err = removeInRoot(root, a.Path, checked)
	}
	freed := before.Size
	if _, statErr := os.Lstat(a.Path); statErr == nil {
		after, _ := r.opts.Walker.Size(ctx, a.Path)
		freed = max(before.Size-after.Size, 0)
	}
	if err != nil {
		return c.done(Outcome{Freed: freed, Err: fmt.Errorf("remove %s: %w", a.Path, err)})
	}
	return c.done(Outcome{Freed: freed})
}

func kindWords(k finding.Kind) string {
	switch k {
	case finding.KindFile:
		return "file"
	case finding.KindEmptyDir:
		return "folder removed once empty"
	default:
		return "folder"
	}
}

// makeDirsWritable adds owner read, write and search permission to every
// directory below target, through an os.Root at root so it cannot leave it.
// It never follows symbolic links and refuses to enter another filesystem.
func makeDirsWritable(ctx context.Context, root, target string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open root %s: %w", root, err)
	}
	defer func() { _ = r.Close() }()
	top, err := os.Lstat(target)
	if err != nil {
		return err
	}
	dev, haveDev := fsx.DeviceOf(top)
	return filepath.WalkDir(target, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A directory is fixed before it is read, so an error here means it
		// stays unreadable even for its owner, and removal cannot succeed.
		if walkErr != nil || !d.IsDir() {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if other, ok := fsx.DeviceOf(info); ok && haveDev && other != dev {
			return fmt.Errorf("refusing %s: another filesystem is mounted inside it at %s", target, path)
		}
		if info.Mode().Perm()&0o700 == 0o700 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return r.Chmod(rel, info.Mode().Perm()|0o700)
	})
}

const maxOutput = 4096

func (r *runner) runCommand(ctx context.Context, a *plan.Action) Outcome {
	var c checks
	cmdline := strings.Join(a.Command, " ")
	if !plan.AllowedCommand(a.Command) {
		return c.fail(CheckAllowed, fmt.Errorf("refusing command %q: not an allowed clean command", cmdline))
	}
	c.pass(CheckAllowed, cmdline)
	if err := checkExcluded(a.Path, a, r.opts.Excluded); err != nil {
		return c.fail(CheckExcluded, err)
	}
	if a.Path != "" {
		c.pass(CheckExcluded, "the folder it cleans is not one of your exclude or ignore entries, inside one or holding one")
	} else {
		c.pass(CheckExcluded, "its target is not one of your exclude or ignore entries")
	}
	if r.opts.Exec == nil {
		return c.fail(CheckTool, errors.New("no command runner configured"))
	}
	tool, err := r.opts.Exec.LookPath(a.Command[0])
	if err != nil {
		return c.fail(CheckTool, err)
	}
	c.pass(CheckTool, tool)
	if _, _, ok := plan.LocateQuery(a.Command); ok {
		if err := r.relocate(ctx, a); err != nil {
			if errors.Is(err, errGone) {
				return c.gone(CheckLocation, a.Path+" no longer exists")
			}
			return c.fail(CheckLocation, err)
		}
		c.pass(CheckLocation, "the tool still reports "+a.Path)
	}
	if err := r.verifyArgument(ctx, a); err != nil {
		if errors.Is(err, errGone) {
			return c.gone(CheckTarget, "the target no longer exists")
		}
		return c.fail(CheckTarget, err)
	}
	if detail := argumentWords(a); detail != "" {
		c.pass(CheckTarget, detail)
	}
	if r.opts.DryRun {
		return c.done(Outcome{})
	}
	var before int64
	measured := false
	if a.Path != "" {
		if res, err := r.opts.Walker.Size(ctx, a.Path); err == nil {
			before, measured = res.Size, true
		}
	}
	out, err := r.opts.Exec.Run(ctx, a.Command)
	o := Outcome{Output: truncate(string(out))}
	if measured {
		after := int64(0)
		if res, serr := r.opts.Walker.Size(ctx, a.Path); serr == nil {
			after = res.Size
		}
		o.Freed = max(before-after, 0)
	}
	if err != nil {
		o.Err = err
	}
	return c.done(o)
}

// relocate asks the tool again where its cache lives, right before running
// its clean command, and refuses when the answer is not the directory the
// plan recorded. The clean command acts on wherever the tool's configuration
// points now, so a cache moved since the scan would otherwise be cleaned
// without having been measured or shown.
func (r *runner) relocate(ctx context.Context, a *plan.Action) error {
	query, sub, ok := plan.LocateQuery(a.Command)
	if !ok {
		return nil
	}
	cmdline := strings.Join(a.Command, " ")
	if a.Path == "" {
		return fmt.Errorf("refusing %q: the plan does not record the directory it cleans", cmdline)
	}
	out, err := r.opts.Exec.Output(ctx, query[0], query[1:]...)
	if err != nil {
		return fmt.Errorf("refusing %q: could not ask %s where its cache is: %w", cmdline, query[0], err)
	}
	dir := execx.FirstAbsPath(out)
	if dir == "" {
		return fmt.Errorf("refusing %q: %s did not report a cache directory", cmdline, strings.Join(query, " "))
	}
	now := filepath.Join(dir, sub)
	resolved, err := filepath.EvalSymlinks(now)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist) && now == a.Path:
		return errGone
	case errors.Is(err, fs.ErrNotExist):
		resolved = now
	default:
		return fmt.Errorf("refusing %q: resolve %s: %w", cmdline, now, err)
	}
	if resolved != a.Path {
		return fmt.Errorf("refusing %q: %s now reports %s, but the plan was made for %s", cmdline, strings.Join(query, " "), resolved, a.Path)
	}
	return nil
}

// truncate trims command output to maxOutput bytes for the log.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n[output truncated]"
}

// closed reports whether a stop channel is closed. A nil channel never is.
func closed(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
