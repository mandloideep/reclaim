package worktree

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
)

func TestParseList(t *testing.T) {
	out := "worktree /r\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /r/.claude/worktrees/a\x00HEAD bbb\x00branch refs/heads/feat/x\x00locked claude agent a (pid 1 start Mon Oct  5 11:42:02 2026)\x00\x00" +
		"worktree /tmp/gone\x00HEAD ccc\x00detached\x00prunable gitdir file points to non-existent location\x00\x00" +
		"worktree /r/locked-no-reason\x00HEAD ddd\x00branch refs/heads/l\x00locked\x00\x00"
	got := parseList(out)
	require.Equal(t, []Entry{
		{Path: "/r", Head: "aaa", Branch: "main", Main: true},
		{Path: "/r/.claude/worktrees/a", Head: "bbb", Branch: "feat/x", Locked: true, LockReason: "claude agent a (pid 1 start Mon Oct  5 11:42:02 2026)"},
		{Path: "/tmp/gone", Head: "ccc", Detached: true, Prunable: true, PrunableReason: "gitdir file points to non-existent location"},
		{Path: "/r/locked-no-reason", Head: "ddd", Branch: "l", Locked: true},
	}, got)
	repo := &Repo{Entries: got}
	require.Len(t, repo.Linked(), 2)
	require.Len(t, repo.Stale(), 1)
}

func TestParseStatus(t *testing.T) {
	cases := []struct {
		name                string
		out                 string
		modified, untracked int
	}{
		{"empty", "", 0, 0},
		{"modified and untracked", " M a.go\x00?? b.go\x00?? c/d.go\x00", 1, 2},
		{"first record lost its leading space", "M a.go\x00A  b.go\x00", 2, 0},
		{"rename carries the old path", "R  new.go\x00old.go\x00 M x.go\x00", 2, 0},
		{"copy carries the old path", "C  copy.go\x00orig.go\x00", 1, 0},
		{"ignored records are not counted", "!! .env\x00", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, u := parseStatus(tc.out)
			require.Equal(t, tc.modified, m)
			require.Equal(t, tc.untracked, u)
		})
	}
}

func TestIsArtifact(t *testing.T) {
	cases := map[string]bool{
		"node_modules/":                    true,
		"apps/portal/node_modules/":        true,
		"apps/portal/.next/":               true,
		"apps/portal/tsconfig.tsbuildinfo": true,
		"bin/":                             true,
		".build/":                          true,
		".cache/go-build/":                 true,
		".DS_Store":                        true,
		"pkg/__pycache__/x.pyc":            true,
		".env":                             false,
		".env.local":                       false,
		"local.sqlite":                     false,
		"qa/week-01/":                      false,
		"notes.md":                         false,
		".claude/":                         false,
	}
	for name, want := range cases {
		require.Equal(t, want, IsArtifact(name), name)
	}
}

func TestDefaultRef(t *testing.T) {
	cmd := "git -C /r for-each-ref --format=%(refname)%09%(symref) refs/remotes/origin/HEAD refs/remotes/origin/main refs/remotes/origin/master refs/heads/main refs/heads/master"
	cases := []struct {
		name, out, want string
	}{
		{"origin HEAD wins", "refs/heads/main\t\nrefs/remotes/origin/HEAD\trefs/remotes/origin/trunk\nrefs/remotes/origin/main\t\n", "refs/remotes/origin/trunk"},
		{"origin main", "refs/heads/master\t\nrefs/remotes/origin/main\t\n", "refs/remotes/origin/main"},
		{"origin master", "refs/remotes/origin/master\t\nrefs/heads/main\t\n", "refs/remotes/origin/master"},
		{"local main", "refs/heads/main\t\n", "refs/heads/main"},
		{"local master", "refs/heads/master\t\n", "refs/heads/master"},
		{"none", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{Exec: &execx.Fake{Tools: map[string]string{"git": "/usr/bin/git"}, Outputs: map[string]string{cmd: tc.out}}}
			got, err := defaultRef(context.Background(), env, "/r")
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestMergeStateFake covers the order of the merge checks and what each
// git answer means, without a repository.
func TestMergeStateFake(t *testing.T) {
	const (
		dir  = "/w"
		def  = "refs/remotes/origin/main"
		head = "1111"
		base = "2222"
	)
	g := func(args string) string { return "git -C " + dir + " " + args }
	commitTree := "git -C /w -c user.name=reclaim -c user.email=reclaim@localhost commit-tree tree-head -p 2222 -m reclaim squash merge check"
	// exact answers the commands that compare the diff from..to with the
	// commits of the default branch that touch the same file, whose diff is
	// upstream.
	exact := func(from, to, diff, upstream string) map[string]string {
		c := "u" + to
		return map[string]string{
			g("diff-tree -p --no-color --full-index " + from + " " + to):               diff,
			g("diff-tree -r --name-only -z " + from + " " + to):                        "f-" + to + "\x00",
			g("--literal-pathspecs rev-list --no-merges 2222.." + def + " -- f-" + to): "other " + c,
			g("diff-tree -p --no-color --full-index other^ other"):                     "unrelated diff",
			g("diff-tree -p --no-color --full-index " + c + "^ " + c):                  upstream,
		}
	}
	common := map[string]string{
		g("rev-parse HEAD"):                       head,
		g("merge-base HEAD " + def):               base,
		g("rev-parse HEAD^{tree}"):                "tree-head",
		g("rev-parse 2222^{tree}"):                "tree-base",
		commitTree:                                "3333",
		g("cherry " + def + " 3333 2222"):         "+ 3333",
		g("rev-list --count --merges 2222..HEAD"): "0",
		g("rev-list --no-merges 2222..HEAD"):      "1111\n0000",
	}
	with := func(overs ...map[string]string) map[string]string {
		out := maps.Clone(common)
		for _, over := range overs {
			for k, v := range over {
				if v == "<fail>" {
					delete(out, k)
					continue
				}
				out[k] = v
			}
		}
		return out
	}
	rebased := map[string]string{g("cherry " + def + " HEAD"): "- 1111\n- 0000\n"}
	squashed := map[string]string{g("cherry " + def + " HEAD"): "+ 1111\n", g("cherry " + def + " 3333 2222"): "- 3333\n"}
	cases := []struct {
		name    string
		outputs map[string]string
		want    finding.Merged
		note    string
		err     bool
	}{
		{"ancestor", with(map[string]string{g("merge-base HEAD " + def): head}), finding.MergedAncestor, "", false},
		{"rebase, exactly", with(rebased, exact("1111^", "1111", "d1", "d1"), exact("0000^", "0000", "d0", "d0")), finding.MergedRebase, "", false},
		{"rebase by patch id only falls to the squash check", with(rebased, exact("1111^", "1111", "d1", "d1"), exact("0000^", "0000", "d0", "d0 with other spacing")),
			finding.MergedNo, "", false},
		{"rebase with a merge commit falls to the squash check", with(rebased, map[string]string{g("rev-list --count --merges 2222..HEAD"): "1"},
			squashed, exact("2222", "HEAD", "ds", "ds")), finding.MergedSquash, "", false},
		{"partly rebased is not merged", with(map[string]string{g("cherry " + def + " HEAD"): "- 1111\n+ 0000\n"}), finding.MergedNo, "", false},
		{"squash, exactly", with(squashed, exact("2222", "HEAD", "ds", "ds")), finding.MergedSquash, "", false},
		{"squash by patch id only", with(squashed, exact("2222", "HEAD", "ds", "ds but whitespace differs")), finding.MergedNo,
			"the branch's changes are not exactly in origin/main", false},
		{"squash with no candidate commit", with(squashed, exact("2222", "HEAD", "ds", "ds"),
			map[string]string{g("--literal-pathspecs rev-list --no-merges 2222.." + def + " -- f-HEAD"): ""}), finding.MergedNo, "the branch's changes are not exactly in origin/main", false},
		{"no net change", with(map[string]string{g("cherry " + def + " HEAD"): "+ 1111\n", g("rev-parse 2222^{tree}"): "tree-head"}), finding.MergedSquash,
			"its commits add up to no change since origin/main", false},
		{"not merged", with(map[string]string{g("cherry " + def + " HEAD"): "+ 1111\n"}), finding.MergedNo, "", false},
		{"unrelated histories", with(map[string]string{g("merge-base HEAD " + def): "<fail>"}), finding.MergedNo, "", false},
		{"merge-base fails otherwise", with(map[string]string{g("merge-base HEAD " + def): "<fail>"}), finding.MergedUnknown, "", true},
		{"cherry fails", with(map[string]string{g("cherry " + def + " HEAD"): "<fail>"}), finding.MergedUnknown, "", true},
		{"commit-tree fails", with(map[string]string{g("cherry " + def + " HEAD"): "+ 1111\n", commitTree: "<fail>"}), finding.MergedUnknown, "", true},
		{"exact diff fails", with(squashed), finding.MergedUnknown, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &execx.Fake{Tools: map[string]string{"git": "/usr/bin/git"}, Outputs: tc.outputs}
			if tc.name == "unrelated histories" {
				fake.Exits = map[string]int{g("merge-base HEAD " + def): 1}
			}
			got, note, err := mergeState(context.Background(), Env{Exec: fake}, dir, def)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.note, note)
			require.Equal(t, tc.err, err != nil, "%v", err)
		})
	}
}

// TestUnknownStateIsNeverRemovable checks that a worktree git cannot report
// on is listed for attention, whatever else is known.
func TestUnknownStateIsNeverRemovable(t *testing.T) {
	s := &finding.WorktreeState{Exists: true, Merged: finding.MergedAncestor, Unknown: []string{"modified", "untracked"}, Problem: "git status failed"}
	Judge(s)
	require.Equal(t, finding.VerdictAttention, s.Verdict)
	require.Contains(t, s.Reasons[0], "git could not report modified, untracked")

	s = &finding.WorktreeState{Exists: true, Merged: finding.MergedUnknown}
	Judge(s)
	require.Equal(t, finding.VerdictAttention, s.Verdict)
	require.Equal(t, []string{"merge state unknown"}, s.Reasons)
}

func TestLockLive(t *testing.T) {
	recordedUTC := "claude agent a (pid 42 start Mon Oct  5 11:42:02 2026)"
	start := time.Date(2026, 10, 5, 11, 42, 2, 0, time.UTC).In(time.Local)
	psOut := start.Format("Mon Jan _2 15:04:05 2006")
	cases := []struct {
		name   string
		reason string
		fake   *execx.Fake
		want   bool
	}{
		{"no pid named", "kept on purpose", &execx.Fake{}, true},
		{"process gone", recordedUTC, &execx.Fake{Tools: map[string]string{"ps": "/bin/ps"},
			Exits: map[string]int{"ps -o lstart= -p 42": 1}}, false},
		{"process table unreadable", recordedUTC, &execx.Fake{Tools: map[string]string{"ps": "/bin/ps"}}, true},
		{"same process, start recorded in UTC", recordedUTC, &execx.Fake{Tools: map[string]string{"ps": "/bin/ps"},
			Outputs: map[string]string{"ps -o lstart= -p 42": psOut}}, true},
		{"same process, start recorded in local time", "claude agent a (pid 42 start " + psOut + ")", &execx.Fake{Tools: map[string]string{"ps": "/bin/ps"},
			Outputs: map[string]string{"ps -o lstart= -p 42": psOut}}, true},
		{"pid reused by a later process", recordedUTC, &execx.Fake{Tools: map[string]string{"ps": "/bin/ps"},
			Outputs: map[string]string{"ps -o lstart= -p 42": "Tue Oct 27 09:00:00 2026"}}, false},
		{"running, no start recorded", "locked by pid 42", &execx.Fake{Tools: map[string]string{"ps": "/bin/ps"},
			Outputs: map[string]string{"ps -o lstart= -p 42": psOut}}, true},
		{"no ps", recordedUTC, &execx.Fake{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, lockLive(context.Background(), Env{Exec: tc.fake}, tc.reason))
		})
	}
}

func TestGitHubRepo(t *testing.T) {
	cases := map[string]string{
		"git@github.com:mandloideep/reclaim.git":      "mandloideep/reclaim",
		"https://github.com/mandloideep/reclaim":      "mandloideep/reclaim",
		"https://github.com/mandloideep/reclaim.git/": "mandloideep/reclaim",
		"ssh://git@github.com/o/r.git":                "o/r",
		"https://token@github.com/o/r.git":            "o/r",
		"https://gitlab.com/o/r.git":                  "",
		"/tmp/origin.git":                             "",
		"":                                            "",
	}
	for in, want := range cases {
		got, ok := GitHubRepo(in)
		require.Equal(t, want != "", ok, in)
		require.Equal(t, want, got, in)
	}
}

// TestLookupPullRequests uses a fake gh: one call per branch, the merged
// pull request preferred, states never changed by the lookup, and a failure
// returned once while the other branches still get their answer.
func TestLookupPullRequests(t *testing.T) {
	gh := func(repo, branch string) string {
		return "gh pr list --repo " + repo + " --head " + branch + " --state all --json number,state,url"
	}
	fake := &execx.Fake{Tools: map[string]string{"gh": "/usr/bin/gh"}, Outputs: map[string]string{
		gh("o/r", "merged"): `[{"number":2,"state":"CLOSED","url":"u2"},{"number":3,"state":"MERGED","url":"u3"},{"number":5,"state":"OPEN","url":"u5"}]`,
		gh("o/r", "open"):   `[{"number":7,"state":"OPEN","url":"u7"}]`,
		gh("o/r", "none"):   `[]`,
	}}
	pushed := &finding.WorktreeState{Origin: "git@github.com:o/r.git", Branch: "open", Exists: true, Merged: finding.MergedNo}
	Judge(pushed)
	states := []*finding.WorktreeState{
		{Origin: "git@github.com:o/r.git", Branch: "merged", Exists: true, Merged: finding.MergedNo},
		{Origin: "https://github.com/o/r", Branch: "merged", Exists: true, Merged: finding.MergedNo},
		pushed,
		{Origin: "git@github.com:o/r.git", Branch: "none", Exists: true, Merged: finding.MergedNo},
		{Origin: "git@github.com:o/r.git", Branch: "broken", Exists: true, Merged: finding.MergedNo},
		{Origin: "/tmp/origin.git", Branch: "local", Exists: true},
		{Origin: "git@github.com:o/r.git", Detached: true, Exists: true},
	}
	for _, s := range states {
		Judge(s)
	}
	err := LookupPullRequests(context.Background(), Env{Exec: fake}, states)
	require.Error(t, err)
	require.ErrorContains(t, err, "broken")
	require.Equal(t, 3, states[0].PR.Number)
	require.Same(t, states[0].PR, states[1].PR, "one lookup per branch")
	require.Equal(t, "u7", states[2].PR.URL)
	require.Contains(t, states[2].Hints, "review and merge u7")
	require.Equal(t, finding.VerdictPushed, states[0].Verdict, "a merged pull request never changes the verdict")
	require.Nil(t, states[3].PR)
	require.Nil(t, states[4].PR)
	require.Nil(t, states[5].PR)
	require.Nil(t, states[6].PR)

	err = LookupPullRequests(context.Background(), Env{Exec: &execx.Fake{}}, states)
	require.True(t, errors.Is(err, execx.ErrNotInstalled))
}

func TestQuote(t *testing.T) {
	require.Equal(t, "/a/b-c_d.e", Quote("/a/b-c_d.e"))
	require.Equal(t, "'/a/b c'", Quote("/a/b c"))
	require.Equal(t, `'it'\''s'`, Quote("it's"))
	require.Equal(t, "''", Quote(""))
}
