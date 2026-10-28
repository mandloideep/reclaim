package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExcludeTargets(t *testing.T) {
	cfg, err := Parse(`exclude = ["~/Code/archive", "docker:4f2a9c1e7b3d", "ollama:llama3", "docker:4f2a9c1e7b3d"]`, "/Users/me")
	require.NoError(t, err)
	require.Equal(t, []string{"/Users/me/Code/archive"}, cfg.Exclude)
	require.Equal(t, []string{"docker:4f2a9c1e7b3d", "ollama:llama3"}, cfg.ExcludeTargets)
	require.Equal(t, cfg.Exclude, cfg.ExcludedPaths(), "no ignore list adds nothing")
	require.Equal(t, cfg.ExcludeTargets, cfg.ExcludedTargets())

	_, err = Parse(`exclude = ["docker:"]`, "/Users/me")
	require.ErrorContains(t, err, `exclude: "docker:" is not a target entry`)
}

func TestIgnoreList(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cfgPath := DefaultPath(home, nil)
	path := IgnorePath(cfgPath)
	require.Equal(t, filepath.Join(home, ".config", "reclaim", "ignore"), path)

	// No file is an empty list, with or without config.toml.
	cfg, err := Load(cfgPath, home)
	require.NoError(t, err)
	require.Equal(t, path, cfg.Ignore.Path)
	require.Empty(t, cfg.Ignore.Entries)

	// The first edit creates the folder and the file with its header.
	keep := filepath.Join(home, "Code", "web", "dist")
	ig, err := EditIgnore(path, home, []string{keep, "docker:4f2a9c1e7b3d", keep, "~/Code/web/dist"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{keep, "docker:4f2a9c1e7b3d"}, ig.Entries, "an entry already on the list is not added twice")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# Things reclaim never offers again")
	require.Contains(t, string(data), "\n~/Code/web/dist\ndocker:4f2a9c1e7b3d\n", "paths are written with ~")

	// Hand edits survive: comments, blank lines and an absolute path.
	require.NoError(t, os.WriteFile(path, append(data, []byte("\n# mine\n/Volumes/work/keep\n")...), 0o644))
	ig, err = EditIgnore(path, home, []string{"ollama:llama3"}, []string{"~/Code/web/dist"})
	require.NoError(t, err)
	require.Equal(t, []string{"docker:4f2a9c1e7b3d", "/Volumes/work/keep", "ollama:llama3"}, ig.Entries)
	require.Equal(t, []string{"/Volumes/work/keep"}, ig.Paths())
	require.Equal(t, []string{"docker:4f2a9c1e7b3d", "ollama:llama3"}, ig.Targets())
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# mine\n/Volumes/work/keep\nollama:llama3\n")
	require.NotContains(t, string(data), "~/Code/web/dist")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file is left next to the list")

	// Load merges the list into what every command excludes.
	require.NoError(t, os.WriteFile(cfgPath, []byte(`exclude = ["~/Code/archive", "docker:data"]`), 0o644))
	cfg, err = Load(cfgPath, home)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(home, "Code", "archive"), "/Volumes/work/keep"}, cfg.ExcludedPaths())
	require.Equal(t, []string{"docker:data", "docker:4f2a9c1e7b3d", "ollama:llama3"}, cfg.ExcludedTargets())

	// A bad line is an error that names the file and the line.
	require.NoError(t, os.WriteFile(path, []byte("# ok\nCode/relative\n"), 0o644))
	_, err = Load(cfgPath, home)
	require.ErrorContains(t, err, "ignore list "+path+": line 2:")
	_, err = EditIgnore(path, home, []string{"/x/y"}, nil)
	require.ErrorContains(t, err, "line 2")
	_, err = EditIgnore(filepath.Join(home, "other"), home, []string{"relative"}, nil)
	require.ErrorContains(t, err, `"relative" is not an absolute path`)
	require.NoFileExists(t, filepath.Join(home, "other"), "a refused entry writes nothing")
}

// TestIgnorePathFollowsTheConfigFile checks the name of the list for the
// default configuration file and for one --config names.
func TestIgnorePathFollowsTheConfigFile(t *testing.T) {
	require.Equal(t, "/Users/me/.config/reclaim/ignore", IgnorePath("/Users/me/.config/reclaim/config.toml"))
	require.Equal(t, "/Users/me/dotfiles/reclaim.ignore", IgnorePath("/Users/me/dotfiles/reclaim.toml"))
	require.Equal(t, "/etc/reclaim.ignore", IgnorePath("/etc/reclaim"))
}

// TestEditIgnoreKeepsALinkAndItsMode checks that a list kept through a
// symbolic link is written where the link points, the link stays, and the
// file keeps its permissions.
func TestEditIgnoreKeepsALinkAndItsMode(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(home, "dotfiles", "ignore")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("# mine\n"), 0o600))
	link := filepath.Join(home, ".config", "reclaim", "ignore")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))

	ig, err := EditIgnore(link, home, []string{"docker:data"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"docker:data"}, ig.Entries)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the link stays a link")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "# mine\ndocker:data\n", string(data))
	info, err = os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestIgnoreOnANilList(t *testing.T) {
	var ig *Ignore
	require.Empty(t, ig.Paths())
	require.Empty(t, ig.Targets())
	cfg := &Config{Exclude: []string{"/a"}}
	require.Equal(t, []string{"/a"}, cfg.ExcludedPaths())
}
