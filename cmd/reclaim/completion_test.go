package main

import (
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// complete runs cobra's hidden completion command, as the shell scripts do,
// and returns the candidate values without descriptions and the directive.
func complete(t *testing.T, args ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	a, out := testApp(t, "")
	require.NoError(t, execute(a, append([]string{cobra.ShellCompRequestCmd}, args...)...))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.NotEmpty(t, lines)
	last := lines[len(lines)-1]
	require.True(t, strings.HasPrefix(last, ":"), "the directive comes last: %q", out.String())
	directive, err := strconv.Atoi(strings.TrimPrefix(last, ":"))
	require.NoError(t, err)
	values := make([]string, 0, len(lines)-1)
	for _, l := range lines[:len(lines)-1] {
		value, _, _ := strings.Cut(l, "\t")
		values = append(values, value)
	}
	return values, cobra.ShellCompDirective(directive)
}

func TestCompletion(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      []string
		contains  []string
		directive cobra.ShellCompDirective
	}{
		{name: "tiers", args: []string{"scan", "--tier", ""}, want: []string{"A", "B", "C"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "tiers with =", args: []string{"scan", "--tier=A,"}, want: []string{"A,B", "A,C"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "tiers with = and nothing yet", args: []string{"scan", "--tier="}, want: []string{"A", "B", "C"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "more tiers after a comma", args: []string{"scan", "--tier", "A,"}, want: []string{"A,B", "A,C"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "every tier listed", args: []string{"scan", "--tier", "A,B,C,"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{
			name: "categories, ecosystems and scanners", args: []string{"scan", "--category", ""},
			contains:  []string{"project", "package-cache", "docker", "app-cache", "downloads", "node", "python", "node_modules", "ollama", "xcode-derived-data"},
			directive: cobra.ShellCompDirectiveNoFileComp,
		},
		{
			name: "categories after a comma", args: []string{"scan", "--category", "docker,"},
			contains:  []string{"docker,node", "docker,ollama"},
			directive: cobra.ShellCompDirectiveNoFileComp,
		},
		{name: "presets", args: []string{"select", "--preset", ""}, want: []string{"safe", "aggressive"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "sizes", args: []string{"scan", "--min-size", ""}, contains: []string{"0", "10MB", "1GB"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "ages", args: []string{"scan", "--stale", ""}, contains: []string{"90d", "1y"}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "config files", args: []string{"scan", "--config", ""}, want: []string{"toml"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "config files on any command", args: []string{"select", "--config", ""}, want: []string{"toml"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "select reports", args: []string{"select", "--report", ""}, want: []string{"json"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "select plan", args: []string{"select", "--out", ""}, want: []string{"json"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "select has no arguments", args: []string{"select", ""}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "apply plan", args: []string{"apply", ""}, want: []string{"json"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "apply takes one plan", args: []string{"apply", "plan.json", ""}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "scan report", args: []string{"scan", "--out", ""}, want: []string{"json"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "scan folders", args: []string{"scan", ""}, directive: cobra.ShellCompDirectiveFilterDirs},
		{name: "here folder", args: []string{"here", ""}, directive: cobra.ShellCompDirectiveFilterDirs},
		{name: "here takes one folder", args: []string{"here", "src", ""}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "here report", args: []string{"here", "--out", ""}, want: []string{"json"}, directive: cobra.ShellCompDirectiveFilterFileExt},
		{name: "scanners has no arguments", args: []string{"scanners", ""}, directive: cobra.ShellCompDirectiveNoFileComp},
		{name: "version has no arguments", args: []string{"version", ""}, directive: cobra.ShellCompDirectiveNoFileComp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, directive := complete(t, tt.args...)
			require.Equal(t, tt.directive, directive)
			if tt.contains != nil {
				for _, v := range tt.contains {
					require.Contains(t, values, v)
				}
				return
			}
			require.ElementsMatch(t, tt.want, values)
		})
	}
}

// TestCategoryChoicesAreAccepted checks that every value completion offers
// for --category is one the scan accepts, and that none is offered twice.
func TestCategoryChoicesAreAccepted(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range categoryChoices() {
		require.False(t, seen[c.value], c.value)
		seen[c.value] = true
		a, _ := testApp(t, "")
		err := execute(a, "scan", "--category", c.value, "--json", t.TempDir())
		if err != nil {
			require.NotContains(t, err.Error(), "unknown category", c.value)
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			a, out := testApp(t, "")
			require.NoError(t, execute(a, "completion", shell))
			require.Contains(t, out.String(), "reclaim")
			require.Contains(t, out.String(), cobra.ShellCompRequestCmd)
		})
	}
}

func TestVersionString(t *testing.T) {
	info := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/mandloideep/reclaim", Version: v}}
	}
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{name: "GoReleaser stamps the tag without its v", stamped: "0.1.0", info: info("(devel)"), want: "v0.1.0"},
		{name: "a stamped tag keeps its v", stamped: "v1.2.3", info: info("(devel)"), want: "v1.2.3"},
		{name: "a GoReleaser snapshot", stamped: "0.1.1-SNAPSHOT-abc1234", want: "v0.1.1-SNAPSHOT-abc1234"},
		{name: "a stamped name that is not a number", stamped: "dev", want: "dev"},
		{name: "go install of a tag", info: info("v0.1.0"), want: "v0.1.0"},
		{name: "go install of a commit", info: info("v0.1.1-0.20261005120000-abcdef123456"), want: "v0.1.1-0.20261005120000-abcdef123456"},
		{name: "a build from a checkout", info: info("(devel)"), want: "(devel)"},
		{name: "build info without a version", info: info(""), want: "(devel)"},
		{name: "no build info", want: "(devel)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, versionString(tt.stamped, tt.info))
		})
	}
}

func TestVersionCommand(t *testing.T) {
	a, out := testApp(t, "")
	require.NoError(t, execute(a, "version"))
	require.True(t, strings.HasPrefix(out.String(), "reclaim "+buildVersion()+" go"), out.String())
}

func TestHelpExplainsConfigPrecedence(t *testing.T) {
	a, out := testApp(t, "")
	require.NoError(t, execute(a, "--help"))
	help := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"Settings come from the first of: the file named by --config, which must exist; $XDG_CONFIG_HOME/reclaim/config.toml when XDG_CONFIG_HOME is an absolute path; ~/.config/reclaim/config.toml.",
		"flags override the file",
		"--config string configuration file to read; it must exist and takes precedence over $XDG_CONFIG_HOME/reclaim/config.toml and ~/.config/reclaim/config.toml",
	} {
		require.Contains(t, help, want)
	}
}
