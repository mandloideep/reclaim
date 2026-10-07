// Package scan defines the Scanner interface, the environment scanners run in
// and the concurrent runner that collects their findings.
package scan

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/project"
)

// Scanner finds reclaimable space of one kind.
type Scanner interface {
	// Name is a short stable identifier such as "node_modules" or "docker".
	Name() string
	// Category groups findings in reports.
	Category() finding.Category
	// Ecosystem is the tool family the scanner belongs to, such as "node",
	// "python" or "docker". The --category flag accepts it as a selector.
	Ecosystem() string
	// Description says in one line what the scanner looks for.
	Description() string
	// Scan returns findings. It must not modify anything and must respect ctx
	// cancellation.
	Scan(ctx context.Context, env Env) ([]finding.Finding, error)
}

// CatchAll is implemented by scanners that report every entry of a shared
// folder, such as the one that reports each entry of ~/Library/Caches. The
// runner drops their findings that overlap a finding of another scanner, so
// the same bytes are never offered twice and the specific scanner, which
// knows the right tier and restore hint, wins.
type CatchAll interface {
	// CatchAll reports whether the scanner is a catch-all.
	CatchAll() bool
}

func isCatchAll(s Scanner) bool {
	c, ok := s.(CatchAll)
	return ok && c.CatchAll()
}

// Env carries everything a scanner needs, so tests can substitute fakes.
type Env struct {
	// Roots are the directories project scanners walk.
	Roots []string
	// Home is the user's home directory.
	Home string
	// GOOS is the operating system, as in runtime.GOOS.
	GOOS string
	// Now is when the scan started. Scanners that judge age, such as the
	// Downloads attention list, measure it from here.
	Now time.Time
	// Applications are the folders that hold installed applications, such as
	// /Applications and ~/Applications on macOS. The Downloads scanner looks
	// there for the apps that installers belong to.
	Applications []string
	// OllamaSystemModels is the models folder of a system wide Ollama
	// service, plan.OllamaSystemModels on Linux and empty where there is
	// none.
	OllamaSystemModels string
	// Getenv reads an environment variable. Nil reads nothing.
	Getenv func(string) string
	// Exec runs external tools, such as "npm config get cache".
	Exec execx.Runner
	// Docker is the Docker client, nil when none could be created.
	Docker dockerx.API
	// DockerLabels limits Docker findings to objects with these labels.
	DockerLabels []dockerx.Label
	// Walker measures directory sizes.
	Walker *fsx.Walker
	// Sizes holds directory sizes measured earlier in the same run, such as by
	// the folder walk of "here". Size returns them instead of walking again.
	Sizes map[string]int64
	// Projects discovers projects and artifact folders under Roots once per scan.
	Projects *project.Source
	// Log receives debug output. Nil discards it.
	Log *slog.Logger
	// Diag receives warnings and notes for the report. The runner binds it to
	// the scanner being run.
	Diag Diagnostics
}

// Var reads an environment variable through Getenv.
func (e *Env) Var(key string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(key)
}

// UserCacheDir is the per user cache directory: ~/Library/Caches on macOS and
// $XDG_CACHE_HOME or ~/.cache elsewhere.
func (e *Env) UserCacheDir() string {
	if e.GOOS == "darwin" {
		return filepath.Join(e.Home, "Library", "Caches")
	}
	if x := e.Var("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		return x
	}
	return filepath.Join(e.Home, ".cache")
}

// Logger returns Log or a logger that discards everything.
func (e *Env) Logger() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Log
}

// Size measures path with Walker and reports unreadable entries and skipped
// mount points as warnings.
func (e *Env) Size(ctx context.Context, path string) (int64, error) {
	if n, ok := e.Sizes[path]; ok {
		return n, nil
	}
	w := e.Walker
	if w == nil {
		w = fsx.NewWalker(0)
	}
	res, err := w.Size(ctx, path)
	if err != nil {
		return 0, err
	}
	for _, u := range res.Unreadable {
		e.Diag.Warn(u.Path, "not counted: "+errText(u.Err))
	}
	for _, m := range res.Mounts {
		e.Diag.Warn(m, "not counted: on another filesystem")
	}
	return res.Size, nil
}

func errText(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

// maxWarningsPerScanner caps the warnings one scanner can add to a report, so
// a tree full of unreadable folders does not bury the findings.
const maxWarningsPerScanner = 50

type collector struct {
	mu       sync.Mutex
	warnings []finding.Warning
	notes    []string
	counts   map[string]int
}

// Diagnostics collects warnings and notes for the report. The zero value
// drops everything.
type Diagnostics struct {
	scanner string
	c       *collector
}

// NewDiagnostics returns an empty collector.
func NewDiagnostics() Diagnostics {
	return Diagnostics{c: &collector{counts: map[string]int{}}}
}

// For returns a Diagnostics that attributes entries to the named scanner and
// shares this collector.
func (d Diagnostics) For(scanner string) Diagnostics {
	return Diagnostics{scanner: scanner, c: d.c}
}

// Warn records a warning about path, which may be empty.
func (d Diagnostics) Warn(path, msg string) {
	if d.c == nil {
		return
	}
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	d.c.counts[d.scanner]++
	if d.c.counts[d.scanner] > maxWarningsPerScanner {
		return
	}
	d.c.warnings = append(d.c.warnings, finding.Warning{Scanner: d.scanner, Path: path, Message: msg})
}

// Note records an informational message once.
func (d Diagnostics) Note(msg string) {
	if d.c == nil {
		return
	}
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	if !slices.Contains(d.c.notes, msg) {
		d.c.notes = append(d.c.notes, msg)
	}
}

// Warnings returns the warnings recorded so far, with a summary line for
// every scanner that went over the cap.
func (d Diagnostics) Warnings() []finding.Warning {
	if d.c == nil {
		return nil
	}
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	out := slices.Clone(d.c.warnings)
	scanners := make([]string, 0, len(d.c.counts))
	for s := range d.c.counts {
		scanners = append(scanners, s)
	}
	slices.Sort(scanners)
	for _, s := range scanners {
		if n := d.c.counts[s]; n > maxWarningsPerScanner {
			out = append(out, finding.Warning{Scanner: s, Message: fmt.Sprintf("%d more warnings not shown", n-maxWarningsPerScanner)})
		}
	}
	return out
}

// Notes returns the notes recorded so far.
func (d Diagnostics) Notes() []string {
	if d.c == nil {
		return nil
	}
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	return slices.Clone(d.c.notes)
}

// Result is the combined output of a run.
type Result struct {
	// Findings from every scanner, sorted with finding.Sort.
	Findings []finding.Finding
	// Warnings from scanners and from the runner itself.
	Warnings []finding.Warning
	// Notes from scanners.
	Notes []string
}

// Run runs the scanners concurrently, at most concurrency at a time. A
// scanner that fails or panics adds a warning and does not stop the others.
// Findings are normalized: the runner sets their scanner, category and id, and
// drops findings with an invalid tier or action.
func Run(ctx context.Context, scanners []Scanner, env Env, concurrency int) Result {
	if concurrency < 1 {
		concurrency = 4
	}
	diag := env.Diag
	if diag.c == nil {
		diag = NewDiagnostics()
	}
	var (
		mu       sync.Mutex
		findings []finding.Finding
		catchAll []finding.Finding
		wg       sync.WaitGroup
		sem      = make(chan struct{}, concurrency)
	)
	for _, s := range scanners {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			senv := env
			senv.Diag = diag.For(s.Name())
			got := runOne(ctx, s, senv)
			mu.Lock()
			if isCatchAll(s) {
				catchAll = append(catchAll, got...)
			} else {
				findings = append(findings, got...)
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		diag.Warn("", "scan interrupted: "+err.Error())
	}
	findings = append(findings, withoutOverlaps(catchAll, findings)...)
	finding.Sort(findings)
	if findings == nil {
		findings = []finding.Finding{}
	}
	return Result{Findings: findings, Warnings: diag.Warnings(), Notes: diag.Notes()}
}

// withoutOverlaps returns the catch-all findings whose path neither contains
// nor lies inside the path of a specific finding.
func withoutOverlaps(catchAll, specific []finding.Finding) []finding.Finding {
	var paths []string
	for i := range specific {
		if specific[i].Path != "" {
			paths = append(paths, specific[i].Path)
		}
	}
	out := make([]finding.Finding, 0, len(catchAll))
	for _, f := range catchAll {
		overlaps := f.Path != "" && slices.ContainsFunc(paths, func(p string) bool {
			return isWithin(p, f.Path) || isWithin(f.Path, p)
		})
		if !overlaps {
			out = append(out, f)
		}
	}
	return out
}

// isWithin reports whether path is parent or lies below it.
func isWithin(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}

func runOne(ctx context.Context, s Scanner, env Env) (out []finding.Finding) {
	defer func() {
		if r := recover(); r != nil {
			env.Logger().Error("scanner panicked", "scanner", s.Name(), "panic", r, "stack", string(debug.Stack()))
			env.Diag.Warn("", fmt.Sprintf("scanner crashed and was skipped: %v", r))
			out = nil
		}
	}()
	got, err := s.Scan(ctx, env)
	if err != nil {
		env.Diag.Warn("", "scanner failed: "+err.Error())
	}
	for _, f := range got {
		f.Scanner = s.Name()
		f.Category = s.Category()
		if f.Target == "" {
			f.Target = f.Path
		}
		if f.Target == "" || !f.Tier.Valid() || !f.Action.Valid() {
			env.Diag.Warn(f.Path, "scanner produced an invalid finding, dropped")
			continue
		}
		if f.Tier == finding.TierC && f.Warning == "" {
			f.Warning = "this may be data that cannot be restored"
		}
		f.ID = finding.MakeID(f.Scanner, f.Target)
		out = append(out, f)
	}
	return out
}
