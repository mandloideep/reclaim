package appcache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
)

// sessionMinAge is how long a month of Codex sessions must have been left
// alone before it is offered, so recent conversations stay resumable.
const sessionMinAge = 30 * 24 * time.Hour

func codexHome(env *scan.Env) string {
	return resolve(firstNonEmpty(absVar(env, "CODEX_HOME"), filepath.Join(env.Home, ".codex")))
}

// scanCodexWorktrees reports each task folder under ~/.codex/worktrees. A
// task folder holds one checkout per repository, each a git worktree. A
// folder whose worktrees have uncommitted changes, or whose state git cannot
// report, is tier C.
func scanCodexWorktrees(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := filepath.Join(codexHome(env), "worktrees")
	var out []finding.Finding
	for _, e := range subdirs(env, dir) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		task := filepath.Join(dir, e.Name())
		ent := entry{
			path:    task,
			tier:    finding.TierB,
			restore: "Codex creates a new worktree for the next task; commits on branches stay in the main repository",
			warning: "git worktrees Codex made for a task; ignored files in them, such as .env, are lost; run git worktree prune in their main repository afterwards",
		}
		if reason := dirtyWorktree(ctx, env, task); reason != "" {
			ent.tier = finding.TierC
			ent.warning = reason
		}
		f, ok, err := dirFinding(ctx, env, ent)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// dirtyWorktree returns why the worktrees in a task folder may hold work that
// exists nowhere else, or an empty string when git reports them all clean.
func dirtyWorktree(ctx context.Context, env *scan.Env, task string) string {
	for _, e := range subdirs(env, task) {
		repo := filepath.Join(task, e.Name())
		if _, err := os.Lstat(filepath.Join(repo, ".git")); err != nil {
			continue
		}
		if env.Exec == nil {
			return "git is not available, so uncommitted changes in " + e.Name() + " could not be checked"
		}
		if _, err := env.Exec.LookPath("git"); err != nil {
			return "git is not installed, so uncommitted changes in " + e.Name() + " could not be checked"
		}
		status, err := env.Exec.Output(ctx, "git", "-C", repo, "status", "--porcelain")
		if err != nil {
			return "git could not report the state of " + e.Name() + ", which may hold uncommitted changes"
		}
		if strings.TrimSpace(status) != "" {
			return e.Name() + " has uncommitted changes, which are lost"
		}
		// Commits made on a detached HEAD live only in the worktree's own
		// history and become unreachable once it is removed and pruned.
		loose, err := env.Exec.Output(ctx, "git", "-C", repo, "rev-list", "-n", "1", "HEAD", "--not", "--branches", "--remotes", "--tags")
		if err != nil {
			return "git could not report whether " + e.Name() + " has commits outside its branches"
		}
		if strings.TrimSpace(loose) != "" {
			return e.Name() + " has commits that no branch, remote or tag holds, which are lost"
		}
	}
	return ""
}

// scanCodexSessions reports the Codex conversation logs one month at a time,
// skipping months touched in the last 30 days, and the archived sessions.
func scanCodexSessions(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	home := codexHome(env)
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	const (
		restore = "cannot be restored"
		warning = "Codex can no longer show or resume these conversations"
	)
	sessions := filepath.Join(home, "sessions")
	var entries []entry
	for _, y := range subdirs(env, sessions) {
		for _, m := range subdirs(env, filepath.Join(sessions, y.Name())) {
			path := filepath.Join(sessions, y.Name(), m.Name())
			// Codex appends to the log of a resumed conversation, which does
			// not change the folder's own time, so the newest file decides.
			if now.Sub(newestModTime(ctx, path)) < sessionMinAge {
				continue
			}
			entries = append(entries, entry{path: path, tier: finding.TierB, name: "codex sessions " + y.Name() + "-" + m.Name(),
				restore: restore, warning: warning})
		}
	}
	entries = append(entries, entry{path: filepath.Join(home, "archived_sessions"), tier: finding.TierB,
		name: "codex archived sessions", restore: restore, warning: warning})
	return collect(ctx, env, entries)
}

// newestModTime returns the latest modification time of dir and everything
// below it. An unreadable entry counts as modified now, so it is never
// treated as old.
func newestModTime(ctx context.Context, dir string) time.Time {
	var newest time.Time
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return time.Now()
	}
	return newest
}

// scanClaudeVM reports the virtual machine bundles of the Claude desktop app
// and Claude Code VM versions other than the current one.
func scanClaudeVM(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	if env.GOOS != "darwin" {
		return nil, nil
	}
	support := resolve(filepath.Join(env.Home, "Library", "Application Support", "Claude"))
	var entries []entry
	bundles := filepath.Join(support, "vm_bundles")
	for _, e := range subdirs(env, bundles) {
		if !strings.HasSuffix(e.Name(), ".bundle") {
			continue
		}
		entries = append(entries, entry{
			path:    filepath.Join(bundles, e.Name()),
			tier:    finding.TierB,
			restore: "Claude downloads the VM image again when it next needs it",
			warning: "quit Claude first; files kept inside its VM sessions are lost",
		})
	}
	vm := filepath.Join(support, "claude-code-vm")
	current, err := os.ReadFile(filepath.Join(vm, ".sdk-version"))
	switch {
	case err == nil:
		version := strings.TrimSpace(string(current))
		for _, e := range subdirs(env, vm) {
			if version == "" || e.Name() == version {
				continue
			}
			entries = append(entries, entry{
				path:    filepath.Join(vm, e.Name()),
				tier:    finding.TierB,
				restore: "downloaded again if Claude needs this version",
				warning: "an older Claude Code VM version; the current one is " + version,
			})
		}
	case !errors.Is(err, os.ErrNotExist):
		env.Diag.Warn(vm, "could not read the current version: "+err.Error())
	}
	return collect(ctx, env, entries)
}
