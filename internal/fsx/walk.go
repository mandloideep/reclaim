// Package fsx measures directory trees quickly and safely.
//
// The walker counts the apparent size of files, counts a file with several
// hard links once, never follows symbolic links, never crosses into another
// filesystem and records unreadable paths instead of failing. Directories are
// read concurrently with a bound shared by every walk started from the same
// Walker.
package fsx

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// statInfo is the subset of stat data the walker needs.
type statInfo struct {
	dev   uint64
	ino   uint64
	nlink uint64
}

// Unreadable records a path the walker could not read.
type Unreadable struct {
	// Path is the file or directory that could not be read.
	Path string
	// Err is the underlying error.
	Err error
}

// Error implements error.
func (u Unreadable) Error() string { return u.Path + ": " + u.Err.Error() }

// Unwrap returns the underlying error.
func (u Unreadable) Unwrap() error { return u.Err }

// Result is the outcome of measuring one tree.
type Result struct {
	// Size is the apparent size in bytes of every file in the tree, with hard
	// linked files counted once. Directory entries themselves are not counted.
	Size int64
	// Files is the number of non directory entries counted.
	Files int64
	// Dirs is the number of directories visited below the root.
	Dirs int64
	// Unreadable lists paths that could not be read. Their contents are not counted.
	Unreadable []Unreadable
	// Mounts lists directories that were skipped because they are on another filesystem.
	Mounts []string
	// DirSizes maps every directory in the tree, the root included, to its
	// size. It is only filled when TreeOptions.DirSizes is set.
	DirSizes map[string]int64
}

// TreeOptions configure Walker.Tree.
type TreeOptions struct {
	// Depth is how many levels of Node.Children to fill. One returns the
	// direct children of the root.
	Depth int
	// DirSizes records the size of every directory in Result.DirSizes, so
	// callers can look up any subtree without walking it again.
	DirSizes bool
}

// Node is one entry of a size tree built by Walker.Tree.
type Node struct {
	// Name is the base name of the entry.
	Name string
	// Path is the full path of the entry.
	Path string
	// IsDir reports whether the entry is a directory.
	IsDir bool
	// Size is the apparent size of the entry, including everything below it.
	Size int64
	// Children are the entries below a directory, sorted by size descending.
	// They are only filled down to the depth requested from Tree.
	Children []*Node

	acc atomic.Int64
	mu  sync.Mutex
}

func (n *Node) addChild(c *Node) {
	n.mu.Lock()
	n.Children = append(n.Children, c)
	n.mu.Unlock()
}

func (n *Node) finalize() {
	n.Size = n.acc.Load()
	for _, c := range n.Children {
		c.finalize()
	}
	slices.SortFunc(n.Children, func(a, b *Node) int {
		if a.Size != b.Size {
			return cmp.Compare(b.Size, a.Size)
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// Walker measures directory trees. It is safe for concurrent use. All walks
// started from one Walker share its concurrency bound.
type Walker struct {
	sem    chan struct{}
	statFn func(fs.FileInfo) (statInfo, bool)
}

// NewWalker returns a Walker that reads at most concurrency directories at the
// same time across every walk started from it. A value below one selects a
// default: the number of CPUs, between 4 and 8. Measurements on APFS show
// that more parallel readers add kernel contention instead of speed.
func NewWalker(concurrency int) *Walker {
	if concurrency < 1 {
		concurrency = min(max(runtime.GOMAXPROCS(0), 4), 8)
	}
	return &Walker{sem: make(chan struct{}, concurrency), statFn: statOf}
}

// Size measures the tree rooted at root. If root is a symbolic link, the link
// itself is measured, not its target. The error is non nil only when root
// cannot be inspected at all or ctx is done; unreadable paths below root are
// reported in the result.
func (w *Walker) Size(ctx context.Context, root string) (Result, error) {
	_, res, err := w.walk(ctx, root, TreeOptions{})
	return res, err
}

// Tree measures the tree rooted at root like Size and also returns the
// entries down to opts.Depth levels below root with their sizes.
func (w *Walker) Tree(ctx context.Context, root string, opts TreeOptions) (*Node, Result, error) {
	opts.Depth = max(opts.Depth, 0)
	return w.walk(ctx, root, opts)
}

type walk struct {
	ctx      context.Context
	w        *Walker
	maxDepth int
	dirSizes bool
	rootDev  uint64
	haveDev  bool

	size  atomic.Int64
	files atomic.Int64
	dirs  atomic.Int64
	wg    sync.WaitGroup

	mu         sync.Mutex
	seen       map[[2]uint64]struct{}
	unreadable []Unreadable
	mounts     []string
	all        []*Node
}

func (w *Walker) walk(ctx context.Context, root string, opts TreeOptions) (*Node, Result, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, Result{}, fmt.Errorf("measure %s: %w", root, err)
	}
	node := &Node{Name: filepath.Base(root), Path: root, IsDir: info.IsDir()}
	if !info.IsDir() {
		node.Size = info.Size()
		return node, Result{Size: info.Size(), Files: 1}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, Result{}, fmt.Errorf("measure %s: %w", root, err)
	}
	// The root of every walk takes a slot too, so concurrent walks share the
	// bound instead of each adding a reader.
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, Result{}, fmt.Errorf("measure %s: %w", root, ctx.Err())
	}
	defer func() { <-w.sem }()
	wk := &walk{ctx: ctx, w: w, maxDepth: opts.Depth, dirSizes: opts.DirSizes, seen: map[[2]uint64]struct{}{}}
	if st, ok := w.statFn(info); ok {
		wk.rootDev, wk.haveDev = st.dev, true
	}
	var chain []*Node
	if opts.Depth > 0 || opts.DirSizes {
		chain = []*Node{node}
	}
	wk.dir(root, chain, 0)
	wk.wg.Wait()

	res := Result{
		Size:       wk.size.Load(),
		Files:      wk.files.Load(),
		Dirs:       wk.dirs.Load(),
		Unreadable: wk.unreadable,
		Mounts:     wk.mounts,
	}
	slices.SortFunc(res.Unreadable, func(a, b Unreadable) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(res.Mounts)
	node.acc.Store(res.Size)
	node.finalize()
	if opts.DirSizes {
		res.DirSizes = make(map[string]int64, len(wk.all)+1)
		res.DirSizes[root] = res.Size
		for _, n := range wk.all {
			res.DirSizes[n.Path] = n.acc.Load()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, res, fmt.Errorf("measure %s: %w", root, err)
	}
	return node, res, nil
}

// dir reads one directory. chain holds the tree nodes that the sizes found
// here contribute to, from the root down to this directory. It is empty when
// neither a tree nor directory sizes are being built.
func (wk *walk) dir(path string, chain []*Node, depth int) {
	if wk.ctx.Err() != nil {
		return
	}
	entries, err := readDir(path)
	if err != nil {
		wk.addUnreadable(path, err)
		// Entries read before the error are still counted below.
	}
	withNodes := depth < wk.maxDepth
	var local, files int64
	for _, e := range entries {
		if wk.ctx.Err() != nil {
			break
		}
		child := filepath.Join(path, e.Name())
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				wk.addUnreadable(child, err)
			}
			continue
		}
		if info.IsDir() {
			if st, ok := wk.w.statFn(info); ok && wk.haveDev && st.dev != wk.rootDev {
				wk.addMount(child)
				continue
			}
			wk.dirs.Add(1)
			childChain := chain
			if withNodes || wk.dirSizes {
				n := &Node{Name: e.Name(), Path: child, IsDir: true}
				if withNodes {
					chain[len(chain)-1].addChild(n)
				}
				if wk.dirSizes {
					wk.record(n)
				}
				childChain = append(slices.Clip(chain), n)
			}
			wk.spawn(child, childChain, depth+1)
			continue
		}
		if info.Mode().IsRegular() {
			if st, ok := wk.w.statFn(info); ok && st.nlink > 1 && !wk.firstSighting(st) {
				continue
			}
		}
		size := info.Size()
		local += size
		files++
		if withNodes {
			n := &Node{Name: e.Name(), Path: child}
			n.acc.Store(size)
			chain[len(chain)-1].addChild(n)
		}
	}
	wk.files.Add(files)
	wk.size.Add(local)
	for _, n := range chain {
		n.acc.Add(local)
	}
}

// spawn walks a subdirectory on a new goroutine when a slot is free and on the
// current goroutine otherwise, so the number of goroutines stays bounded and
// no walk ever blocks waiting for a slot.
func (wk *walk) spawn(path string, chain []*Node, depth int) {
	select {
	case wk.w.sem <- struct{}{}:
		wk.wg.Add(1)
		go func() {
			defer func() {
				<-wk.w.sem
				wk.wg.Done()
			}()
			wk.dir(path, chain, depth)
		}()
	default:
		wk.dir(path, chain, depth)
	}
}

func (wk *walk) firstSighting(st statInfo) bool {
	key := [2]uint64{st.dev, st.ino}
	wk.mu.Lock()
	defer wk.mu.Unlock()
	if _, ok := wk.seen[key]; ok {
		return false
	}
	wk.seen[key] = struct{}{}
	return true
}

func (wk *walk) addUnreadable(path string, err error) {
	wk.mu.Lock()
	wk.unreadable = append(wk.unreadable, Unreadable{Path: path, Err: err})
	wk.mu.Unlock()
}

func (wk *walk) record(n *Node) {
	wk.mu.Lock()
	wk.all = append(wk.all, n)
	wk.mu.Unlock()
}

func (wk *walk) addMount(path string) {
	wk.mu.Lock()
	wk.mounts = append(wk.mounts, path)
	wk.mu.Unlock()
}

// DeviceOf returns the id of the filesystem holding the file described by fi,
// which must come from os.Lstat or DirEntry.Info. The boolean is false on
// platforms where the id is not available.
func DeviceOf(fi fs.FileInfo) (uint64, bool) {
	st, ok := statOf(fi)
	return st.dev, ok
}

// ReadDir returns the entries of a directory in directory order, which is
// cheaper than the sorted order of os.ReadDir. It refuses to follow a symbolic
// link at path.
func ReadDir(path string) ([]fs.DirEntry, error) {
	return readDir(path)
}

// readDir returns the entries of a directory in directory order, which is
// cheaper than the sorted order of os.ReadDir.
func readDir(path string) ([]fs.DirEntry, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openDirFlags, 0)
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return entries, err
}
