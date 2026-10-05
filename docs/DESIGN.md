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
                         [--stale 90d] [--depth 2] [--out report.json] [--all]
                         [--docker-label key=value]
reclaim select           [--report report.json] [--preset safe|aggressive] [--out plan.json]
reclaim apply plan.json  [--yes] [--log apply.log] [--keep-going] [--stale-ok]
reclaim here [path]      [--depth 2] [--json] [--out report.json]
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
The only file it writes is its own copy of the report, `last-report.json` in the reclaim folder under the user cache directory, which `select` reads by default.

`--min-size` defaults to 10 MB so thousands of tiny `__pycache__` folders do not bury the large findings; pass `--min-size 0` to see everything.
`--category` accepts a category such as `docker`, an ecosystem such as `node` or `python`, or a scanner name such as `node_modules`, and an unknown value is an error.
`--depth` limits how many levels below each root the project walk descends, and 0 means no limit.
`--docker-label` may be repeated, and every label must match.
The report records its roots: the scan roots, plus the home directory when package cache or Docker scanners ran, because caches live there.
Nested roots are kept, so apply still refuses to remove `~/Code` itself when `~` is also a root.

### select

`select` reads the last report, or the one given with `--report`, and opens an interactive checklist.
It writes a plan file.
`--preset safe` writes a plan with every tier A finding and skips the checklist.
`--preset aggressive` adds tier B.
The checklist must handle thousands of findings without the user scrolling through them one by one.
See "Handling long lists".

Until the checklist lands in phase 2, `select` without `--preset` prints a numbered list and reads a selection such as `1,4-9,12` from standard input.
Tier C items are selected only when their number is listed on its own; a range that covers them skips them and says so.

### apply

`apply` reads a plan file, prints every action it is about to take with its size, and asks the user to type `yes`.
`--yes` skips the prompt for scripted use.
It executes actions sequentially, logs each one with its result and the bytes freed, and stops on the first error unless `--keep-going` is set.
After it finishes, it prints the total freed and the log path.
Filesystem deletions go through `os.RemoveAll` on the exact path recorded in the plan.
Before removing, `apply` re-checks that the path still exists, is still the same kind of thing the scanner found, and is still inside an allowed root.
A plan older than 24 hours is refused unless `--stale-ok` is given, because the disk may have changed.
The age is measured from the scan the plan was built from, not from when the plan was written.

Re-verification of a filesystem target means: the path is clean and absolute, has at least four components, is not on the denylist, is not inside a `.git` directory, is not the home directory, a scan root, the owning project root or an ancestor of any of them, lies inside a recorded root, has no symbolic link anywhere in it, is still the kind of object the scanner found, does not hold a `.git` entry or a project manifest, is not itself a mount point, and has no other filesystem mounted inside it.
Measuring the target takes time, so right before removing it apply checks once more that no component of the path became a symbolic link and that the object is the same file it verified, then removes it through an `os.Root` opened at the recorded root, which cannot escape that root.
A target that no longer exists is reported as already gone and is not an error.
When a removal fails on read only directories, as in a Go module cache, apply adds owner permissions to the directories inside the target, refusing to enter another filesystem, measures again for mounts that unreadable folders hid, and tries once more.
Docker removals never force: a container must be stopped, an image must not be used by any container, and a volume must not be used by a running container.
Images are removed tag by tag and then by id, without pruning untagged parents, so the daemon refuses anything still referenced and nothing outside the plan goes with them.
The plan records each image's tags at scan time, and an image whose tags changed since, or that other images are built on, is refused before any tag is removed.
Bytes freed are measured before and after for filesystem and command actions, reported by the daemon for the build cache, and estimated from the scan for other Docker objects.
The log is JSON lines: one entry when the run starts, one per action with its timestamp, status, bytes freed and any error or command output, and one when it ends.
The default log is a new file in the reclaim folder under the user cache directory.
A plan made on another host is applied with a warning, because host names on macOS change with the network.

### here

`here` is the granular mode.
It answers "what in this folder takes space, and which of it can go".
It prints the children of the folder sorted by size, like `du` with one level of depth, and marks every child that a scanner recognizes as reclaimable with its tier and category.
`--depth` increases how deep the breakdown goes.
Reclaimable entries found at any depth are listed in a second section with their full paths, so the user sees both the shape of the folder and the exact candidates.
`here` findings can be piped into `select` through `--out`, exactly like `scan`.
In phase 1, `here` runs the project scanners only, since package caches and Docker live outside project folders.
It measures the folder in one walk that records the size of every directory, and the scanners take artifact sizes from that walk instead of measuring them again.
Project discovery still reads the folders outside artifacts a second time, which is the cheap part of the tree.
`--json` prints `{"version": 1, "path", "size", "children", "findings", "warnings", "notes"}`, where each child has `name`, `path`, `dir`, `size` and nested `children` down to `--depth`.

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

Scanners are registered in an explicit list in `internal/scanners/all.go`.
Hidden registration via `init` makes the set of scanners harder to see.
Scanners are grouped by family in `internal/scanners/projects`, `internal/scanners/caches` and `internal/scanners/docker` instead of one package per scanner.
Every rule is still its own scanner with its own name, but the project scanners share one walk of the roots and the cache scanners share their location helpers, so a package per scanner would only add boilerplate.

### Scanner interface

```go
type Scanner interface {
    // Name is a short stable identifier such as "node_modules" or "docker".
    Name() string
    // Category groups findings in reports, such as "project", "package-cache", "docker", "app-cache".
    Category() Category
    // Ecosystem is the tool family, such as "node" or "python". --category accepts it as a selector.
    Ecosystem() string
    // Description says in one line what the scanner looks for, for `reclaim scanners`.
    Description() string
    // Scan returns findings. It must not modify anything. It must respect ctx cancellation.
    Scan(ctx context.Context, env Env) ([]Finding, error)
}
```

`Env` carries the roots, the home directory, the OS, an environment variable reader, a command runner for asking tools where their caches live, a Docker client that may be nil, the Docker label filter, the size walker, the shared project walk, sizes already measured in the same run, a logger and a diagnostics sink for warnings and notes.
Everything a scanner needs comes from `Env` so tests can substitute fakes.
There is no general filesystem abstraction.
The walker needs device ids, inodes and link counts, which an in-memory filesystem would have to fake, so scanner tests build real fixtures in temp directories instead.
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
    Kind        Kind      // "dir" or "file" for path findings, checked again before removal
    Target      string    // docker image id, volume name, or the same as Path
    Name        string    // short label for tables, such as an image tag
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

The JSON keys are the snake case field names, such as `last_used` and `needs_sudo`, and a report carries `"version": 1`.

Sizes are measured with a concurrent walker in `internal/fsx`.
It counts apparent file size and hard links once.
It does not cross filesystem boundaries.
It does not follow symlinks.
It skips paths it cannot read and records them as warnings.
Hard links are deduplicated within one measurement, so a pnpm `node_modules` that hard links into the store still shows its full size, which is what removing it alone would not free; the pnpm findings say so.
All size walks in a run share one bound on concurrent directory reads, by default the number of CPUs between 4 and 8, because more parallel readers add kernel contention on APFS instead of speed.
Project discovery has its own bound of the same size; the project scanners only start measuring once discovery has finished, so the two overlap only with the package cache and Docker scanners.

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

All project scanners share one concurrent walk per scan.
The walk never enters `.git`, never follows symbolic links and never crosses into another filesystem.
It never treats a directory holding a `.git` entry or a project manifest such as `package.json` or `pyproject.toml` as an artifact, so a `dist` folder checked out as a git worktree, or a project that ran `python -m venv .` in its own root, is safe.
A root that is an artifact folder or lies inside one, such as `~/Code/web/node_modules/left-pad`, is not walked, and the report says why.
In a git working tree, an artifact that git does not ignore, or that holds tracked files, is tier C whatever its rule says, because it may be committed source such as electron-builder's `build` resources; this needs `git`, and without it the rule's tier stands.
A finding belongs to the nearest enclosing git working tree, so the packages of a monorepo group under the repository.
Outside git, the nearest directory with a marker owns its contents.
When a scan root lies inside a project, the walk looks for that project in the root's ancestors, stopping below the home directory so a dotfiles repository in `~` does not swallow every project.

The project scanners are `node_modules`, Python virtual environments (any folder with `pyvenv.cfg`), Rust `target` next to `Cargo.toml` or tagged by cargo, in-repo Go build caches (`.cache/go-build` or the GOCACHE layout, which also covers golangci-lint caches), `.next`, `.turbo`, `.nuxt`, `.svelte-kit`, `.parcel-cache`, `dist` and `build`, `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, Gradle `.gradle`, CocoaPods `Pods`, `.dart_tool` and `.terraform`.
Two more were added in phase 1 because the reference machine had gigabytes in them: in-repo Go module caches (a GOMODCACHE layout with `cache/download`) and in-repo pnpm stores (a `store-dir` with `v<N>/files`), both tier B because they are downloaded again.
`dist` and `build` are only considered directly inside a project root.
They are tier A when the project has a build config: a `build` script in `package.json`, a bundler or framework config such as `vite.config.*`, Angular, Gradle, Python packaging, CMake or Meson, or `pubspec.yaml`; otherwise they are tier C.
A `node_modules` without a `package.json` next to it and a virtual environment outside any project are tier B, because an install will not recreate them.

### Docker scanner

Uses the Docker Engine API through the official client, honoring `DOCKER_HOST` and the current context.
It uses `docker system df` style data plus `ContainerList(all)`, `ImageList`, `VolumeList` and `BuildCachePrune` dry-run data.
Findings:

- Stopped or created containers, with action `DockerRemoveContainer`: tier A when compose created them and their image still exists, as the tiers section says, and tier B otherwise, because a container made by hand may hold work in its writable layer.
- Dangling images: tier A.
- Images with no containers: tier B.
- Volumes with no containers: tier B, with a warning that volume contents are not inspected.
- Volumes attached only to stopped containers: tier C.
- Build cache: tier A, one finding for the whole cache with the reclaimable size, action `DockerPruneBuildCache`.

The scanner never offers running containers or their images and volumes.
It is a single scanner named `docker` that reports every kind of Docker finding from one pass over the daemon.
Images used by any container, running or stopped, are not offered, because the daemon refuses to remove them; removing the container first frees them for the next scan.
Image sizes are the unique size, the image size minus the size shared with other images, which matches the reclaimable figure of `docker system df`.
A stopped container whose image no longer exists is tier B, because it cannot be recreated as it was.
`--docker-label key=value` limits Docker findings to objects with that label.
The build cache is skipped while a label filter is active, because cache records carry no labels.
If OrbStack is detected, the report notes that OrbStack returns host space automatically after removal.

### Package cache scanners

One finding per cache, with the native clean command as the action where one exists, otherwise `RemovePath`.
Covered: npm `_cacache` and `_npx`, pnpm store and cache, bun install cache, yarn cache, uv cache, pip cache, Go module cache and build cache, cargo registry and git caches, Homebrew cache with `brew cleanup -s`, swiftpm cache, gradle caches, maven repository, CocoaPods cache, composer cache.
The scanner locates each cache by asking the tool when it is installed, for example `npm config get cache`, and falls back to the documented environment variables and the default location.
The native clean command is used only when the location came from the tool itself, so the command acts on the directory that was measured.
CocoaPods cannot be asked where its cache is, so its cache is removed with `RemovePath` instead of `pod cache clean`.
Apply runs only commands from a fixed allowlist of exactly these argument lists, so a hand edited plan cannot run anything else.
Tools are queried from `/` with a timeout, with corepack downloads and update checks disabled.

### App cache scanners

- `~/Library/Caches/*` entries above the size threshold, tier A, excluding a small denylist of apps known to store data there.
- Electron updater leftovers: `*.ShipIt`, `*-updater` folders in `~/Library/Caches`, tier A.
- Playwright and Puppeteer browser downloads, tier B.
- Xcode `DerivedData`, tier A.
Simulator devices in `~/Library/Developer/CoreSimulator/Devices` for unavailable runtimes, tier B.
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
The fixture images are built from `LABEL` only Dockerfiles on top of `busybox`, so the build leaves no unlabeled intermediate images, and the `busybox` base image is never removed.
The build cache prune is covered by unit tests against a fake client only, because a real prune cannot be limited to labeled records.
Without `RECLAIM_DOCKER_TESTS=1` the tests skip, and the scanner and the Docker executors are unit tested against an in-memory fake of the client interface in `internal/dockerx`.
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
