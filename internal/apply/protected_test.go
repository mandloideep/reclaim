package apply

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
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
			require.Error(t, checkExcluded(p, nil, Exclusions{Exclude: []string{"/Users/me/Code/archive"}}), p)
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

// TestProtectedListNamesTheUsersEntries checks that the exclude entries and
// the ignore list, paths and targets, are listed under their own titles with
// the file they come from, and that the exclude check refuses each of them.
func TestProtectedListNamesTheUsersEntries(t *testing.T) {
	ex := Exclusions{Exclude: []string{"/Users/me/Code/archive", "docker:data"}, Ignore: []string{"/Users/me/Code/web/dist", "ollama:llama3"},
		IgnorePath: "/Users/me/.config/reclaim/ignore"}
	entries := ProtectedList(ProtectedOptions{Home: "/Users/me", Exclude: ex.Exclude, ConfigPath: "/Users/me/.config/reclaim/config.toml",
		Ignore: ex.Ignore, IgnorePath: ex.IgnorePath})
	got := map[string]ProtectedEntry{}
	for _, e := range entries {
		if e.Source == ProtectedExclude || e.Source == ProtectedIgnore {
			got[e.Path] = e
		}
	}
	require.Len(t, got, 4)
	require.Equal(t, ProtectedIgnore, got["ollama:llama3"].Source)
	require.Contains(t, got["ollama:llama3"].Text, "apply refuses the object it names; set in /Users/me/.config/reclaim/ignore")
	require.Contains(t, got["/Users/me/Code/archive"].Text, "set in /Users/me/.config/reclaim/config.toml")
	require.Error(t, checkExcluded("/Users/me/Code/archive", nil, ex))
	require.Error(t, checkExcluded("/Users/me/Code/web/dist", nil, ex))
	require.Error(t, checkExcluded("", &plan.Action{Action: finding.ActionDockerRemoveVolume, Target: "data"}, ex))
	require.Error(t, checkExcluded("", &plan.Action{Action: finding.ActionRunCommand, Target: "ollama:llama3:latest"}, ex))
}

func TestProtectedListWithoutHome(t *testing.T) {
	entries := ProtectedList(ProtectedOptions{})
	for _, e := range entries {
		require.NotEqual(t, ProtectedHome, e.Source)
	}
	require.NotEmpty(t, entries)
}
