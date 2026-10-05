// Package config loads the optional configuration file,
// ~/.config/reclaim/config.toml:
//
//	roots = ["~/Code", "~/Downloads"]
//	exclude = ["~/Code/archive"]
//	min_size = "20MB"
//	stale = "60d"
//
//	[scanners]
//	disable = ["ollama"]
//
// Every key is optional. An unknown key is an error that names it, so a typo
// never silently changes what a scan covers. Command line flags override the
// file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/mandloideep/reclaim/internal/units"
)

// file is the TOML layout.
type file struct {
	Roots    []string `toml:"roots"`
	Exclude  []string `toml:"exclude"`
	MinSize  string   `toml:"min_size"`
	Stale    string   `toml:"stale"`
	Scanners struct {
		Disable []string `toml:"disable"`
	} `toml:"scanners"`
}

// Config is the loaded configuration. Paths are absolute and clean, with "~"
// expanded. Zero values mean the key was not set.
type Config struct {
	// Path is the file the configuration was read from, empty when there was
	// no file.
	Path string
	// Roots replace the default scan roots when set.
	Roots []string
	// Exclude are paths the project walk never enters and whose findings are
	// dropped.
	Exclude []string
	// MinSize is the default for --min-size. HasMinSize reports whether it was set.
	MinSize    int64
	HasMinSize bool
	// Stale is the default for --stale.
	Stale time.Duration
	// Disable lists scanner names, categories or ecosystems that do not run
	// unless a --category flag names them.
	Disable []string
}

// DefaultPath returns the configuration file path: $XDG_CONFIG_HOME/reclaim
// when that variable holds an absolute path, else ~/.config/reclaim, on
// macOS too, because that is where command line tools keep their settings.
func DefaultPath(home string, getenv func(string) string) string {
	base := filepath.Join(home, ".config")
	if getenv != nil {
		if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
			base = x
		}
	}
	return filepath.Join(base, "reclaim", "config.toml")
}

// Load reads the configuration at path. A missing file is not an error and
// yields an empty Config.
func Load(path, home string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg, err := Parse(string(data), home)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

// Parse parses configuration text. Paths starting with "~" are expanded
// against home; other relative paths are an error.
func Parse(text, home string) (*Config, error) {
	var f file
	md, err := toml.Decode(text, &f)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		if len(keys) == 1 {
			return nil, fmt.Errorf("unknown key %q", keys[0])
		}
		return nil, fmt.Errorf("unknown keys %s", quoteAll(keys))
	}
	cfg := &Config{}
	if cfg.Roots, err = expandAll("roots", f.Roots, home); err != nil {
		return nil, err
	}
	if cfg.Exclude, err = expandAll("exclude", f.Exclude, home); err != nil {
		return nil, err
	}
	if f.MinSize != "" {
		if cfg.MinSize, err = units.ParseSize(f.MinSize); err != nil {
			return nil, fmt.Errorf("min_size: %w", err)
		}
		cfg.HasMinSize = true
	}
	if f.Stale != "" {
		if cfg.Stale, err = units.ParseAge(f.Stale); err != nil {
			return nil, fmt.Errorf("stale: %w", err)
		}
	}
	for _, d := range f.Scanners.Disable {
		if d = strings.TrimSpace(d); d != "" && !slices.Contains(cfg.Disable, d) {
			cfg.Disable = append(cfg.Disable, d)
		}
	}
	return cfg, nil
}

func expandAll(key string, paths []string, home string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		e, err := Expand(p, home)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Expand turns "~" and "~/x" into paths under home and checks that the
// result is absolute. The path is cleaned, and symbolic links are resolved
// when it exists, so it compares equal to the canonical paths in findings.
func Expand(p, home string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path or a path starting with ~/", p)
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}
