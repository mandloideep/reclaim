// Package docker holds the Docker scanner. It reads the daemon's disk usage
// and container list and reports stopped containers, unused images, unused
// volumes and the build cache. It never reports a running container or
// anything a running container uses.
package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// BuildCacheTarget is the target of the single build cache finding.
const BuildCacheTarget = "docker-build-cache"

// Scanner reports reclaimable Docker objects.
type Scanner struct{}

var _ scan.Scanner = Scanner{}

// Name implements scan.Scanner.
func (Scanner) Name() string { return "docker" }

// Category implements scan.Scanner.
func (Scanner) Category() finding.Category { return finding.CategoryDocker }

// Ecosystem implements scan.Scanner.
func (Scanner) Ecosystem() string { return "docker" }

// Description implements scan.Scanner.
func (Scanner) Description() string {
	return "stopped containers, dangling and unused images, unused volumes and build cache"
}

// usage is what the scanner knows about which objects containers use.
type usage struct {
	images      map[string]bool     // image id used by any container
	activeVols  map[string]bool     // volume used by a running container
	stoppedVols map[string][]string // volume name to stopped container names
	imageExists map[string]bool
}

// Scan implements scan.Scanner.
func (Scanner) Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error) {
	if env.Docker == nil {
		return nil, errors.New("no Docker client available")
	}
	cli := env.Docker
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Size: true})
	if err != nil {
		return nil, fmt.Errorf("list containers at %s: %w", cli.DaemonHost(), err)
	}
	du, err := cli.DiskUsage(ctx, types.DiskUsageOptions{
		Types: []types.DiskUsageObject{types.ImageObject, types.VolumeObject, types.BuildCacheObject},
	})
	if err != nil {
		return nil, fmt.Errorf("read disk usage at %s: %w", cli.DaemonHost(), err)
	}
	if info, err := cli.Info(ctx); err == nil && dockerx.IsOrbStack(cli.DaemonHost(), info) {
		env.Diag.Note("Docker runs in OrbStack, which returns freed space to macOS automatically after removal.")
	}

	u := usage{
		images:      map[string]bool{},
		activeVols:  map[string]bool{},
		stoppedVols: map[string][]string{},
		imageExists: map[string]bool{},
	}
	for _, img := range du.Images {
		u.imageExists[img.ID] = true
	}
	for _, c := range containers {
		u.images[c.ImageID] = true
		for _, m := range c.Mounts {
			if m.Name == "" {
				continue
			}
			if dockerx.IsActiveState(c.State) {
				u.activeVols[m.Name] = true
			} else {
				u.stoppedVols[m.Name] = append(u.stoppedVols[m.Name], containerName(c))
			}
		}
	}

	var out []finding.Finding
	for _, c := range containers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if dockerx.IsActiveState(c.State) || !dockerx.MatchLabels(env.DockerLabels, c.Labels) {
			continue
		}
		out = append(out, containerFinding(ctx, cli, c, u))
	}
	out = append(out, imageFindings(du.Images, u, env.DockerLabels)...)
	out = append(out, volumeFindings(du.Volumes, u, env.DockerLabels)...)
	if len(env.DockerLabels) > 0 {
		env.Diag.Note("Docker build cache is not reported when filtering by label, because cache records carry no labels.")
	} else if f, ok := buildCacheFinding(du.BuildCache); ok {
		out = append(out, f)
	}
	return out, nil
}

func containerName(c container.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return shortID(c.ID)
}

func containerFinding(ctx context.Context, cli dockerx.API, c container.Summary, u usage) finding.Finding {
	// Only containers made by compose are tier A: compose recreates them from
	// its file. A container made by hand may hold work in its writable layer.
	f := finding.Finding{
		Tier:     finding.TierB,
		Target:   c.ID,
		Name:     fmt.Sprintf("container %s (%s, %s)", containerName(c), c.Image, c.State),
		Size:     c.SizeRw,
		LastUsed: time.Unix(c.Created, 0),
		Action:   finding.ActionDockerRemoveContainer,
		Restore:  "docker run or docker create from image " + c.Image,
		Warning:  "files written inside the container are lost; named volumes are kept",
	}
	if p := c.Labels["com.docker.compose.project"]; p != "" {
		f.Restore = "docker compose up for project " + p
		f.Tier = finding.TierA
	}
	if !u.imageExists[c.ImageID] {
		f.Tier = finding.TierB
		f.Warning = "its image no longer exists, so it cannot be recreated as it was; " + f.Warning
	}
	if info, err := cli.ContainerInspect(ctx, c.ID); err == nil && info.ContainerJSONBase != nil && info.State != nil {
		if t, err := time.Parse(time.RFC3339Nano, info.State.FinishedAt); err == nil && t.After(f.LastUsed) {
			f.LastUsed = t
		}
	}
	return f
}

func imageFindings(images []*image.Summary, u usage, labels []dockerx.Label) []finding.Finding {
	parents := map[string]bool{}
	for _, img := range images {
		if img.ParentID != "" {
			parents[img.ParentID] = true
		}
	}
	var out []finding.Finding
	for _, img := range images {
		// Images used by any container are never offered. Parents of other
		// images are intermediate layers that go away with their children.
		if u.images[img.ID] || parents[img.ID] || !dockerx.MatchLabels(labels, img.Labels) {
			continue
		}
		size := img.Size
		if img.SharedSize > 0 && img.SharedSize <= img.Size {
			size = img.Size - img.SharedSize
		}
		tags := realTags(img.RepoTags)
		f := finding.Finding{
			Target:   img.ID,
			Tags:     tags,
			Size:     size,
			LastUsed: time.Unix(img.Created, 0),
			Action:   finding.ActionDockerRemoveImage,
		}
		if len(tags) == 0 {
			f.Tier = finding.TierA
			f.Name = "dangling image " + shortID(img.ID)
			f.Restore = "rebuilt by the next build that needs it"
		} else {
			f.Tier = finding.TierB
			f.Name = "image " + strings.Join(tags, ", ")
			f.Restore = "docker pull " + tags[0]
			if len(img.RepoDigests) == 0 {
				f.Restore = "rebuild it, it was built locally and never pulled"
			}
		}
		out = append(out, f)
	}
	return out
}

func realTags(tags []string) []string {
	return slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return t == "<none>:<none>" || t == "" })
}

func volumeFindings(vols []*volume.Volume, u usage, labels []dockerx.Label) []finding.Finding {
	var out []finding.Finding
	for _, v := range vols {
		if u.activeVols[v.Name] || !dockerx.MatchLabels(labels, v.Labels) {
			continue
		}
		var size int64
		if v.UsageData != nil && v.UsageData.Size > 0 {
			size = v.UsageData.Size
		}
		f := finding.Finding{
			Tier:    finding.TierB,
			Target:  v.Name,
			Name:    "volume " + volumeLabel(v.Name),
			Size:    size,
			Action:  finding.ActionDockerRemoveVolume,
			Restore: "recreated empty by the next container that mounts it; its contents cannot be restored",
			Warning: "volume contents are not inspected and may hold data such as a database",
		}
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			f.LastUsed = t
		}
		if users := u.stoppedVols[v.Name]; len(users) > 0 {
			slices.Sort(users)
			f.Tier = finding.TierC
			f.Warning = "attached to stopped container " + strings.Join(slices.Compact(users), ", ") +
				"; remove the container first, and expect to lose the data it kept here"
		}
		out = append(out, f)
	}
	return out
}

func volumeLabel(name string) string {
	if len(name) == 64 && isHex(name) {
		return shortID(name) + " (anonymous)"
	}
	return name
}

func buildCacheFinding(records []*build.CacheRecord) (finding.Finding, bool) {
	var size int64
	var count int
	var last time.Time
	for _, r := range records {
		if r.InUse || r.Shared {
			continue
		}
		size += r.Size
		count++
		if r.LastUsedAt != nil && r.LastUsedAt.After(last) {
			last = *r.LastUsedAt
		}
	}
	if size <= 0 {
		return finding.Finding{}, false
	}
	return finding.Finding{
		Tier:     finding.TierA,
		Target:   BuildCacheTarget,
		Name:     fmt.Sprintf("build cache (%d unused records)", count),
		Size:     size,
		LastUsed: last,
		Action:   finding.ActionDockerPruneBuildCache,
		Restore:  "rebuilt by the next docker build, which will be slower the first time",
	}, true
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
