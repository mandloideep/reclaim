package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/ui"
)

// testApp returns an app that touches nothing outside temp directories: a
// fake home, a private state folder, no external tools and no Docker.
func testApp(t *testing.T, stdin string) (*app, *bytes.Buffer) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	var out bytes.Buffer
	return &app{
		stdin:        strings.NewReader(stdin),
		stdout:       &out,
		stderr:       &bytes.Buffer{},
		home:         home,
		goos:         "linux",
		host:         "test-host",
		stateDir:     t.TempDir(),
		configPath:   filepath.Join(home, ".config", "reclaim", "config.toml"),
		applications: []string{filepath.Join(home, "Applications")},
		interactive:  func() bool { return false },
		getenv:       func(string) string { return "" },
		exec:         &execx.Fake{},
		docker:       func() (dockerx.API, error) { return nil, errors.New("docker disabled in tests") },
		now:          time.Now,
	}, &out
}

func execute(a *app, args ...string) error {
	cmd := newRootCmd(a)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// snapshot lists every path below root, relative and slash separated.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	}))
	slices.Sort(out)
	return out
}

func fixtureTree(t *testing.T, root string) {
	t.Helper()
	writeTree(t, root, map[string]string{
		"web/.git/HEAD":                    "ref: refs/heads/main",
		"web/package.json":                 `{"scripts":{"build":"vite build"}}`,
		"web/src/index.js":                 "console.log(1)",
		"web/node_modules/react/index.js":  strings.Repeat("r", 2000),
		"web/node_modules/.bin/vite":       "#!/bin/sh",
		"web/dist/app.js":                  strings.Repeat("d", 500),
		"web/.next/cache/x":                strings.Repeat("n", 300),
		"lib/package.json":                 `{"name":"lib"}`,
		"lib/dist/only-copy.js":            "precious",
		"stray/node_modules/z/index.js":    "z",
		"py/pyproject.toml":                "[project]",
		"py/.venv/pyvenv.cfg":              "home = /usr/bin",
		"py/.venv/lib/site-packages/x.py":  strings.Repeat("v", 800),
		"py/pkg/mod.py":                    "x = 1",
		"py/pkg/__pycache__/mod.cpython.p": "pyc",
		"crate/Cargo.toml":                 "[package]",
		"crate/src/main.rs":                "fn main() {}",
		"crate/target/debug/app":           strings.Repeat("b", 1500),
		"notes/todo.md":                    "nothing to see",
	})
}

// TestEndToEndSafePreset scans a fixture tree, writes a plan with the safe
// preset, applies it with --yes and checks that exactly the tier A folders
// are gone and everything else, including a decoy outside the root, remains.
func TestEndToEndSafePreset(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	decoy := filepath.Join(a.home, "elsewhere", "app")
	writeTree(t, decoy, map[string]string{"package.json": "{}", "node_modules/x/index.js": "decoy"})
	homeBefore := snapshot(t, a.home)

	work := t.TempDir()
	reportPath := filepath.Join(work, "report.json")
	planPath := filepath.Join(work, "plan.json")
	logPath := filepath.Join(work, "apply.log")

	// 1. Scan.
	require.NoError(t, execute(a, "scan", root, "--json", "--min-size", "0", "--out", reportPath))
	var report finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, finding.ReportVersion, report.Version)
	require.Equal(t, []string{root}, report.Roots)
	tiers := map[string]finding.Tier{}
	for _, f := range report.Findings {
		rel, err := filepath.Rel(root, f.Path)
		require.NoError(t, err)
		tiers[filepath.ToSlash(rel)] = f.Tier
	}
	require.Equal(t, map[string]finding.Tier{
		"web/node_modules":   finding.TierA,
		"web/dist":           finding.TierA,
		"web/.next":          finding.TierA,
		"lib/dist":           finding.TierC,
		"stray/node_modules": finding.TierB,
		"py/.venv":           finding.TierA,
		"py/pkg/__pycache__": finding.TierA,
		"crate/target":       finding.TierA,
	}, tiers)
	require.Equal(t, homeBefore, snapshot(t, a.home), "scan must not modify anything")
	saved, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.JSONEq(t, out.String(), string(saved))

	// 2. Select the safe preset.
	out.Reset()
	require.NoError(t, execute(a, "select", "--preset", "safe", "--report", reportPath, "--out", planPath))
	require.Contains(t, out.String(), "Wrote "+planPath+": 6 actions")
	p, err := plan.Load(planPath)
	require.NoError(t, err)
	require.Len(t, p.Actions, 6)
	for _, act := range p.Actions {
		require.Equal(t, finding.TierA, act.Tier)
	}

	// 3. Apply without a prompt.
	before := snapshot(t, root)
	out.Reset()
	require.NoError(t, execute(a, "apply", planPath, "--yes", "--log", logPath))
	require.Contains(t, out.String(), "Freed")
	require.Contains(t, out.String(), "[6/6] done")

	// 4. Exactly the planned folders and their contents are gone.
	removed := []string{"web/node_modules", "web/dist", "web/.next", "py/.venv", "py/pkg/__pycache__", "crate/target"}
	var want []string
	for _, rel := range before {
		gone := slices.ContainsFunc(removed, func(r string) bool { return rel == r || strings.HasPrefix(rel, r+"/") })
		if !gone {
			want = append(want, rel)
		}
	}
	require.Equal(t, want, snapshot(t, root))
	require.FileExists(t, filepath.Join(root, "lib", "dist", "only-copy.js"))
	require.FileExists(t, filepath.Join(root, "stray", "node_modules", "z", "index.js"))
	require.FileExists(t, filepath.Join(decoy, "node_modules", "x", "index.js"))

	// 5. The log has a line per action with bytes freed.
	logData, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	require.Len(t, lines, 8)
	var total float64
	for _, l := range lines[1:7] {
		var e map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &e))
		require.Equal(t, "done", e["status"])
		total += e["freed"].(float64)
	}
	require.Positive(t, total)

	// 5. Applying the same plan again finds everything already gone.
	out.Reset()
	require.NoError(t, execute(a, "apply", planPath, "--yes", "--log", logPath))
	require.Equal(t, 6, strings.Count(out.String(), "] gone"))
}

func TestApplyAsksForConfirmation(t *testing.T) {
	for _, tt := range []struct {
		input   string
		removed bool
	}{
		{input: "no\n", removed: false},
		{input: "", removed: false},
		{input: "YES\n", removed: false},
		{input: "yes\n", removed: true},
	} {
		t.Run(strings.TrimSpace(tt.input), func(t *testing.T) {
			a, out := testApp(t, tt.input)
			root := filepath.Join(a.home, "Code")
			fixtureTree(t, root)
			work := t.TempDir()
			require.NoError(t, execute(a, "scan", root, "--min-size", "0", "--out", filepath.Join(work, "r.json")))
			require.NoError(t, execute(a, "select", "--preset", "safe", "--report", filepath.Join(work, "r.json"), "--out", filepath.Join(work, "p.json")))
			out.Reset()
			err := execute(a, "apply", filepath.Join(work, "p.json"), "--log", filepath.Join(work, "log"))
			require.Contains(t, out.String(), "Type yes to remove these 6 items")
			target := filepath.Join(root, "web", "node_modules")
			if tt.removed {
				require.NoError(t, err)
				require.NoDirExists(t, target)
			} else {
				require.ErrorContains(t, err, "nothing was removed")
				require.DirExists(t, target)
			}
		})
	}
}

func TestApplyRefusesStalePlans(t *testing.T) {
	a, _ := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	work := t.TempDir()
	require.NoError(t, execute(a, "scan", root, "--min-size", "0", "--out", filepath.Join(work, "r.json")))
	require.NoError(t, execute(a, "select", "--preset", "safe", "--report", filepath.Join(work, "r.json"), "--out", filepath.Join(work, "p.json")))

	a.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	err := execute(a, "apply", filepath.Join(work, "p.json"), "--yes", "--log", filepath.Join(work, "log"))
	require.ErrorContains(t, err, "--stale-ok")
	require.DirExists(t, filepath.Join(root, "web", "node_modules"))

	require.NoError(t, execute(a, "apply", filepath.Join(work, "p.json"), "--yes", "--stale-ok", "--log", filepath.Join(work, "log")))
	require.NoDirExists(t, filepath.Join(root, "web", "node_modules"))
}

func TestSelectInteractive(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	require.NoError(t, execute(a, "scan", root, "--min-size", "0"))
	report, err := loadReport(a.lastReportPath())
	require.NoError(t, err)
	n := len(report.Findings)
	// The list is numbered in its grouped order, which the plain printer
	// reproduces.
	numbered := ui.NewPlain(io.Discard, a.home, time.Now()).Numbered(report.Findings)
	var tierC int
	for i, f := range numbered {
		if f.Tier == finding.TierC {
			tierC = i + 1
		}
	}
	require.NotZero(t, tierC)

	planPath := filepath.Join(t.TempDir(), "plan.json")
	a.stdin = strings.NewReader("1-" + strconv.Itoa(n) + "\n")
	a.in = nil
	out.Reset()
	require.NoError(t, execute(a, "select", "--out", planPath))
	require.Contains(t, out.String(), "Report from reclaim scan "+root+" --min-size=0, just now (")
	require.Contains(t, out.String(), "It covers only ~/Code, not the whole machine")
	require.Contains(t, out.String(), "1. ")
	require.Contains(t, out.String(), "Skipped tier C items "+strconv.Itoa(tierC))
	p, err := plan.Load(planPath)
	require.NoError(t, err)
	require.Len(t, p.Actions, n-1, "a range never selects tier C")

	a.stdin = strings.NewReader(strconv.Itoa(tierC) + "\n")
	a.in = nil
	require.NoError(t, execute(a, "select", "--out", planPath))
	p, err = plan.Load(planPath)
	require.NoError(t, err)
	require.Len(t, p.Actions, 1)
	require.Equal(t, finding.TierC, p.Actions[0].Tier, "tier C can be picked on its own")

	a.stdin = strings.NewReader("\n")
	a.in = nil
	out.Reset()
	require.NoError(t, execute(a, "select", "--out", filepath.Join(t.TempDir(), "none.json")))
	require.Contains(t, out.String(), "Nothing selected")

	a.stdin = strings.NewReader("99\n")
	a.in = nil
	require.ErrorContains(t, execute(a, "select", "--out", planPath), "out of range")
}

func TestParseSelection(t *testing.T) {
	fs := []finding.Finding{{Tier: finding.TierA}, {Tier: finding.TierC}, {Tier: finding.TierB}, {Tier: finding.TierA}, {Tier: finding.TierC}}
	tests := []struct {
		in          string
		want        []int
		wantSkipped []int
		wantErr     string
	}{
		{in: "", want: nil},
		{in: "1", want: []int{0}},
		{in: "1,4", want: []int{0, 3}},
		{in: "1-4", want: []int{0, 2, 3}, wantSkipped: []int{2}},
		{in: "1-5 2", want: []int{0, 1, 2, 3}, wantSkipped: []int{5}},
		{in: " 3 , 3 ", want: []int{2}},
		{in: "0", wantErr: "out of range"},
		{in: "6", wantErr: "out of range"},
		{in: "4-2", wantErr: "backwards"},
		{in: "a", wantErr: "not a number"},
		{in: "1-", wantErr: "not a number"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, skipped, err := parseSelection(tt.in, fs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantSkipped, skipped)
		})
	}
}

func TestSelectWithoutReport(t *testing.T) {
	a, _ := testApp(t, "")
	err := execute(a, "select", "--preset", "safe")
	require.ErrorContains(t, err, "run reclaim scan first")
}

func TestHere(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	web := filepath.Join(root, "web")

	require.NoError(t, execute(a, "here", web, "--json"))
	var got hereOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, hereVersion, got.Version)
	require.Equal(t, web, got.Path)
	require.Positive(t, got.Size)
	require.Equal(t, "node_modules", got.Children[0].Name, "largest first")
	require.Empty(t, got.Children[0].Children, "depth one shows direct children only")
	paths := make([]string, 0, len(got.Findings))
	for _, f := range got.Findings {
		paths = append(paths, f.Path)
	}
	require.ElementsMatch(t, []string{filepath.Join(web, "node_modules"), filepath.Join(web, "dist"), filepath.Join(web, ".next")}, paths)

	out.Reset()
	reportPath := filepath.Join(t.TempDir(), "here.json")
	require.NoError(t, execute(a, "here", web, "--depth", "2", "--out", reportPath))
	require.Contains(t, out.String(), "Reclaimable (3 items")
	require.Contains(t, out.String(), "node_modules/")
	require.NotContains(t, out.String(), "\x1b[")
	r, err := loadReport(reportPath)
	require.NoError(t, err)
	require.Equal(t, []string{web}, r.Roots)
	require.Len(t, r.Findings, 3)

	require.ErrorContains(t, execute(a, "here", filepath.Join(root, "missing")), "missing")
	require.ErrorContains(t, execute(a, "here", web, "--depth", "0"), "at least 1")
}

func TestScanTableAndFlags(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)

	require.NoError(t, execute(a, "scan", root, "--min-size", "0", "--tier", "A"))
	require.Contains(t, out.String(), "Project artifacts")
	require.Contains(t, out.String(), "~/Code/web")
	require.NotContains(t, out.String(), "only-copy")
	require.NotContains(t, out.String(), "stray")

	out.Reset()
	require.NoError(t, execute(a, "scan", root, "--json", "--min-size", "0", "--category", "python"))
	var r finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Len(t, r.Findings, 2)

	out.Reset()
	require.NoError(t, execute(a, "scan", root, "--json", "--min-size", "1MB"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Empty(t, r.Findings)

	out.Reset()
	require.NoError(t, execute(a, "scan", root, "--json", "--min-size", "0", "--stale", "30d"))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Empty(t, r.Findings, "every fixture project was just written")

	require.ErrorContains(t, execute(a, "scan", root, "--category", "nope"), "unknown category")
	require.ErrorContains(t, execute(a, "scan", root, "--tier", "Z"), "unknown tier")
	require.ErrorContains(t, execute(a, "scan", root, "--min-size", "lots"), "--min-size")
	require.ErrorContains(t, execute(a, "scan", root, "--stale", "never"), "--stale")
	require.ErrorContains(t, execute(a, "scan", root, "--docker-label", "=x"), "--docker-label")
	require.ErrorContains(t, execute(a, "scan", root, "--category", "docker"), "no scanners selected")
}

func TestScanInsideArtifactOffersNothing(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	writeTree(t, filepath.Join(root, "web", "node_modules", "left-pad"), map[string]string{
		"package.json":   `{"scripts":{"build":"tsc"}}`,
		"dist/index.js":  "compiled",
		"node_modules/x": "nested",
	})
	pkg := filepath.Join(root, "web", "node_modules", "left-pad")
	require.NoError(t, execute(a, "scan", pkg, "--json", "--min-size", "0"))
	var r finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Empty(t, r.Findings)
	require.Len(t, r.Notes, 1)
	require.Contains(t, r.Notes[0], "inside the artifact folder "+filepath.Join(root, "web", "node_modules"))
}

func TestScanAllReportsDockerUnavailable(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	require.NoError(t, execute(a, "scan", root, "--all", "--json", "--min-size", "0"))
	var r finding.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	require.Equal(t, []string{a.home, root}, r.Roots, "caches live under home, so home is a root next to the scan root")
	msgs := make([]string, 0, len(r.Warnings))
	for _, w := range r.Warnings {
		msgs = append(msgs, w.String())
	}
	require.Contains(t, strings.Join(msgs, "\n"), "docker: Docker is not available: docker disabled in tests")
}

func TestScannersAndVersion(t *testing.T) {
	a, out := testApp(t, "")
	require.NoError(t, execute(a, "scanners"))
	require.Contains(t, out.String(), "node_modules")
	require.Contains(t, out.String(), "homebrew-cache")
	require.Contains(t, out.String(), "docker")
	out.Reset()
	require.NoError(t, execute(a, "version"))
	require.True(t, strings.HasPrefix(out.String(), "reclaim "))
}

func TestDefaultRoots(t *testing.T) {
	a, _ := testApp(t, "")
	for _, d := range []string{"Code", "Downloads", "work"} {
		require.NoError(t, os.MkdirAll(filepath.Join(a.home, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(a.home, "src"), nil, 0o644))
	got := a.defaultRoots()
	require.Equal(t, []string{filepath.Join(a.home, "Code"), filepath.Join(a.home, "work"), filepath.Join(a.home, "Downloads")}, got)
}
