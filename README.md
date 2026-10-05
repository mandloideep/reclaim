# reclaim

Find the disk space your developer machine is wasting and take back only what you choose.

`reclaim` scans for build artifacts, dependency folders, package manager caches, Docker leftovers and application caches.
It shows everything with sizes, grouped by category and project, and tells you how each item would be restored.
Nothing is deleted until you pick it and confirm.

```
reclaim scan                    # whole machine report, read only
reclaim here                    # what takes space in this folder, and what can go
reclaim select --preset safe    # writes plan.json with every tier A finding
reclaim apply plan.json         # removes exactly what the plan says, after confirmation
reclaim scanners                # what each scanner looks for
```

Status: phase 1 is in place: `scan`, `here`, `select` with presets or a numbered list, `apply`, and the project, package cache and Docker scanners.
The interactive checklist, app cache and Downloads scanners, Markdown output and the config file come in phase 2.
See [docs/DESIGN.md](docs/DESIGN.md) for the specification.

Findings come in three tiers.
Tier A rebuilds itself on the next build or install, tier B costs a download or a rebuild, and tier C may be data that cannot be restored.
Presets never select tier C.

## Install

Coming soon via Homebrew.
For now:

```
go install github.com/mandloideep/reclaim/cmd/reclaim@latest
```

## License

MIT
