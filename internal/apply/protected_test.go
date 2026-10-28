package apply

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProtectedListCoversTheDenylists proves the protected list the screen
// shows names every path of the two denylists the path checks use, so the
// list cannot drift from the tables.
func TestProtectedListCoversTheDenylists(t *testing.T) {
	home := "/Users/me"
	entries := ProtectedList(ProtectedOptions{
		Home:       home,
		Roots:      []string{"/Users/me/Code", "/Users/me"},
		Exclude:    []string{"/Users/me/Code/archive"},
		ConfigPath: "/Users/me/.config/reclaim/config.toml",
	})
	paths := map[string]ProtectedSource{}
	for _, e := range entries {
		require.NotEmpty(t, e.Text, "every entry says what it protects")
		require.NotEmpty(t, e.Source, "every entry says where it comes from")
		if e.Path != "" {
			if _, seen := paths[e.Path]; !seen {
				paths[e.Path] = e.Source
			}
		}
	}
	for _, p := range protectedSystemPaths() {
		require.Equal(t, ProtectedSystem, paths[p], "system path %s", p)
	}
	for _, rel := range protectedHomePaths() {
		p := filepath.Join(home, rel)
		require.Contains(t, paths, p, "home path %s", p)
	}
	require.Contains(t, paths, "/Users/me/Code")
	require.Equal(t, ProtectedExclude, paths["/Users/me/Code/archive"])

	// Every path the list names is refused by the path checks, and the
	// exclude entries by the exclude check.
	for p, src := range paths {
		if src == ProtectedExclude {
			require.Error(t, checkExcluded(p, []string{"/Users/me/Code/archive"}), p)
			continue
		}
		require.Error(t, checkProtected(p, home, "", []string{"/Users/me"}), "%s is listed as protected", p)
	}

	var rules []string
	for _, e := range entries {
		if e.Source == ProtectedRule {
			rules = append(rules, e.Text)
		}
	}
	require.True(t, slices.ContainsFunc(rules, func(r string) bool { return r == "nothing inside a .git directory" }))
	require.Len(t, slices.DeleteFunc(slices.Clone(entries), func(e ProtectedEntry) bool { return e.Source != ProtectedRoot }), 2)
}

func TestProtectedListWithoutHome(t *testing.T) {
	entries := ProtectedList(ProtectedOptions{})
	for _, e := range entries {
		require.NotEqual(t, ProtectedHome, e.Source)
	}
	require.NotEmpty(t, entries)
}
