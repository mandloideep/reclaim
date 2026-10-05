package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
)

// Section is one category of a report with its findings in groups. The
// table, the Markdown summary, the numbered list and the checklist all show
// findings in this shape, so they always agree.
type Section struct {
	// Category is the category of every finding in the section.
	Category finding.Category
	// Groups hold the findings. A section whose findings are not grouped,
	// such as package caches, has one group with an empty Key.
	Groups []Group
	// Size is the reclaimable size of the section, attention findings excluded.
	Size int64
	// Count is the number of findings that can be applied.
	Count int
}

// Flat reports whether the section lists its findings without group headings.
func (s *Section) Flat() bool {
	return len(s.Groups) == 1 && s.Groups[0].Key == ""
}

// Group is a set of findings shown under one heading: a project, a kind of
// Docker object or a scanner.
type Group struct {
	// Key identifies the group: a project root, a Docker kind or a scanner
	// name. Loose project findings have the key "(none)".
	Key string
	// Title is the heading.
	Title string
	// Project is the project root for project groups, used to show paths
	// relative to it.
	Project string
	// Findings are in report order, largest first.
	Findings []finding.Finding
	// Size is the total size of the findings. For an attention group it is
	// not reclaimable and is not counted in any total.
	Size int64
	// Attention marks a group of findings listed for attention only.
	Attention bool
}

// looseKey is the key of the group of project findings outside any project.
const looseKey = "(none)"

// Sections groups findings by category, in report order, and then within
// each category: project findings by project, Docker findings by kind, app
// cache and Downloads findings by scanner. short shortens paths for titles.
func Sections(fs []finding.Finding, short func(string) string) []Section {
	var cats []finding.Category
	byCat := map[finding.Category][]finding.Finding{}
	for _, f := range fs {
		if _, ok := byCat[f.Category]; !ok {
			cats = append(cats, f.Category)
		}
		byCat[f.Category] = append(byCat[f.Category], f)
	}
	out := make([]Section, 0, len(cats))
	for _, c := range cats {
		s := Section{Category: c, Groups: groupCategory(c, byCat[c], short)}
		for _, f := range byCat[c] {
			if f.Actionable() {
				s.Size += f.Size
				s.Count++
			}
		}
		out = append(out, s)
	}
	return out
}

func groupCategory(c finding.Category, fs []finding.Finding, short func(string) string) []Group {
	switch c {
	case finding.CategoryProject:
		return groupBy(fs, func(f *finding.Finding) string {
			if f.Project == "" {
				return looseKey
			}
			return f.Project
		}, func(key string) Group {
			if key == looseKey {
				return Group{Key: key, Title: "(not in a project)"}
			}
			return Group{Key: key, Title: short(key), Project: key}
		}, bySizeLooseLast)
	case finding.CategoryDocker:
		return groupBy(fs, func(f *finding.Finding) string { return f.DockerKind() },
			func(key string) Group { return Group{Key: key, Title: capitalize(key)} },
			func(a, b *Group) int {
				return cmp.Compare(slices.Index(finding.DockerKinds(), a.Key), slices.Index(finding.DockerKinds(), b.Key))
			})
	case finding.CategoryAppCache, finding.CategoryDownloads:
		return groupBy(fs, func(f *finding.Finding) string { return f.Scanner },
			func(key string) Group { return Group{Key: key, Title: scannerTitle(key)} },
			bySizeAttentionLast)
	default:
		g := Group{Findings: fs}
		for i := range fs {
			if fs[i].Actionable() {
				g.Size += fs[i].Size
			}
		}
		return []Group{g}
	}
}

func groupBy(fs []finding.Finding, key func(*finding.Finding) string, mk func(string) Group, order func(a, b *Group) int) []Group {
	idx := map[string]int{}
	var groups []Group
	for i := range fs {
		k := key(&fs[i])
		j, ok := idx[k]
		if !ok {
			j = len(groups)
			idx[k] = j
			groups = append(groups, mk(k))
		}
		g := &groups[j]
		g.Findings = append(g.Findings, fs[i])
		g.Size += fs[i].Size
	}
	for i := range groups {
		g := &groups[i]
		g.Attention = !slices.ContainsFunc(g.Findings, func(f finding.Finding) bool { return f.Actionable() })
		if !g.Attention {
			g.Size = finding.ReclaimableSize(g.Findings)
		}
	}
	slices.SortStableFunc(groups, func(a, b Group) int { return order(&a, &b) })
	return groups
}

func bySizeLooseLast(a, b *Group) int {
	if d := cmp.Compare(boolInt(a.Key == looseKey), boolInt(b.Key == looseKey)); d != 0 {
		return d
	}
	if a.Size != b.Size {
		return cmp.Compare(b.Size, a.Size)
	}
	return strings.Compare(a.Key, b.Key)
}

func bySizeAttentionLast(a, b *Group) int {
	if d := cmp.Compare(boolInt(a.Attention), boolInt(b.Attention)); d != 0 {
		return d
	}
	if a.Size != b.Size {
		return cmp.Compare(b.Size, a.Size)
	}
	return strings.Compare(a.Key, b.Key)
}

// scannerTitle is the group heading for a scanner's findings.
func scannerTitle(name string) string {
	switch name {
	case "installers":
		return "Installers of installed apps"
	case "extracted-archives":
		return "Archives with an extracted copy"
	case "old-downloads":
		return "Attention, never removed"
	default:
		return name
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Ordered returns the findings in the order Sections shows them.
func Ordered(sections []Section) []finding.Finding {
	var out []finding.Finding
	for _, s := range sections {
		for _, g := range s.Groups {
			out = append(out, g.Findings...)
		}
	}
	return out
}
