package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
)

// hereVersion is the schema version of "here --json" output.
const hereVersion = 1

// hereNode is one entry of the folder breakdown in "here --json".
type hereNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Dir      bool        `json:"dir"`
	Size     int64       `json:"size"`
	Children []*hereNode `json:"children,omitempty"`
}

// hereOutput is the "here --json" document.
type hereOutput struct {
	Version  int               `json:"version"`
	Path     string            `json:"path"`
	Size     int64             `json:"size"`
	Children []*hereNode       `json:"children"`
	Findings []finding.Finding `json:"findings"`
	Warnings []finding.Warning `json:"warnings,omitempty"`
	Notes    []string          `json:"notes,omitempty"`
}

func toHereNodes(nodes []*fsx.Node) []*hereNode {
	out := make([]*hereNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, &hereNode{Name: n.Name, Path: n.Path, Dir: n.IsDir, Size: n.Size, Children: toHereNodes(n.Children)})
	}
	return out
}

type hereFlags struct {
	depth int
	json  bool
	out   string
}

func newHereCmd(a *app) *cobra.Command {
	var f hereFlags
	cmd := &cobra.Command{
		Use:   "here [path]",
		Short: "Show what takes space in a folder and which of it can go",
		Long: "here lists the entries of a folder sorted by size, like du with one level of depth, and marks\n" +
			"every entry a scanner recognizes as reclaimable. Reclaimable entries at any depth are listed\n" +
			"below with their full paths. --out saves them as a report for select.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			return a.hereCommand(cmd, path, f)
		},
	}
	cmd.Flags().IntVar(&f.depth, "depth", 1, "how many levels of the breakdown to show")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the breakdown and findings as JSON")
	cmd.Flags().StringVar(&f.out, "out", "", "also save the findings as a JSON report for select")
	return cmd
}

func (a *app) hereCommand(cmd *cobra.Command, path string, f hereFlags) error {
	if f.depth < 1 {
		return errors.New("--depth must be at least 1")
	}
	root, err := resolveDir(path)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	reg := scanners.New()
	walker := fsx.NewWalker(0)

	// One walk measures every directory; the scanners then look up the sizes
	// of the artifact folders they find instead of walking them again.
	tree, treeRes, err := walker.Tree(ctx, root, fsx.TreeOptions{Depth: f.depth, DirSizes: true})
	if err != nil {
		return err
	}
	report := a.runScan(ctx, reg, scanRequest{
		scanners: scan.OnlyCategory(reg.All(), finding.CategoryProject),
		roots:    []string{root},
		walker:   walker,
		sizes:    treeRes.DirSizes,
	})
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("here interrupted: %w", err)
	}
	for i, u := range treeRes.Unreadable {
		if i == 20 {
			report.Warnings = append(report.Warnings, finding.Warning{Message: fmt.Sprintf("%d more unreadable entries not counted", len(treeRes.Unreadable)-20)})
			break
		}
		report.Warnings = append(report.Warnings, finding.Warning{Path: u.Path, Message: "not counted: " + u.Err.Error()})
	}

	if f.out != "" {
		if err := saveReport(f.out, report); err != nil {
			return err
		}
	}
	a.saveLastReport(report)
	if f.json {
		return writeJSON(a.stdout, hereOutput{
			Version:  hereVersion,
			Path:     root,
			Size:     tree.Size,
			Children: toHereNodes(tree.Children),
			Findings: report.Findings,
			Warnings: report.Warnings,
			Notes:    report.Notes,
		})
	}
	a.printer().Here(tree, report.Findings, report.Warnings, report.Notes)
	if f.out != "" {
		a.sayf("\nReport saved to %s. Next: reclaim select --report %s\n", f.out, f.out)
	}
	return nil
}
