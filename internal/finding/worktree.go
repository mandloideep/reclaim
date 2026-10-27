package finding

import (
	"strings"
	"time"
)

// Merged says whether the commits of a worktree are in the default branch of
// its repository, and how that was found.
type Merged string

// Merge states, in the order the checks run.
const (
	// MergedAncestor means HEAD is an ancestor of the default branch.
	MergedAncestor Merged = "ancestor"
	// MergedRebase means every commit of HEAD has an equivalent patch in the
	// default branch, as after a rebase merge.
	MergedRebase Merged = "rebase"
	// MergedSquash means the combined change of HEAD since the merge base is
	// a commit of the default branch, as after a squash merge.
	MergedSquash Merged = "squash"
	// MergedNo means the commits are not in the default branch.
	MergedNo Merged = "no"
	// MergedUnknown means git could not tell, for example because the
	// repository has no default branch.
	MergedUnknown Merged = "unknown"
)

// Proven reports whether the merge state proves the commits are in the
// default branch.
func (m Merged) Proven() bool {
	return m == MergedAncestor || m == MergedRebase || m == MergedSquash
}

// MergedRemotely reports whether the worktree's changes are proven to be,
// byte for byte, in a default branch that lives on a remote: an ancestor of
// it, or rebase or squash merged into it. Its commits may then be on no
// remote, as after GitHub deletes a merged branch, without any work being
// lost.
func (s *WorktreeState) MergedRemotely() bool {
	return s.Merged.Proven() && strings.HasPrefix(s.DefaultRef, "refs/remotes/")
}

// UnpushedBlocks reports whether the commits on no remote hold work that
// exists nowhere else: there are some, and the changes are not proven to be
// in a default branch on a remote.
func (s *WorktreeState) UnpushedBlocks() bool {
	return s.Unpushed > 0 && !s.MergedRemotely()
}

// Verdict is what reclaim concludes about a linked worktree.
type Verdict string

// Verdicts.
const (
	// VerdictRemovable is tier A: committed, pushed and merged, so the work
	// exists in the default branch.
	VerdictRemovable Verdict = "removable"
	// VerdictPushed is tier B: committed and pushed but not merged, so the
	// work is safe on the remote.
	VerdictPushed Verdict = "pushed"
	// VerdictAttention means the worktree holds work that exists nowhere
	// else, or its state could not be determined. Reclaim never removes it.
	VerdictAttention Verdict = "attention"
	// VerdictInUse means a running process holds the worktree's lock.
	VerdictInUse Verdict = "in-use"
)

// PullRequest is the GitHub pull request for a worktree's branch.
type PullRequest struct {
	// Number is the pull request number.
	Number int `json:"number"`
	// State is OPEN, CLOSED or MERGED, as gh reports it.
	State string `json:"state"`
	// URL is the pull request's web address.
	URL string `json:"url"`
}

// WorktreeState is the state of a linked git worktree. The scanner writes it
// on the finding, select copies it into the plan, and apply computes it again
// right before removing the worktree.
type WorktreeState struct {
	// Repo is the main repository: the folder holding the .git directory.
	Repo string `json:"repo"`
	// Path is the worktree folder.
	Path string `json:"path"`
	// Tool is the tool that made the worktree: claude-code, codex, the name
	// of a .worktrees or .conductor folder, or manual.
	Tool string `json:"tool"`
	// Branch is the checked out branch without refs/heads/, empty when HEAD
	// is detached.
	Branch string `json:"branch,omitempty"`
	// Detached is set when HEAD is not on a branch.
	Detached bool `json:"detached,omitempty"`
	// Head is the commit HEAD points at.
	Head string `json:"head"`
	// Upstream is the branch's upstream, such as origin/feature.
	Upstream string `json:"upstream,omitempty"`
	// Origin is the URL of the repository's origin remote.
	Origin string `json:"origin,omitempty"`
	// Exists reports whether the worktree folder exists.
	Exists bool `json:"exists"`
	// Size is the size of the worktree folder, measured by the walk that
	// looks for repositories inside it.
	Size int64 `json:"size,omitempty"`
	// Locked is set when git has a lock on the worktree.
	Locked bool `json:"locked,omitempty"`
	// LockReason is the reason recorded with the lock.
	LockReason string `json:"lock_reason,omitempty"`
	// LockLive is set when the process the lock names is running and, when
	// the reason records a start time, started at that time.
	LockLive bool `json:"lock_live,omitempty"`
	// Modified counts changed tracked files.
	Modified int `json:"modified"`
	// Untracked counts files git does not track and does not ignore.
	Untracked int `json:"untracked"`
	// Ignored counts ignored files and folders outside artifact folders such
	// as node_modules, which are lost with the worktree.
	Ignored int `json:"ignored"`
	// IgnoredSample names a few of them, such as .env.
	IgnoredSample []string `json:"ignored_sample,omitempty"`
	// Loose counts commits reachable from HEAD that no branch, remote or tag
	// holds.
	Loose int `json:"loose"`
	// Unpushed counts commits reachable from HEAD that no remote holds.
	Unpushed int `json:"unpushed"`
	// Ahead counts commits of HEAD that the upstream does not have.
	Ahead int `json:"ahead,omitempty"`
	// Merged says whether the commits are in the default branch.
	Merged Merged `json:"merged"`
	// DefaultBranch is the branch Merged was checked against, such as
	// origin/main.
	DefaultBranch string `json:"default_branch,omitempty"`
	// DefaultRef is the full ref of DefaultBranch, such as
	// refs/remotes/origin/main.
	DefaultRef string `json:"default_ref,omitempty"`
	// MergeNote explains a merge state, such as a patch that git cherry
	// matched but whose diff is not exactly in the default branch.
	MergeNote string `json:"merge_note,omitempty"`
	// FetchedAt is when the repository last fetched, from FETCH_HEAD. Merge
	// state is as of then.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// PR is the GitHub pull request for the branch, when the lookup ran.
	PR *PullRequest `json:"pr,omitempty"`
	// Unknown names the parts of the state git could not report, which make
	// the worktree one reclaim never removes.
	Unknown []string `json:"unknown,omitempty"`
	// Problem says why some of the state is unknown.
	Problem string `json:"problem,omitempty"`
	// Blockers say what makes the worktree impossible to remove safely
	// although git reported on it, such as a nested repository or a
	// submodule. Each also names a part in Unknown, so nothing removes it.
	Blockers []string `json:"blockers,omitempty"`
	// Verdict is what reclaim concludes from the state.
	Verdict Verdict `json:"verdict"`
	// Reasons say in words why the verdict is not removable, such as "58
	// modified and 6 untracked files".
	Reasons []string `json:"reasons,omitempty"`
	// Hints are commands that save the work before removing the worktree.
	// Reclaim prints them and never runs them.
	Hints []string `json:"hints,omitempty"`
	// ForceCommand removes the worktree whatever it holds. Reclaim prints it
	// for a person who has read the hints and never runs it.
	ForceCommand string `json:"force_command,omitempty"`
}
