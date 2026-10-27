// Package worktreetest builds git repositories with linked worktrees in temp
// directories for tests. Every fixture has a bare repository in the same
// temp directory as its origin, so nothing touches the network, and git runs
// with no global or system configuration, so the tests do not depend on the
// machine's settings and never change them.
package worktreetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixture is a main repository cloned from a bare origin.
type Fixture struct {
	t testing.TB
	// Dir is the temp directory holding everything.
	Dir string
	// Remote is the bare origin repository.
	Remote string
	// Repo is the main checkout.
	Repo string
}

// Isolate points git at an empty configuration and sets a fixed identity.
// It skips the test when git is not installed. It uses t.Setenv, so tests
// that call it cannot run in parallel.
func Isolate(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[init]\n\tdefaultBranch = main\n[advice]\n\tdetachedHead = false\n"), 0o644))
	setenv(t, "GIT_CONFIG_GLOBAL", cfg)
	setenv(t, "GIT_CONFIG_NOSYSTEM", "1")
	setenv(t, "GIT_AUTHOR_NAME", "Test")
	setenv(t, "GIT_AUTHOR_EMAIL", "test@example.com")
	setenv(t, "GIT_COMMITTER_NAME", "Test")
	setenv(t, "GIT_COMMITTER_EMAIL", "test@example.com")
}

func setenv(t testing.TB, k, v string) {
	if s, ok := t.(interface{ Setenv(string, string) }); ok {
		s.Setenv(k, v)
		return
	}
	t.Fatalf("cannot set %s", k)
}

// New creates a bare origin and a main checkout with one pushed commit on
// main, and origin/HEAD pointing at origin/main.
func New(t testing.TB) *Fixture {
	t.Helper()
	Isolate(t)
	dir := RealTempDir(t)
	f := &Fixture{t: t, Dir: dir, Remote: filepath.Join(dir, "origin.git"), Repo: filepath.Join(dir, "repo")}
	f.Git(dir, "init", "-q", "--bare", "-b", "main", f.Remote)
	f.Git(dir, "init", "-q", "-b", "main", f.Repo)
	f.Write(f.Repo, ".gitignore", ".env\nnode_modules/\n.claude/\n")
	f.Write(f.Repo, "README.md", "hello\n")
	f.Git(f.Repo, "add", "-A")
	f.Git(f.Repo, "commit", "-q", "-m", "first")
	f.Git(f.Repo, "remote", "add", "origin", f.Remote)
	f.Git(f.Repo, "push", "-q", "-u", "origin", "main")
	f.Git(f.Repo, "remote", "set-head", "origin", "main")
	return f
}

// NewLocal creates a repository with one commit and no remote.
func NewLocal(t testing.TB) *Fixture {
	t.Helper()
	Isolate(t)
	dir := RealTempDir(t)
	f := &Fixture{t: t, Dir: dir, Repo: filepath.Join(dir, "repo")}
	f.Git(dir, "init", "-q", "-b", "main", f.Repo)
	f.Write(f.Repo, "README.md", "hello\n")
	f.Git(f.Repo, "add", "-A")
	f.Git(f.Repo, "commit", "-q", "-m", "first")
	return f
}

// RealTempDir returns a temp directory with symbolic links resolved, as
// /var is a link to /private/var on macOS, so paths compare equal to the
// canonical ones reclaim works with.
func RealTempDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

// Git runs git in dir and returns its trimmed output, failing the test on
// error.
func (f *Fixture) Git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(f.t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// Write writes a file below dir, creating its folders.
func (f *Fixture) Write(dir, name, content string) {
	f.t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(f.t, os.WriteFile(p, []byte(content), 0o644))
}

// Commit writes a file in dir and commits it, returning the new commit.
func (f *Fixture) Commit(dir, name, content string) string {
	f.t.Helper()
	f.Write(dir, name, content)
	f.Git(dir, "add", "-A")
	f.Git(dir, "commit", "-q", "-m", "change "+name)
	return f.Git(dir, "rev-parse", "HEAD")
}

// Worktree adds a linked worktree at <repo>/.claude/worktrees/<name> on a
// new branch of the same name, started from origin/main when there is a
// remote and main otherwise.
func (f *Fixture) Worktree(name string) string {
	f.t.Helper()
	return f.WorktreeAt(filepath.Join(f.Repo, ".claude", "worktrees", name), name)
}

// WorktreeAt adds a linked worktree at path on a new branch, or detached
// when branch is empty.
func (f *Fixture) WorktreeAt(path, branch string) string {
	f.t.Helper()
	start := "main"
	if f.Remote != "" {
		start = "origin/main"
	}
	if branch == "" {
		f.Git(f.Repo, "worktree", "add", "-q", "--detach", path, start)
	} else {
		f.Git(f.Repo, "worktree", "add", "-q", "--no-track", "-b", branch, path, start)
	}
	return path
}

// Push pushes the worktree's branch to origin and sets its upstream.
func (f *Fixture) Push(path, branch string) {
	f.t.Helper()
	f.Git(path, "push", "-q", "-u", "origin", branch)
}

// MergeFF fast forwards main to branch and pushes it.
func (f *Fixture) MergeFF(branch string) {
	f.t.Helper()
	f.Git(f.Repo, "merge", "-q", "--ff-only", branch)
	f.Git(f.Repo, "push", "-q", "origin", "main")
}

// MergeSquash squash merges branch into main as one commit, after another
// commit lands on main first so main is not a plain fast forward, and pushes.
func (f *Fixture) MergeSquash(branch string) {
	f.t.Helper()
	f.Commit(f.Repo, "other-"+branch+".txt", "other\n")
	f.Git(f.Repo, "merge", "-q", "--squash", branch)
	f.Git(f.Repo, "commit", "-q", "-m", "squash "+branch)
	f.Git(f.Repo, "push", "-q", "origin", "main")
}

// MergeRebase replays the commits of branch onto main one by one, as a
// rebase merge does, and pushes.
func (f *Fixture) MergeRebase(branch string) {
	f.t.Helper()
	f.Commit(f.Repo, "other-"+branch+".txt", "other\n")
	f.Git(f.Repo, "cherry-pick", "main.."+branch)
	f.Git(f.Repo, "push", "-q", "origin", "main")
}

// Fetch fetches origin in the main checkout.
func (f *Fixture) Fetch() {
	f.t.Helper()
	f.Git(f.Repo, "fetch", "-q", "origin")
}
