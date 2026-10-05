package fsx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
}

func TestSize(t *testing.T) {
	tests := []struct {
		name      string
		build     func(t *testing.T, root string)
		wantSize  int64
		wantFiles int64
	}{
		{
			name:     "empty directory",
			build:    func(*testing.T, string) {},
			wantSize: 0,
		},
		{
			name: "nested files",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "a"), 100)
				writeFile(t, filepath.Join(root, "x", "b"), 200)
				writeFile(t, filepath.Join(root, "x", "y", "z", "c"), 300)
			},
			wantSize:  600,
			wantFiles: 3,
		},
		{
			name: "hard links count once",
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "orig"), 1000)
				require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
				require.NoError(t, os.Link(filepath.Join(root, "orig"), filepath.Join(root, "link1")))
				require.NoError(t, os.Link(filepath.Join(root, "orig"), filepath.Join(root, "sub", "link2")))
			},
			wantSize:  1000,
			wantFiles: 1,
		},
		{
			name: "symlinks are not followed",
			build: func(t *testing.T, root string) {
				outside := t.TempDir()
				writeFile(t, filepath.Join(outside, "big"), 1<<20)
				writeFile(t, filepath.Join(root, "small"), 10)
				require.NoError(t, os.Symlink(filepath.Join(outside, "big"), filepath.Join(root, "file-link")))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "dir-link")))
			},
			// The links count as their own apparent size, the length of their target.
			wantSize: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.build(t, root)
			res, err := NewWalker(4).Size(context.Background(), root)
			require.NoError(t, err)
			require.Empty(t, res.Unreadable)
			if tt.wantSize >= 0 {
				require.Equal(t, tt.wantSize, res.Size)
				require.Equal(t, tt.wantFiles, res.Files)
				return
			}
			require.Less(t, res.Size, int64(10+2*4096), "symlink targets must not be counted")
			require.GreaterOrEqual(t, res.Size, int64(10))
		})
	}
}

func TestSizeOfSymlinkRootMeasuresTheLink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "target", "big"), 1<<20)
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(filepath.Join(dir, "target"), link))

	res, err := NewWalker(2).Size(context.Background(), link)
	require.NoError(t, err)
	require.Less(t, res.Size, int64(4096))
}

func TestSizeMissingRoot(t *testing.T) {
	_, err := NewWalker(2).Size(context.Background(), filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.Contains(t, err.Error(), "missing")
}

func TestSizeUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced for this user")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ok", "f"), 50)
	locked := filepath.Join(root, "locked")
	writeFile(t, filepath.Join(locked, "hidden"), 5000)
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	res, err := NewWalker(2).Size(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, int64(50), res.Size)
	require.Len(t, res.Unreadable, 1)
	require.Equal(t, locked, res.Unreadable[0].Path)
	require.ErrorIs(t, res.Unreadable[0], fs.ErrPermission)
}

func TestSizeHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	for i := range 50 {
		writeFile(t, filepath.Join(root, strings.Repeat("d", i%5+1), "f"+string(rune('a'+i%26))), 10)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewWalker(2).Size(ctx, root)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSizeCancellationDuringWalk(t *testing.T) {
	root := t.TempDir()
	for i := range 200 {
		writeFile(t, filepath.Join(root, "d"+string(rune('a'+i%26)), "e"+string(rune('a'+i/26)), "f"), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := NewWalker(1)
	var calls atomic.Int32
	inner := w.statFn
	w.statFn = func(fi fs.FileInfo) (statInfo, bool) {
		if calls.Add(1) == 5 {
			cancel()
		}
		return inner(fi)
	}
	res, err := w.Size(ctx, root)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, res.Files, int64(200), "the walk must stop early")
}

func TestSizeDoesNotCrossFilesystems(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "local", "f"), 10)
	writeFile(t, filepath.Join(root, "mnt", "other", "f"), 999)

	w := NewWalker(2)
	inner := w.statFn
	w.statFn = func(fi fs.FileInfo) (statInfo, bool) {
		st, ok := inner(fi)
		if fi.Name() == "mnt" {
			st.dev++
		}
		return st, ok
	}
	res, err := w.Size(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, int64(10), res.Size)
	require.Equal(t, []string{filepath.Join(root, "mnt")}, res.Mounts)
}

func TestTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "big", "a"), 300)
	writeFile(t, filepath.Join(root, "big", "inner", "b"), 700)
	writeFile(t, filepath.Join(root, "small", "c"), 20)
	writeFile(t, filepath.Join(root, "file"), 5)

	t.Run("depth one", func(t *testing.T) {
		node, res, err := NewWalker(3).Tree(context.Background(), root, TreeOptions{Depth: 1})
		require.NoError(t, err)
		require.Equal(t, int64(1025), res.Size)
		require.Equal(t, int64(1025), node.Size)
		require.Len(t, node.Children, 3)
		require.Equal(t, "big", node.Children[0].Name)
		require.Equal(t, int64(1000), node.Children[0].Size)
		require.Empty(t, node.Children[0].Children)
		require.Equal(t, "small", node.Children[1].Name)
		require.Equal(t, int64(20), node.Children[1].Size)
		require.Equal(t, "file", node.Children[2].Name)
		require.False(t, node.Children[2].IsDir)
	})
	t.Run("depth two", func(t *testing.T) {
		node, _, err := NewWalker(3).Tree(context.Background(), root, TreeOptions{Depth: 2})
		require.NoError(t, err)
		big := node.Children[0]
		require.Len(t, big.Children, 2)
		require.Equal(t, "inner", big.Children[0].Name)
		require.Equal(t, int64(700), big.Children[0].Size)
		require.Equal(t, "a", big.Children[1].Name)
	})
	t.Run("directory sizes", func(t *testing.T) {
		node, res, err := NewWalker(3).Tree(context.Background(), root, TreeOptions{Depth: 1, DirSizes: true})
		require.NoError(t, err)
		require.Equal(t, map[string]int64{
			root:                                1025,
			filepath.Join(root, "big"):          1000,
			filepath.Join(root, "big", "inner"): 700,
			filepath.Join(root, "small"):        20,
		}, res.DirSizes)
		require.Empty(t, node.Children[0].Children, "deeper directories are measured but not attached")
	})
	t.Run("size only has no directory sizes", func(t *testing.T) {
		res, err := NewWalker(3).Size(context.Background(), root)
		require.NoError(t, err)
		require.Nil(t, res.DirSizes)
	})
}

func TestUnreadableUnwrap(t *testing.T) {
	u := Unreadable{Path: "/x", Err: fs.ErrPermission}
	require.True(t, errors.Is(u, fs.ErrPermission))
	require.Equal(t, "/x: permission denied", u.Error())
}
