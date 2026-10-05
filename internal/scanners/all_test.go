package scanners

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

func TestRegistry(t *testing.T) {
	r := New()
	all := r.All()
	names := map[string]bool{}
	counts := map[finding.Category]int{}
	for _, s := range all {
		require.False(t, names[s.Name()], "duplicate scanner name %s", s.Name())
		names[s.Name()] = true
		counts[s.Category()]++
		require.NotEmpty(t, s.Description(), s.Name())
		require.NotEmpty(t, s.Ecosystem(), s.Name())
	}
	require.Equal(t, 21, counts[finding.CategoryProject])
	require.Equal(t, 18, counts[finding.CategoryPackageCache])
	require.Equal(t, 1, counts[finding.CategoryDocker])
	require.Equal(t, 11, counts[finding.CategoryAppCache])
	require.Equal(t, 3, counts[finding.CategoryDownloads])
	for c := range counts {
		require.Contains(t, finding.Categories(), c)
	}
	require.Len(t, r.ProjectMatchers(), counts[finding.CategoryProject], "every project scanner prunes the shared walk")
	for _, m := range r.ProjectMatchers() {
		require.True(t, names[m.Name()])
	}
}
