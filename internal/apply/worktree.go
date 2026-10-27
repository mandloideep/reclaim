package apply

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/worktree"
)

// Check is the result of one step of the proof that removing a worktree is
// safe. Every check is written to the apply log.
type Check struct {
	// Name is the check, such as "linked worktree".
	Name string `json:"name"`
	// OK reports whether it passed.
	OK bool `json:"ok"`
	// Detail says what was found.
	Detail string `json:"detail,omitempty"`
}

// The checks of DESIGN.md's "Removing a worktree", in the order they run.
const (
	CheckLinked  = "linked worktree"
	CheckPath    = "path"
	CheckState   = "work exists elsewhere"
	CheckHead    = "head unchanged"
	CheckWorkdir = "not the working directory"
	CheckRemove  = "git worktree remove"
)

// Refusal explains why a worktree was not removed: the check that failed
// and, when work would be lost, the commands that save it and the command
// that removes the worktree anyway, which reclaim never runs.
type Refusal struct {
	// Path is the worktree.
	Path string
	// Check is the check that failed.
	Check string
	// Detail says why.
	Detail string
	// Hints are commands that save the work, from the state computed now.
	Hints []string
	// ForceCommand removes the worktree whatever it holds.
	ForceCommand string
	// State is the state computed during the proof, nil when the proof
	// stopped before computing it.
	State *finding.WorktreeState
}

// Error implements error.
func (r *Refusal) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to remove the worktree %s: %s: %s", r.Path, r.Check, r.Detail)
	if len(r.Hints) > 0 {
		b.WriteString("\n  to keep the work, first run:")
		for _, h := range r.Hints {
			b.WriteString("\n    " + h)
		}
	}
	if r.ForceCommand != "" {
		b.WriteString("\n  to remove it anyway, losing that work, run it yourself:\n    " + r.ForceCommand)
	}
	return b.String()
}

// Proof is the outcome of the checks that run before a worktree is removed.
type Proof struct {
	// Checks are the checks that ran, in order. The last one failed when
	// the proof was refused.
	Checks []Check
	// State is the state computed now, nil when the proof stopped earlier.
	State *finding.WorktreeState
	// checked is the worktree folder as the path check saw it.
	checked fs.FileInfo
	// size is the worktree's size, measured by the path check's walk.
	size int64
}

// ProveWorktree runs the first five checks of the proof for a
// GitWorktreeRemove action: the path is a linked worktree of the recorded
// repository at the recorded head, it passes every RemovePath check, its
// state computed now with the scanner's code shows no work that exists
// nowhere else, its head did not move, and it does not hold the working
// directory. The sixth check is git's own refusal when apply runs the
// removal. Callers that write a plan run it before writing, and apply runs
// it again right before acting. The error is a *Refusal.
func ProveWorktree(ctx context.Context, opts Options, roots []string, a *plan.Action) (*Proof, error) {
	pr := &Proof{}
	fail := func(check, detail string) (*Proof, error) {
		pr.Checks = append(pr.Checks, Check{Name: check, Detail: detail})
		ref := &Refusal{Path: a.Path, Check: check, Detail: detail, State: pr.State}
		if s := pr.State; s != nil && s.Verdict != finding.VerdictRemovable && s.Verdict != finding.VerdictPushed {
			ref.Hints, ref.ForceCommand = s.Hints, s.ForceCommand
		}
		return pr, ref
	}
	pass := func(check, detail string) {
		pr.Checks = append(pr.Checks, Check{Name: check, OK: true, Detail: detail})
	}
	if a.Action != finding.ActionGitWorktreeRemove {
		return fail(CheckLinked, fmt.Sprintf("action %s is not %s", a.Action, finding.ActionGitWorktreeRemove))
	}
	env := worktree.Env{Exec: opts.Exec}

	// 1. A linked worktree of the recorded repository at the recorded head.
	if err := checkLinked(ctx, env, a); err != nil {
		return fail(CheckLinked, err.Error())
	}
	pass(CheckLinked, "registered in "+filepath.Join(a.Repo, ".git", "worktrees")+" at "+a.Head)

	// 2. Every RemovePath check that applies to a folder holding a .git file.
	info, size, err := checkWorktreePath(ctx, opts, roots, a)
	if err != nil {
		return fail(CheckPath, err.Error())
	}
	pr.checked, pr.size = info, size
	pass(CheckPath, "inside a recorded root, not protected, no symbolic link, not a mount point")

	// 3. The state computed now with the scanner's code.
	state, err := worktree.Classify(ctx, env, a.Path)
	if err != nil {
		return fail(CheckState, err.Error())
	}
	pr.State = state
	if state.Repo != a.Repo {
		return fail(CheckState, fmt.Sprintf("it now belongs to %s, not %s", state.Repo, a.Repo))
	}
	if err := plan.RemovableState(state, a.Tier); err != nil {
		detail := err.Error()
		if len(state.Reasons) > 0 {
			detail += " (" + strings.Join(state.Reasons, "; ") + ")"
		}
		return fail(CheckState, detail)
	}
	pass(CheckState, stateWords(state))

	// 4. No commits since the plan was written.
	if state.Head != a.Head {
		return fail(CheckHead, fmt.Sprintf("HEAD is now %s, the plan was made at %s", state.Head, a.Head))
	}
	pass(CheckHead, a.Head)

	// 5. Never the folder the process runs in.
	getwd := opts.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	wd, err := getwd()
	if err != nil {
		return fail(CheckWorkdir, "cannot tell the working directory: "+err.Error())
	}
	if resolved, err := filepath.EvalSymlinks(wd); err == nil {
		wd = resolved
	}
	if project.IsWithin(wd, a.Path) {
		return fail(CheckWorkdir, "reclaim is running inside it, at "+wd+"; run it from the main checkout "+a.Repo)
	}
	pass(CheckWorkdir, wd)
	return pr, nil
}

// checkLinked checks that path's .git is a file naming a registration under
// <repo>/.git/worktrees, that the registration points back at it, and that
// git lists it at the recorded head. A folder holding a .git directory, a
// main checkout or a project root, is never a linked worktree.
func checkLinked(ctx context.Context, env worktree.Env, a *plan.Action) error {
	loc, err := worktree.Locate(a.Path)
	if err != nil {
		return err
	}
	if loc.Repo != a.Repo {
		return fmt.Errorf("its .git file names the repository %s, the plan names %s", loc.Repo, a.Repo)
	}
	back, err := os.ReadFile(filepath.Join(loc.GitDir, "gitdir"))
	if err != nil {
		return fmt.Errorf("read the registration %s: %w", loc.GitDir, err)
	}
	backPath := strings.TrimSpace(string(back))
	if !filepath.IsAbs(backPath) {
		backPath = filepath.Join(loc.GitDir, backPath)
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(backPath)); err != nil || resolved != filepath.Join(a.Path, ".git") {
		return fmt.Errorf("the registration %s points at %s, not at this worktree", loc.GitDir, backPath)
	}
	entries, err := worktree.List(ctx, env, a.Repo)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Path != a.Path && project.IsWithin(e.Path, a.Path) {
			return fmt.Errorf("another worktree of %s, %s, lies inside it", a.Repo, e.Path)
		}
	}
	for _, e := range entries {
		if e.Path != a.Path {
			if resolved, err := filepath.EvalSymlinks(e.Path); err != nil || resolved != a.Path {
				continue
			}
		}
		switch {
		case e.Main || e.Bare:
			return fmt.Errorf("git lists it as the main worktree of %s", a.Repo)
		case e.Prunable:
			return fmt.Errorf("git lists it as prunable: %s", e.PrunableReason)
		case e.Head != a.Head:
			return fmt.Errorf("git lists it at %s, the plan was made at %s", e.Head, a.Head)
		}
		return nil
	}
	return fmt.Errorf("git -C %s worktree list does not list it", a.Repo)
}

// checkWorktreePath applies the RemovePath checks to a worktree folder:
// inside a recorded root, not protected, not the repository or a folder
// holding it, no symbolic link in it, still a directory, not a mount point,
// and no other filesystem mounted inside. Unlike RemovePath it expects the
// .git file that makes the folder a worktree.
func checkWorktreePath(ctx context.Context, opts Options, roots []string, a *plan.Action) (fs.FileInfo, int64, error) {
	if err := checkProtected(a.Path, opts.Home, a.Repo, roots); err != nil {
		return nil, 0, err
	}
	info, err := os.Lstat(a.Path)
	if err != nil {
		return nil, 0, fmt.Errorf("inspect %s: %w", a.Path, err)
	}
	if err := checkShape(a, info); err != nil {
		return nil, 0, err
	}
	walker := opts.Walker
	if walker == nil {
		walker = fsx.NewWalker(0)
	}
	res, err := walker.Size(ctx, a.Path)
	if err != nil {
		return nil, 0, err
	}
	if len(res.Mounts) > 0 {
		return nil, 0, fmt.Errorf("refusing %s: another filesystem is mounted inside it at %s", a.Path, res.Mounts[0])
	}
	return info, res.Size, nil
}

// stateWords summarizes a state that passed the check.
func stateWords(s *finding.WorktreeState) string {
	return fmt.Sprintf("nothing uncommitted, untracked, ignored, loose or unpushed; merged: %s", s.Merged)
}

// removeWorktree proves the worktree safe to remove, immediately before
// acting, and removes it with git worktree remove, never forced, so git's
// own refusal to remove modified or untracked files is the last guard.
func (r *runner) removeWorktree(ctx context.Context, p *plan.Plan, a *plan.Action) Outcome {
	if _, err := os.Lstat(a.Path); errors.Is(err, fs.ErrNotExist) {
		return goneWorktree(a)
	}
	proof, err := ProveWorktree(ctx, r.opts, p.Roots, a)
	if err != nil {
		var ref *Refusal
		o := Outcome{Checks: proof.Checks, Err: err}
		if errors.As(err, &ref) {
			o.Refusal = ref
		}
		return o
	}
	checks := proof.Checks
	if r.opts.Exec == nil {
		return Outcome{Checks: checks, Err: errors.New("no command runner configured")}
	}
	// The last look before git acts, which guards what git itself does
	// not: the same folder the checks saw, with no symbolic link in its
	// path, at the same HEAD, and still with nothing uncommitted, ignored
	// outside artifact folders or hidden from git status.
	refuse := func(detail string) Outcome {
		checks = append(checks, Check{Name: CheckRemove, Detail: detail})
		ref := &Refusal{Path: a.Path, Check: CheckRemove, Detail: detail, State: proof.State}
		return Outcome{Checks: checks, Err: ref, Refusal: ref}
	}
	if resolved, err := filepath.EvalSymlinks(a.Path); err != nil || resolved != a.Path {
		return refuse("the path changed since it was checked")
	}
	if now, err := os.Lstat(a.Path); err != nil || !os.SameFile(now, proof.checked) {
		return refuse("it was replaced since it was checked")
	}
	if err := worktree.Recheck(ctx, worktree.Env{Exec: r.opts.Exec}, a.Path, a.Head); err != nil {
		return refuse(err.Error())
	}
	out, err := r.opts.Exec.Run(ctx, plan.WorktreeRemoveCommand(a.Repo, a.Path))
	output := truncate(string(out))
	if err != nil {
		checks = append(checks, Check{Name: CheckRemove, Detail: firstLineOf(output)})
		ref := &Refusal{Path: a.Path, Check: CheckRemove, Detail: "git refused: " + firstLineOf(output), State: proof.State}
		if proof.State != nil {
			ref.ForceCommand = worktree.ForceCommand(proof.State)
		}
		return Outcome{Checks: checks, Output: output, Err: ref, Refusal: ref}
	}
	checks = append(checks, Check{Name: CheckRemove, OK: true, Detail: "removed"})
	freed := proof.size
	if _, err := os.Lstat(a.Path); err == nil {
		after, _ := r.opts.Walker.Size(ctx, a.Path)
		freed = max(proof.size-after.Size, 0)
		return Outcome{Checks: checks, Output: output, Freed: freed,
			Err: fmt.Errorf("git worktree remove reported success, but %s is still there", a.Path)}
	}
	if note := r.afterRemove(ctx, a); note != "" {
		output = strings.TrimSpace(output + "\n" + note)
	}
	return Outcome{Checks: checks, Output: output, Freed: freed}
}

// goneWorktree reports a worktree whose folder is gone. Its registration may
// remain, which a GitWorktreePrune finding covers in a later scan.
func goneWorktree(a *plan.Action) Outcome {
	return Outcome{Err: errGone, Checks: []Check{{Name: CheckLinked, Detail: a.Path + " no longer exists"}}}
}

// afterRemove looks, after a removal, at what git worktree prune would
// still remove, with a dry run. git worktree remove deletes the worktree's
// own registration, so there is normally nothing; registrations of other
// folders, such as a worktree on a drive that is not mounted, are not this
// plan's to prune, so apply never runs the prune here and only notes them.
// What it finds is a note for the log, never a failure: the worktree is gone.
func (r *runner) afterRemove(ctx context.Context, a *plan.Action) string {
	cands, err := worktree.PruneCandidates(ctx, worktree.Env{Exec: r.opts.Exec}, a.Repo)
	switch {
	case err != nil:
		return "could not check for stale registrations: " + err.Error()
	case len(cands) == 0:
		return ""
	}
	names := make([]string, 0, len(cands))
	for _, c := range cands {
		names = append(names, c.Path)
	}
	return fmt.Sprintf("git worktree prune not run: it would remove %d other registrations, which this plan does not cover: %s",
		len(cands), strings.Join(names, ", "))
}

// pruneWorktrees removes stale registrations of a repository with git
// worktree prune. It asks git first, with a dry run, what the prune would
// remove, and refuses unless every one of them is in the plan and names a
// folder that is gone, so a registration whose folder still holds work is
// never orphaned.
func (r *runner) pruneWorktrees(ctx context.Context, a *plan.Action) Outcome {
	info, err := os.Lstat(filepath.Join(a.Repo, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		return Outcome{Err: errGone}
	}
	if err != nil || !info.IsDir() {
		return Outcome{Err: fmt.Errorf("refusing to prune %s: it has no .git directory", a.Repo)}
	}
	if resolved, err := filepath.EvalSymlinks(a.Repo); err != nil || resolved != a.Repo {
		return Outcome{Err: fmt.Errorf("refusing to prune %s: the path goes through a symbolic link", a.Repo)}
	}
	if r.opts.Exec == nil {
		return Outcome{Err: errors.New("no command runner configured")}
	}
	cands, err := worktree.PruneCandidates(ctx, worktree.Env{Exec: r.opts.Exec}, a.Repo)
	if err != nil {
		return Outcome{Err: err}
	}
	if len(cands) == 0 {
		return Outcome{Err: errGone}
	}
	for _, c := range cands {
		switch {
		case !slices.Contains(a.Registrations, c.Path):
			return Outcome{Err: fmt.Errorf("refusing to prune %s: git would also prune the registration of %s, which the plan does not list; scan again", a.Repo, c.Path)}
		case !c.Gone:
			return Outcome{Err: fmt.Errorf("refusing to prune %s: git would prune the registration of %s (%s), whose folder may still hold work", a.Repo, c.Path, c.Reason)}
		}
	}
	out, err := r.opts.Exec.Run(ctx, plan.WorktreePruneCommand(a.Repo))
	o := Outcome{Output: truncate(string(out)), Freed: a.Size, Estimated: true}
	if err != nil {
		o.Err = err
	}
	return o
}

func firstLineOf(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}
