# Working in ticfac (for any agent: Claude, pi, Codex, …)

## How much to run locally: targeted tests + `make gate`, then let CI verify

Run the tests you touched or added (`go test -run '<names>' -timeout 20m ./<pkg>/`)
and `make gate`, then push and let CI run the full suite. Don't run the full
`internal/reconcile` suite locally as a matter of course: it takes 20-30 minutes
and this host is shared with live runs and other agents. If CI goes red on your
commit, fix it and push again. Run heavy local commands at low priority
(GOTEST_PARALLEL=4, GOFLAGS=-p=2).

## Before you push: run `make gate`, not just your package

`make gate` is gofmt, `go vet ./...` and the short suite across the whole
repository: exactly what a ticfac run's per-tick gate runs. A package-scoped
`go test ./internal/reconcile/` is not enough. Some tests guard the repository
rather than a package, and they only run in a whole-repo pass. The one that
catches people is `internal/shorttest`: every end-to-end test must call
`shorttest.EndToEnd(t)`, or carry a `short:` doc-comment line saying why the
gate should pay for it (see an existing test for the form). PR #71 failed CI
on exactly this after its package tests had passed.

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
cross-compiled into the image). So to get a fix into the factory, merge it
and watch that workflow's run for your commit (`gh run list --workflow
deploy-factory.yml`, then confirm its summary names your sha); check the
result read-only with `ticfac factory status`. It does not wait for live
runs: `rollout_active_grace_period` keeps a rollout off their containers, and
the run's summary names any run still holding the previous image. Retry a failed deploy with `gh workflow run
deploy-factory.yml`. A local `ticfac factory deploy` is the fallback only
(first install, token rotation, CI unable to deploy): from this Mac it builds
an emulated amd64 image and is fragile. The repository is public: never paste
the factory URL, account ids or tokens into a log, commit or PR.

## A live run may own the main checkout

`ticfac run-epic` works from the main checkout. Do your work in a git
worktree, and never kill processes by pattern (`pkill -f`): this host is
shared with other agents' runs. Kill by exact pid only.
