package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/execx"
)

type nameMatcher string

func (n nameMatcher) Name() string      { return string(n) }
func (n nameMatcher) Match(d *Dir) bool { return d.Name == string(n) }

type funcMatcher struct {
	name string
	fn   func(d *Dir) bool
}

func (f funcMatcher) Name() string      { return f.name }
func (f funcMatcher) Match(d *Dir) bool { return f.fn(d) }

func touch(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	if !mtime.IsZero() {
		require.NoError(t, os.Chtimes(path, mtime, mtime))
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
}

func artifactPaths(ix *Index) []string {
	out := make([]string, 0, len(ix.Artifacts))
	for _, a := range ix.Artifacts {
		out = append(out, a.Dir.Path)
	}
	return out
}

func projectRoots(ix *Index) []string {
	out := make([]string, 0, len(ix.Projects))
	for _, p := range ix.Projects {
		out = append(out, p.Root)
	}
	return out
}

func TestDiscoverFindsProjectsAndPrunesArtifacts(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-400 * 24 * time.Hour)

	// A node project with node_modules that itself contains package.json files.
	touch(t, filepath.Join(root, "web", "package.json"), old)
	touch(t, filepath.Join(root, "web", "src", "index.js"), old)
	touch(t, filepath.Join(root, "web", "node_modules", "left-pad", "package.json"), time.Now())
	touch(t, filepath.Join(root, "web", "node_modules", "left-pad", "node_modules", "x", "package.json"), time.Now())
	// A go project.
	touch(t, filepath.Join(root, "svc", "go.mod"), old)
	// An xcode project detected by suffix.
	mkdir(t, filepath.Join(root, "ios", "App.xcodeproj"))
	// A folder that is not a project.
	touch(t, filepath.Join(root, "notes", "todo.txt"), old)

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("node_modules")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "web", "node_modules")}, artifactPaths(ix))
	require.Equal(t, []string{filepath.Join(root, "ios"), filepath.Join(root, "svc"), filepath.Join(root, "web")}, projectRoots(ix))

	web := ix.Artifacts[0].Project
	require.NotNil(t, web)
	require.Equal(t, filepath.Join(root, "web"), web.Root)
	require.Equal(t, []string{"package.json"}, web.Markers)
	// Files inside node_modules are newer but must not count as activity.
	require.WithinDuration(t, old, web.LastActivity(), time.Second)
}

func TestDiscoverGitOwnsNestedManifests(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "mono")
	mkdir(t, filepath.Join(repo, ".git"))
	touch(t, filepath.Join(repo, "package.json"), time.Time{})
	touch(t, filepath.Join(repo, "packages", "a", "package.json"), time.Time{})
	mkdir(t, filepath.Join(repo, "packages", "a", "node_modules"))
	// A nested repository is its own project.
	mkdir(t, filepath.Join(repo, "vendor", "lib", ".git"))
	mkdir(t, filepath.Join(repo, "vendor", "lib", "node_modules"))

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("node_modules")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Len(t, ix.Artifacts, 2)
	require.Equal(t, repo, ix.Artifacts[0].Project.Root)
	require.Equal(t, filepath.Join(repo, "vendor", "lib"), ix.Artifacts[1].Project.Root)
	require.True(t, ix.Artifacts[0].Project.Git)
}

func TestDiscoverNearestMarkerWithoutGit(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "app", "package.json"), time.Time{})
	touch(t, filepath.Join(root, "app", "server", "go.mod"), time.Time{})
	mkdir(t, filepath.Join(root, "app", "server", "bin"))

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("bin")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Len(t, ix.Artifacts, 1)
	require.Equal(t, filepath.Join(root, "app", "server"), ix.Artifacts[0].Project.Root)
}

func TestDiscoverLastActivityUsesNewestOfCommitAndFiles(t *testing.T) {
	root := t.TempDir()
	fileTime := time.Now().Add(-100 * 24 * time.Hour).Truncate(time.Second)
	repo := filepath.Join(root, "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	touch(t, filepath.Join(repo, "main.go"), fileTime)
	touch(t, filepath.Join(repo, "go.mod"), fileTime)

	tests := []struct {
		name   string
		commit time.Time
		want   time.Time
	}{
		{name: "commit newer", commit: fileTime.Add(48 * time.Hour), want: fileTime.Add(48 * time.Hour)},
		{name: "file newer", commit: fileTime.Add(-48 * time.Hour), want: fileTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix, err := Discover(context.Background(), []string{root}, nil, Options{
				Home: filepath.Dir(root),
				CommitTime: func(_ context.Context, r string) (time.Time, error) {
					require.Equal(t, repo, r)
					return tt.commit, nil
				},
			})
			require.NoError(t, err)
			require.Len(t, ix.Projects, 1)
			require.True(t, tt.want.Equal(ix.Projects[0].LastActivity()), "got %v want %v", ix.Projects[0].LastActivity(), tt.want)
		})
	}
}

func TestDiscoverDoesNotFollowSymlinksOrEnterGit(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mkdir(t, filepath.Join(outside, "proj", "node_modules"))
	touch(t, filepath.Join(outside, "proj", "package.json"), time.Time{})
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
	mkdir(t, filepath.Join(root, "repo", ".git", "node_modules"))

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("node_modules")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Empty(t, ix.Artifacts)
}

func TestDiscoverNeverMatchesWorkingTrees(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "site", "package.json"), time.Time{})
	touch(t, filepath.Join(root, "site", "dist", ".git"), time.Time{})

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("dist")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Empty(t, ix.Artifacts)
}

func TestDiscoverNeverMatchesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node_modules")
	mkdir(t, filepath.Join(root, "pkg"))
	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("node_modules")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Empty(t, ix.Artifacts)
	require.Equal(t, map[string]string{root: root}, ix.InsideArtifact)
}

func TestDiscoverRootInsideArtifact(t *testing.T) {
	home := t.TempDir()
	modules := filepath.Join(home, "Code", "web", "node_modules")
	pkg := filepath.Join(modules, "left-pad")
	touch(t, filepath.Join(home, "Code", "web", "package.json"), time.Time{})
	touch(t, filepath.Join(pkg, "package.json"), time.Time{})
	mkdir(t, filepath.Join(pkg, "dist"))
	mkdir(t, filepath.Join(pkg, "node_modules", "x"))

	ix, err := Discover(context.Background(), []string{pkg}, []Matcher{nameMatcher("node_modules"), nameMatcher("dist")}, Options{Home: home})
	require.NoError(t, err)
	require.Empty(t, ix.Artifacts, "nothing inside node_modules is offered on its own")
	require.Empty(t, ix.Projects)
	require.Equal(t, map[string]string{pkg: modules}, ix.InsideArtifact)
}

func TestDiscoverNeverMatchesProjects(t *testing.T) {
	root := t.TempDir()
	// "python -m venv ." inside a project puts pyvenv.cfg in the project root.
	touch(t, filepath.Join(root, "app", "pyproject.toml"), time.Time{})
	touch(t, filepath.Join(root, "app", "pyvenv.cfg"), time.Time{})
	touch(t, filepath.Join(root, "app", "main.py"), time.Time{})
	// A build folder that is itself a package.
	touch(t, filepath.Join(root, "lib", "package.json"), time.Time{})
	touch(t, filepath.Join(root, "lib", "build", "package.json"), time.Time{})
	// Generated folders may hold suffix markers and still match.
	touch(t, filepath.Join(root, "ios", "Podfile"), time.Time{})
	mkdir(t, filepath.Join(root, "ios", "Pods", "Pods.xcodeproj"))

	venv := funcMatcher{name: "venv", fn: func(d *Dir) bool { return d.HasFile("pyvenv.cfg") }}
	ix, err := Discover(context.Background(), []string{root}, []Matcher{venv, nameMatcher("build"), nameMatcher("Pods")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "ios", "Pods")}, artifactPaths(ix))
}

func TestDiscoverDepth(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "a", "target"))
	mkdir(t, filepath.Join(root, "a", "b", "c", "target"))

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("target")}, Options{Depth: 2, Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "a", "target")}, artifactPaths(ix))
}

func TestDiscoverExclude(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "keep", "target"))
	mkdir(t, filepath.Join(root, "archive", "old", "target"))
	mkdir(t, filepath.Join(root, "keep", "vendor", "target"))
	other := filepath.Join(t.TempDir(), "excluded-root")
	mkdir(t, filepath.Join(other, "target"))

	ix, err := Discover(context.Background(), []string{root, other}, []Matcher{nameMatcher("target")}, Options{
		Home:    filepath.Dir(root),
		Exclude: []string{filepath.Join(root, "archive"), filepath.Join(root, "keep", "vendor", "target"), other},
	})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "keep", "target")}, artifactPaths(ix))
}

func TestDiscoverEnclosingProject(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "Code", "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	mkdir(t, filepath.Join(repo, "packages", "web", "node_modules"))
	// A dotfiles repository in the home directory must be ignored.
	mkdir(t, filepath.Join(home, ".git"))
	mkdir(t, filepath.Join(home, "Code", "loose", "node_modules"))

	ix, err := Discover(context.Background(),
		[]string{filepath.Join(repo, "packages"), filepath.Join(home, "Code", "loose")},
		[]Matcher{nameMatcher("node_modules")}, Options{Home: home})
	require.NoError(t, err)
	require.Len(t, ix.Artifacts, 2)
	require.Nil(t, ix.Artifacts[0].Project, "loose folder has no project")
	require.Equal(t, repo, ix.Artifacts[1].Project.Root)
}

func TestDiscoverUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	mkdir(t, filepath.Join(locked, "node_modules"))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	ix, err := Discover(context.Background(), []string{root}, []Matcher{nameMatcher("node_modules")}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Empty(t, ix.Artifacts)
	require.Len(t, ix.Unreadable, 1)
	require.Equal(t, locked, ix.Unreadable[0].Path)
}

func TestDiscoverCancellation(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "a", "b"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Discover(ctx, []string{root}, nil, Options{Home: filepath.Dir(root)})
	require.ErrorIs(t, err, context.Canceled)
}

func TestMatcherSeesParentAndProject(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "crate", "Cargo.toml"), time.Time{})
	mkdir(t, filepath.Join(root, "crate", "target"))
	mkdir(t, filepath.Join(root, "other", "target"))

	m := funcMatcher{name: "rust", fn: func(d *Dir) bool {
		return d.Name == "target" && d.Parent.HasFile("Cargo.toml") && d.Project != nil
	}}
	ix, err := Discover(context.Background(), []string{root}, []Matcher{m}, Options{Home: filepath.Dir(root)})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "crate", "target")}, artifactPaths(ix))
	require.Len(t, ix.ArtifactsFor("rust"), 1)
	require.Empty(t, ix.ArtifactsFor("other"))
}

func TestNormalizeRoots(t *testing.T) {
	got := NormalizeRoots([]string{"/a/b/", "/a", "/c", "/a/b", "/cd"})
	require.Equal(t, []string{"/a", "/c", "/cd"}, got)
}

func TestIsWithin(t *testing.T) {
	require.True(t, IsWithin("/a/b", "/a"))
	require.True(t, IsWithin("/a", "/a"))
	require.False(t, IsWithin("/ab", "/a"))
	require.True(t, IsWithin("/x", "/"))
}

func TestSourceRunsOnce(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "node_modules"))
	var calls atomic.Int32
	s := NewSource([]string{root}, []Matcher{funcMatcher{name: "n", fn: func(*Dir) bool {
		calls.Add(1)
		return false
	}}}, Options{Home: filepath.Dir(root)})
	_, err := s.Index(context.Background())
	require.NoError(t, err)
	first := calls.Load()
	require.Positive(t, first)
	_, err = s.Index(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, calls.Load(), "the second call reuses the first walk")
}

func TestGitCommitTime(t *testing.T) {
	repo := t.TempDir()
	r := &execx.Fake{
		Tools:   map[string]string{"git": "/usr/bin/git"},
		Outputs: map[string]string{"git -C " + repo + " --no-pager log -1 --format=%ct": "1700000000\n"},
	}
	got, err := GitCommitTime(r)(context.Background(), repo)
	require.NoError(t, err)
	require.Equal(t, int64(1700000000), got.Unix())

	t.Run("falls back to the reflog", func(t *testing.T) {
		logs := filepath.Join(repo, ".git", "logs", "HEAD")
		when := time.Now().Add(-time.Hour).Truncate(time.Second)
		touch(t, logs, when)
		got, err := GitCommitTime(&execx.Fake{})(context.Background(), repo)
		require.NoError(t, err)
		require.True(t, when.Equal(got))
	})
	t.Run("no git data", func(t *testing.T) {
		_, err := GitCommitTime(&execx.Fake{})(context.Background(), t.TempDir())
		require.True(t, errors.Is(err, os.ErrNotExist))
	})
}
