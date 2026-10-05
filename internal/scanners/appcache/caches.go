package appcache

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
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
		"dev.kdrag0n.macvirt",
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

func scanUserCaches(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := resolve(env.UserCacheDir())
	var entries []entry
	for _, e := range subdirs(env, dir) {
		if skipCatchAll(e.Name()) {
			continue
		}
		entries = append(entries, entry{
			path:    filepath.Join(dir, e.Name()),
			tier:    finding.TierA,
			restore: "the app downloads or rebuilds what it needs",
			warning: "quit the app first if it is running",
		})
	}
	fs, err := collect(ctx, env, entries)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(fs, func(f finding.Finding) bool { return f.Size < minEntrySize }), nil
}

func scanElectronUpdaters(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := resolve(env.UserCacheDir())
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
	cache := resolve(env.UserCacheDir())
	browsers := firstNonEmpty(absVar(env, "PLAYWRIGHT_BROWSERS_PATH"), filepath.Join(cache, "ms-playwright"))
	main := entry{
		path:    resolve(browsers),
		tier:    finding.TierB,
		restore: "downloaded again by npx playwright install",
	}
	// Playwright MCP keeps persistent browser profiles, with their logins,
	// next to the browsers when it runs with the default settings.
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
	return collect(ctx, env, []entry{
		main,
		{
			path:    filepath.Join(cache, "ms-playwright-go"),
			tier:    finding.TierB,
			restore: "downloaded again by the next playwright-go install",
		},
		{
			path:    filepath.Join(cache, "ms-playwright-mcp"),
			tier:    finding.TierC,
			restore: "created again empty by the next Playwright MCP session",
			warning: "browser profiles with their logins and history, which are lost",
		},
	})
}

func scanPuppeteer(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := firstNonEmpty(absVar(env, "PUPPETEER_CACHE_DIR"), filepath.Join(env.Home, ".cache", "puppeteer"))
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
