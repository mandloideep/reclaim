# Phase 1 brief

This is the brief handed to the implementation worker for phase 1.
It is kept in the repository so the work is reproducible and so later phases can follow the same shape.

## Setup

Read `docs/DESIGN.md` and `CLAUDE.md` in full before writing code.
Follow them exactly.
If you must deviate, update DESIGN.md in the same change and explain why in the pull request body.

## Deliverables

1. `internal/finding`: Finding, Tier, Category, Action, Report types with stable JSON.
2. `internal/fsx`: concurrent directory size walker.
It counts apparent size, counts hard links once, never follows symlinks, never crosses filesystem boundaries, records unreadable paths as warnings and honors context cancellation.
3. `internal/project`: project detection by marker files, artifact folder pruning, and last activity as the newest of the last git commit date and the newest source mtime.
4. `internal/scan`: Scanner interface, Env, explicit registry in `internal/scanners/all.go`, bounded concurrent runner where one failing scanner yields a warning instead of a failure.
5. Project scanners: node_modules, Python venvs, Rust target, Go in-repo build caches (a `.cache/go-build` or any directory containing a go-build `CACHEDIR.TAG` or `trim.txt` marker inside a project), .next, .turbo, .nuxt, .svelte-kit, .parcel-cache, dist and build (tier A only when a build config is present, else tier C), __pycache__, .pytest_cache, .mypy_cache, .ruff_cache, .gradle, Pods, .dart_tool, .terraform.
6. Package cache scanners: npm `_cacache` and `_npx`, pnpm store and cache, bun, yarn, uv, pip, Go module cache and GOCACHE, cargo registry and git, Homebrew cache with `brew cleanup -s` as a RunCommand action, swiftpm, gradle, maven, CocoaPods, composer.
Locate each cache through the tool's own config command when installed and fall back to the default path.
7. Docker scanner using `github.com/docker/docker/client`, honoring DOCKER_HOST and the active docker context.
Findings: stopped or created containers (A), dangling images (A), unused images (B), unattached volumes (B), volumes attached only to stopped containers (C), build cache as one finding (A).
Never offer running containers or anything they use.
Support filtering by label through `--docker-label key=value`.
8. `internal/plan` and `internal/apply`: plan file v1, validation, 24 hour staleness check, re-verification before every removal, denylist of protected paths, executors for RemovePath, RunCommand, DockerRemoveContainer, DockerRemoveImage, DockerRemoveVolume and DockerPruneBuildCache, and a log file with timestamps and bytes freed.
9. Cobra commands: `scan [path...]` with `--json --min-size --tier --category --stale --out --all --depth`, `here [path]` with `--depth --json --out`, `select` with `--preset safe|aggressive --report --out`, `apply plan.json` with `--yes --log --keep-going --stale-ok`, `scanners`, `version`.
The interactive checklist is phase 2.
In phase 1, `select` without `--preset` prints a numbered list and accepts indices and ranges from stdin such as `1,4-9,12`.
10. Terminal table output with human readable sizes using lipgloss, colors only when stdout is a TTY, and stable versioned `--json` output.
11. Tests as specified in DESIGN.md: unit tests for every scanner with temp directory fixtures, fsx tests for hard links, symlinks, unreadable paths and cancellation, an end to end test that scans a fixture tree, selects the safe preset, applies with `--yes` and asserts only the expected paths are gone, and Docker integration tests gated behind `RECLAIM_DOCKER_TESTS=1` that create busybox fixtures labeled `reclaim.test=1` and touch only those.
Docker is probably unavailable in the worker environment, so the gated tests must compile and skip cleanly, and the Docker scanner is unit tested against a fake client interface.

## Hard rules

- Never run any deletion, prune or cleanup command against the machine you are on.
- No em dash characters anywhere.
- Markdown: one sentence per line.
- No co-author trailers in commits.
- Only the dependencies allowed in CLAUDE.md, with any addition justified in the pull request body.
- Every code path that removes anything has a test proving it removes only the target.
- `gofmt`, `go vet`, `go test -race ./...` and `golangci-lint run` must all pass before the pull request is opened.

## Workflow

- Branch `phase-1/core`, coherent commits with imperative subjects under 72 characters.
- One pull request against `main` titled "Phase 1: scan, here, select, apply with project, cache and Docker scanners".
- The pull request body lists what was built, what was tested and how, anything deferred, and any DESIGN.md deviations.
- Keep the README status line accurate.
- If CI fails after the pull request opens, fix it and push until it passes.

## Final report

Report the pull request URL, CI status, a short list of what was built, test counts, anything deferred or deviated on, and any judgment calls a reviewer should look at.
Do not include file dumps.
