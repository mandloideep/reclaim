package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
)

// IgnoreFile is the name of the ignore list, which lives next to the
// configuration file.
const IgnoreFile = "ignore"

// ignoreHeader starts an ignore list that reclaim creates, so a person who
// opens it knows what it is.
const ignoreHeader = "# Things reclaim never offers again, and apply refuses, one per line:\n" +
	"# a path starting with ~/ or absolute, or a target such as docker:<id> or ollama:<model>.\n" +
	"# The interactive screen adds to it with x on the Review screen and removes with d on the Start screen.\n"

// Ignore is the persistent ignore list: one entry per line, a path
// starting with ~/ or absolute, or a target entry, with # comments and
// blank lines ignored. reclaim treats its entries exactly as exclude
// entries.
type Ignore struct {
	// Path is the file the list was read from, which may not exist yet.
	Path string
	// Entries are the entries in file order, each a clean absolute path
	// with symbolic links resolved or a target entry, without duplicates.
	Entries []string
}

// Paths returns the path entries.
func (ig *Ignore) Paths() []string {
	if ig == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(ig.Entries), finding.IsTargetEntry)
}

// Targets returns the target entries.
func (ig *Ignore) Targets() []string {
	if ig == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(ig.Entries), func(e string) bool { return !finding.IsTargetEntry(e) })
}

// IgnorePath returns the ignore list that goes with the configuration file
// at configPath: the file named ignore in the same folder.
func IgnorePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), IgnoreFile)
}

// LoadIgnore reads the ignore list at path. A missing file is an empty list.
func LoadIgnore(path, home string) (*Ignore, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Ignore{Path: path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ignore list %s: %w", path, err)
	}
	entries, err := parseIgnore(string(data), home)
	if err != nil {
		return nil, fmt.Errorf("ignore list %s: %w", path, err)
	}
	return &Ignore{Path: path, Entries: entries}, nil
}

// parseIgnore returns the entries of an ignore list in file order.
func parseIgnore(text, home string) ([]string, error) {
	var out []string
	for i, line := range strings.Split(text, "\n") {
		e, ok, err := ignoreLine(line, home)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if ok && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// ignoreLine returns the entry of one line of the ignore list, and false
// for a comment or a blank line.
func ignoreLine(line, home string) (string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false, nil
	}
	e, err := entry(line, home)
	return e, err == nil, err
}

// entry turns an exclude or ignore entry into its canonical form: a target
// entry as written, or a clean absolute path with symbolic links resolved.
func entry(text, home string) (string, error) {
	text = strings.TrimSpace(text)
	if finding.IsTargetEntry(text) {
		if strings.Contains(text, " ") || strings.HasSuffix(text, ":") {
			return "", fmt.Errorf("%q is not a target entry such as docker:<id> or ollama:<model>", text)
		}
		return text, nil
	}
	return Expand(text, home)
}

// EditIgnore adds entries to the ignore list at path and drops entries
// from it, then returns the list as written. Entries are paths starting
// with ~/ or absolute, or target entries; an entry already on the list is
// not added twice, and dropping an entry removes every line that names it.
// Every other line, comments included, is kept as it was. The file is
// written atomically, and its folder is created when it does not exist.
// Nothing but the list itself is ever written.
func EditIgnore(path, home string, add, drop []string) (*Ignore, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read ignore list %s: %w", path, err)
	}
	text := string(data)
	if errors.Is(err, fs.ErrNotExist) {
		text = ignoreHeader
	}
	var dropped []string
	for _, d := range drop {
		e, err := entry(d, home)
		if err != nil {
			return nil, err
		}
		dropped = append(dropped, e)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	kept := make([]string, 0, len(lines)+len(add))
	var have []string
	for i, line := range lines {
		e, ok, err := ignoreLine(line, home)
		if err != nil {
			return nil, fmt.Errorf("ignore list %s: line %d: %w", path, i+1, err)
		}
		if ok && slices.Contains(dropped, e) {
			continue
		}
		if ok {
			have = append(have, e)
		}
		kept = append(kept, line)
	}
	for _, a := range add {
		e, err := entry(a, home)
		if err != nil {
			return nil, err
		}
		if slices.Contains(have, e) {
			continue
		}
		have = append(have, e)
		kept = append(kept, written(e, home))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the folder of %s: %w", path, err)
	}
	if err := fsx.WriteFileAtomic(path, []byte(strings.Join(kept, "\n")+"\n")); err != nil {
		return nil, err
	}
	return LoadIgnore(path, home)
}

// written is how an entry is written to the ignore list: a path under the
// home folder starting with ~/, so the file reads the way the screen shows
// paths.
func written(e, home string) string {
	if home != "" && strings.HasPrefix(e, home+string(filepath.Separator)) {
		return "~/" + strings.TrimPrefix(e, home+string(filepath.Separator))
	}
	return e
}
