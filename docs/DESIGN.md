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
reclaim here [path]      [--depth 2] [--json] [--md] [--out report.json]
reclaim worktrees [path...] [--json] [--md] [--out report.json]
reclaim done [path]      [--json] [--remove] [--allow-unmerged]
reclaim skill            # print the agent skill that tells an agent to run reclaim done
reclaim scanners         # list registered scanners, what they look for and whether the config disables them
reclaim completion bash|zsh|fish|powershell
reclaim version
```

Every command accepts `--config path` to read another configuration file and `--verbose` for debug logs.
`reclaim --help` explains which configuration file is read, in the order given under "Configuration".

### completion

`completion` is cobra's command and writes a completion script for the shell.
The scripts complete commands and flags, and reclaim gives them real values: `A`, `B` and `C` for `--tier`, every category, ecosystem and scanner name for `--category`, `safe` and `aggressive` for `--preset`, a few sizes and ages for `--min-size` and `--stale`, TOML files for `--config`, JSON files for `select --report`, the `--out` flags and the plan argument of `apply`, and folders for the paths of `scan` and `here`.
Flags that take a comma separated list offer the values not listed yet after the last comma.
Values carry a short description, which zsh and fish show.

### version

`version` prints `reclaim <version> <go version> <os>/<arch>`.
A release build is stamped with `-X main.version=<version>`, which GoReleaser fills with the tag without its `v`; reclaim adds the `v` back, so a release prints its tag, such as `v0.1.0`.
A `go install` build prints the module version from the build info, which is the tag or a pseudo version, and a build from a checkout prints `(devel)`.

### scan

With no path, `scan` uses the roots from the configuration file.
Without a configuration, the default roots are `~/Code`, `~/Developer`, `~/Projects`, `~/src`, `~/work`, `~/Downloads`, whichever exist, plus the well known cache locations each scanner knows about.
With one or more paths, `scan` restricts the project scanners and the root based worktree scanners, `git-worktrees` and `worktree-registrations`, to those paths and skips the package cache, Docker, app cache, Codex worktree and Downloads scanners unless `--all` is given.
The report groups findings by category, then by project, Docker object kind or scanner, sorted by size descending.
`--json` writes the full report, `--md` writes a Markdown summary, and `--out` saves the JSON report for `select` to consume.
A scan never modifies anything.
The only file it writes is its own copy of the report, `last-report.json` in the reclaim folder under the user cache directory, which `select` reads by default.

`--min-size` defaults to 10 MB so thousands of tiny `__pycache__` folders do not bury the large findings; pass `--min-size 0` to see everything.
`--category` accepts a category such as `docker`, an ecosystem such as `node` or `python`, or a scanner name such as `node_modules`, and an unknown value is an error.
`--depth` limits how many levels below each root the project walk descends, and 0 means no limit.
`--docker-label` may be repeated, and every label must match.
The report records its roots: the scan roots, plus the home directory when any scanner ran that looks outside the roots, because caches live there; the root based worktree scanners do not add it, so a scan given paths stays inside them.
Nested roots are kept, so apply still refuses to remove `~/Code` itself when `~` is also a root.
The report also records its scope: the command that wrote it, its arguments with every flag that was set, the resolved path arguments and the walked roots.
`scan` and `here` both save the most recent report as `last-report.json`, whatever they covered, because running `here` and then `select` is a supported flow; the scope is what lets `select` say which kind of report it is reading.

### select

`select` reads the last report, or the one given with `--report`, and opens an interactive checklist.
It writes a plan file.
`--preset safe` writes a plan with every tier A finding and skips the checklist.
`--preset aggressive` adds tier B.
The checklist must handle thousands of findings without the user scrolling through them one by one.
See "Handling long lists".

Before the list or the checklist, `select` prints one provenance line: the command that produced the report, how long ago and when, how many findings it holds, how many more are listed for attention only, and their reclaimable total.
When the report came from `here` or from a scan given paths, the line says that it covers only those folders, not the whole machine, and suggests `reclaim scan`.
A report written before scopes existed is described as coming from an older reclaim.

The checklist opens only when standard input and standard output are both terminals and `--preset` is not given.
Otherwise `select` prints a numbered list, grouped exactly like the scan table, and reads a selection such as `1,4-9,12` from standard input, so scripts and tests keep working.
Tier C items are selected only when their number is listed on its own; a range that covers them skips them and says so.
Findings listed for attention only are printed without a number and with a blank tier column, so they can never be selected, and building a plan refuses them as well.

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
A plan action with the `None` action, which marks a finding listed for attention only, fails validation, so a hand edited plan cannot apply one, and apply refuses it again on its own.

`RunCommand` actions are limited to a fixed list.
The package cache clean commands are exact argument lists.
Removal commands that name their target, `ollama rm <model>`, `xcrun simctl delete <device>` and `xcrun simctl runtime delete <runtime>`, take exactly one argument that must match a strict pattern: a UUID for the simulator commands and a model name that cannot start with a dash for Ollama.
For a clean command that came from a tool's own location query, apply asks the tool again right before running it, with the same fixed query, and refuses when the answer, with symbolic links resolved, is not the directory recorded in the plan.
A plan action for such a command without a path is refused too.
If the tool now reports the recorded directory and it no longer exists, the action is reported as already gone.
Plan validation ties the argument of a removal command to the action's target, which the id covers: the Ollama target is `ollama:<model>`, a simulator's path ends in its UDID, and a runtime's target is `simulator-runtime:<identifier>`, so a hand edit cannot point the command at something else.
Right before running them, apply checks those targets again.
`ollama rm` runs only while the model's manifest is still in the models folder the plan records, and only when `OLLAMA_HOST` is unset or names this machine, so a same named model on another server is never removed.
`xcrun simctl delete` runs only while `xcrun simctl list` still reports the simulator as unavailable, so a simulator whose runtime came back is kept.
A target that no longer exists is reported as already gone.

### here

`here` is the granular mode.
It answers "what in this folder takes space, and which of it can go".
It prints the children of the folder sorted by size, like `du` with one level of depth, and marks every child that a scanner recognizes as reclaimable with its tier and category.
`--depth` increases how deep the breakdown goes.
Reclaimable entries found at any depth are listed in a second section with their full paths, so the user sees both the shape of the folder and the exact candidates.
`here` findings can be piped into `select` through `--out`, exactly like `scan`.
`here` runs the project scanners only, since package caches, Docker and app caches live outside project folders.
The configuration's `exclude` paths and disabled scanners apply to it as they do to `scan`.
It measures the folder in one walk that records the size of every directory, and the scanners take artifact sizes from that walk instead of measuring them again.
Project discovery still reads the folders outside artifacts a second time, which is the cheap part of the tree.
`--json` prints `{"version": 1, "path", "size", "children", "findings", "warnings", "notes"}`, where each child has `name`, `path`, `dir`, `size`, nested `children` down to `--depth`, and `worktree`, the state in words, for a linked worktree.
`--md` prints the same breakdown and findings as Markdown tables.

### worktrees

`worktrees` runs only the worktree scanners, over the repositories under the given paths or under the configured roots, and prints one table per repository with every linked worktree, including the ones reclaim never offers, so the user sees the whole picture even when nothing is reclaimable.
Columns: the tier or a dash for a worktree that is listed only, tool, branch or commit, uncommitted, unpushed, merged, pull request, lock, size and path.
Under a worktree that is listed only it prints why, the retrieval hints and the `--force` command, with the home directory shortened to `~`; stale registrations, other clones and Codex task folders follow the table.
It runs all three worktree scanners, the Codex one too, reports worktrees held by a live lock as listed only, and applies no `--min-size`.
`--json` prints the report with the full state of every worktree, `--md` prints Markdown, and `--out` saves a report that `select` reads, so `reclaim worktrees` followed by `reclaim select` cleans up worktrees alone.
Like `scan`, it saves `last-report.json`.

### done

`done` is the one command an agent, or a person, runs at the end of a task in a worktree.
It takes the path of a linked worktree, defaulting to the current directory, and:

1. classifies it exactly as the worktree scanner does, pull request lookup included;
2. prints the state in plain words, such as "committed, pushed, merged in PR #3, 158 MB", and with `--json` as the same state object the scanner writes;
3. exits 0 when the worktree is tier A, 1 when it is tier B, 2 when it holds work that exists nowhere else, printing the retrieval hints, nested repositories, submodules and hidden changes included, and 3 when the path is not a linked worktree or the state could not be determined because a git or `ps` command failed, saying that reclaim could not tell whether its work exists elsewhere;
the exit code judges the work and not the lock, because an agent's own running session holds the lock of its worktree, and the words say which lock it is, "in use by pid 30201" or "stale lock, pid 50949 gone";
4. with `--remove`, removes a tier A worktree through the same plan, proof and apply path as everything else, writing the plan and the log to the reclaim cache folder, and refuses anything else with the reason.

`--remove --allow-unmerged` extends removal to tier B, for agents whose branches are reviewed elsewhere.
With `--remove`, exit 0 means the worktree is gone.
A refusal exits by the check that failed: 1 when the work is safe and only the removal was refused, as for a lock, the working directory or a path outside the roots; the code of the state computed again when the work or `HEAD` changed since `done` classified it; 2 when git itself refused because of files to keep; and 3 when it is no longer a linked worktree or the failure is of another kind.
A mistake on the command line exits 3, never 1.
With `--json` the state goes to standard output and every other line to standard error.
A path inside a worktree finds the worktree.
A worktree with work that exists nowhere else is never removed by `done`, whatever flags are given; `done` prints the retrieval hints and the `git worktree remove --force` command for the user to run by hand.
`done` refuses to remove a worktree that contains its own working directory, so an agent runs it from the main checkout, and the message says so.
It never deletes branches and never pushes.

### skill

`skill` prints `skills/reclaim/SKILL.md`, embedded in the binary, so `reclaim skill > ~/.claude/skills/reclaim/SKILL.md` installs it.
The skill tells an agent to run `reclaim done --json` when its task is finished, read the exit code, push or open a pull request first when the state says so, and remove only after the work is merged.
`docs/AGENTS.md` holds the same instructions as a snippet for the `AGENTS.md` or `CLAUDE.md` of other projects and for Codex.
The skill lives in the `skills` package at the repository root, because `go:embed` cannot reach a file above the embedding package.

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
internal/worktree      Worktree discovery and state classification shared by the scanners, done and apply
internal/ui            Table rendering, Markdown rendering, interactive checklist
internal/config        Config file loading
```

Scanners are registered in an explicit list in `internal/scanners/all.go`.
Hidden registration via `init` makes the set of scanners harder to see.
Scanners are grouped by family in `internal/scanners/projects`, `internal/scanners/caches`, `internal/scanners/docker`, `internal/scanners/appcache` and `internal/scanners/downloads` instead of one package per scanner.
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

`Env` carries the roots, the home directory, the OS, the time the scan started, the folders holding installed applications, the models folder of a system wide Ollama service on Linux, an environment variable reader, a command runner for asking tools where their caches live, a Docker client that may be nil, the Docker label filter, the size walker, the shared project walk, sizes already measured in the same run, a logger and a diagnostics sink for warnings and notes.
Everything a scanner needs comes from `Env` so tests can substitute fakes.
There is no general filesystem abstraction.
The walker needs device ids, inodes and link counts, which an in-memory filesystem would have to fake, so scanner tests build real fixtures in temp directories instead.
Scanners run concurrently with a bounded worker count.
A scanner failing, for example because Docker is not running, produces a warning in the report and does not fail the scan.

A scanner that reports every entry of a shared folder, such as the one for `~/Library/Caches`, implements the optional `CatchAll` interface.
The runner drops each catch-all finding whose path contains, equals or lies inside the path of a finding from any other scanner, so the same bytes are never offered twice and the specific scanner, which knows the right tier and restore hint, wins.

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
    Action      Action    // what apply does: RemovePath, RunCommand, a Docker action, or None
    Command     []string  // for RunCommand actions, the exact argv
    NeedsSudo   bool
    Warning     string    // shown in the checklist for tier C findings
}
```

The JSON keys are the snake case field names, such as `last_used` and `needs_sudo`, and a report carries `"version": 1`.

The `None` action marks a finding that is shown but never applied: a large old file in Downloads listed for the user's attention.
Such findings are tier C in the data, carry a warning that says reclaim never removes them, are excluded from every reclaimable total and from the per tier totals, appear in their own group, cannot be ticked in the checklist, get no number in the numbered list, are never picked by a preset, and are refused when building or validating a plan.
Because they carry no action, the table, the Markdown summary, the numbered list and the checklist leave their tier column blank, and the checklist marks their rows as not selectable.

A report carries an optional `scope` object with `command`, `args`, `paths` and `roots`, described under `scan`.
Reports without one remain valid.

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
Examples: `node_modules`, `.venv`, Rust `target`, Go build caches, `.next`, `dist` when a build config exists, `__pycache__`, dangling Docker images, Docker build cache, stopped containers that were created from compose, Electron updater caches, app cache folders, Xcode DerivedData.
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
The scan table, the Markdown summary, the numbered list and the checklist group Docker findings by kind, in this order: build cache, stopped containers, dangling images, unused images, volumes.
Each group shows its subtotal and count, like project groups do.

### Package cache scanners

One finding per cache, with the native clean command as the action where one exists, otherwise `RemovePath`.
Covered: npm `_cacache` and `_npx`, pnpm store and cache, bun install cache, yarn cache, uv cache, pip cache, Go module cache and build cache, cargo registry and git caches, Homebrew cache with `brew cleanup -s`, swiftpm cache, gradle caches, maven repository, CocoaPods cache, composer cache.
The scanner locates each cache by asking the tool when it is installed, for example `npm config get cache`, and falls back to the documented environment variables and the default location.
The native clean command is used only when the location came from the tool itself, so the command acts on the directory that was measured.
CocoaPods cannot be asked where its cache is, so its cache is removed with `RemovePath` instead of `pod cache clean`.
Apply runs only commands from a fixed allowlist of exactly these argument lists, so a hand edited plan cannot run anything else.
Tools are queried from `/` with a timeout, with corepack downloads and update checks disabled.
The allowlist pairs every clean command with the query that located its cache, and apply runs that query again before the command, as described under `apply`.

### App cache scanners

All of them are in category `app-cache` and are read only.
Scanners for macOS only things return nothing on other systems.

- `user-caches`: every entry of the per user cache folder, `~/Library/Caches` on macOS and `$XDG_CACHE_HOME` or `~/.cache` elsewhere, that is a real directory of at least 1 MB, with the warning to quit the app first.
Entries of `~/Library/Caches`, a folder apps are told to treat as disposable, are tier A.
Entries of `~/.cache` on Linux are tier B, because apps use that folder less strictly, so they are never preselected.
The cache folder is scanned only when it lies strictly inside the home directory, so a cache variable that names the home directory or another folder does not turn its contents into cache findings; the scan warns instead.
Every finding is a direct child of the cache folder, never the folder itself, which apply protects anyway.
It skips entries other scanners report at their default locations (package caches, browser downloads, updater leftovers, and reclaim's own folder, which holds the last report and the apply logs), and a denylist of entries known to hold data or managed by the system: every `com.apple.*` and `com.docker.*` entry, CloudKit, Family, GameKit, PassKit and GeoServices data, Spotify's offline storage, Hugging Face and Torch models, Poetry virtual environments, Evolution mail and OrbStack.
It is a catch-all scanner, so a cache another scanner reports, even after being moved into the cache folder, is offered only once.
- `electron-updaters`: `*.ShipIt` and `*-updater` folders in the cache folder, tier A.
- `playwright`: the `ms-playwright` browser folder, or `PLAYWRIGHT_BROWSERS_PATH`, and `ms-playwright-go`, tier B, one finding per folder.
Playwright MCP keeps persistent browser profiles with logins in `ms-playwright-mcp` and sometimes in `ms-playwright` itself, as `mcp-*` folders; such folders are tier C with a warning naming the profiles.
- `puppeteer`: `~/.cache/puppeteer` or `PUPPETEER_CACHE_DIR`, tier B.
A browser folder named by `PLAYWRIGHT_BROWSERS_PATH` or `PUPPETEER_CACHE_DIR` is offered only when it lies strictly inside the home directory, is neither the cache folder nor holds it, and holds at least one entry that looks like a downloaded browser.
- `xcode-derived-data`: each folder in `~/Library/Developer/Xcode/DerivedData` and in the custom DerivedData location, tier A.
The custom location is the `IDECustomDerivedDataLocation` setting, read with `defaults read com.apple.dt.Xcode IDECustomDerivedDataLocation` through the command runner.
The default location is scanned even when a custom one is set, because Xcode leaves the folders it built there before the setting changed.
A custom location is scanned only when it lies strictly inside the home directory, because apply removes nothing outside the recorded roots, and the report notes when it does not.
Because a folder the user chose may hold other things, only folders Xcode made are offered there: its shared caches (`ModuleCache.noindex`, `SymbolCache.noindex`, `SDKStatCaches.noindex`, `CompilationCache.noindex`) and project folders named after the project with a dash and 28 lowercase letters that hold an `info.plist` recording a `WorkspacePath`.
- `simulator-devices`: simulators that `xcrun simctl list -j devices` reports unavailable, usually because their runtime is gone, tier B, removed with `xcrun simctl delete <udid>` so CoreSimulator's own records stay consistent.
- `simulator-runtimes`: deletable runtimes from `xcrun simctl runtime list -j`, tier B, `NeedsSudo`, with the `xcrun simctl runtime delete <identifier>` command for the user to run.
Without Xcode there is no `simctl`, and both simulator scanners report nothing.
- `ollama`: one finding per model, read from the manifests in `~/.ollama/models` or `OLLAMA_MODELS`, so the server need not run for a scan; tier B with `ollama rm <model>` as the action.
A model's size counts only the blobs no other model in its folder uses, because shared blobs are freed only when every model using them is removed; the warning says how much is shared and that `ollama rm` needs Ollama running.
On Linux the scanner also reads `/usr/share/ollama/.ollama/models`, where the service set up by Ollama's install script keeps its models.
Those models belong to the `ollama` account and serve every user, so their findings are `NeedsSudo`: apply prints the command and never runs it.
When that folder exists but cannot be read, the report notes it.
`ollama rm` removes a model from whichever server answers, which is usually the system service, so a model of the user's folder is offered only while the system folder can be read and holds no model of the same name; otherwise the report notes why it is left out, and apply checks the same right before running the command.
- Codex worktrees moved to the worktree scanners in phase 4.
- `codex-sessions`: Codex conversation logs, one finding per month folder in `sessions` whose newest file is older than 30 days, because resuming a conversation appends to its old log without changing the folder's time, and the `archived_sessions` folder, tier B.
- `claude-sessions` and `codex-plugin-backups`: see "Agent session leftovers".
- `claude-vm`: the `*.bundle` folders in `~/Library/Application Support/Claude/vm_bundles`, and Claude Code VM versions in `claude-code-vm` other than the one its `.sdk-version` names, tier B.

### Downloads scanner

Three scanners in category `downloads` look at the files directly inside `~/Downloads`.
They never descend into folders there and skip hidden files and symbolic links.

- `installers`: `.dmg`, `.pkg`, `.iso`, `.zip` and `.app.tar.*` files whose name matches an application in the applications folders, `/Applications` and `~/Applications` on macOS, including apps one folder deep such as `/Applications/DaVinci Resolve/DaVinci Resolve.app`, tier B.
The installer name must start with the app name on a word boundary, ignoring case, spaces and punctuation, so `DaVinci_Resolve_21.1_Mac.zip` matches `DaVinci Resolve.app` and `zen.macos-universal.dmg` matches `Zen.app`, but `Zenith-2.dmg` does not.
Every word after the app name must be a version or name a platform or release channel, such as `4.41.105`, `macOS`, `universal` or `arm64`, so `Notion Export.zip` or `Signal backup.dmg`, which may be the only copy of data, are not installers.
A zip must also hold an app bundle, a disk image or a package at its top level, read from its central directory without extracting anything.
- `extracted-archives`: archives (`.zip`, `.tar`, `.tar.gz`, `.tgz`, `.tar.bz2`, `.tbz2`, `.tar.xz`, `.txz`, `.tar.zst`, `.7z`, `.rar`) with a folder of the same name without the extension next to them, tier C on the archive with a warning naming that folder.
This rule wins over the installer rule, because the archive may be the only untouched copy.
- `old-downloads`: any other file of at least 250 MB that has been neither modified nor read for 90 days, using the later of the modification and access times, listed under an "attention" group with the `None` action, so the user sees it without the tool offering to delete it.

### Worktree scanners

Coding agents and people create git worktrees and leave them behind.
Claude Code puts them in `.claude/worktrees/agent-*` inside the repository, Codex in `$CODEX_HOME/worktrees/<task>/<repo>`, other tools in `.worktrees/` or `/tmp`.
Each one is a full checkout plus its own `node_modules`, `.venv` or build cache.
The scanners in category `worktree` live in `internal/scanners/worktrees` and use `internal/worktree` for discovery and classification, so `done` and `apply` reach the same answers from the same code.

Discovery:

- Every repository the project walk finds, a directory holding a `.git` directory, is asked for its linked worktrees with `git worktree list --porcelain`, which also reports registrations whose folder is gone and locks with their reasons.
- A directory holding a `.git` file is a linked worktree; its main repository is read from that file and the worktree is reported under that repository even when the repository lies outside the roots.
- Codex task folders are found as today, and each repository inside is classified like any other worktree.
- The main worktree of a repository is never a finding.

Every answer comes from read only git commands run through `execx.Runner`, so tests use fakes and the scan stays offline.
Git runs with `GIT_OPTIONAL_LOCKS=0`, so even `git status` refreshes no index.
Every command runs without the variables that point git at another repository, work tree, index or configuration, such as `GIT_DIR`, `GIT_WORK_TREE` and `GIT_INDEX_FILE`, which git hooks, `git rebase -x` and `git submodule foreach` set and which would make git describe the wrong worktree.
- `hidden`: tracked files marked assume-unchanged or skip-worktree hide their changes from `git status`, and `git worktree remove` deletes those changes without complaint, so `git ls-files -v` finds them and any one makes the state unknown.
- Nested repositories: any `.git` file or folder below the worktree other than its own, even inside an ignored artifact folder such as `node_modules`, is another repository whose commits and changes `git status` cannot see and `git worktree remove` deletes, so it makes the state unknown with the reason "nested repositories: <path>".
The size walker records such folders on the way, so the worktree is walked once for both.
A folder the walk cannot read, or another filesystem mounted inside, could hide one and makes the state unknown too.
- Submodules: a gitlink, mode 160000 in `git ls-files -s`, makes the state unknown with the reason "holds submodules; git worktree remove cannot remove it without --force", because forcing it would delete the submodule's repository with any commit not pushed to its own remote; the hints say to push the submodules first.
- `blockers`: the reasons, in words, for the parts of `unknown` that git did report but that make removal unsafe: nested repositories, submodules and hidden changes.
`scan` never fetches.
Merge state is as of the last fetch, which the report shows from the modification time of `FETCH_HEAD`.

State, per worktree, written to `--json` as `worktree` on the finding and carried into the plan:

- `repo`, `tool`, `branch` or detached, `head`, `upstream`.
- `default_ref` and `merge_note`: the full ref of the default branch, and why a patch id match was not confirmed.
- `exists`, `locked`, `lock_reason`, `lock_live`: a lock is live only when the process it names is running and, when the reason records a start time as Claude Code's locks do, started at that time, because process ids are reused.
The process table is read with `ps -o lstart= -p <pid>`.
Claude Code records the start time in UTC and `ps` prints local time, so the two are compared as wall clock readings in both zones, within a second.
A lock that names no process, or one whose process table cannot be read, counts as live: someone locked it on purpose, and it is never removed either way.
- `modified`, `untracked`: counts from `git status --porcelain`.
- `ignored`: ignored files outside recognized artifact folders, as a count and a sample, so `.env`, `*.local`, `*.sqlite` and the like are named; ignored artifact folders such as `node_modules` do not count.
They come from `git ls-files --others --ignored --exclude-standard --directory`, which reports a wholly ignored folder once.
An entry is an artifact when any folder in its path is one of `node_modules`, `.pnpm-store`, `.yarn`, `.venv`, `venv`, `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.tox`, `.nox`, `target`, `dist`, `build`, `out`, `bin`, `obj`, `.build`, `.next`, `.nuxt`, `.svelte-kit`, `.astro`, `.turbo`, `.parcel-cache`, `.angular`, `.expo`, `.output`, `.vercel`, `.cache`, `.tools`, `.gradle`, `Pods`, `.dart_tool`, `.terraform`, `coverage`, `.nyc_output`, `test-results`, `playwright-report`, `storybook-static`, `DerivedData`, `.swiftpm` or `vendor`, or when it is a generated file: `.DS_Store`, `*.tsbuildinfo`, `*.pyc`, `.eslintcache`, `*.log`, `*.coverprofile` or `coverage.out`.
- `loose`: commits reachable from `HEAD` that no branch, remote or tag holds.
- `unpushed`: commits reachable from `HEAD` that no remote ref holds, and `ahead` of the upstream when one is set.
- `merged`: `ancestor`, `rebase`, `squash`, `no` or `unknown`, against the default branch, which is `refs/remotes/origin/HEAD`, else `origin/main`, else `origin/master`, else local `main` or `master`.
`ancestor` is `git merge-base --is-ancestor`.
`rebase` is `git cherry <default> HEAD` reporting nothing missing.
`squash` is `git cherry` reporting a synthetic squash commit, `git commit-tree HEAD^{tree} -p <merge-base>`, as present in the default branch; it writes one dangling object that `git gc` collects and changes no ref and no file.
The checks run in that order because each is cheaper than the next.
`git cherry` compares patch ids, which ignore whitespace, so a `rebase` or `squash` match is confirmed exactly: `git diff-tree -p --no-color --full-index <merge-base> HEAD` for a squash, or `<commit>^ <commit>` for each commit of a rebase, must be byte for byte equal to `git diff-tree -p --no-color --full-index C^ C` of a commit C of the default branch since the merge base that touches the same files.
When it is not, `merged` is `no` and the state says "the branch's changes are not exactly in origin/main".
A branch whose commits undo each other, so that `HEAD` has the tree of the merge base, matches no commit of the default branch: `merged` is `no` with the note "its commits add up to no change", and the other counts decide its tier.
A branch with merge commits is left to the squash check, and more than 300 candidate commits count as no match.
- `pr`: number, state and URL from GitHub when the lookup ran.
- `fetched_at`, `default_branch` and `origin`.
- `unknown` and `problem`: the parts git could not report and why; any of them makes the worktree one reclaim never removes.
- `verdict` (`removable`, `pushed`, `attention` or `in-use`), `reasons`, `hints` and `force_command`, derived from the rest so a reader of the JSON need not repeat the rules.

The tool is inferred from the path and the lock reason: a lock reason starting with "claude agent" or a path under `.claude/worktrees` is `claude-code`; a path under `$CODEX_HOME/worktrees` is `codex`; a path under a `.worktrees` or `.conductor` folder is named after it without the dot, `worktrees` or `conductor`; anything else is `manual`.

Pull request state comes from `gh pr list --repo <owner>/<name> --head <branch> --state all --json number,state,url`, once per branch, when `gh` is installed and signed in and the repository's origin is on GitHub.
`--repo` is read from the origin URL, because the runner runs every tool from `/`, where `gh` cannot find a repository.
When a branch has several pull requests, a merged one wins, then an open one, then the newest.
The lookups run concurrently with a bound and a short timeout.
A failure, such as being offline or not signed in, is one warning in the report and leaves every finding as it was.
`[worktrees] github = false` turns the lookups off.
The lookup never changes a tier: a branch whose pull request is merged but whose commits are not detected in the default branch stays tier B, because the commit based check is the one that proves the work exists locally.
It adds to the restore text, so such a finding says "merged in PR #18; fetch origin to see it locally".

Tiers:

- Tier A, preselected: the folder exists, `modified`, `untracked`, `loose` and `unpushed` are zero, `merged` is not `no` or `unknown`, and no live lock holds it.
One exception: when `merged` is `rebase` or `squash` against a default branch on a remote, a ref under `refs/remotes/`, commits on no remote do not count, because their changes are proven to be, byte for byte, on that remote; this is the branch GitHub deletes after a squash merge.
The state then says "merged by squash into origin/main; the branch is not on the remote".
Against a local only default branch, commits on no remote always count.
Restore: `git worktree add <path> <branch>`.
- Tier B, never preselected: the same, except `merged` is `no`.
The work is safe on the remote.
- Listed for attention, with the `None` action, never removed by reclaim: anything else.
That is any worktree with `modified`, `untracked` or `loose` above zero, `unpushed` above zero outside the exception above, `merged` or any count `unknown`, or `ignored` files that are not artifacts.
The warning says exactly what: "58 modified and 6 untracked files", "10 commits on no remote", "detached with 2 commits no branch holds", "2 ignored files: .env, local.sqlite".
Under it, the finding carries retrieval hints as text that reclaim never runs: `git -C <path> add -A && git -C <path> commit` or `git -C <path> diff > <branch>.patch` for uncommitted changes, `git -C <path> push -u origin <branch>` for unpushed commits, `git -C <path> branch <name>` for commits on a detached `HEAD`, and the pull request URL or `gh pr create --head <branch>` for an unmerged pushed branch.
It also carries the exact `git -C <repo> worktree remove --force <path>` command, so a user who has read the hints and wants the folder gone copies one line.
- Never shown as a finding: a worktree held by a live lock, which is in use, and the main worktree.
A stale lock, one whose process is gone, is listed for attention with the `git worktree unlock` command.

Stale registrations, entries in `.git/worktrees` whose folder no longer exists, are one tier A finding per repository, "14 stale worktree registrations", with action `GitWorktreePrune`.
They come from `git worktree prune --dry-run --verbose`, not from `git worktree list`, which leaves out a registration whose `gitdir` file is missing although `git worktree prune` removes it.
The finding is offered only when every registration the dry run names points at a folder that does not exist; when one names a folder that still exists, or cannot be read, pruning could orphan a folder that holds work, so the finding is listed for attention.
Apply runs the dry run again and refuses unless every registration it names is in the plan and points at a folder that is gone.
A folder on a volume that is not mounted, under `/Volumes`, `/mnt`, `/media` or `/run/media`, is not gone: it may come back with its drive, and its registration holds its `HEAD`, so such a registration is listed for attention and never pruned.
They free almost nothing but they let `git worktree add` reuse the paths and keep `git worktree list` honest.

Findings are grouped by repository, then ordered by tool: Claude Code, Codex, other tools, manual, then stale registrations and other clones.
The tool is a column, not a heading of its own, so the checklist keeps its three levels.
The repository row shows its origin, default branch, last fetch time and how many worktrees it has in each state.
Two clones of the same origin under the roots are listed for attention under the repository group as "another clone of this origin", never offered, because reclaim cannot know which one the user wants.
The first clone in path order is the group, and the ssh and https forms of an origin compare equal.
A worktree that lies outside the report's roots is listed for attention with the plain `git worktree remove` command, because apply removes nothing outside them.
Findings carry the main repository as `repo`, which groups them.
`here` inside a repository marks linked worktrees in the breakdown with their state words.

Removal is action `GitWorktreeRemove`: `git -C <repo> worktree remove <path>`, never with `--force`.
`git worktree remove` deletes the worktree's own registration, so apply does not run `git worktree prune` after it, unlike the first version of this design: a prune would also remove registrations the plan does not cover, such as one of a worktree on a drive that is not mounted.
Apply only asks git, with `git worktree prune --dry-run --verbose`, what a prune would still remove, and notes it in the log.
`RemovePath` would leave the registration behind, and git's own refusal to remove a dirty worktree is a second guard worth keeping.
Branches are never deleted; the restore text of a merged worktree names the `git branch -d` command for the user.
Codex task folders become one `GitWorktreeRemove` per repository inside, then `RemovePath` on the empty task folder.
That finding has kind `empty-dir`: apply removes it with `os.Root.Remove`, which fails on a folder that is not empty, and refuses it while it holds anything, so a worktree whose removal failed is never removed with its task folder.
It is offered only when every entry of the task folder is a removable worktree, at tier B when any of them is tier B, and it is kept whatever `--min-size` says while a finding inside it is kept.
Report totals count a finding that lies inside another applicable finding once, as part of the outer one, and a folder removed once empty never counts as an outer finding.

### Agent session leftovers

- `claude-sessions`: Claude Code session folders under `~/.claude/projects/<slug>`, or `$CLAUDE_CONFIG_DIR/projects/<slug>`, whose project folder no longer exists.
The slug replaces `/` with `-`, which is lossy, so the scanner reads the `cwd` field of a session file instead of decoding the name.
Claude Code writes it in one of the first few records, not always the first, so the scanner reads up to 200 lines of each session file in name order until one records it.
Tier B once the newest file in the folder is 30 days old, with the warning that Claude Code cannot resume those sessions.
A project on a volume that is not mounted, under `/Volumes`, `/mnt`, `/media` or `/run/media`, may come back with the drive, so its sessions are not offered.
- `codex-plugin-backups`: `$CODEX_HOME/.tmp/plugins-backup-*`, tier A.
- Codex sessions, archived sessions and Claude desktop VM bundles are covered by the app cache scanners.

## Handling long lists

The checklist is a tree, not a flat list, built with bubbletea and bubbles.
Top level nodes are categories.
Under a category, project findings are grouped by project, Docker findings by object kind, and app cache and Downloads findings by scanner; package caches are one row each.
The table, the Markdown summary and the numbered list use the same grouping.
Every node shows its aggregate size, its count and how many of its findings are selected, over the findings the current filters show.
Tier A findings are preselected.
Toggling a group or category toggles its visible tier A and tier B children and never its tier C children, and says how many tier C items it left alone.
Tier C findings are ticked one at a time, and their rows show their warning.
Findings listed for attention only show their size and warning but cannot be ticked, and their group says it is not counted.
Keys: space toggles, enter expands and collapses (right and left also expand and collapse), `/` filters by text, `s` cycles the sort between size, name and oldest first, `t` cycles the tier filter between all, A, B and C, `a` selects every visible tier A finding, `w` writes the plan and exits, `q` quits without writing, and esc clears the text filter.
The text filter matches a finding's name, path, scanner, project and group title, so typing a project path shows all its findings; while any filter is active, every group is shown open.
Categories start open and groups start closed.
The header shows the provenance line, wrapped to the window so its end, which says when a report covers only some folders, stays visible.
Two lines under the list show the full warning, or else the path, of the finding under the cursor, which a narrow window cuts off in its row.
The footer always shows the selected count and total size and the plan path that `w` will write.
Only the rows that fit in the window are rendered, so the checklist stays responsive with thousands of findings.
The plan file is JSON and `select` tells the user where it was written, so editing it by hand is always an option.
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

A `GitWorktreeRemove` action also records `repo`, the main repository, `head`, the commit the worktree was at, and `worktree`, the full state the scanner found, which is the proof that removal is safe.
A `GitWorktreePrune` action records `repo` and the registrations it covers.
Plan validation ties these fields to the action's target, which the id covers: a `GitWorktreeRemove` needs kind `dir`, a commit id as `head`, a recorded state whose path, repository and head are the action's, the exact command `git -C <repo> worktree remove <path>`, a path that neither is nor contains the repository, and a recorded state that allows removal at the action's tier; a `GitWorktreePrune` needs the target `git-worktree-prune:<repo>`, the exact command `git -C <repo> worktree prune` and its registrations.
These git commands are not on the `RunCommand` list, so no other action can run them, and no other action may carry the worktree fields.
Apply refuses a prune when git would prune a registration the plan does not list.

### Removing a worktree

Removing a worktree is the one case where an agent, through `done --remove`, can trigger a deletion without a person reading a checklist, so the proof is explicit and is checked twice: once when the plan is written and again by `apply` immediately before acting.
Every check must pass, and the result of each is written to the log.

1. The path is a linked worktree: its `.git` is a file naming a registration under `<repo>/.git/worktrees/`, the registration points back at it, `git -C <repo> worktree list --porcelain` lists it, and no other registered worktree lies inside it.
A `HEAD` that moved is left to check 4, after the state is computed again, so `done` can report what the worktree holds now.
A directory holding a `.git` directory is never removed, so a main checkout or a project root cannot be, whatever the plan says.
2. The path passes every `RemovePath` check: inside a recorded root, not protected, no symbolic link in it, not a mount point, the same object as at scan time; the walk that measures it finds no other filesystem and no nested repository inside it, a second guard after check 3.
3. The state is recomputed with the same code the scanner uses, and `modified`, `untracked`, `loose`, `unpushed` and `ignored` are all zero, `merged` is `ancestor`, `rebase` or `squash`, and no lock holds it.
`unpushed` may be above zero only when `merged` is `rebase` or `squash`, confirmed exactly, against a default branch under `refs/remotes/`.
`--allow-unmerged`, or a tier B finding ticked in `select`, relaxes `merged` only, and `unpushed` must still be zero.
4. The recomputed `head` equals the recorded one, so a worktree that received commits since the scan is refused.
5. The path does not contain the working directory of the reclaim process.
6. Right before git acts, apply checks once more what git does not guard: the folder is the one it checked, with no symbolic link in its path, `HEAD` has not moved, and nothing modified, untracked, hidden or ignored outside artifact folders appeared; the size comes from the walk of check 2, so no slow walk sits between the checks and git.
Then `git worktree remove` runs without `--force`, so git's own check for untracked and modified files is the last guard.

The log entry of the action lists every check that ran with its result; when one fails it is the last.
Check 2 cannot reuse the `RemovePath` rule that refuses a folder holding a `.git` entry, which every worktree holds; check 1 takes its place.
Writing the plan is `select` from a scanner's tier A or B finding or `done --remove`, which runs checks 1 to 5 before it writes the plan.
A worktree that fails any check is refused with the check that failed and, when work would be lost, the retrieval hints and the `--force` command for the user.
Reclaim never runs that command.

## Safety rules

- No command deletes anything by default.
Only `apply` deletes, only from a plan, only after confirmation.
- `apply` re-verifies every target immediately before acting on it.
- The tool never follows symlinks when deleting.
- The tool never deletes a project root, a `.git` directory, or anything that is not a finding produced by a scanner.
- The tool never deletes a path shorter than four components or any path in a hard coded denylist such as `/`, `/Users`, `/Users/*`, `~/Library`, `~/Documents`, `~/Desktop`.
- Running containers, their images and their volumes are never offered.
- Anything that needs `sudo` is printed as a command and never executed.
- Some findings are print only, whoever asks and whatever flags are given: reclaim shows the exact command and never runs it.
They are anything that needs `sudo`, a worktree that holds work existing nowhere else, that is uncommitted changes, untracked files, ignored files that are not artifacts, or commits on no remote whose changes are not exactly in a default branch on a remote, and a stale worktree lock.
A person or an agent who has read the hints removes those by hand.
- A worktree is removed only after the proof under "Removing a worktree" passes twice, at plan time and immediately before acting, and the main worktree of a repository is never removed.
- `done --remove` is the only path on which an agent triggers a removal, it goes through the same plan, proof, apply and log as everything else, and it removes nothing but the one worktree it was given.
- Every `apply` run writes a log with timestamps, actions and results.

## Configuration

Optional file at `~/.config/reclaim/config.toml`, or `$XDG_CONFIG_HOME/reclaim/config.toml` when that variable holds an absolute path, read through `internal/config` with BurntSushi toml.
`--config` reads another file.
A missing file means the defaults, except that a file named with `--config` must exist.

```toml
roots = ["~/Code", "~/Downloads"]
exclude = ["~/Code/archive"]
min_size = "20MB"
stale = "60d"

[scanners]
disable = ["ollama"]

[worktrees]
github = true
```

Every key is optional, and an unknown key is an error that names it, so a typo never silently changes what a scan covers.
Paths start with `~/` or are absolute, and symbolic links in them are resolved.

- `roots` replaces the default scan roots; a root that does not exist is skipped with a warning.
- `exclude` lists paths the project walk never enters, and drops every finding whose path is one of them, lies under one, or contains one, since removing it would remove the excluded path too, whichever scanner found it.
An exclude that does not exist gets a warning, because it protects nothing.
- `min_size` and `stale` are the defaults for `--min-size` and `--stale`.
- `[scanners] disable` takes scanner names, categories or ecosystems, like `--category`; an unknown one is an error.
Disabled scanners do not run, and `reclaim scanners` marks them disabled.
- `[worktrees] github`, default true, lets the worktree scanners and `done` ask `gh` for pull request state; `false` keeps a machine offline.

Flags override the config file: `--min-size` and `--stale` replace the file's values, paths given to `scan` replace its roots, and `--category` runs exactly the scanners it selects, disabled or not.

## Output formats

The terminal table uses lipgloss for layout, human readable sizes, and colors only when stdout is a terminal.
`--json` is stable and versioned.
`--md` is for pasting into an issue or a document.
It has a heading per category and per group with their subtotals, a table of findings per group, the totals, warnings and notes, and never colors.
Text in table cells is escaped, including pipes in paths.

## Testing

- Unit tests for every scanner using an in-memory or temp directory fixture.
Fixtures build a fake home with projects, artifact folders and caches, then assert on the findings.
- `fsx` size tests cover hard links, symlinks, unreadable directories and cancellation.
- Docker tests run against a real daemon when `RECLAIM_DOCKER_TESTS=1` is set.
They create their own fixtures: a `busybox` image, a stopped container, an unused volume and a small build, all labeled `reclaim.test=1`.
They assert that the scanner finds exactly those fixtures when filtered by the label and that `apply` removes them.
They never touch anything without the label.
The fixture images are built from Dockerfiles with a single `LABEL` instruction on top of `busybox` that sets the test label too, so the build has one step and leaves no unlabeled intermediate images, and the `busybox` base image is never removed.
Build time labels passed through the API are not used, because the classic builder adds a step for them whose intermediate image carries no label.
The build cache prune is covered by unit tests against a fake client only, because a real prune cannot be limited to labeled records.
Without `RECLAIM_DOCKER_TESTS=1` the tests skip, and the scanner and the Docker executors are unit tested against an in-memory fake of the client interface in `internal/dockerx`.
- An end-to-end test runs `scan` on a fixture tree, writes a plan with `--preset safe`, runs `apply --yes`, and asserts that only the expected paths are gone.
- Worktree tests build fixture repositories with real git in temp directories, with an empty git configuration and a fixed identity, skipped when git is missing, one per state: clean merged, clean squash merged, clean rebase merged, clean pushed unmerged, modified, untracked only, ignored `.env` only, detached with loose commits, unpushed on a branch, locked by a dead pid, locked by the test's own pid, stale registration, and no remote.
The remote is a bare repository in the same temp directory, so nothing touches the network.
Parsing tests with the fake runner cover every git output the classifier reads, and the GitHub lookup is tested with a fake `gh`.
- An apply test proves `GitWorktreeRemove` removes only the worktree folder and its registration, leaves every other worktree, every branch and the main checkout untouched, and refuses a worktree that was modified, received a commit or lost its remote after the plan was written.
A prune test proves stale registrations go and live ones stay.
A `done` test proves each exit code, that `--remove` removes a tier A worktree and nothing else, that it refuses tier B without `--allow-unmerged`, and that no flag removes a worktree with work that exists nowhere else.
A second one scans a whole fake machine, with projects, package and app caches, installed apps and a Downloads folder, applies `--preset aggressive`, and asserts that only the tier A and B paths are gone while the archive with an extracted copy and the attention file remain.
- The checklist is tested by feeding key messages to its `Update` method and asserting on the selection and on `View`.
- Shell completion is tested through cobra's hidden completion command, and `reclaim version` through a function that takes the stamped version and the build info as arguments.
- `TestScreenshots` runs `scan`, `here`, `worktrees` and the checklist in color over a fixture machine with fake tools, scripted git, `ps` and `gh` answers for a repository with worktrees, and a fake Docker daemon, at a fixed time, and compares the output with the files in `cmd/reclaim/testdata/screenshots`.
`scripts/screenshots.sh` rewrites those files and renders them with freeze as the SVGs in `docs/screenshots` that the README shows, so the screenshots never come from a real machine and cannot drift from the code.
- CI runs `go test -race` on macOS and Linux and `golangci-lint`, and a separate job on `ubuntu-latest` runs the Docker integration test against the runner's daemon with `RECLAIM_DOCKER_TESTS=1`, failing if the test skips.

## Releasing

Releases are cut by pushing a tag such as `v0.1.0`.
The release workflow runs every CI job and then GoReleaser, which builds `cmd/reclaim` for darwin and linux on arm64 and amd64 with CGO disabled, `-trimpath` and the version stamped, publishes tar.gz archives with `LICENSE` and `README.md`, a checksums file and release notes from the commit subjects on the GitHub release, and updates `Formula/reclaim.rb` in the `mandloideep/homebrew-tap` repository.
The formula installs the bash, zsh and fish completions from the binary and tests `reclaim version`.
There is no `CHANGELOG.md`; the release notes are the change log.
GoReleaser and freeze run through `go run` and are not dependencies in `go.mod`.
[RELEASING.md](RELEASING.md) has the steps, the token the tap needs and how to verify a release.
Without the tap token the workflow still publishes the GitHub release and skips the formula.

### Installing

Three install paths, in the README in this order: `brew install mandloideep/tap/reclaim`, `scripts/install.sh` and `go install`.
`scripts/install.sh` is for Linux machines and CI runners without Homebrew: it detects the operating system and architecture, downloads the matching archive of the latest release, or of the tag given with `--version`, checks it against `checksums.txt`, installs `reclaim` into `~/.local/bin` or the folder given with `--bin-dir`, and prints the completion command for the shell it finds.
It reads the archive's name from `checksums.txt`, so it needs no version to name it, and it downloads from `releases/latest/download` when no tag is given.
It is POSIX sh, needs only `curl` or `wget` and `tar`, never uses `sudo`, and fails with a clear message when the checksum does not match.
The README gives the one line `curl -fsSL https://raw.githubusercontent.com/mandloideep/reclaim/main/scripts/install.sh | sh`.
CI tests the script against a snapshot build served from a local folder through `--base-url`, on Linux and macOS, checks `reclaim version` of what it installed, checks that an archive that does not match its checksum is refused, and runs `shellcheck` on it.

## Phases

Phase 1 delivered `scan`, `here`, `select --preset`, `apply`, the project scanners, the package cache scanners, the Docker scanner, JSON and table output, and the test suite.
Phase 2 delivered the interactive checklist, the app cache scanners, the Downloads scanner, Markdown output, the config file, report provenance, Docker grouping, the location check before clean commands and the Docker integration test in CI.
Phase 3 delivered GoReleaser and the release workflow, the Homebrew tap, shell completion values, the README with screenshots rendered from a fixture, `docs/RELEASING.md`, Xcode's custom DerivedData location, the Linux system wide Ollama models, a blank tier column for attention findings and the release tag in `reclaim version`.
Phase 4 delivered `internal/worktree` with the classification, the merge checks and the GitHub lookup, the `git-worktrees`, `codex-worktrees` and `worktree-registrations` scanners, the `GitWorktreeRemove` and `GitWorktreePrune` actions with plan validation and the six checks in apply, the `worktrees`, `done` and `skill` commands, the agent skill and `docs/AGENTS.md`, the `claude-sessions` and `codex-plugin-backups` scanners, `scripts/install.sh` with its CI job, worktrees in `here`, and the `worktrees` screenshot.

Each phase is a pull request or a short series of pull requests.
Every pull request passes CI and includes tests for what it adds.

## Acceptance

On the reference machine, `reclaim scan` completes in under two minutes over 165 GB of projects and reports, within 5 percent, the following that were measured by hand: 57 GB of project artifacts, about 109 GB of reclaimable Docker data and about 40 GB of package caches.
`reclaim here` in a project folder shows the breakdown within one second for a folder up to 20 GB.
`reclaim apply` on the end-to-end fixture removes exactly the planned paths and nothing else.
`reclaim worktrees` on the reference machine classifies its two dozen linked worktrees in under five seconds without the GitHub lookup and under fifteen with it, reports the three squash merged Claude Code worktrees of this repository as tier A, and lists the Codex `webtools` worktree with its modified files for attention only.
