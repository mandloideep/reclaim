package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeWorktree describes a linked worktree of the screenshot fixture: the
// answers git, ps and gh give about it, without running them.
type fakeWorktree struct {
	path, name, branch, head string
	merged                   string // ancestor, squash or no
	modified, untracked      int
	lock                     string
	pr                       string // gh's JSON answer, empty for none
	size                     int64
}

// fakeRepo is a repository of the screenshot fixture with its worktrees and
// the registrations whose folders are gone.
type fakeRepo struct {
	path, origin string
	fetched      time.Time
	worktrees    []fakeWorktree
	stale        []string
}

// addFakeRepo writes the repository's registrations and its worktrees' .git
// files and contents, and the answers of every git, ps and gh command the
// worktree scanners run for them.
func addFakeRepo(t *testing.T, r fakeRepo, out map[string]string) {
	t.Helper()
	const def = "refs/remotes/origin/main"
	git := func(dir, args string) string { return "git -C " + dir + " " + args }
	gitDir := filepath.Join(r.path, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("x\n"), 0o644))
	require.NoError(t, os.Chtimes(filepath.Join(gitDir, "FETCH_HEAD"), r.fetched, r.fetched))

	var list strings.Builder
	list.WriteString("worktree " + r.path + "\x00HEAD 0000000000000000000000000000000000000000\x00branch refs/heads/main\x00\x00")
	register := func(name, path string) {
		reg := filepath.Join(gitDir, "worktrees", name)
		require.NoError(t, os.MkdirAll(reg, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(reg, "gitdir"), []byte(filepath.Join(path, ".git")+"\n"), 0o644))
		sparse(t, filepath.Join(reg, "index"), 400_000)
	}
	for _, w := range r.worktrees {
		register(w.name, w.path)
		list.WriteString("worktree " + w.path + "\x00HEAD " + w.head + "\x00")
		if w.branch != "" {
			list.WriteString("branch refs/heads/" + w.branch + "\x00")
		} else {
			list.WriteString("detached\x00")
		}
		if w.lock != "" {
			list.WriteString("locked " + w.lock + "\x00")
		}
		list.WriteString("\x00")
		require.NoError(t, os.MkdirAll(w.path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(w.path, ".git"), []byte("gitdir: "+filepath.Join(gitDir, "worktrees", w.name)+"\n"), 0o644))
		sparse(t, filepath.Join(w.path, "src", "app.ts"), w.size)

		status := make([]string, 0, w.modified+w.untracked)
		for i := range w.modified {
			status = append(status, " M src/file"+strconv.Itoa(i)+".ts")
		}
		for i := range w.untracked {
			status = append(status, "?? notes/n"+strconv.Itoa(i)+".md")
		}
		out[git(w.path, "status --porcelain=v1 -z --untracked-files=all --ignore-submodules=none")] = strings.Join(status, "\x00")
		out[git(w.path, "ls-files --others --ignored --exclude-standard --directory -z")] = "node_modules/\x00"
		out[git(w.path, "rev-list --count HEAD --not --branches --remotes --tags")] = "0"
		out[git(w.path, "rev-list --count HEAD --not --remotes")] = "0"
		out[git(w.path, "rev-parse HEAD")] = w.head
		if w.branch != "" {
			out[git(w.path, "for-each-ref --format=%(upstream) refs/heads/"+w.branch)] = "refs/remotes/origin/" + w.branch
			out[git(w.path, "rev-list --count refs/remotes/origin/"+w.branch+"..HEAD")] = "0"
			out["gh pr list --repo acme/storefront --head "+w.branch+" --state all --json number,state,url"] = w.pr
		}
		base := "b" + w.head[1:]
		if w.merged == "ancestor" {
			base = w.head
		}
		out[git(w.path, "merge-base HEAD "+def)] = base
		out[git(w.path, "cherry "+def+" HEAD")] = "+ " + w.head
		out[git(w.path, "rev-parse HEAD^{tree}")] = "tree-" + w.name
		out[git(w.path, "rev-parse "+base+"^{tree}")] = "tree-base"
		squash := "s" + w.head[1:]
		out["git -C "+w.path+" -c user.name=reclaim -c user.email=reclaim@localhost commit-tree tree-"+w.name+" -p "+base+" -m reclaim squash merge check"] = squash
		mark := "+ "
		if w.merged == "squash" {
			mark = "- "
		}
		out[git(w.path, "cherry "+def+" "+squash+" "+base)] = mark + squash
	}
	for i, path := range r.stale {
		name := "old" + strconv.Itoa(i)
		register(name, path)
		list.WriteString("worktree " + path + "\x00HEAD 1111111111111111111111111111111111111111\x00detached\x00prunable gitdir file points to non-existent location\x00\x00")
	}
	out[git(r.path, "worktree list --porcelain -z")] = list.String()
	out[git(r.path, "config --get remote.origin.url")] = r.origin
	out[git(r.path, "for-each-ref --format=%(refname)%09%(symref) refs/remotes/origin/HEAD refs/remotes/origin/main refs/remotes/origin/master refs/heads/main refs/heads/master")] =
		"refs/remotes/origin/HEAD\t" + def + "\n" + def + "\t\n"
}
