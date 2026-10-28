package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/config"
	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
)

// ignoreDocker is a Docker daemon with a stopped container, an unused
// volume and a dangling image, all of which the scan offers.
func ignoreDocker() *dockerx.Fake {
	const MB = 1_000_000
	old := time.Now().Add(-90 * 24 * time.Hour)
	return &dockerx.Fake{
		Host: "unix:///var/run/docker.sock",
		Containers: []container.Summary{
			{ID: "c-worker", Names: []string{"/worker-1"}, Image: "worker:dev", ImageID: "sha256:worker", State: container.StateExited,
				SizeRw: 48 * MB, Created: old.Unix()},
		},
		FinishedAt: map[string]string{"c-worker": old.Format(time.RFC3339Nano)},
		Images: []image.Summary{
			{ID: "sha256:worker", RepoTags: []string{"worker:dev"}, Size: 500 * MB},
			{ID: "sha256:9f3c1a7be2d40000", RepoTags: []string{"<none>:<none>"}, Size: 1_000 * MB, Created: old.Unix()},
		},
		Volumes: []volume.Volume{
			{Name: "old_redis", UsageData: &volume.UsageData{Size: 200 * MB}, CreatedAt: old.Format(time.RFC3339)},
		},
	}
}

// TestExcludedAndIgnoredHoldEverywhere proves what DESIGN.md asks of the
// ignore list and of exclude, paths and target entries alike: what they
// name is dropped by every scanner, never enters a plan, and is refused by
// apply, dry and real, when a plan written before the entries were added
// names it; nothing it names is removed, and the rest of the plan runs.
func TestExcludedAndIgnoredHoldEverywhere(t *testing.T) {
	a, out := testApp(t, "")
	code := filepath.Join(a.home, "Code")
	fixtureTree(t, code)
	fake := ignoreDocker()
	a.docker = func() (dockerx.API, error) { return fake, nil }
	work := t.TempDir()
	report, planPath := filepath.Join(work, "report.json"), filepath.Join(work, "plan.json")

	// A plan written before the entries exist names everything.
	require.NoError(t, execute(a, "scan", code, "--all", "--json", "--min-size", "0", "--out", report))
	require.NoError(t, execute(a, "select", "--report", report, "--preset", "aggressive", "--out", planPath))
	before, err := plan.Load(planPath)
	require.NoError(t, err)
	named := func(p *plan.Plan) []string {
		out := make([]string, 0, len(p.Actions))
		for _, act := range p.Actions {
			out = append(out, act.Path+" "+act.Target)
		}
		return out
	}
	venv, target := filepath.Join(code, "py", ".venv"), filepath.Join(code, "crate", "target")
	for _, want := range []string{venv, target, "c-worker", "old_redis"} {
		require.Contains(t, strings.Join(named(before), "\n"), want)
	}

	// exclude names a path and a volume; the ignore list a folder that
	// holds a finding and the container, by an entry the screen writes.
	writeConfig(t, a, "exclude = [\"~/Code/py/.venv\", \"docker:old_redis\"]\n")
	ig, err := config.EditIgnore(config.IgnorePath(a.configPath), a.home, []string{filepath.Join(code, "crate"), "docker:c-worker"}, nil)
	require.NoError(t, err)
	require.Len(t, ig.Entries, 2)
	dropped := func(fs []finding.Finding) {
		t.Helper()
		for _, f := range fs {
			require.False(t, strings.HasPrefix(f.Path, venv) || strings.HasPrefix(f.Path, filepath.Join(code, "crate")), f.Path)
			require.NotContains(t, []string{"c-worker", "old_redis"}, f.Target)
		}
	}

	// Every scanner drops them: scan, here and the report select reads.
	out.Reset()
	require.NoError(t, execute(a, "scan", code, "--all", "--json", "--min-size", "0"))
	var r finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	dropped(r.Findings)
	require.True(t, slices.ContainsFunc(r.Findings, func(f finding.Finding) bool { return f.Target == "sha256:9f3c1a7be2d40000" }),
		"what no entry names is still offered")
	out.Reset()
	require.NoError(t, execute(a, "here", code, "--json"))
	var h hereOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &h))
	dropped(h.Findings)

	// They never enter a plan, even from the report written before them.
	after := filepath.Join(work, "after.json")
	require.NoError(t, execute(a, "select", "--report", report, "--preset", "aggressive", "--out", after))
	p, err := plan.Load(after)
	require.NoError(t, err)
	for _, act := range p.Actions {
		require.False(t, strings.HasPrefix(act.Path, venv) || strings.HasPrefix(act.Path, filepath.Join(code, "crate")), act.Path)
		require.NotContains(t, []string{"c-worker", "old_redis"}, act.Target)
	}

	// apply refuses them in the plan written before, dry and real.
	homeBefore := snapshot(t, a.home)
	out.Reset()
	err = execute(a, "apply", planPath, "--dry-run")
	require.ErrorContains(t, err, "4 of ")
	for _, want := range []string{
		"refusing " + venv + ": it is an exclude entry of your configuration",
		"refusing " + target + ": it lies inside " + filepath.Join(code, "crate") + ", which is on your ignore list (" + ig.Path + ")",
		"refusing c-worker: the entry docker:c-worker on your ignore list",
		"refusing old_redis: the exclude entry docker:old_redis of your configuration names it",
	} {
		require.Contains(t, out.String(), want)
	}
	require.Equal(t, homeBefore, snapshot(t, a.home), "the dry run removed nothing")

	out.Reset()
	require.Error(t, execute(a, "apply", planPath, "--yes", "--keep-going"))
	require.Contains(t, out.String(), "which is on your ignore list")
	require.DirExists(t, venv)
	require.DirExists(t, target)
	removed := fake.Removed()
	require.NotContains(t, removed, "c-worker")
	require.NotContains(t, removed, "old_redis")
	require.NoDirExists(t, filepath.Join(code, "web", "node_modules"), "what no entry names is removed")

	// The ignore list's count and path are in reclaim scanners.
	out.Reset()
	require.NoError(t, execute(a, "scanners"))
	require.Contains(t, out.String(), "Ignore list: 2 entries in "+ig.Path+".")
	data, err := os.ReadFile(ig.Path)
	require.NoError(t, err)
	require.Contains(t, string(data), "~/Code/crate\ndocker:c-worker\n", "the list is written the way the screen shows paths")
}
