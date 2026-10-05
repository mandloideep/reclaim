// Package apply executes plan files.
//
// Every action is verified again immediately before it runs, because the disk
// may have changed since the scan. Filesystem removals go through
// os.RemoveAll on the exact path in the plan after the safety checks in
// safety.go. Clean commands are limited to a fixed allowlist. Docker removals
// never force and are refused when a running container uses the target.
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
	"strings"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

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
	// Err is set when Status is StatusFailed.
	Err error
	// Output is the output of a clean command, truncated.
	Output string
}

// Summary is the result of a run.
type Summary struct {
	// Outcomes holds one entry per plan action, in plan order.
	Outcomes []Outcome
	// Freed is the total number of bytes freed.
	Freed int64
	// Failed counts failed actions.
	Failed int
}

// Options configure a run.
type Options struct {
	// Home is the user's home directory, used by the path denylist.
	Home string
	// Walker measures sizes before and after removal.
	Walker *fsx.Walker
	// Exec runs clean commands.
	Exec execx.Runner
	// Docker returns a Docker client. It is called at most once, and only when
	// the plan has Docker actions.
	Docker func() (dockerx.API, error)
	// Log receives one JSON object per line for the start, every action and
	// the end of the run.
	Log io.Writer
	// PlanPath is recorded in the log.
	PlanPath string
	// KeepGoing continues after a failed action instead of stopping.
	KeepGoing bool
	// Now returns the current time. Nil uses time.Now.
	Now func() time.Time
	// Progress, when set, is called after every action.
	Progress func(index int, o Outcome)
}

// logEntry is one line of the apply log.
type logEntry struct {
	Time      time.Time      `json:"time"`
	Event     string         `json:"event"`
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
	Failed    int            `json:"failed,omitempty"`
}

// Run executes the plan's actions in order. The returned error is non nil
// when any action failed or ctx was canceled; the summary is always valid.
func Run(ctx context.Context, p *plan.Plan, opts Options) (Summary, error) {
	r := &runner{opts: opts}
	if r.opts.Now == nil {
		r.opts.Now = time.Now
	}
	if r.opts.Walker == nil {
		r.opts.Walker = fsx.NewWalker(0)
	}
	defer r.closeDocker()

	r.log(logEntry{Event: "start", Plan: opts.PlanPath, Actions: len(p.Actions)})
	sum := Summary{Outcomes: make([]Outcome, 0, len(p.Actions))}
	stopped := false
	for i := range p.Actions {
		a := p.Actions[i]
		var o Outcome
		switch {
		case stopped:
			o = Outcome{Action: a, Status: StatusNotRun}
		case ctx.Err() != nil:
			o = Outcome{Action: a, Status: StatusNotRun}
			stopped = true
		default:
			o = r.one(ctx, p, &a)
		}
		if o.Status != StatusNotRun {
			r.log(logEntry{
				Event: "action", ID: a.ID, Action: a.Action, Target: a.Target, Path: a.Path, Command: a.Command,
				Status: o.Status, Freed: &o.Freed, Estimated: o.Estimated, Error: errString(o.Err), Output: o.Output,
			})
		}
		sum.Outcomes = append(sum.Outcomes, o)
		sum.Freed += o.Freed
		if o.Status == StatusFailed {
			sum.Failed++
			if !opts.KeepGoing {
				stopped = true
			}
		}
		if opts.Progress != nil {
			opts.Progress(i, o)
		}
	}
	r.log(logEntry{Event: "end", Freed: &sum.Freed, Failed: sum.Failed})

	if err := ctx.Err(); err != nil {
		return sum, fmt.Errorf("apply interrupted: %w", err)
	}
	if sum.Failed > 0 {
		return sum, fmt.Errorf("%d of %d actions failed", sum.Failed, len(p.Actions))
	}
	return sum, nil
}

type runner struct {
	opts      Options
	docker    dockerx.API
	dockerErr error
	dockerSet bool
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
	if a.NeedsSudo {
		return Outcome{Action: *a, Status: StatusManual}
	}
	var o Outcome
	switch a.Action {
	case finding.ActionRemovePath:
		o = r.removePath(ctx, p, a)
	case finding.ActionRunCommand:
		o = r.runCommand(ctx, a)
	case finding.ActionDockerRemoveContainer, finding.ActionDockerRemoveImage,
		finding.ActionDockerRemoveVolume, finding.ActionDockerPruneBuildCache:
		o = r.dockerAction(ctx, a)
	default:
		o = Outcome{Err: fmt.Errorf("unknown action %q", a.Action)}
	}
	o.Action = *a
	switch {
	case errors.Is(o.Err, errGone):
		o.Status, o.Err = StatusGone, nil
	case o.Err != nil:
		o.Status = StatusFailed
	default:
		o.Status = StatusDone
	}
	return o
}

func (r *runner) removePath(ctx context.Context, p *plan.Plan, a *plan.Action) Outcome {
	if err := verifyPath(a, r.opts.Home, p.Roots); err != nil {
		return Outcome{Err: err}
	}
	before, err := r.opts.Walker.Size(ctx, a.Path)
	if err != nil {
		return Outcome{Err: err}
	}
	if len(before.Mounts) > 0 {
		return Outcome{Err: fmt.Errorf("refusing %s: another filesystem is mounted inside it at %s", a.Path, before.Mounts[0])}
	}
	err = os.RemoveAll(a.Path)
	if err != nil && errors.Is(err, fs.ErrPermission) && a.Kind == finding.KindDir {
		// Read only directories, such as those in a Go module cache, cannot
		// have entries removed. Make the directories inside the target
		// writable by their owner and try once more.
		if werr := makeDirsWritable(ctx, a.Path); werr == nil {
			err = os.RemoveAll(a.Path)
		}
	}
	freed := before.Size
	if _, statErr := os.Lstat(a.Path); statErr == nil {
		after, _ := r.opts.Walker.Size(ctx, a.Path)
		freed = max(before.Size-after.Size, 0)
	}
	if err != nil {
		return Outcome{Freed: freed, Err: fmt.Errorf("remove %s: %w", a.Path, err)}
	}
	return Outcome{Freed: freed}
}

// makeDirsWritable adds owner write and search permission to every directory
// below root. It never follows symbolic links.
func makeDirsWritable(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Entries that cannot be read are left alone; the second removal
		// attempt reports them.
		if walkErr == nil && d.IsDir() {
			if info, err := d.Info(); err == nil && info.Mode().Perm()&0o300 != 0o300 {
				return os.Chmod(path, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
}

const maxOutput = 4096

func (r *runner) runCommand(ctx context.Context, a *plan.Action) Outcome {
	if !plan.AllowedCommand(a.Command) {
		return Outcome{Err: fmt.Errorf("refusing command %q: not an allowed clean command", strings.Join(a.Command, " "))}
	}
	if r.opts.Exec == nil {
		return Outcome{Err: errors.New("no command runner configured")}
	}
	if _, err := r.opts.Exec.LookPath(a.Command[0]); err != nil {
		return Outcome{Err: err}
	}
	var before int64
	measured := false
	if a.Path != "" {
		if res, err := r.opts.Walker.Size(ctx, a.Path); err == nil {
			before, measured = res.Size, true
		}
	}
	out, err := r.opts.Exec.Run(ctx, a.Command)
	o := Outcome{Output: truncate(string(out), maxOutput)}
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
	return o
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[output truncated]"
}

func (r *runner) client() (dockerx.API, error) {
	if !r.dockerSet {
		r.dockerSet = true
		if r.opts.Docker == nil {
			r.dockerErr = errors.New("no Docker client configured")
		} else {
			r.docker, r.dockerErr = r.opts.Docker()
		}
	}
	return r.docker, r.dockerErr
}

func (r *runner) closeDocker() {
	if r.docker != nil {
		_ = r.docker.Close()
	}
}

func (r *runner) dockerAction(ctx context.Context, a *plan.Action) Outcome {
	cli, err := r.client()
	if err != nil {
		return Outcome{Err: err}
	}
	switch a.Action {
	case finding.ActionDockerRemoveContainer:
		err = removeContainer(ctx, cli, a.Target)
	case finding.ActionDockerRemoveImage:
		err = removeImage(ctx, cli, a.Target)
	case finding.ActionDockerRemoveVolume:
		err = removeVolume(ctx, cli, a.Target)
	case finding.ActionDockerPruneBuildCache:
		rep, perr := cli.BuildCachePrune(ctx, build.CachePruneOptions{All: true})
		if perr != nil {
			return Outcome{Err: fmt.Errorf("prune build cache: %w", perr)}
		}
		return Outcome{Freed: int64(min(rep.SpaceReclaimed, uint64(1<<62)))}
	}
	if err != nil {
		return Outcome{Err: err}
	}
	return Outcome{Freed: a.Size, Estimated: true}
}

func removeContainer(ctx context.Context, cli dockerx.API, id string) error {
	info, err := cli.ContainerInspect(ctx, id)
	if dockerx.IsNotFound(err) {
		return errGone
	}
	if err != nil {
		return fmt.Errorf("inspect container %s: %w", id, err)
	}
	if info.ContainerJSONBase == nil || info.State == nil {
		return fmt.Errorf("inspect container %s: no state returned", id)
	}
	if dockerx.IsActiveState(info.State.Status) || info.State.Running || info.State.Paused || info.State.Restarting {
		return fmt.Errorf("refusing container %s: it is %s now", id, info.State.Status)
	}
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{}); err != nil {
		if dockerx.IsNotFound(err) {
			return errGone
		}
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	return nil
}

// removeImage removes an image that no container uses. It removes each tag
// first and then the image id, never forcing, so the daemon refuses anything
// still referenced.
func removeImage(ctx context.Context, cli dockerx.API, id string) error {
	img, err := cli.ImageInspect(ctx, id)
	if dockerx.IsNotFound(err) {
		return errGone
	}
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", id, err)
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		if c.ImageID == img.ID {
			return fmt.Errorf("refusing image %s: container %s uses it now", id, strings.TrimPrefix(firstOr(c.Names, c.ID), "/"))
		}
	}
	opts := image.RemoveOptions{PruneChildren: true}
	for _, tag := range img.RepoTags {
		if tag == "" || tag == "<none>:<none>" {
			continue
		}
		if _, err := cli.ImageRemove(ctx, tag, opts); err != nil && !dockerx.IsNotFound(err) {
			return fmt.Errorf("remove image %s tag %s: %w", id, tag, err)
		}
	}
	if _, err := cli.ImageInspect(ctx, img.ID); dockerx.IsNotFound(err) {
		return nil
	}
	if _, err := cli.ImageRemove(ctx, img.ID, opts); err != nil && !dockerx.IsNotFound(err) {
		return fmt.Errorf("remove image %s: %w", id, err)
	}
	return nil
}

func removeVolume(ctx context.Context, cli dockerx.API, name string) error {
	if _, err := cli.VolumeInspect(ctx, name); dockerx.IsNotFound(err) {
		return errGone
	} else if err != nil {
		return fmt.Errorf("inspect volume %s: %w", name, err)
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		if !dockerx.IsActiveState(c.State) {
			continue
		}
		for _, m := range c.Mounts {
			if m.Name == name {
				return fmt.Errorf("refusing volume %s: running container %s uses it", name, strings.TrimPrefix(firstOr(c.Names, c.ID), "/"))
			}
		}
	}
	if err := cli.VolumeRemove(ctx, name, false); err != nil {
		if dockerx.IsNotFound(err) {
			return errGone
		}
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

func firstOr(xs []string, def string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return def
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
