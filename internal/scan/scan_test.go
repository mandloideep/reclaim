package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
)

type fakeScanner struct {
	name     string
	category finding.Category
	eco      string
	scan     func(ctx context.Context, env Env) ([]finding.Finding, error)
}

func (f *fakeScanner) Name() string               { return f.name }
func (f *fakeScanner) Category() finding.Category { return f.category }
func (f *fakeScanner) Ecosystem() string          { return f.eco }
func (f *fakeScanner) Description() string        { return "fake " + f.name }
func (f *fakeScanner) Scan(ctx context.Context, env Env) ([]finding.Finding, error) {
	return f.scan(ctx, env)
}

func returns(fs ...finding.Finding) func(context.Context, Env) ([]finding.Finding, error) {
	return func(context.Context, Env) ([]finding.Finding, error) { return fs, nil }
}

func TestRunCollectsAndNormalizes(t *testing.T) {
	scanners := []Scanner{
		&fakeScanner{name: "a", category: finding.CategoryProject, scan: returns(
			finding.Finding{Path: "/x/a", Tier: finding.TierA, Action: finding.ActionRemovePath, Size: 10},
			finding.Finding{Path: "/x/c", Tier: finding.TierC, Action: finding.ActionRemovePath, Size: 30},
		)},
		&fakeScanner{name: "b", category: finding.CategoryDocker, scan: returns(
			finding.Finding{Target: "img", Tier: finding.TierB, Action: finding.ActionDockerRemoveImage, Size: 99},
			finding.Finding{Target: "bad", Tier: "Q", Action: finding.ActionDockerRemoveImage},
		)},
	}
	res := Run(context.Background(), scanners, Env{}, 2)
	require.Len(t, res.Findings, 3)

	byTarget := map[string]finding.Finding{}
	for _, f := range res.Findings {
		byTarget[f.Target] = f
	}
	a := byTarget["/x/a"]
	require.Equal(t, "a", a.Scanner)
	require.Equal(t, finding.CategoryProject, a.Category)
	require.Equal(t, finding.MakeID("a", "/x/a"), a.ID)
	require.NotEmpty(t, byTarget["/x/c"].Warning, "tier C always carries a warning")
	require.Equal(t, finding.CategoryDocker, byTarget["img"].Category)

	// Sorted by category then size.
	require.Equal(t, "/x/c", res.Findings[0].Target)
	require.Equal(t, "img", res.Findings[2].Target)

	require.Len(t, res.Warnings, 1)
	require.Equal(t, "b", res.Warnings[0].Scanner)
	require.Contains(t, res.Warnings[0].Message, "invalid finding")
}

type catchAllScanner struct{ fakeScanner }

func (catchAllScanner) CatchAll() bool { return true }

func TestRunDropsCatchAllOverlaps(t *testing.T) {
	rm := func(path string) finding.Finding {
		return finding.Finding{Path: path, Tier: finding.TierA, Action: finding.ActionRemovePath, Size: 1}
	}
	scanners := []Scanner{
		&fakeScanner{name: "npm-cache", category: finding.CategoryPackageCache, scan: returns(rm("/h/.cache/npm/_cacache"))},
		&fakeScanner{name: "playwright", category: finding.CategoryAppCache, scan: returns(rm("/h/.cache/ms-playwright"))},
		&catchAllScanner{fakeScanner{name: "user-caches", category: finding.CategoryAppCache, scan: returns(
			rm("/h/.cache/npm"),           // contains a specific finding
			rm("/h/.cache/ms-playwright"), // the same path
			rm("/h/.cache/thumbnails"),    // only the catch-all knows it
			rm("/h/.cache/npm-other"),     // shares a prefix, not a path
		)}},
	}
	res := Run(context.Background(), scanners, Env{}, 3)
	got := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		got = append(got, f.Scanner+" "+f.Path)
	}
	require.ElementsMatch(t, []string{
		"npm-cache /h/.cache/npm/_cacache",
		"playwright /h/.cache/ms-playwright",
		"user-caches /h/.cache/thumbnails",
		"user-caches /h/.cache/npm-other",
	}, got)
}

func TestRunTurnsFailuresIntoWarnings(t *testing.T) {
	scanners := []Scanner{
		&fakeScanner{name: "broken", scan: func(context.Context, Env) ([]finding.Finding, error) {
			return nil, errors.New("docker is not running")
		}},
		&fakeScanner{name: "panics", scan: func(context.Context, Env) ([]finding.Finding, error) {
			panic("boom")
		}},
		&fakeScanner{name: "ok", category: finding.CategoryProject, scan: returns(
			finding.Finding{Path: "/x/y", Tier: finding.TierA, Action: finding.ActionRemovePath},
		)},
	}
	res := Run(context.Background(), scanners, Env{}, 1)
	require.Len(t, res.Findings, 1)
	msgs := map[string]string{}
	for _, w := range res.Warnings {
		msgs[w.Scanner] = w.Message
	}
	require.Contains(t, msgs["broken"], "docker is not running")
	require.Contains(t, msgs["panics"], "boom")
	require.NotNil(t, res.Findings, "findings are never null in JSON")
}

func TestRunBoundsConcurrency(t *testing.T) {
	var running, peak atomic.Int32
	scanners := make([]Scanner, 0, 12)
	for i := range 12 {
		scanners = append(scanners, &fakeScanner{name: string(rune('a' + i)), scan: func(context.Context, Env) ([]finding.Finding, error) {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			return nil, nil
		}})
	}
	Run(context.Background(), scanners, Env{}, 3)
	require.LessOrEqual(t, peak.Load(), int32(3))
	require.Positive(t, peak.Load())
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Run(ctx, []Scanner{&fakeScanner{name: "x", scan: returns()}}, Env{}, 1)
	require.Empty(t, res.Findings)
	require.NotEmpty(t, res.Warnings)
}

func TestScannersReportWarningsAndNotes(t *testing.T) {
	s := &fakeScanner{name: "noisy", scan: func(_ context.Context, env Env) ([]finding.Finding, error) {
		for range maxWarningsPerScanner + 5 {
			env.Diag.Warn("/p", "unreadable")
		}
		env.Diag.Note("hello")
		env.Diag.Note("hello")
		return nil, nil
	}}
	res := Run(context.Background(), []Scanner{s}, Env{}, 1)
	require.Len(t, res.Warnings, maxWarningsPerScanner+1)
	require.Contains(t, res.Warnings[maxWarningsPerScanner].Message, "5 more warnings")
	require.Equal(t, []string{"hello"}, res.Notes)
}

func TestSelect(t *testing.T) {
	all := []Scanner{
		&fakeScanner{name: "node_modules", category: finding.CategoryProject, eco: "node"},
		&fakeScanner{name: "npm-cache", category: finding.CategoryPackageCache, eco: "node"},
		&fakeScanner{name: "docker", category: finding.CategoryDocker, eco: "docker"},
		&fakeScanner{name: "pycache", category: finding.CategoryProject, eco: "python"},
	}
	names := func(ss []Scanner) []string {
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			out = append(out, s.Name())
		}
		return out
	}
	tests := []struct {
		name      string
		selectors []string
		want      []string
		wantErr   bool
	}{
		{name: "none selects all", want: []string{"node_modules", "npm-cache", "docker", "pycache"}},
		{name: "category and ecosystem", selectors: []string{"docker", "node"}, want: []string{"docker", "node_modules", "npm-cache"}},
		{name: "scanner name", selectors: []string{"pycache"}, want: []string{"pycache"}},
		{name: "category", selectors: []string{"project"}, want: []string{"node_modules", "pycache"}},
		{name: "no duplicates", selectors: []string{"node", "node_modules"}, want: []string{"node_modules", "npm-cache"}},
		{name: "unknown", selectors: []string{"nod"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Select(all, tt.selectors)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, names(got))
		})
	}
	require.Equal(t, []string{"node_modules", "pycache"}, names(OnlyCategory(all, finding.CategoryProject)))
}

func TestFilter(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	fs := []finding.Finding{
		{ID: "small", Tier: finding.TierA, Size: 5, Category: finding.CategoryProject, Path: "/c/app/x"},
		{ID: "fresh", Tier: finding.TierA, Size: 100, Category: finding.CategoryProject, LastUsed: now.Add(-24 * time.Hour)},
		{ID: "stale", Tier: finding.TierA, Size: 100, Category: finding.CategoryProject, LastUsed: now.Add(-200 * 24 * time.Hour), Path: "/c/archive/old/node_modules"},
		{ID: "cache", Tier: finding.TierB, Size: 100, Category: finding.CategoryPackageCache, LastUsed: now},
		{ID: "data", Tier: finding.TierC, Size: 100, Category: finding.CategoryDocker},
	}
	ids := func(fs []finding.Finding) []string {
		out := make([]string, 0, len(fs))
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	tests := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{name: "none", filter: Filter{}, want: []string{"small", "fresh", "stale", "cache", "data"}},
		{name: "min size", filter: Filter{MinSize: 10}, want: []string{"fresh", "stale", "cache", "data"}},
		{name: "tiers", filter: Filter{Tiers: []finding.Tier{finding.TierB, finding.TierC}}, want: []string{"cache", "data"}},
		{name: "exclude", filter: Filter{Exclude: []string{"/c/archive", "/c/ap"}}, want: []string{"small", "fresh", "cache", "data"}},
		{name: "exclude a finding itself", filter: Filter{Exclude: []string{"/c/app/x"}}, want: []string{"fresh", "stale", "cache", "data"}},
		{name: "exclude inside a finding", filter: Filter{Exclude: []string{"/c/archive/old/node_modules/keep"}}, want: []string{"small", "fresh", "cache", "data"}},
		{name: "stale only affects projects", filter: Filter{Stale: 90 * 24 * time.Hour, Now: now, MinSize: 10}, want: []string{"stale", "cache", "data"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ids(tt.filter.Apply(fs)))
		})
	}
}

func TestEnvHelpers(t *testing.T) {
	env := Env{Home: "/home/u", GOOS: "linux", Getenv: func(k string) string {
		if k == "XDG_CACHE_HOME" {
			return "/xdg"
		}
		return ""
	}}
	require.Equal(t, "/xdg", env.UserCacheDir())
	env.Getenv = nil
	require.Equal(t, "", env.Var("HOME"))
	require.Equal(t, "/home/u/.cache", env.UserCacheDir())
	env.GOOS = "darwin"
	require.Equal(t, "/home/u/Library/Caches", env.UserCacheDir())
	require.NotNil(t, env.Logger())
}

func TestEnvSizeReportsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "f"), make([]byte, 10), 0o644))
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	diag := NewDiagnostics()
	env := Env{Walker: fsx.NewWalker(2), Diag: diag.For("x")}
	size, err := env.Size(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, int64(10), size)
	ws := diag.Warnings()
	require.Len(t, ws, 1)
	require.Equal(t, locked, ws[0].Path)
	require.Equal(t, "x", ws[0].Scanner)
}
