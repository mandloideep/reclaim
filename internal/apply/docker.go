package apply

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
)

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

// dockerAction checks a Docker object right before acting on it and, unless
// this is a dry run, removes it, never forcing.
func (r *runner) dockerAction(ctx context.Context, a *plan.Action) Outcome {
	var c checks
	if err := checkExcluded("", a, r.opts.Excluded); err != nil {
		return c.fail(CheckExcluded, err)
	}
	c.pass(CheckExcluded, "not one of your exclude or ignore entries")
	cli, err := r.client()
	if err != nil {
		return c.fail(CheckDaemon, err)
	}
	c.pass(CheckDaemon, cli.DaemonHost())
	dry := r.opts.DryRun
	switch a.Action {
	case finding.ActionDockerRemoveContainer:
		err = removeContainer(ctx, cli, a.Target, dry, &c)
	case finding.ActionDockerRemoveImage:
		// A dry run removes no container, so the containers it checked
		// earlier in the plan, which the real run removes first, do not
		// keep the image in use.
		var removedFirst map[string]bool
		if dry {
			removedFirst = r.containers
		}
		err = removeImage(ctx, cli, a.Target, a.Tags, dry, removedFirst, &c)
	case finding.ActionDockerRemoveVolume:
		err = removeVolume(ctx, cli, a.Target, dry, &c)
	case finding.ActionDockerPruneBuildCache:
		// The daemon prunes only build cache no build uses; there is no
		// dry run of it, so a dry run stops after reaching the daemon.
		c.pass(CheckDockerUse, "only unused build cache is pruned")
		if dry {
			return c.done(Outcome{})
		}
		rep, perr := cli.BuildCachePrune(ctx, build.CachePruneOptions{All: true})
		if perr != nil {
			return c.done(Outcome{Err: fmt.Errorf("prune build cache: %w", perr)})
		}
		return c.done(Outcome{Freed: int64(min(rep.SpaceReclaimed, uint64(1<<62)))})
	}
	switch {
	case err != nil:
		return c.done(Outcome{Err: err})
	case dry:
		return c.done(Outcome{})
	}
	return c.done(Outcome{Freed: a.Size, Estimated: true})
}

// removeContainer removes a container that is not running. The checks it
// runs are recorded in c; a failed one is the last.
func removeContainer(ctx context.Context, cli dockerx.API, id string, dry bool, c *checks) error {
	info, err := cli.ContainerInspect(ctx, id)
	if dockerx.IsNotFound(err) {
		c.pass(CheckDockerUse, "container "+id+" no longer exists")
		return errGone
	}
	if err != nil {
		return failed(c, CheckDockerUse, fmt.Errorf("inspect container %s: %w", id, err))
	}
	if info.ContainerJSONBase == nil || info.State == nil {
		return failed(c, CheckDockerUse, fmt.Errorf("inspect container %s: no state returned", id))
	}
	if dockerx.IsActiveState(info.State.Status) || info.State.Running || info.State.Paused || info.State.Restarting {
		return failed(c, CheckDockerUse, fmt.Errorf("refusing container %s: it is %s now", id, info.State.Status))
	}
	c.pass(CheckDockerUse, "the container is "+info.State.Status)
	if dry {
		return nil
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
func removeImage(ctx context.Context, cli dockerx.API, id string, planned []string, dry bool, removedFirst map[string]bool, c *checks) error {
	img, err := cli.ImageInspect(ctx, id)
	if dockerx.IsNotFound(err) {
		c.pass(CheckDockerUse, "image "+id+" no longer exists")
		return errGone
	}
	if err != nil {
		return failed(c, CheckDockerUse, fmt.Errorf("inspect image %s: %w", id, err))
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return failed(c, CheckDockerUse, fmt.Errorf("list containers: %w", err))
	}
	for _, ct := range containers {
		if ct.ImageID == img.ID && !removedFirst[ct.ID] {
			return failed(c, CheckDockerUse, fmt.Errorf("refusing image %s: container %s uses it now", id, strings.TrimPrefix(firstOr(ct.Names, ct.ID), "/")))
		}
	}
	// An image with child images only loses its tags when removed, which
	// frees nothing and leaves it untagged, so it is refused up front.
	images, err := cli.ImageList(ctx, image.ListOptions{All: true})
	if err != nil {
		return failed(c, CheckDockerUse, fmt.Errorf("list images: %w", err))
	}
	for _, other := range images {
		if other.ParentID == img.ID {
			return failed(c, CheckDockerUse, fmt.Errorf("refusing image %s: image %s is built on it", id, other.ID))
		}
	}
	c.pass(CheckDockerUse, "no container uses it and no image is built on it")
	if current := realTags(img.RepoTags); !slices.Equal(current, sortedTags(planned)) {
		return failed(c, CheckTags, fmt.Errorf("refusing image %s: its tags changed since the scan, from %v to %v", id, sortedTags(planned), current))
	}
	c.pass(CheckTags, "tags as at scan time: "+strings.Join(orNone(sortedTags(planned)), ", "))
	if dry {
		return nil
	}
	// PruneChildren stays off so untagged parent images, which the plan
	// does not list, are never removed along with this one.
	opts := image.RemoveOptions{}
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

func orNone(xs []string) []string {
	if len(xs) == 0 {
		return []string{"none"}
	}
	return xs
}

// realTags returns the sorted tags of an image, without placeholders.
func realTags(tags []string) []string {
	out := slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return t == "" || t == "<none>:<none>" })
	slices.Sort(out)
	return out
}

func sortedTags(tags []string) []string {
	out := slices.Clone(tags)
	slices.Sort(out)
	return out
}

// removeVolume removes a volume that no running container uses.
func removeVolume(ctx context.Context, cli dockerx.API, name string, dry bool, c *checks) error {
	if _, err := cli.VolumeInspect(ctx, name); dockerx.IsNotFound(err) {
		c.pass(CheckDockerUse, "volume "+name+" no longer exists")
		return errGone
	} else if err != nil {
		return failed(c, CheckDockerUse, fmt.Errorf("inspect volume %s: %w", name, err))
	}
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return failed(c, CheckDockerUse, fmt.Errorf("list containers: %w", err))
	}
	for _, ct := range containers {
		if !dockerx.IsActiveState(ct.State) {
			continue
		}
		for _, m := range ct.Mounts {
			if m.Name == name {
				return failed(c, CheckDockerUse, fmt.Errorf("refusing volume %s: running container %s uses it", name, strings.TrimPrefix(firstOr(ct.Names, ct.ID), "/")))
			}
		}
	}
	c.pass(CheckDockerUse, "no running container uses it")
	if dry {
		return nil
	}
	if err := cli.VolumeRemove(ctx, name, false); err != nil {
		if dockerx.IsNotFound(err) {
			return errGone
		}
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

// failed records a failed check in c and returns its error.
func failed(c *checks, name string, err error) error {
	c.list = append(c.list, Check{Name: name, Detail: err.Error()})
	return err
}

func firstOr(xs []string, def string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return def
}
