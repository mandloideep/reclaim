package main

import (
	"github.com/spf13/cobra"

	"github.com/mandloideep/reclaim/skills"
)

func newSkillCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the agent skill that tells a coding agent to run reclaim done",
		Long: "skill prints the agent skill that tells a coding agent, when its task in a git worktree is done,\n" +
			"to run reclaim done, push or open a pull request when the state says so, and remove the worktree\n" +
			"only once its work is merged. Install it for Claude Code with:\n\n" +
			"  mkdir -p ~/.claude/skills/reclaim && reclaim skill > ~/.claude/skills/reclaim/SKILL.md\n\n" +
			"docs/AGENTS.md in the reclaim repository holds the same instructions for AGENTS.md and CLAUDE.md files.",
		Args: cobra.NoArgs,
		// No arguments, so there is nothing to complete.
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			_, err := a.stdout.Write([]byte(skills.Reclaim))
			return err
		},
	}
}
