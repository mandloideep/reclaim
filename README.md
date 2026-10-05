# reclaim

Find the disk space your developer machine is wasting and take back only what you choose.

`reclaim` scans for build artifacts, dependency folders, package manager caches, Docker leftovers and application caches.
It shows everything with sizes, grouped by category and project, and tells you how each item would be restored.
Nothing is deleted until you pick it and confirm.

```
reclaim scan              # whole machine report, read only
reclaim here              # what takes space in this folder, and what can go
reclaim select            # interactive checklist, writes plan.json
reclaim apply plan.json   # removes exactly what the plan says, after confirmation
```

Status: under construction.
See [docs/DESIGN.md](docs/DESIGN.md) for the specification.

## Install

Coming soon via Homebrew.
For now:

```
go install github.com/mandloideep/reclaim/cmd/reclaim@latest
```

## License

MIT
