package fsx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteFileAtomic checks that the file is replaced whole, that nothing
// but it is left in its folder, and that a failed write removes only its
// own temporary file.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list")
	neighbor := filepath.Join(dir, "neighbor")
	require.NoError(t, os.WriteFile(neighbor, []byte("keep"), 0o644))
	require.NoError(t, WriteFileAtomic(path, []byte("one\n")))
	require.NoError(t, WriteFileAtomic(path, []byte("two\n")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "two\n", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "no temporary file is left")

	// A path whose name is taken by a folder cannot be replaced; the
	// folder, its contents and the neighbor stay.
	taken := filepath.Join(dir, "taken")
	require.NoError(t, os.MkdirAll(filepath.Join(taken, "inside"), 0o755))
	require.Error(t, WriteFileAtomic(taken, []byte("x")))
	require.DirExists(t, filepath.Join(taken, "inside"))
	require.FileExists(t, neighbor)
	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 3, "the failed write left nothing of its own")
}
