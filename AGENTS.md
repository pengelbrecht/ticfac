# Working in ticfac (for any agent: Claude, pi, Codex, …)

## How much to run locally: targeted tests + `make gate`, then merge

Run the tests you touched or added (`go test -run '<names>' -timeout 20m ./<pkg>/`)
and `make gate`. That is the bar to merge: PR CI is advisory, not a required
check, so you do not wait for it. Don't run the full `internal/reconcile`
suite locally as a matter of course: it takes 20-30 minutes and this host is
shared with live runs and other agents. Run heavy local commands at low
priority (GOTEST_PARALLEL=4, GOFLAGS=-p=2).

## What CI runs, and where it must be green

- **Pull requests: affected-only, advisory.** CI's `plan` job runs the Go
  packages the diff can change the verdict of (the changed packages plus every
  package whose tests depend on one), skips the Go jobs when no Go package is
  affected and `typescript` when nothing under cloudflare/ or contracts/
  changed. A diff touching go.mod/go.sum, the Makefile, internal/shorttest,
  .github/ or an unowned file runs everything. Useful when you want a second
  opinion; not a merge gate.
- **main and epic/\*\* pushes: the full suite, sharded.** internal/reconcile
  in three `go test` jobs, every other package in a fourth, plus race, lint,
  contracts and typescript. Main's run is the deploy gate.

CI must be green at three junctures, and only these:

1. **main, before a deploy.** Automatic: deploy-factory runs only on a green
   CI run on main.
2. **Before starting a real run on new code.** Check main's CI for the commit
   the run will use.
3. **Before an epic's PR is marked ready.** The close-out waits for CI on the
   epic branch's head.

**A red main is fixed forward immediately**, by whoever sees it first: it
blocks every deploy behind it. Don't revert-and-wait unless the fix is not
obvious.

## Before you push: run `make gate`, not just your package

`make gate` is gofmt, `go vet ./...` and the short suite across the whole
repository: exactly what a ticfac run's per-tick gate runs. A package-scoped
`go test ./internal/reconcile/` is not enough. Some tests guard the repository
rather than a package, and they only run in a whole-repo pass. The one that
catches people is `internal/shorttest`: every end-to-end test must call
`shorttest.EndToEnd(t)`, or carry a `short:` doc-comment line saying why the
gate should pay for it (see an existing test for the form). PR #71 failed CI
on exactly this after its package tests had passed. With PR CI advisory,
`make gate` is the only whole-repo check before main: skipping it is how main
goes red.

The full suite (`make test`) is slow, and `internal/reconcile` alone takes
20-30 minutes. Always pass `-timeout` when you run it directly: go's default
of 10m kills it.

## Read CI for YOUR commit, never an unfiltered run list

Several agents push branches here at once, so `gh run list` without a filter
shows other branches' runs, and one of them failing says nothing about yours.
An agent once took another branch's red `go` job for its own and started a
needless stress run. Always key CI to the commit you pushed:

- `gh pr checks <your PR> --watch` (checks for the PR's head commit), or
- `gh run list --commit $(git rev-parse HEAD)` / `--branch <your branch>`.

Before acting on a failure, confirm its `headSha` is your HEAD.

## Deploying the factory: CI does it, merging to main is the deploy

`.github/workflows/deploy-factory.yml` deploys the factory after CI passes on
every main commit that changes what it ships (cloudflare/, image/, the Go
cross-compiled into the image). So to get a fix into the factory, merge it.
To wait for your fix to be live, run `ticfac factory wait-deployed <your
merge sha>` — never a hand-rolled `gh run list` polling loop. It reads the
factory's own answer to what it runs and exits 0 once that commit contains
yours (or nothing it ships changed), 1 naming the deploy-factory or CI run
that failed to carry it, 5 on `--timeout` (default 90m). A deploy for your
sha that is skipped or cancelled is normal: deploy-factory deploys the newest
green main head and a newer commit's deploy supersedes yours, carrying your
fix with it. A loop keyed to your sha's own run never ends in that case
(several did, for hours, on 2026-10-01). Check the factory read-only with
`ticfac factory status`. A deploy does not wait for live runs:
`rollout_active_grace_period` keeps a rollout off their containers, and the
run's summary names any run still holding the previous image. Retry a failed
deploy with `gh workflow run deploy-factory.yml`. A local `ticfac factory deploy` is the fallback only
(first install, token rotation, CI unable to deploy): from this Mac it builds
an emulated amd64 image and is fragile. The repository is public: never paste
the factory URL, account ids or tokens into a log, commit or PR.

## A live run may own the main checkout

`ticfac run-epic` works from the main checkout. Do your work in a git
worktree, and never kill processes by pattern (`pkill -f`): this host is
shared with other agents' runs. Kill by exact pid only.

## Never change machine-wide tools

`pi`, `tk`, `ticfac`, `claude`, `herdr` and the Node/Go toolchains on this
host are shared by every live run and every other agent. Never install,
upgrade or downgrade them globally (`npm i -g`, `go install` into the shared
bin, `brew upgrade`) to test something: install into a scratch prefix
(`npm i --prefix <scratch>`, `GOBIN=<scratch>`) or a container. A global pi
upgrade to test Pi 1.0 left a 1.0-format `~/.pi/agent/models-store.json`
behind; after the downgrade the old pi crashed listing models and a live
local run died dispatching its next tick (epic 43y, 2026-10-04).
