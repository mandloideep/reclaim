# Conventions for agents working on reclaim

Read `docs/DESIGN.md` before changing anything.
It is the specification.
If you need to deviate from it, update it in the same pull request and say why in the pull request body.

## Rules

- This tool deletes files.
Every code path that removes anything must be covered by a test that proves it removes only the intended target.
- Never run destructive commands against the machine you are working on.
Tests create their own fixtures in temp directories and, for Docker, label everything they create with `reclaim.test=1`.
- Never use the em dash character in code, comments or docs.
Use a plain hyphen.
- In Markdown files, put each full sentence on its own line.
- Do not add co-author trailers, `Claude-Session:` lines, session URLs or any other attribution to commits or pull request bodies.
- Do not edit `CHANGELOG.md` by hand.
- Prefer quality, simplicity and long term maintainability over development speed.

## Go

- Go 1.25 or newer, standard library first.
Approved dependencies: `github.com/spf13/cobra`, `github.com/docker/docker/client`, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/bubbles`, `github.com/BurntSushi/toml`, `github.com/stretchr/testify`.
Ask before adding anything else by explaining the need in the pull request.
- `gofmt` and `golangci-lint` clean using the repo config.
- Errors are wrapped with `%w` and carry the path or target they relate to.
- Every exported type and function has a doc comment.
- Table driven tests with `testify/require`.
- Context is the first parameter and is honored in every loop that touches the filesystem or the network.
- No global mutable state. Scanners receive everything through `scan.Env`.

## Workflow

- Work on a branch named `phase-N/<topic>`.
- Open a pull request against `main` for each phase or coherent slice.
- CI must pass: build, vet, race tests on macOS and Linux, lint.
- Commit messages: imperative subject under 72 characters, body explains why.
