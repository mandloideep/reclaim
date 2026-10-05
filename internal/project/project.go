// Package project finds projects and their artifact folders under a set of
// roots in a single concurrent walk.
//
// A project is a directory containing a marker file such as .git or
// package.json. Artifact folders are recognized by Matchers supplied by the
// caller. The walk never descends into an artifact folder, so a package.json
// inside node_modules never makes a project, and it never descends into .git,
// across a symbolic link or into another filesystem.
package project

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mandloideep/reclaim/internal/fsx"
)

// ManifestNames are the file and directory names that make a directory a
// project. A directory holding any of them is never an artifact folder.
func ManifestNames() []string { return markerNames() }

// markerNames are the file and directory names that make a directory a project.
func markerNames() []string {
	return []string{
		".git", "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "requirements.txt",
		"pom.xml", "build.gradle", "build.gradle.kts", "Package.swift", "pubspec.yaml",
		"composer.json", "Gemfile", "mix.exs",
	}
}

// markerSuffixes are name suffixes that make a directory a project.
func markerSuffixes() []string { return []string{".xcodeproj"} }

// skipNames are directories the walk never enters.
func skipNames() []string { return []string{".git", ".hg", ".svn", ".jj"} }

// Dir is a directory the walk has read. Matchers inspect it to decide whether
// it is an artifact folder.
type Dir struct {
	// Path is the full path of the directory.
	Path string
	// Name is the base name of the directory.
	Name string
	// Parent is the directory containing this one, nil for a walk root.
	Parent *Dir
	// Project is the project that owns this directory, nil when there is none.
	// For an artifact folder it is the project the folder belongs to.
	Project *Project

	entries map[string]fs.FileMode
}

// NewDir reads path and returns it as a Dir with the given parent. It is used
// by tests and by callers that inspect a single directory.
func NewDir(path string, parent *Dir) (*Dir, error) {
	entries, err := fsx.ReadDir(path)
	if err != nil {
		return nil, err
	}
	return newDir(path, parent, entries), nil
}

func newDir(path string, parent *Dir, entries []fs.DirEntry) *Dir {
	d := &Dir{Path: path, Name: filepath.Base(path), Parent: parent, entries: make(map[string]fs.FileMode, len(entries))}
	for _, e := range entries {
		d.entries[e.Name()] = e.Type()
	}
	return d
}

// Has reports whether the directory has an entry with this name.
func (d *Dir) Has(name string) bool {
	if d == nil {
		return false
	}
	_, ok := d.entries[name]
	return ok
}

// HasFile reports whether the directory has a regular file with this name.
func (d *Dir) HasFile(name string) bool {
	if d == nil {
		return false
	}
	m, ok := d.entries[name]
	return ok && m.IsRegular()
}

// HasDir reports whether the directory has a subdirectory with this name.
func (d *Dir) HasDir(name string) bool {
	if d == nil {
		return false
	}
	m, ok := d.entries[name]
	return ok && m.IsDir()
}

// Names returns the names of the entries in the directory, sorted.
func (d *Dir) Names() []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.entries))
	for name := range d.entries {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// HasAny reports whether the directory has an entry with any of the names.
func (d *Dir) HasAny(names ...string) bool {
	return slices.ContainsFunc(names, d.Has)
}

// HasPrefix reports whether any entry name starts with prefix, for patterns
// such as "vite.config." that cover several extensions.
func (d *Dir) HasPrefix(prefix string) bool {
	if d == nil {
		return false
	}
	for name := range d.entries {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// HasSuffix reports whether any entry name ends with suffix.
func (d *Dir) HasSuffix(suffix string) bool {
	if d == nil {
		return false
	}
	for name := range d.entries {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// ReadFile reads at most limit bytes of a regular file in the directory.
func (d *Dir) ReadFile(name string, limit int64) ([]byte, error) {
	if !d.HasFile(name) {
		return nil, fs.ErrNotExist
	}
	f, err := os.Open(filepath.Join(d.Path, name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// Markers returns the project markers present in the directory.
func (d *Dir) Markers() []string {
	if d == nil {
		return nil
	}
	var out []string
	for _, m := range markerNames() {
		if d.Has(m) {
			out = append(out, m)
		}
	}
	for name := range d.entries {
		for _, suf := range markerSuffixes() {
			if strings.HasSuffix(name, suf) {
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// HasManifest reports whether the directory holds one of ManifestNames. Unlike
// IsProject it ignores suffix markers such as App.xcodeproj, which generated
// folders like Pods contain.
func (d *Dir) HasManifest() bool { return d.HasAny(markerNames()...) }

// IsProject reports whether the directory contains a project marker.
func (d *Dir) IsProject() bool { return len(d.Markers()) > 0 }

// Project is a directory with a project marker.
type Project struct {
	// Root is the project directory.
	Root string
	// Markers are the markers found in Root.
	Markers []string
	// Git reports whether Root is a git working tree root.
	Git bool

	mu         sync.Mutex
	newestFile time.Time
	commitTime time.Time
}

// NewestFile is the newest modification time of a source file in the project,
// artifact folders excluded.
func (p *Project) NewestFile() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.newestFile
}

// CommitTime is the time of the last git commit, zero when unknown.
func (p *Project) CommitTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.commitTime
}

// LastActivity is the newest of the last commit time and the newest source
// file modification time.
func (p *Project) LastActivity() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.commitTime.After(p.newestFile) {
		return p.commitTime
	}
	return p.newestFile
}

func (p *Project) observe(t time.Time) {
	if t.IsZero() {
		return
	}
	p.mu.Lock()
	if t.After(p.newestFile) {
		p.newestFile = t
	}
	p.mu.Unlock()
}

func (p *Project) setCommitTime(t time.Time) {
	p.mu.Lock()
	p.commitTime = t
	p.mu.Unlock()
}

// Matcher recognizes artifact folders.
type Matcher interface {
	// Name identifies the matcher. Artifacts record the name of the matcher
	// that recognized them.
	Name() string
	// Match reports whether d is an artifact folder. It must not modify anything.
	Match(d *Dir) bool
}

// Artifact is an artifact folder found by the walk.
type Artifact struct {
	// Dir is the artifact folder with its entries.
	Dir *Dir
	// Matcher is the name of the matcher that recognized it.
	Matcher string
	// Project is the project it belongs to, nil when there is none.
	Project *Project
	// ModTime is the modification time of the folder itself.
	ModTime time.Time
}

// Index is the result of a walk.
type Index struct {
	// Roots are the walked roots after normalization.
	Roots []string
	// Projects are every project that owns at least one walked directory.
	Projects []*Project
	// Artifacts are the artifact folders found, sorted by path.
	Artifacts []Artifact
	// Unreadable lists directories the walk could not read.
	Unreadable []fsx.Unreadable
	// InsideArtifact maps roots that are themselves artifact folders, or lie
	// inside one, to that folder. Such roots are not walked, because nothing
	// inside an artifact folder is a project or an artifact of its own.
	InsideArtifact map[string]string
}

// ArtifactsFor returns the artifacts recognized by the named matcher.
func (ix *Index) ArtifactsFor(matcher string) []Artifact {
	var out []Artifact
	for _, a := range ix.Artifacts {
		if a.Matcher == matcher {
			out = append(out, a)
		}
	}
	return out
}

// Options tune a walk.
type Options struct {
	// Depth limits how many levels below each root the walk descends. Zero
	// means no limit.
	Depth int
	// Concurrency bounds the number of directories read at the same time.
	// Zero selects the number of CPUs, between 4 and 8.
	Concurrency int
	// Home is the user's home directory. When a root is inside a project, the
	// walk looks for that project in the root's ancestors, but never at Home or
	// above, so a dotfiles repository in the home directory does not swallow
	// every project.
	Home string
	// CommitTime returns the time of the last commit of a git project. When
	// nil, last activity uses file modification times only.
	CommitTime func(ctx context.Context, root string) (time.Time, error)
	// Exclude lists clean absolute paths the walk never enters. A root that
	// is excluded, or lies inside an excluded path, is not walked at all.
	Exclude []string
}

func (o *Options) excluded(path string) bool {
	return slices.ContainsFunc(o.Exclude, func(ex string) bool { return IsWithin(path, ex) })
}

// NormalizeRoots cleans the roots, drops duplicates and drops roots nested
// inside another root, so no directory is walked twice.
func NormalizeRoots(roots []string) []string {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		clean = append(clean, filepath.Clean(r))
	}
	slices.Sort(clean)
	clean = slices.Compact(clean)
	out := clean[:0]
	for _, r := range clean {
		if !slices.ContainsFunc(out, func(parent string) bool { return IsWithin(r, parent) }) {
			out = append(out, r)
		}
	}
	return out
}

// IsWithin reports whether path is parent or lies below it. Both must be clean.
func IsWithin(path, parent string) bool {
	if path == parent {
		return true
	}
	if parent == string(filepath.Separator) {
		return strings.HasPrefix(path, parent)
	}
	return strings.HasPrefix(path, parent+string(filepath.Separator))
}

// Discover walks the roots and returns the projects and artifact folders
// found. The error is non nil only when ctx is done; unreadable directories
// are reported in the index.
func Discover(ctx context.Context, roots []string, matchers []Matcher, opts Options) (*Index, error) {
	conc := opts.Concurrency
	if conc < 1 {
		conc = min(max(runtime.GOMAXPROCS(0), 4), 8)
	}
	ds := &discovery{
		ctx:      ctx,
		matchers: matchers,
		opts:     opts,
		sem:      make(chan struct{}, conc),
		projects: map[string]*Project{},
	}
	idx := &Index{Roots: NormalizeRoots(roots)}
	for _, root := range idx.Roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.excluded(root) {
			continue
		}
		info, err := os.Lstat(root)
		if err != nil {
			ds.addUnreadable(root, err)
			continue
		}
		if !info.IsDir() {
			continue
		}
		if artifact, ok := ds.insideArtifact(root); ok {
			if idx.InsideArtifact == nil {
				idx.InsideArtifact = map[string]string{}
			}
			idx.InsideArtifact[root] = artifact
			continue
		}
		dev, haveDev := fsx.DeviceOf(info)
		owner := ds.enclosingProject(root)
		ds.visit(task{path: root, owner: owner, dev: dev, haveDev: haveDev, root: true})
	}
	ds.wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ds.resolveCommitTimes()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	idx.Artifacts = ds.artifacts
	slices.SortFunc(idx.Artifacts, func(a, b Artifact) int { return strings.Compare(a.Dir.Path, b.Dir.Path) })
	for _, p := range ds.projects {
		idx.Projects = append(idx.Projects, p)
	}
	slices.SortFunc(idx.Projects, func(a, b *Project) int { return strings.Compare(a.Root, b.Root) })
	idx.Unreadable = ds.unreadable
	slices.SortFunc(idx.Unreadable, func(a, b fsx.Unreadable) int { return strings.Compare(a.Path, b.Path) })
	return idx, nil
}

type task struct {
	path    string
	parent  *Dir
	owner   *Project
	depth   int
	dev     uint64
	haveDev bool
	root    bool
}

type discovery struct {
	ctx      context.Context
	matchers []Matcher
	opts     Options
	sem      chan struct{}
	wg       sync.WaitGroup

	mu         sync.Mutex
	projects   map[string]*Project
	artifacts  []Artifact
	unreadable []fsx.Unreadable
}

func (ds *discovery) visit(t task) {
	if ds.ctx.Err() != nil {
		return
	}
	entries, err := fsx.ReadDir(t.path)
	if err != nil {
		ds.addUnreadable(t.path, err)
		return
	}
	d := newDir(t.path, t.parent, entries)
	d.Project = t.owner

	// A directory holding a project manifest or a git working tree is never
	// an artifact, even when its name or contents say so: a dist folder
	// checked out as a worktree, or a project that ran "python -m venv ." in
	// its own root, must never be offered for removal.
	if !t.root && !d.HasManifest() {
		for _, m := range ds.matchers {
			if m.Match(d) {
				ds.addArtifact(d, m.Name(), t)
				return
			}
		}
	}

	owner := t.owner
	if markers := d.Markers(); len(markers) > 0 {
		owner = ds.ownerFor(d, markers, owner)
		d.Project = owner
	}

	var newest time.Time
	for _, e := range entries {
		if ds.ctx.Err() != nil {
			return
		}
		name := e.Name()
		switch {
		case e.IsDir():
			if slices.Contains(skipNames(), name) {
				continue
			}
			if ds.opts.Depth > 0 && t.depth+1 > ds.opts.Depth {
				continue
			}
			child := filepath.Join(t.path, name)
			if ds.opts.excluded(child) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					ds.addUnreadable(child, err)
				}
				continue
			}
			if dev, ok := fsx.DeviceOf(info); ok && t.haveDev && dev != t.dev {
				continue
			}
			ds.spawn(task{path: child, parent: d, owner: owner, depth: t.depth + 1, dev: t.dev, haveDev: t.haveDev})
		case e.Type().IsRegular() && owner != nil:
			if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
	}
	if owner != nil {
		owner.observe(newest)
	}
}

func (ds *discovery) spawn(t task) {
	select {
	case ds.sem <- struct{}{}:
		ds.wg.Add(1)
		go func() {
			defer func() {
				<-ds.sem
				ds.wg.Done()
			}()
			ds.visit(t)
		}()
	default:
		ds.visit(t)
	}
}

// ownerFor decides which project owns a directory with markers. A git working
// tree root always starts a project. Inside a git project, nested manifests
// such as the packages of a monorepo stay with the repository. Outside git, the
// nearest directory with a marker owns its contents.
func (ds *discovery) ownerFor(d *Dir, markers []string, current *Project) *Project {
	isGit := d.Has(".git")
	if !isGit && current != nil && current.Git {
		return current
	}
	return ds.project(d.Path, markers, isGit)
}

func (ds *discovery) project(root string, markers []string, git bool) *Project {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if p, ok := ds.projects[root]; ok {
		return p
	}
	p := &Project{Root: root, Markers: markers, Git: git}
	ds.projects[root] = p
	return p
}

// insideArtifact reports whether root is an artifact folder or lies inside
// one, by running the matchers over root and its ancestors below the home
// directory, top down so every matcher sees the parent it expects.
func (ds *discovery) insideArtifact(root string) (string, bool) {
	var chain []string
	for dir := root; ; dir = filepath.Dir(dir) {
		if ds.opts.Home != "" && IsWithin(ds.opts.Home, dir) {
			break
		}
		chain = append(chain, dir)
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if len(chain) == 0 {
		return "", false
	}
	parent, err := NewDir(filepath.Dir(chain[len(chain)-1]), nil)
	if err != nil {
		parent = nil
	}
	for _, path := range slices.Backward(chain) {
		d, err := NewDir(path, parent)
		if err != nil {
			return "", false
		}
		if !d.HasManifest() {
			for _, m := range ds.matchers {
				if m.Match(d) {
					return d.Path, true
				}
			}
		}
		parent = d
	}
	return "", false
}

// enclosingProject finds the project a root lies in by looking at its
// ancestors, stopping below the home directory.
func (ds *discovery) enclosingProject(root string) *Project {
	var nearest *Dir
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if ds.opts.Home != "" && IsWithin(ds.opts.Home, dir) {
			break
		}
		if dir == filepath.Dir(dir) {
			break
		}
		d, err := NewDir(dir, nil)
		if err != nil {
			break
		}
		if d.Has(".git") {
			return ds.project(d.Path, d.Markers(), true)
		}
		if nearest == nil && d.IsProject() {
			nearest = d
		}
	}
	if nearest != nil {
		return ds.project(nearest.Path, nearest.Markers(), false)
	}
	return nil
}

func (ds *discovery) addArtifact(d *Dir, matcher string, t task) {
	a := Artifact{Dir: d, Matcher: matcher, Project: t.owner}
	if info, err := os.Lstat(d.Path); err == nil {
		a.ModTime = info.ModTime()
	}
	ds.mu.Lock()
	ds.artifacts = append(ds.artifacts, a)
	ds.mu.Unlock()
}

func (ds *discovery) addUnreadable(path string, err error) {
	ds.mu.Lock()
	ds.unreadable = append(ds.unreadable, fsx.Unreadable{Path: path, Err: err})
	ds.mu.Unlock()
}

func (ds *discovery) resolveCommitTimes() {
	if ds.opts.CommitTime == nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, p := range ds.projects {
		if !p.Git {
			continue
		}
		if ds.ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()
			if t, err := ds.opts.CommitTime(ds.ctx, p.Root); err == nil {
				p.setCommitTime(t)
			}
		}()
	}
	wg.Wait()
}

// Source runs Discover once, on first use, and shares the result between the
// project scanners of one scan.
type Source struct {
	roots    []string
	matchers []Matcher
	opts     Options

	once sync.Once
	idx  *Index
	err  error
}

// NewSource returns a Source that discovers projects under roots.
func NewSource(roots []string, matchers []Matcher, opts Options) *Source {
	return &Source{roots: roots, matchers: matchers, opts: opts}
}

// Index runs the walk on the first call and returns its result on every call.
func (s *Source) Index(ctx context.Context) (*Index, error) {
	s.once.Do(func() {
		s.idx, s.err = Discover(ctx, s.roots, s.matchers, s.opts)
	})
	return s.idx, s.err
}
