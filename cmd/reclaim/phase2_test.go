package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
)

// sparse creates a file with a large apparent size that uses no disk space.
func sparse(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

// machineFixture builds a fake home with projects, package and app caches,
// installed apps and a Downloads folder.
func machineFixture(t *testing.T, a *app) {
	t.Helper()
	fixtureTree(t, filepath.Join(a.home, "Code"))
	h := a.home
	sparse(t, filepath.Join(h, ".cache", "thumbnails", "large", "x.png"), 3_000_000)
	sparse(t, filepath.Join(h, ".cache", "some-app-updater", "update.AppImage"), 2_000_000)
	sparse(t, filepath.Join(h, ".cache", "huggingface", "hub", "model.bin"), 5_000_000)
	sparse(t, filepath.Join(h, ".cache", "ms-playwright", "chromium-1234", "chrome"), 4_000_000)
	sparse(t, filepath.Join(h, ".cache", "tiny", "x"), 100)
	sparse(t, filepath.Join(h, ".npm", "_cacache", "index"), 1_000_000)
	require.NoError(t, os.MkdirAll(filepath.Join(h, "Applications", "Zen.app", "Contents"), 0o755))
	dl := filepath.Join(h, "Downloads")
	sparse(t, filepath.Join(dl, "zen.macos-universal.dmg"), 200_000_000)
	sparse(t, filepath.Join(dl, "project-1.2.zip"), 20_000_000)
	sparse(t, filepath.Join(dl, "project-1.2", "README"), 10)
	sparse(t, filepath.Join(dl, "talk.mov"), 3_000_000_000)
	old := time.Now().Add(-200 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dl, "talk.mov"), old, old))
	sparse(t, filepath.Join(dl, "notes.pdf"), 1000)
}

// TestEndToEndAggressivePreset scans a whole fake machine, writes a plan
// with tiers A and B, applies it and checks that exactly the planned paths
// are gone: project artifacts, package and app caches, and an installer. The
// archive with an extracted copy (tier C), the attention file and everything
// else remain.
func TestEndToEndAggressivePreset(t *testing.T) {
	a, out := testApp(t, "")
	machineFixture(t, a)
	before := snapshot(t, a.home)
	work := t.TempDir()
	reportPath := filepath.Join(work, "report.json")
	planPath := filepath.Join(work, "plan.json")

	require.NoError(t, execute(a, "scan", "--json", "--min-size", "0", "--out", reportPath))
	var report finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, snapshot(t, a.home), before, "scan must not modify anything")
	require.Equal(t, "scan", report.Scope.Command)
	require.False(t, report.Scope.Partial())
	require.Equal(t, []string{a.home, filepath.Join(a.home, "Code"), filepath.Join(a.home, "Downloads")}, report.Roots)

	got := map[string]string{}
	for _, f := range report.Findings {
		rel, err := filepath.Rel(a.home, f.Path)
		require.NoError(t, err)
		got[filepath.ToSlash(rel)] = f.Scanner + " " + string(f.Tier) + " " + string(f.Action)
	}
	for rel, want := range map[string]string{
		".cache/thumbnails":                 "user-caches B RemovePath",
		".cache/some-app-updater":           "electron-updaters A RemovePath",
		".cache/ms-playwright":              "playwright B RemovePath",
		".npm/_cacache":                     "npm-cache B RemovePath",
		"Downloads/zen.macos-universal.dmg": "installers B RemovePath",
		"Downloads/project-1.2.zip":         "extracted-archives C RemovePath",
		"Downloads/talk.mov":                "old-downloads C None",
		"Code/web/node_modules":             "node_modules A RemovePath",
	} {
		require.Equal(t, want, got[rel], rel)
	}
	require.NotContains(t, got, ".cache/huggingface", "apps known to keep data in the cache folder are never offered")
	require.NotContains(t, got, ".cache/tiny")

	out.Reset()
	require.NoError(t, execute(a, "select", "--preset", "aggressive", "--report", reportPath, "--out", planPath))
	require.Contains(t, out.String(), "Report from reclaim scan --json --min-size=0 --out="+reportPath)
	p, err := plan.Load(planPath)
	require.NoError(t, err)
	planned := make([]string, 0, len(p.Actions))
	for _, act := range p.Actions {
		require.NotEqual(t, finding.TierC, act.Tier)
		planned = append(planned, act.Path)
	}

	out.Reset()
	require.NoError(t, execute(a, "apply", planPath, "--yes", "--log", filepath.Join(work, "apply.log")))
	require.NotContains(t, out.String(), "failed")
	var want []string
	for _, p := range before {
		abs := filepath.Join(a.home, filepath.FromSlash(p))
		gone := slices.ContainsFunc(planned, func(t string) bool { return abs == t || strings.HasPrefix(abs, t+string(filepath.Separator)) })
		if !gone {
			want = append(want, p)
		}
	}
	require.Equal(t, want, snapshot(t, a.home))
	for _, keep := range []string{"Downloads/talk.mov", "Downloads/project-1.2.zip", "Downloads/project-1.2/README", ".cache/huggingface/hub/model.bin", "Code/lib/dist/only-copy.js"} {
		require.FileExists(t, filepath.Join(a.home, keep))
	}
	for _, gone := range []string{"Downloads/zen.macos-universal.dmg", ".cache/thumbnails", ".cache/ms-playwright", ".npm/_cacache"} {
		require.NoFileExists(t, filepath.Join(a.home, gone))
		require.NoDirExists(t, filepath.Join(a.home, gone))
	}
	require.DirExists(t, filepath.Join(a.home, ".cache"))
	require.DirExists(t, filepath.Join(a.home, "Downloads"))
}

// TestSelectNumberedNeverOffersAttention selects every number of the
// numbered list and checks that the attention file is not in the plan.
func TestSelectNumberedNeverOffersAttention(t *testing.T) {
	a, out := testApp(t, "")
	machineFixture(t, a)
	require.NoError(t, execute(a, "scan", "--min-size", "0"))
	report, err := loadReport(a.lastReportPath())
	require.NoError(t, err)
	n := 0
	for _, f := range report.Findings {
		if f.Actionable() {
			n++
		}
	}
	require.Less(t, n, len(report.Findings))

	out.Reset()
	planPath := filepath.Join(t.TempDir(), "plan.json")
	a.stdin = strings.NewReader("1-" + strconv.Itoa(n) + "\n")
	require.NoError(t, execute(a, "select", "--out", planPath))
	require.Contains(t, out.String(), "-        3.0 GB  old-downloads", "attention items have no number and no tier")
	require.Contains(t, out.String(), "Attention, never removed")
	p, err := plan.Load(planPath)
	require.NoError(t, err)
	for _, act := range p.Actions {
		require.NotEqual(t, "old-downloads", act.Scanner)
	}

	a.stdin = strings.NewReader(strconv.Itoa(n+1) + "\n")
	a.in = nil
	require.ErrorContains(t, execute(a, "select", "--out", planPath), "out of range", "attention items have no number to pick")
}

func writeConfig(t *testing.T, a *app, text string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(a.configPath), 0o755))
	require.NoError(t, os.WriteFile(a.configPath, []byte(text), 0o644))
}

func TestConfigFile(t *testing.T) {
	a, out := testApp(t, "")
	code := filepath.Join(a.home, "Code")
	fixtureTree(t, code)
	other := filepath.Join(a.home, "Elsewhere")
	writeTree(t, other, map[string]string{"app/package.json": "{}", "app/node_modules/x/i.js": strings.Repeat("x", 100)})
	writeConfig(t, a, `
roots = ["~/Code", "~/Elsewhere", "~/Missing"]
exclude = ["~/Code/web"]
min_size = "0"

[scanners]
disable = ["python-venv"]
`)
	require.NoError(t, execute(a, "scan", "--json"))
	var r finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Equal(t, []string{code, other}, r.Scope.Roots, "a missing root is skipped")
	require.Contains(t, a.stderr.(interface{ String() string }).String(), "config root skipped")
	require.NotContains(t, a.stderr.(interface{ String() string }).String(), "excludes nothing")
	var paths []string
	for _, f := range r.Findings {
		if f.Category == finding.CategoryProject {
			rel, err := filepath.Rel(a.home, f.Path)
			require.NoError(t, err)
			paths = append(paths, filepath.ToSlash(rel))
		}
	}
	require.ElementsMatch(t, []string{
		"Code/lib/dist", "Code/stray/node_modules", "Code/py/pkg/__pycache__", "Code/crate/target", "Elsewhere/app/node_modules",
	}, paths, "the excluded project and the disabled scanner are left out, min_size 0 keeps tiny findings")

	// Flags override the file.
	out.Reset()
	require.NoError(t, execute(a, "scan", "--json", "--min-size", "1MB"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Empty(t, r.Findings)
	out.Reset()
	require.NoError(t, execute(a, "scan", code, "--json", "--category", "python"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Len(t, r.Findings, 2, "selecting the ecosystem of a disabled scanner runs it")
	require.Contains(t, []string{r.Findings[0].Scanner, r.Findings[1].Scanner}, "python-venv")

	// here honors the exclusions and the disabled scanners too.
	out.Reset()
	require.NoError(t, execute(a, "here", code, "--json"))
	var h hereOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &h))
	for _, f := range h.Findings {
		require.NotEqual(t, "python-venv", f.Scanner)
		require.False(t, strings.HasPrefix(f.Path, filepath.Join(code, "web")), f.Path)
	}

	out.Reset()
	require.NoError(t, execute(a, "scanners"))
	lines := strings.Split(out.String(), "\n")
	require.Contains(t, lines[0], "STATUS")
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "python-venv "):
			require.Contains(t, l, " disabled ")
		case strings.HasPrefix(l, "node_modules "):
			require.Contains(t, l, " enabled ")
		}
	}
	require.Contains(t, out.String(), "Disabled in "+a.configPath)

	// --config reads another file.
	other2 := filepath.Join(t.TempDir(), "other.toml")
	require.NoError(t, os.WriteFile(other2, []byte("[scanners]\ndisable = [\"node_modules\"]\n"), 0o644))
	out.Reset()
	require.NoError(t, execute(a, "scanners", "--config", other2))
	require.Contains(t, out.String(), "Disabled in "+other2)
	require.ErrorContains(t, execute(a, "scanners", "--config", filepath.Join(t.TempDir(), "typo.toml")), "--config", "a file named on the command line must exist")
	a.configPath = filepath.Join(a.home, ".config", "reclaim", "config.toml")

	writeConfig(t, a, "exclude = [\"~/Code/nope\"]\n")
	require.NoError(t, execute(a, "scan", code, "--json"))
	require.Contains(t, a.stderr.(interface{ String() string }).String(), "config exclude "+filepath.Join(code, "nope")+" does not exist")

	writeConfig(t, a, "[scanners]\ndisable = [\"nope\"]\n")
	require.ErrorContains(t, execute(a, "scan"), `config [scanners] disable: unknown category, ecosystem or scanner "nope"`)
	writeConfig(t, a, "rootz = []\n")
	err := execute(a, "scan")
	require.ErrorContains(t, err, `unknown key "rootz"`)
	require.ErrorContains(t, err, a.configPath)
	require.ErrorContains(t, execute(a, "scanners"), "rootz")
}

func TestMarkdownOutput(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	require.NoError(t, execute(a, "scan", root, "--md", "--min-size", "0"))
	md := out.String()
	require.True(t, strings.HasPrefix(md, "# Reclaimable space\n"))
	require.Contains(t, md, "## Project artifacts (")
	require.Contains(t, md, "### `~/Code/web` (")
	require.Contains(t, md, "| A | 2.0 kB | node\\_modules | `node_modules` |  |")
	require.NotContains(t, md, "\x1b[")
	require.NotContains(t, md, "Next:")

	out.Reset()
	require.NoError(t, execute(a, "here", filepath.Join(root, "web"), "--md"))
	md = out.String()
	require.True(t, strings.HasPrefix(md, "# `~/Code/web`\n"), md)
	require.Contains(t, md, "| Size | Reclaimable | Tier | Entry |")
	require.Contains(t, md, "| A node\\_modules | `node_modules/` |")
	require.Contains(t, md, "## Reclaimable (3 items,")

	require.ErrorContains(t, execute(a, "scan", root, "--md", "--json"), "cannot be used together")
	require.ErrorContains(t, execute(a, "here", root, "--md", "--json"), "cannot be used together")
}

// TestHereThenSelectSaysWhereTheReportCameFrom covers the supported flow of
// running here and then select: the last report is the folder's, and select
// says so before offering anything.
func TestHereThenSelectSaysWhereTheReportCameFrom(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	web := filepath.Join(root, "web")
	require.NoError(t, execute(a, "here", web, "--depth", "2"))
	out.Reset()
	planPath := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, execute(a, "select", "--preset", "safe", "--out", planPath))
	first, _, _ := strings.Cut(out.String(), "\n")
	require.True(t, strings.HasPrefix(first, "Report from reclaim here "+web+" --depth=2, just now ("), first)
	require.Contains(t, first, ": 3 findings, ")
	require.Contains(t, first, "It covers only ~/Code/web, not the whole machine; run reclaim scan for everything.")

	report, err := loadReport(a.lastReportPath())
	require.NoError(t, err)
	require.Equal(t, &finding.Scope{Command: "here", Args: []string{web, "--depth=2"}, Paths: []string{web}, Roots: []string{web}}, report.Scope)
}
