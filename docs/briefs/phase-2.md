# Phase 2 brief

This is the brief handed to the implementation worker for phase 2.
It follows the shape of `docs/briefs/phase-1.md`.
Phase 1 is merged in pull request #1.

## Setup

Read `docs/DESIGN.md` and `CLAUDE.md` in full before writing code.
Follow them exactly.
If you must deviate, update DESIGN.md in the same change and explain why in the pull request body.
Read the body of pull request #1 as well; it records the judgment calls phase 1 made.

## Deliverables

### Planned for phase 2 in DESIGN.md

1. Interactive checklist in `internal/ui`, using bubbletea and bubbles, exactly as "Handling long lists" describes.
It is a tree: categories, then projects or Docker object kinds, then findings.
Every node shows its aggregate size and count.
Toggling a group toggles its tier A and B children and never its tier C children.
Tier A findings are preselected.
Keys: space toggles, enter expands and collapses, `/` filters by text, `s` cycles sort, `t` filters by tier, `a` selects all tier A, `w` writes the plan and exits, `q` quits without writing.
The footer always shows the selected count and total size and the plan path that `w` will write.
Tier C rows show their warning.
The checklist must stay responsive with a few thousand findings, so the view renders only the visible window.
`select` opens the checklist when stdin and stdout are terminals and `--preset` is not given.
When either is not a terminal, `select` keeps the phase 1 numbered list, so scripts and tests keep working.
Test the model directly by feeding key messages to `Update` and asserting on the selection and on `View`; do not add a test dependency for this.
2. App cache scanners in `internal/scanners/appcache`, category `app-cache`, as the "App cache scanners" section lists them: `~/Library/Caches/*` entries above the size threshold with a denylist of apps known to keep data there, Electron updater leftovers, Playwright and Puppeteer browsers, Xcode `DerivedData`, simulator devices for unavailable runtimes, simulator runtimes with `NeedsSudo` and the `xcrun simctl runtime delete` command, Ollama models with `ollama rm`, and agent tool leftovers.
On Linux, the generic cache scanner covers `~/.cache/*` with the same denylist approach, and the macOS only scanners return nothing.
Every `~/Library/Caches` finding must be a direct child of that folder, never the folder itself, which apply already protects.
3. Downloads scanner in `internal/scanners/downloads`, category `downloads`, as the "Downloads scanner" section lists it: installer files whose application exists in `/Applications` (tier B), archives with an extracted sibling (tier C with a warning naming the sibling), and an "attention" list of large old files that carries no action.
Add an `Action` value for findings that are shown but never applied, make `select` refuse to put them in a plan, and make `apply` refuse them in a hand edited plan.
4. `--md` on `scan` and `here`: a Markdown summary with the same grouping as the table, meant for pasting into an issue.
5. Config file at `~/.config/reclaim/config.toml` through `internal/config` with BurntSushi toml, as the "Configuration" section shows: `roots`, `exclude`, `min_size`, `stale` and `[scanners] disable`.
Flags override the config file.
`exclude` prunes the project walk and drops findings under the excluded paths.
Unknown keys are an error that names the key.
`reclaim scanners` marks disabled scanners.

### Gaps and debt found while validating phase 1

These came from running the merged binary read only against the reference machine and from the phase 1 report.

6. Report provenance.
`here` and a scoped `scan path...` overwrite `last-report.json`, so running `here` to look at one folder and then `select` silently offers only that folder's findings.
Add a `scope` to the report recording the command, its arguments and the roots.
`select` prints one provenance line before the list or checklist: what produced the report, when, and how many findings it holds.
When the report came from `here` or a scoped scan, the line says so.
Keep saving the most recent report of any kind, because `here` followed by `select` is a supported flow.
7. Docker table grouping.
The scan table prints over two hundred flat Docker rows on the reference machine.
Group Docker findings in the table, the Markdown output and the numbered list by kind, in this order: build cache, stopped containers, dangling images, unused images, volumes.
Each group shows its subtotal and count, like project groups do.
8. Re-query cache locations at apply time.
Phase 1 deferred this.
For `RunCommand` actions that came from a tool's own config command, apply asks the tool again right before running and refuses when the answer differs from the path in the plan.
9. Add `/.claude/` to `.gitignore`.
The harness creates worktrees there, and their presence makes `go build` stamp the binary as dirty.
10. Run the Docker integration test once in CI.
Add a job on `ubuntu-latest`, where Docker is available on the runner, that runs the gated tests with `RECLAIM_DOCKER_TESTS=1`.
It must only touch objects labeled `reclaim.test=1`, which the tests already guarantee.
If the runner's Docker setup makes this impossible, say so in the pull request and leave the job out.

### Documentation

11. Update DESIGN.md: move the checklist, app cache, Downloads, Markdown and config items from the phases list into the body as delivered, record the provenance line and the Docker grouping, and describe the no-action finding kind.
12. Keep the README status line accurate and add a short "Config" section showing the file.

## Hard rules

- Never run any deletion, prune or cleanup command against the machine you are on.
Scanning and dry runs are fine.
- No em dash characters anywhere.
- Markdown: one sentence per line.
- No co-author trailers in commits.
- Only the dependencies allowed in CLAUDE.md, with any addition justified in the pull request body.
bubbletea, bubbles and BurntSushi toml are already approved.
- Every code path that removes anything has a test proving it removes only the target.
This includes the new app cache and Downloads findings, which must go through the same apply re-verification and must be covered by the end to end test.
- `gofmt`, `go vet`, `go test -race ./...` and `golangci-lint run` must all pass before the pull request is opened.

## Workflow

- Branch `phase-2/checklist-and-scanners`, coherent commits with imperative subjects under 72 characters.
- One pull request against `main` titled "Phase 2: interactive checklist, app cache and Downloads scanners, Markdown output and config".
- The pull request body lists what was built, what was tested and how, anything deferred, and any DESIGN.md deviations.
- After the first push, have an independent review of the diff as phase 1 did, fix what it finds, and summarize it in the pull request body.
- If CI fails after the pull request opens, fix it and push until it passes.

## Validation on the reference machine

Build the binary and run these read only, then report the results in the pull request body:

- `reclaim scan` must still finish in under two minutes and report project artifacts, package caches and Docker within 5 percent of the phase 1 figures.
- `reclaim scan` must now also list app cache and Downloads findings, with their totals.
- `reclaim select` must open the checklist in a terminal and show the provenance line.
Do not press `w` on a real report; quit with `q`.
- `reclaim scan --md` must render cleanly when pasted into a Markdown preview.
- With a config file that excludes one project root and disables one scanner, the scan must omit exactly those.

## Final report

Report the pull request URL, CI status, a short list of what was built, test counts, anything deferred or deviated on, and any judgment calls a reviewer should look at.
Do not include file dumps.
