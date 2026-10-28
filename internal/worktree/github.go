package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
)

// gitHubRemote matches the owner and name of a repository on github.com in
// an https, ssh or scp style remote URL.
var gitHubRemote = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com/|git@github\.com:)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// GitHubRepo returns "owner/name" for a remote URL on github.com.
func GitHubRepo(origin string) (string, bool) {
	m := gitHubRemote.FindStringSubmatch(strings.TrimSpace(origin))
	if m == nil {
		return "", false
	}
	return m[1] + "/" + m[2], true
}

// lookupTimeout bounds one gh call.
const lookupTimeout = 8 * time.Second

// lookupConcurrency bounds the gh calls running at once.
const lookupConcurrency = 6

// lookupKey is one branch of one GitHub repository.
type lookupKey struct{ repo, branch string }

// lookupKeys returns the branches LookupPullRequests asks about, in the
// order of the states, and the states of each.
func lookupKeys(states []*finding.WorktreeState) ([]lookupKey, map[lookupKey][]*finding.WorktreeState) {
	byKey := map[lookupKey][]*finding.WorktreeState{}
	var keys []lookupKey
	for _, s := range states {
		repo, ok := GitHubRepo(s.Origin)
		if !ok || s.Branch == "" {
			continue
		}
		k := lookupKey{repo, s.Branch}
		if _, seen := byKey[k]; !seen {
			keys = append(keys, k)
		}
		byKey[k] = append(byKey[k], s)
	}
	return keys, byKey
}

// PullRequestLookups counts the branches LookupPullRequests would ask
// GitHub about, for progress reports.
func PullRequestLookups(states []*finding.WorktreeState) int {
	keys, _ := lookupKeys(states)
	return len(keys)
}

// LookupPullRequests asks GitHub, through gh, for the pull request of every
// branch whose repository's origin is on GitHub, once per branch, and sets
// PR on the states. It never changes a verdict: the commit based checks are
// what prove the work exists locally. It returns the first failure, which
// callers report as one warning; states without an answer are left as they
// were.
func LookupPullRequests(ctx context.Context, env Env, states []*finding.WorktreeState) error {
	keys, byKey := lookupKeys(states)
	if len(keys) == 0 {
		return nil
	}
	if env.Exec == nil {
		return errors.New("gh is not available")
	}
	if _, err := env.Exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh: %w", err)
	}
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, lookupConcurrency)
	)
	for _, k := range keys {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			pr, err := lookupPullRequest(ctx, env, k.repo, k.branch)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for _, s := range byKey[k] {
				s.PR = pr
				// The hints mention the pull request, so they are made again.
				Judge(s)
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil && firstErr == nil {
		return err
	}
	return firstErr
}

// lookupPullRequest returns the pull request for a branch: a merged one
// first, then an open one, then the newest, or nil when there is none.
func lookupPullRequest(ctx context.Context, env Env, repo, branch string) (*finding.PullRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	out, err := env.Exec.Output(ctx, "gh", "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--json", "number,state,url")
	if err != nil {
		return nil, fmt.Errorf("look up the pull request of %s in %s: %w", branch, repo, err)
	}
	var prs []finding.PullRequest
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("read the pull requests of %s in %s: %w", branch, repo, err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	rank := map[string]int{"MERGED": 0, "OPEN": 1}
	slices.SortStableFunc(prs, func(a, b finding.PullRequest) int {
		ra, oka := rank[a.State]
		rb, okb := rank[b.State]
		if !oka {
			ra = 2
		}
		if !okb {
			rb = 2
		}
		if ra != rb {
			return ra - rb
		}
		return b.Number - a.Number
	})
	return &prs[0], nil
}
