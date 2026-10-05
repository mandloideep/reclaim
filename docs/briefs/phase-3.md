# Phase 3 brief

This is the brief handed to the implementation worker for phase 3.
It follows the shape of the phase 1 and phase 2 briefs.
Phases 1 and 2 are merged in pull requests #1 and #2.

## Setup

Read `docs/DESIGN.md` and `CLAUDE.md` in full before writing code.
Follow them exactly.
If you must deviate, update DESIGN.md in the same change and explain why in the pull request body.
Read the bodies of pull requests #1 and #2 as well; they record the judgment calls earlier phases made.

## Deliverables

### Planned for phase 3 in DESIGN.md

1. GoReleaser.
Add `.goreleaser.yaml` building `cmd/reclaim` for darwin and linux on arm64 and amd64, with CGO disabled, `-trimpath`, and `-X main.version={{.Version}}` so `reclaim version` prints the release tag.
Produce tar.gz archives with the binary, LICENSE and README, a checksums file, and release notes generated from commit subjects grouped by prefix, excluding merge and docs-only commits.
Do not create a `CHANGELOG.md`; release notes live on the GitHub release.
Add `.github/workflows/release.yml` that runs GoReleaser on tags matching `v*` after the CI jobs pass, with `contents: write` permission and nothing broader.
Run GoReleaser locally with `go run github.com/goreleaser/goreleaser/v2@latest check` and `... release --snapshot --clean`; do not install it on the machine.
`dist/` is already ignored.
2. Homebrew tap.
Create the public repository `mandloideep/homebrew-tap` with `gh repo create` if it does not exist, containing only a README that says what it is and how to tap it.
Add a `brews` entry to the GoReleaser config that publishes `Formula/reclaim.rb` to that repository with a description, homepage, license, `reclaim version` as the formula test, and shell completions installed from the binary.
The release workflow needs a token that can push to the tap, read from a repository secret named `HOMEBREW_TAP_GITHUB_TOKEN`.
You cannot create that token.
Document in `docs/RELEASING.md` exactly which token to create, with which permission on which repository, and where to add it.
Do not tag or publish a release; the owner cuts `v0.1.0` after adding the secret.
3. Shell completion.
Cobra's `completion` command is already there.
Register completions for `--tier`, `--category`, `--preset` and `--config`, and for the `select` and `apply` file arguments, so tab completion offers real values.
Document installation for zsh, bash and fish in the README, and make the Homebrew formula install completions automatically.
4. README with screenshots.
Rewrite the README for someone who has never seen the tool: what it does in two sentences, install (Homebrew, release binaries, `go install`), a quick start, the three tiers, the safety rules in plain words, every command with its main flags, the config file, how to add a scanner, and the license.
Screenshots are SVGs under `docs/screenshots/` rendered from a deterministic fixture tree, never from the reference machine, so no personal paths or hostnames appear.
Use `go run github.com/charmbracelet/freeze@latest` to render; do not install it.
Cover `scan`, `here` and the checklist.
For the checklist, render the model's `View` at a fixed size from a small generator program, since a live terminal cannot be captured by freeze.
Commit the generator so the images can be regenerated with one documented command, and make it a test or a CI step that the generator still runs.
5. `docs/RELEASING.md`: how to cut a release, what the workflow does, the required secret, and how to verify the formula.

### Gaps and debt found while validating phase 2

6. Attention findings show a tier letter.
Items under "Attention, never removed" print `C` in the table, the Markdown output and the numbered list even though they carry no action.
Print a blank tier column for them in all three, and make the checklist show them with a marker that says they cannot be selected.
7. Xcode's custom DerivedData location.
Read `IDECustomDerivedDataLocation` from the Xcode defaults through the command runner, scan that folder when set and the default otherwise, and test both paths with a fake runner.
8. Ollama system models on Linux.
Scan the system wide service directory as well as the user's folder, with the same per model `ollama rm` action; mark findings that need root with `NeedsSudo` and print the command instead of running it.
9. The global `--config` flag was added in phase 2.
Confirm it is described in DESIGN.md and the README, and that `reclaim --help` explains the precedence between the flag, the environment and the default path.
10. Versions.
`reclaim version` on a `go install` build prints the pseudo version from build info; keep that, and make sure a GoReleaser build prints the plain tag.
Add a test for `buildVersion` covering both paths.

### Documentation

11. Update DESIGN.md: move the phase 3 items from the phases list into the body as delivered, and add a short "Releasing" section pointing at `docs/RELEASING.md`.
12. Keep the README status line accurate.

## Hard rules

- Never run any deletion, prune, cleanup or install command against the machine you are on.
`brew install`, `brew tap`, `brew audit` and `brew style` are install or network commands against the real Homebrew; do not run them.
Validate the formula by reading it and by GoReleaser's snapshot output.
- Never tag, push a tag, or create a GitHub release.
- No em dash characters anywhere.
- Markdown: one sentence per line.
- Do not add co-author trailers, `Claude-Session:` lines, session URLs or any other attribution to commits or pull request bodies.
- Only the dependencies allowed in CLAUDE.md as module dependencies; GoReleaser and freeze run through `go run` and are not added to `go.mod`.
- Every code path that removes anything has a test proving it removes only the target.
- `gofmt`, `go vet`, `go test -race ./...` and `golangci-lint run` must all pass before the pull request is opened.

## Workflow

- Branch `phase-3/release-and-docs`, coherent commits with imperative subjects under 72 characters.
- One pull request against `main` titled "Phase 3: GoReleaser, Homebrew tap, shell completion and README".
- The pull request body lists what was built, what was tested and how, anything deferred, and any DESIGN.md deviations.
- After the first push, have an independent review of the diff as earlier phases did, fix what it finds, and summarize it in the pull request body.
- If CI fails after the pull request opens, fix it and push until it passes.

## Validation on the reference machine

Run these read only and report the results in the pull request body:

- `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean` succeeds and `dist/` holds all four binaries, archives and the checksums file.
- The darwin arm64 snapshot binary prints the snapshot version from `reclaim version` and runs `reclaim scan ~/Code/personal/reclaim` correctly.
- The generated formula in `dist/` has the right URLs, checksums, test block and completion lines.
- `reclaim completion zsh` output sources without error in a fresh `zsh -f` shell, and completing `reclaim scan --tier ` offers `A`, `B` and `C`.
- The screenshots render from the fixture and contain no path under `/Users`.
- A full `reclaim scan` still finishes in under two minutes with totals within 5 percent of phase 2, and attention items show no tier letter.

## Final report

Report the pull request URL, CI status, the tap repository URL, a short list of what was built, test counts, anything deferred or deviated on, the exact secret the owner must add, and any judgment calls a reviewer should look at.
Do not include file dumps.
