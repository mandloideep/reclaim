# reclaim

Find the disk space your developer machine is wasting and take back only what you choose.

`reclaim` scans for build artifacts, dependency folders, package manager caches, Docker leftovers, application caches and forgotten downloads.
It shows everything with sizes, grouped by category and project, and tells you how each item would be restored.
Nothing is deleted until you pick it and confirm.

```
reclaim scan                    # whole machine report, read only
reclaim here                    # what takes space in this folder, and what can go
reclaim select                  # pick findings in a checklist, writes plan.json
reclaim select --preset safe    # writes plan.json with every tier A finding
reclaim apply plan.json         # removes exactly what the plan says, after confirmation
reclaim scanners                # what each scanner looks for
```

Status: phases 1 and 2 are in place: `scan` and `here` with table, JSON and Markdown output, `select` with an interactive checklist, presets or a numbered list, `apply`, the config file, and scanners for project artifacts, package caches, Docker, app caches and Downloads.
Packaging, a Homebrew tap and shell completion come in phase 3.
See [docs/DESIGN.md](docs/DESIGN.md) for the specification.

Findings come in three tiers.
Tier A rebuilds itself on the next build or install, tier B costs a download or a rebuild, and tier C may be data that cannot be restored.
Presets never select tier C.

## Config

`reclaim` reads `~/.config/reclaim/config.toml` when it exists.
Every key is optional, flags override the file, and an unknown key is an error.

```toml
roots = ["~/Code", "~/Downloads"]   # replaces the default roots
exclude = ["~/Code/archive"]        # never walked, never offered
min_size = "20MB"                   # default for --min-size
stale = "60d"                       # default for --stale

[scanners]
disable = ["ollama"]                # names, categories or ecosystems; see reclaim scanners
```

## Install

Coming soon via Homebrew.
For now:

```
go install github.com/mandloideep/reclaim/cmd/reclaim@latest
```

## License

MIT
