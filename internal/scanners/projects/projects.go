// Package projects holds the project artifact scanners: dependency folders,
// build outputs and tool caches that live inside projects.
//
// Every rule is its own scanner with its own name, and every scanner is also
// a project.Matcher. All project scanners share one walk of the roots through
// scan.Env.Projects, so adding a rule does not add a walk.
package projects

import (
	"context"
	"errors"
	"sync"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
)

// verdict is what a rule decides about one artifact folder.
type verdict struct {
	tier    finding.Tier
	restore string
	warning string
}

// rule describes one kind of artifact folder.
type rule struct {
	name        string
	ecosystem   string
	description string
	match       func(d *project.Dir) bool
	classify    func(d *project.Dir) verdict
}

// Scanner reports the artifact folders recognized by one rule.
type Scanner struct {
	r rule
}

var (
	_ scan.Scanner    = (*Scanner)(nil)
	_ project.Matcher = (*Scanner)(nil)
)

// Name implements scan.Scanner and project.Matcher.
func (s *Scanner) Name() string { return s.r.name }

// Category implements scan.Scanner.
func (s *Scanner) Category() finding.Category { return finding.CategoryProject }

// Ecosystem implements scan.Scanner.
func (s *Scanner) Ecosystem() string { return s.r.ecosystem }

// Description implements scan.Scanner.
func (s *Scanner) Description() string { return s.r.description }

// Match implements project.Matcher.
func (s *Scanner) Match(d *project.Dir) bool { return s.r.match(d) }

// sizeConcurrency bounds how many artifact folders one scanner measures at once.
const sizeConcurrency = 4

// Scan implements scan.Scanner.
func (s *Scanner) Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error) {
	if env.Projects == nil {
		return nil, errors.New("no project source configured")
	}
	idx, err := env.Projects.Index(ctx)
	if err != nil {
		return nil, err
	}
	arts := idx.ArtifactsFor(s.Name())
	out := make([]finding.Finding, len(arts))
	keep := make([]bool, len(arts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, sizeConcurrency)
	for i, a := range arts {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()
			size, err := env.Size(ctx, a.Dir.Path)
			if err != nil {
				if ctx.Err() == nil {
					env.Diag.Warn(a.Dir.Path, "could not measure: "+err.Error())
				}
				return
			}
			if size == 0 {
				return
			}
			out[i] = s.finding(a, size)
			keep[i] = true
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var res []finding.Finding
	for i := range out {
		if keep[i] {
			res = append(res, out[i])
		}
	}
	return res, nil
}

func (s *Scanner) finding(a project.Artifact, size int64) finding.Finding {
	v := s.r.classify(a.Dir)
	f := finding.Finding{
		Tier:     v.tier,
		Path:     a.Dir.Path,
		Kind:     finding.KindDir,
		Target:   a.Dir.Path,
		Size:     size,
		LastUsed: a.ModTime,
		Restore:  v.restore,
		Action:   finding.ActionRemovePath,
		Warning:  v.warning,
	}
	if a.Project != nil {
		f.Project = a.Project.Root
		if t := a.Project.LastActivity(); !t.IsZero() {
			f.LastUsed = t
		}
	}
	return f
}

// Scanners returns one scanner per rule, in matching order.
func Scanners() []*Scanner {
	rules := rules()
	out := make([]*Scanner, len(rules))
	for i, r := range rules {
		out[i] = &Scanner{r: r}
	}
	return out
}

// Matchers returns the project scanners as matchers for project.Discover.
func Matchers(scanners []*Scanner) []project.Matcher {
	out := make([]project.Matcher, len(scanners))
	for i, s := range scanners {
		out[i] = s
	}
	return out
}
