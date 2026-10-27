package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/scanners"
	"github.com/mandloideep/reclaim/internal/worktree"
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
	// Worktree is the state in words of a linked worktree.
	Worktree string `json:"worktree,omitempty"`
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

func toHereNodes(nodes []*fsx.Node, worktrees map[string]string) []*hereNode {
	out := make([]*hereNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, &hereNode{Name: n.Name, Path: n.Path, Dir: n.IsDir, Size: n.Size,
			Children: toHereNodes(n.Children, worktrees), Worktree: worktrees[n.Path]})
	}
	return out
}

// worktreeMarks classifies the linked worktrees among the folders of the
// breakdown, without asking GitHub, and returns their state in words.
func (a *app) worktreeMarks(ctx context.Context, root *fsx.Node) map[string]string {
	senv := scan.Env{Home: a.home, Getenv: a.getenv}
	env := worktree.Env{Exec: a.exec, CodexHome: senv.CodexHome()}
	marks := map[string]string{}
	var visit func(n *fsx.Node)
	visit = func(n *fsx.Node) {
		for _, c := range n.Children {
			if ctx.Err() != nil || !c.IsDir {
				continue
			}
			if info, err := os.Lstat(filepath.Join(c.Path, ".git")); err == nil && info.Mode().IsRegular() {
				if s, err := worktree.Classify(ctx, env, c.Path); err == nil {
					marks[c.Path] = worktree.Words(s)
				}
				continue
			}
			visit(c)
		}
	}
	visit(root)
	return marks
}

type hereFlags struct {
	depth int
	json  bool
	md    bool
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
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: firstArg(folders),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			return a.hereCommand(cmd, args, path, f)
		},
	}
	cmd.Flags().IntVar(&f.depth, "depth", 1, "how many levels of the breakdown to show")
	cmd.Flags().BoolVar(&f.json, "json", false, "print the breakdown and findings as JSON")
	cmd.Flags().BoolVar(&f.md, "md", false, "print the breakdown and findings as Markdown")
	cmd.Flags().StringVar(&f.out, "out", "", "also save the findings as a JSON report for select")
	completeFlag(cmd, "depth", cobra.NoFileCompletions)
	completeFlag(cmd, "out", filesWithExt("json"))
	return cmd
}

func (a *app) hereCommand(cmd *cobra.Command, args []string, path string, f hereFlags) error {
	if f.depth < 1 {
		return errors.New("--depth must be at least 1")
	}
	if f.json && f.md {
		return errors.New("--json and --md cannot be used together")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
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
	selected, err := enabledScanners(reg.All(), nil, cfg.Disable)
	if err != nil {
		return err
	}
	report := a.runScan(ctx, reg, scanRequest{
		scanners: scan.OnlyCategory(selected, finding.CategoryProject),
		roots:    []string{root},
		exclude:  cfg.Exclude,
		walker:   walker,
		sizes:    treeRes.DirSizes,
		scope:    scopeOf(cmd, args, []string{root}, []string{root}, "depth", "json", "md", "out"),
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
	marks := a.worktreeMarks(ctx, tree)
	if f.json {
		return writeJSON(a.stdout, hereOutput{
			Version:  hereVersion,
			Path:     root,
			Size:     tree.Size,
			Children: toHereNodes(tree.Children, marks),
			Findings: report.Findings,
			Warnings: report.Warnings,
			Notes:    report.Notes,
		})
	}
	if f.md {
		a.markdownPrinter().HereMarkdown(tree, report.Findings, marks, report.Warnings, report.Notes)
		return nil
	}
	a.printer().Here(tree, report.Findings, marks, report.Warnings, report.Notes)
	if f.out != "" {
		a.sayf("\nReport saved to %s. Next: reclaim select --report %s\n", f.out, f.out)
	}
	return nil
}
