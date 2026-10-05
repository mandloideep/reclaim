package appcache

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
)

// minEntrySize is the smallest cache folder entry the catch-all scanner
// reports. The cache folder holds hundreds of tiny entries that are not worth
// a row each, even with --min-size 0.
const minEntrySize = 1_000_000

// claimedNames are cache folder entries that other scanners report, at their
// default locations, with a better tier and restore hint. The catch-all
// scanner skips them even when those scanners do not run, so a scan limited
// to app caches does not list a package cache as an app cache. Names are
// compared case insensitively.
func claimedNames() []string {
	return []string{
		// Package caches.
		"homebrew", "pnpm", "pip", "go-build", "org.swift.swiftpm", "yarn", "cocoapods", "composer", "uv",
		// Browser downloads.
		"ms-playwright", "ms-playwright-go", "ms-playwright-mcp", "puppeteer",
		// reclaim keeps its last report and its apply logs here.
		"reclaim",
	}
}

// deniedNames are cache folder entries known to hold data that is not a
// cache, such as offline music or downloaded models, or that the system
// manages itself. They are never offered. Names are compared case
// insensitively.
func deniedNames() []string {
	return []string{
		"cloudkit", "familycircle", "familycircled", "gamekit", "passkit", "geoservices",
		"com.spotify.client", "spotify", "huggingface", "torch", "pypoetry", "evolution",
		"dev.kdrag0n.macvirt", "lm-studio", "whisper", "modelscope", "chroma", "sentence_transformers",
		"vllm", "keepassxc", "flatpak",
	}
}

// deniedPrefixes cover families of entries that are never offered: Apple's
// own services, which keep state such as iCloud sync data there, and Docker.
func deniedPrefixes() []string {
	return []string{"com.apple.", "com.docker."}
}

func isUpdater(name string) bool {
	return strings.HasSuffix(name, ".ShipIt") || strings.HasSuffix(name, "-updater")
}

func skipCatchAll(name string) bool {
	lower := strings.ToLower(name)
	if isUpdater(name) || slices.Contains(claimedNames(), lower) || slices.Contains(deniedNames(), lower) {
		return true
	}
	return slices.ContainsFunc(deniedPrefixes(), func(p string) bool { return strings.HasPrefix(lower, p) })
}

// cacheFolder returns the per user cache folder when it is safe to treat
// its entries as caches: it must lie strictly inside the home directory. A
// cache variable pointing at the home directory or elsewhere would turn
// every folder there into a cache entry, so such a folder is not scanned.
func cacheFolder(env *scan.Env) (string, bool) {
	dir := resolve(env.UserCacheDir())
	home := resolve(env.Home)
	if dir == home || !project.IsWithin(dir, home) {
		env.Diag.Warn(dir, "not inside the home directory, so its entries are not offered as caches")
		return "", false
	}
	return dir, true
}

// customStore checks a browser folder named by an environment variable: it
// must lie strictly inside the home directory, must not be the cache folder
// or the home directory or hold either of them, and must hold at least one
// entry that looks like a downloaded browser.
func customStore(env *scan.Env, dir string, looksLike func(name string) bool) bool {
	home := resolve(env.Home)
	cache := resolve(env.UserCacheDir())
	if dir == home || !project.IsWithin(dir, home) || project.IsWithin(cache, dir) {
		env.Diag.Warn(dir, "not a folder of its own inside the home directory, so it is not offered")
		return false
	}
	return slices.ContainsFunc(subdirs(env, dir), func(e fs.DirEntry) bool { return looksLike(e.Name()) })
}

func playwrightBrowser(name string) bool {
	return slices.ContainsFunc([]string{"chromium", "firefox", "webkit", "ffmpeg", "winldd", "android"}, func(p string) bool {
		return strings.HasPrefix(name, p)
	})
}

func puppeteerBrowser(name string) bool {
	return slices.Contains([]string{"chrome", "chrome-headless-shell", "chromium", "firefox", "chromedriver"}, name)
}

func scanUserCaches(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir, ok := cacheFolder(env)
	if !ok {
		return nil, nil
	}
	// ~/Library/Caches is a folder apps are told to treat as disposable.
	// ~/.cache on Linux is used less strictly, so its entries are tier B and
	// are never preselected.
	tier, warning := finding.TierA, "quit the app first if it is running"
	if env.GOOS != "darwin" {
		tier, warning = finding.TierB, "quit the app first if it is running; check that it keeps only a cache here"
	}
	var entries []entry
	for _, e := range subdirs(env, dir) {
		if skipCatchAll(e.Name()) {
			continue
		}
		entries = append(entries, entry{
			path:    filepath.Join(dir, e.Name()),
			tier:    tier,
			restore: "the app downloads or rebuilds what it needs",
			warning: warning,
		})
	}
	fs, err := collect(ctx, env, entries)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(fs, func(f finding.Finding) bool { return f.Size < minEntrySize }), nil
}

func scanElectronUpdaters(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir, ok := cacheFolder(env)
	if !ok {
		return nil, nil
	}
	var entries []entry
	for _, e := range subdirs(env, dir) {
		if !isUpdater(e.Name()) {
			continue
		}
		entries = append(entries, entry{
			path:    filepath.Join(dir, e.Name()),
			tier:    finding.TierA,
			restore: "the app downloads its next update again when one is available",
		})
	}
	return collect(ctx, env, entries)
}

func scanPlaywright(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	var entries []entry
	cache, cacheOK := cacheFolder(env)
	browsers := ""
	if custom := absVar(env, "PLAYWRIGHT_BROWSERS_PATH"); custom != "" {
		if custom = resolve(custom); customStore(env, custom, playwrightBrowser) {
			browsers = custom
		}
	} else if cacheOK {
		browsers = filepath.Join(cache, "ms-playwright")
	}
	if browsers != "" {
		main := entry{path: browsers, tier: finding.TierB, restore: "downloaded again by npx playwright install"}
		// Playwright MCP keeps persistent browser profiles, with their
		// logins, next to the browsers when it runs with the default settings.
		var profiles []string
		for _, e := range subdirs(env, main.path) {
			if strings.HasPrefix(e.Name(), "mcp-") {
				profiles = append(profiles, e.Name())
			}
		}
		if len(profiles) > 0 {
			main.tier = finding.TierC
			main.warning = "also holds Playwright MCP browser profiles (" + strings.Join(profiles, ", ") + "), whose logins and history are lost"
		}
		entries = append(entries, main)
	}
	if cacheOK {
		entries = append(entries,
			entry{
				path:    filepath.Join(cache, "ms-playwright-go"),
				tier:    finding.TierB,
				restore: "downloaded again by the next playwright-go install",
			},
			entry{
				path:    filepath.Join(cache, "ms-playwright-mcp"),
				tier:    finding.TierC,
				restore: "created again empty by the next Playwright MCP session",
				warning: "browser profiles with their logins and history, which are lost",
			})
	}
	return collect(ctx, env, entries)
}

func scanPuppeteer(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := filepath.Join(env.Home, ".cache", "puppeteer")
	if custom := absVar(env, "PUPPETEER_CACHE_DIR"); custom != "" {
		if dir = resolve(custom); !customStore(env, dir, puppeteerBrowser) {
			return nil, nil
		}
	}
	return collect(ctx, env, []entry{{
		path:    resolve(dir),
		tier:    finding.TierB,
		restore: "downloaded again by npx puppeteer browsers install or the next npm install of puppeteer",
	}})
}

// firstNonEmpty returns the first argument that is not empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
