package appcache

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/worktree"
)

// sessionMinAge is how long a month of Codex sessions must have been left
// alone before it is offered, so recent conversations stay resumable.
const sessionMinAge = 30 * 24 * time.Hour

func codexHome(env *scan.Env) string { return env.CodexHome() }

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

// claudeConfigDir is $CLAUDE_CONFIG_DIR when it holds an absolute path, else
// ~/.claude, with symbolic links resolved.
func claudeConfigDir(env *scan.Env) string {
	return resolve(firstNonEmpty(absVar(env, "CLAUDE_CONFIG_DIR"), filepath.Join(env.Home, ".claude")))
}

// maxSessionLines bounds how far into a session file the scanner looks for
// the working directory, which Claude Code writes in its first few records.
const maxSessionLines = 200

// scanClaudeSessions reports the session folders of Claude Code projects
// whose folder no longer exists, once they have been left alone for 30
// days. A folder's name replaces every / of the project path with -, which
// cannot be undone, so the project path is read from the cwd field of the
// session files instead.
func scanClaudeSessions(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	projects := filepath.Join(claudeConfigDir(env), "projects")
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	var entries []entry
	for _, e := range subdirs(env, projects) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := filepath.Join(projects, e.Name())
		cwd := sessionCwd(dir)
		if cwd == "" {
			continue
		}
		if _, err := os.Lstat(cwd); !errors.Is(err, fs.ErrNotExist) || worktree.OnMissingVolume(cwd) {
			continue
		}
		if now.Sub(newestModTime(ctx, dir)) < sessionMinAge {
			continue
		}
		entries = append(entries, entry{
			path:    dir,
			tier:    finding.TierB,
			name:    "claude sessions of " + cwd,
			restore: "cannot be restored",
			warning: "Claude Code cannot resume these sessions; their project folder " + cwd + " no longer exists",
		})
	}
	return collect(ctx, env, entries)
}

// sessionCwd returns the working directory recorded in the session files of
// a Claude Code project folder, or an empty string when none records one.
func sessionCwd(dir string) string {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return ""
	}
	slices.Sort(files)
	for _, name := range files {
		if cwd := fileCwd(name); cwd != "" {
			return cwd
		}
	}
	return ""
}

// fileCwd reads the first records of one session file for a cwd field.
func fileCwd(name string) string {
	f, err := os.Open(name)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for i := 0; i < maxSessionLines && sc.Scan(); i++ {
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && filepath.IsAbs(rec.Cwd) {
			return filepath.Clean(rec.Cwd)
		}
	}
	return ""
}

// scanCodexPluginBackups reports the plugin backups Codex leaves in its
// temporary folder when it updates plugins.
func scanCodexPluginBackups(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	tmp := filepath.Join(codexHome(env), ".tmp")
	var entries []entry
	for _, e := range subdirs(env, tmp) {
		if !strings.HasPrefix(e.Name(), "plugins-backup-") {
			continue
		}
		entries = append(entries, entry{
			path:    filepath.Join(tmp, e.Name()),
			tier:    finding.TierA,
			restore: "Codex keeps its plugins in its plugins folder; this is a copy from an update",
		})
	}
	return collect(ctx, env, entries)
}
