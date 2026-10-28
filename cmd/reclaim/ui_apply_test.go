package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/tui"
)

// TestUIEndToEndRemovesOnlyTheTickedPaths drives the screens with the real
// backend over the end to end fixture, through Start, Scanning, Review,
// Confirm with its dry run, Removing and Done, and proves that only the
// ticked paths are gone: the assertion the --preset safe test makes,
// reached through the screen.
func TestUIEndToEndRemovesOnlyTheTickedPaths(t *testing.T) {
	a, out := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	decoy := filepath.Join(a.home, "elsewhere", "app")
	writeTree(t, decoy, map[string]string{"package.json": "{}", "node_modules/x/index.js": "decoy"})
	before := snapshot(t, root)
	homeBefore := snapshot(t, a.home)

	d := newDriver(t, a, []string{root}, uiFlags{minSize: "0", out: "plan.json"})
	d.key("enter")
	require.Contains(t, d.m.View(), "Review")
	d.key("c")
	view := d.m.View()
	require.Contains(t, view, "Confirm")
	require.Contains(t, view, "Type yes to remove these 6 items")

	// d runs every check and removes nothing.
	d.key("d")
	require.Contains(t, d.m.View(), "Checked without removing anything: 6 ok, 0 gone, 0 for you to run, 0 refused.")
	require.Equal(t, homeBefore, snapshot(t, a.home), "the dry run removed nothing")

	// Anything but yes removes nothing either.
	d.key("y", "e", "enter")
	require.Contains(t, d.m.View(), "Type yes, the whole word")
	require.Equal(t, homeBefore, snapshot(t, a.home))

	d.key("y", "e", "s", "enter")
	view = d.m.View()
	require.Contains(t, view, "Done")
	require.Contains(t, view, "6 removed")

	removed := []string{"web/node_modules", "web/dist", "web/.next", "py/.venv", "py/pkg/__pycache__", "crate/target"}
	var want []string
	for _, rel := range before {
		if !slices.ContainsFunc(removed, func(r string) bool { return rel == r || strings.HasPrefix(rel, r+"/") }) {
			want = append(want, rel)
		}
	}
	require.Equal(t, want, snapshot(t, root), "only the ticked tier A paths are gone")
	require.FileExists(t, filepath.Join(root, "lib", "dist", "only-copy.js"))
	require.FileExists(t, filepath.Join(root, "stray", "node_modules", "z", "index.js"))
	require.FileExists(t, filepath.Join(decoy, "node_modules", "x", "index.js"))

	// The dry run and the real run each wrote a log; the dry run's says so.
	dryLogs, err := filepath.Glob(filepath.Join(a.stateDir, "logs", "dry-run-*.log"))
	require.NoError(t, err)
	require.Len(t, dryLogs, 1)
	data, err := os.ReadFile(dryLogs[0])
	require.NoError(t, err)
	var start map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &start))
	require.Equal(t, true, start["dry_run"])

	d.key("q")
	require.True(t, d.quit)
	res := d.m.Result()
	require.Equal(t, tui.OutcomeApplied, res.Outcome)
	out.Reset()
	require.NoError(t, a.uiSummary(context.Background(), res, "plan.json"))
	summary := out.String()
	require.Contains(t, summary, "Freed ")
	require.Contains(t, summary, ": 6 removed, 0 already gone, 0 failed, 0 not run, 0 for you to run.")
	require.Contains(t, summary, "Log: "+filepath.Join(a.stateDir, "logs", "apply-"))
	require.Contains(t, summary, "Plan: "+res.Plan.Path)
}

// TestUISavePlanReleasesItsReservedName checks that a plan that cannot be
// written leaves no empty reserved file behind, and that releasing a name
// removes nothing but that empty file.
func TestUISavePlanReleasesItsReservedName(t *testing.T) {
	a, _ := testApp(t, "")
	root := filepath.Join(a.home, "Code")
	fixtureTree(t, root)
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	r, err := be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{root},
		Categories: []finding.Category{finding.CategoryProject}}, nil)
	require.NoError(t, err)
	selected := plan.SelectTiers(r.Findings, []finding.Tier{finding.TierA})
	other := filepath.Join(a.stateDir, "plans", "plan-other.json")
	writeTree(t, filepath.Dir(other), map[string]string{"plan-other.json": "{}"})
	plansBefore := snapshot(t, filepath.Dir(other))

	be.save = func(string, *plan.Plan) error { return errors.New("disk full") }
	_, err = be.SavePlan(context.Background(), r, selected)
	require.EqualError(t, err, "disk full")
	require.Equal(t, plansBefore, snapshot(t, filepath.Dir(other)), "the reserved name is released and nothing else changes")

	// A file that is not the empty reservation is never removed.
	full := filepath.Join(filepath.Dir(other), "plan-full.json")
	require.NoError(t, os.WriteFile(full, []byte("{}"), 0o644))
	releasePlanPath(full)
	require.FileExists(t, full)
	require.FileExists(t, other)
}
