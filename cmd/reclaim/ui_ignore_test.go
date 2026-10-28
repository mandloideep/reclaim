package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/config"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/tui"
)

// paths lists the paths of findings, sorted.
func paths(fs []finding.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	slices.Sort(out)
	return out
}

// TestUIFolderScanIsHere checks that "This folder" runs the single
// measuring walk of reclaim here: the same findings here reports, the
// scope of here, the breakdown for the folder tab down to the depth, and
// the walk's progress on the project row.
func TestUIFolderScanIsHere(t *testing.T) {
	a, out := testApp(t, "")
	code := filepath.Join(a.home, "Code")
	fixtureTree(t, code)
	many := map[string]string{}
	for i := range fsxProgressDirs {
		many[fmt.Sprintf("d%03d/x", i)] = "x"
	}
	writeTree(t, filepath.Join(code, "notes", "many"), many)
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	var mu sync.Mutex
	var events []scan.Event
	record := func(ev scan.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	}
	res, err := be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{code},
		Categories: []finding.Category{finding.CategoryProject}}, record)
	require.NoError(t, err)
	require.Equal(t, &finding.Scope{Command: "here", Args: []string{"~/Code"}, Paths: []string{code}, Roots: []string{code}}, res.Report.Scope, "the command as typed")

	out.Reset()
	require.NoError(t, execute(a, "here", code, "--json"))
	var h hereOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &h))
	require.Equal(t, paths(h.Findings), paths(res.Report.Findings), "the same findings here reports")

	require.NotNil(t, res.Folder)
	require.Equal(t, code, res.Folder.Path)
	require.Equal(t, h.Size, res.Folder.Size)
	web := res.Folder.Entries[slices.IndexFunc(res.Folder.Entries, func(e tui.FolderEntry) bool { return e.Name == "web" })]
	nm := web.Entries[slices.IndexFunc(web.Entries, func(e tui.FolderEntry) bool { return e.Name == "node_modules" })]
	require.True(t, nm.Dir)
	react := nm.Entries[slices.IndexFunc(nm.Entries, func(e tui.FolderEntry) bool { return e.Name == "react" })]
	require.Empty(t, react.Entries, "three levels by default")
	require.True(t, slices.ContainsFunc(events, func(ev scan.Event) bool {
		return ev.Kind == scan.Step && ev.Scanner == "" && strings.HasPrefix(ev.Text, "measuring the folder, ")
	}), "the measuring walk reports its progress")

	// --depth is the depth of the breakdown, and the scope says so.
	res, err = be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{code}, Depth: 1,
		Categories: []finding.Category{finding.CategoryProject}}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"~/Code", "--depth=1"}, res.Report.Scope.Args)
	for _, e := range res.Folder.Entries {
		require.Empty(t, e.Entries, e.Name)
	}
	require.Equal(t, paths(h.Findings), paths(res.Report.Findings), "the project walk has no depth limit, as for here")

	// Several folders scan as scan path does, without a breakdown.
	res, err = be.Scan(context.Background(), tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{filepath.Join(code, "web"), filepath.Join(code, "py")},
		Categories: []finding.Category{finding.CategoryProject}}, nil)
	require.NoError(t, err)
	require.Nil(t, res.Folder)
	require.Equal(t, "scan", res.Report.Scope.Command)
}

// fsxProgressDirs is enough folders for the measuring walk to report.
const fsxProgressDirs = 520

// TestFolderEntries checks the conversion of a measuring walk into the
// entries of the folder tab, with the state words of linked worktrees.
func TestFolderEntries(t *testing.T) {
	wt := &fsx.Node{Name: "agent-1", Path: "/c/app/.claude/worktrees/agent-1", IsDir: true, Size: 40}
	tree := []*fsx.Node{
		{Name: "app", Path: "/c/app", IsDir: true, Size: 100, Children: []*fsx.Node{
			{Name: ".claude", Path: "/c/app/.claude", IsDir: true, Size: 40, Children: []*fsx.Node{
				{Name: "worktrees", Path: "/c/app/.claude/worktrees", IsDir: true, Size: 40, Children: []*fsx.Node{wt}},
			}},
			{Name: "main.go", Path: "/c/app/main.go", Size: 60},
		}},
	}
	got := folderEntries(tree, map[string]string{wt.Path: "squash, PR #41 merged"})
	require.Equal(t, []tui.FolderEntry{{Name: "app", Path: "/c/app", Dir: true, Size: 100, Entries: []tui.FolderEntry{
		{Name: ".claude", Path: "/c/app/.claude", Dir: true, Size: 40, Entries: []tui.FolderEntry{
			{Name: "worktrees", Path: "/c/app/.claude/worktrees", Dir: true, Size: 40, Entries: []tui.FolderEntry{
				{Name: "agent-1", Path: wt.Path, Dir: true, Size: 40, Worktree: "squash, PR #41 merged", Entries: []tui.FolderEntry{}},
			}},
		}},
		{Name: "main.go", Path: "/c/app/main.go", Size: 60, Entries: []tui.FolderEntry{}},
	}}}, got)
}

// TestUIBackendIgnore checks Ignore: it writes the ignore list next to the
// configuration and nothing else, and what it names is dropped by the next
// scan, listed as protected and refused by apply, through the backend the
// screen uses.
func TestUIBackendIgnore(t *testing.T) {
	a, _ := testApp(t, "")
	code := filepath.Join(a.home, "Code")
	fixtureTree(t, code)
	cfg, err := a.loadConfig()
	require.NoError(t, err)
	be := &uiBackend{a: a, cfg: cfg}
	req := tui.ScanRequest{Mode: tui.ModeFolder, Paths: []string{code}, Categories: []finding.Category{finding.CategoryProject}}
	r, err := scanReport(context.Background(), be, req, nil)
	require.NoError(t, err)
	saved, err := be.SavePlan(context.Background(), r, plan.SelectTiers(r.Findings, []finding.Tier{finding.TierA}))
	require.NoError(t, err)
	target := filepath.Join(code, "crate", "target")
	require.Contains(t, paths(r.Findings), target)

	homeBefore := snapshot(t, a.home)
	list, err := be.Ignore([]string{target, "docker:abc123def456"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{target, "docker:abc123def456"}, list)
	ignorePath := config.IgnorePath(a.configPath)
	data, err := os.ReadFile(ignorePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "~/Code/crate/target\ndocker:abc123def456\n")
	after := snapshot(t, a.home)
	for _, p := range homeBefore {
		require.Contains(t, after, p, "Ignore removed nothing")
	}
	require.ElementsMatch(t, append(homeBefore, ".config", ".config/reclaim", ".config/reclaim/ignore"), after, "and wrote nothing but the list and its folder")

	r, err = scanReport(context.Background(), be, req, nil)
	require.NoError(t, err)
	require.NotContains(t, paths(r.Findings), target, "the next scan drops it")
	protected := be.Protected([]string{code})
	require.True(t, slices.ContainsFunc(protected, func(e apply.ProtectedEntry) bool {
		return e.Source == apply.ProtectedIgnore && e.Path == target
	}))

	// The plan saved before the entry still names it; apply refuses it
	// there, dry and real, and removes the rest.
	sum, err := be.Apply(context.Background(), saved.Path, true, apply.Progress{})
	require.Error(t, err)
	refused := slices.IndexFunc(sum.Outcomes, func(o apply.Outcome) bool { return o.Action.Path == target })
	require.Equal(t, apply.StatusRefused, sum.Outcomes[refused].Status)
	require.ErrorContains(t, sum.Outcomes[refused].Err, "it is on your ignore list ("+ignorePath+")")
	sum, err = be.Apply(context.Background(), saved.Path, false, apply.Progress{})
	require.Error(t, err)
	require.Equal(t, apply.StatusFailed, sum.Outcomes[refused].Status)
	require.DirExists(t, target)
	require.NoDirExists(t, filepath.Join(code, "web", "node_modules"))

	// Dropping the entry takes it off the list.
	list, err = be.Ignore(nil, []string{target})
	require.NoError(t, err)
	require.Equal(t, []string{"docker:abc123def456"}, list)

	// Without a configuration folder there is nowhere to keep the list.
	_, err = (&uiBackend{a: a, cfg: &config.Config{}}).Ignore([]string{target}, nil)
	require.ErrorContains(t, err, "no configuration folder")
}

// TestUIReportFlag checks --report: the screen opens on the report without
// scanning, without what exclude and the ignore list name, and a report
// that cannot be read is an error before the screen opens.
func TestUIReportFlag(t *testing.T) {
	a, out := testApp(t, "")
	code := filepath.Join(a.home, "Code")
	fixtureTree(t, code)
	report := filepath.Join(t.TempDir(), "report.json")
	require.NoError(t, execute(a, "scan", code, "--json", "--min-size", "0", "--out", report))
	out.Reset()
	_, err := config.EditIgnore(config.IgnorePath(a.configPath), a.home, []string{filepath.Join(code, "py")}, nil)
	require.NoError(t, err)

	d := newDriver(t, a, nil, uiFlags{minSize: "0", out: "plan.json", report: report})
	view := d.m.View()
	require.Contains(t, view, "Review")
	require.Contains(t, view, "~/Code/web")
	require.NotContains(t, view, "~/Code/py", "the ignore list holds for a saved report")
	require.Contains(t, view, "x ignore")

	_, err = a.uiOptions(context.Background(), nil, uiFlags{minSize: "0", report: filepath.Join(t.TempDir(), "nope.json")},
		func(string) bool { return false }, nil)
	require.ErrorContains(t, err, "read report")
}
