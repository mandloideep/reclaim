# Phase 5 brief

This is the brief handed to the implementation worker for phase 5.
It follows the shape of the phase 1 to 4 briefs.
Phases 1 to 4 are merged in pull requests #1 to #4, and `v0.2.0` is released from the commit after #4.

## Setup

Read `docs/DESIGN.md` and `CLAUDE.md` in full before writing code.
Follow them exactly.
If you must deviate, update DESIGN.md in the same change and explain why in the pull request body.
Read the bodies of pull requests #1 to #4 as well; they record the judgment calls earlier phases made.
The sections of DESIGN.md that this phase implements are `ui` under the command surface, the `--dry-run`, checks, refusals, protected list and progress paragraphs under `apply`, `internal/tui` and the `Backend` interface under "Architecture", the `Progress` field of `Env` under "Scanner interface", the tree paragraph at the end of "Handling long lists", the whole "Interactive mode" section, the three new safety rules, the ignore file and target entries under "Configuration", the interactive items under "Testing", the phase 5 line under "Phases" and the last line of "Acceptance".

## Why this phase exists

Today reclaim is four commands run in order, and a new user has to know the order; nothing shows progress while a scan runs or while a plan is applied, and the folders reclaim will never touch are visible only in the code.
This phase makes bare `reclaim` one screen that walks a person through scan, review, confirm, remove and done, with live progress, worktrees and their proof readable in place, and the safety rules stated on the screen with the result of every check.
The screen is a front end over `plan.New`, `plan.Save` and `apply.Run`.
It must gain no way to remove anything that `reclaim apply plan.json` does not already have, and a reviewer must be able to see that from the imports of `internal/tui` and the shape of the `Backend` interface.

## Deliverables

Three pull requests, in this order, each passing CI and each useful on its own.

### 1. `phase-5/tui-scan-and-review`

1. Scan progress: `scan.Env.Progress` with the `ScannerStarted`, `ScannerFinished` and `Step` events, emitted by the runner and by the scanners with long phases, the GitHub lookup and the Docker listing included, and the throttled walk progress of `project.Options` forwarded as `Step` events.
Events reach the bubbletea program through `Program.Send` from the scan goroutine.
2. `ui.Tree` extracted from `ui.Checklist`, which becomes a thin wrapper that keeps its keys, outcome and footer for `select`, with the checklist tests moved to `tree_test.go` and still green.
3. `internal/tui` with the root model and the Start, Scanning and Review screens, the details pane with everything DESIGN.md lists for findings, worktrees, Docker objects, repository rows and Claude session rows, the `n`, `i`, `w` and `c` keys, the warnings node, the help overlay with its closing sentence, and `P` showing the protected list that `apply.ProtectedList` renders.
`c` ends this pull request's flow by writing the plan under the reclaim folder, printing its path and how to apply it, and quitting; the Confirm screen comes next.
4. `cmd/reclaim/ui.go`: the `ui` command with the scan flags and `--out`, the backend over the existing scan code, bare `reclaim` opening the screen only when standard input and standard output are terminals, the refusal with the commands to use otherwise, and `ui` listed first in the help.
5. `apply.ProtectedList` and `apply.ProtectedEntry`, rendered from the same tables `safety.go` uses, with the test that every path of the two denylists appears in the output.
6. `TestScreenshots` rendering the Start, Scanning and Review screens from the fixture machine, `scripts/screenshots.sh` rendering `docs/screenshots/ui-*.svg`, and the README leading with the review screen and describing `reclaim` with no arguments.

### 2. `phase-5/tui-apply`

7. `apply.Options.Progress` with `Starting` and `Finished`, `apply.Options.DryRun`, `apply --dry-run` with its log marked as a dry run and its statuses, `Checks` on every outcome of every action kind with every check logged, and `apply.Options.Protected` carrying the `exclude` entries that `apply` refuses.
8. `fsx.FreeSpace` on darwin and linux, not supported elsewhere.
9. The Confirm, Removing and Done screens exactly as DESIGN.md draws them: the plan saved as `plans/plan-<timestamp>.json` before confirmation, the plan printer's lines, the left out worktrees named, the typed `yes`, `d` for the dry run with the per row results and the worktree checks by name, keep going on, the spinner row, the checks per outcome, the disk line, `ctrl+c` stopping after the current action, the `sudo` commands in full, `r` to scan again and the plain summary printed on exit.
10. The proof test that the review, confirm and removing screens call exactly `SavePlan` then `Apply` with the returned path, the end to end test through the screens over the fixture machine, the dry run test, and the remaining three screenshots.

### 3. `phase-5/tui-ignore-and-folder`

11. The ignore file in `internal/config`, read like `exclude`, written atomically, with target entries `docker:<id>` and `ollama:<model>` in both the ignore file and `exclude`, the `Ignore` backend method, `x` on the Review screen with its confirmation, the Start screen's ignore list with `d` to remove an entry, the count on the Start screen, and `reclaim scanners` reporting the count and the path.
12. `apply` refusing a target that is, lies under or contains an `exclude` or ignore entry, with the test that an ignored or excluded path is dropped by every scanner, never enters a plan, and is refused by apply when a hand edited plan names it.
13. The folder breakdown tab of "This folder" mode with the worktree state words and the shared selection, and `--report` on `ui`.
14. DESIGN.md: move the phase 5 line from "delivers" to "delivered" with what was actually built, and record any deviation.

## Hard rules

- Never run `reclaim apply`, `reclaim done --remove`, `git worktree remove`, `git worktree prune`, `git branch -d`, or any deletion, prune, cleanup or install command against the machine you are on, and never type `yes` on the Confirm screen outside a test fixture.
Validation on the reference machine is read only: the screen up to Confirm and its `d` dry run, then `esc` and `q`, and `reclaim scan`, `reclaim worktrees` and `reclaim apply --dry-run` on a plan written from the reference machine.
Before the first dry run on the reference machine, the dry run test must prove that `--dry-run` removes nothing.
- The repository you are working in has three locked Claude Code worktrees of its own under `.claude/worktrees` of the clone at `~/Code/personal/reclaim`, and the clone at `~/Code/reclaim` is the owner's.
Touch neither; your own worktree is the one you were started in.
- Never tag, push a tag or create a GitHub release.
- No em dash characters anywhere.
- Markdown: one sentence per line.
- Do not add co-author trailers, `Claude-Session:` lines, session URLs or any other attribution to commits or pull request bodies.
- Only the dependencies allowed in CLAUDE.md.
The `progress`, `spinner`, `textinput`, `viewport`, `help` and `key` components of bubbles cover every widget; add nothing else.
- `internal/tui` imports neither `os` nor `os/exec`, and the only backend method that removes anything takes a plan path.
- Every code path that removes anything has a test proving it removes only the target, and the screens have the proof test, the end to end test and the dry run test DESIGN.md lists before the second pull request opens.
- `gofmt`, `go vet`, `go test -race ./...` and `golangci-lint run` must all pass before each pull request is opened.

## Workflow

- One branch per pull request, named as above, each cut from `main` after the previous pull request has merged, with coherent commits with imperative subjects under 72 characters and bodies saying why.
- Pull request titles: "Phase 5: the interactive screen, scan progress and the shared findings tree", "Phase 5: confirm, removing and done, apply dry run and checks on every outcome", "Phase 5: the ignore list, apply refusing excluded paths and the folder tab".
- The pull request body lists what was built, what was tested and how, anything deferred, and any DESIGN.md deviations, in the shape of the earlier pull request bodies.
- After the first push of each pull request, have an independent review of the diff as earlier phases did, fix what it finds, and summarize it in the pull request body.
- If CI fails after a pull request opens, fix it and push until it passes.
- When a pull request is open, CI is green and the review is addressed, end your turn and report; the owner's orchestrating session reviews and merges it and tells you to continue.
While waiting you may start the next branch on top of the open one and rebase it onto `main` once the merge lands.
- Ask the owner through your session when something in DESIGN.md cannot be built as written; do not guess on anything that touches the removal path, the proof or the protected list.

## Validation on the reference machine

Run these read only and report the results in the pull request bodies:

- `reclaim` in a terminal opens the Start screen; `reclaim | cat` prints the help.
- A whole machine scan from the screen finishes in under two minutes, every category row updates while it runs, and the totals on the Review screen are within 5 percent of `reclaim scan`.
- The Review screen shows the worktrees of `~/Code/personal/reclaim` grouped under the repository with their verdict words and pull request numbers, and the Codex `webtools` worktree as a listed only row with its hints in the details pane.
- `P` lists the system paths, `~/Documents`, `~/Desktop`, `~/Library`, the roots of the scan and the `exclude` entries of the configuration, if any.
- `esc` during a scan opens the Review screen on the partial findings with the interruption warning.
- On the Confirm screen with the preselected tier A findings, `d` reports a result for every action and removes nothing: `reclaim scan` before and after reports the same totals.
- `reclaim select` over the `last-report.json` the screen saved still works unchanged.
- The screen renders at 80 columns with the details pane below the list and at 140 columns with it on the right.

## Final report

Report for each pull request its URL, CI status, a short list of what was built, test counts, the validation results above, anything deferred or deviated on, and any judgment calls a reviewer should look at, above all in the removal path and the dry run.
Do not include file dumps.
