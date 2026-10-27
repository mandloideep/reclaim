// Package worktree discovers linked git worktrees and classifies them: is
// every change committed, every commit pushed, and every commit in the
// default branch. The worktree scanners, reclaim done and apply all use it,
// so they reach the same answer from the same code.
//
// Every answer comes from read only git commands run through execx.Runner.
// Nothing here fetches, pushes, or writes a ref or a file in a worktree. The
// squash merge check writes one dangling commit object, which git gc
// collects.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/fsx"
)

// Env carries what classification needs.
type Env struct {
	// Exec runs git, ps and gh.
	Exec execx.Runner
	// CodexHome is $CODEX_HOME or ~/.codex, for telling Codex worktrees
	// apart. Empty means none.
	CodexHome string
	// Walker walks a worktree for the repositories inside it. Nil uses a
	// walker of its own.
	Walker *fsx.Walker
}

// ErrNotLinked means a path is not a linked worktree: it does not exist, is
// a main checkout, is not in a git repository, or git does not list it.
var ErrNotLinked = errors.New("not a linked worktree")

// Entry is one worktree as "git worktree list --porcelain" reports it.
type Entry struct {
	// Path is the worktree folder as git recorded it.
	Path string
	// Head is the commit HEAD points at.
	Head string
	// Branch is the checked out branch without refs/heads/.
	Branch string
	// Detached is set when HEAD is not on a branch.
	Detached bool
	// Bare is set for the main entry of a bare repository.
	Bare bool
	// Main is set for the first entry, the main worktree.
	Main bool
	// Locked is set when the worktree is locked.
	Locked bool
	// LockReason is the reason given when it was locked.
	LockReason string
	// Prunable is set when the folder is gone and git would prune the
	// registration.
	Prunable bool
	// PrunableReason is git's explanation.
	PrunableReason string
}

// git runs a read only git command in dir.
func (e *Env) git(ctx context.Context, dir string, args ...string) (string, error) {
	if e.Exec == nil {
		return "", fmt.Errorf("git -C %s %s: %w", dir, strings.Join(args, " "), execx.ErrNotInstalled)
	}
	return e.Exec.Output(ctx, "git", append([]string{"-C", dir}, args...)...)
}

// List returns every worktree of the repository at repo, the main one first,
// with locks, their reasons and registrations whose folder is gone.
func List(ctx context.Context, env Env, repo string) ([]Entry, error) {
	out, err := env.git(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("list worktrees of %s: %w", repo, err)
	}
	return parseList(out), nil
}

// parseList parses the -z porcelain output: attributes end with a NUL and
// records end with an extra NUL.
func parseList(out string) []Entry {
	var entries []Entry
	var cur *Entry
	for field := range strings.SplitSeq(out, "\x00") {
		field = strings.TrimRight(field, "\n")
		if field == "" {
			cur = nil
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			entries = append(entries, Entry{Path: filepath.Clean(value), Main: len(entries) == 0})
			cur = &entries[len(entries)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.Head = value
		case "branch":
			cur.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked, cur.LockReason = true, value
		case "prunable":
			cur.Prunable, cur.PrunableReason = true, value
		}
	}
	return entries
}

// Location is where a linked worktree's registration lives.
type Location struct {
	// Repo is the main repository, the folder holding the .git directory.
	Repo string
	// GitDir is the worktree's registration, <repo>/.git/worktrees/<name>.
	GitDir string
	// CommonDir is the repository's .git directory.
	CommonDir string
}

// Locate reads the .git file of a linked worktree at path and returns its
// registration and main repository, without running git. A folder holding a
// .git directory is a main checkout, never a linked worktree.
func Locate(path string) (Location, error) {
	dotgit := filepath.Join(path, ".git")
	info, err := os.Lstat(dotgit)
	switch {
	case err != nil:
		return Location{}, fmt.Errorf("%s has no .git file: %w", path, ErrNotLinked)
	case info.IsDir():
		return Location{}, fmt.Errorf("%s holds a .git directory, so it is a main checkout: %w", path, ErrNotLinked)
	case !info.Mode().IsRegular():
		return Location{}, fmt.Errorf("%s/.git is not a regular file: %w", path, ErrNotLinked)
	}
	data, err := readSmall(dotgit)
	if err != nil {
		return Location{}, fmt.Errorf("read %s: %w", dotgit, err)
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(data), "gitdir:")
	if !ok {
		return Location{}, fmt.Errorf("%s does not name a gitdir: %w", dotgit, ErrNotLinked)
	}
	gitdir := absFrom(path, strings.TrimSpace(rest))
	common := filepath.Join(gitdir, "..", "..")
	if c, err := readSmall(filepath.Join(gitdir, "commondir")); err == nil {
		common = absFrom(gitdir, strings.TrimSpace(c))
	}
	common = resolve(filepath.Clean(common))
	gitdir = resolve(gitdir)
	if filepath.Base(common) != ".git" {
		return Location{}, fmt.Errorf("%s belongs to the bare repository %s, which reclaim does not handle: %w", path, common, ErrNotLinked)
	}
	if filepath.Dir(gitdir) != filepath.Join(common, "worktrees") {
		return Location{}, fmt.Errorf("%s names %s, which is not under %s: %w", dotgit, gitdir, filepath.Join(common, "worktrees"), ErrNotLinked)
	}
	return Location{Repo: filepath.Dir(common), GitDir: gitdir, CommonDir: common}, nil
}

// FindRoot returns the folder at or above path that holds a .git entry, so a
// command run in a subfolder of a worktree finds the worktree.
func FindRoot(path string) (string, bool) {
	for dir := path; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		if dir == filepath.Dir(dir) {
			return "", false
		}
	}
}

func absFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// resolve returns path with symbolic links resolved, or path itself when it
// cannot be resolved, such as a folder that is gone.
func resolve(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// readSmall reads a small metadata file such as .git or commondir.
func readSmall(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

// PruneCandidate is a registration that git worktree prune would remove.
type PruneCandidate struct {
	// Name is the registration's folder under .git/worktrees.
	Name string
	// Path is the worktree folder the registration names, or the
	// registration itself when its gitdir file cannot be read.
	Path string
	// Reason is git's explanation, such as "gitdir file points to
	// non-existent location".
	Reason string
	// Gone is set when the registration names a folder that does not exist,
	// the only case in which pruning it loses nothing.
	Gone bool
}

// dryRunTimeout bounds git worktree prune --dry-run.
const dryRunTimeout = 10 * time.Second

// PruneCandidates asks git which registrations git worktree prune would
// remove, with --dry-run, which changes nothing. git worktree list leaves
// out some of them, such as a registration whose gitdir file is missing
// while its folder, with its work, still exists, so only the dry run says
// what a prune would do. The dry run reports on standard error, which the
// runner's Run returns with standard output.
func PruneCandidates(ctx context.Context, env Env, repo string) ([]PruneCandidate, error) {
	if env.Exec == nil {
		return nil, fmt.Errorf("git worktree prune --dry-run in %s: %w", repo, execx.ErrNotInstalled)
	}
	ctx, cancel := context.WithTimeout(ctx, dryRunTimeout)
	defer cancel()
	out, err := env.Exec.Run(ctx, []string{"git", "-C", repo, "worktree", "prune", "--dry-run", "--verbose"})
	if err != nil {
		return nil, fmt.Errorf("ask git what a prune of %s would remove: %w", repo, err)
	}
	var cands []PruneCandidate
	for line := range strings.Lines(string(out)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Removing worktrees/")
		if !ok {
			continue
		}
		name, reason, _ := strings.Cut(rest, ": ")
		reg := filepath.Join(repo, ".git", "worktrees", name)
		c := PruneCandidate{Name: name, Path: reg, Reason: reason}
		if data, err := readSmall(filepath.Join(reg, "gitdir")); err == nil {
			if p := strings.TrimSpace(data); filepath.IsAbs(p) {
				c.Path = filepath.Dir(filepath.Clean(p))
				_, statErr := os.Lstat(c.Path)
				c.Gone = errors.Is(statErr, os.ErrNotExist) && reason == "gitdir file points to non-existent location"
				if c.Gone && OnMissingVolume(c.Path) {
					// The folder may come back with its drive.
					c.Gone, c.Reason = false, "its volume is not mounted"
				}
			}
		}
		cands = append(cands, c)
	}
	return cands, nil
}

// OnMissingVolume reports whether path lies on a removable or network volume
// that is not mounted now, such as /Volumes/External on macOS or
// /media/me/disk on Linux: what was there may come back with the drive.
func OnMissingVolume(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var depth int
	switch {
	case len(parts) > 1 && (parts[0] == "Volumes" || parts[0] == "mnt"):
		depth = 2
	case len(parts) > 2 && parts[0] == "media":
		depth = 3
	case len(parts) > 3 && parts[0] == "run" && parts[1] == "media":
		depth = 4
	default:
		return false
	}
	_, err := os.Lstat("/" + strings.Join(parts[:depth], "/"))
	return errors.Is(err, os.ErrNotExist)
}
