package plan

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
)

// commitID matches a full SHA-1 or SHA-256 object name.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// cleanAbs reports whether p is a clean absolute path.
func cleanAbs(p string) bool { return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p }

// within reports whether path is parent or lies below it.
func within(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}

// validateWorktreeRemove ties a GitWorktreeRemove action together: its path,
// target, repository, head, recorded state and command must all name the
// same worktree, and the recorded state must be one that its tier allows
// removing. Apply proves the state again on disk; this check makes a hand
// edit that breaks the proof fail before anything runs.
func (a *Action) validateWorktreeRemove() error {
	switch {
	case a.Path == "" || a.Target != a.Path:
		return errors.New("GitWorktreeRemove needs a path equal to its target")
	case a.Kind != finding.KindDir:
		return fmt.Errorf("GitWorktreeRemove needs kind dir, got %q", a.Kind)
	case !cleanAbs(a.Repo):
		return fmt.Errorf("GitWorktreeRemove needs the clean absolute path of its repository, got %q", a.Repo)
	case within(a.Repo, a.Path):
		return fmt.Errorf("the worktree %s is or contains its repository %s, which is never removed", a.Path, a.Repo)
	case !commitID.MatchString(a.Head):
		return fmt.Errorf("GitWorktreeRemove needs the commit the worktree was at, got %q", a.Head)
	case a.Worktree == nil:
		return errors.New("GitWorktreeRemove needs the worktree state that proves it is safe")
	case a.Worktree.Path != a.Path || a.Worktree.Repo != a.Repo || a.Worktree.Head != a.Head:
		return errors.New("the recorded worktree state names another worktree, repository or commit, the plan was edited inconsistently")
	case !slices.Equal(a.Command, WorktreeRemoveCommand(a.Repo, a.Path)):
		return fmt.Errorf("command %q is not %q, the plan was edited inconsistently",
			strings.Join(a.Command, " "), strings.Join(WorktreeRemoveCommand(a.Repo, a.Path), " "))
	case a.NeedsSudo || len(a.Registrations) > 0:
		return errors.New("GitWorktreeRemove must not need sudo or carry registrations")
	}
	return RemovableState(a.Worktree, a.Tier)
}

// RemovableState checks that a worktree state allows removal at a tier:
// nothing uncommitted, untracked, loose, unpushed or ignored outside
// artifact folders, nothing unknown, no lock, and the commits in the default
// branch. Tier B relaxes only the merge: its commits may be unmerged, but
// they must be on a remote. Tier C is never removed.
func RemovableState(s *finding.WorktreeState, tier finding.Tier) error {
	var problems []string
	if !s.Exists {
		problems = append(problems, "it does not exist")
	}
	if len(s.Unknown) > 0 {
		problems = append(problems, "git could not report "+strings.Join(s.Unknown, ", "))
	}
	for _, c := range []struct {
		n    int
		what string
	}{{s.Modified, "modified files"}, {s.Untracked, "untracked files"}, {s.Ignored, "ignored files outside artifact folders"},
		{s.Loose, "commits no branch holds"}, {s.Unpushed, "commits on no remote"}} {
		if c.n != 0 {
			problems = append(problems, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	if s.Locked {
		problems = append(problems, "it is locked")
	}
	switch {
	case tier == finding.TierA && !s.Merged.Proven():
		problems = append(problems, fmt.Sprintf("merged is %q, not ancestor, rebase or squash", s.Merged))
	case tier == finding.TierB && !s.Merged.Proven() && s.Merged != finding.MergedNo:
		problems = append(problems, fmt.Sprintf("merged is %q", s.Merged))
	case tier != finding.TierA && tier != finding.TierB:
		problems = append(problems, "only tier A and B worktrees are ever removed")
	}
	if len(problems) > 0 {
		return fmt.Errorf("the worktree %s cannot be removed: %s", s.Path, strings.Join(problems, ", "))
	}
	return nil
}

// validateWorktreePrune ties a GitWorktreePrune action to its repository:
// target, command and registrations.
func (a *Action) validateWorktreePrune() error {
	switch {
	case !cleanAbs(a.Repo):
		return fmt.Errorf("GitWorktreePrune needs the clean absolute path of its repository, got %q", a.Repo)
	case a.Target != WorktreePruneTarget(a.Repo):
		return errors.New("the target does not name the repository, the plan was edited inconsistently")
	case a.Path != "" || a.Head != "" || a.Worktree != nil || a.NeedsSudo:
		return errors.New("GitWorktreePrune must not carry a path, head, worktree state or sudo")
	case !slices.Equal(a.Command, WorktreePruneCommand(a.Repo)):
		return fmt.Errorf("command %q is not %q, the plan was edited inconsistently",
			strings.Join(a.Command, " "), strings.Join(WorktreePruneCommand(a.Repo), " "))
	case len(a.Registrations) == 0:
		return errors.New("GitWorktreePrune needs the registrations it covers")
	}
	for _, r := range a.Registrations {
		if !cleanAbs(r) {
			return fmt.Errorf("registration %q is not a clean absolute path", r)
		}
	}
	return nil
}
