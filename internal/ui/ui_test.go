package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func report() *finding.Report {
	return &finding.Report{
		Version: 1,
		Findings: []finding.Finding{
			{Scanner: "node_modules", Category: finding.CategoryProject, Tier: finding.TierA, Path: "/Users/me/Code/app/node_modules",
				Target: "/Users/me/Code/app/node_modules", Project: "/Users/me/Code/app", Size: 2_900_000_000, LastUsed: now.Add(-21 * 24 * time.Hour), Action: finding.ActionRemovePath},
			{Scanner: "dist", Category: finding.CategoryProject, Tier: finding.TierC, Path: "/Users/me/Code/lib/dist",
				Target: "/Users/me/Code/lib/dist", Project: "/Users/me/Code/lib", Size: 5_000_000, Action: finding.ActionRemovePath, Warning: "may be the only copy"},
			{Scanner: "pycache", Category: finding.CategoryProject, Tier: finding.TierA, Path: "/tmp/x/__pycache__", Target: "/tmp/x/__pycache__", Size: 1000, Action: finding.ActionRemovePath},
			{Scanner: "homebrew-cache", Category: finding.CategoryPackageCache, Tier: finding.TierB, Path: "/Users/me/Library/Caches/Homebrew",
				Target: "/Users/me/Library/Caches/Homebrew", Size: 1_200_000_000, Action: finding.ActionRunCommand, Command: []string{"brew", "cleanup", "-s"}},
			{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierA, Target: "sha256:abc", Name: "dangling image abc", Size: 300_000_000, Action: finding.ActionDockerRemoveImage},
		},
		Warnings: []finding.Warning{{Scanner: "projects", Path: "/Users/me/Code/locked", Message: "not scanned: permission denied"}},
		Notes:    []string{"Docker runs in OrbStack."},
	}
}

func TestReportTable(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "/Users/me", now).Report(report())
	out := buf.String()

	require.NotContains(t, out, "\x1b[", "no colors when the output is not a terminal")
	for _, want := range []string{
		"Project artifacts  2.9 GB  3 items",
		"~/Code/app  2.9 GB  active 3w ago",
		"A    2.9 GB  node_modules    node_modules",
		"(not in a project)",
		"! may be the only copy",
		"Package caches  1.2 GB  1 item",
		"~/Library/Caches/Homebrew  via brew cleanup -s",
		"Docker  300.0 MB  1 item",
		"dangling image abc",
		"Total reclaimable: 4.4 GB in 5 items (A 3.2 GB, B 1.2 GB, C 5.0 MB)",
		"Nothing was removed.",
		"Warnings (1)",
		"projects: ~/Code/locked: not scanned: permission denied",
		"Note: Docker runs in OrbStack.",
	} {
		require.Contains(t, out, want)
	}
	// Projects are ordered by size and loose findings come last.
	require.Less(t, strings.Index(out, "~/Code/app"), strings.Index(out, "~/Code/lib"))
	require.Less(t, strings.Index(out, "~/Code/lib"), strings.Index(out, "(not in a project)"))
}

func TestEmptyReport(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "", now).Report(&finding.Report{})
	require.Contains(t, buf.String(), "Nothing reclaimable found.")
}

func TestNumbered(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "/Users/me", now).Numbered(report().Findings)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 7)
	require.Equal(t, "#  T      SIZE  SCANNER         ITEM", lines[0])
	require.True(t, strings.HasPrefix(lines[1], "1. A"))
	require.Equal(t, strings.Repeat(" ", 32)+"! may be the only copy", lines[3], "the warning sits under the item")
	require.True(t, strings.HasPrefix(lines[6], "5. A"))
}

func TestHere(t *testing.T) {
	root := &fsx.Node{Name: "app", Path: "/Users/me/Code/app", IsDir: true, Size: 3_000_000_000, Children: []*fsx.Node{
		{Name: "node_modules", Path: "/Users/me/Code/app/node_modules", IsDir: true, Size: 2_900_000_000},
		{Name: "packages", Path: "/Users/me/Code/app/packages", IsDir: true, Size: 90_000_000, Children: []*fsx.Node{
			{Name: "web", Path: "/Users/me/Code/app/packages/web", IsDir: true, Size: 90_000_000},
		}},
		{Name: "README.md", Path: "/Users/me/Code/app/README.md", Size: 1200},
	}}
	fs := []finding.Finding{
		{Scanner: "node_modules", Tier: finding.TierA, Path: "/Users/me/Code/app/node_modules", Size: 2_900_000_000, Restore: "npm install"},
		{Scanner: "next", Tier: finding.TierA, Path: "/Users/me/Code/app/packages/web/.next", Size: 80_000_000},
	}
	var buf bytes.Buffer
	New(&buf, "/Users/me", now).Here(root, fs, nil, nil)
	out := buf.String()
	for _, want := range []string{
		"~/Code/app  3.0 GB total, 3.0 GB reclaimable",
		"   2.9 GB     2.9 GB A  node_modules/  node_modules",
		"  90.0 MB    80.0 MB    packages/",
		"  90.0 MB    80.0 MB      web/",
		"   1.2 kB          -    README.md",
		"Reclaimable (2 items, 3.0 GB)",
		"~/Code/app/packages/web/.next",
		"restore: npm install",
	} {
		require.Contains(t, out, want)
	}
}

func TestPlanPreview(t *testing.T) {
	p := &plan.Plan{Scanned: now.Add(-2 * time.Hour), Actions: []plan.Action{
		{Action: finding.ActionRemovePath, Path: "/Users/me/Code/app/node_modules", Tier: finding.TierA, Size: 100, Scanner: "node_modules"},
		{Action: finding.ActionRunCommand, Path: "/Users/me/.npm/_cacache", Command: []string{"npm", "cache", "clean", "--force"}, Tier: finding.TierB, Size: 50, Warning: "slow next install"},
		{Action: finding.ActionDockerRemoveImage, Target: "sha256:x", Name: "image busybox:latest", Tier: finding.TierB, Size: 7},
		{Action: finding.ActionDockerPruneBuildCache, Target: "docker-build-cache", Name: "build cache", Tier: finding.TierA, Size: 9},
	}}
	var buf bytes.Buffer
	New(&buf, "/Users/me", now).Plan(p)
	out := buf.String()
	for _, want := range []string{
		"Plan: 166 B in 4 actions, scanned 2h ago",
		"1. A     100 B  remove ~/Code/app/node_modules  node_modules",
		"2. B      50 B  run npm cache clean --force  cleans ~/.npm/_cacache",
		"\n                ! slow next install",
		"docker remove image busybox:latest",
		"docker prune build cache",
	} {
		require.Contains(t, out, want)
	}
}

func TestOutcome(t *testing.T) {
	a := plan.Action{Action: finding.ActionRemovePath, Path: "/Users/me/Code/app/node_modules", Scanner: "node_modules"}
	img := plan.Action{Action: finding.ActionDockerRemoveImage, Target: "sha256:x", Name: "image busybox:latest"}
	var buf bytes.Buffer
	p := New(&buf, "/Users/me", now)
	p.Outcome(0, 12, apply.Outcome{Action: a, Status: apply.StatusDone, Freed: 2_900_000_000})
	p.Outcome(1, 12, apply.Outcome{Action: img, Status: apply.StatusDone, Freed: 7_000_000, Estimated: true})
	p.Outcome(10, 12, apply.Outcome{Action: a, Status: apply.StatusGone})
	p.Outcome(11, 12, apply.Outcome{Action: a, Status: apply.StatusFailed, Err: errors.New("refusing it")})
	require.Equal(t, strings.Join([]string{
		"[ 1/12] done        2.9 GB  remove ~/Code/app/node_modules  node_modules",
		"[ 2/12] done       ~7.0 MB  docker remove image busybox:latest",
		"[11/12] gone                remove ~/Code/app/node_modules  node_modules",
		"[12/12] failed              remove ~/Code/app/node_modules  node_modules",
		"        refusing it",
	}, "\n")+"\n", buf.String())
}

func TestPath(t *testing.T) {
	p := New(&bytes.Buffer{}, "/Users/me", now)
	require.Equal(t, "~", p.Path("/Users/me"))
	require.Equal(t, "~/x", p.Path("/Users/me/x"))
	require.Equal(t, "/Users/meow", p.Path("/Users/meow"))
	require.Equal(t, "", p.Path(""))
}
