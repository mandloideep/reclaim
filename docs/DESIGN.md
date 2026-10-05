# reclaim design

`reclaim` is a command line tool that finds disk space you can get back and removes only what you explicitly select.
It is built for developer machines where the space goes to build artifacts, dependency folders, package manager caches, Docker leftovers and application caches.
macOS is the first target.
Linux is a supported target.
Windows is out of scope.

## Goals

- Show, with sizes, everything on the machine that can be reclaimed, grouped by category and by project.
- Never delete anything unless the user selected it and confirmed.
- Work at two zoom levels: the whole machine, and a single folder the user is standing in.
- Make it cheap to add a new scanner, so other people can extend it for their tools.
- Produce machine readable output so it can run in scripts and cron.

## Non-goals

- It is not a general disk usage visualizer like ncdu or GrandPerspective, although the folder mode borrows from them.
- It never runs with elevated privileges.
Items that need `sudo` are reported with the exact command for the user to run.
- It does not uninstall applications.
- It does not touch user data such as documents, photos, databases or git working trees.

## Command surface

```
reclaim scan [path...]   [--json] [--md] [--min-size 50MB] [--tier A,B] [--category docker,node]
                         [--stale 90d] [--depth 2] [--out report.json]
reclaim select           [--report report.json] [--preset safe|aggressive] [--out plan.json]
reclaim apply plan.json  [--yes] [--log apply.log]
reclaim here [path]      [--depth 2] [--json]
reclaim scanners         # list registered scanners and what they look for
reclaim version
```

### scan

With no path, `scan` uses the configured roots.
The default roots on macOS are `~/Code`, `~/Developer`, `~/Projects`, `~/src`, `~/work`, `~/Downloads`, whichever exist, plus the well known cache locations each scanner knows about.
With one or more paths, `scan` restricts the project scanners to those paths and skips the global cache scanners unless `--all` is given.
The report groups findings by category, then by project, sorted by size descending.
`--json` writes the full report, `--md` writes a Markdown summary, and `--out` saves the JSON report for `select` to consume.
A scan never modifies anything.

### select

`select` reads the last report, or the one given with `--report`, and opens an interactive checklist.
It writes a plan file.
`--preset safe` writes a plan with every tier A finding and skips the checklist.
`--preset aggressive` adds tier B.
The checklist must handle thousands of findings without the user scrolling through them one by one.
See "Handling long lists".

### apply

`apply` reads a plan file, prints every action it is about to take with its size, and asks the user to type `yes`.
`--yes` skips the prompt for scripted use.
It executes actions sequentially, logs each one with its result and the bytes freed, and stops on the first error unless `--keep-going` is set.
After it finishes, it prints the total freed and the log path.
Filesystem deletions go through `os.RemoveAll` on the exact path recorded in the plan.
Before removing, `apply` re-checks that the path still exists, is still the same kind of thing the scanner found, and is still inside an allowed root.
A plan older than 24 hours is refused unless `--stale-ok` is given, because the disk may have changed.

### here

`here` is the granular mode.
It answers "what in this folder takes space, and which of it can go".
It prints the children of the folder sorted by size, like `du` with one level of depth, and marks every child that a scanner recognizes as reclaimable with its tier and category.
`--depth` increases how deep the breakdown goes.
Reclaimable entries found at any depth are listed in a second section with their full paths, so the user sees both the shape of the folder and the exact candidates.
`here` findings can be piped into `select` through `--out`, exactly like `scan`.

## Architecture

```
cmd/reclaim            cobra commands, flag parsing, output selection
internal/scan          Scanner interface, registry, concurrent runner
internal/scanners/...  one package per scanner
internal/finding       Finding, Tier, Category, Report types and JSON schema
internal/plan          Plan file format, validation, staleness checks
internal/apply         Executors: filesystem removal, shell command, docker
internal/dockerx       Thin wrapper over the Docker Engine API client
internal/fsx           Fast concurrent directory size calculation, symlink and mount boundary handling
internal/project       Project detection (markers, type, last activity)
internal/ui            Table rendering, Markdown rendering, interactive checklist
internal/config        Config file loading and default roots
```

Each scanner is a package that registers itself from `init` or from an explicit list in `internal/scanners/all.go`.
Prefer the explicit list.
Hidden registration via `init` makes the set of scanners harder to see.

### Scanner interface

```go
type Scanner interface {
    // Name is a short stable identifier such as "node_modules" or "docker-images".
    Name() string
    // Category groups findings in reports, such as "project", "package-cache", "docker", "app-cache".
    Category() Category
    // Scan returns findings. It must not modify anything. It must respect ctx cancellation.
    Scan(ctx context.Context, env Env) ([]Finding, error)
}
```

`Env` carries the roots, the home directory, the OS, a filesystem abstraction, a Docker client that may be nil, and a logger.
Everything a scanner needs comes from `Env` so tests can substitute fakes.
Scanners run concurrently with a bounded worker count.
A scanner failing, for example because Docker is not running, produces a warning in the report and does not fail the scan.

### Finding

```go
type Finding struct {
    ID          string    // stable hash of scanner name plus target
    Scanner     string
    Category    Category
    Tier        Tier      // A, B or C
    Path        string    // filesystem path, or empty for non-filesystem targets
    Target      string    // docker image id, volume name, or the same as Path
    Size        int64     // bytes, apparent size on disk
    Project     string    // owning project root when known
    LastUsed    time.Time // best effort: project last commit, container finished time, file mtime
    Restore     string    // how to get it back, such as "npm install" or "docker pull"
    Action      Action    // what apply does: RemovePath, DockerRemoveImage, RunCommand
    Command     []string  // for RunCommand actions, the exact argv
    NeedsSudo   bool
    Warning     string    // shown in the checklist for tier C findings
}
```

Sizes are measured with a concurrent walker in `internal/fsx`.
It counts apparent file size and hard links once.
It does not cross filesystem boundaries.
It does not follow symlinks.
It skips paths it cannot read and records them as warnings.

### Tiers

Tier A regenerates itself the next time the project builds or installs.
Examples: `node_modules`, `.venv`, Rust `target`, Go build caches, `.next`, `dist` when a build config exists, `__pycache__`, dangling Docker images, Docker build cache, stopped containers that were created from compose, Electron updater caches.
Tier A findings are preselected in the checklist.

Tier B can be re-downloaded or rebuilt but costs real time or bandwidth.
Examples: package manager stores such as the npm cache, pnpm store, Go module cache, uv cache, Homebrew cache, Playwright browsers, Ollama models, simulator runtimes, Docker images that are not dangling but are not used by any container, Docker volumes not attached to any container.
Tier B findings are shown and never preselected.

Tier C is data.
Examples: Docker volumes attached to a stopped container, browser profiles, large media in a project, archives in Downloads that have an extracted sibling.
Tier C findings carry a warning, are never preselected, and cannot be selected through a group toggle.
The user must tick each one individually.

A scanner decides the tier per finding, not per scanner.
A `dist` folder with no build config next to it is tier C, because it may be the only copy.

### Project detection

A project is a directory containing a marker: `.git`, `package.json`, `go.mod`, `Cargo.toml`, `pyproject.toml`, `requirements.txt`, `pom.xml`, `build.gradle`, `Package.swift`, `*.xcodeproj`, `pubspec.yaml`, `composer.json`, `Gemfile`, `mix.exs`.
Project scanners walk the roots, stop descending at artifact folders, and do not descend into another project's artifact folders.
Last activity is the newest of the last git commit date and the newest source file mtime, excluding artifact folders.
`--stale 90d` limits project findings to projects with no activity in that window.
Projects inside artifact folders, for example a `package.json` inside `node_modules`, are not projects.

### Docker scanner

Uses the Docker Engine API through the official client, honoring `DOCKER_HOST` and the current context.
It uses `docker system df` style data plus `ContainerList(all)`, `ImageList`, `VolumeList` and `BuildCachePrune` dry-run data.
Findings:

- Stopped or created containers: tier A if their image still exists, with action `DockerRemoveContainer`.
- Dangling images: tier A.
- Images with no containers: tier B.
- Volumes with no containers: tier B, with a warning that volume contents are not inspected.
- Volumes attached only to stopped containers: tier C.
- Build cache: tier A, one finding for the whole cache with the reclaimable size, action `DockerPruneBuildCache`.

The scanner never offers running containers or their images and volumes.
If OrbStack is detected, the report notes that OrbStack returns host space automatically after removal.

### Package cache scanners

One finding per cache, with the native clean command as the action where one exists, otherwise `RemovePath`.
Covered: npm `_cacache` and `_npx`, pnpm store and cache, bun install cache, yarn cache, uv cache, pip cache, Go module cache and build cache, cargo registry and git caches, Homebrew cache with `brew cleanup -s`, swiftpm cache, gradle caches, maven repository, CocoaPods cache, composer cache.
The scanner locates each cache by asking the tool when it is installed, for example `npm config get cache`, and falls back to the default location.

### App cache scanners

- `~/Library/Caches/*` entries above the size threshold, tier A, excluding a small denylist of apps known to store data there.
- Electron updater leftovers: `*.ShipIt`, `*-updater` folders in `~/Library/Caches`, tier A.
- Playwright and Puppeteer browser downloads, tier B.
- Xcode `DerivedData`, tier A. Simulator devices in `~/Library/Developer/CoreSimulator/Devices` for unavailable runtimes, tier B.
- Simulator runtimes under `/Library/Developer/CoreSimulator`, tier B, `NeedsSudo`, with the `xcrun simctl runtime delete` command.
- Ollama models, tier B, one finding per model with `ollama rm` as the action.
- Agent tool leftovers: Codex worktrees and sessions, Claude VM bundles, tier B.

### Downloads scanner

- Installer files: `.dmg`, `.pkg`, `.iso`, `.zip` and `.app.tar.*` whose application name matches an app in `/Applications`, tier B.
- Archive with an extracted sibling, detected by matching the archive stem to a sibling directory, tier C on the archive with a warning naming the sibling.
- Any file above a large threshold not touched in a long time, listed under an "attention" section but with no action, so the user sees it without the tool offering to delete it.

## Handling long lists

The checklist is a tree, not a flat list.
Top level nodes are categories.
Under a category, project findings are grouped by project, and package caches are one row each.
Every node shows its aggregate size and count.
Toggling a group toggles its tier A and tier B children and never its tier C children.
Keys: space toggles, enter expands, `/` filters by text, `s` cycles sort, `t` filters by tier, `a` selects all tier A, `w` writes the plan and exits, `q` quits without writing.
The footer always shows the selected count and total size.
The plan file is JSON and the checklist tells the user where it was written, so editing it by hand is always an option.
For users who never want a screen, `--preset` and `--json` piped through `jq` give the same result.

## Plan file

```json
{
  "version": 1,
  "created": "2026-10-05T12:00:00Z",
  "host": "machine-name",
  "actions": [
    {
      "id": "sha256:...",
      "action": "RemovePath",
      "path": "/Users/me/Code/app/node_modules",
      "size": 1234567,
      "tier": "A",
      "scanner": "node_modules",
      "restore": "npm install"
    }
  ]
}
```

`apply` validates the schema, refuses unknown actions, and refuses paths outside the roots recorded at scan time.

## Safety rules

- No command deletes anything by default.
Only `apply` deletes, only from a plan, only after confirmation.
- `apply` re-verifies every target immediately before acting on it.
- The tool never follows symlinks when deleting.
- The tool never deletes a project root, a `.git` directory, or anything that is not a finding produced by a scanner.
- The tool never deletes a path shorter than four components or any path in a hard coded denylist such as `/`, `/Users`, `/Users/*`, `~/Library`, `~/Documents`, `~/Desktop`.
- Running containers, their images and their volumes are never offered.
- Anything that needs `sudo` is printed as a command and never executed.
- Every `apply` run writes a log with timestamps, actions and results.

## Configuration

Optional file at `~/.config/reclaim/config.toml`.

```toml
roots = ["~/Code", "~/Downloads"]
exclude = ["~/Code/archive"]
min_size = "20MB"
stale = "60d"

[scanners]
disable = ["ollama"]
```

Flags override the config file.

## Output formats

The terminal table uses lipgloss for layout, human readable sizes, and colors only when stdout is a terminal.
`--json` is stable and versioned.
`--md` is for pasting into an issue or a document.

## Testing

- Unit tests for every scanner using an in-memory or temp directory fixture.
Fixtures build a fake home with projects, artifact folders and caches, then assert on the findings.
- `fsx` size tests cover hard links, symlinks, unreadable directories and cancellation.
- Docker tests run against a real daemon when `RECLAIM_DOCKER_TESTS=1` is set.
They create their own fixtures: a `busybox` image, a stopped container, an unused volume and a small build, all labeled `reclaim.test=1`.
They assert that the scanner finds exactly those fixtures when filtered by the label and that `apply` removes them.
They never touch anything without the label.
- An end-to-end test runs `scan` on a fixture tree, writes a plan with `--preset safe`, runs `apply --yes`, and asserts that only the expected paths are gone.
- CI runs `go test -race` on macOS and Linux and `golangci-lint`.

## Phases

Phase 1 delivers `scan`, `here`, `select --preset`, `apply`, the project scanners, the package cache scanners, the Docker scanner, JSON and table output, and the test suite.
Phase 2 delivers the interactive checklist, the app cache scanners, the Downloads scanner, Markdown output and the config file.
Phase 3 delivers GoReleaser, a Homebrew tap, shell completion and a README with screenshots.

Each phase is a pull request or a short series of pull requests.
Every pull request passes CI and includes tests for what it adds.

## Acceptance

On the reference machine, `reclaim scan` completes in under two minutes over 165 GB of projects and reports, within 5 percent, the following that were measured by hand: 57 GB of project artifacts, about 109 GB of reclaimable Docker data and about 40 GB of package caches.
`reclaim here` in a project folder shows the breakdown within one second for a folder up to 20 GB.
`reclaim apply` on the end-to-end fixture removes exactly the planned paths and nothing else.
