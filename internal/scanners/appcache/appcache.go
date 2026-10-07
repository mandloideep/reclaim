// Package appcache holds the application cache scanners: the entries of the
// per user cache folder, Electron updater leftovers, downloaded browsers for
// Playwright and Puppeteer, Xcode build data and simulators, Ollama models and
// the leftovers of coding agents.
//
// Every scanner is read only. Scanners that only make sense on macOS return
// nothing elsewhere. Findings inside the per user cache folder are always
// direct children of it, never the folder itself.
package appcache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// spec describes one app cache scanner.
type spec struct {
	name        string
	ecosystem   string
	description string
	// catchAll marks the scanner that reports every entry of the cache folder,
	// whose findings give way to those of more specific scanners.
	catchAll bool
	scan     func(ctx context.Context, env *scan.Env) ([]finding.Finding, error)
}

// Scanner is one app cache scanner.
type Scanner struct {
	s spec
}

var (
	_ scan.Scanner  = (*Scanner)(nil)
	_ scan.CatchAll = (*Scanner)(nil)
)

// Name implements scan.Scanner.
func (s *Scanner) Name() string { return s.s.name }

// Category implements scan.Scanner.
func (s *Scanner) Category() finding.Category { return finding.CategoryAppCache }

// Ecosystem implements scan.Scanner.
func (s *Scanner) Ecosystem() string { return s.s.ecosystem }

// Description implements scan.Scanner.
func (s *Scanner) Description() string { return s.s.description }

// CatchAll implements scan.CatchAll.
func (s *Scanner) CatchAll() bool { return s.s.catchAll }

// Scan implements scan.Scanner.
func (s *Scanner) Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error) {
	return s.s.scan(ctx, &env)
}

// Scanners returns every app cache scanner.
func Scanners() []*Scanner {
	specs := []spec{
		{name: "user-caches", ecosystem: "apps", catchAll: true, scan: scanUserCaches,
			description: "entries of ~/Library/Caches or ~/.cache that apps rebuild, except apps known to keep data there"},
		{name: "electron-updaters", ecosystem: "electron", scan: scanElectronUpdaters,
			description: "Electron updater leftovers (*.ShipIt and *-updater folders in the cache folder)"},
		{name: "playwright", ecosystem: "playwright", scan: scanPlaywright,
			description: "browsers downloaded by Playwright"},
		{name: "puppeteer", ecosystem: "puppeteer", scan: scanPuppeteer,
			description: "browsers downloaded by Puppeteer"},
		{name: "xcode-derived-data", ecosystem: "xcode", scan: scanDerivedData,
			description: "Xcode DerivedData build folders, one per project, in the default location and in a custom one set in Xcode"},
		{name: "simulator-devices", ecosystem: "xcode", scan: scanSimulatorDevices,
			description: "iOS and other simulators whose runtime is no longer installed"},
		{name: "simulator-runtimes", ecosystem: "xcode", scan: scanSimulatorRuntimes,
			description: "installed simulator runtimes, removed with xcrun simctl runtime delete (needs sudo)"},
		{name: "ollama", ecosystem: "ollama", scan: scanOllama,
			description: "Ollama models, removed with ollama rm"},
		{name: "codex-worktrees", ecosystem: "codex", scan: scanCodexWorktrees,
			description: "git worktrees Codex made for tasks"},
		{name: "codex-sessions", ecosystem: "codex", scan: scanCodexSessions,
			description: "Codex conversation logs older than 30 days"},
		{name: "claude-vm", ecosystem: "claude", scan: scanClaudeVM,
			description: "Claude desktop VM bundles and old Claude Code VM versions"},
	}
	out := make([]*Scanner, len(specs))
	for i, s := range specs {
		out[i] = &Scanner{s: s}
	}
	return out
}

// entry describes a folder to report with RemovePath.
type entry struct {
	path    string
	tier    finding.Tier
	name    string
	restore string
	warning string
}

// dirFinding measures a directory and returns it as a RemovePath finding. It
// reports false for anything that is not a real directory, such as a symbolic
// link, and for empty directories.
func dirFinding(ctx context.Context, env *scan.Env, e entry) (finding.Finding, bool, error) {
	info, ok := realDir(env, e.path)
	if !ok {
		return finding.Finding{}, false, nil
	}
	size, err := env.Size(ctx, e.path)
	if err != nil {
		if ctx.Err() != nil {
			return finding.Finding{}, false, ctx.Err()
		}
		env.Diag.Warn(e.path, "could not measure: "+err.Error())
		return finding.Finding{}, false, nil
	}
	if size == 0 {
		return finding.Finding{}, false, nil
	}
	return finding.Finding{
		Tier:     e.tier,
		Path:     e.path,
		Kind:     finding.KindDir,
		Target:   e.path,
		Name:     e.name,
		Size:     size,
		LastUsed: info.ModTime(),
		Restore:  e.restore,
		Action:   finding.ActionRemovePath,
		Warning:  e.warning,
	}, true, nil
}

// realDir reports whether path is a directory and not a symbolic link. A
// path that cannot be inspected for another reason than not existing adds a
// warning.
func realDir(env *scan.Env, path string) (fs.FileInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			env.Diag.Warn(path, "could not inspect: "+err.Error())
		}
		return nil, false
	}
	return info, info.IsDir()
}

// collect turns entries into findings, skipping the ones that are not there.
func collect(ctx context.Context, env *scan.Env, entries []entry) ([]finding.Finding, error) {
	var out []finding.Finding
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, ok, err := dirFinding(ctx, env, e)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// subdirs returns the real directories directly inside dir, sorted by name,
// skipping hidden ones. A missing dir has none.
func subdirs(env *scan.Env, dir string) []fs.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			env.Diag.Warn(dir, "could not read: "+err.Error())
		}
		return nil
	}
	return slices.DeleteFunc(entries, func(e fs.DirEntry) bool {
		return !e.IsDir() || strings.HasPrefix(e.Name(), ".")
	})
}

// absVar returns an environment variable when it holds an absolute path.
func absVar(env *scan.Env, key string) string {
	if v := env.Var(key); filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return ""
}

// resolve returns path with symbolic links resolved, or path itself when it
// does not exist, so findings carry canonical paths.
func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}
