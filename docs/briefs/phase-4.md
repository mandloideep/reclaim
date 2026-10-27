# Phase 4 brief

This is the brief handed to the implementation worker for phase 4.
It follows the shape of the phase 1, 2 and 3 briefs.
Phases 1 to 3 are merged in pull requests #1, #2 and #3, and `v0.1.0` is released from the commit after #3.

## Setup

Read `docs/DESIGN.md` and `CLAUDE.md` in full before writing code.
Follow them exactly.
If you must deviate, update DESIGN.md in the same change and explain why in the pull request body.
Read the bodies of pull requests #1, #2 and #3 as well; they record the judgment calls earlier phases made.
The sections of DESIGN.md that this phase implements are "worktrees", "done" and "skill" under the command surface, "Worktree scanners", "Agent session leftovers", "Removing a worktree" under the plan file, the two new safety rules, `[worktrees] github` under configuration, "Installing" under releasing, and the worktree items under testing.

## Why this phase exists

Coding agents leave git worktrees behind, and nobody can tell from a folder listing whether the work in one was committed, pushed and merged.
This phase makes reclaim answer that per worktree, offers the safe ones, and refuses, in code, to remove a worktree whose work exists nowhere else, even when an agent asks through `reclaim done --remove`.
The safety proof is the heart of the phase.
Build it first, test it hardest, and make every refusal explain itself.

## Deliverables

### Planned for phase 4 in DESIGN.md

1. `internal/worktree`: discovery and classification.
`List(ctx, env, repo)` returns the linked worktrees of a repository from `git worktree list --porcelain`, with locks, reasons and prunable registrations.
`Classify(ctx, env, path)` returns the `WorktreeState` DESIGN.md describes: repository, tool, branch, head, upstream, exists, lock state with the live check against the process table and the recorded start time, modified, untracked, ignored outside artifact folders with a sample, loose, unpushed, ahead, merged with its method, default branch, fetched time, and pull request.
The three merge checks run in the order DESIGN.md gives, with the synthetic squash commit from `git commit-tree`.
The tool is inferred as DESIGN.md says.
Every git and gh call goes through `execx.Runner` with the context; nothing runs `git fetch`, `git push` or anything that writes a ref or a file in a worktree.
2. The GitHub lookup: `gh pr list --head <branch> --state all --json number,state,url` through the runner, once per branch, bounded concurrency, a short timeout, on by default, off with `[worktrees] github = false`, one warning on failure.
It decorates and never changes a tier.
3. `internal/scanners/worktrees`, category `worktree`: scanners `git-worktrees` for every repository the project walk finds and every `.git` file it meets, `codex-worktrees` moved here from `appcache` and reworked onto the shared classification, and `worktree-registrations` for stale entries with action `GitWorktreePrune`.
Tiers, attention listings, warnings, retrieval hints and the copy and paste `--force` command exactly as DESIGN.md lists them.
Findings carry the state as `worktree` in JSON.
Grouping by repository, then tool, with the repository row DESIGN.md describes, in the table, the Markdown output, the numbered list and the checklist.
The duplicate clone listing.
`here` marks linked worktrees in its breakdown.
4. `finding.ActionGitWorktreeRemove` and `finding.ActionGitWorktreePrune`, the plan fields `repo`, `head` and `worktree`, plan validation that ties them to the action's target, and the apply executor that performs the six checks of "Removing a worktree" immediately before acting, logs each check, runs `git worktree remove` without `--force` and then `git worktree prune`, and refuses with the failed check, the retrieval hints and the `--force` command when work would be lost.
The commands join the fixed list in `internal/plan/commands.go` with the same discipline the existing entries have.
5. `reclaim worktrees [path...]` with `--json`, `--md` and `--out`, saving `last-report.json`, printing every linked worktree including the ones never offered, with the columns DESIGN.md lists.
6. `reclaim done [path]` with `--json`, `--remove` and `--allow-unmerged`, the four exit codes, the plain words output, the refusal to remove a worktree that contains its own working directory, and removal only through a plan and apply with a log in the reclaim cache folder.
7. `reclaim skill` printing `skills/reclaim/SKILL.md` embedded with `go:embed`, the skill file itself, and `docs/AGENTS.md` with the same instructions as a snippet.
Write the skill for an agent that has just finished a task: run `reclaim done --json` from the main checkout, read the exit code, on 2 push or commit first as the hints say and never force anything, on 1 open the pull request or wait for review, on 0 run `reclaim done --remove`, and never run `git worktree remove --force` itself.
8. Agent session leftovers in `internal/scanners/appcache`: Claude Code session folders whose `cwd` no longer exists, read from the session files, tier B after 30 days, and Codex plugin backups, tier A.
9. `scripts/install.sh` as the "Installing" section describes, with `--version`, `--bin-dir` and `--base-url`, and a CI job that builds a snapshot with GoReleaser, serves `dist/` from a local folder and installs from it on Linux and macOS.
10. README: a status line saying phase 4 is complete, `worktrees`, `done` and `skill` under Commands, the agent skill under a short "For agents" section, the install script under Install after Homebrew, the `[worktrees]` key under Config file, and a `worktrees` screenshot rendered from the fixture like the others, with the golden test extended.
11. DESIGN.md: move the phase 4 line from "delivers" to "delivered" with what was actually built, and record any deviation.

### Gaps and debt to fix on the way

12. The `codex-worktrees` scanner today reports a whole task folder as one finding; after the move it reports one finding per repository inside plus the empty task folder, as DESIGN.md says, and its existing tests move with it.

## Hard rules

- Never run `reclaim apply`, `reclaim done --remove`, `git worktree remove`, `git worktree prune`, `git branch -d`, or any deletion, prune, cleanup or install command against the machine you are on.
Validation on the reference machine is read only: `reclaim worktrees`, `reclaim done` without `--remove`, `reclaim scan`.
- The repository you are working in has three locked Claude Code worktrees of its own under `.claude/worktrees` of the clone at `~/Code/personal/reclaim`, and the clone at `~/Code/reclaim` is the owner's.
Touch neither; your own worktree is the one you were started in.
- Never tag, push a tag or create a GitHub release.
- No em dash characters anywhere.
- Markdown: one sentence per line.
- Do not add co-author trailers, `Claude-Session:` lines, session URLs or any other attribution to commits or pull request bodies.
- Only the dependencies allowed in CLAUDE.md.
Git and gh are external tools run through `execx.Runner`, never libraries.
- Every code path that removes anything has a test proving it removes only the target, and the worktree tests DESIGN.md lists are all present.
- `gofmt`, `go vet`, `go test -race ./...` and `golangci-lint run` must all pass before the pull request is opened.

## Workflow

- Branch `phase-4/worktrees`, coherent commits with imperative subjects under 72 characters, each body saying why.
- One pull request against `main` titled "Phase 4: worktree scanners, done, the agent skill and the install script".
- The pull request body lists what was built, what was tested and how, anything deferred, and any DESIGN.md deviations, in the shape of the earlier pull request bodies.
- After the first push, have an independent review of the diff as earlier phases did, fix what it finds, and summarize it in the pull request body.
- If CI fails after the pull request opens, fix it and push until it passes.
- Ask the owner through your session when something in DESIGN.md cannot be built as written; do not guess on anything that touches the safety proof.

## Validation on the reference machine

Run these read only and report the results in the pull request body:

- `reclaim worktrees` over `~/Code` and `~/.codex/worktrees` finishes in under five seconds with `[worktrees] github = false` and under fifteen with the lookup on.
- The three Claude Code worktrees under `~/Code/personal/reclaim/.claude/worktrees` are tier A with `merged = squash` and their pull request numbers 1, 2 and 3 shown as merged; their locks name a process that may or may not be running, and the output says which.
- The Codex `inboxgate` worktree under `~/.codex/worktrees` is tier A with `merged = ancestor`.
- The Codex `ai-teach` worktree is tier B with its open pull request shown.
- The Codex `webtools` worktree is listed for attention with its modified file count, its retrieval hints and the `--force` command, and `reclaim done` on it exits 2.
- `~/Code/personal/webtools` shows its stale registrations under `/private/tmp` as one `GitWorktreePrune` finding.
- `reclaim done ~/Code/personal/reclaim/.claude/worktrees/<any>` exits 0 and prints "committed, pushed, merged in PR #N".
- `reclaim done` on a main checkout exits 3 and says it is not a linked worktree.
- A full `reclaim scan` still finishes in under two minutes with totals within 5 percent of phase 3, plus the new worktree category.
- `scripts/install.sh --base-url file://<dist>` installs the snapshot binary into a temp folder and `reclaim version` from it prints the snapshot version.

## Final report

Report the pull request URL, CI status, a short list of what was built, test counts, the validation results above, anything deferred or deviated on, and any judgment calls a reviewer should look at, above all in the safety proof.
Do not include file dumps.
