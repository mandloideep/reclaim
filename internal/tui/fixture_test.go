package tui

import (
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
)

const (
	kB = 1000
	MB = 1000 * kB
	GB = 1000 * MB
)

// fixtureReport is a report of a fake machine with a finding of every kind
// the details pane describes, including a repository with worktrees in
// every state.
func fixtureReport() *finding.Report {
	day := 24 * time.Hour
	repo := "/Users/me/Code/storefront"
	wt := func(name string) string { return repo + "/.claude/worktrees/" + name }
	fetched := now.Add(-3 * time.Hour)
	state := func(path, branch, head string) *finding.WorktreeState {
		return &finding.WorktreeState{Repo: repo, Path: path, Tool: "claude-code", Branch: branch, Head: head, Exists: true,
			Origin: "git@github.com:acme/storefront.git", DefaultBranch: "origin/main", DefaultRef: "refs/remotes/origin/main", FetchedAt: fetched}
	}
	removable := state(wt("agent-3f9c1e2a"), "fix/checkout-rounding", "a3f9c1e2a7d04b6e8c1f2a3b4c5d6e7f8a9b0c1d")
	removable.Merged, removable.Verdict = finding.MergedSquash, finding.VerdictRemovable
	removable.PR = &finding.PullRequest{Number: 41, State: "MERGED", URL: "https://github.com/acme/storefront/pull/41"}
	pushed := state(wt("agent-7b2d40aa"), "feat/gift-cards", "a7b2d40aa1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6")
	pushed.Merged, pushed.Verdict = finding.MergedNo, finding.VerdictPushed
	pushed.PR = &finding.PullRequest{Number: 44, State: "OPEN", URL: "https://github.com/acme/storefront/pull/44"}
	inUse := state(wt("agent-c0ffee12"), "chore/upgrade-next", "ac0ffee12b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e")
	inUse.Merged, inUse.Verdict, inUse.Locked, inUse.LockLive = finding.MergedNo, finding.VerdictInUse, true, true
	inUse.LockReason = "claude agent agent-c0ffee12 (pid 4242 start Mon Oct 27 14:00:00 2026)"
	inUse.Reasons = []string{"locked by a running process: " + inUse.LockReason}
	codex := state("/Users/me/.codex/worktrees/5e1d/storefront", "", "a5e1d0c9b8a7f6e5d4c3b2a1f0e9d8c7b6a5f4e3")
	codex.Tool, codex.Detached, codex.Modified, codex.Untracked = "codex", true, 12, 3
	codex.Merged, codex.Verdict = finding.MergedAncestor, finding.VerdictAttention
	codex.Reasons = []string{"12 modified and 3 untracked files"}
	codex.Hints = []string{"git -C /Users/me/.codex/worktrees/5e1d/storefront add -A && git -C /Users/me/.codex/worktrees/5e1d/storefront commit"}
	codex.ForceCommand = "git -C /Users/me/Code/storefront worktree remove --force /Users/me/.codex/worktrees/5e1d/storefront"

	wtFinding := func(s *finding.WorktreeState, tier finding.Tier, action finding.Action, size int64, warning string) finding.Finding {
		f := finding.Finding{Scanner: "git-worktrees", Category: finding.CategoryWorktree, Tier: tier, Path: s.Path, Kind: finding.KindDir,
			Target: s.Path, Name: s.Tool + " " + s.Branch, Size: size, Project: repo, Repo: repo, Action: action, Worktree: s, Warning: warning}
		if action == finding.ActionGitWorktreeRemove {
			f.Command = []string{"git", "-C", repo, "worktree", "remove", s.Path}
			f.Restore = "git -C " + repo + " worktree add " + s.Path + " " + s.Branch
		}
		return f
	}
	findings := []finding.Finding{
		{Scanner: "rust-target", Category: finding.CategoryProject, Tier: finding.TierA, Path: "/Users/me/Code/ripgrep-fork/target", Kind: finding.KindDir,
			Target: "/Users/me/Code/ripgrep-fork/target", Project: "/Users/me/Code/ripgrep-fork", Size: 3_900 * MB,
			LastUsed: now.Add(-150 * day), Restore: "cargo build", Action: finding.ActionRemovePath},
		{Scanner: "python-venv", Category: finding.CategoryProject, Tier: finding.TierA, Path: "/Users/me/Code/ml-pipeline/.venv", Kind: finding.KindDir,
			Target: "/Users/me/Code/ml-pipeline/.venv", Project: "/Users/me/Code/ml-pipeline", Size: 2_300 * MB,
			LastUsed: now.Add(-20 * day), Restore: "uv sync", Action: finding.ActionRemovePath},
		{Scanner: "dist", Category: finding.CategoryProject, Tier: finding.TierC, Path: "/Users/me/Code/blog/dist", Kind: finding.KindDir,
			Target: "/Users/me/Code/blog/dist", Project: "/Users/me/Code/blog", Size: 24 * MB, Action: finding.ActionRemovePath,
			Warning: "no build config next to it, so it may be the only copy"},
		wtFinding(removable, finding.TierA, finding.ActionGitWorktreeRemove, 184*MB, ""),
		wtFinding(pushed, finding.TierB, finding.ActionGitWorktreeRemove, 96*MB, "pushed, not merged; PR #44 is open"),
		wtFinding(inUse, finding.TierC, finding.ActionNone, 88*MB, "in use, locked by a running process"),
		wtFinding(codex, finding.TierC, finding.ActionNone, 231*MB, "12 modified and 3 untracked files"),
		{Scanner: "worktree-registrations", Category: finding.CategoryWorktree, Tier: finding.TierA, Target: "git-worktree-prune:" + repo,
			Name: "2 stale worktree registrations", Size: 800, Repo: repo, Project: repo, Action: finding.ActionGitWorktreePrune,
			Registrations: []string{"/Users/me/Code/storefront-spike", "/Users/me/Code/storefront-v1"},
			Command:       []string{"git", "-C", repo, "worktree", "prune"}},
		{Scanner: "git-worktrees", Category: finding.CategoryWorktree, Tier: finding.TierC, Path: "/Users/me/Code/personal/storefront", Kind: finding.KindDir,
			Target: "/Users/me/Code/personal/storefront", Name: "another clone of this origin", Repo: repo, Project: repo, Action: finding.ActionNone,
			Warning: "another clone of github.com/acme/storefront, also cloned at " + repo},
		{Scanner: "npm-cache", Category: finding.CategoryPackageCache, Tier: finding.TierB, Path: "/Users/me/.npm/_cacache", Kind: finding.KindDir,
			Target: "/Users/me/.npm/_cacache", Size: 1_800 * MB, Action: finding.ActionRunCommand, Command: []string{"npm", "cache", "clean", "--force"},
			Restore: "npm downloads packages again"},
		{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierB, Target: "sha256:node20", Name: "image node:20", Tags: []string{"node:20"},
			Size: 1_100 * MB, LastUsed: now.Add(-90 * day), Action: finding.ActionDockerRemoveImage, Restore: "docker pull node:20"},
		{Scanner: "docker", Category: finding.CategoryDocker, Tier: finding.TierA, Target: "docker-build-cache", Name: "build cache (2 unused records)",
			Size: 3_200 * MB, Action: finding.ActionDockerPruneBuildCache},
		{Scanner: "claude-sessions", Category: finding.CategoryAppCache, Tier: finding.TierB, Path: "/Users/me/.claude/projects/-Users-me-Code-old", Kind: finding.KindDir,
			Target: "/Users/me/.claude/projects/-Users-me-Code-old", Name: "claude sessions of /Users/me/Code/old", Size: 40 * MB, Restore: "cannot be restored",
			Warning: "Claude Code cannot resume these sessions; their project folder /Users/me/Code/old no longer exists", Action: finding.ActionRemovePath},
		{Scanner: "simulator-runtimes", Category: finding.CategoryAppCache, Tier: finding.TierB, Target: "simulator-runtime:iOS-17-0", Name: "iOS 17.0",
			Size: 7_000 * MB, NeedsSudo: true, Action: finding.ActionRunCommand,
			Command: []string{"xcrun", "simctl", "runtime", "delete", "iOS-17-0"}},
		{Scanner: "old-downloads", Category: finding.CategoryDownloads, Tier: finding.TierC, Path: "/Users/me/Downloads/talk.mov", Kind: finding.KindFile,
			Target: "/Users/me/Downloads/talk.mov", Size: 3_240 * MB, Action: finding.ActionNone, Warning: "not touched for 6 months; reclaim never removes it"},
	}
	for i := range findings {
		findings[i].ID = finding.MakeID(findings[i].Scanner, findings[i].Target)
	}
	finding.Sort(findings)
	return &finding.Report{
		Version:  finding.ReportVersion,
		Created:  now,
		Host:     "laptop",
		Scope:    &finding.Scope{Command: "scan", Args: []string{"--min-size=10MB"}, Roots: []string{"/Users/me/Code"}},
		Roots:    []string{"/Users/me", "/Users/me/Code"},
		Findings: findings,
		Warnings: []finding.Warning{{Scanner: "projects", Path: "/Users/me/Code/locked", Message: "not scanned: permission denied"}},
		Notes:    []string{"Docker runs in OrbStack, which returns freed space to macOS automatically after removal."},
	}
}
