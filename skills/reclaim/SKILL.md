---
name: reclaim
description: Use when you finish a task in a git worktree, or are asked to clean up worktrees. Runs reclaim done to check that the worktree's changes are committed, pushed and merged, and removes the worktree only when they are.
---

# Finishing work in a git worktree

You have just finished a task in a linked git worktree, such as one under `.claude/worktrees` or `~/.codex/worktrees`.
Before the worktree is removed, its work must exist somewhere else: committed, pushed, and merged.
`reclaim done` checks exactly that and removes the worktree only when it is safe.

## Steps

1. Change to the main checkout of the repository, not the worktree.
`reclaim done` never removes the folder it runs in.
The main checkout is the repository folder that holds the `.git` directory.

2. Run `reclaim done --json <worktree path>` and read the exit code.

3. Act on the exit code:

   - `0`: committed, pushed and merged.
   Run `reclaim done --remove <worktree path>`.
   It removes the worktree and keeps its branch.
   - `1`: pushed but not merged.
   Open the pull request if there is none, or tell the user it is waiting for review.
   Do not remove the worktree.
   Only if the user asks for it, run `reclaim done --remove --allow-unmerged <worktree path>`.
   - `2`: the worktree holds work that exists nowhere else.
   The `reasons` and `hints` in the JSON say what: uncommitted changes, untracked files, commits on no remote, commits on a detached HEAD, ignored files such as `.env`, a repository nested inside the worktree, or a submodule.
   Commit and push as the hints say, then run `reclaim done --json` again.
   If you cannot save the work, stop and tell the user, quoting the `reasons`.
   - `3`: the path is not a linked worktree, or reclaim could not tell whether its work exists elsewhere, because a git command failed.
   Stop and tell the user what `reclaim done` printed.

4. If `reclaim done --remove` refuses, read why and tell the user.
A worktree locked by a running session, for example your own, cannot be removed until that session ends.

## Never

- Never run `git worktree remove --force`, `git worktree remove` or `rm` on a worktree yourself.
`reclaim done` prints the `--force` command for the user; it is theirs to run.
- Never force push, rewrite history or delete branches to make a worktree pass.
- Never run `reclaim done --remove` from inside the worktree being removed.
