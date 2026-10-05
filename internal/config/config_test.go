package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	home := "/Users/me"
	cfg, err := Parse(`
roots = ["~/Code", "~/Downloads", "/Volumes/work/src", "~/Code"]
exclude = ["~/Code/archive"]
min_size = "20MB"
stale = "60d"

[scanners]
disable = ["ollama", "docker", "ollama"]
`, home)
	require.NoError(t, err)
	require.Equal(t, []string{"/Users/me/Code", "/Users/me/Downloads", "/Volumes/work/src"}, cfg.Roots)
	require.Equal(t, []string{"/Users/me/Code/archive"}, cfg.Exclude)
	require.Equal(t, int64(20_000_000), cfg.MinSize)
	require.True(t, cfg.HasMinSize)
	require.Equal(t, 60*24*time.Hour, cfg.Stale)
	require.Equal(t, []string{"ollama", "docker"}, cfg.Disable)

	empty, err := Parse("", home)
	require.NoError(t, err)
	require.False(t, empty.HasMinSize)
	require.Empty(t, empty.Roots)
	require.Zero(t, empty.Stale)
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr string
	}{
		{name: "unknown key", text: `root = ["~/Code"]`, wantErr: `unknown key "root"`},
		{name: "unknown nested key", text: "[scanners]\nenable = [\"x\"]", wantErr: `unknown key "scanners.enable"`},
		{name: "unknown keys", text: "a = 1\nb = 2", wantErr: `unknown keys "a", "b"`},
		{name: "bad size", text: `min_size = "lots"`, wantErr: "min_size"},
		{name: "bad age", text: `stale = "soon"`, wantErr: "stale"},
		{name: "relative root", text: `roots = ["Code"]`, wantErr: `roots: "Code" is not an absolute path`},
		{name: "relative exclude", text: `exclude = ["./x"]`, wantErr: "exclude"},
		{name: "wrong type", text: `roots = "~/Code"`, wantErr: "roots"},
		{name: "not toml", text: `roots = [`, wantErr: "expected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.text, "/Users/me")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	home, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	path := DefaultPath(home, nil)
	require.Equal(t, filepath.Join(home, ".config", "reclaim", "config.toml"), path)

	cfg, err := Load(path, home)
	require.NoError(t, err, "a missing file is an empty configuration")
	require.Empty(t, cfg.Path)

	require.NoError(t, os.MkdirAll(filepath.Join(home, "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(home, "real"), filepath.Join(home, "link")))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`roots = ["~/link"]`), 0o644))
	cfg, err = Load(path, home)
	require.NoError(t, err)
	require.Equal(t, path, cfg.Path)
	require.Equal(t, []string{filepath.Join(home, "real")}, cfg.Roots, "symbolic links are resolved")

	require.NoError(t, os.WriteFile(path, []byte(`nope = 1`), 0o644))
	_, err = Load(path, home)
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, err, `unknown key "nope"`)
}

func TestDefaultPathHonorsXDG(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return v
			}
			return ""
		}
	}
	require.Equal(t, "/xdg/reclaim/config.toml", DefaultPath("/Users/me", env("/xdg")))
	require.Equal(t, "/Users/me/.config/reclaim/config.toml", DefaultPath("/Users/me", env("relative")))
}
