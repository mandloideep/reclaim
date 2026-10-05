package dockerx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// Fake is an in-memory API for tests. It models the parts of the daemon's
// behavior that matter for safety: a running container cannot be removed, an
// image used by a container cannot be removed, an image with several tags
// cannot be removed by id without force, and a volume used by a container
// cannot be removed.
type Fake struct {
	// Containers, Images, Volumes and BuildCache are the daemon state.
	Containers []container.Summary
	Images     []image.Summary
	Volumes    []volume.Volume
	BuildCache []build.CacheRecord
	// FinishedAt maps a container id to its finish time as returned by inspect.
	FinishedAt map[string]string
	// Host is returned by DaemonHost.
	Host string
	// OperatingSystem is returned in Info.
	OperatingSystem string
	// Err, when set, is returned by every read call.
	Err error

	mu      sync.Mutex
	removed []string
}

// notFoundError satisfies the errdefs not found interface the client checks.
type notFoundError struct{ msg string }

func (e notFoundError) Error() string { return e.msg }

// NotFound marks the error as a not found error for client.IsErrNotFound.
func (notFoundError) NotFound() {}

// conflictError mirrors the daemon's refusal to remove something in use.
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

// Conflict marks the error as a conflict.
func (conflictError) Conflict() {}

var _ API = (*Fake)(nil)

// Removed lists every removal the fake performed, as "container:<id>",
// "image:<ref>", "untag:<ref>", "volume:<name>" or "build-cache".
func (f *Fake) Removed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.removed)
}

// DiskUsage implements API.
func (f *Fake) DiskUsage(context.Context, types.DiskUsageOptions) (types.DiskUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return types.DiskUsage{}, f.Err
	}
	var du types.DiskUsage
	for i := range f.Images {
		img := f.Images[i]
		du.Images = append(du.Images, &img)
	}
	for i := range f.Containers {
		c := f.Containers[i]
		du.Containers = append(du.Containers, &c)
	}
	for i := range f.Volumes {
		v := f.Volumes[i]
		du.Volumes = append(du.Volumes, &v)
	}
	for i := range f.BuildCache {
		r := f.BuildCache[i]
		du.BuildCache = append(du.BuildCache, &r)
	}
	return du, nil
}

// ContainerList implements API.
func (f *Fake) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []container.Summary
	for _, c := range f.Containers {
		if opts.All || c.State == container.StateRunning {
			out = append(out, c)
		}
	}
	return out, nil
}

// ContainerInspect implements API.
func (f *Fake) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return container.InspectResponse{}, f.Err
	}
	i := f.containerIndex(id)
	if i < 0 {
		return container.InspectResponse{}, notFoundError{"No such container: " + id}
	}
	c := f.Containers[i]
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:    c.ID,
			Image: c.ImageID,
			State: &container.State{
				Status:     c.State,
				Running:    c.State == container.StateRunning,
				Paused:     c.State == container.StatePaused,
				Restarting: c.State == container.StateRestarting,
				FinishedAt: f.FinishedAt[c.ID],
			},
		},
		Mounts: c.Mounts,
	}, nil
}

// ContainerRemove implements API.
func (f *Fake) ContainerRemove(_ context.Context, id string, opts container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.containerIndex(id)
	if i < 0 {
		return notFoundError{"No such container: " + id}
	}
	if IsActiveState(f.Containers[i].State) && !opts.Force {
		return conflictError{"cannot remove container " + id + ": container is " + f.Containers[i].State}
	}
	f.Containers = slices.Delete(f.Containers, i, i+1)
	f.removed = append(f.removed, "container:"+id)
	return nil
}

// ImageInspect implements API.
func (f *Fake) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return image.InspectResponse{}, f.Err
	}
	i := f.imageIndex(ref)
	if i < 0 {
		return image.InspectResponse{}, notFoundError{"No such image: " + ref}
	}
	img := f.Images[i]
	return image.InspectResponse{ID: img.ID, RepoTags: slices.Clone(img.RepoTags), Size: img.Size}, nil
}

// ImageList implements API. It always lists every image.
func (f *Fake) ImageList(context.Context, image.ListOptions) ([]image.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	return slices.Clone(f.Images), nil
}

// ImageRemove implements API. Removing a tag untags; the image itself goes
// away with its last tag or when removed by id.
func (f *Fake) ImageRemove(_ context.Context, ref string, opts image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.imageIndex(ref)
	if i < 0 {
		return nil, notFoundError{"No such image: " + ref}
	}
	img := &f.Images[i]
	byTag := slices.Contains(img.RepoTags, ref)
	if byTag && len(img.RepoTags) > 1 {
		img.RepoTags = slices.DeleteFunc(img.RepoTags, func(t string) bool { return t == ref })
		f.removed = append(f.removed, "untag:"+ref)
		return []image.DeleteResponse{{Untagged: ref}}, nil
	}
	if !byTag && len(img.RepoTags) > 1 && !opts.Force {
		return nil, conflictError{"unable to delete " + ref + " (must be forced) - image is referenced in multiple repositories"}
	}
	for _, c := range f.Containers {
		if c.ImageID == img.ID && !opts.Force {
			return nil, conflictError{"unable to delete " + ref + " - image is being used by container " + c.ID}
		}
	}
	id := img.ID
	f.Images = slices.Delete(f.Images, i, i+1)
	f.removed = append(f.removed, "image:"+ref)
	return []image.DeleteResponse{{Deleted: id}}, nil
}

// VolumeInspect implements API.
func (f *Fake) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return volume.Volume{}, f.Err
	}
	for _, v := range f.Volumes {
		if v.Name == name {
			return v, nil
		}
	}
	return volume.Volume{}, notFoundError{"no such volume: " + name}
}

// VolumeRemove implements API.
func (f *Fake) VolumeRemove(_ context.Context, name string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.Volumes, func(v volume.Volume) bool { return v.Name == name })
	if i < 0 {
		return notFoundError{"no such volume: " + name}
	}
	for _, c := range f.Containers {
		for _, m := range c.Mounts {
			if m.Name == name && !force {
				return conflictError{"remove " + name + ": volume is in use - [" + c.ID + "]"}
			}
		}
	}
	f.Volumes = slices.Delete(f.Volumes, i, i+1)
	f.removed = append(f.removed, "volume:"+name)
	return nil
}

// BuildCachePrune implements API. It removes every record not in use.
func (f *Fake) BuildCachePrune(_ context.Context, opts build.CachePruneOptions) (*build.CachePruneReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !opts.All {
		return nil, errors.New("fake only supports pruning all unused build cache")
	}
	rep := &build.CachePruneReport{}
	kept := f.BuildCache[:0]
	for _, r := range f.BuildCache {
		if r.InUse {
			kept = append(kept, r)
			continue
		}
		rep.CachesDeleted = append(rep.CachesDeleted, r.ID)
		if !r.Shared {
			rep.SpaceReclaimed += uint64(max(r.Size, 0))
		}
	}
	f.BuildCache = kept
	f.removed = append(f.removed, "build-cache")
	return rep, nil
}

// Info implements API.
func (f *Fake) Info(context.Context) (system.Info, error) {
	if f.Err != nil {
		return system.Info{}, f.Err
	}
	return system.Info{OperatingSystem: f.OperatingSystem}, nil
}

// DaemonHost implements API.
func (f *Fake) DaemonHost() string { return f.Host }

// Close implements API.
func (f *Fake) Close() error { return nil }

func (f *Fake) containerIndex(id string) int {
	return slices.IndexFunc(f.Containers, func(c container.Summary) bool { return c.ID == id })
}

func (f *Fake) imageIndex(ref string) int {
	return slices.IndexFunc(f.Images, func(img image.Summary) bool {
		return img.ID == ref || slices.Contains(img.RepoTags, ref) || strings.TrimPrefix(img.ID, "sha256:") == ref
	})
}

// String summarizes the fake state for test failure messages.
func (f *Fake) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf("containers=%d images=%d volumes=%d build-cache=%d", len(f.Containers), len(f.Images), len(f.Volumes), len(f.BuildCache))
}
