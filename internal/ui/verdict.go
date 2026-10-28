package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/worktree"
)

// WorktreeWords is the verdict of a worktree finding in a few words, as a
// row of the findings tree shows it: "squash, PR #41 merged", "pushed, PR
// #44 open", "in use, pid 4242", "12 modified, 3 untracked", "stale lock,
// pid 50949 gone" or "outside the scanned roots". It is built from the
// verdict and the facts the scanner wrote, so it repeats no rule. A Codex
// task folder says that it is removed once empty. Other findings, such as
// stale registrations and other clones, are named by their own name and
// get an empty string.
func WorktreeWords(f *finding.Finding) string {
	s := f.Worktree
	if s == nil {
		if f.Kind == finding.KindEmptyDir {
			return "task folder, removed once empty"
		}
		return ""
	}
	switch s.Verdict {
	case finding.VerdictInUse:
		return "in use" + pidWords(s.LockReason, "") + prWords(s)
	case finding.VerdictAttention:
		return attentionWords(s) + prWords(s)
	}
	if !f.Actionable() {
		return "outside the scanned roots"
	}
	if s.Verdict == finding.VerdictPushed {
		if s.PR != nil {
			return "pushed, PR #" + strconv.Itoa(s.PR.Number) + " " + strings.ToLower(s.PR.State)
		}
		return "pushed, not merged"
	}
	method := string(s.Merged)
	if s.Merged == finding.MergedAncestor {
		method = "merged"
	}
	if s.PR == nil {
		return method
	}
	pr := "PR #" + strconv.Itoa(s.PR.Number)
	if method == "merged" && s.PR.State == "MERGED" {
		return method + ", " + pr
	}
	return method + ", " + pr + " " + strings.ToLower(s.PR.State)
}

// attentionWords lists in a few words what keeps a worktree from being
// removed: a stale lock, what git could not report, what blocks removal and
// the work that exists nowhere else.
func attentionWords(s *finding.WorktreeState) string {
	var parts []string
	if s.Locked && !s.LockLive {
		parts = append(parts, "stale lock"+pidWords(s.LockReason, " gone"))
	}
	blocked := []string{worktree.PartHidden, worktree.PartSubmodules, worktree.PartNested}
	if slices.ContainsFunc(s.Unknown, func(p string) bool { return !slices.Contains(blocked, p) }) {
		parts = append(parts, "not checkable")
	}
	for _, b := range []struct{ part, words string }{
		{worktree.PartNested, "nested repositories"}, {worktree.PartSubmodules, "submodules"}, {worktree.PartHidden, "hidden changes"},
	} {
		if slices.Contains(s.Unknown, b.part) {
			parts = append(parts, b.words)
		}
	}
	if s.Modified > 0 {
		parts = append(parts, strconv.Itoa(s.Modified)+" modified")
	}
	if s.Untracked > 0 {
		parts = append(parts, strconv.Itoa(s.Untracked)+" untracked")
	}
	if s.Loose > 0 {
		parts = append(parts, plural(s.Loose, "loose commit", "loose commits"))
	}
	if s.UnpushedBlocks() {
		parts = append(parts, strconv.Itoa(s.Unpushed)+" unpushed")
	}
	if s.Ignored > 0 {
		parts = append(parts, strconv.Itoa(s.Ignored)+" ignored")
	}
	if len(parts) == 0 && len(s.Reasons) > 0 {
		return s.Reasons[0]
	}
	if len(parts) == 0 {
		return "holds work"
	}
	return strings.Join(parts, ", ")
}

// prWords names the pull request of a worktree that is listed only, such as
// ", PR #2 merged", so its row says where its work went.
func prWords(s *finding.WorktreeState) string {
	if s.PR == nil {
		return ""
	}
	return ", PR #" + strconv.Itoa(s.PR.Number) + " " + strings.ToLower(s.PR.State)
}

// pidWords is ", pid N" and the suffix when the lock reason names a process.
func pidWords(reason, suffix string) string {
	if pid := lockPID(reason); pid != "" {
		return ", pid " + pid + suffix
	}
	return ""
}

// VerdictCounts says how many worktrees of a repository are in each state,
// "1 removable, 1 pushed, 1 work, 1 in use", in the few words the
// repository row of the findings tree has room for.
func VerdictCounts(r *RepoSummary) string {
	if r == nil {
		return ""
	}
	return strings.Join(verdictParts(r, "work"), ", ")
}

// RepoCounts says the same in full, "1 holding work", with the stale
// registrations and the other clones of the repository.
func RepoCounts(r *RepoSummary) string {
	if r == nil {
		return ""
	}
	parts := verdictParts(r, "holding work")
	if r.Stale > 0 {
		parts = append(parts, plural(r.Stale, "stale registration", "stale registrations"))
	}
	if r.Clones > 0 {
		parts = append(parts, plural(r.Clones, "other clone", "other clones"))
	}
	return strings.Join(parts, ", ")
}

func verdictParts(r *RepoSummary, attention string) []string {
	var parts []string
	for _, v := range []struct {
		verdict finding.Verdict
		words   string
	}{{finding.VerdictRemovable, "removable"}, {finding.VerdictPushed, "pushed"},
		{finding.VerdictAttention, attention}, {finding.VerdictInUse, "in use"}} {
		if n := r.Verdicts[v.verdict]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v.words))
		}
	}
	return parts
}

// HiddenChanges reports whether tracked files of a worktree are marked
// assume-unchanged or skip-worktree, which hides their changes from git
// status.
func HiddenChanges(s *finding.WorktreeState) bool {
	return slices.Contains(s.Unknown, worktree.PartHidden)
}
