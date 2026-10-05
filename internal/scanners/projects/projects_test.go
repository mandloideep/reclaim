package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
)

// fixture writes files relative to root. A key ending in "/" makes a directory.
func fixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if rel[len(rel)-1] == '/' {
			require.NoError(t, os.MkdirAll(p, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// runAll runs every project scanner over root and returns the findings keyed
// by path relative to root.
func runAll(t *testing.T, root string) map[string]finding.Finding {
	t.Helper()
	all := Scanners()
	env := scan.Env{
		Roots:    []string{root},
		Walker:   fsx.NewWalker(4),
		Projects: project.NewSource([]string{root}, Matchers(all), project.Options{Home: filepath.Dir(root)}),
	}
	ss := make([]scan.Scanner, 0, len(all))
	for _, s := range all {
		ss = append(ss, s)
	}
	res := scan.Run(context.Background(), ss, env, 4)
	require.Empty(t, res.Warnings)
	out := map[string]finding.Finding{}
	for _, f := range res.Findings {
		rel, err := filepath.Rel(root, f.Path)
		require.NoError(t, err)
		out[filepath.ToSlash(rel)] = f
	}
	return out
}

func TestProjectScanners(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, map[string]string{
		// Node project with a build script, pnpm lockfile and framework outputs.
		"web/package.json":                    `{"scripts":{"build":"vite build"}}`,
		"web/pnpm-lock.yaml":                  "",
		"web/node_modules/react/index.js":     "react",
		"web/node_modules/react/package.json": "{}",
		"web/node_modules/x/node_modules/y/a": "nested",
		"web/.next/cache/a":                   "next",
		"web/.turbo/a":                        "turbo",
		"web/.nuxt/a":                         "nuxt",
		"web/.svelte-kit/a":                   "svelte",
		"web/.parcel-cache/a":                 "parcel",
		"web/dist/app.js":                     "built",
		"web/src/dist/keep.txt":               "dist below the project root is not an output folder",
		"web/build/app.js":                    "built",
		// A project without build config: dist is tier C.
		"lib/package.json":      `{"name":"lib"}`,
		"lib/dist/only-copy.js": "precious",
		// node_modules with no package.json next to it.
		"stray/node_modules/z/a": "z",
		// Python.
		"py/pyproject.toml":                    "",
		"py/uv.lock":                           "",
		"py/.venv/pyvenv.cfg":                  "home = /usr/bin",
		"py/.venv/lib/site.py":                 "site",
		"py/pkg/__pycache__/m.cpython-312.pyc": "pyc",
		"py/.pytest_cache/v/a":                 "pt",
		"py/.mypy_cache/a":                     "mypy",
		"py/.ruff_cache/a":                     "ruff",
		"loose-env/pyvenv.cfg":                 "home = /usr/bin",
		// Rust, including a workspace member that must not be double counted.
		"crate/Cargo.toml":           "",
		"crate/target/debug/app":     "bin",
		"crate/member/Cargo.toml":    "",
		"notrust/target/x":           "maven or something else",
		"tagged/target/CACHEDIR.TAG": "Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by cargo.\n",
		// Go in-repo build caches.
		"gosvc/go.mod":                                 "module x",
		"gosvc/.cache/go-build/00/a":                   "obj",
		"gosvc/tmp/gocache/trim.txt":                   "1",
		"gosvc/tmp/gocache/README":                     "This directory holds cached build artifacts from the Go build system.",
		"gosvc/build/Dockerfile":                       "FROM scratch",
		"gosvc/.cache/gomod/cache/download/x":          "zip",
		"gosvc/.cache/gomod/golang.org/x/a.go":         "src",
		"gosvc/.cache/empty-mod/cache/download/":       "",
		"gosvc/vendorish/cache/notdownload":            "x",
		"gosvc/.cache/golangci-lint/trim.txt":          "1",
		"gosvc/.cache/golangci-lint/00/a":              "lint",
		"gosvc/.cache/golangci-lint/ff/b":              "lint",
		"web/.pnpm/store/v11/files/00/abc":             "blob",
		"web/.pnpm/store/v11/links/x/node_modules/y/a": "link",
		"web/.pnpm/notstore/v2/other/a":                "x",
		"outside/.cache/go-build/00/a":                 "not in a project",
		// Gradle, CocoaPods, Dart, Terraform.
		"android/build.gradle":         "",
		"android/.gradle/8.0/a":        "g",
		"android/build/outputs/a.apk":  "apk",
		"ios/Podfile":                  "",
		"ios/Pods/Alamofire/a":         "pod",
		"ios/App.xcodeproj/":           "",
		"noios/Pods/a":                 "no podfile",
		"flutter/pubspec.yaml":         "",
		"flutter/.dart_tool/a":         "dart",
		"infra/main.tf":                "",
		"infra/.terraform/providers/a": "tf",
	})
	got := runAll(t, root)

	want := map[string]struct {
		scanner string
		tier    finding.Tier
		restore string
		project string
	}{
		"web/node_modules":           {"node_modules", finding.TierA, "pnpm install", "web"},
		"web/.next":                  {"next", finding.TierA, "", "web"},
		"web/.turbo":                 {"turbo", finding.TierA, "", "web"},
		"web/.nuxt":                  {"nuxt", finding.TierA, "", "web"},
		"web/.svelte-kit":            {"svelte-kit", finding.TierA, "", "web"},
		"web/.parcel-cache":          {"parcel-cache", finding.TierA, "", "web"},
		"web/dist":                   {"dist", finding.TierA, "pnpm run build", "web"},
		"web/build":                  {"build", finding.TierA, "pnpm run build", "web"},
		"lib/dist":                   {"dist", finding.TierC, "", "lib"},
		"stray/node_modules":         {"node_modules", finding.TierB, "", ""},
		"py/.venv":                   {"python-venv", finding.TierA, "uv sync", "py"},
		"py/pkg/__pycache__":         {"pycache", finding.TierA, "", "py"},
		"py/.pytest_cache":           {"pytest-cache", finding.TierA, "", "py"},
		"py/.mypy_cache":             {"mypy-cache", finding.TierA, "", "py"},
		"py/.ruff_cache":             {"ruff-cache", finding.TierA, "", "py"},
		"loose-env":                  {"python-venv", finding.TierB, "", ""},
		"crate/target":               {"rust-target", finding.TierA, "cargo build", "crate"},
		"tagged/target":              {"rust-target", finding.TierA, "cargo build", ""},
		"gosvc/.cache/go-build":      {"go-build-local", finding.TierA, "", "gosvc"},
		"gosvc/tmp/gocache":          {"go-build-local", finding.TierA, "", "gosvc"},
		"gosvc/build":                {"build", finding.TierC, "", "gosvc"},
		"gosvc/.cache/gomod":         {"go-mod-local", finding.TierB, "", "gosvc"},
		"gosvc/.cache/golangci-lint": {"go-build-local", finding.TierA, "rebuilt by the next golangci-lint run", "gosvc"},
		"web/.pnpm/store":            {"pnpm-store-local", finding.TierB, "downloaded again by the next pnpm install", "web"},
		"android/.gradle":            {"gradle-project", finding.TierA, "", "android"},
		"android/build":              {"build", finding.TierA, "gradle build", "android"},
		"ios/Pods":                   {"pods", finding.TierA, "pod install", "ios"},
		"flutter/.dart_tool":         {"dart-tool", finding.TierA, "", "flutter"},
		"infra/.terraform":           {"terraform", finding.TierA, "", ""},
	}
	for rel, w := range want {
		f, ok := got[rel]
		require.True(t, ok, "missing finding for %s", rel)
		require.Equal(t, w.scanner, f.Scanner, rel)
		require.Equal(t, w.tier, f.Tier, rel)
		require.Equal(t, finding.ActionRemovePath, f.Action, rel)
		require.Equal(t, finding.KindDir, f.Kind, rel)
		require.Equal(t, f.Path, f.Target, rel)
		require.Positive(t, f.Size, rel)
		if w.restore != "" {
			require.Equal(t, w.restore, f.Restore, rel)
		}
		require.NotEmpty(t, f.Restore, rel)
		if w.project != "" {
			require.Equal(t, filepath.Join(root, w.project), f.Project, rel)
		}
		if f.Tier == finding.TierC || f.Tier == finding.TierB {
			require.NotEmpty(t, f.Warning, rel)
		}
	}
	for rel := range got {
		_, ok := want[rel]
		require.True(t, ok, "unexpected finding for %s", rel)
	}

	// Sizes count the whole folder, nested node_modules included.
	require.Equal(t, int64(len("react")+len("{}")+len("nested")), got["web/node_modules"].Size)
}

func TestLastUsedComesFromProjectActivity(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, map[string]string{
		"app/package.json":     "{}",
		"app/index.js":         "x",
		"app/node_modules/a/b": "y",
	})
	old := time.Now().Add(-300 * 24 * time.Hour).Truncate(time.Second)
	for _, p := range []string{"app/package.json", "app/index.js"} {
		require.NoError(t, os.Chtimes(filepath.Join(root, p), old, old))
	}
	got := runAll(t, root)
	require.True(t, old.Equal(got["app/node_modules"].LastUsed), "got %v", got["app/node_modules"].LastUsed)
}

// TestGitTrackedFoldersAreTierC uses the real git binary on a throwaway
// repository, because the meaning of check-ignore and ls-files output is the
// point of the test.
func TestGitTrackedFoldersAreTierC(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo := filepath.Join(root, "app")
	fixture(t, repo, map[string]string{
		".gitignore":              "node_modules/\ndist/\n.next\n",
		"package.json":            `{"scripts":{"build":"electron-builder"}}`,
		"node_modules/a/index.js": "dep",
		"dist/app.js":             "built",
		"dist/keep.txt":           "force added",
		".next/cache/x":           "next",
		"pyproject.toml":          "[project]",
		".venv/pyvenv.cfg":        "home = /usr/bin",
		".venv/.gitignore":        "*",
		".venv/lib/site.py":       "ignored by its own .gitignore",
		"build/icon.icns":         "icon committed to the repository",
		"__pycache__/m.pyc":       "not ignored and not committed",
	})
	gitCmd := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	gitCmd("init", "-q")
	gitCmd("add", ".gitignore", "package.json", "build/icon.icns")
	gitCmd("add", "--force", "dist/keep.txt")
	gitCmd("commit", "-q", "-m", "init")

	all := Scanners()
	env := scan.Env{
		Roots:    []string{root},
		Walker:   fsx.NewWalker(4),
		Exec:     execx.OS{},
		Projects: project.NewSource([]string{root}, Matchers(all), project.Options{Home: filepath.Dir(root)}),
	}
	ss := make([]scan.Scanner, 0, len(all))
	for _, s := range all {
		ss = append(ss, s)
	}
	res := scan.Run(context.Background(), ss, env, 4)
	tiers := map[string]finding.Tier{}
	warnings := map[string]string{}
	for _, f := range res.Findings {
		rel, err := filepath.Rel(repo, f.Path)
		require.NoError(t, err)
		tiers[rel] = f.Tier
		warnings[rel] = f.Warning
	}
	require.Equal(t, map[string]finding.Tier{
		"node_modules": finding.TierA,
		".next":        finding.TierA,
		".venv":        finding.TierA,
		"dist":         finding.TierC,
		"build":        finding.TierC,
		"__pycache__":  finding.TierC,
	}, tiers)
	require.Contains(t, warnings["dist"], "tracks files")
	require.Contains(t, warnings["build"], "tracks files")
	require.Contains(t, warnings["__pycache__"], "does not ignore")
}

func TestScannerMetadata(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Scanners() {
		require.False(t, seen[s.Name()], "duplicate scanner %s", s.Name())
		seen[s.Name()] = true
		require.Equal(t, finding.CategoryProject, s.Category())
		require.NotEmpty(t, s.Ecosystem())
		require.NotEmpty(t, s.Description())
	}
}

func TestScanWithoutProjectSource(t *testing.T) {
	_, err := Scanners()[0].Scan(context.Background(), scan.Env{})
	require.Error(t, err)
}
