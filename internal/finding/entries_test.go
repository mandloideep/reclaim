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
		tags   []string
	}{
		{"container by id", "docker:c0ffee", true, "c0ffee", true, nil},
		{"image by its full id", "docker:sha256:4f2a9c1e7b3d5a6b", true, "sha256:4f2a9c1e7b3d5a6b", true, nil},
		{"image by 12 hex digits", "docker:4f2a9c1e7b3d", true, "sha256:4f2a9c1e7b3d5a6b", true, nil},
		{"too short a prefix", "docker:4f2a9c1e", true, "sha256:4f2a9c1e7b3d5a6b", false, nil},
		{"volume by name", "docker:pgdata", true, "pgdata", true, nil},
		{"another volume", "docker:pgdata", true, "pgdata2", false, nil},
		{"a Docker entry names no model", "docker:llama3", false, "llama3", false, nil},
		{"model with its tag", "ollama:llama3:8b", false, "ollama:llama3:8b", true, nil},
		{"model without a tag is latest", "ollama:llama3", false, "ollama:llama3:latest", true, nil},
		{"model without a tag is only latest", "ollama:llama3", false, "ollama:llama3:8b", false, nil},
		{"a model entry names no Docker object", "ollama:llama3", true, "ollama:llama3", false, nil},
		{"a path entry names no target", "/Users/me/x", false, "/Users/me/x", false, nil},
		{"an empty Docker entry", "docker:", true, "", false, nil},
		{"an id prefix in capitals", "docker:4F2A9C1E7B3D", true, "sha256:4f2a9c1e7b3d5a6b", true, nil},
		{"image by a tag", "docker:postgres:16", true, "sha256:aa", true, []string{"postgres:16"}},
		{"image by its latest tag", "docker:postgres", true, "sha256:aa", true, []string{"postgres:latest"}},
		{"another tag", "docker:postgres:15", true, "sha256:aa", false, []string{"postgres:16"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, MatchesTargetEntry(tc.entry, tc.docker, tc.target, tc.tags))
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
