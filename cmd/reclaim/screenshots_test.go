package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/tui"
	"github.com/mandloideep/reclaim/internal/ui"
)

// The README screenshots are rendered from the output of the real commands
// over a fixture machine built here, never from the machine running the
// test, so no personal path or host name can appear in them.
//
// TestScreenshots renders scan, here and the select checklist in color and
// compares them with the committed files in testdata/screenshots, so a change
// to the output fails until the screenshots are regenerated with
// scripts/screenshots.sh, which sets this variable to rewrite the files and
// then turns them into the SVGs in docs/screenshots with freeze.
const updateScreenshots = "RECLAIM_UPDATE_SCREENSHOTS"

// screenshotNow is the fixed time of the fixture machine.
var screenshotNow = time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)

func TestScreenshots(t *testing.T) {
	// Color is forced for writers that are not terminals, the way
	// lipgloss reads it from the environment.
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "")
	// Box drawing characters count as one column, as they do outside East
	// Asian locales; go-runewidth reads this once at startup, so
	// scripts/screenshots.sh sets RUNEWIDTH_EASTASIAN=0.
	if os.Getenv("RUNEWIDTH_EASTASIAN") == "1" {
		t.Skip("East Asian character widths are on")
	}

	a, out := screenshotApp(t)
	shots := map[string]string{}

	require.NoError(t, execute(a, "scan"))
	shots["scan"] = prompt("reclaim scan") + out.String()
	report, err := loadReport(a.lastReportPath())
	require.NoError(t, err)

	out.Reset()
	require.NoError(t, execute(a, "here", filepath.Join(a.home, "Code", "storefront")))
	shots["here"] = prompt("reclaim here ~/Code/storefront") + out.String()

	out.Reset()
	require.NoError(t, execute(a, "worktrees"))
	shots["worktrees"] = prompt("reclaim worktrees") + out.String()

	var view bytes.Buffer
	m := ui.NewChecklist(report, ui.ChecklistOptions{
		Home:       a.home,
		PlanPath:   "plan.json",
		Provenance: ui.New(&view, a.home, a.now()).Provenance(report),
		Renderer:   lipgloss.NewRenderer(&view),
		Now:        a.now(),
	})
	m.Update(tea.WindowSizeMsg{Width: 116, Height: 30})
	down := tea.KeyMsg{Type: tea.KeyDown}
	for _, k := range []tea.KeyMsg{
		down, {Type: tea.KeyEnter}, // open ~/Code/ripgrep-fork
		down, down, {Type: tea.KeyEnter}, // open ~/Code/ml-pipeline
		down, down, down, down, down, down, down, down, down, down, // down to the npm cache
		{Type: tea.KeySpace, Runes: []rune{' '}}, // tick it, a tier B finding
	} {
		m.Update(k)
	}
	// The checklist takes over the whole screen, so no prompt sits above it.
	shots["select"] = m.View() + "\n"

	// The interactive screen, over the same machine, with the report of the
	// scan above as the last one, started in the storefront project.
	// The reclaim folder moves under the fixture home, where it is on a
	// real machine, so the plan and log paths start with ~.
	a.stateDir = filepath.Join(a.home, "Library", "Caches", "reclaim")
	require.NoError(t, saveReport(a.lastReportPath(), report))
	a.getwd = func() (string, error) { return filepath.Join(a.home, "Code", "storefront"), nil }
	maps.Copy(shots, uiScreens(t, a))

	dir := filepath.Join("testdata", "screenshots")
	for name, got := range shots {
		require.NotContains(t, got, a.home, name)
		for _, private := range []string{"/Users/", "/home/", "/var/folders/", "/tmp/"} {
			require.NotContains(t, got, private, "%s shows a path outside the fixture", name)
		}
		require.Contains(t, got, "\x1b[", "%s is in color", name)
		path := filepath.Join(dir, name+".ansi")
		if os.Getenv(updateScreenshots) != "" {
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
			continue
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err, "run scripts/screenshots.sh to render the screenshots")
		require.Equal(t, string(want), got, "the %s screenshot is out of date; run scripts/screenshots.sh", name)
	}
}

// prompt renders a shell prompt with the command that produced a screenshot.
func prompt(command string) string {
	return "\x1b[2m$\x1b[0m \x1b[1m" + command + "\x1b[0m\n"
}

// screenshotApp returns an app over a fixture macOS machine: projects,
// package caches the tools report, app caches, a Docker daemon and a
// Downloads folder, all with fixed sizes and times.
func screenshotApp(t *testing.T) (*app, *bytes.Buffer) {
	t.Helper()
	a, out := testApp(t, "")
	a.home = filepath.Join(a.home, "Users", "me")
	a.goos = "darwin"
	a.host = "laptop"
	a.configPath = filepath.Join(a.home, ".config", "reclaim", "config.toml")
	a.applications = []string{filepath.Join(a.home, "Applications")}
	a.now = func() time.Time { return screenshotNow }
	h := a.home
	const (
		kB = 1000
		MB = 1000 * kB
		GB = 1000 * MB
	)
	files := map[string]int64{
		// A web shop with a build script, so dist is tier A.
		"Code/storefront/package.json":                                                                kB,
		"Code/storefront/src/main.ts":                                                                 12 * kB,
		"Code/storefront/node_modules/react-dom/cjs/index.js":                                         1_240 * MB,
		"Code/storefront/.next/cache/webpack/client.pack":                                             412 * MB,
		"Code/storefront/dist/assets/index.js":                                                        38 * MB,
		"Code/storefront/public/hero.png":                                                             3 * MB,
		"Code/ml-pipeline/pyproject.toml":                                                             kB,
		"Code/ml-pipeline/train.py":                                                                   9 * kB,
		"Code/ml-pipeline/.venv/pyvenv.cfg":                                                           kB,
		"Code/ml-pipeline/.venv/lib/python3.12/torch/lib.so":                                          2_310 * MB,
		"Code/ml-pipeline/.mypy_cache/3.12/cache.db":                                                  61 * MB,
		"Code/ml-pipeline/data/raw.parquet":                                                           740 * MB,
		"Code/ripgrep-fork/Cargo.toml":                                                                kB,
		"Code/ripgrep-fork/src/main.rs":                                                               30 * kB,
		"Code/ripgrep-fork/target/release/deps/librg.rlib":                                            3_870 * MB,
		"Code/weather/pubspec.yaml":                                                                   kB,
		"Code/weather/lib/main.dart":                                                                  8 * kB,
		"Code/weather/.dart_tool/flutter_build/app.dill":                                              186 * MB,
		"Code/weather/build/ios/Runner.app/Runner":                                                    512 * MB,
		"Code/blog/package.json":                                                                      kB,
		"Code/blog/dist/index.html":                                                                   24 * MB,
		".npm/_cacache/content-v2/sha512/aa":                                                          1_830 * MB,
		"go/pkg/mod/cache/download/golang.org/x/sys/@v/v1.zip":                                        2_150 * MB,
		"Library/Caches/go-build/aa/aa-d":                                                             940 * MB,
		"Library/Caches/Homebrew/downloads/node.bottle.tar.gz":                                        870 * MB,
		"Library/Caches/Google/Chrome/Default/Cache/data_1":                                           655 * MB,
		"Library/Caches/com.microsoft.VSCode.ShipIt/update.zip":                                       214 * MB,
		"Library/Caches/ms-playwright/chromium-1140/chrome":                                           930 * MB,
		"Library/Caches/com.apple.Safari/Cache.db":                                                    90 * MB,
		"Library/Developer/Xcode/DerivedData/Weather-bxrjzktdwfaugpcvhzjmlzkhwqyi/Build/Products/app": 1_420 * MB,
		"Downloads/Zed-0.150.4.dmg":                                                                   120 * MB,
		"Downloads/conference-talk.mov":                                                               3_240 * MB,
		"Applications/Zed.app/Contents/Info.plist":                                                    kB,
	}
	for rel, size := range files {
		sparse(t, filepath.Join(h, filepath.FromSlash(rel)), size)
	}
	manifest := `{"config":{"digest":"sha256:c1","size":500},"layers":[{"digest":"sha256:w1","size":2019000000}]}`
	writeTree(t, h, map[string]string{
		".ollama/models/manifests/registry.ollama.ai/library/llama3.2/latest": manifest,
		"Code/storefront/.git/HEAD":    "ref: refs/heads/main",
		"Code/ml-pipeline/.git/HEAD":   "ref: refs/heads/main",
		"Code/ripgrep-fork/.git/HEAD":  "ref: refs/heads/main",
		"Code/storefront/package.json": `{"scripts":{"build":"next build"}}`,
	})

	// The storefront repository has worktrees from Claude Code and Codex.
	day := 24 * time.Hour
	repo := filepath.Join(h, "Code", "storefront")
	agents := filepath.Join(repo, ".claude", "worktrees")
	lockStart := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	outputs := map[string]string{
		"npm config get cache": filepath.Join(h, ".npm"),
		"go env GOMODCACHE":    filepath.Join(h, "go", "pkg", "mod"),
		"go env GOCACHE":       filepath.Join(h, "Library", "Caches", "go-build"),
		"brew --cache":         filepath.Join(h, "Library", "Caches", "Homebrew"),
		// ps prints local time; Claude Code records the lock in UTC.
		"ps -o lstart= -p 4242": lockStart.In(time.Local).Format("Mon Jan _2 15:04:05 2006"),
	}
	runOutputs := map[string]string{}
	addFakeRepo(t, fakeRepo{
		path: repo, origin: "git@github.com:acme/storefront.git", fetched: screenshotNow.Add(-3 * time.Hour),
		worktrees: []fakeWorktree{
			{path: filepath.Join(agents, "agent-3f9c1e2a"), name: "agent-3f9c1e2a", branch: "fix/checkout-rounding",
				head: "a3f9c1e2a7d04b6e8c1f2a3b4c5d6e7f8a9b0c1d", merged: "squash", size: 184 * MB,
				pr: `[{"number":41,"state":"MERGED","url":"https://github.com/acme/storefront/pull/41"}]`},
			{path: filepath.Join(agents, "agent-7b2d40aa"), name: "agent-7b2d40aa", branch: "feat/gift-cards",
				head: "a7b2d40aa1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6", merged: "no", size: 96 * MB,
				pr: `[{"number":44,"state":"OPEN","url":"https://github.com/acme/storefront/pull/44"}]`},
			{path: filepath.Join(agents, "agent-c0ffee12"), name: "agent-c0ffee12", branch: "chore/upgrade-next",
				head: "ac0ffee12b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e", merged: "no", size: 88 * MB, pr: `[]`,
				lock: "claude agent agent-c0ffee12 (pid 4242 start " + lockStart.Format("Mon Jan _2 15:04:05 2006") + ")"},
			{path: filepath.Join(h, ".codex", "worktrees", "5e1d", "storefront"), name: "storefront",
				head: "a5e1d0c9b8a7f6e5d4c3b2a1f0e9d8c7b6a5f4e3", merged: "ancestor", modified: 12, untracked: 3, size: 231 * MB},
		},
		stale: []string{filepath.Join(h, "Code", "storefront-spike"), filepath.Join(h, "Code", "storefront-v1")},
	}, outputs, runOutputs)

	// Everything is old, then each project gets its own last activity.
	setTimes(t, h, screenshotNow.Add(-400*day))
	for project, age := range map[string]time.Duration{
		"storefront": 2 * day, "ml-pipeline": 20 * day, "ripgrep-fork": 150 * day, "weather": 60 * day, "blog": 380 * day,
	} {
		for _, src := range []string{"package.json", "pyproject.toml", "Cargo.toml", "pubspec.yaml", "src", "lib", "train.py"} {
			p := filepath.Join(h, "Code", project, src)
			if _, err := os.Lstat(p); err == nil {
				setTimes(t, p, screenshotNow.Add(-age))
			}
		}
	}
	// Caches are in daily use, and so are the worktrees.
	for _, recent := range []string{".npm", "go", "Library/Caches", "Library/Developer/Xcode/DerivedData", ".ollama/models", "Downloads/Zed-0.150.4.dmg",
		"Code/storefront/.git/worktrees", "Code/storefront/.claude", ".codex"} {
		setTimes(t, filepath.Join(h, filepath.FromSlash(recent)), screenshotNow.Add(-3*day))
	}
	fetched := screenshotNow.Add(-3 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(repo, ".git", "FETCH_HEAD"), fetched, fetched))

	a.exec = &execx.Fake{
		Tools: map[string]string{"npm": "/opt/homebrew/bin/npm", "go": "/opt/homebrew/bin/go", "brew": "/opt/homebrew/bin/brew",
			"git": "/usr/bin/git", "ps": "/bin/ps", "gh": "/opt/homebrew/bin/gh"},
		Outputs:    outputs,
		RunOutputs: runOutputs,
		// The project scanners ask git whether artifact folders hold
		// committed files; in the fixture none do.
		Fallback: func(name string, args []string) (string, error) {
			if name == "git" && slices.Contains(args, "ls-files") {
				return "", nil
			}
			return "", errors.New(name + " " + strings.Join(args, " ") + ": no fake output")
		},
	}
	finished := screenshotNow.Add(-12 * day).Format(time.RFC3339Nano)
	created := screenshotNow.Add(-90 * day).Unix()
	fake := &dockerx.Fake{
		Host: "unix:///var/run/docker.sock",
		Containers: []container.Summary{
			{ID: "c-api", Names: []string{"/storefront-api-1"}, Image: "storefront-api:dev", ImageID: "sha256:api", State: container.StateRunning,
				Labels: map[string]string{"com.docker.compose.project": "storefront"},
				Mounts: []container.MountPoint{{Type: "volume", Name: "storefront_pgdata"}}},
			{ID: "c-worker", Names: []string{"/storefront-worker-1"}, Image: "storefront-worker:dev", ImageID: "sha256:worker",
				State: container.StateExited, SizeRw: 48 * MB, Created: created,
				Labels: map[string]string{"com.docker.compose.project": "storefront"}},
		},
		FinishedAt: map[string]string{"c-worker": finished},
		Images: []image.Summary{
			{ID: "sha256:api", RepoTags: []string{"storefront-api:dev"}, Size: 690 * MB},
			{ID: "sha256:worker", RepoTags: []string{"storefront-worker:dev"}, Size: 540 * MB},
			{ID: "sha256:9f3c1a7be2d40000", RepoTags: []string{"<none>:<none>"}, Size: 1_180 * MB, Created: created},
			{ID: "sha256:node20", RepoTags: []string{"node:20"}, Size: 1_110 * MB, Created: created},
			{ID: "sha256:pg15", RepoTags: []string{"postgres:15"}, Size: 425 * MB, Created: created},
		},
		Volumes: []volume.Volume{
			{Name: "storefront_pgdata", UsageData: &volume.UsageData{Size: 830 * MB, RefCount: 1}},
			{Name: "old_redis", UsageData: &volume.UsageData{Size: 205 * MB}, CreatedAt: screenshotNow.Add(-200 * day).Format(time.RFC3339)},
		},
		BuildCache: []build.CacheRecord{
			{ID: "r1", Size: 2_400 * MB},
			{ID: "r2", Size: 760 * MB},
		},
	}
	a.docker = func() (dockerx.API, error) { return fake, nil }
	return a, out
}

// uiScreens renders the six screens of the interactive mode. The scan is
// the real one, its progress replayed in a fixed order with the worktree
// and Docker scanners still running, so the Scanning screen does not
// depend on which scanner finished first. The Confirm screen shows the
// real dry run of the preselected findings, and the Removing screen the
// real run of the plan, replayed up to its fifth action, before Done.
func uiScreens(t *testing.T, a *app) map[string]string {
	t.Helper()
	now := a.now
	defer func() { a.now = now }()
	at := func(d time.Duration) func() {
		return func() { a.now = func() time.Time { return screenshotNow.Add(d) } }
	}
	shots := map[string]string{}
	// The scripted git removes the fixture worktree it is told to, as git
	// worktree remove would, inside the fixture's temp folder.
	if fake, ok := a.exec.(*execx.Fake); ok {
		fake.OnRun = func(argv []string) error {
			if len(argv) == 6 && argv[0] == "git" && argv[3] == "worktree" && argv[4] == "remove" {
				require.True(t, strings.HasPrefix(argv[5], a.home+string(filepath.Separator)), "only the fixture")
				return os.RemoveAll(argv[5])
			}
			return nil
		}
	}
	d := newDriverWith(t, a, nil, uiFlags{minSize: defaultMinSize, out: "plan.json"}, 124, 20, func(o *tui.Options) {
		o.Version = "v0.3.0"
		o.Backend = &curatedScan{Backend: o.Backend, hold: map[string]bool{"git-worktrees": true, "docker": true},
			after: at(7 * time.Second), applied: at(48 * time.Second), runningAt: 4}
	})
	shots["ui-start"] = d.m.View() + "\n"
	d.beforeDone = func() {
		d.m.Update(tea.WindowSizeMsg{Width: 104, Height: 14})
		shots["ui-scanning"] = d.m.View() + "\n"
		d.m.Update(tea.WindowSizeMsg{Width: 124, Height: 34})
	}
	d.key("enter")
	d.moveTo("~/Code/storefront  1 removable")
	d.key("enter")
	d.moveTo("~/Code/ripgrep-fork")
	d.key("enter", "down")
	shots["ui-review"] = d.m.View() + "\n"

	d.key("c", "d")
	shots["ui-confirm"] = d.m.View() + "\n"
	d.beforeDone = func() {
		d.m.Update(tea.WindowSizeMsg{Width: 124, Height: 15})
		shots["ui-removing"] = d.m.View() + "\n"
	}
	d.key("y", "e", "s", "enter")
	d.m.Update(tea.WindowSizeMsg{Width: 124, Height: 12})
	shots["ui-done"] = d.m.View() + "\n"
	return shots
}

// curatedScan runs the real scan and replays its progress in the order of
// the plan, with the last report of the project walk, and without the end
// of the scanners it holds, which then show as running.
type curatedScan struct {
	tui.Backend
	hold  map[string]bool
	after func()
	// applied moves the clock once the real run is done, and runningAt is
	// the action the replay of its progress stops in.
	applied   func()
	runningAt int
	freeReads int
}

// Apply runs the plan for real over the fixture machine. A dry run reports
// as it goes; the real run's progress is replayed up to the action at
// runningAt, which is shown running.
func (c *curatedScan) Apply(ctx context.Context, path string, dryRun bool, progress apply.Progress) (apply.Summary, error) {
	if dryRun {
		return c.Backend.Apply(ctx, path, true, progress)
	}
	var logPath string
	sum, err := c.Backend.Apply(ctx, path, false, apply.Progress{Begin: func(p string) { logPath = p }})
	c.applied()
	progress.Begin(logPath)
	for i, o := range sum.Outcomes {
		progress.Starting(i, o.Action)
		if i == c.runningAt {
			break
		}
		progress.Finished(i, o)
	}
	return sum, err
}

// FreeSpace reports a fixed disk: 41.2 GB before the run, more after.
func (c *curatedScan) FreeSpace(string) (int64, error) {
	c.freeReads++
	if c.freeReads == 1 {
		return 41_200_000_000, nil
	}
	return 50_600_000_000, nil
}

func (c *curatedScan) Scan(ctx context.Context, req tui.ScanRequest, progress func(scan.Event)) (*finding.Report, error) {
	var mu sync.Mutex
	var events []scan.Event
	r, err := c.Backend.Scan(ctx, req, func(ev scan.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	})
	var planned, walk scan.Event
	byScanner := map[string][]scan.Event{}
	for _, ev := range events {
		switch {
		case ev.Kind == scan.Planned:
			planned = ev
		case ev.Kind == scan.Step && ev.Scanner == "":
			walk = ev
		case ev.Kind == scan.ScannerFinished && c.hold[ev.Scanner]:
		default:
			byScanner[ev.Scanner] = append(byScanner[ev.Scanner], ev)
		}
	}
	progress(planned)
	progress(walk)
	for _, p := range planned.Scanners {
		for _, ev := range byScanner[p.Name] {
			progress(ev)
		}
	}
	c.after()
	return r, err
}

// setTimes sets the access and modification times of path and everything
// below it.
func setTimes(t *testing.T, path string, ts time.Time) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Chtimes(p, ts, ts); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}))
}
