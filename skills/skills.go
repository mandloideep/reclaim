// Package skills holds the agent skills reclaim ships, embedded so the
// binary can print them.
package skills

import _ "embed"

// Reclaim is skills/reclaim/SKILL.md, the skill that tells a coding agent to
// run reclaim done when it finishes a task in a worktree.
//
//go:embed reclaim/SKILL.md
var Reclaim string
