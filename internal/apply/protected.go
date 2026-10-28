package apply

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mandloideep/reclaim/internal/project"
)

// ProtectedSource says where an entry of the protected list comes from.
type ProtectedSource string

// Sources of protected entries, in the order the list shows them.
const (
	// ProtectedSystem is a system path from the built in denylist.
	ProtectedSystem ProtectedSource = "built in, system"
	// ProtectedHome is a folder of the home directory from the built in
	// denylist.
	ProtectedHome ProtectedSource = "built in, home"
	// ProtectedRule is a rule of the path checks that names no single path.
	ProtectedRule ProtectedSource = "built in, rule"
	// ProtectedRoot is a root recorded in the report or the plan.
	ProtectedRoot ProtectedSource = "scan root"
	// ProtectedExclude is an exclude entry of the configuration file.
	ProtectedExclude ProtectedSource = "exclude in config"
)

// ProtectedEntry is one thing reclaim never removes, in words.
type ProtectedEntry struct {
	// Source says where the entry comes from.
	Source ProtectedSource
	// Path is the protected path, empty for a rule.
	Path string
	// Text says what is protected and how.
	Text string
}

// ProtectedOptions are the facts the protected list depends on.
type ProtectedOptions struct {
	// Home is the user's home directory.
	Home string
	// Roots are the roots of the scan or of the plan.
	Roots []string
	// Exclude are the exclude entries of the configuration.
	Exclude []string
	// ConfigPath is the configuration file the exclude entries come from.
	ConfigPath string
}

// ProtectedList renders in words the tables and rules the path checks of
// apply use, so a screen that shows it cannot drift from what apply does:
// the system paths, the protected folders of the home directory, the rules
// that name no single path, the roots, and the user's exclude entries.
func ProtectedList(opts ProtectedOptions) []ProtectedEntry {
	var out []ProtectedEntry
	for _, p := range protectedSystemPaths() {
		out = append(out, ProtectedEntry{Source: ProtectedSystem, Path: p, Text: "never removed itself"})
	}
	if opts.Home != "" {
		for _, rel := range protectedHomePaths() {
			p := filepath.Join(opts.Home, rel)
			text := "never removed itself; what lies inside may be, when a scanner found it"
			if rel == "" {
				text = "your home folder: never removed, nor any folder that contains it"
			}
			out = append(out, ProtectedEntry{Source: ProtectedHome, Path: p, Text: text})
		}
	}
	for _, rule := range []string{
		fmt.Sprintf("no path with fewer than %d components, such as /Users/me/Code", minComponents),
		"nothing inside a .git directory",
		"no folder holding a .git entry or a project manifest: " + strings.Join(project.ManifestNames(), ", "),
		"not the project a finding belongs to, nor any folder that contains it",
		"nothing outside the roots recorded at scan time, and no root itself nor any folder that contains one",
		"no path that goes through a symbolic link, no mount point and nothing on another filesystem",
		"nothing replaced since it was checked: every target is checked again right before it is removed",
		"a linked worktree only after its proof passes, and never a main checkout",
		"nothing that needs sudo: such commands are printed for you and never run",
	} {
		out = append(out, ProtectedEntry{Source: ProtectedRule, Text: rule})
	}
	for _, r := range opts.Roots {
		out = append(out, ProtectedEntry{Source: ProtectedRoot, Path: filepath.Clean(r),
			Text: "never removed itself, nor any folder that contains it; what reclaim removes lies inside a root"})
	}
	for _, ex := range opts.Exclude {
		text := "the scan never enters it, and apply refuses any target that is it, lies inside it or holds it"
		if opts.ConfigPath != "" {
			text += "; set in " + opts.ConfigPath
		}
		out = append(out, ProtectedEntry{Source: ProtectedExclude, Path: ex, Text: text})
	}
	return out
}
