# reclaim for coding agents

Paste this section into the `AGENTS.md` or `CLAUDE.md` of a project whose agents work in git worktrees, or into Codex's instructions.
It says the same as the skill that `reclaim skill` prints.

## Finishing work in a git worktree

When your task in a linked git worktree is done, make sure its work exists somewhere else before the worktree goes.

1. Change to the main checkout of the repository, the folder that holds the `.git` directory.
`reclaim done` never removes the folder it runs in.
2. Run `reclaim done --json <worktree path>` and read the exit code.
3. On `0`, the work is committed, pushed and merged: run `reclaim done --remove <worktree path>`.
4. On `1`, the work is pushed but not merged: open the pull request or say it waits for review, and leave the worktree.
Only when the user asks, run `reclaim done --remove --allow-unmerged <worktree path>`.
5. On `2`, the worktree holds work that exists nowhere else, such as uncommitted changes, unpushed commits, an ignored `.env`, a nested repository or a submodule: commit and push as the `hints` say, never forcing anything, and run `reclaim done --json` again.
If you cannot, stop and tell the user the `reasons`.
6. On `3`, the path is not a linked worktree, or reclaim could not tell whether its work exists elsewhere because a git command failed: stop and tell the user.

Never run `git worktree remove --force`, `git worktree remove` or `rm` on a worktree yourself, never force push or delete branches to make a worktree pass, and never run `reclaim done --remove` from inside the worktree.
`reclaim done` prints the `--force` command for the user, who decides.
