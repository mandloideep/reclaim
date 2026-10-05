package ui

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
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
		Created: now.Add(-2 * time.Hour),
		Findings: []finding.Finding{
			{Scanner: "node_modules", Category: finding.CategoryProject, Tier: finding.TierA, Path: "/Users/me/Code/app/node_modules",
				Target: "/Users/me/Code/app/node_modules", Project: "/Users/me/Code/app", Size: 2_900_000_000, LastUsed: now.Add(-21 * 24 * time.Hour), Action: finding.ActionRemovePath},
			{Scanner: "dist", Category: finding.CategoryProject, Tier: finding.TierC, Path: "/Users/me/Code/lib/dist",
				Target: "/Users/me/Code/lib/dist", Project: "/Users/me/Code/lib", Size: 5_000_000, Action: finding.ActionRemovePath, Warning: "may be the only copy"},
			{Scanner: "pycache", Category: finding.CategoryProject, Tier: finding.TierA, Path: "/tmp/x/__pycache__", Target: "/tmp/x/__pycache__", Size: 1000, Action: finding.ActionRemovePath},
			{Scanner: "homebrew-cache", Category: finding.CategoryPackageCache, Tier: finding.TierB, Path: "/Users/me/Library/Caches/Homebrew",
				Target: "/Users/me/Library/Caches/Homebrew", Size: 1_200_000_000, Action: finding.ActionRunCommand, Command: []string{"brew", "cleanup", "-s"}},
			{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierB, Target: "sha256:img", Name: "image node:20", Tags: []string{"node:20"}, Size: 900_000_000, Action: finding.ActionDockerRemoveImage},
			{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierA, Target: "sha256:abc", Name: "dangling image abc", Size: 300_000_000, Action: finding.ActionDockerRemoveImage},
			{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierB, Target: "vol", Name: "volume data", Size: 200_000_000, Action: finding.ActionDockerRemoveVolume},
			{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierA, Target: "docker-build-cache", Name: "build cache (3 unused records)", Size: 100_000_000, Action: finding.ActionDockerPruneBuildCache},
			{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierA, Target: "c1", Name: "container web-1 (web, exited)", Size: 1_000_000, Action: finding.ActionDockerRemoveContainer},
			{Scanner: "user-caches", Category: finding.CategoryAppCache, Tier: finding.TierA, Path: "/Users/me/Library/Caches/Google",
				Target: "/Users/me/Library/Caches/Google", Size: 700_000_000, Action: finding.ActionRemovePath, Kind: finding.KindDir},
			{Scanner: "installers", Category: finding.CategoryDownloads, Tier: finding.TierB, Path: "/Users/me/Downloads/Zen.dmg",
				Target: "/Users/me/Downloads/Zen.dmg", Size: 200_000_000, Action: finding.ActionRemovePath, Kind: finding.KindFile},
			{Scanner: "old-downloads", Category: finding.CategoryDownloads, Tier: finding.TierC, Path: "/Users/me/Downloads/talk.mov",
				Target: "/Users/me/Downloads/talk.mov", Size: 4_000_000_000, Action: finding.ActionNone, Kind: finding.KindFile, Warning: "not touched for 6mo"},
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
		"  ~/Code/app  2.9 GB  1 item  active 3w ago",
		"    A    2.9 GB  node_modules    node_modules",
		"(not in a project)",
		"! may be the only copy",
		"Package caches  1.2 GB  1 item",
		"~/Library/Caches/Homebrew  via brew cleanup -s",
		"Docker  1.5 GB  5 items",
		"  Unused images  900.0 MB  1 item",
		"  Dangling images  300.0 MB  1 item",
		"    A  300.0 MB  docker          dangling image abc",
		"App caches  700.0 MB  1 item",
		"  user-caches  700.0 MB  1 item",
		"Downloads  200.0 MB  1 item",
		"  Installers of installed apps  200.0 MB  1 item",
		"  Attention, never removed  4.0 GB  1 item  not counted, reclaim never removes these",
		"\n         4.0 GB  old-downloads", // no tier letter: it carries no action
		"! not touched for 6mo",
		"Total reclaimable: 6.5 GB in 11 items (A 4.0 GB, B 2.5 GB, C 5.0 MB)",
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
	// Docker kinds come in a fixed order, whatever their sizes.
	order := []string{"Build cache  ", "Stopped containers  ", "Dangling images  ", "Unused images  ", "Volumes  "}
	for i := 1; i < len(order); i++ {
		require.Less(t, strings.Index(out, order[i-1]), strings.Index(out, order[i]), order[i])
	}
	// Attention groups come after the groups that can be applied.
	require.Less(t, strings.Index(out, "Installers of installed apps"), strings.Index(out, "Attention, never removed"))
}

func TestEmptyReport(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "", now).Report(&finding.Report{})
	require.Contains(t, buf.String(), "Nothing reclaimable found.")
}

func TestNumbered(t *testing.T) {
	var buf bytes.Buffer
	r := report()
	numbered := New(&buf, "/Users/me", now).Numbered(r.Findings)
	out := buf.String()
	require.Len(t, numbered, 11, "attention findings get no number")
	for _, f := range numbered {
		require.True(t, f.Actionable())
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Equal(t, "#   T      SIZE  SCANNER         ITEM", lines[0])
	require.Contains(t, out, "1.  A    2.9 GB  node_modules    node_modules")
	require.Contains(t, out, "    ~/Code/app  2.9 GB, 1 item")
	require.Contains(t, out, "\n                                 ! may be the only copy", "the warning sits under the item")
	require.Contains(t, out, "    Attention, never removed  4.0 GB, 1 item, not counted, cannot be selected")
	require.Contains(t, out, "-        4.0 GB  old-downloads   ~/Downloads/talk.mov", "attention findings have a blank tier column")

	// The numbers follow the grouped order: the build cache is the first
	// Docker item even though it is the smallest image-sized finding.
	idx := slices.IndexFunc(numbered, func(f finding.Finding) bool { return f.Category == finding.CategoryDocker })
	require.Equal(t, finding.ActionDockerPruneBuildCache, numbered[idx].Action)
	require.Contains(t, out, strconv.Itoa(idx+1)+".  A  100.0 MB  docker          build cache (3 unused records)")
}

func TestMarkdown(t *testing.T) {
	var buf bytes.Buffer
	r := report()
	r.Scope = &finding.Scope{Command: "scan", Args: []string{"--min-size=0"}}
	r.Findings[0].Path = "/Users/me/Code/app/node_modules|x"
	r.Findings[0].Target = r.Findings[0].Path
	NewPlain(&buf, "/Users/me", now).ReportMarkdown(r)
	out := buf.String()
	require.NotContains(t, out, "\x1b[")
	for _, want := range []string{
		"# Reclaimable space\n",
		"Scanned 2026-10-05 10:00 UTC by `reclaim scan --min-size=0`: **6.5 GB** reclaimable in 11 items (A 4.0 GB, B 2.5 GB, C 5.0 MB).",
		"## Project artifacts (2.9 GB, 3 items)",
		"### `~/Code/app` (2.9 GB, 1 item)",
		"| Tier | Size | Scanner | Item | Note |\n| --- | ---: | --- | --- | --- |\n| A | 2.9 GB | node\\_modules | `node_modules\\|x` |  |",
		"| C | 5.0 MB | dist | `dist` | warning: may be the only copy |",
		"### (not in a project) (1.0 kB, 1 item)",
		"## Package caches (1.2 GB, 1 item)\n\n| Tier |",
		"| B | 1.2 GB | homebrew-cache | `~/Library/Caches/Homebrew` | via brew cleanup -s |",
		"### Unused images (900.0 MB, 1 item)",
		"| B | 900.0 MB | docker | image node:20 |  |",
		"### Attention, never removed (4.0 GB, 1 item, not counted, reclaim never removes these)",
		"|  | 4.0 GB | old-downloads | `~/Downloads/talk.mov` | warning: not touched for 6mo |",
		"## Warnings (1)\n\n- projects: ~/Code/locked: not scanned: permission denied",
		"## Notes\n\n- Docker runs in OrbStack.",
	} {
		require.Contains(t, out, want)
	}
	// Every table row has the same number of unescaped cell separators.
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "| ") {
			require.Equal(t, 6, strings.Count(strings.ReplaceAll(line, `\|`, ""), "|"), line)
		}
	}
}

func TestProvenance(t *testing.T) {
	p := New(&bytes.Buffer{}, "/Users/me", now)
	r := report()
	require.Contains(t, p.Provenance(r), "Report from an older reclaim that did not record its command, 2h ago")

	r.Scope = &finding.Scope{Command: "scan", Roots: []string{"/Users/me/Code"}}
	line := p.Provenance(r)
	require.True(t, strings.HasPrefix(line, "Report from reclaim scan, 2h ago ("), line)
	require.Contains(t, line, "11 findings and 1 for attention only, 6.5 GB reclaimable.")
	require.NotContains(t, line, "covers only")

	r.Scope = &finding.Scope{Command: "here", Args: []string{"."}, Paths: []string{"/Users/me/Code/app"}}
	line = p.Provenance(r)
	require.True(t, strings.HasPrefix(line, "Report from reclaim here ., 2h ago ("), line)
	require.Contains(t, line, "It covers only ~/Code/app, not the whole machine; run reclaim scan for everything.")

	r.Scope = &finding.Scope{Command: "scan", Args: []string{"/Users/me/Code/app", "/Users/me/Code/lib"}, Paths: []string{"/Users/me/Code/app", "/Users/me/Code/lib"}}
	require.Contains(t, p.Provenance(r), "It covers only ~/Code/app, ~/Code/lib, not the whole machine")
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
		{Scanner: "node_modules", Tier: finding.TierA, Path: "/Users/me/Code/app/node_modules", Size: 2_900_000_000, Action: finding.ActionRemovePath, Restore: "npm install"},
		{Scanner: "next", Tier: finding.TierA, Path: "/Users/me/Code/app/packages/web/.next", Size: 80_000_000, Action: finding.ActionRemovePath},
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
