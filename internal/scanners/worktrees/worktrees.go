// Package worktrees holds the scanners for linked git worktrees: the ones of
// every repository the project walk finds, the ones Codex makes for its
// tasks, and the registrations whose folders are gone.
//
// Classification comes from internal/worktree, which apply and reclaim done
// use too, so all three reach the same answer. Every scanner is read only:
// git is asked, never told.
package worktrees

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/worktree"
)

// spec describes one worktree scanner.
type spec struct {
	name        string
	ecosystem   string
	description string
	rootBased   bool
	scan        func(ctx context.Context, env *scan.Env) ([]finding.Finding, error)
}

// Scanner is one worktree scanner.
type Scanner struct {
	s spec
}

var (
	_ scan.Scanner   = (*Scanner)(nil)
	_ scan.RootBased = (*Scanner)(nil)
)

// Name implements scan.Scanner.
func (s *Scanner) Name() string { return s.s.name }

// Category implements scan.Scanner.
func (s *Scanner) Category() finding.Category { return finding.CategoryWorktree }

// Ecosystem implements scan.Scanner.
func (s *Scanner) Ecosystem() string { return s.s.ecosystem }

// Description implements scan.Scanner.
func (s *Scanner) Description() string { return s.s.description }

// RootBased implements scan.RootBased.
func (s *Scanner) RootBased() bool { return s.s.rootBased }

// Scan implements scan.Scanner.
func (s *Scanner) Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error) {
	return s.s.scan(ctx, &env)
}

// Scanners returns every worktree scanner.
func Scanners() []*Scanner {
	specs := []spec{
		{name: "git-worktrees", ecosystem: "git", rootBased: true, scan: scanGitWorktrees,
			description: "linked git worktrees of the repositories under the roots, removed only when their work is pushed and merged"},
		{name: "codex-worktrees", ecosystem: "codex", scan: scanCodexWorktrees,
			description: "git worktrees Codex made for tasks, one per repository, and the task folders once empty"},
		{name: "worktree-registrations", ecosystem: "git", rootBased: true, scan: scanRegistrations,
			description: "registrations of linked worktrees whose folders are gone, removed with git worktree prune"},
	}
	out := make([]*Scanner, len(specs))
	for i, s := range specs {
		out[i] = &Scanner{s: s}
	}
	return out
}

// concurrency bounds the worktrees classified at once.
const concurrency = 8

func wenv(env *scan.Env) worktree.Env {
	return worktree.Env{Exec: env.Exec, CodexHome: env.CodexHome()}
}

// noGit reports whether git is missing, and notes it when there were
// worktrees to check.
func noGit(env *scan.Env, worktrees bool) bool {
	missing := env.Exec == nil
	if !missing {
		_, err := env.Exec.LookPath("git")
		missing = err != nil
	}
	if missing && worktrees {
		env.Diag.Note("git is not installed, so git worktrees were not checked.")
	}
	return missing
}

// repos returns the repositories the project walk found, folders holding a
// .git directory, and the main repositories of the linked worktrees it met,
// which may lie outside the roots.
func repos(ctx context.Context, env *scan.Env) ([]string, error) {
	if env.Projects == nil {
		return nil, nil
	}
	idx, err := env.Projects.Index(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for _, p := range idx.Projects {
		if !p.Git {
			continue
		}
		info, err := os.Lstat(filepath.Join(p.Root, ".git"))
		switch {
		case err != nil:
		case info.IsDir():
			add(p.Root)
		default:
			if loc, err := worktree.Locate(p.Root); err == nil {
				add(loc.Repo)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// hasLinked reports whether a repository has any worktree registration.
func hasLinked(repo string) bool {
	entries, err := os.ReadDir(filepath.Join(repo, ".git", "worktrees"))
	return err == nil && len(entries) > 0
}

// openRepos opens the repositories that have worktree registrations,
// concurrently, warning about the ones git cannot read.
func openRepos(ctx context.Context, env *scan.Env, paths []string) []*worktree.Repo {
	var with []string
	for _, r := range paths {
		if hasLinked(r) {
			with = append(with, r)
		}
	}
	out := make([]*worktree.Repo, len(with))
	parallel(ctx, len(with), func(i int) {
		r, err := worktree.OpenRepo(ctx, wenv(env), with[i])
		if err != nil {
			env.Diag.Warn(with[i], "could not list its worktrees: "+err.Error())
			return
		}
		out[i] = r
	})
	return slices.DeleteFunc(out, func(r *worktree.Repo) bool { return r == nil })
}

// parallel runs fn for 0..n-1 with a bound, stopping early when ctx is done.
func parallel(ctx context.Context, n int, fn func(i int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			fn(i)
		})
	}
	wg.Wait()
}

// underCodex reports whether path is in Codex's worktrees folder, which the
// codex-worktrees scanner reports.
func underCodex(env *scan.Env, path string) bool {
	return project.IsWithin(path, filepath.Join(env.CodexHome(), "worktrees")) && path != filepath.Join(env.CodexHome(), "worktrees")
}

// scanGitWorktrees reports every linked worktree of the repositories under
// the roots, and other clones of the same origin.
func scanGitWorktrees(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	paths, err := repos(ctx, env)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 || noGit(env, slices.ContainsFunc(paths, hasLinked)) {
		return nil, nil
	}
	var targets []target
	for _, r := range openRepos(ctx, env, paths) {
		for _, e := range r.Linked() {
			if !underCodex(env, e.Path) {
				targets = append(targets, target{repo: r, entry: e})
			}
		}
	}
	classified, err := classifyAll(ctx, env, targets)
	if err != nil {
		return nil, err
	}
	clones, err := otherClones(ctx, env, paths)
	if err != nil {
		return nil, err
	}
	return append(found(classified), clones...), nil
}

// target is one linked worktree to classify.
type target struct {
	repo  *worktree.Repo
	entry worktree.Entry
}

// classifyAll classifies the targets concurrently, asks GitHub for their
// pull requests, and turns them into findings, in the order of the targets.
// A worktree that is not a finding, because a running process holds it, is
// nil.
func classifyAll(ctx context.Context, env *scan.Env, targets []target) ([]*finding.Finding, error) {
	states := make([]*finding.WorktreeState, len(targets))
	parallel(ctx, len(targets), func(i int) {
		states[i] = worktree.ClassifyEntry(ctx, wenv(env), targets[i].repo, targets[i].entry)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if env.Worktrees.GitHub {
		if err := worktree.LookupPullRequests(ctx, wenv(env), states); err != nil {
			env.Diag.Warn("", "pull request state is not shown: "+err.Error())
		}
	}
	out := make([]*finding.Finding, len(states))
	for i, s := range states {
		f, ok, err := toFinding(ctx, env, s)
		if err != nil {
			return nil, err
		}
		if ok {
			out[i] = &f
		}
	}
	return out, nil
}

// found keeps the findings that are not nil.
func found(fs []*finding.Finding) []finding.Finding {
	out := make([]finding.Finding, 0, len(fs))
	for _, f := range fs {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// toFinding turns a state into a finding: tier A or B with GitWorktreeRemove
// when the work exists elsewhere, listed for attention otherwise. A worktree
// a running process holds is reported only when env.Worktrees.InUse asks
// for it.
func toFinding(ctx context.Context, env *scan.Env, s *finding.WorktreeState) (finding.Finding, bool, error) {
	if s.Verdict == finding.VerdictInUse && !env.Worktrees.InUse {
		return finding.Finding{}, false, nil
	}
	f := finding.Finding{
		Path:     s.Path,
		Kind:     finding.KindDir,
		Target:   s.Path,
		Name:     Label(s),
		Project:  s.Repo,
		Repo:     s.Repo,
		Worktree: s,
	}
	if s.Exists {
		size, err := env.Size(ctx, s.Path)
		if err != nil {
			if ctx.Err() != nil {
				return finding.Finding{}, false, ctx.Err()
			}
			env.Diag.Warn(s.Path, "could not measure: "+err.Error())
		}
		f.Size = size
		// git names a registration after the folder, with a number added
		// when the name is taken, so the .git file says which one it is.
		if loc, err := worktree.Locate(s.Path); err == nil {
			if info, err := os.Stat(filepath.Join(loc.GitDir, "index")); err == nil {
				f.LastUsed = info.ModTime()
			}
		}
	}
	switch {
	case s.Verdict == finding.VerdictInUse:
		attention(&f, "in use, "+strings.Join(s.Reasons, "; "))
	case s.Verdict == finding.VerdictAttention:
		attention(&f, strings.Join(s.Reasons, "; "))
	case !env.Removable(s.Path):
		attention(&f, "outside the scanned folders and the home folder, so reclaim does not remove it; "+
			strings.Join(plan.WorktreeRemoveCommand(worktree.Quote(s.Repo), worktree.Quote(s.Path)), " ")+" removes it")
	case s.Verdict == finding.VerdictRemovable:
		f.Tier = finding.TierA
		f.Action = finding.ActionGitWorktreeRemove
		f.Command = plan.WorktreeRemoveCommand(s.Repo, s.Path)
		f.Restore = restore(s)
	default:
		f.Tier = finding.TierB
		f.Action = finding.ActionGitWorktreeRemove
		f.Command = plan.WorktreeRemoveCommand(s.Repo, s.Path)
		f.Restore = restore(s)
		f.Warning = pushedWords(s)
	}
	return f, true, nil
}

func attention(f *finding.Finding, warning string) {
	f.Tier = finding.TierC
	f.Action = finding.ActionNone
	f.Warning = warning
}

// Label is the short name of a worktree in tables: its tool and its branch,
// or the commit of a detached HEAD.
func Label(s *finding.WorktreeState) string {
	ref := s.Branch
	if ref == "" {
		ref = "detached " + shortSHA(s.Head)
	}
	return s.Tool + " " + ref
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// restore says how to get a removed worktree back, and how to delete its
// branch, which reclaim never does.
func restore(s *finding.WorktreeState) string {
	parts := []string{worktree.RestoreCommand(s)}
	if s.Branch != "" && s.Merged.Proven() {
		parts = append(parts, "the branch stays; delete it with git -C "+worktree.Quote(s.Repo)+" branch -d "+worktree.Quote(s.Branch))
	}
	if s.PR != nil && s.PR.State == "MERGED" && !s.Merged.Proven() {
		parts = append(parts, fmt.Sprintf("merged in PR #%d; fetch origin to see it locally", s.PR.Number))
	}
	return strings.Join(parts, "; ")
}

// pushedWords is the warning of a tier B worktree: pushed, not merged.
func pushedWords(s *finding.WorktreeState) string {
	switch {
	case s.PR != nil && s.PR.State == "MERGED":
		return fmt.Sprintf("pushed and merged in PR #%d, but the merge is not in %s yet; fetch origin", s.PR.Number, s.DefaultBranch)
	case s.PR != nil:
		return fmt.Sprintf("pushed, not merged; PR #%d is %s", s.PR.Number, strings.ToLower(s.PR.State))
	default:
		return "pushed, not merged into " + s.DefaultBranch
	}
}

// scanRegistrations reports, per repository, the registrations that git
// worktree prune would remove, as one GitWorktreePrune finding, asking git
// with a dry run. It offers them only when every one names a folder that is
// gone; otherwise a prune could orphan a folder that still holds work, so
// the registrations are listed for attention.
func scanRegistrations(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	paths, err := repos(ctx, env)
	if err != nil {
		return nil, err
	}
	paths = slices.DeleteFunc(paths, func(r string) bool { return !hasLinked(r) })
	if len(paths) == 0 || noGit(env, true) {
		return nil, nil
	}
	cands := make([][]worktree.PruneCandidate, len(paths))
	parallel(ctx, len(paths), func(i int) {
		c, err := worktree.PruneCandidates(ctx, wenv(env), paths[i])
		if err != nil {
			env.Diag.Warn(paths[i], err.Error())
			return
		}
		cands[i] = c
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []finding.Finding
	for i, repo := range paths {
		if len(cands[i]) == 0 {
			continue
		}
		f, err := registrationsFinding(ctx, env, repo, cands[i])
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// registrationsFinding turns the prune candidates of one repository into a
// finding.
func registrationsFinding(ctx context.Context, env *scan.Env, repo string, cands []worktree.PruneCandidate) (finding.Finding, error) {
	regs := make([]string, 0, len(cands))
	var size int64
	var unsafe []string
	for _, c := range cands {
		regs = append(regs, c.Path)
		n, err := env.Size(ctx, filepath.Join(repo, ".git", "worktrees", c.Name))
		if err != nil && ctx.Err() != nil {
			return finding.Finding{}, ctx.Err()
		}
		size += n
		if !c.Gone {
			why := c.Reason
			if _, err := os.Lstat(c.Path); err == nil && c.Path != filepath.Join(repo, ".git", "worktrees", c.Name) {
				why = "the folder still exists without its .git file"
			}
			unsafe = append(unsafe, c.Path+" ("+why+")")
		}
	}
	name := "1 stale worktree registration"
	if len(regs) != 1 {
		name = fmt.Sprintf("%d stale worktree registrations", len(regs))
	}
	f := finding.Finding{
		Tier:          finding.TierA,
		Target:        plan.WorktreePruneTarget(repo),
		Name:          name,
		Size:          size,
		Project:       repo,
		Repo:          repo,
		Registrations: regs,
		Action:        finding.ActionGitWorktreePrune,
		Command:       plan.WorktreePruneCommand(repo),
		Restore:       "nothing to restore: the folders are gone, and git worktree add can use their paths again",
	}
	if len(unsafe) > 0 {
		f.Command, f.Restore = nil, ""
		attention(&f, "git worktree prune would remove the registrations of "+strings.Join(unsafe, ", ")+
			"; those folders may still hold work, so check them, then remove them or run git worktree repair, before pruning")
	}
	return f, nil
}

// scanCodexWorktrees reports each repository in each Codex task folder as a
// worktree, classified like any other, and the task folder itself, removed
// once empty, when every worktree in it can be removed.
func scanCodexWorktrees(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := filepath.Join(env.CodexHome(), "worktrees")
	tasks, err := subdirs(dir)
	if err != nil {
		// An unreadable Codex folder is a warning, not a failed scanner.
		env.Diag.Warn(dir, "could not read: "+err.Error())
		return nil, nil //nolint:nilerr // reported as a warning
	}
	if len(tasks) == 0 || noGit(env, true) {
		return nil, nil
	}
	// Every repository of every task is classified in one batch, so the
	// pull request lookups run concurrently.
	type task struct {
		dir     string
		targets []int
		other   []finding.Finding
		// stays is set when the folder holds something that is not a
		// worktree reclaim can classify.
		stays bool
	}
	cache := map[string]*worktree.Repo{}
	var all []target
	var list []task
	for _, name := range tasks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t := task{dir: filepath.Join(dir, name)}
		entries, err := os.ReadDir(t.dir)
		if err != nil {
			env.Diag.Warn(t.dir, "could not read: "+err.Error())
			continue
		}
		for _, e := range entries {
			path := filepath.Join(t.dir, e.Name())
			if !e.IsDir() {
				t.stays = true
				continue
			}
			tg, why := codexTarget(ctx, env, path, cache)
			if why != "" {
				f, err := unknownFolder(ctx, env, path, why)
				if err != nil {
					return nil, err
				}
				t.other = append(t.other, f)
				t.stays = true
				continue
			}
			t.targets = append(t.targets, len(all))
			all = append(all, tg)
		}
		list = append(list, t)
	}
	classified, err := classifyAll(ctx, env, all)
	if err != nil {
		return nil, err
	}
	var out []finding.Finding
	for _, t := range list {
		out = append(out, t.other...)
		tier := finding.TierA
		stays := t.stays
		// The task folder is listed under its repository when all its
		// worktrees share one, as they usually do.
		repo := ""
		for n, i := range t.targets {
			f := classified[i]
			if f == nil || f.Action != finding.ActionGitWorktreeRemove {
				stays = true
			}
			if f == nil {
				continue
			}
			if f.Tier == finding.TierB {
				tier = finding.TierB
			}
			if n == 0 {
				repo = f.Repo
			} else if repo != f.Repo {
				repo = ""
			}
			out = append(out, *f)
		}
		if stays {
			continue
		}
		out = append(out, finding.Finding{
			Tier:    tier,
			Path:    t.dir,
			Kind:    finding.KindEmptyDir,
			Target:  t.dir,
			Name:    "codex task folder " + filepath.Base(t.dir),
			Repo:    repo,
			Restore: "Codex makes a new task folder for the next task",
			Warning: "removed only once the worktrees in it are gone",
			Action:  finding.ActionRemovePath,
		})
	}
	return out, nil
}

// codexTarget finds the repository and the registration of one repository
// folder of a Codex task, or says why it cannot be classified.
func codexTarget(ctx context.Context, env *scan.Env, path string, cache map[string]*worktree.Repo) (target, string) {
	loc, err := worktree.Locate(path)
	if err != nil {
		return target{}, "not a linked git worktree, so reclaim cannot tell what it holds"
	}
	repo, ok := cache[loc.Repo]
	if !ok {
		repo, err = worktree.OpenRepo(ctx, wenv(env), loc.Repo)
		if err != nil {
			return target{}, "git could not read its repository " + loc.Repo
		}
		cache[loc.Repo] = repo
	}
	entry, ok := repo.Entry(path)
	if !ok {
		return target{}, "its repository " + loc.Repo + " no longer lists it as a worktree"
	}
	return target{repo: repo, entry: entry}, ""
}

// unknownFolder lists a folder reclaim cannot classify for attention.
func unknownFolder(ctx context.Context, env *scan.Env, path, why string) (finding.Finding, error) {
	f := finding.Finding{Path: path, Kind: finding.KindDir, Target: path, Name: "codex " + filepath.Base(path)}
	size, err := env.Size(ctx, path)
	if err != nil && ctx.Err() != nil {
		return finding.Finding{}, ctx.Err()
	}
	f.Size = size
	attention(&f, why+"; reclaim never removes it")
	return f, nil
}

// otherClones lists, for every origin with more than one clone under the
// roots, the clones after the first under the first one's repository, for
// attention only: reclaim cannot know which clone the user wants.
func otherClones(ctx context.Context, env *scan.Env, repos []string) ([]finding.Finding, error) {
	origins := make([]string, len(repos))
	parallel(ctx, len(repos), func(i int) {
		if out, err := env.Exec.Output(ctx, "git", "-C", repos[i], "config", "--get", "remote.origin.url"); err == nil {
			origins[i] = strings.TrimSpace(out)
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byOrigin := map[string][]int{}
	var keys []string
	for i, o := range origins {
		k := originKey(o)
		if k == "" {
			continue
		}
		if _, ok := byOrigin[k]; !ok {
			keys = append(keys, k)
		}
		byOrigin[k] = append(byOrigin[k], i)
	}
	slices.Sort(keys)
	var out []finding.Finding
	for _, k := range keys {
		idx := byOrigin[k]
		if len(idx) < 2 {
			continue
		}
		primary := repos[idx[0]]
		for _, i := range idx[1:] {
			f := finding.Finding{Path: repos[i], Kind: finding.KindDir, Target: repos[i], Name: "another clone of this origin",
				Project: primary, Repo: primary}
			size, err := env.Size(ctx, repos[i])
			if err != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			f.Size = size
			attention(&f, "another clone of "+origins[i]+", also cloned at "+primary+"; reclaim cannot know which one you want, so it never offers a clone")
			out = append(out, f)
		}
	}
	return out, nil
}

// originKey normalizes a remote URL so the ssh and https forms of the same
// repository compare equal.
func originKey(origin string) string {
	o := strings.TrimSpace(origin)
	if o == "" {
		return ""
	}
	if gh, ok := worktree.GitHubRepo(o); ok {
		return "github.com/" + strings.ToLower(gh)
	}
	o = strings.TrimSuffix(strings.TrimSuffix(o, "/"), ".git")
	if _, rest, ok := strings.Cut(o, "://"); ok {
		o = rest
	} else if host, path, ok := strings.Cut(o, ":"); ok && !strings.HasPrefix(o, "/") {
		o = host + "/" + path
	}
	if _, rest, ok := strings.Cut(o, "@"); ok {
		o = rest
	}
	return strings.ToLower(o)
}

// subdirs returns the names of the real directories in dir, sorted, without
// hidden ones. A missing dir has none.
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
