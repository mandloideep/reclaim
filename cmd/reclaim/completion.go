package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/scanners"
)

// Shell completion. Cobra's completion command writes the scripts; the
// functions here give the scripts real values to offer: tiers, categories,
// presets and sizes for flags, JSON files for reports and plans, TOML files
// for --config and folders for scan and here.

// choice is one value a flag accepts, with a short description that shells
// such as zsh and fish show next to it.
type choice struct {
	value, help string
}

// candidate is the completion candidate for the choice, after prefix.
func (c choice) candidate(prefix string) string {
	if c.help == "" {
		return prefix + c.value
	}
	return prefix + c.value + "\t" + c.help
}

// completeFlag registers fn for the named flag of cmd. The flag must exist,
// so a typo fails every test that builds the command tree.
func completeFlag(cmd *cobra.Command, name string, fn cobra.CompletionFunc) {
	if err := cmd.RegisterFlagCompletionFunc(name, fn); err != nil {
		panic(fmt.Sprintf("register completion for --%s of %s: %v", name, cmd.Name(), err))
	}
}

// oneOf completes a flag that takes one of the choices.
func oneOf(choices func() []choice) cobra.CompletionFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		cs := choices()
		out := make([]string, 0, len(cs))
		for _, c := range cs {
			out = append(out, c.candidate(""))
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// listOf completes a flag that takes a comma separated list of choices, such
// as --tier A,B. It offers the choices not listed yet after the last comma,
// each prefixed with what was typed before it, since shells match candidates
// against the whole word.
func listOf(choices func() []choice) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix := ""
		if i := strings.LastIndexByte(toComplete, ','); i >= 0 {
			prefix = toComplete[:i+1]
		}
		listed := strings.Split(prefix, ",")
		var out []string
		for _, c := range choices() {
			if !slices.Contains(listed, c.value) {
				out = append(out, c.candidate(prefix))
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// filesWithExt completes file names with one of the extensions, and folders
// to reach them.
func filesWithExt(exts ...string) cobra.CompletionFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return exts, cobra.ShellCompDirectiveFilterFileExt
	}
}

// folders completes folder names.
func folders(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// firstArg applies fn to the first positional argument only.
func firstArg(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fn(cmd, args, toComplete)
	}
}

func tierChoices() []choice {
	return []choice{
		{string(finding.TierA), "rebuilds itself on the next build or install"},
		{string(finding.TierB), "costs a download or a rebuild"},
		{string(finding.TierC), "may be data"},
	}
}

// categoryChoices lists every value --category and [scanners] disable accept:
// the categories, then the ecosystems, then the scanner names, each once.
func categoryChoices() []choice {
	all := scanners.New().All()
	var out []choice
	add := func(value, help string) {
		if value != "" && !slices.ContainsFunc(out, func(c choice) bool { return c.value == value }) {
			out = append(out, choice{value, help})
		}
	}
	for _, c := range finding.Categories() {
		add(string(c), "category: "+c.Title())
	}
	for _, s := range all {
		add(s.Ecosystem(), "ecosystem")
	}
	for _, s := range all {
		add(s.Name(), "scanner: "+s.Description())
	}
	return out
}

func presetChoices() []choice {
	return []choice{
		{"safe", "every tier A finding"},
		{"aggressive", "every tier A and B finding"},
	}
}

func sizeChoices() []choice {
	return []choice{
		{"0", "show everything"},
		{"10MB", "the default"},
		{"50MB", ""},
		{"100MB", ""},
		{"1GB", ""},
	}
}

func staleChoices() []choice {
	return []choice{
		{"30d", "no activity for 30 days"},
		{"90d", "no activity for 90 days"},
		{"180d", "no activity for 180 days"},
		{"1y", "no activity for a year"},
	}
}
