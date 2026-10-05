package caches

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
)

func put(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("cached bytes"), 0o644))
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

func runScanners(t *testing.T, env scan.Env) map[string][]finding.Finding {
	t.Helper()
	ss := make([]scan.Scanner, 0, len(Scanners()))
	for _, s := range Scanners() {
		ss = append(ss, s)
	}
	env.Walker = fsx.NewWalker(2)
	res := scan.Run(context.Background(), ss, env, 4)
	require.Empty(t, res.Warnings)
	out := map[string][]finding.Finding{}
	for _, f := range res.Findings {
		out[f.Scanner] = append(out[f.Scanner], f)
	}
	return out
}

// defaultLayout creates every cache at its default location in a fake home.
func defaultLayout(t *testing.T, home, goos string) map[string][]string {
	t.Helper()
	cache := filepath.Join(home, ".cache")
	if goos == "darwin" {
		cache = filepath.Join(home, "Library", "Caches")
	}
	pnpmStore := filepath.Join(home, ".local", "share", "pnpm", "store")
	yarnClassic := filepath.Join(cache, "yarn")
	if goos == "darwin" {
		pnpmStore = filepath.Join(home, "Library", "pnpm", "store")
		yarnClassic = filepath.Join(cache, "Yarn")
	}
	layout := map[string][]string{
		"npm-cache":       {filepath.Join(home, ".npm", "_cacache")},
		"npx-cache":       {filepath.Join(home, ".npm", "_npx")},
		"pnpm-store":      {pnpmStore},
		"pnpm-cache":      {filepath.Join(cache, "pnpm")},
		"bun-cache":       {filepath.Join(home, ".bun", "install", "cache")},
		"yarn-cache":      {yarnClassic, filepath.Join(home, ".yarn", "berry", "cache")},
		"uv-cache":        {filepath.Join(home, ".cache", "uv")},
		"pip-cache":       {filepath.Join(cache, "pip")},
		"go-modcache":     {filepath.Join(home, "go", "pkg", "mod")},
		"go-build-cache":  {filepath.Join(cache, "go-build")},
		"cargo-registry":  {filepath.Join(home, ".cargo", "registry")},
		"cargo-git":       {filepath.Join(home, ".cargo", "git")},
		"homebrew-cache":  {filepath.Join(cache, "Homebrew")},
		"swiftpm-cache":   {filepath.Join(cache, "org.swift.swiftpm")},
		"gradle-cache":    {filepath.Join(home, ".gradle", "caches"), filepath.Join(home, ".gradle", "wrapper", "dists")},
		"maven-repo":      {filepath.Join(home, ".m2", "repository")},
		"cocoapods-cache": {filepath.Join(cache, "CocoaPods")},
		"composer-cache":  {filepath.Join(cache, "composer"), filepath.Join(home, ".composer", "cache")},
	}
	for _, dirs := range layout {
		for _, d := range dirs {
			put(t, filepath.Join(d, "entry"))
		}
	}
	return layout
}

func TestDefaultLocationsWithoutTools(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := resolved(t, t.TempDir())
			layout := defaultLayout(t, home, goos)
			got := runScanners(t, scan.Env{Home: home, GOOS: goos, Exec: &execx.Fake{}})

			require.Len(t, got, len(Scanners()), "every scanner finds its default cache")
			for name, dirs := range layout {
				fs := got[name]
				require.Len(t, fs, len(dirs), name)
				paths := make([]string, 0, len(fs))
				for _, f := range fs {
					paths = append(paths, f.Path)
					require.Equal(t, finding.ActionRemovePath, f.Action, "%s: without the tool there is no clean command", name)
					require.Equal(t, finding.KindDir, f.Kind)
					require.Equal(t, int64(len("cached bytes")), f.Size)
					require.NotEmpty(t, f.Restore)
					wantTier := finding.TierB
					if name == "go-build-cache" {
						wantTier = finding.TierA
					}
					require.Equal(t, wantTier, f.Tier, name)
				}
				require.ElementsMatch(t, dirs, paths, name)
			}
		})
	}
}

func TestToolReportedLocationsUseCleanCommands(t *testing.T) {
	home := resolved(t, t.TempDir())
	custom := filepath.Join(home, "custom")
	dirs := map[string]string{
		"npm":      filepath.Join(custom, "npm"),
		"pnpm":     filepath.Join(custom, "pnpm-store", "v10"),
		"bun":      filepath.Join(custom, "bun"),
		"yarn":     filepath.Join(custom, "yarn", "v6"),
		"uv":       filepath.Join(custom, "uv"),
		"pip3":     filepath.Join(custom, "pip"),
		"gomod":    filepath.Join(custom, "gomod"),
		"gocache":  filepath.Join(custom, "gocache"),
		"brew":     filepath.Join(custom, "brew"),
		"composer": filepath.Join(custom, "composer"),
	}
	put(t, filepath.Join(dirs["npm"], "_cacache", "x"))
	put(t, filepath.Join(dirs["npm"], "_npx", "x"))
	for k, d := range dirs {
		if k != "npm" {
			put(t, filepath.Join(d, "x"))
		}
	}
	put(t, filepath.Join(home, "Library", "Caches", "CocoaPods", "x"))
	tools := map[string]string{}
	for _, tool := range []string{"npm", "pnpm", "bun", "yarn", "uv", "pip3", "go", "brew", "composer", "pod"} {
		tools[tool] = "/usr/local/bin/" + tool
	}
	fake := &execx.Fake{
		Tools: tools,
		Outputs: map[string]string{
			"npm config get cache":               dirs["npm"] + "\n",
			"pnpm store path":                    dirs["pnpm"],
			"bun pm cache":                       dirs["bun"],
			"yarn cache dir":                     dirs["yarn"],
			"uv cache dir":                       dirs["uv"],
			"pip3 cache dir":                     dirs["pip3"],
			"go env GOMODCACHE":                  dirs["gomod"],
			"go env GOCACHE":                     dirs["gocache"],
			"brew --cache":                       dirs["brew"],
			"composer config --global cache-dir": "Warning: running as root\n" + dirs["composer"],
		},
	}
	got := runScanners(t, scan.Env{Home: home, GOOS: "darwin", Exec: fake})

	wantCommand := map[string][]string{
		"npm-cache":       {"npm", "cache", "clean", "--force"},
		"bun-cache":       {"bun", "pm", "cache", "rm"},
		"yarn-cache":      {"yarn", "cache", "clean"},
		"uv-cache":        {"uv", "cache", "clean"},
		"pip-cache":       {"pip3", "cache", "purge"},
		"go-modcache":     {"go", "clean", "-modcache"},
		"go-build-cache":  {"go", "clean", "-cache"},
		"homebrew-cache":  {"brew", "cleanup", "-s"},
		"composer-cache":  {"composer", "clear-cache"},
		"cocoapods-cache": {"pod", "cache", "clean", "--all"},
	}
	for name, cmd := range wantCommand {
		require.Len(t, got[name], 1, name)
		f := got[name][0]
		require.Equal(t, finding.ActionRunCommand, f.Action, name)
		require.Equal(t, cmd, f.Command, name)
		require.True(t, plan.AllowedCommand(f.Command), "%s emits a command apply refuses: %v", name, f.Command)
	}
	require.Equal(t, filepath.Join(dirs["npm"], "_cacache"), got["npm-cache"][0].Path)
	require.Equal(t, filepath.Join(dirs["npm"], "_npx"), got["npx-cache"][0].Path)
	require.Equal(t, finding.ActionRemovePath, got["npx-cache"][0].Action)
	require.Equal(t, dirs["pnpm"], got["pnpm-store"][0].Path)
	require.Equal(t, finding.ActionRemovePath, got["pnpm-store"][0].Action)
	require.Equal(t, dirs["composer"], got["composer-cache"][0].Path)
	require.Contains(t, got["homebrew-cache"][0].Warning, "old versions")
}

func TestEnvironmentOverrides(t *testing.T) {
	home := resolved(t, t.TempDir())
	vars := map[string]string{
		"npm_config_cache": filepath.Join(home, "env", "npm"),
		"UV_CACHE_DIR":     filepath.Join(home, "env", "uv"),
		"PIP_CACHE_DIR":    filepath.Join(home, "env", "pip"),
		"GOPATH":           filepath.Join(home, "env", "gopath") + string(os.PathListSeparator) + "/second",
		"GOCACHE":          filepath.Join(home, "env", "gocache"),
		"CARGO_HOME":       filepath.Join(home, "env", "cargo"),
		"GRADLE_USER_HOME": filepath.Join(home, "env", "gradle"),
		"HOMEBREW_CACHE":   filepath.Join(home, "env", "brew"),
		"BUN_INSTALL":      filepath.Join(home, "env", "bun"),
		"PNPM_HOME":        filepath.Join(home, "env", "pnpm"),
		"XDG_CACHE_HOME":   "relative/is/ignored",
	}
	want := map[string]string{
		"npm-cache":      filepath.Join(vars["npm_config_cache"], "_cacache"),
		"uv-cache":       vars["UV_CACHE_DIR"],
		"pip-cache":      vars["PIP_CACHE_DIR"],
		"go-modcache":    filepath.Join(home, "env", "gopath", "pkg", "mod"),
		"go-build-cache": vars["GOCACHE"],
		"cargo-registry": filepath.Join(vars["CARGO_HOME"], "registry"),
		"gradle-cache":   filepath.Join(vars["GRADLE_USER_HOME"], "caches"),
		"homebrew-cache": vars["HOMEBREW_CACHE"],
		"bun-cache":      filepath.Join(vars["BUN_INSTALL"], "install", "cache"),
		"pnpm-store":     filepath.Join(vars["PNPM_HOME"], "store"),
	}
	for _, p := range want {
		put(t, filepath.Join(p, "x"))
	}
	got := runScanners(t, scan.Env{Home: home, GOOS: "linux", Exec: &execx.Fake{}, Getenv: func(k string) string { return vars[k] }})
	for name, p := range want {
		require.Len(t, got[name], 1, name)
		require.Equal(t, p, got[name][0].Path, name)
	}
}

func TestMavenSettingsLocalRepository(t *testing.T) {
	home := resolved(t, t.TempDir())
	settings := `<settings><localRepository>${user.home}/m2repo</localRepository></settings>`
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".m2"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".m2", "settings.xml"), []byte(settings), 0o644))
	put(t, filepath.Join(home, "m2repo", "org", "x.jar"))
	put(t, filepath.Join(home, ".m2", "repository", "ignored.jar"))

	got := runScanners(t, scan.Env{Home: home, GOOS: "linux", Exec: &execx.Fake{}})
	require.Len(t, got["maven-repo"], 1)
	require.Equal(t, filepath.Join(home, "m2repo"), got["maven-repo"][0].Path)
	require.NotEmpty(t, got["maven-repo"][0].Warning)

	require.Equal(t, "", mavenLocalRepository(filepath.Join(home, "missing.xml"), home))
	require.Equal(t, filepath.Join(home, "r"), mavenLocalRepository(writeTemp(t, `<settings><localRepository>~/r</localRepository></settings>`), home))
	require.Equal(t, "", mavenLocalRepository(writeTemp(t, `<settings><localRepository>rel</localRepository></settings>`), home))
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.xml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestMissingAndEmptyCachesAreSkipped(t *testing.T) {
	home := resolved(t, t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".npm", "_cacache"), 0o755))
	got := runScanners(t, scan.Env{Home: home, GOOS: "linux", Exec: &execx.Fake{}})
	require.Empty(t, got)
}

func TestSymlinkedCacheIsResolved(t *testing.T) {
	home := resolved(t, t.TempDir())
	target := filepath.Join(home, "elsewhere", "cargo")
	put(t, filepath.Join(target, "registry", "x"))
	require.NoError(t, os.Symlink(target, filepath.Join(home, ".cargo")))
	got := runScanners(t, scan.Env{Home: home, GOOS: "linux", Exec: &execx.Fake{}})
	require.Len(t, got["cargo-registry"], 1)
	require.Equal(t, filepath.Join(target, "registry"), got["cargo-registry"][0].Path)
}

func TestScannerMetadata(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Scanners() {
		require.False(t, seen[s.Name()], s.Name())
		seen[s.Name()] = true
		require.Equal(t, finding.CategoryPackageCache, s.Category())
		require.NotEmpty(t, s.Ecosystem())
		require.NotEmpty(t, s.Description())
		require.False(t, strings.Contains(s.Name(), " "))
	}
}
