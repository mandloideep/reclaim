package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/ui"
)

// detailLine is one line of the details pane before wrapping, with how to
// style it.
type detailLine struct {
	text  string
	style detailStyle
}

type detailStyle int

const (
	plainLine detailStyle = iota
	boldLine
	dimLine
	warnLine
)

// details describes the row under the cursor of the Review screen in full.
// Everything it says comes from the finding, its group or the report: the
// verdict, reasons, hints and force command of a worktree are the ones the
// scanner wrote, so the pane repeats no rule.
func (m *Model) details(r *finding.Report, it ui.TreeItem) []detailLine {
	switch it.Kind {
	case ui.TreeFinding:
		return m.findingDetails(r, it.Finding)
	case ui.TreeGroup:
		return m.groupDetails(it)
	case ui.TreeCategory:
		return []detailLine{
			{it.Category.Title(), boldLine},
			{fmt.Sprintf("%s, %s reclaimable", plural(it.Count, "item", "items"), sizeText(it.Size)), plainLine},
			{fmt.Sprintf("%d selected", it.SelectedCount), plainLine},
			{"space ticks its tier A and B items; tier C is ticked one by one.", dimLine},
		}
	case ui.TreeWarnings:
		return []detailLine{
			{"Warnings and notes", boldLine},
			{"What the scan could not read or wants you to know. They are never selectable; enter opens the list.", dimLine},
		}
	case ui.TreeWarning:
		return []detailLine{{it.Text, warnLine}}
	}
	return nil
}

func (m *Model) findingDetails(r *finding.Report, f *finding.Finding) []detailLine {
	var out []detailLine
	add := func(text string, style detailStyle) {
		if text != "" {
			out = append(out, detailLine{text, style})
		}
	}
	// The path, or for a target that is no path, such as a Docker image or
	// a repository's stale registrations, its name and then the target.
	what, name := f.Path, f.Name
	switch {
	case what == "" && f.Name != "":
		what, name = f.Name, ""
	case what == "":
		what = f.Target
	}
	add(m.path(what), boldLine)
	if name != "" && name != f.Path {
		add(name, plainLine)
	}
	if f.Action == finding.ActionGitWorktreePrune {
		for _, r := range f.Registrations {
			add("  "+m.path(r)+", gone", dimLine)
		}
	}
	add(sizeText(f.Size)+", "+tierWords(f), plainLine)
	add("Scanner: "+f.Scanner, plainLine)
	if f.Restore != "" {
		add("Restore: "+m.plain.ShortCommand(f.Restore), plainLine)
	}
	if !f.LastUsed.IsZero() {
		add("Last used: "+m.ago(f.LastUsed)+" ("+f.LastUsed.In(m.opts.Now().Location()).Format("2006-01-02")+")", plainLine)
	}
	// A worktree that is listed only says why in its reasons below.
	if f.Warning != "" && (f.Worktree == nil || f.Actionable()) {
		add("! "+f.Warning, warnLine)
	}
	if f.Scanner == "claude-sessions" {
		add("Claude Code cannot resume these sessions.", plainLine)
		if gone, ok := strings.CutPrefix(f.Name, "claude sessions of "); ok {
			add("Project folder gone: "+m.path(gone), plainLine)
		}
	}
	if f.Category == finding.CategoryDocker {
		out = append(out, m.dockerDetails(f)...)
	}
	if f.Worktree != nil {
		out = append(out, m.worktreeDetails(f.Worktree)...)
	}
	if f.Path != "" {
		if root := coveringRoot(f.Path, r.Roots); root != "" {
			add("In root "+m.path(root)+".", plainLine)
		} else {
			add("Outside every recorded root, so apply refuses it.", warnLine)
		}
	}
	if f.Project != "" {
		add("Project "+m.path(f.Project)+", which apply never removes.", plainLine)
	}
	if f.Actionable() {
		add("Apply checks before removing: "+checkWords(f), dimLine)
		act := plan.FromFinding(f)
		line := m.plain.ActionLine(&act)
		if f.NeedsSudo {
			line += ", printed, never run"
		}
		add("Apply will: "+m.plain.ShortCommand(line), plainLine)
	} else {
		add("Listed for attention only: reclaim never removes it, whatever is selected.", dimLine)
	}
	return out
}

// tierWords says what a finding's tier means.
func tierWords(f *finding.Finding) string {
	if !f.Actionable() {
		return "listed for attention only"
	}
	switch {
	case f.Action == finding.ActionGitWorktreePrune:
		return "tier " + string(f.Tier) + ": registrations of worktrees whose folders are gone; pruning them lets git worktree add reuse the paths"
	case f.Category == finding.CategoryWorktree && f.Kind == finding.KindEmptyDir:
		return "tier " + string(f.Tier) + ": a task folder, removed only once the worktrees in it are gone"
	case f.Category == finding.CategoryWorktree && f.Tier == finding.TierA:
		return "tier A: committed, pushed and merged, so the work is in the default branch"
	case f.Category == finding.CategoryWorktree && f.Tier == finding.TierB:
		return "tier B: committed and pushed but not merged, so the work is safe on the remote"
	}
	switch f.Tier {
	case finding.TierA:
		return "tier A: rebuilds itself the next time the project builds or installs"
	case finding.TierB:
		return "tier B: can be downloaded or rebuilt again, at a cost in time or bandwidth"
	default:
		return "tier C: may be data that cannot be restored; it is ticked only on its own"
	}
}

// checkWords says in one line what apply checks right before it acts on a
// finding of this kind.
func checkWords(f *finding.Finding) string {
	switch f.Action {
	case finding.ActionRemovePath:
		kind := "still a folder"
		switch f.Kind {
		case finding.KindFile:
			kind = "still a file"
		case finding.KindEmptyDir:
			kind = "still a folder and empty"
		}
		return "inside the root, not a protected folder, not the project root, no .git entry or project manifest, " +
			"no symbolic link, not a mount point, " + kind + "."
	case finding.ActionRunCommand:
		if f.NeedsSudo {
			return "nothing runs: it needs administrator rights, so the command is printed for you."
		}
		return "only an allowlisted command, and for a cache the tool reported, the tool reports the same folder again."
	case finding.ActionDockerRemoveContainer:
		return "the container is still stopped; nothing is forced."
	case finding.ActionDockerRemoveImage:
		return "no container uses the image and its tags are unchanged; tags go one by one, nothing is forced."
	case finding.ActionDockerRemoveVolume:
		return "no running container uses the volume; nothing is forced."
	case finding.ActionDockerPruneBuildCache:
		return "only build cache no build uses is pruned."
	case finding.ActionGitWorktreeRemove:
		return "linked worktree, the path rules, the state computed again (committed, pushed, merged, unlocked), " +
			"the same commit, not the working directory, then git worktree remove without --force."
	case finding.ActionGitWorktreePrune:
		return "git worktree prune --dry-run again; refused unless every registration it names is in the plan and its folder is gone."
	}
	return "every check of its kind."
}

func (m *Model) dockerDetails(f *finding.Finding) []detailLine {
	out := []detailLine{{"Kind: " + f.DockerKind(), plainLine}}
	if len(f.Tags) > 0 {
		out = append(out, detailLine{"Tags: " + strings.Join(f.Tags, ", "), plainLine})
	}
	if !f.LastUsed.IsZero() {
		verb := "Created"
		if f.Action == finding.ActionDockerRemoveContainer {
			verb = "Last ran"
		}
		out = append(out, detailLine{verb + ": " + m.ago(f.LastUsed), plainLine})
	}
	uses := "Used by: no container"
	switch f.Action {
	case finding.ActionDockerRemoveContainer:
		uses = "A stopped container; its image and named volumes stay."
	case finding.ActionDockerRemoveVolume:
		if f.Tier == finding.TierC {
			uses = "Used by: stopped containers, named in the warning"
		}
	case finding.ActionDockerPruneBuildCache:
		uses = "Used by: no build in progress"
	}
	return append(out, detailLine{uses, plainLine})
}

// worktreeDetails shows everything the scanner found about a worktree:
// the verdict and reasons, where it comes from, what it holds, the merge,
// the pull request, the lock, the fetch, what git could not report, and the
// hints and force command when it holds work.
func (m *Model) worktreeDetails(s *finding.WorktreeState) []detailLine {
	var out []detailLine
	add := func(text string, style detailStyle) { out = append(out, detailLine{text, style}) }
	add("", plainLine)
	add("Verdict: "+verdictWords(s.Verdict), boldLine)
	for _, r := range s.Reasons {
		add("  "+r, warnLine)
	}
	add("Repository "+m.path(s.Repo)+", made by "+s.Tool, plainLine)
	branch := "Branch " + s.Branch + " at " + shortSHA(s.Head)
	if s.Branch == "" {
		branch = "Detached at " + shortSHA(s.Head)
	}
	if s.Upstream != "" {
		branch += ", upstream " + s.Upstream
	}
	add(branch, plainLine)
	hidden := "no hidden changes"
	if ui.HiddenChanges(s) {
		hidden = "changes hidden by assume-unchanged or skip-worktree files"
	}
	ignored := strconv.Itoa(s.Ignored) + " ignored outside artifact folders"
	if len(s.IgnoredSample) > 0 {
		ignored += " (" + strings.Join(s.IgnoredSample, ", ") + ")"
	}
	add(fmt.Sprintf("Uncommitted: %d modified, %d untracked, %s, %s", s.Modified, s.Untracked, ignored, hidden), plainLine)
	add(fmt.Sprintf("Commits: %d on no remote, %d no branch holds", s.Unpushed, s.Loose), plainLine)
	merged := "Merged: " + string(s.Merged)
	if s.DefaultBranch != "" {
		merged += " into " + s.DefaultBranch
	}
	add(merged, plainLine)
	if s.MergeNote != "" {
		add("  "+s.MergeNote, dimLine)
	}
	if s.PR != nil {
		add(fmt.Sprintf("Pull request: #%d %s %s", s.PR.Number, strings.ToLower(s.PR.State), s.PR.URL), plainLine)
	}
	switch {
	case s.Locked && s.LockLive:
		add("Lock: held by a running process: "+s.LockReason, warnLine)
	case s.Locked:
		add("Lock: stale, its process is gone: "+s.LockReason, warnLine)
	default:
		add("Lock: none", plainLine)
	}
	if !s.FetchedAt.IsZero() {
		add("Fetched "+m.ago(s.FetchedAt)+"; merge state is as of that fetch.", dimLine)
	} else if s.DefaultBranch != "" {
		add("Never fetched; merge state is as of the last clone or pull.", dimLine)
	}
	if len(s.Unknown) > 0 {
		add("git could not report: "+strings.Join(s.Unknown, ", ")+problem(s.Problem), warnLine)
	}
	for _, b := range s.Blockers {
		add("  "+b, warnLine)
	}
	if len(s.Hints) > 0 {
		add("Save the work first:", plainLine)
		for _, h := range s.Hints {
			add("  "+m.plain.ShortCommand(h), plainLine)
		}
	}
	if s.ForceCommand != "" {
		add("Remove anyway, losing it:", plainLine)
		add("  "+m.plain.ShortCommand(s.ForceCommand), plainLine)
	}
	add("", plainLine)
	return out
}

// verdictWords says what a verdict means.
func verdictWords(v finding.Verdict) string {
	switch v {
	case finding.VerdictRemovable:
		return "removable: committed, pushed and merged"
	case finding.VerdictPushed:
		return "pushed, but not merged"
	case finding.VerdictInUse:
		return "in use by a running process"
	default:
		return "holds work that exists nowhere else, or could not be checked"
	}
}

func problem(p string) string {
	if p == "" {
		return ""
	}
	return ": " + p
}

// within reports whether path is parent or lies below it. Both are clean.
func within(path, parent string) bool {
	sep := string(filepath.Separator)
	return path == parent || strings.HasPrefix(path, strings.TrimSuffix(parent, sep)+sep)
}

// groupDetails describes a group: a repository of worktrees with its origin,
// default branch, fetch time and counts, or any other group with its size.
func (m *Model) groupDetails(it ui.TreeItem) []detailLine {
	g := it.Group
	out := []detailLine{
		{g.Title, boldLine},
		{fmt.Sprintf("%s, %s reclaimable, %d selected", plural(it.Count, "item", "items"), sizeText(it.Size), it.SelectedCount), plainLine},
	}
	if g.Attention {
		out = append(out, detailLine{"Listed for attention only; not counted, never removed.", dimLine})
	}
	if r := g.Repo; r != nil {
		if r.Origin != "" {
			out = append(out, detailLine{"Origin: " + r.Origin, plainLine})
		}
		if r.DefaultBranch != "" {
			out = append(out, detailLine{"Default branch: " + r.DefaultBranch, plainLine})
		}
		if !r.FetchedAt.IsZero() {
			out = append(out, detailLine{"Fetched " + m.ago(r.FetchedAt) + "; merge state is as of that fetch.", dimLine})
		} else {
			out = append(out, detailLine{"Never fetched; merge state is as of the last clone or pull.", dimLine})
		}
		if counts := ui.RepoCounts(r); counts != "" {
			out = append(out, detailLine{counts, plainLine})
		}
	} else if g.Project != "" {
		out = append(out, detailLine{"Project " + m.path(g.Project) + ", which apply never removes.", plainLine})
	}
	return out
}

// coveringRoot returns the deepest recorded root that holds path, the root
// apply checks it against.
func coveringRoot(path string, roots []string) string {
	best := ""
	for _, r := range roots {
		if path != r && within(path, r) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
