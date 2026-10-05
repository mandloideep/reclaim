package scan

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
)

// Select returns the scanners matching any selector. A selector is a
// category such as "docker", an ecosystem such as "node" or a scanner name
// such as "node_modules". No selectors selects every scanner. An unknown
// selector is an error so typos do not silently scan nothing.
func Select(all []Scanner, selectors []string) ([]Scanner, error) {
	if len(selectors) == 0 {
		return all, nil
	}
	var out []Scanner
	for _, sel := range selectors {
		sel = strings.TrimSpace(sel)
		if sel == "" {
			continue
		}
		matched := false
		for _, s := range all {
			if s.Name() == sel || string(s.Category()) == sel || s.Ecosystem() == sel {
				matched = true
				if !slices.ContainsFunc(out, func(o Scanner) bool { return o.Name() == s.Name() }) {
					out = append(out, s)
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("unknown category, ecosystem or scanner %q (see reclaim scanners)", sel)
		}
	}
	return out, nil
}

// OnlyCategory returns the scanners in category c.
func OnlyCategory(all []Scanner, c finding.Category) []Scanner {
	var out []Scanner
	for _, s := range all {
		if s.Category() == c {
			out = append(out, s)
		}
	}
	return out
}

// Filter narrows a list of findings.
type Filter struct {
	// MinSize drops findings smaller than this many bytes.
	MinSize int64
	// Tiers keeps only these tiers. Empty keeps all.
	Tiers []finding.Tier
	// Stale keeps only project findings whose last use is older than this.
	// Zero disables the check. Findings in other categories are not affected.
	Stale time.Duration
	// Now is the reference time for Stale.
	Now time.Time
}

// Apply returns the findings that pass the filter, in the same order.
func (f Filter) Apply(in []finding.Finding) []finding.Finding {
	out := make([]finding.Finding, 0, len(in))
	for _, x := range in {
		if x.Size < f.MinSize {
			continue
		}
		if len(f.Tiers) > 0 && !slices.Contains(f.Tiers, x.Tier) {
			continue
		}
		if f.Stale > 0 && x.Category == finding.CategoryProject {
			if !x.LastUsed.IsZero() && f.Now.Sub(x.LastUsed) < f.Stale {
				continue
			}
		}
		out = append(out, x)
	}
	return out
}
