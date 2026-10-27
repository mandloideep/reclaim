package worktree

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
)

// artifactNames are folders that ignored files may live in without making a
// worktree hold work: dependencies, build output and caches that a build or
// an install creates again.
func artifactNames() []string {
	return []string{
		"node_modules", ".pnpm-store", ".yarn", ".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache",
		".ruff_cache", ".tox", ".nox", "target", "dist", "build", "out", "bin", "obj", ".build", ".next", ".nuxt",
		".svelte-kit", ".astro", ".turbo", ".parcel-cache", ".angular", ".expo", ".output", ".vercel", ".cache",
		".tools", ".gradle", "Pods", ".dart_tool", ".terraform", "coverage", ".nyc_output", "test-results",
		"playwright-report", "storybook-static", "DerivedData", ".swiftpm", "vendor",
	}
}

// artifactFiles are generated files that may be ignored without being work.
func artifactFiles() []string {
	return []string{".DS_Store", "*.tsbuildinfo", "*.pyc", ".eslintcache", "*.log", "*.coverprofile", "coverage.out"}
}

// IsArtifact reports whether an ignored path, relative to the worktree with
// slashes and a trailing slash for folders, lies in an artifact folder or is
// a generated file, so losing it loses no work.
func IsArtifact(rel string) bool {
	rel = strings.TrimSuffix(rel, "/")
	parts := strings.Split(rel, "/")
	if slices.ContainsFunc(parts, func(p string) bool { return slices.Contains(artifactNames(), p) }) {
		return true
	}
	base := parts[len(parts)-1]
	return slices.ContainsFunc(artifactFiles(), func(pattern string) bool {
		ok, _ := path.Match(pattern, base)
		return ok
	})
}

// lockPID and lockStart read the process Claude Code records in its lock
// reasons, such as "claude agent agent-a4f0 (pid 30201 start Mon Oct  5
// 11:42:02 2026)".
var (
	lockPID   = regexp.MustCompile(`\bpid (\d+)\b`)
	lockStart = regexp.MustCompile(`\bstart ([A-Z][a-z]{2} [A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2} \d{4})`)
)

// psLayout is how ps -o lstart prints a start time, with single spaces.
const psLayout = "Mon Jan 2 15:04:05 2006"

// lockLive reports whether the process a lock names is running and, when the
// reason records its start time, started then, because process ids are
// reused. A lock that names no process is held by whoever made it. When the
// process table cannot be read the lock counts as live, which is never
// removed.
func lockLive(ctx context.Context, env Env, reason string) bool {
	m := lockPID.FindStringSubmatch(reason)
	if m == nil {
		return true
	}
	if env.Exec == nil {
		return true
	}
	if _, err := env.Exec.LookPath("ps"); err != nil {
		return true
	}
	out, err := env.Exec.Output(ctx, "ps", "-o", "lstart=", "-p", m[1])
	started := strings.Join(strings.Fields(out), " ")
	if code, ok := execx.ExitCode(err); ok && code == 1 {
		// ps exits 1 when no process has the id.
		return false
	}
	if err != nil || started == "" {
		// The process table could not be read.
		return true
	}
	s := lockStart.FindStringSubmatch(reason)
	if s == nil {
		return true
	}
	recorded, err := time.Parse(psLayout, strings.Join(strings.Fields(s[1]), " "))
	if err != nil {
		return true
	}
	running, err := time.ParseInLocation(psLayout, started, time.Local)
	if err != nil {
		return true
	}
	// Claude Code records the start time in UTC and ps prints local time,
	// so the times are compared as wall clock readings in both zones.
	for _, loc := range []*time.Location{time.UTC, time.Local} {
		wall := time.Date(running.In(loc).Year(), running.In(loc).Month(), running.In(loc).Day(),
			running.In(loc).Hour(), running.In(loc).Minute(), running.In(loc).Second(), 0, time.UTC)
		if d := wall.Sub(recorded); d >= -time.Second && d <= time.Second {
			return true
		}
	}
	return false
}

// Judge sets the verdict, the reasons, the retrieval hints and the force
// command of a state from its facts. A live lock makes the worktree in use;
// otherwise the verdict is the one WorkVerdict gives, with a stale lock
// listed for attention.
func Judge(s *finding.WorktreeState) {
	s.Verdict, s.Reasons = WorkVerdict(s)
	s.Hints = hints(s)
	s.ForceCommand = ""
	switch {
	case s.Locked && s.LockLive:
		s.Verdict = finding.VerdictInUse
		s.Reasons = append([]string{lockWords(s)}, s.Reasons...)
	case s.Locked:
		s.Verdict = finding.VerdictAttention
		s.Reasons = append([]string{lockWords(s)}, s.Reasons...)
	}
	if s.Verdict == finding.VerdictAttention && s.Exists {
		s.ForceCommand = ForceCommand(s)
	}
}

// WorkVerdict judges the work in a worktree alone, ignoring its lock:
// removable when everything is committed, pushed and merged, pushed when
// only the merge is missing, and attention otherwise, with the reasons.
func WorkVerdict(s *finding.WorktreeState) (finding.Verdict, []string) {
	var reasons []string
	if len(s.Unknown) > 0 {
		reasons = append(reasons, fmt.Sprintf("git could not report %s: %s", strings.Join(s.Unknown, ", "), s.Problem))
	}
	if s.Modified > 0 || s.Untracked > 0 {
		reasons = append(reasons, changedWords(s))
	}
	switch {
	case s.Loose > 0 && s.Detached:
		reasons = append(reasons, "detached with "+plural(s.Loose, "commit", "commits")+" no branch holds")
	case s.Loose > 0:
		reasons = append(reasons, plural(s.Loose, "commit", "commits")+" no branch holds")
	}
	if s.UnpushedBlocks() {
		reasons = append(reasons, plural(s.Unpushed, "commit", "commits")+" on no remote")
	}
	if s.Ignored > 0 {
		reasons = append(reasons, plural(s.Ignored, "ignored file", "ignored files")+": "+strings.Join(s.IgnoredSample, ", ")+more(s.Ignored, len(s.IgnoredSample)))
	}
	if !slices.Contains(s.Unknown, "merged") && s.Merged == finding.MergedUnknown && len(reasons) == 0 {
		reasons = append(reasons, "merge state unknown")
	}
	switch {
	case len(reasons) > 0:
		return finding.VerdictAttention, reasons
	case s.Merged == finding.MergedNo:
		return finding.VerdictPushed, nil
	default:
		return finding.VerdictRemovable, nil
	}
}

func changedWords(s *finding.WorktreeState) string {
	switch {
	case s.Modified > 0 && s.Untracked > 0:
		return fmt.Sprintf("%d modified and %d untracked files", s.Modified, s.Untracked)
	case s.Modified > 0:
		return plural(s.Modified, "modified file", "modified files")
	default:
		return plural(s.Untracked, "untracked file", "untracked files")
	}
}

func lockWords(s *finding.WorktreeState) string {
	reason := s.LockReason
	if reason == "" {
		reason = "no reason given"
	}
	if s.LockLive {
		return "locked by a running process: " + reason
	}
	return "locked by a process that is no longer running: " + reason
}

// hints are the commands that save the work of a worktree before removing
// it, one per kind of work that would be lost.
func hints(s *finding.WorktreeState) []string {
	if !s.Exists {
		return nil
	}
	p := Quote(s.Path)
	name := s.Branch
	if name == "" {
		name = "rescue-" + short(s.Head)
	}
	var out []string
	if s.Modified > 0 || s.Untracked > 0 {
		out = append(out,
			"git -C "+p+" add -A && git -C "+p+" commit",
			"git -C "+p+" diff > "+Quote(strings.ReplaceAll(name, "/", "-")+".patch"))
	}
	if s.Loose > 0 && s.Detached {
		out = append(out, "git -C "+p+" branch "+Quote(name))
	}
	if s.UnpushedBlocks() && s.Branch != "" {
		out = append(out, "git -C "+p+" push -u origin "+Quote(s.Branch))
	}
	if s.Ignored > 0 {
		out = append(out, "copy the ignored files you need, such as "+s.IgnoredSample[0]+", out of "+p)
	}
	if s.Merged == finding.MergedNo && s.Unpushed == 0 && s.Branch != "" {
		switch {
		case s.PR != nil && s.PR.State == "OPEN":
			out = append(out, "review and merge "+s.PR.URL)
		case s.PR == nil:
			out = append(out, "gh pr create --head "+Quote(s.Branch))
		}
	}
	if s.Locked && !s.LockLive {
		out = append(out, "git -C "+Quote(s.Repo)+" worktree unlock "+p)
	}
	return out
}

// ForceCommand is the command that removes a worktree whatever it holds.
// Reclaim prints it and never runs it.
func ForceCommand(s *finding.WorktreeState) string {
	force := "--force"
	if s.Locked {
		// git asks for --force twice to remove a locked worktree.
		force = "--force --force"
	}
	return "git -C " + Quote(s.Repo) + " worktree remove " + force + " " + Quote(s.Path)
}

// RestoreCommand is how to check the worktree out again after removing it.
func RestoreCommand(s *finding.WorktreeState) string {
	ref := s.Branch
	if ref == "" {
		ref = s.Head
	}
	return "git -C " + Quote(s.Repo) + " worktree add " + Quote(s.Path) + " " + Quote(ref)
}

// Quote quotes a word for a POSIX shell when it needs it.
func Quote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("@%+=:,./_-~", r)
	}) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func more(total, shown int) string {
	if total > shown {
		return fmt.Sprintf(" and %d more", total-shown)
	}
	return ""
}

// Words describes a worktree's state in plain words, such as "committed,
// pushed, merged in PR #3".
func Words(s *finding.WorktreeState) string {
	var parts []string
	if len(s.Unknown) > 0 {
		parts = append(parts, "git could not report "+strings.Join(s.Unknown, ", "))
	}
	if s.Modified > 0 || s.Untracked > 0 {
		parts = append(parts, changedWords(s))
	} else if !slices.Contains(s.Unknown, "modified") {
		parts = append(parts, "committed")
	}
	if s.Ignored > 0 {
		parts = append(parts, plural(s.Ignored, "ignored file", "ignored files")+": "+strings.Join(s.IgnoredSample, ", ")+more(s.Ignored, len(s.IgnoredSample)))
	}
	pr := ""
	if s.PR != nil {
		pr = fmt.Sprintf(" in PR #%d", s.PR.Number)
	}
	switch {
	case s.Loose > 0 && s.Detached:
		parts = append(parts, "detached with "+plural(s.Loose, "commit", "commits")+" no branch holds")
	case s.Unpushed > 0 && s.MergedRemotely() && s.MergeNote != "":
		parts = append(parts, s.MergeNote+"; the branch is not on the remote")
		return finishWords(s, parts)
	case s.Unpushed > 0 && s.MergedRemotely():
		// The branch was merged and then deleted on the remote, as GitHub
		// does; its changes are proven to be in the default branch.
		parts = append(parts, "merged by "+string(s.Merged)+" into "+s.DefaultBranch+pr+"; the branch is not on the remote")
		return finishWords(s, parts)
	case s.Unpushed > 0:
		parts = append(parts, plural(s.Unpushed, "commit", "commits")+" on no remote")
	case !slices.Contains(s.Unknown, "unpushed"):
		parts = append(parts, "pushed")
	}
	switch {
	case s.Merged.Proven() && s.PR != nil && s.PR.State == "MERGED":
		parts = append(parts, "merged"+pr)
	case s.Merged.Proven():
		parts = append(parts, "merged")
	case s.Merged == finding.MergedNo && s.PR != nil && s.PR.State == "MERGED":
		parts = append(parts, "merged"+pr+" but not in "+s.DefaultBranch+" locally; fetch origin")
	case s.Merged == finding.MergedNo && s.PR != nil:
		parts = append(parts, "not merged, PR #"+fmt.Sprint(s.PR.Number)+" "+strings.ToLower(s.PR.State))
	case s.Merged == finding.MergedNo:
		parts = append(parts, "not merged")
	default:
		parts = append(parts, "merge state unknown")
	}
	if s.MergeNote != "" {
		parts = append(parts, s.MergeNote)
	}
	return finishWords(s, parts)
}

// finishWords adds the lock to the words and joins them.
func finishWords(s *finding.WorktreeState, parts []string) string {
	if s.Locked {
		parts = append(parts, shortLockWords(s))
	}
	return strings.Join(parts, ", ")
}

// shortLockWords says who holds a lock in a few words.
func shortLockWords(s *finding.WorktreeState) string {
	pid := ""
	if m := lockPID.FindStringSubmatch(s.LockReason); m != nil {
		pid = " pid " + m[1]
	}
	switch {
	case s.LockLive && pid != "":
		return "in use by" + pid
	case s.LockLive:
		return "locked"
	case pid != "":
		return "stale lock," + pid + " gone"
	default:
		return "stale lock"
	}
}
