package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
	"github.com/mandloideep/reclaim/internal/ui"
)

// app holds everything the commands touch outside the process, so tests can
// run the real command tree against fixtures.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	home     string
	goos     string
	host     string
	stateDir string
	getenv   func(string) string
	exec     execx.Runner
	docker   func() (dockerx.API, error)
	now      func() time.Time

	verbose bool
	in      *bufio.Reader
}

func defaultApp() (*app, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find home directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = filepath.Join(home, ".cache")
	}
	host, _ := os.Hostname()
	return &app{
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		home:     home,
		goos:     runtime.GOOS,
		host:     host,
		stateDir: filepath.Join(cacheDir, "reclaim"),
		getenv:   os.Getenv,
		exec:     execx.OS{},
		docker: func() (dockerx.API, error) {
			c, err := dockerx.New(os.Getenv, home)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		now: time.Now,
	}, nil
}

func (a *app) logger() *slog.Logger {
	if !a.verbose {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// sayf writes to standard output. Write errors to the terminal are not
// actionable, so they are ignored.
func (a *app) sayf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.stdout, format, args...)
}

// warnf writes a warning line to standard error.
func (a *app) warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.stderr, "reclaim: warning: "+format+"\n", args...)
}

func (a *app) printer() *ui.Printer {
	return ui.New(a.stdout, a.home, a.now())
}

// readLine reads one line from stdin, without the line ending.
func (a *app) readLine() (string, error) {
	if a.in == nil {
		a.in = bufio.NewReader(a.stdin)
	}
	line, err := a.in.ReadString('\n')
	switch {
	case err == nil:
	case errors.Is(err, io.EOF) && line != "":
	case errors.Is(err, io.EOF):
		return "", errors.New("no input")
	default:
		return "", fmt.Errorf("read input: %w", err)
	}
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line, nil
}

// lastReportPath is where scan and here save their latest report for select.
func (a *app) lastReportPath() string {
	return filepath.Join(a.stateDir, "last-report.json")
}

// defaultRoots are the usual project folders that exist, without duplicates
// that differ only in case on case insensitive filesystems.
func (a *app) defaultRoots() []string {
	var roots []string
	var infos []os.FileInfo
	for _, name := range []string{"Code", "code", "Developer", "Projects", "projects", "src", "work", "Downloads"} {
		p, err := resolveDir(filepath.Join(a.home, name))
		if err != nil {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(infos, func(o os.FileInfo) bool { return os.SameFile(o, info) }) {
			continue
		}
		infos = append(infos, info)
		roots = append(roots, p)
	}
	return roots
}

// resolveDir makes path absolute, resolves symbolic links and checks that it
// is a directory, so every finding path is canonical.
func resolveDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return resolved, nil
}

// scanRequest describes one run of the scanners.
type scanRequest struct {
	scanners []scan.Scanner
	roots    []string
	depth    int
	labels   []dockerx.Label
	walker   *fsx.Walker
	sizes    map[string]int64
}

// runScan runs the scanners and builds a report. Filesystem findings may live
// under the roots and, when any scanner outside the project category ran,
// under the home directory.
func (a *app) runScan(ctx context.Context, reg *scanners.Registry, req scanRequest) *finding.Report {
	diag := scan.NewDiagnostics()
	walker := req.walker
	if walker == nil {
		walker = fsx.NewWalker(0)
	}
	env := scan.Env{
		Roots:        req.roots,
		Home:         a.home,
		GOOS:         a.goos,
		Getenv:       a.getenv,
		Exec:         a.exec,
		DockerLabels: req.labels,
		Walker:       walker,
		Sizes:        req.sizes,
		Projects: project.NewSource(req.roots, reg.ProjectMatchers(), project.Options{
			Depth:      req.depth,
			Home:       a.home,
			CommitTime: project.GitCommitTime(a.exec),
		}),
		Log:  a.logger(),
		Diag: diag,
	}

	selected := req.scanners
	reportRoots := slices.Clone(req.roots)
	needsHome := false
	for _, s := range selected {
		switch s.Category() {
		case finding.CategoryDocker:
			cli, err := a.docker()
			if err != nil {
				diag.For(s.Name()).Warn("", "Docker is not available: "+err.Error())
				selected = slices.DeleteFunc(slices.Clone(selected), func(x scan.Scanner) bool { return x.Category() == finding.CategoryDocker })
				continue
			}
			env.Docker = cli
		case finding.CategoryProject:
		default:
			needsHome = true
		}
	}
	if env.Docker != nil {
		defer func() { _ = env.Docker.Close() }()
	}
	if needsHome && a.home != "" {
		reportRoots = append(reportRoots, a.home)
	}

	res := scan.Run(ctx, selected, env, 8)
	warnings := res.Warnings
	if slices.ContainsFunc(selected, func(s scan.Scanner) bool { return s.Category() == finding.CategoryProject }) {
		if idx, err := env.Projects.Index(ctx); err == nil {
			for i, u := range idx.Unreadable {
				if i == 50 {
					warnings = append(warnings, finding.Warning{Scanner: "projects", Message: fmt.Sprintf("%d more unreadable folders not shown", len(idx.Unreadable)-50)})
					break
				}
				warnings = append(warnings, finding.Warning{Scanner: "projects", Path: u.Path, Message: "not scanned: " + u.Err.Error()})
			}
		}
	}
	return &finding.Report{
		Version:  finding.ReportVersion,
		Created:  a.now().UTC(),
		Host:     a.host,
		OS:       a.goos,
		Roots:    project.NormalizeRoots(reportRoots),
		Findings: res.Findings,
		Warnings: warnings,
		Notes:    res.Notes,
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// saveReport writes a report to path.
func saveReport(path string, r *finding.Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save report %s: %w", path, err)
	}
	return plan.WriteFileAtomic(path, append(data, '\n'))
}

// saveLastReport keeps a copy of the report for select. Failing to save it
// does not fail the scan.
func (a *app) saveLastReport(r *finding.Report) {
	if a.stateDir == "" {
		return
	}
	if err := saveReport(a.lastReportPath(), r); err != nil {
		a.warnf("%v", err)
	}
}

func loadReport(path string) (*finding.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	var r finding.Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse report %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("report %s: %w", path, err)
	}
	return &r, nil
}
