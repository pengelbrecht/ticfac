# Working in ticfac (for any agent: Claude, pi, Codex, …)

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

## A live run may own the main checkout

`ticfac run-epic` works from the main checkout. Do your work in a git
worktree, and never kill processes by pattern (`pkill -f`): this host is
shared with other agents' runs. Kill by exact pid only.
