package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

var testLabel = map[string]string{"reclaim.test": "1"}

// newFake returns a daemon with one object of every interesting kind.
func newFake() *dockerx.Fake {
	return &dockerx.Fake{
		Host: "unix:///var/run/docker.sock",
		Containers: []container.Summary{
			{ID: "c-running", Names: []string{"/web"}, Image: "web:latest", ImageID: "sha256:web", State: container.StateRunning,
				Mounts: []container.MountPoint{{Type: "volume", Name: "vol-db"}}},
			{ID: "c-paused", Names: []string{"/paused"}, Image: "paused", ImageID: "sha256:paused", State: container.StatePaused},
			{ID: "c-exited", Names: []string{"/old"}, Image: "old:1", ImageID: "sha256:old", State: container.StateExited, SizeRw: 1000,
				Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
				Labels:  map[string]string{"reclaim.test": "1", "com.docker.compose.project": "shop"},
				Mounts:  []container.MountPoint{{Type: "volume", Name: "vol-stopped"}, {Type: "bind", Source: "/host"}}},
			{ID: "c-created", Names: []string{"/fresh"}, Image: "gone:1", ImageID: "sha256:gone", State: container.StateCreated, SizeRw: 10},
		},
		FinishedAt: map[string]string{"c-exited": "2026-09-01T10:00:00.123456789Z"},
		Images: []image.Summary{
			{ID: "sha256:web", RepoTags: []string{"web:latest"}, ParentID: "sha256:parent", Size: 900},
			{ID: "sha256:parent", RepoTags: []string{"<none>:<none>"}, Size: 800, SharedSize: 800},
			{ID: "sha256:paused", RepoTags: []string{"paused:latest"}, Size: 100},
			{ID: "sha256:old", RepoTags: []string{"old:1"}, Size: 700},
			{ID: "sha256:dangling00000000", RepoTags: []string{"<none>:<none>"}, Size: 500, SharedSize: 100, Created: 1700000000},
			{ID: "sha256:unused", RepoTags: []string{"busybox:latest", "busybox:1.36"}, RepoDigests: []string{"busybox@sha256:abc"}, Size: 4000, SharedSize: -1, Labels: testLabel},
			{ID: "sha256:local", RepoTags: []string{"mine:dev"}, Size: 300},
		},
		Volumes: []volume.Volume{
			{Name: "vol-db", UsageData: &volume.UsageData{Size: 9000, RefCount: 1}},
			{Name: "vol-stopped", UsageData: &volume.UsageData{Size: 3000, RefCount: 1}},
			{Name: "vol-free", UsageData: &volume.UsageData{Size: 2000}, Labels: testLabel, CreatedAt: "2026-02-03T04:05:06Z"},
			{Name: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", UsageData: &volume.UsageData{Size: -1}},
		},
		BuildCache: []build.CacheRecord{
			{ID: "r1", Size: 300},
			{ID: "r2", Size: 100, InUse: true},
			{ID: "r3", Size: 50, Shared: true},
		},
	}
}

func run(t *testing.T, f *dockerx.Fake, labels []dockerx.Label) (map[string]finding.Finding, scan.Result) {
	t.Helper()
	res := scan.Run(context.Background(), []scan.Scanner{Scanner{}}, scan.Env{Docker: f, DockerLabels: labels}, 1)
	out := map[string]finding.Finding{}
	for _, x := range res.Findings {
		out[x.Target] = x
	}
	return out, res
}

func TestScanFindsOnlyUnusedObjects(t *testing.T) {
	got, res := run(t, newFake(), nil)
	require.Empty(t, res.Warnings)
	require.Empty(t, res.Notes)

	type want struct {
		tier   finding.Tier
		action finding.Action
		size   int64
	}
	expected := map[string]want{
		"c-exited":                {finding.TierA, finding.ActionDockerRemoveContainer, 1000},
		"c-created":               {finding.TierB, finding.ActionDockerRemoveContainer, 10},
		"sha256:dangling00000000": {finding.TierA, finding.ActionDockerRemoveImage, 400},
		"sha256:unused":           {finding.TierB, finding.ActionDockerRemoveImage, 4000},
		"sha256:local":            {finding.TierB, finding.ActionDockerRemoveImage, 300},
		"vol-stopped":             {finding.TierC, finding.ActionDockerRemoveVolume, 3000},
		"vol-free":                {finding.TierB, finding.ActionDockerRemoveVolume, 2000},
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": {finding.TierB, finding.ActionDockerRemoveVolume, 0},
		BuildCacheTarget: {finding.TierA, finding.ActionDockerPruneBuildCache, 300},
	}
	for target, w := range expected {
		f, ok := got[target]
		require.True(t, ok, "missing %s", target)
		require.Equal(t, w.tier, f.Tier, target)
		require.Equal(t, w.action, f.Action, target)
		require.Equal(t, w.size, f.Size, target)
		require.Equal(t, finding.CategoryDocker, f.Category)
		require.Empty(t, f.Path, "docker findings have no path")
		require.NotEmpty(t, f.Restore, target)
	}
	require.Len(t, got, len(expected), "running containers, their images and volumes, used images and parents are never offered")

	old := got["c-exited"]
	require.Contains(t, old.Name, "old")
	require.Equal(t, "docker compose up for project shop", old.Restore)
	require.Equal(t, time.Date(2026, 9, 1, 10, 0, 0, 123456789, time.UTC), old.LastUsed.UTC())
	require.Contains(t, got["c-created"].Warning, "image no longer exists")
	require.Equal(t, "docker pull busybox:latest", got["sha256:unused"].Restore)
	require.Contains(t, got["sha256:unused"].Name, "busybox:1.36")
	require.Contains(t, got["sha256:local"].Restore, "built locally")
	require.Contains(t, got["vol-stopped"].Warning, "old")
	require.Contains(t, got["vol-free"].Warning, "not inspected")
	require.Contains(t, got["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"].Name, "anonymous")
	require.Contains(t, got[BuildCacheTarget].Name, "1 unused records")
}

func TestScanLabelFilter(t *testing.T) {
	label, err := dockerx.ParseLabel("reclaim.test=1")
	require.NoError(t, err)
	got, res := run(t, newFake(), []dockerx.Label{label})
	require.Len(t, got, 3)
	require.Contains(t, got, "c-exited")
	require.Contains(t, got, "sha256:unused")
	require.Contains(t, got, "vol-free")
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "build cache")
}

func TestScanOrbStackNote(t *testing.T) {
	f := newFake()
	f.Host = "unix:///Users/me/.orbstack/run/docker.sock"
	_, res := run(t, f, nil)
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "OrbStack")
}

func TestScanErrors(t *testing.T) {
	t.Run("no client", func(t *testing.T) {
		_, err := Scanner{}.Scan(context.Background(), scan.Env{})
		require.Error(t, err)
	})
	t.Run("daemon down becomes a warning", func(t *testing.T) {
		f := newFake()
		f.Err = errors.New("Cannot connect to the Docker daemon")
		got, res := run(t, f, nil)
		require.Empty(t, got)
		require.Len(t, res.Warnings, 1)
		require.Contains(t, res.Warnings[0].Message, "Cannot connect")
	})
}

func TestNoBuildCacheFindingWhenNothingReclaimable(t *testing.T) {
	f := newFake()
	f.BuildCache = []build.CacheRecord{{ID: "x", Size: 10, InUse: true}}
	got, _ := run(t, f, nil)
	require.NotContains(t, got, BuildCacheTarget)
}
