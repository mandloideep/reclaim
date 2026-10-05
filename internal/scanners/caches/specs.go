package caches

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// specs lists every package cache scanner.
func specs() []spec {
	return []spec{
		{name: "npm-cache", ecosystem: "node", description: "npm package cache (_cacache)", locate: locateNpmCache},
		{name: "npx-cache", ecosystem: "node", description: "npx package cache (_npx)", locate: locateNpxCache},
		{name: "pnpm-store", ecosystem: "node", description: "pnpm content addressable store", locate: locatePnpmStore},
		{name: "pnpm-cache", ecosystem: "node", description: "pnpm metadata cache", locate: locatePnpmCache},
		{name: "bun-cache", ecosystem: "node", description: "bun install cache", locate: locateBunCache},
		{name: "yarn-cache", ecosystem: "node", description: "Yarn classic cache and Yarn Berry global cache", locate: locateYarnCache},
		{name: "uv-cache", ecosystem: "python", description: "uv cache", locate: locateUvCache},
		{name: "pip-cache", ecosystem: "python", description: "pip wheel and HTTP cache", locate: locatePipCache},
		{name: "go-modcache", ecosystem: "go", description: "Go module cache (GOMODCACHE)", locate: locateGoModCache},
		{name: "go-build-cache", ecosystem: "go", description: "Go build cache (GOCACHE)", locate: locateGoBuildCache},
		{name: "cargo-registry", ecosystem: "rust", description: "Cargo registry index, crate archives and sources", locate: locateCargo("registry")},
		{name: "cargo-git", ecosystem: "rust", description: "Cargo git dependency checkouts", locate: locateCargo("git")},
		{name: "homebrew-cache", ecosystem: "homebrew", description: "Homebrew download cache", locate: locateHomebrew},
		{name: "swiftpm-cache", ecosystem: "swift", description: "Swift Package Manager cache", locate: locateSwiftPM},
		{name: "gradle-cache", ecosystem: "java", description: "Gradle dependency caches and wrapper distributions", locate: locateGradle},
		{name: "maven-repo", ecosystem: "java", description: "Maven local repository", locate: locateMaven},
		{name: "cocoapods-cache", ecosystem: "cocoapods", description: "CocoaPods download cache", locate: locateCocoaPods},
		{name: "composer-cache", ecosystem: "php", description: "Composer download cache", locate: locateComposer},
	}
}

func npmDir(ctx context.Context, env *scan.Env) (dir string, fromTool bool) {
	if d := query(ctx, env, "npm", "config", "get", "cache"); d != "" {
		return d, true
	}
	return firstNonEmpty(absVar(env, "npm_config_cache"), absVar(env, "NPM_CONFIG_CACHE"), filepath.Join(env.Home, ".npm")), false
}

func locateNpmCache(ctx context.Context, env *scan.Env) []location {
	dir, fromTool := npmDir(ctx, env)
	return []location{{
		path:    filepath.Join(dir, "_cacache"),
		tier:    finding.TierB,
		restore: "downloaded again by the next npm install",
		command: withCommand(fromTool, "npm", "cache", "clean", "--force"),
	}}
}

func locateNpxCache(ctx context.Context, env *scan.Env) []location {
	dir, _ := npmDir(ctx, env)
	return []location{{
		path:    filepath.Join(dir, "_npx"),
		tier:    finding.TierB,
		restore: "downloaded again by the next npx run",
	}}
}

func locatePnpmStore(ctx context.Context, env *scan.Env) []location {
	store := query(ctx, env, "pnpm", "store", "path")
	if store == "" {
		dataHome := filepath.Join(env.Home, "Library")
		if env.GOOS != "darwin" {
			dataHome = firstNonEmpty(absVar(env, "XDG_DATA_HOME"), filepath.Join(env.Home, ".local", "share"))
		}
		store = filepath.Join(firstNonEmpty(absVar(env, "PNPM_HOME"), filepath.Join(dataHome, "pnpm")), "store")
	}
	return []location{{
		path:    store,
		tier:    finding.TierB,
		restore: "downloaded again by the next pnpm install",
		warning: "files hard linked into node_modules folders are only freed once those folders are removed too",
	}}
}

func locatePnpmCache(_ context.Context, env *scan.Env) []location {
	return []location{{
		path:    filepath.Join(env.UserCacheDir(), "pnpm"),
		tier:    finding.TierB,
		restore: "metadata downloaded again by the next pnpm install",
	}}
}

func locateBunCache(ctx context.Context, env *scan.Env) []location {
	dir := query(ctx, env, "bun", "pm", "cache")
	fromTool := dir != ""
	if !fromTool {
		dir = firstNonEmpty(
			absVar(env, "BUN_INSTALL_CACHE_DIR"),
			joinIf(absVar(env, "BUN_INSTALL"), "install", "cache"),
			filepath.Join(env.Home, ".bun", "install", "cache"),
		)
	}
	return []location{{
		path:    dir,
		tier:    finding.TierB,
		restore: "downloaded again by the next bun install",
		command: withCommand(fromTool, "bun", "pm", "cache", "rm"),
	}}
}

func locateYarnCache(ctx context.Context, env *scan.Env) []location {
	restore := "downloaded again by the next yarn install"
	berry := location{
		path:    filepath.Join(firstNonEmpty(absVar(env, "YARN_GLOBAL_FOLDER"), filepath.Join(env.Home, ".yarn", "berry")), "cache"),
		tier:    finding.TierB,
		name:    "yarn berry global cache",
		restore: restore,
	}
	// "yarn cache dir" only exists in Yarn classic, so an answer means the
	// classic clean command applies to that directory.
	if dir := query(ctx, env, "yarn", "cache", "dir"); dir != "" {
		return []location{{
			path:    dir,
			tier:    finding.TierB,
			name:    "yarn classic cache",
			restore: restore,
			command: []string{"yarn", "cache", "clean"},
		}, berry}
	}
	classic := filepath.Join(env.UserCacheDir(), "yarn")
	if env.GOOS == "darwin" {
		classic = filepath.Join(env.UserCacheDir(), "Yarn")
	}
	return []location{{
		path:    firstNonEmpty(absVar(env, "YARN_CACHE_FOLDER"), classic),
		tier:    finding.TierB,
		name:    "yarn classic cache",
		restore: restore,
	}, berry}
}

func locateUvCache(ctx context.Context, env *scan.Env) []location {
	dir := query(ctx, env, "uv", "cache", "dir")
	fromTool := dir != ""
	if !fromTool {
		// uv uses the XDG layout on macOS too.
		dir = firstNonEmpty(
			absVar(env, "UV_CACHE_DIR"),
			joinIf(absVar(env, "XDG_CACHE_HOME"), "uv"),
			filepath.Join(env.Home, ".cache", "uv"),
		)
	}
	return []location{{
		path:    dir,
		tier:    finding.TierB,
		restore: "downloaded again by the next uv sync or uv pip install",
		command: withCommand(fromTool, "uv", "cache", "clean"),
	}}
}

func locatePipCache(ctx context.Context, env *scan.Env) []location {
	for _, tool := range []string{"pip3", "pip"} {
		if dir := query(ctx, env, tool, "cache", "dir"); dir != "" {
			return []location{{
				path:    dir,
				tier:    finding.TierB,
				restore: "downloaded again by the next pip install",
				command: []string{tool, "cache", "purge"},
			}}
		}
	}
	return []location{{
		path:    firstNonEmpty(absVar(env, "PIP_CACHE_DIR"), filepath.Join(env.UserCacheDir(), "pip")),
		tier:    finding.TierB,
		restore: "downloaded again by the next pip install",
	}}
}

func goEnv(ctx context.Context, env *scan.Env, key string) string {
	return query(ctx, env, "go", "env", key)
}

func locateGoModCache(ctx context.Context, env *scan.Env) []location {
	dir := goEnv(ctx, env, "GOMODCACHE")
	fromTool := dir != ""
	if !fromTool {
		gopath := filepath.Join(env.Home, "go")
		if v := env.Var("GOPATH"); v != "" {
			first, _, _ := strings.Cut(v, string(os.PathListSeparator))
			if filepath.IsAbs(first) {
				gopath = first
			}
		}
		dir = firstNonEmpty(absVar(env, "GOMODCACHE"), filepath.Join(gopath, "pkg", "mod"))
	}
	return []location{{
		path:    dir,
		tier:    finding.TierB,
		restore: "downloaded again by the next go build or go mod download",
		command: withCommand(fromTool, "go", "clean", "-modcache"),
	}}
}

func locateGoBuildCache(ctx context.Context, env *scan.Env) []location {
	dir := goEnv(ctx, env, "GOCACHE")
	fromTool := dir != ""
	if !fromTool {
		dir = firstNonEmpty(absVar(env, "GOCACHE"), filepath.Join(env.UserCacheDir(), "go-build"))
	}
	return []location{{
		path:    dir,
		tier:    finding.TierA,
		restore: "rebuilt automatically by the next go build",
		command: withCommand(fromTool, "go", "clean", "-cache"),
	}}
}

func locateCargo(sub string) func(context.Context, *scan.Env) []location {
	return func(_ context.Context, env *scan.Env) []location {
		home := firstNonEmpty(absVar(env, "CARGO_HOME"), filepath.Join(env.Home, ".cargo"))
		return []location{{
			path:    filepath.Join(home, sub),
			tier:    finding.TierB,
			restore: "downloaded again by the next cargo build",
		}}
	}
}

func locateHomebrew(ctx context.Context, env *scan.Env) []location {
	if dir := query(ctx, env, "brew", "--cache"); dir != "" {
		return []location{{
			path:    dir,
			tier:    finding.TierB,
			restore: "downloaded again by the next brew install or upgrade",
			warning: "brew cleanup -s also removes old versions of installed formulae and casks",
			command: []string{"brew", "cleanup", "-s"},
		}}
	}
	def := filepath.Join(env.UserCacheDir(), "Homebrew")
	return []location{{
		path:    firstNonEmpty(absVar(env, "HOMEBREW_CACHE"), def),
		tier:    finding.TierB,
		restore: "downloaded again by the next brew install or upgrade",
	}}
}

func locateSwiftPM(_ context.Context, env *scan.Env) []location {
	return []location{{
		path:    filepath.Join(env.UserCacheDir(), "org.swift.swiftpm"),
		tier:    finding.TierB,
		restore: "resolved again by the next swift package resolve or Xcode build",
	}}
}

func locateGradle(_ context.Context, env *scan.Env) []location {
	home := firstNonEmpty(absVar(env, "GRADLE_USER_HOME"), filepath.Join(env.Home, ".gradle"))
	const stop = "stop running Gradle daemons first with gradle --stop"
	return []location{
		{
			path:    filepath.Join(home, "caches"),
			tier:    finding.TierB,
			name:    "gradle caches",
			restore: "downloaded again by the next Gradle build",
			warning: stop,
		},
		{
			path:    filepath.Join(home, "wrapper", "dists"),
			tier:    finding.TierB,
			name:    "gradle wrapper distributions",
			restore: "downloaded again by the next ./gradlew run",
			warning: stop,
		},
	}
}

func locateMaven(_ context.Context, env *scan.Env) []location {
	m2 := filepath.Join(env.Home, ".m2")
	repo := firstNonEmpty(mavenLocalRepository(filepath.Join(m2, "settings.xml"), env.Home), filepath.Join(m2, "repository"))
	return []location{{
		path:    repo,
		tier:    finding.TierB,
		restore: "downloaded again by the next Maven build",
		warning: "artifacts you installed locally with mvn install must be installed again",
	}}
}

// mavenLocalRepository reads <localRepository> from a Maven settings file.
func mavenLocalRepository(settings, home string) string {
	data, err := os.ReadFile(settings)
	if err != nil {
		return ""
	}
	var s struct {
		LocalRepository string `xml:"localRepository"`
	}
	if xml.Unmarshal(data, &s) != nil {
		return ""
	}
	repo := strings.TrimSpace(s.LocalRepository)
	repo = strings.ReplaceAll(repo, "${user.home}", home)
	if strings.HasPrefix(repo, "~/") {
		repo = filepath.Join(home, repo[2:])
	}
	if !filepath.IsAbs(repo) {
		return ""
	}
	return filepath.Clean(repo)
}

func locateCocoaPods(_ context.Context, env *scan.Env) []location {
	return []location{{
		path: firstNonEmpty(absVar(env, "CP_CACHE_DIR"), filepath.Join(env.UserCacheDir(), "CocoaPods")),
		tier: finding.TierB,
		// CocoaPods cannot be asked where its cache is, so the folder found
		// here is removed directly rather than trusting "pod cache clean" to
		// clean the same one.
		restore: "downloaded again by the next pod install",
	}}
}

func locateComposer(ctx context.Context, env *scan.Env) []location {
	restore := "downloaded again by the next composer install"
	if dir := query(ctx, env, "composer", "config", "--global", "cache-dir"); dir != "" {
		return []location{{path: dir, tier: finding.TierB, restore: restore, command: []string{"composer", "clear-cache"}}}
	}
	def := filepath.Join(env.UserCacheDir(), "composer")
	return []location{
		{path: firstNonEmpty(absVar(env, "COMPOSER_CACHE_DIR"), def), tier: finding.TierB, restore: restore},
		{path: filepath.Join(firstNonEmpty(absVar(env, "COMPOSER_HOME"), filepath.Join(env.Home, ".composer")), "cache"), tier: finding.TierB, restore: restore},
	}
}

// joinIf joins elems onto base when base is not empty.
func joinIf(base string, elems ...string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elems...)...)
}
