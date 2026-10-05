package downloads

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func put(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	// Truncate makes a sparse file: a large apparent size on no disk space.
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

// zipFile writes a real zip archive holding the named entries.
func zipFile(t *testing.T, path string, names ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for _, n := range names {
		e, err := w.Create(n)
		require.NoError(t, err)
		_, err = e.Write([]byte("content"))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	ts := now.Add(-d)
	require.NoError(t, os.Chtimes(path, ts, ts))
}

func TestInstallerNames(t *testing.T) {
	apps := []installedApp{}
	for _, name := range []string{"Zen.app", "DaVinci Resolve.app", "FileZilla.app", "Google Chrome.app", "1Password.app", "Go.app", "Notion.app", "Notion Calendar.app", "Signal.app"} {
		joined := strings.Join(words(name[:len(name)-4]), "")
		if len(joined) >= 3 {
			apps = append(apps, installedApp{name: name, joined: joined})
		}
	}
	tests := []struct {
		file string
		want string
	}{
		{file: "zen.macos-universal.dmg", want: "Zen.app"},
		{file: "Zen.dmg", want: "Zen.app"},
		{file: "FileZilla_3.69.6_macos-arm64.app.tar.bz2", want: "FileZilla.app"},
		{file: "Signal backup.dmg"},
		{file: "Notion Export.pkg"},
		{file: "Google Chrome profile.iso"},
		{file: "googlechrome.dmg", want: "Google Chrome.app"},
		{file: "1Password-8.10.pkg", want: "1Password.app"},
		{file: "Notion-Calendar-1.2.dmg", want: "Notion Calendar.app"},
		{file: "Notion-4.0.dmg", want: "Notion.app"},
		{file: "Zenith-2.dmg"},
		{file: "go1.25.darwin-arm64.pkg"},
		{file: "Flow-v1.4.268.dmg"},
		{file: "notes.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			app, ok := installerApp(t.TempDir(), tt.file, apps)
			if tt.want == "" {
				require.False(t, ok, "matched %s", app.name)
				return
			}
			require.True(t, ok)
			require.Equal(t, tt.want, app.name)
		})
	}
}

func TestStems(t *testing.T) {
	require.Equal(t, "project-1.2", archiveStem("project-1.2.tar.gz"))
	require.Equal(t, "FileZilla.app", archiveStem("FileZilla.app.tar.bz2"))
	require.Equal(t, "data", archiveStem("data.ZIP"))
	require.Empty(t, archiveStem("movie.mov"))
	require.Empty(t, archiveStem(".zip"))
	require.Equal(t, "Rectangle", installerStem("Rectangle.app.zip"))
	require.Equal(t, "Foo", installerStem("Foo.app.tar.xz"))
	require.Empty(t, installerStem("Foo.tar.gz"))
}

// fixture builds a home with Downloads and Applications folders.
func fixture(t *testing.T) (home, dl string, env scan.Env) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home = filepath.Join(tmp, "Users", "me")
	dl = filepath.Join(home, "Downloads")
	apps := filepath.Join(tmp, "Applications")
	for _, app := range []string{"Zen.app", "Slack.app", "DaVinci Resolve/DaVinci Resolve.app"} {
		require.NoError(t, os.MkdirAll(filepath.Join(apps, app, "Contents"), 0o755))
	}
	put(t, filepath.Join(dl, "zen.macos-universal.dmg"), 200_000_000)
	put(t, filepath.Join(dl, "Slack-4.41.105-macOS.dmg"), 150_000_000)
	zipFile(t, filepath.Join(dl, "DaVinci_Resolve_21.1_Mac.zip"), "DaVinci_Resolve_21.1_Mac.dmg")
	// Zips named after an app that hold no app, disk image or package may be
	// the only copy of an export, so they are never installers.
	zipFile(t, filepath.Join(dl, "Zen-2.0-mac.zip"), "notes/todo.md")
	zipFile(t, filepath.Join(dl, "Slack export Jan 2024.zip"), "general/2024-01-02.json")
	put(t, filepath.Join(dl, "Zen backup.dmg"), 20_000_000)
	put(t, filepath.Join(dl, "Unknown-1.0.dmg"), 300_000_000)
	put(t, filepath.Join(dl, "project-1.2.tar.gz"), 30_000_000)
	put(t, filepath.Join(dl, "project-1.2", "README"), 10)
	put(t, filepath.Join(dl, "Slack-old.zip"), 100_000_000)
	put(t, filepath.Join(dl, "Slack-old", "Slack.app", "x"), 10)
	put(t, filepath.Join(dl, "talk.mov"), 3_000_000_000)
	put(t, filepath.Join(dl, "recent.mov"), 3_000_000_000)
	put(t, filepath.Join(dl, "small-old.pdf"), 1_000_000)
	put(t, filepath.Join(dl, "nested", "deep-old.mov"), 3_000_000_000)
	put(t, filepath.Join(dl, ".hidden-old.iso"), 3_000_000_000)
	for _, f := range []string{"talk.mov", "small-old.pdf", "Unknown-1.0.dmg", "nested/deep-old.mov", ".hidden-old.iso"} {
		age(t, filepath.Join(dl, f), 200*24*time.Hour)
	}
	age(t, filepath.Join(dl, "recent.mov"), 10*24*time.Hour)
	age(t, filepath.Join(dl, "zen.macos-universal.dmg"), 300*24*time.Hour)
	env = scan.Env{Home: home, GOOS: "darwin", Now: now, Applications: []string{apps, filepath.Join(home, "Applications")}, Walker: fsx.NewWalker(1)}
	return home, dl, env
}

func run(t *testing.T, env scan.Env) map[string]map[string]finding.Finding {
	t.Helper()
	ss := make([]scan.Scanner, 0, 3)
	for _, s := range Scanners() {
		ss = append(ss, s)
	}
	res := scan.Run(context.Background(), ss, env, 3)
	require.Empty(t, res.Warnings)
	out := map[string]map[string]finding.Finding{}
	for _, f := range res.Findings {
		if out[f.Scanner] == nil {
			out[f.Scanner] = map[string]finding.Finding{}
		}
		out[f.Scanner][filepath.Base(f.Path)] = f
	}
	return out
}

func keys(m map[string]finding.Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestScanners(t *testing.T) {
	_, _, env := fixture(t)
	got := run(t, env)

	require.Equal(t, []string{"DaVinci_Resolve_21.1_Mac.zip", "Slack-4.41.105-macOS.dmg", "zen.macos-universal.dmg"}, keys(got["installers"]),
		"an old installer is an installer, not an attention item")
	zen := got["installers"]["zen.macos-universal.dmg"]
	require.Equal(t, finding.TierB, zen.Tier)
	require.Equal(t, finding.KindFile, zen.Kind)
	require.Equal(t, finding.ActionRemovePath, zen.Action)
	require.Contains(t, zen.Restore, "Zen.app is installed")

	require.Equal(t, []string{"Slack-old.zip", "project-1.2.tar.gz"}, keys(got["extracted-archives"]),
		"an archive with an extracted copy wins over the installer rule")
	arch := got["extracted-archives"]["project-1.2.tar.gz"]
	require.Equal(t, finding.TierC, arch.Tier)
	require.Contains(t, arch.Warning, "the folder project-1.2 next to it")

	require.Equal(t, []string{"Unknown-1.0.dmg", "talk.mov"}, keys(got["old-downloads"]),
		"large and untouched for 90 days; small, recent, nested and hidden files are not listed")
	talk := got["old-downloads"]["talk.mov"]
	require.Equal(t, finding.ActionNone, talk.Action)
	require.False(t, talk.Actionable())
	require.Equal(t, finding.TierC, talk.Tier)
	require.Contains(t, talk.Warning, "never removes it")
}

func TestReadingAFileKeepsItOffTheAttentionList(t *testing.T) {
	_, dl, env := fixture(t)
	p := filepath.Join(dl, "talk.mov")
	read := now.Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(p, read, now.Add(-200*24*time.Hour)))
	info, err := os.Stat(p)
	require.NoError(t, err)
	if _, ok := fsx.AccessTime(info); !ok {
		t.Skip("this platform reports no access times")
	}
	require.NotContains(t, run(t, env)["old-downloads"], "talk.mov")
}

func TestNoDownloadsFolder(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	got := run(t, scan.Env{Home: tmp, Now: now})
	require.Empty(t, got)
}

// TestApplyRemovesOnlyTheSelectedDownloads applies every Downloads finding
// that can be applied, and checks that exactly those files are gone while
// attention files, extracted folders, apps and everything else remain.
func TestApplyRemovesOnlyTheSelectedDownloads(t *testing.T) {
	home, dl, env := fixture(t)
	res := scan.Run(context.Background(), func() []scan.Scanner {
		ss := make([]scan.Scanner, 0, len(Scanners()))
		for _, s := range Scanners() {
			ss = append(ss, s)
		}
		return ss
	}(), env, 3)
	r := &finding.Report{Version: finding.ReportVersion, Created: now, Roots: []string{home}, Findings: res.Findings}
	selected := plan.SelectTiers(r.Findings, []finding.Tier{finding.TierA, finding.TierB})
	for _, f := range r.Findings {
		if f.Scanner == "extracted-archives" && filepath.Base(f.Path) == "project-1.2.tar.gz" {
			selected = append(selected, f) // tier C ticked on its own
		}
	}
	p, err := plan.New(r, selected, "host", now)
	require.NoError(t, err)
	require.NoError(t, p.Validate())

	entries := func() []string {
		des, err := os.ReadDir(dl)
		require.NoError(t, err)
		out := make([]string, 0, len(des))
		for _, d := range des {
			out = append(out, d.Name())
		}
		return out
	}
	_, err = apply.Run(context.Background(), p, apply.Options{Home: home, Walker: fsx.NewWalker(1), Exec: &execx.Fake{}})
	require.NoError(t, err)
	require.Equal(t, []string{
		".hidden-old.iso", "Slack export Jan 2024.zip", "Slack-old", "Slack-old.zip", "Unknown-1.0.dmg", "Zen backup.dmg",
		"Zen-2.0-mac.zip", "nested", "project-1.2", "recent.mov", "small-old.pdf", "talk.mov",
	}, entries())
	require.FileExists(t, filepath.Join(dl, "project-1.2", "README"))
}
