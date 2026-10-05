package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/ui"
	"github.com/mandloideep/reclaim/internal/units"
)

type selectFlags struct {
	preset string
	report string
	out    string
}

func newSelectCmd(a *app) *cobra.Command {
	var f selectFlags
	cmd := &cobra.Command{
		Use:   "select",
		Short: "Choose findings from a report and write a plan",
		Long: "select reads the last report, or the one given with --report, and writes a plan file for apply.\n" +
			"In a terminal it opens a checklist grouped by category and project, with tier A preselected.\n" +
			"--preset safe selects every tier A finding, --preset aggressive adds tier B. Tier C is never\n" +
			"selected by a preset or a group. When standard input or output is not a terminal, select prints\n" +
			"a numbered list and reads the numbers to select from standard input, such as 1,4-9,12; tier C\n" +
			"items must be listed one by one.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.selectCommand(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.preset, "preset", "", "select without prompting: safe (tier A) or aggressive (tiers A and B)")
	cmd.Flags().StringVar(&f.report, "report", "", "report to read, default the last scan or here report")
	cmd.Flags().StringVar(&f.out, "out", "plan.json", "plan file to write")
	return cmd
}

func (a *app) selectCommand(cmd *cobra.Command, f selectFlags) error {
	path := f.report
	if path == "" {
		path = a.lastReportPath()
	}
	report, err := loadReport(path)
	if err != nil {
		if f.report == "" {
			return fmt.Errorf("%w; run reclaim scan first or pass --report", err)
		}
		return err
	}
	p := a.printer()
	provenance := p.Provenance(report)
	a.sayf("%s\n", provenance)

	var selected []finding.Finding
	switch {
	case f.preset != "":
		tiers, err := plan.Preset(f.preset)
		if err != nil {
			return fmt.Errorf("--preset: %w", err)
		}
		selected = plan.SelectTiers(report.Findings, tiers)
	case !slices.ContainsFunc(report.Findings, func(f finding.Finding) bool { return f.Actionable() }):
		a.sayf("%s\n", "The report has nothing that can be selected, so no plan was written.")
		return nil
	case a.interactive != nil && a.interactive():
		m := ui.NewChecklist(report, ui.ChecklistOptions{
			Home:       a.home,
			PlanPath:   f.out,
			Provenance: provenance,
			Renderer:   lipgloss.NewRenderer(a.stdout),
			Now:        a.now(),
		})
		prog := tea.NewProgram(m, tea.WithContext(cmd.Context()), tea.WithInput(a.stdin), tea.WithOutput(a.stdout), tea.WithAltScreen())
		if _, err := prog.Run(); err != nil {
			return fmt.Errorf("checklist: %w", err)
		}
		if m.Outcome() != ui.ChecklistWrite {
			a.sayf("%s\n", "Quit without writing a plan.")
			return nil
		}
		selected = m.Selected()
	default:
		numbered := p.Numbered(report.Findings)
		a.sayf("\nSelect items by number, such as 1,4-9,12 (empty to cancel): ")
		line, err := a.readLine()
		if err != nil {
			return err
		}
		idx, skipped, err := parseSelection(line, numbered)
		if err != nil {
			return err
		}
		if len(skipped) > 0 {
			a.sayf("Skipped tier C items %s because they were part of a range; list them one by one to select them.\n", joinInts(skipped))
		}
		for _, i := range idx {
			selected = append(selected, numbered[i])
		}
	}
	if len(selected) == 0 {
		a.sayf("%s\n", "Nothing selected, no plan written.")
		return nil
	}
	pl, err := plan.New(report, selected, a.host, a.now())
	if err != nil {
		return err
	}
	if err := plan.Save(f.out, pl); err != nil {
		return err
	}
	a.sayf("Wrote %s: %d actions, %s. Review it, then run: reclaim apply %s\n",
		f.out, len(pl.Actions), units.FormatSize(pl.TotalSize()), f.out)
	return nil
}

// parseSelection parses 1 based indices and ranges such as "1,4-9 12" into 0
// based indices in ascending order. Tier C findings are only selected when
// their number is given on its own; those reached through a range are
// returned in skipped, 1 based.
func parseSelection(input string, fs []finding.Finding) (idx, skipped []int, err error) {
	fields := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(fields) == 0 {
		return nil, nil, nil
	}
	n := len(fs)
	chosen := map[int]bool{}
	parse := func(s string) (int, error) {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		if v < 1 || v > n {
			return 0, fmt.Errorf("%d is out of range, pick between 1 and %d", v, n)
		}
		return v, nil
	}
	for _, field := range fields {
		lo, hi, isRange := strings.Cut(field, "-")
		if !isRange {
			v, err := parse(lo)
			if err != nil {
				return nil, nil, err
			}
			chosen[v-1] = true
			continue
		}
		from, err := parse(lo)
		if err != nil {
			return nil, nil, err
		}
		to, err := parse(hi)
		if err != nil {
			return nil, nil, err
		}
		if from > to {
			return nil, nil, fmt.Errorf("range %s is backwards", field)
		}
		for v := from; v <= to; v++ {
			if fs[v-1].Tier == finding.TierC {
				skipped = append(skipped, v)
				continue
			}
			chosen[v-1] = true
		}
	}
	for i := range chosen {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	skipped = slices.DeleteFunc(slices.Compact(sortedCopy(skipped)), func(v int) bool { return chosen[v-1] })
	return idx, skipped, nil
}

func sortedCopy(xs []int) []int {
	out := slices.Clone(xs)
	slices.Sort(out)
	return out
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ", ")
}
