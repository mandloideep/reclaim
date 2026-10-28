package finding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchesTargetEntry(t *testing.T) {
	tests := []struct {
		name   string
		entry  string
		docker bool
		target string
		want   bool
	}{
		{"container by id", "docker:c0ffee", true, "c0ffee", true},
		{"image by its full id", "docker:sha256:4f2a9c1e7b3d5a6b", true, "sha256:4f2a9c1e7b3d5a6b", true},
		{"image by 12 hex digits", "docker:4f2a9c1e7b3d", true, "sha256:4f2a9c1e7b3d5a6b", true},
		{"too short a prefix", "docker:4f2a9c1e", true, "sha256:4f2a9c1e7b3d5a6b", false},
		{"volume by name", "docker:pgdata", true, "pgdata", true},
		{"another volume", "docker:pgdata", true, "pgdata2", false},
		{"a Docker entry names no model", "docker:llama3", false, "llama3", false},
		{"model with its tag", "ollama:llama3:8b", false, "ollama:llama3:8b", true},
		{"model without a tag is latest", "ollama:llama3", false, "ollama:llama3:latest", true},
		{"model without a tag is only latest", "ollama:llama3", false, "ollama:llama3:8b", false},
		{"a model entry names no Docker object", "ollama:llama3", true, "ollama:llama3", false},
		{"a path entry names no target", "/Users/me/x", false, "/Users/me/x", false},
		{"an empty Docker entry", "docker:", true, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, MatchesTargetEntry(tc.entry, tc.docker, tc.target))
		})
	}
	require.True(t, IsTargetEntry("docker:x"))
	require.True(t, IsTargetEntry("ollama:x"))
	require.False(t, IsTargetEntry("/docker:x"))
}

func TestEntryFor(t *testing.T) {
	tests := []struct {
		name string
		f    Finding
		want string
		ok   bool
	}{
		{"path", Finding{Category: CategoryProject, Path: "/c/web/dist", Target: "/c/web/dist"}, "/c/web/dist", true},
		{"Docker object", Finding{Category: CategoryDocker, Target: "sha256:abc"}, "docker:sha256:abc", true},
		{"Ollama model, not its shared folder", Finding{Category: CategoryAppCache, Path: "/h/.ollama/models", Target: "ollama:llama3:latest"},
			"ollama:llama3:latest", true},
		{"stale registrations", Finding{Category: CategoryWorktree, Target: "git-worktree-prune:/c/repo"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := EntryFor(&tc.f)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.ok, ok)
		})
	}
}
