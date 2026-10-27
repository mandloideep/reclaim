package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
)

// Repo is a main repository with the facts every one of its worktrees shares.
type Repo struct {
	// Path is the folder holding the .git directory.
	Path string
	// Origin is the URL of the origin remote, empty when there is none.
	Origin string
	// DefaultRef is the full ref merge state is checked against, such as
	// refs/remotes/origin/main, empty when there is none.
	DefaultRef string
	// FetchedAt is the modification time of FETCH_HEAD.
	FetchedAt time.Time
	// Entries are the repository's worktrees, the main one first.
	Entries []Entry
}

// DefaultBranch is the short name of DefaultRef, such as origin/main.
func (r *Repo) DefaultBranch() string {
	s := strings.TrimPrefix(r.DefaultRef, "refs/remotes/")
	return strings.TrimPrefix(s, "refs/heads/")
}

// Linked returns the linked worktrees whose folders exist.
func (r *Repo) Linked() []Entry {
	var out []Entry
	for _, e := range r.Entries {
		if !e.Main && !e.Bare && !e.Prunable {
			out = append(out, e)
		}
	}
	return out
}

// Stale returns the registrations whose folders are gone.
func (r *Repo) Stale() []Entry {
	var out []Entry
	for _, e := range r.Entries {
		if !e.Main && e.Prunable {
			out = append(out, e)
		}
	}
	return out
}

// Entry returns the worktree registered at path.
func (r *Repo) Entry(path string) (Entry, bool) {
	for _, e := range r.Entries {
		if e.Path == path || resolve(e.Path) == path {
			return e, true
		}
	}
	return Entry{}, false
}

// OpenRepo reads the facts of the repository at path: its worktrees, origin,
// default branch and last fetch time.
func OpenRepo(ctx context.Context, env Env, path string) (*Repo, error) {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s has no .git directory: %w", path, ErrNotLinked)
	}
	entries, err := List(ctx, env, path)
	if err != nil {
		return nil, err
	}
	r := &Repo{Path: path, Entries: entries}
	// A repository without an origin makes git exit with an error, which
	// is the same as having none.
	if out, err := env.git(ctx, path, "config", "--get", "remote.origin.url"); err == nil {
		r.Origin = strings.TrimSpace(out)
	}
	r.DefaultRef, err = defaultRef(ctx, env, path)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(filepath.Join(path, ".git", "FETCH_HEAD")); err == nil {
		r.FetchedAt = fi.ModTime()
	}
	return r, nil
}

// defaultRef finds the default branch: what refs/remotes/origin/HEAD points
// at, else origin/main, else origin/master, else local main or master.
func defaultRef(ctx context.Context, env Env, repo string) (string, error) {
	candidates := []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}
	out, err := env.git(ctx, repo, append([]string{"for-each-ref", "--format=%(refname)%09%(symref)"}, candidates...)...)
	if err != nil {
		return "", fmt.Errorf("find the default branch of %s: %w", repo, err)
	}
	found := map[string]string{}
	for line := range strings.Lines(out) {
		ref, sym, _ := strings.Cut(strings.TrimRight(line, "\n"), "\t")
		found[ref] = sym
	}
	if sym := found["refs/remotes/origin/HEAD"]; sym != "" {
		return sym, nil
	}
	for _, c := range candidates[1:] {
		if _, ok := found[c]; ok {
			return c, nil
		}
	}
	return "", nil
}

// Classify returns the state of the linked worktree at path. It reports
// ErrNotLinked when path is not one. Parts of the state that git cannot
// report are named in Unknown rather than failing the call.
func Classify(ctx context.Context, env Env, path string) (*finding.WorktreeState, error) {
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err != nil {
		return nil, fmt.Errorf("%s does not exist: %w", path, ErrNotLinked)
	}
	path = resolve(path)
	loc, err := Locate(path)
	if err != nil {
		return nil, err
	}
	repo, err := OpenRepo(ctx, env, loc.Repo)
	if err != nil {
		return nil, err
	}
	e, ok := repo.Entry(path)
	if !ok {
		return nil, fmt.Errorf("git does not list %s as a worktree of %s: %w", path, loc.Repo, ErrNotLinked)
	}
	if e.Main || e.Bare {
		return nil, fmt.Errorf("%s is the main worktree of %s: %w", path, loc.Repo, ErrNotLinked)
	}
	return ClassifyEntry(ctx, env, repo, e), nil
}

// ClassifyEntry returns the state of one linked worktree of repo.
func ClassifyEntry(ctx context.Context, env Env, repo *Repo, e Entry) *finding.WorktreeState {
	path := resolve(e.Path)
	s := &finding.WorktreeState{
		Repo:          repo.Path,
		Path:          path,
		Branch:        e.Branch,
		Detached:      e.Detached,
		Head:          e.Head,
		Origin:        repo.Origin,
		Locked:        e.Locked,
		LockReason:    e.LockReason,
		DefaultBranch: repo.DefaultBranch(),
		DefaultRef:    repo.DefaultRef,
		FetchedAt:     repo.FetchedAt,
		Merged:        finding.MergedUnknown,
	}
	s.Tool = inferTool(env, path, e.LockReason)
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		s.Exists = true
	}
	unknown := func(part string, err error) {
		s.Unknown = append(s.Unknown, part)
		if s.Problem == "" {
			s.Problem = err.Error()
		}
	}
	if e.Locked {
		s.LockLive = lockLive(ctx, env, e.LockReason)
	}
	if !s.Exists {
		unknown("exists", fmt.Errorf("%s is gone", path))
		Judge(s)
		return s
	}
	if s.Head == "" {
		unknown("head", errors.New("HEAD has no commit"))
	}
	if err := countStatus(ctx, env, s); err != nil {
		unknown("modified", err)
		s.Unknown = append(s.Unknown, "untracked")
	}
	if err := countIgnored(ctx, env, s); err != nil {
		unknown("ignored", err)
	}
	if n, err := countHidden(ctx, env, s.Path); err != nil {
		unknown("hidden", err)
	} else if n > 0 {
		unknown("hidden", fmt.Errorf("%d tracked files are marked assume-unchanged or skip-worktree, so git status cannot see changes to them", n))
	}
	if s.Head != "" {
		if n, err := revCount(ctx, env, path, "HEAD", "--not", "--branches", "--remotes", "--tags"); err != nil {
			unknown("loose", err)
		} else {
			s.Loose = n
		}
		if n, err := revCount(ctx, env, path, "HEAD", "--not", "--remotes"); err != nil {
			unknown("unpushed", err)
		} else {
			s.Unpushed = n
		}
		upstreamAndAhead(ctx, env, s)
		if repo.DefaultRef == "" {
			unknown("merged", fmt.Errorf("%s has no origin/HEAD, origin/main, origin/master, main or master branch", repo.Path))
		} else if m, note, err := mergeState(ctx, env, path, repo.DefaultRef); err != nil {
			unknown("merged", err)
		} else {
			s.Merged, s.MergeNote = m, note
		}
	}
	Judge(s)
	return s
}

// countStatus counts modified and untracked files from git status.
func countStatus(ctx context.Context, env Env, s *finding.WorktreeState) error {
	out, err := env.git(ctx, s.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	s.Modified, s.Untracked = parseStatus(out)
	return nil
}

// parseStatus counts the records of "git status --porcelain=v1 -z". A rename
// or copy is followed by an extra field holding the original path.
func parseStatus(out string) (modified, untracked int) {
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.TrimSpace(f) == "" {
			continue
		}
		xy := f[:min(2, len(f))]
		switch {
		case strings.HasPrefix(f, "??"):
			untracked++
		case strings.HasPrefix(f, "!!"):
		default:
			modified++
			if strings.ContainsAny(xy, "RC") {
				i++
			}
		}
	}
	return modified, untracked
}

// countHidden counts the tracked files whose changes git status does not
// show: marked assume-unchanged (a lowercase tag in git ls-files -v) or
// skip-worktree (tag S). git worktree remove would delete changes to them
// without complaint.
func countHidden(ctx context.Context, env Env, dir string) (int, error) {
	out, err := env.git(ctx, dir, "ls-files", "-v", "-z")
	if err != nil {
		return 0, err
	}
	n := 0
	for rec := range strings.SplitSeq(out, "\x00") {
		if rec == "" {
			continue
		}
		tag := rec[0]
		if tag == 'S' || tag >= 'a' && tag <= 'z' {
			n++
		}
	}
	return n, nil
}

// maxIgnoredSample is how many ignored names the state keeps.
const maxIgnoredSample = 5

// countIgnored counts ignored files and folders outside artifact folders.
// Wholly ignored folders are reported once, so node_modules counts as one
// entry, and it is an artifact.
func countIgnored(ctx context.Context, env Env, s *finding.WorktreeState) error {
	out, err := env.git(ctx, s.Path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(out, "\x00") {
		name = strings.TrimSpace(name)
		if name == "" || IsArtifact(name) {
			continue
		}
		s.Ignored++
		if len(s.IgnoredSample) < maxIgnoredSample {
			s.IgnoredSample = append(s.IgnoredSample, name)
		}
	}
	return nil
}

func revCount(ctx context.Context, env Env, dir string, args ...string) (int, error) {
	out, err := env.git(ctx, dir, append([]string{"rev-list", "--count"}, args...)...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("git rev-list in %s printed %q: %w", dir, out, err)
	}
	return n, nil
}

// upstreamAndAhead sets the branch's upstream and how many commits HEAD has
// that it does not. Neither decides a verdict, so failures leave them unset.
func upstreamAndAhead(ctx context.Context, env Env, s *finding.WorktreeState) {
	if s.Branch == "" {
		return
	}
	out, err := env.git(ctx, s.Path, "for-each-ref", "--format=%(upstream)", "refs/heads/"+s.Branch)
	ref := strings.TrimSpace(out)
	if err != nil || ref == "" {
		return
	}
	s.Upstream = strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "refs/heads/")
	if n, err := revCount(ctx, env, s.Path, ref+"..HEAD"); err == nil {
		s.Ahead = n
	}
}

// mergeState runs the three merge checks in order, cheapest first: HEAD is
// an ancestor of the default branch; every commit has an equivalent patch in
// it, as after a rebase merge; or the whole change since the merge base is
// one commit of it, as after a squash merge. git cherry compares patch ids,
// which ignore whitespace, so a rebase or squash match is confirmed by
// finding a commit of the default branch whose diff is exactly the same.
// When that fails the note says so and the state is not merged.
func mergeState(ctx context.Context, env Env, dir, def string) (finding.Merged, string, error) {
	head, err := env.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	base, err := env.git(ctx, dir, "merge-base", "HEAD", def)
	if code, ok := execx.ExitCode(err); ok && code == 1 {
		// git merge-base exits 1 when the histories share no commit.
		return finding.MergedNo, "", nil
	}
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	head, base = strings.TrimSpace(head), strings.TrimSpace(base)
	if base == head {
		return finding.MergedAncestor, "", nil
	}
	notExact := "the branch's changes are not exactly in " + shortRef(def)
	cherry, err := env.git(ctx, dir, "cherry", def, "HEAD")
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	if allEquivalent(cherry) {
		exact, err := rebaseExact(ctx, env, dir, def, base)
		if err != nil {
			return finding.MergedUnknown, "", err
		}
		if exact {
			return finding.MergedRebase, "", nil
		}
	}
	tree, err := env.git(ctx, dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	baseTree, err := env.git(ctx, dir, "rev-parse", base+"^{tree}")
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	if strings.TrimSpace(tree) == strings.TrimSpace(baseTree) {
		// The files at HEAD are those of a commit in the default branch.
		return finding.MergedSquash, "its commits add up to no change since " + shortRef(def), nil
	}
	// A synthetic commit holding the whole change since the merge base. It
	// is written as a dangling object that no ref points at.
	squash, err := env.Exec.Output(ctx, "git", "-C", dir, "-c", "user.name=reclaim", "-c", "user.email=reclaim@localhost",
		"commit-tree", strings.TrimSpace(tree), "-p", base, "-m", "reclaim squash merge check")
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	cherry, err = env.git(ctx, dir, "cherry", def, strings.TrimSpace(squash), base)
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	if !allEquivalent(cherry) {
		return finding.MergedNo, "", nil
	}
	exact, err := inDefaultExactly(ctx, env, dir, def, base, base, "HEAD")
	if err != nil {
		return finding.MergedUnknown, "", err
	}
	if !exact {
		return finding.MergedNo, notExact, nil
	}
	return finding.MergedSquash, "", nil
}

// rebaseExact confirms a rebase match: every commit of the branch since the
// merge base has a commit in the default branch with exactly its diff. A
// branch with merge commits is left to the squash check, which compares the
// whole change.
func rebaseExact(ctx context.Context, env Env, dir, def, base string) (bool, error) {
	merges, err := revCount(ctx, env, dir, "--merges", base+"..HEAD")
	if err != nil {
		return false, err
	}
	if merges > 0 {
		return false, nil
	}
	out, err := env.git(ctx, dir, "rev-list", "--no-merges", base+"..HEAD")
	if err != nil {
		return false, err
	}
	for c := range strings.FieldsSeq(out) {
		exact, err := inDefaultExactly(ctx, env, dir, def, base, c+"^", c)
		if err != nil || !exact {
			return false, err
		}
	}
	return true, nil
}

// maxCandidates bounds the commits of the default branch compared byte for
// byte with a change; more than that counts as no match, which never makes
// work look merged.
const maxCandidates = 300

// inDefaultExactly reports whether a commit of the default branch since the
// merge base has exactly the diff between from and to: the same files,
// modes, blob ids and lines, compared byte for byte.
func inDefaultExactly(ctx context.Context, env Env, dir, def, base, from, to string) (bool, error) {
	want, err := env.git(ctx, dir, "diff-tree", "-p", "--no-color", "--full-index", from, to)
	if err != nil {
		return false, err
	}
	names, err := env.git(ctx, dir, "diff-tree", "-r", "--name-only", "-z", from, to)
	if err != nil {
		return false, err
	}
	paths := slices.DeleteFunc(strings.Split(names, "\x00"), func(p string) bool { return strings.TrimSpace(p) == "" })
	if len(paths) == 0 || strings.TrimSpace(want) == "" {
		return false, nil
	}
	// File names are pathspecs; --literal-pathspecs keeps a name such as
	// "*.go" or ":x" from matching anything but itself.
	out, err := env.git(ctx, dir, append([]string{"--literal-pathspecs", "rev-list", "--no-merges", base + ".." + def, "--"}, paths...)...)
	if err != nil {
		return false, err
	}
	candidates := strings.Fields(out)
	if len(candidates) > maxCandidates {
		return false, nil
	}
	for _, c := range candidates {
		got, err := env.git(ctx, dir, "diff-tree", "-p", "--no-color", "--full-index", c+"^", c)
		if err != nil {
			return false, err
		}
		if got == want {
			return true, nil
		}
	}
	return false, nil
}

// shortRef is a ref without refs/remotes/ or refs/heads/.
func shortRef(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "refs/heads/")
}

// allEquivalent reports whether git cherry printed at least one commit and
// marked every one as already present upstream.
func allEquivalent(out string) bool {
	lines := slices.DeleteFunc(strings.Split(strings.TrimSpace(out), "\n"), func(l string) bool { return strings.TrimSpace(l) == "" })
	if len(lines) == 0 {
		return false
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "- ") {
			return false
		}
	}
	return true
}

// inferTool names the tool that made a worktree from its path and lock.
func inferTool(env Env, path, lockReason string) string {
	sep := string(filepath.Separator)
	switch {
	case strings.HasPrefix(lockReason, "claude agent"), strings.Contains(path+sep, sep+".claude"+sep+"worktrees"+sep):
		return "claude-code"
	case env.CodexHome != "" && strings.HasPrefix(path, filepath.Join(env.CodexHome, "worktrees")+sep):
		return "codex"
	}
	for _, dir := range []string{".worktrees", ".conductor"} {
		if strings.Contains(path, sep+dir+sep) {
			return strings.TrimPrefix(dir, ".")
		}
	}
	return "manual"
}

// Recheck repeats, right before git acts, the cheap parts of the state that
// git worktree remove itself does not guard: HEAD is still head, nothing is
// modified, untracked or hidden from git status, and no ignored file outside
// artifact folders appeared. The full classification ran moments earlier;
// this closes the time it took to measure the worktree.
func Recheck(ctx context.Context, env Env, path, head string) error {
	now, err := env.git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(now) != head {
		return fmt.Errorf("HEAD moved to %s since it was checked", strings.TrimSpace(now))
	}
	s := &finding.WorktreeState{Path: path}
	if err := countStatus(ctx, env, s); err != nil {
		return err
	}
	if err := countIgnored(ctx, env, s); err != nil {
		return err
	}
	hidden, err := countHidden(ctx, env, path)
	if err != nil {
		return err
	}
	if s.Modified+s.Untracked+s.Ignored+hidden > 0 {
		return fmt.Errorf("since it was checked it gained %d modified, %d untracked, %d ignored and %d hidden files", s.Modified, s.Untracked, s.Ignored, hidden)
	}
	return nil
}
