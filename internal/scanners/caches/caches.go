// Package caches holds the package manager cache scanners.
//
// Each scanner reports one finding per cache directory. It asks the tool where
// its cache lives when the tool is installed, for example "npm config get
// cache", and falls back to the environment variables and default locations
// the tool documents. When the location came from the tool and the tool has a
// clean command of its own, the finding uses that command as a RunCommand
// action; otherwise the action is RemovePath.
package caches

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// location is one candidate cache directory.
type location struct {
	path    string
	tier    finding.Tier
	name    string
	restore string
	warning string
	// command is the tool's own clean command. Nil means RemovePath.
	command []string
}

// spec describes one cache scanner.
type spec struct {
	name        string
	ecosystem   string
	description string
	locate      func(ctx context.Context, env *scan.Env) []location
}

// Scanner reports the caches of one tool.
type Scanner struct {
	s spec
}

var _ scan.Scanner = (*Scanner)(nil)

// Name implements scan.Scanner.
func (s *Scanner) Name() string { return s.s.name }

// Category implements scan.Scanner.
func (s *Scanner) Category() finding.Category { return finding.CategoryPackageCache }

// Ecosystem implements scan.Scanner.
func (s *Scanner) Ecosystem() string { return s.s.ecosystem }

// Description implements scan.Scanner.
func (s *Scanner) Description() string { return s.s.description }

// Scan implements scan.Scanner.
func (s *Scanner) Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error) {
	var out []finding.Finding
	seen := map[string]bool{}
	for _, loc := range s.s.locate(ctx, &env) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if loc.path == "" || !filepath.IsAbs(loc.path) {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Clean(loc.path))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				env.Diag.Warn(loc.path, "could not resolve: "+err.Error())
			}
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		size, err := env.Size(ctx, path)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			continue
		}
		f := finding.Finding{
			Tier:     loc.tier,
			Path:     path,
			Kind:     finding.KindDir,
			Target:   path,
			Name:     loc.name,
			Size:     size,
			LastUsed: info.ModTime(),
			Restore:  loc.restore,
			Action:   finding.ActionRemovePath,
			Warning:  loc.warning,
		}
		if len(loc.command) > 0 {
			f.Action = finding.ActionRunCommand
			f.Command = loc.command
		}
		out = append(out, f)
	}
	return out, nil
}

// Scanners returns every package cache scanner.
func Scanners() []*Scanner {
	specs := specs()
	out := make([]*Scanner, len(specs))
	for i, s := range specs {
		out[i] = &Scanner{s: s}
	}
	return out
}

// query asks a tool for a path. It returns the first absolute path printed on
// standard output, or an empty string when the tool is missing or fails.
func query(ctx context.Context, env *scan.Env, tool string, args ...string) string {
	if env.Exec == nil {
		return ""
	}
	out, err := env.Exec.Output(ctx, tool, args...)
	if err != nil {
		if !errors.Is(err, execx.ErrNotInstalled) {
			env.Logger().Debug("cache location query failed", "tool", tool, "err", err)
		}
		return ""
	}
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if filepath.IsAbs(line) {
			return filepath.Clean(line)
		}
	}
	return ""
}

// absVar returns an environment variable when it holds an absolute path.
func absVar(env *scan.Env, key string) string {
	if v := env.Var(key); filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return ""
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

// withCommand returns command when the location was reported by the tool
// itself, so the clean command acts on the same directory that was measured.
func withCommand(fromTool bool, command ...string) []string {
	if !fromTool {
		return nil
	}
	return command
}
