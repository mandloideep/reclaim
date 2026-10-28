# reclaim

`reclaim` finds the disk space a developer machine can get back: build artifacts, dependency folders, git worktrees that coding agents left behind, package manager caches, Docker leftovers, app caches and forgotten downloads.
It shows everything with its size and says how each item comes back, and it removes only what you pick and confirm.

Run `reclaim` with no arguments in a terminal and one screen walks you through it: choose what to look at, watch the scan, then review what it found, with everything reclaim knows about the item under the cursor and what it will check before removing it.

![The reclaim review screen: findings grouped by category and project with tier A ticked, a repository's worktrees with their merge state and pull requests, and the details of the selected build folder on the right](docs/screenshots/ui-review.svg)

Status: phases 1 to 4 are complete, and phase 5, the interactive screen, is in progress.
`scan`, `here`, `select`, `apply`, `worktrees` and `done` work on macOS and Linux, with table, JSON and Markdown output, an interactive checklist, a config file, shell completion, release binaries, a Homebrew formula, an install script and a skill for coding agents.
The screen scans, reviews, saves the plan, checks it and, once you type `yes`, removes it, all with the same code as the commands below; the ignore list and the folder tab come next.
Windows is not supported.
[docs/DESIGN.md](docs/DESIGN.md) is the specification.

## Install

With Homebrew on macOS or Linux:

```
brew install mandloideep/tap/reclaim
```

The formula installs shell completions for bash, zsh and fish as well.

Without Homebrew, for example on a Linux server or a CI runner, the install script downloads the latest release for your system, checks it against `checksums.txt` and puts `reclaim` in `~/.local/bin`, without `sudo`:

```
curl -fsSL https://raw.githubusercontent.com/mandloideep/reclaim/main/scripts/install.sh | sh
```

`--version v0.2.0` installs another release and `--bin-dir ~/bin` another folder; pass them after `sh -s --`.
It needs `curl` or `wget` and `tar`, and prints how to turn on shell completion.
The archives for macOS and Linux on arm64 and amd64 are also on the [releases page](https://github.com/mandloideep/reclaim/releases).

With Go 1.25 or newer:

```
go install github.com/mandloideep/reclaim/cmd/reclaim@latest
```

`reclaim version` prints the installed version.

## Quick start

```
reclaim                         # in a terminal: scan, review, confirm and remove on one screen
```

The same steps one command at a time, which also work in scripts and pipes:

```
reclaim scan                    # report what can be reclaimed; changes nothing
reclaim select                  # tick findings in a checklist; writes plan.json
reclaim apply plan.json         # shows the plan, asks you to type yes, then removes it
```

Prefer no screen at all?
`reclaim select --preset safe` writes a plan with every tier A finding, and `reclaim apply plan.json --yes` runs it without asking.

To see what fills the folder you are in:

```
reclaim here
```

## Tiers

Every finding has a tier that says what removing it costs.

- Tier A rebuilds itself on the next build or install: `node_modules`, `.venv`, Rust `target`, `.next`, Go build caches, dangling Docker images, Xcode DerivedData, app caches.
These are selected for you in the checklist.
- Tier B can be downloaded or rebuilt again, but that costs time or bandwidth: package manager stores such as the npm cache or the Go module cache, Ollama models, Playwright browsers, unused Docker images and volumes, installers of apps you already installed.
They are never preselected.
- Tier C may be data that cannot be restored: a `dist` folder with no build config next to it, a Docker volume attached to a stopped container, an archive in Downloads next to its extracted copy.
They come with a warning, a preset never selects them, a group toggle skips them, and you tick each one yourself.

Git worktrees are tier A when every change is committed, every commit is pushed and the work is merged into the default branch, including squash and rebase merges, and tier B when they are pushed but not merged.
A squash or rebase merge counts only when the merged diff is exactly the worktree's, so a branch the remote deleted after merging it is still tier A.
A worktree that holds work that exists nowhere else is never offered: it is listed with the commands that save its work.

Large files in Downloads that nobody touched for 90 days are listed under "Attention, never removed".
They have no tier letter, are never counted as reclaimable and cannot be selected: reclaim shows them so you can decide.

## Safety

- Only `apply` removes anything, only what the plan lists, and only after you type `yes` or pass `--yes`.
`scan`, `here` and `select` never change your files.
- Right before each removal, `apply` checks the target again: it must still exist, be the same kind of thing the scan found, lie inside a folder the scan covered, and contain no symbolic link in its path.
It never removes a project root, a `.git` folder, a folder that holds a project manifest, your home folder, protected folders such as `~/Documents`, or anything shorter than four path components.
- It never follows symbolic links and never crosses into another file system.
- Running Docker containers, their images and their volumes are never offered, and Docker removals never force.
- Package caches are cleaned with the tool's own command when the tool reported where its cache is, and apply asks the tool again first.
Only commands from a fixed list can run, so an edited plan cannot run anything else.
- reclaim never runs with elevated rights.
Anything that needs `sudo` is printed as a command for you to run.
- A plan whose scan is more than 24 hours old is refused unless you pass `--stale-ok`.
- A git worktree is removed only with `git worktree remove`, never with `--force`, after `apply` checks again, right before acting, that it is a linked worktree at the commit the scan saw and that nothing in it is uncommitted, untracked, ignored outside build folders, on no branch, or on no remote without being exactly merged into the remote default branch.
A worktree whose work exists nowhere else is never removed, whoever asks and whatever flags are given; reclaim prints the `--force` command for you instead.
Branches are never deleted and nothing is ever pushed or fetched.
- Every `apply` run writes a log of what it did, the checks of every action and how much it freed, and `apply --dry-run` runs every check without removing anything.
- The interactive screen removes nothing itself: it saves a plan with the code `select` uses and applies it with the code `apply` uses, only after you type `yes`; `P` on every screen lists what reclaim never removes.

## Commands

Every command accepts `--config path` to read another config file and `--verbose` for debug logs.

### ui

```
reclaim ui [path...]
reclaim
```

Opens the interactive screen; `reclaim` alone does the same when standard input and output are a terminal, and prints the help otherwise, so `reclaim | cat` keeps working.
The Start screen chooses what to look at: the whole machine, one folder, which is the current one unless you type another with `p`, or the git worktrees alone.
It also ticks the scanner categories, the minimum size (`m`), the stale filter (`s`) and whether GitHub is asked about pull requests (`g`), and `o` opens the last report without scanning.

![The reclaim start screen with the whole machine selected, every scanner category ticked and the last report named](docs/screenshots/ui-start.svg)

While the scan runs, one row per category says what it is doing and what it has found so far; `esc` stops it and reviews what was found.

![The reclaim scanning screen with project artifacts done, the worktree scanner asking GitHub about branches and Docker inspecting stopped containers](docs/screenshots/ui-scanning.svg)

The Review screen is the checklist of `select` with a details pane: the full path, the tier in words, the restore command, the root and project apply checks it against, what apply checks before removing it and the exact action, and for a worktree everything the worktree scanner found, with the commands that save its work.
`n` clears the selection, `i` hides the details and `J` and `K` scroll them, `w` writes the plan to `--out` and quits, and `c` saves the plan in the reclaim cache folder and continues to Confirm.

The Confirm screen lists the plan the way `reclaim apply` does, says what is checked before every removal, and asks you to type `yes`.
`d` runs every check of every action without removing anything and marks each row ok, gone, manual or refused, with the checks of a worktree's proof by name, so a refusal shows before you confirm.

![The reclaim confirm screen after d: every action of the plan marked ok, the checks of a worktree listed by name, and the prompt to type yes](docs/screenshots/ui-confirm.svg)

The Removing screen shows each action as it finishes, the one running with what it should free, the freed total against the plan and the free space of the disk; `ctrl+c` stops after the current action.
The Done screen counts what happened, prints the commands that need `sudo` in full for you to run, repeats every failure with its reason, and names the log and the plan; `r` scans again.

![The reclaim removing screen with four actions done, one running and eleven waiting](docs/screenshots/ui-removing.svg)
`P` on every screen lists what reclaim never removes, rendered from the tables its checks use, and `?` lists the keys.
The screen itself never removes anything.

It takes the flags of `scan`, `--min-size`, `--category`, `--stale`, `--depth`, `--all` and `--docker-label`, which prefill the Start screen, and `--out plan.json`.

### scan

```
reclaim scan [path...]
```

Reports everything reclaimable, grouped by category and then by project, Docker object kind or scanner, largest first.
With no path it walks the roots from the config file, or `~/Code`, `~/Developer`, `~/Projects`, `~/src`, `~/work` and `~/Downloads`, whichever exist, and checks package caches, Docker, app caches and Downloads.
With paths it looks only for project artifacts and git worktrees under them.

![reclaim scan listing project artifacts, package caches, Docker objects, app caches and downloads by size](docs/screenshots/scan.svg)

- `--json` prints the full report as JSON, `--md` prints a Markdown summary for an issue or a document.
- `--min-size 50MB` hides smaller findings; the default is 10 MB and `0` shows everything.
- `--tier A,B` shows only those tiers.
- `--category docker,node` runs only those categories, ecosystems or scanners; `reclaim scanners` lists them.
- `--stale 90d` keeps only project findings of projects without activity for that long.
- `--depth 2` limits how deep the project walk goes below each root.
- `--all` with paths also checks package caches, Docker, app caches and Downloads.
- `--docker-label key=value` limits Docker findings to objects with that label.
- `--out report.json` also saves the report for `select`.

Every scan also saves its report where `select` finds it.

### here

```
reclaim here [path]
```

Shows what takes space in a folder, like `du` one level deep, marks the entries a scanner recognizes with their tier, and lists every reclaimable item below the folder with its full path.

![reclaim here showing the entries of a project folder by size, with node_modules, .next and dist marked tier A](docs/screenshots/here.svg)

- `--depth 2` shows more levels of the breakdown.
- `--json`, `--md` and `--out report.json` work as for `scan`, so `reclaim here` followed by `reclaim select` cleans up just this folder.

### select

```
reclaim select
```

Reads the last report, or the one given with `--report report.json`, and writes a plan.
In a terminal it opens a checklist: categories, then projects or groups, then findings, each with its size and how many items are selected.

![The reclaim select checklist with project groups expanded, tier A findings ticked and a tier B package cache selected](docs/screenshots/select.svg)

Keys: space toggles, enter or the arrows open and close groups, `/` filters by name, path, scanner or project, `s` changes the sort, `t` cycles the tier filter, `a` selects every visible tier A finding, `w` writes the plan and `q` quits without writing.

- `--preset safe` selects every tier A finding without the checklist, `--preset aggressive` adds tier B.
- `--out plan.json` names the plan file.
- When standard input or output is not a terminal, `select` prints a numbered list and reads numbers such as `1,4-9,12`; tier C items must be listed one by one.

The plan is a JSON file you can read and edit before applying it.

### apply

```
reclaim apply plan.json
```

Prints every action with its size, asks you to type `yes`, then carries the actions out in order and prints the space freed and the log path.

- `--yes` skips the question, for scripts.
- `--keep-going` continues after a failed action instead of stopping.
- `--stale-ok` accepts a plan whose scan is more than 24 hours old.
- `--log apply.log` appends the log to that file instead of a new one in the reclaim cache folder.
- `--dry-run` runs every check of every action and removes nothing: it prints what reclaim never removes, then one line per action, checked, gone, manual or refused with the reason, needs no `yes` and writes a log marked as a dry run.

Apply also refuses any target that is, lies inside or holds an `exclude` entry of the config file, whatever the plan says.

### worktrees

```
reclaim worktrees [path...]
```

Shows every linked git worktree of the repositories under the paths or the configured roots, and the ones Codex made, one table per repository: the tool that made it, its branch, uncommitted files, unpushed commits, whether it is merged, its pull request, its lock and its size.
Under a worktree that holds work it prints the commands that save it and the `git worktree remove --force` command, which reclaim never runs.

![reclaim worktrees showing a merged Claude Code worktree as tier A, an unmerged one as tier B, one in use and a Codex worktree with uncommitted changes](docs/screenshots/worktrees.svg)

Merge state is as of the repository's last fetch; reclaim never fetches.
Pull requests come from `gh` when it is installed and signed in; `[worktrees] github = false` in the config file turns that off.
`--json`, `--md` and `--out report.json` work as for `scan`, so `reclaim worktrees` followed by `reclaim select` cleans up worktrees alone.

### done

```
reclaim done [path]
```

The one command an agent, or a person, runs at the end of a task in a worktree.
It prints the state of the worktree at `path`, default the current folder, in plain words, such as "committed, pushed, merged in PR #3, 158 MB", or as JSON with `--json`, and exits:

- `0` committed, pushed and merged;
- `1` pushed but not merged;
- `2` holding work that exists nowhere else, nested repositories and submodules included, with the commands that save it;
- `3` not a linked worktree, or reclaim could not tell whether its work exists elsewhere because a git command failed.

`--remove` removes a worktree that exits `0` through a plan and `apply`, like everything else, with the plan and log in the reclaim cache folder.
`--remove --allow-unmerged` also removes one that exits `1`.
Nothing removes a worktree that exits `2`.
Run it from the main checkout: it never removes the folder it runs in.

### skill

```
mkdir -p ~/.claude/skills/reclaim && reclaim skill > ~/.claude/skills/reclaim/SKILL.md
```

Prints the agent skill described under "For agents".

### scanners and version

`reclaim scanners` lists every scanner with its category, ecosystem, what it looks for and whether the config file disables it.
`reclaim version` prints the version, the Go version and the platform.

## Config file

reclaim reads its settings from the first of these that applies:

1. the file named by `--config`, which must exist;
2. `$XDG_CONFIG_HOME/reclaim/config.toml`, when `XDG_CONFIG_HOME` is an absolute path;
3. `~/.config/reclaim/config.toml`, on macOS too.

A missing default file means the built in defaults.
Every key is optional, flags override the file, and an unknown key is an error that names it.

```toml
roots = ["~/Code", "~/Downloads"]   # replaces the default scan roots
exclude = ["~/Code/archive"]        # never walked, never offered, refused by apply
min_size = "20MB"                   # default for --min-size
stale = "60d"                       # default for --stale

[scanners]
disable = ["ollama"]                # names, categories or ecosystems, as in reclaim scanners

[worktrees]
github = true                       # ask gh for pull requests; false keeps reclaim offline
```

## For agents

Claude Code, Codex and other coding agents work in git worktrees and leave them behind.
`reclaim skill` prints a skill that tells an agent, when its task is done, to run `reclaim done --json` from the main checkout, to push or open a pull request when the exit code says so, and to run `reclaim done --remove` only once the work is merged.
The agent never removes a worktree itself and never forces anything.

For Claude Code:

```
mkdir -p ~/.claude/skills/reclaim && reclaim skill > ~/.claude/skills/reclaim/SKILL.md
```

For Codex or any agent that reads `AGENTS.md` or `CLAUDE.md`, paste the section in [docs/AGENTS.md](docs/AGENTS.md).

## Shell completion

Completion offers commands, flags and their values: tiers for `--tier`, categories, ecosystems and scanners for `--category`, presets for `--preset`, TOML files for `--config`, JSON files for reports and plans, and folders for `scan` and `here`.
The Homebrew formula sets it up for you.
Otherwise load it from the binary:

zsh, with `compinit` already in your `~/.zshrc`:

```
reclaim completion zsh > "${fpath[1]}/_reclaim"
```

bash, with the bash-completion package installed:

```
reclaim completion bash > "$(brew --prefix)/etc/bash_completion.d/reclaim"   # macOS with Homebrew
reclaim completion bash | sudo tee /etc/bash_completion.d/reclaim >/dev/null  # Linux
```

fish:

```
reclaim completion fish > ~/.config/fish/completions/reclaim.fish
```

Open a new shell afterwards.
`reclaim completion <shell> --help` shows more options, such as loading it for the current session only.

## Adding a scanner

A scanner is a Go type with a name, a category, an ecosystem, a one line description and a `Scan` method:

```go
type Scanner interface {
    Name() string
    Category() finding.Category
    Ecosystem() string
    Description() string
    Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error)
}
```

`Scan` must not change anything and must stop when `ctx` is canceled.
Everything it needs, such as the home folder, the size walker, a runner for asking tools where their caches are and the Docker client, comes from `scan.Env`, so tests can pass fakes.
A scanner with a long phase calls `env.Step("listing images")`, which the interactive screen shows on the scanner's row while it runs.
Each finding says what `apply` does with it, its tier, how to get it back and, for tier C, a warning.

Scanners live in family packages under `internal/scanners`: `projects` for artifact folders inside projects, `worktrees` for git worktrees, `caches` for package managers, `docker`, `appcache` and `downloads`.
A new project artifact is usually one more entry in the rules of `internal/scanners/projects/rules.go`, and a new package cache one more entry in `internal/scanners/caches/specs.go`.
A scanner of a new kind is added to the explicit list in `internal/scanners/all.go`.

Every scanner needs tests over a fixture tree in a temp directory, and anything a new scanner lets `apply` remove needs a test that proves it removes only that target.
Removal commands must be added to the allowlist in `internal/plan/commands.go`.
Read [docs/DESIGN.md](docs/DESIGN.md) and [CLAUDE.md](CLAUDE.md) before you start.

## Development

```
go test -race ./...
golangci-lint run
```

The screenshots above are rendered from a fixture machine, never from a real one; `scripts/screenshots.sh` regenerates them.
[docs/RELEASING.md](docs/RELEASING.md) explains how releases are cut.

## License

MIT, see [LICENSE](LICENSE).
