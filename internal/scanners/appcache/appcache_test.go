package appcache

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners/caches"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func put(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
}

// fakeHome returns a home directory as deep as a real one, so apply's
// minimum depth rule applies as it does on a real machine.
func fakeHome(t *testing.T) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := filepath.Join(tmp, "Users", "me")
	require.NoError(t, os.MkdirAll(home, 0o755))
	return home
}

func testEnv(home, goos string, fake *execx.Fake) scan.Env {
	if fake == nil {
		fake = &execx.Fake{}
	}
	return scan.Env{Home: home, GOOS: goos, Now: now, Exec: fake, Walker: fsx.NewWalker(2), Getenv: func(string) string { return "" }}
}

// run runs the app cache scanners, plus extra ones, and returns the findings
// by scanner. It fails on any warning.
func run(t *testing.T, env scan.Env, extra ...scan.Scanner) map[string][]finding.Finding {
	t.Helper()
	ss := slices.Clone(extra)
	for _, s := range Scanners() {
		ss = append(ss, s)
	}
	res := scan.Run(context.Background(), ss, env, 4)
	require.Empty(t, res.Warnings)
	out := map[string][]finding.Finding{}
	for _, f := range res.Findings {
		out[f.Scanner] = append(out[f.Scanner], f)
	}
	return out
}

func paths(fs []finding.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	slices.Sort(out)
	return out
}

func TestCacheFolderDarwin(t *testing.T) {
	home := fakeHome(t)
	c := filepath.Join(home, "Library", "Caches")
	for _, name := range []string{
		"Google", "com.tinyspeck.slackmacgap", "Homebrew", "pip", "Yarn", "reclaim", "com.apple.Safari", "CloudKit",
		"com.spotify.client", "com.docker.docker", "Slack.ShipIt", "notion-updater", "ms-playwright",
	} {
		put(t, filepath.Join(c, name, "data"), 2_000_000)
	}
	put(t, filepath.Join(c, "tiny", "x"), 10)
	put(t, filepath.Join(c, "loose.plist"), 3_000_000)
	elsewhere := filepath.Join(home, "elsewhere")
	put(t, filepath.Join(elsewhere, "big"), 3_000_000)
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(c, "linked")))

	got := run(t, testEnv(home, "darwin", nil))
	require.Equal(t, []string{filepath.Join(c, "Google"), filepath.Join(c, "com.tinyspeck.slackmacgap")}, paths(got["user-caches"]),
		"claimed, denied, tiny, loose and linked entries are left out")
	for _, f := range got["user-caches"] {
		require.Equal(t, finding.TierA, f.Tier)
		require.Equal(t, finding.KindDir, f.Kind)
		require.Equal(t, finding.ActionRemovePath, f.Action)
	}
	require.Equal(t, []string{filepath.Join(c, "Slack.ShipIt"), filepath.Join(c, "notion-updater")}, paths(got["electron-updaters"]))
	require.Equal(t, []string{filepath.Join(c, "ms-playwright")}, paths(got["playwright"]))
	require.Equal(t, finding.TierB, got["playwright"][0].Tier)

	// Every finding in the cache folder is a direct child of it.
	for _, fs := range got {
		for _, f := range fs {
			if strings.HasPrefix(f.Path, c) {
				require.Equal(t, c, filepath.Dir(f.Path), f.Path)
			}
		}
	}
}

func TestCacheFolderLinux(t *testing.T) {
	home := fakeHome(t)
	c := filepath.Join(home, ".cache")
	for _, name := range []string{"thumbnails", "huggingface", "pip", "electron-app-updater", "ms-playwright", "puppeteer", "pypoetry"} {
		put(t, filepath.Join(c, name, "data"), 2_000_000)
	}
	// macOS only folders are ignored on Linux even when they exist.
	put(t, filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData", "App-x", "f"), 2_000_000)
	put(t, filepath.Join(home, "Library", "Application Support", "Claude", "vm_bundles", "claudevm.bundle", "rootfs.img"), 2_000_000)

	got := run(t, testEnv(home, "linux", nil))
	require.Equal(t, []string{filepath.Join(c, "thumbnails")}, paths(got["user-caches"]))
	require.Equal(t, finding.TierB, got["user-caches"][0].Tier, "~/.cache entries are never preselected")
	require.Equal(t, []string{filepath.Join(c, "electron-app-updater")}, paths(got["electron-updaters"]))
	require.Equal(t, []string{filepath.Join(c, "ms-playwright")}, paths(got["playwright"]))
	require.Equal(t, []string{filepath.Join(c, "puppeteer")}, paths(got["puppeteer"]))
	require.Empty(t, got["xcode-derived-data"])
	require.Empty(t, got["claude-vm"])
	require.Empty(t, got["simulator-devices"])
}

// TestCatchAllGivesWayToSpecificScanners relocates the npm cache into the
// cache folder: the package cache scanner reports it, and the catch-all
// entry that contains it is dropped so the bytes are offered once.
func TestCatchAllGivesWayToSpecificScanners(t *testing.T) {
	home := fakeHome(t)
	c := filepath.Join(home, ".cache")
	put(t, filepath.Join(c, "custom-npm", "_cacache", "x"), 2_000_000)
	put(t, filepath.Join(c, "other", "x"), 2_000_000)
	env := testEnv(home, "linux", nil)
	env.Getenv = func(k string) string {
		if k == "npm_config_cache" {
			return filepath.Join(c, "custom-npm")
		}
		return ""
	}
	extra := make([]scan.Scanner, 0, len(caches.Scanners()))
	for _, s := range caches.Scanners() {
		extra = append(extra, s)
	}
	got := run(t, env, extra...)
	require.Equal(t, []string{filepath.Join(c, "custom-npm", "_cacache")}, paths(got["npm-cache"]))
	require.Equal(t, []string{filepath.Join(c, "other")}, paths(got["user-caches"]))
}

// TestCacheVariablesThatPointTooWide checks that a cache variable naming the
// home directory, or a browser variable naming the cache folder, does not
// turn ordinary folders into cache findings.
func TestCacheVariablesThatPointTooWide(t *testing.T) {
	home := fakeHome(t)
	put(t, filepath.Join(home, "Videos", "film.mkv"), 5_000_000)
	put(t, filepath.Join(home, ".password-store", "x"), 2_000_000)
	put(t, filepath.Join(home, ".cache", "chromium-1234", "x"), 2_000_000)
	put(t, filepath.Join(home, "pw", "chromium-1234", "chrome"), 2_000_000)
	put(t, filepath.Join(home, "notbrowsers", "photos", "x"), 2_000_000)

	scanWith := func(vars map[string]string) scan.Result {
		env := testEnv(home, "linux", nil)
		env.Getenv = func(k string) string { return vars[k] }
		ss := make([]scan.Scanner, 0, len(Scanners()))
		for _, s := range Scanners() {
			ss = append(ss, s)
		}
		return scan.Run(context.Background(), ss, env, 4)
	}
	for _, vars := range []map[string]string{
		{"XDG_CACHE_HOME": home},
		{"XDG_CACHE_HOME": filepath.Dir(home)},
		{"XDG_CACHE_HOME": "/var/tmp"},
		{"PLAYWRIGHT_BROWSERS_PATH": home},
		{"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(home, ".cache")},
		{"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(home, "notbrowsers")},
		{"PUPPETEER_CACHE_DIR": home},
		{"PUPPETEER_CACHE_DIR": filepath.Join(home, "notbrowsers")},
	} {
		res := scanWith(vars)
		for _, f := range res.Findings {
			require.NotEqual(t, home, f.Path, vars)
			require.NotContains(t, []string{filepath.Join(home, "Videos"), filepath.Join(home, ".password-store"), filepath.Join(home, ".cache"), filepath.Join(home, "notbrowsers")}, f.Path, vars)
		}
	}
	res := scanWith(map[string]string{"XDG_CACHE_HOME": home})
	require.NotEmpty(t, res.Warnings, "the skipped cache folder is reported")

	res = scanWith(map[string]string{"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(home, "pw")})
	var pw []string
	for _, f := range res.Findings {
		if f.Scanner == "playwright" {
			pw = append(pw, f.Path)
		}
	}
	require.Equal(t, []string{filepath.Join(home, "pw")}, pw, "a browser folder of its own is offered")
}

func TestPlaywrightProfilesMakeItTierC(t *testing.T) {
	home := fakeHome(t)
	c := filepath.Join(home, "Library", "Caches")
	put(t, filepath.Join(c, "ms-playwright", "chromium-1234", "chrome"), 2_000_000)
	put(t, filepath.Join(c, "ms-playwright", "mcp-chrome-abc", "Cookies"), 1000)
	put(t, filepath.Join(c, "ms-playwright-mcp", "mcp-chrome-def", "Cookies"), 1000)
	put(t, filepath.Join(c, "ms-playwright-go", "1.50.1", "driver"), 1000)

	got := run(t, testEnv(home, "darwin", nil))["playwright"]
	tiers := map[string]finding.Tier{}
	for _, f := range got {
		tiers[filepath.Base(f.Path)] = f.Tier
	}
	require.Equal(t, map[string]finding.Tier{"ms-playwright": finding.TierC, "ms-playwright-mcp": finding.TierC, "ms-playwright-go": finding.TierB}, tiers)
	for _, f := range got {
		if filepath.Base(f.Path) == "ms-playwright" {
			require.Contains(t, f.Warning, "mcp-chrome-abc")
		}
	}
}

func TestDerivedData(t *testing.T) {
	home := fakeHome(t)
	dd := filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")
	put(t, filepath.Join(dd, "App-abc", "Build", "x"), 500)
	put(t, filepath.Join(dd, "ModuleCache.noindex", "y"), 300)
	put(t, filepath.Join(dd, "info.plist"), 10)
	got := run(t, testEnv(home, "darwin", nil))["xcode-derived-data"]
	require.Equal(t, []string{filepath.Join(dd, "App-abc"), filepath.Join(dd, "ModuleCache.noindex")}, paths(got))
	require.Equal(t, finding.TierA, got[0].Tier)
}

// TestDerivedDataLocation reads Xcode's custom DerivedData location through
// the command runner: a location inside the home directory is scanned instead
// of the default and offers only folders named the way Xcode names them, and
// every other answer falls back to the default or scans nothing.
func TestDerivedDataLocation(t *testing.T) {
	const query = "defaults read com.apple.dt.Xcode IDECustomDerivedDataLocation"
	const project = "App-bxrjzktdwfaugpcvhzjmlzkhwqyi"
	home := fakeHome(t)
	def := filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")
	put(t, filepath.Join(def, "Old-abc", "Build", "x"), 500)
	custom := filepath.Join(home, "Builds", "DerivedData")
	put(t, filepath.Join(custom, project, "Build", "x"), 700)
	put(t, filepath.Join(custom, "ModuleCache.noindex", "y"), 300)
	put(t, filepath.Join(custom, "notes", "todo.txt"), 900)
	put(t, filepath.Join(custom, "App-SHORT", "x"), 900)
	outside := filepath.Join(filepath.Dir(filepath.Dir(home)), "Volumes", "Fast", "DerivedData")
	put(t, filepath.Join(outside, project, "x"), 100)

	tests := []struct {
		name  string
		tools map[string]string
		value *string
		want  []string
		warn  string
		note  string
	}{
		{name: "defaults is missing", want: []string{filepath.Join(def, "Old-abc")}},
		{name: "the key is not set", tools: map[string]string{"defaults": "/usr/bin/defaults"}, want: []string{filepath.Join(def, "Old-abc")}},
		{name: "an empty value", tools: map[string]string{"defaults": "/usr/bin/defaults"}, value: ptr("  \n"), want: []string{filepath.Join(def, "Old-abc")}},
		{
			name: "a custom location", tools: map[string]string{"defaults": "/usr/bin/defaults"}, value: ptr(custom + "\n"),
			want: []string{filepath.Join(custom, project), filepath.Join(custom, "ModuleCache.noindex")},
		},
		{
			name: "a custom location under ~", tools: map[string]string{"defaults": "/usr/bin/defaults"}, value: ptr("~/Builds/DerivedData"),
			want: []string{filepath.Join(custom, project), filepath.Join(custom, "ModuleCache.noindex")},
		},
		{name: "outside the home directory", tools: map[string]string{"defaults": "/usr/bin/defaults"}, value: ptr(outside), note: "outside the home folder"},
		{name: "the home directory itself", tools: map[string]string{"defaults": "/usr/bin/defaults"}, value: ptr(home), note: "outside the home folder"},
		{name: "a relative path", tools: map[string]string{"defaults": "/usr/bin/defaults"}, value: ptr("Builds"), warn: "not an absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Tools: tt.tools, Outputs: map[string]string{}}
			if tt.value != nil {
				fake.Outputs[query] = *tt.value
			}
			res := scan.Run(context.Background(), []scan.Scanner{scannerNamed(t, "xcode-derived-data")}, testEnv(home, "darwin", fake), 1)
			got := paths(res.Findings)
			want := append([]string{}, tt.want...)
			slices.Sort(want)
			require.Equal(t, want, append([]string{}, got...))
			for _, f := range res.Findings {
				require.Equal(t, finding.TierA, f.Tier)
				require.Equal(t, finding.ActionRemovePath, f.Action)
			}
			if tt.warn != "" {
				require.Len(t, res.Warnings, 1)
				require.Contains(t, res.Warnings[0].Message, tt.warn)
			} else {
				require.Empty(t, res.Warnings)
			}
			if tt.note != "" {
				require.Len(t, res.Notes, 1)
				require.Contains(t, res.Notes[0], tt.note)
			} else {
				require.Empty(t, res.Notes)
			}
		})
	}
}

func TestDerivedDataEntry(t *testing.T) {
	for name, want := range map[string]bool{
		"App-bxrjzktdwfaugpcvhzjmlzkhwqyi":     true,
		"My-App-bxrjzktdwfaugpcvhzjmlzkhwqyi":  true,
		"ModuleCache.noindex":                  true,
		"SymbolCache.noindex":                  true,
		".noindex":                             false,
		"-bxrjzktdwfaugpcvhzjmlzkhwqyi":        false,
		"App-bxrjzktdwfaugpcvhzjmlzkhwqy":      false,
		"App-bxrjzktdwfaugpcvhzjmlzkhwqyiz":    false,
		"App-BXRJZKTDWFAUGPCVHZJMLZKHWQYI":     false,
		"App-bxrjzktdwfaugpcvhzjmlzkhwq1i":     false,
		"notes":                                false,
		"App-bxrjzktdwfaugpcvhzjmlzkhwqyi.bak": false,
	} {
		require.Equal(t, want, derivedDataEntry(name), name)
	}
}

// TestApplyRemovesOnlyCustomDerivedData applies every finding from a custom
// DerivedData location and checks that exactly those folders are gone: the
// location itself, the folders Xcode did not make and the default location
// all survive.
func TestApplyRemovesOnlyCustomDerivedData(t *testing.T) {
	home := fakeHome(t)
	custom := filepath.Join(home, "Builds", "DerivedData")
	put(t, filepath.Join(custom, "App-bxrjzktdwfaugpcvhzjmlzkhwqyi", "Build", "x"), 700)
	put(t, filepath.Join(custom, "ModuleCache.noindex", "y"), 300)
	put(t, filepath.Join(custom, "notes", "todo.txt"), 900)
	put(t, filepath.Join(custom, "info.plist"), 10)
	put(t, filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData", "Old-abc", "x"), 500)
	fake := &execx.Fake{
		Tools:   map[string]string{"defaults": "/usr/bin/defaults"},
		Outputs: map[string]string{"defaults read com.apple.dt.Xcode IDECustomDerivedDataLocation": custom},
	}
	res := scan.Run(context.Background(), []scan.Scanner{scannerNamed(t, "xcode-derived-data")}, testEnv(home, "darwin", fake), 1)
	require.Len(t, res.Findings, 2)
	r := &finding.Report{Version: finding.ReportVersion, Created: now, Roots: []string{home}, Findings: res.Findings}
	p, err := plan.New(r, r.Findings, "host", now)
	require.NoError(t, err)

	before := tree(t, home)
	sum, err := apply.Run(context.Background(), p, apply.Options{Home: home, Walker: fsx.NewWalker(2), Exec: fake, Now: func() time.Time { return now }})
	require.NoError(t, err)
	for _, o := range sum.Outcomes {
		require.Equal(t, apply.StatusDone, o.Status, o.Action.Label())
	}
	targets := paths(res.Findings)
	var want []string
	for _, p := range before {
		if !slices.ContainsFunc(targets, func(t string) bool { return p == t || strings.HasPrefix(p, t+"/") }) {
			want = append(want, p)
		}
	}
	require.Equal(t, want, tree(t, home))
	require.FileExists(t, filepath.Join(custom, "notes", "todo.txt"))
	require.FileExists(t, filepath.Join(custom, "info.plist"))
	require.Empty(t, fake.Ran(), "removing folders runs no command")
}

func ptr[T any](v T) *T { return &v }

func TestSimulators(t *testing.T) {
	home := fakeHome(t)
	devices := filepath.Join(home, "Library", "Developer", "CoreSimulator", "Devices")
	gone := "11111111-2222-4333-8444-555555555555"
	ok := "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	put(t, filepath.Join(devices, gone, "data", "x"), 700)
	put(t, filepath.Join(devices, ok, "data", "x"), 900)
	list, err := json.Marshal(map[string]any{"devices": map[string]any{
		"com.apple.CoreSimulator.SimRuntime.iOS-16-4": []map[string]any{
			{"udid": gone, "name": "iPhone 14", "isAvailable": false, "availabilityError": "runtime profile not found"},
			{"udid": "not-a-uuid", "name": "Odd", "isAvailable": false},
		},
		"com.apple.CoreSimulator.SimRuntime.iOS-18-0": []map[string]any{
			{"udid": ok, "name": "iPhone 16", "isAvailable": true},
		},
	}})
	require.NoError(t, err)
	runtimes, err := json.Marshal(map[string]any{
		"0F0F0F0F-1111-4222-8333-444444444444": map[string]any{
			"identifier": "0F0F0F0F-1111-4222-8333-444444444444", "version": "18.0", "build": "22A3351",
			"runtimeIdentifier": "com.apple.CoreSimulator.SimRuntime.iOS-18-0", "deletable": true, "sizeBytes": 8_000_000_000,
			"path": "/Library/Developer/CoreSimulator/Images/0F0F0F0F-1111-4222-8333-444444444444.dmg",
		},
		"BUNDLED": map[string]any{"identifier": "BUNDLED", "deletable": false, "sizeBytes": 5},
	})
	require.NoError(t, err)
	fake := &execx.Fake{
		Tools: map[string]string{"xcrun": "/usr/bin/xcrun"},
		Outputs: map[string]string{
			"xcrun simctl list -j devices": string(list),
			"xcrun simctl runtime list -j": string(runtimes),
		},
	}
	got := run(t, testEnv(home, "darwin", fake))

	require.Len(t, got["simulator-devices"], 1)
	d := got["simulator-devices"][0]
	require.Equal(t, filepath.Join(devices, gone), d.Path)
	require.Equal(t, "simulator iPhone 14 (iOS 16.4)", d.Name)
	require.Equal(t, finding.ActionRunCommand, d.Action)
	require.Equal(t, []string{"xcrun", "simctl", "delete", gone}, d.Command)
	require.Equal(t, finding.TierB, d.Tier)

	require.Len(t, got["simulator-runtimes"], 1)
	r := got["simulator-runtimes"][0]
	require.True(t, r.NeedsSudo)
	require.Equal(t, []string{"xcrun", "simctl", "runtime", "delete", "0F0F0F0F-1111-4222-8333-444444444444"}, r.Command)
	require.Equal(t, int64(8_000_000_000), r.Size)
	require.Equal(t, "iOS 18.0 simulator runtime (22A3351)", r.Name)

	// Without Xcode there is no simctl, and nothing to report.
	none := run(t, testEnv(home, "darwin", &execx.Fake{}))
	require.Empty(t, none["simulator-devices"])
	require.Empty(t, none["simulator-runtimes"])
}

func manifest(t *testing.T, path string, layers map[string]int64) {
	t.Helper()
	type layer struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	}
	var m struct {
		Config layer   `json:"config"`
		Layers []layer `json:"layers"`
	}
	m.Config = layer{Digest: "sha256:cfg-" + filepath.Base(filepath.Dir(path)) + "-" + filepath.Base(path), Size: 100}
	keys := make([]string, 0, len(layers))
	for d := range layers {
		keys = append(keys, d)
	}
	slices.Sort(keys)
	for _, d := range keys {
		m.Layers = append(m.Layers, layer{Digest: d, Size: layers[d]})
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func TestOllama(t *testing.T) {
	home := fakeHome(t)
	models := filepath.Join(home, ".ollama", "models")
	m := filepath.Join(models, "manifests")
	manifest(t, filepath.Join(m, "registry.ollama.ai", "library", "llama3", "latest"), map[string]int64{"sha256:w1": 4_000_000_000, "sha256:tmpl": 500})
	manifest(t, filepath.Join(m, "registry.ollama.ai", "library", "llama3", "8b"), map[string]int64{"sha256:w1": 4_000_000_000, "sha256:tmpl": 500})
	manifest(t, filepath.Join(m, "registry.ollama.ai", "me", "tuned", "v1"), map[string]int64{"sha256:w2": 1_000_000_000, "sha256:tmpl": 500})
	manifest(t, filepath.Join(m, "hf.co", "org", "model", "q4"), map[string]int64{"sha256:w3": 2_000_000_000})
	manifest(t, filepath.Join(m, "registry.ollama.ai", "library", "-bad", "x"), map[string]int64{"sha256:w4": 5})

	env := testEnv(home, "linux", nil)
	res := scan.Run(context.Background(), []scan.Scanner{scannerNamed(t, "ollama")}, env, 1)
	require.Len(t, res.Warnings, 1, "a name ollama rm could misread is skipped with a warning")
	byName := map[string]finding.Finding{}
	for _, f := range res.Findings {
		byName[f.Name] = f
	}
	require.Len(t, byName, 4)
	tuned := byName["model me/tuned:v1"]
	require.Equal(t, int64(1_000_000_100), tuned.Size, "only the bytes no other model uses")
	require.Equal(t, []string{"ollama", "rm", "me/tuned:v1"}, tuned.Command)
	require.Equal(t, models, tuned.Path)
	require.Equal(t, "ollama pull me/tuned:v1", tuned.Restore)
	require.Contains(t, tuned.Warning, "shares 500 B with other models")
	require.Equal(t, int64(100), byName["model llama3:latest"].Size, "its weights are shared with llama3:8b")
	require.Equal(t, int64(2_000_000_100), byName["model hf.co/org/model:q4"].Size)
	for _, f := range res.Findings {
		require.True(t, plan.AllowedCommand(f.Command), f.Command)
		// Apply finds the manifest again from the name alone.
		rel, ok := plan.OllamaManifest(f.Command[2])
		require.True(t, ok)
		require.FileExists(t, filepath.Join(models, "manifests", filepath.FromSlash(rel)))
	}
}

func scannerNamed(t *testing.T, name string) *Scanner {
	t.Helper()
	for _, s := range Scanners() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no scanner %s", name)
	return nil
}

func TestCodexWorktrees(t *testing.T) {
	home := fakeHome(t)
	w := filepath.Join(home, ".codex", "worktrees")
	for _, task := range []string{"clean", "dirty", "broken", "detached"} {
		put(t, filepath.Join(w, task, "repo", ".git"), 40)
		put(t, filepath.Join(w, task, "repo", "main.go"), 1000)
	}
	put(t, filepath.Join(w, "empty-leftover", "notes.txt"), 300)
	fake := &execx.Fake{
		Tools: map[string]string{"git": "/usr/bin/git"},
		Outputs: map[string]string{
			"git -C " + filepath.Join(w, "clean", "repo") + " status --porcelain":                                      "",
			"git -C " + filepath.Join(w, "clean", "repo") + " rev-list -n 1 HEAD --not --branches --remotes --tags":    "",
			"git -C " + filepath.Join(w, "dirty", "repo") + " status --porcelain":                                      " M main.go",
			"git -C " + filepath.Join(w, "detached", "repo") + " status --porcelain":                                   "",
			"git -C " + filepath.Join(w, "detached", "repo") + " rev-list -n 1 HEAD --not --branches --remotes --tags": "abc123",
		},
	}
	got := run(t, testEnv(home, "darwin", fake))["codex-worktrees"]
	tiers := map[string]finding.Tier{}
	warnings := map[string]string{}
	for _, f := range got {
		tiers[filepath.Base(f.Path)] = f.Tier
		warnings[filepath.Base(f.Path)] = f.Warning
	}
	require.Equal(t, map[string]finding.Tier{
		"clean": finding.TierB, "dirty": finding.TierC, "broken": finding.TierC, "detached": finding.TierC, "empty-leftover": finding.TierB,
	}, tiers)
	require.Contains(t, warnings["detached"], "commits that no branch")
	require.Contains(t, warnings["dirty"], "repo has uncommitted changes")
	require.Contains(t, warnings["broken"], "could not report")
	require.Contains(t, warnings["clean"], "git worktree prune")

	noGit := run(t, testEnv(home, "darwin", &execx.Fake{}))["codex-worktrees"]
	for _, f := range noGit {
		if filepath.Base(f.Path) != "empty-leftover" {
			require.Equal(t, finding.TierC, f.Tier, "without git nothing can be checked")
		}
	}
}

func TestCodexSessions(t *testing.T) {
	home := fakeHome(t)
	s := filepath.Join(home, ".codex", "sessions")
	put(t, filepath.Join(s, "2026", "07", "01", "rollout-a.jsonl"), 800)
	put(t, filepath.Join(s, "2026", "10", "01", "rollout-b.jsonl"), 900)
	put(t, filepath.Join(home, ".codex", "archived_sessions", "rollout-c.jsonl"), 100)
	put(t, filepath.Join(s, "2026", "08", "03", "rollout-resumed.jsonl"), 700)
	ageTree(t, s, 60*24*time.Hour)
	ageTree(t, filepath.Join(s, "2026", "10"), 2*24*time.Hour)
	// A conversation resumed last week was appended to, but its folders
	// kept their old times.
	recent := now.Add(-7 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(s, "2026", "08", "03", "rollout-resumed.jsonl"), recent, recent))

	got := run(t, testEnv(home, "linux", nil))["codex-sessions"]
	require.Equal(t, []string{filepath.Join(home, ".codex", "archived_sessions"), filepath.Join(s, "2026", "07")}, paths(got))
	for _, f := range got {
		require.Equal(t, finding.TierB, f.Tier)
	}
}

// ageTree sets the times of dir and everything below it to d before now.
func ageTree(t *testing.T, dir string, d time.Duration) {
	t.Helper()
	ts := now.Add(-d)
	require.NoError(t, filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, ts, ts)
	}))
}

func TestClaudeVM(t *testing.T) {
	home := fakeHome(t)
	support := filepath.Join(home, "Library", "Application Support", "Claude")
	put(t, filepath.Join(support, "vm_bundles", "claudevm.bundle", "rootfs.img"), 5000)
	put(t, filepath.Join(support, "vm_bundles", "warm", "x"), 50)
	put(t, filepath.Join(support, "claude-code-vm", ".sdk-version"), 0)
	require.NoError(t, os.WriteFile(filepath.Join(support, "claude-code-vm", ".sdk-version"), []byte("2.1.197\n"), 0o644))
	put(t, filepath.Join(support, "claude-code-vm", "2.1.197", "claude"), 300)
	put(t, filepath.Join(support, "claude-code-vm", "2.0.1", "claude"), 200)

	got := run(t, testEnv(home, "darwin", nil))["claude-vm"]
	require.Equal(t, []string{
		filepath.Join(support, "claude-code-vm", "2.0.1"),
		filepath.Join(support, "vm_bundles", "claudevm.bundle"),
	}, paths(got))
}

// TestApplyRemovesOnlyAppCacheTargets scans a macOS fixture with every
// kind of app cache finding that apply acts on, applies all of them, tier C
// included, and checks that exactly those targets are gone: the cache folder
// itself, the entries left out, dirty worktrees, other simulators and
// everything else survive.
func TestApplyRemovesOnlyAppCacheTargets(t *testing.T) {
	home := fakeHome(t)
	c := filepath.Join(home, "Library", "Caches")
	put(t, filepath.Join(c, "Google", "Chrome", "cache"), 2_000_000)
	put(t, filepath.Join(c, "Google.bak", "keep"), 10)
	put(t, filepath.Join(c, "Slack.ShipIt", "update.zip"), 2_000_000)
	put(t, filepath.Join(c, "com.apple.Safari", "keep"), 2_000_000)
	put(t, filepath.Join(c, "Homebrew", "keep"), 2_000_000)
	put(t, filepath.Join(c, "ms-playwright", "chromium-1234", "chrome"), 2_000_000)
	put(t, filepath.Join(c, "ms-playwright", "mcp-chrome-abc", "Cookies"), 1000)
	put(t, filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData", "App-abc", "Build", "x"), 500)
	put(t, filepath.Join(home, "Library", "Developer", "Xcode", "Archives", "keep"), 500)
	put(t, filepath.Join(home, ".codex", "sessions", "2026", "07", "01", "a.jsonl"), 800)
	put(t, filepath.Join(home, ".codex", "config.toml"), 10)
	ageTree(t, filepath.Join(home, ".codex", "sessions"), 60*24*time.Hour)
	w := filepath.Join(home, ".codex", "worktrees")
	put(t, filepath.Join(w, "clean", "repo", ".git"), 40)
	put(t, filepath.Join(w, "clean", "repo", "main.go"), 1000)
	put(t, filepath.Join(w, "dirty", "repo", ".git"), 40)
	put(t, filepath.Join(w, "dirty", "repo", "main.go"), 1000)
	support := filepath.Join(home, "Library", "Application Support", "Claude")
	put(t, filepath.Join(support, "vm_bundles", "claudevm.bundle", "rootfs.img"), 5000)
	put(t, filepath.Join(support, "config.json"), 10)
	devices := filepath.Join(home, "Library", "Developer", "CoreSimulator", "Devices")
	gone := "11111111-2222-4333-8444-555555555555"
	ok := "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	put(t, filepath.Join(devices, gone, "data", "x"), 700)
	put(t, filepath.Join(devices, ok, "data", "x"), 900)
	put(t, filepath.Join(devices, "device_set.plist"), 10)
	list := `{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-16-4":[{"udid":"` + gone + `","name":"iPhone 14","isAvailable":false}],` +
		`"com.apple.CoreSimulator.SimRuntime.iOS-18-0":[{"udid":"` + ok + `","name":"iPhone 16","isAvailable":true}]}}`
	fake := &execx.Fake{
		Tools: map[string]string{"git": "/usr/bin/git", "xcrun": "/usr/bin/xcrun"},
		Outputs: map[string]string{
			"git -C " + filepath.Join(w, "clean", "repo") + " status --porcelain":                                   "",
			"git -C " + filepath.Join(w, "clean", "repo") + " rev-list -n 1 HEAD --not --branches --remotes --tags": "",
			"git -C " + filepath.Join(w, "dirty", "repo") + " status --porcelain":                                   " M main.go",
			"xcrun simctl list -j devices": list,
		},
		// xcrun simctl delete removes the device's folder, as CoreSimulator does.
		OnRun: func(argv []string) error {
			if len(argv) == 4 && argv[2] == "delete" {
				return os.RemoveAll(filepath.Join(devices, argv[3]))
			}
			return fmt.Errorf("unexpected command %v", argv)
		},
	}

	got := run(t, testEnv(home, "darwin", fake))
	r := &finding.Report{Version: finding.ReportVersion, Created: now, Roots: []string{home}}
	for _, fs := range got {
		r.Findings = append(r.Findings, fs...)
	}
	targets := make([]string, 0, len(r.Findings))
	tiers := map[string]finding.Tier{}
	for _, f := range r.Findings {
		targets = append(targets, f.Path)
		rel, err := filepath.Rel(home, f.Path)
		require.NoError(t, err)
		tiers[filepath.ToSlash(rel)] = f.Tier
	}
	require.Equal(t, map[string]finding.Tier{
		"Library/Caches/Google":                                         finding.TierA,
		"Library/Caches/Slack.ShipIt":                                   finding.TierA,
		"Library/Caches/ms-playwright":                                  finding.TierC,
		"Library/Developer/Xcode/DerivedData/App-abc":                   finding.TierA,
		".codex/sessions/2026/07":                                       finding.TierB,
		".codex/worktrees/clean":                                        finding.TierB,
		".codex/worktrees/dirty":                                        finding.TierC,
		"Library/Application Support/Claude/vm_bundles/claudevm.bundle": finding.TierB,
		"Library/Developer/CoreSimulator/Devices/" + gone:               finding.TierB,
	}, tiers)
	p, err := plan.New(r, r.Findings, "host", now)
	require.NoError(t, err)
	require.NoError(t, p.Validate())

	before := tree(t, home)
	sum, err := apply.Run(context.Background(), p, apply.Options{Home: home, Walker: fsx.NewWalker(2), Exec: fake, Now: func() time.Time { return now }})
	require.NoError(t, err)
	require.Len(t, sum.Outcomes, len(tiers))
	for _, o := range sum.Outcomes {
		require.Equal(t, apply.StatusDone, o.Status, o.Action.Label())
	}
	var want []string
	for _, rel := range before {
		if !slices.ContainsFunc(targets, func(t string) bool { return rel == t || strings.HasPrefix(rel, t+"/") }) {
			want = append(want, rel)
		}
	}
	require.Equal(t, want, tree(t, home))
	require.DirExists(t, c)
	require.DirExists(t, filepath.Join(c, "Google.bak"))
	require.DirExists(t, filepath.Join(devices, ok))
	require.FileExists(t, filepath.Join(support, "config.json"))
	require.Equal(t, [][]string{{"xcrun", "simctl", "delete", gone}}, fake.Ran())
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out = append(out, p)
		return nil
	}))
	slices.Sort(out)
	return out
}
