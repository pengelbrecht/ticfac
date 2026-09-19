# ticfac

Execution and orchestration for [ticks](https://github.com/pengelbrecht/ticks): a
reconciler over two durable authorities (the ticks tracker and Git, including
`.ticfac/` run state), a four-operation executor protocol
(start / inspect / cancel / collect), role jobs for review and closeout, and
hosts — the local subprocess executor, Herdr, and Cloudflare.

The architecture is `docs/projects/2026-09-01-ticfac-architecture/SPEC.md` in
the ticks repository. ticfac talks to ticks only through `tk --json` and the
pinned contract bundle under `contracts/` there; it never imports ticks' Go
packages. Migration phases and gates: SPEC §12. Roadmap and state: the
`hzm` project in ticks' tracker.

Status: Phase 1 (reconciler and local subprocess executor) — in progress.

## Operator surfaces (tick glb)

The operator surfaces ticks' pwp deleted from `tk` with no replacement are
either owned here or recorded as deliberately dropped — nothing is left
implicitly missing:

| Surface | Decision | Where / consequence |
|---|---|---|
| factory webhook | **owned** | `ticfac factory webhook` (register by default, `--status`, `--delete`) |
| herdr pane badges | **owned** | `ticfac herd paint` — display-only, TTL-expiring badges over the herdr executor's own attempt records |
| blocked/settled chimes | **owned** | `ticfac herd notify` — once-per-episode, with rate-limit retraction |
| orchestrator guard | **dropped** | tk's guard nudged an agent-run orchestrator pane; ticfac's orchestrator is a program whose liveness is `ticfac status` and whose attention surface is `ticfac watch`. Consequence: a hand-run agent orchestrator in a herdr pane has no watchdog. |
| herdr-ticks plugin install/check | **dropped** | the plugin is ticks' to settle (ticks tick si0); its hooks may be pointed at `ticfac herd paint`/`notify`. Consequence: `herdr plugin install` remains the install path, and a stale plugin's hooks fail until si0 retires or repoints them. |

The same table lives in `ticfac herd --help`, where the consumer (the
plugin, or a person) looks.

## Contracts

`contracts/` is a **vendored, pinned copy** of the ticks contract bundle —
version 3.0.0, fetched from `pengelbrecht/ticks` at the commit recorded in
`contracts.pin.json`. It is verified offline by digest on every test run and
against GitHub at the pinned ref in CI, and every file in it has a Go reader in
`internal/contracts/parity`. `CONTRACTS.md` says how that works and how to
adopt a new bundle version. Never edit a file under `contracts/` here.

## image

`image/` — the sandbox image's build context, the container a cloud run
boots in either role, moved there from `cloud/sandbox` by SPEC §12 Phase 4
item 4 — is **vendored and pinned**, like `contracts/`. ticks owns the tree:
its `internal/sandbox` suite runs those scripts from its own copy at
`cloud/sandbox`, and this repository consumes it at the immutable commit
recorded in `sandbox.pin.json`, verified offline by digest and git mode on
every test run (`internal/sandboxpin`), and against GitHub at the pinned ref
in CI (`go run ./cmd/sandbox check` / `verify-upstream` / `sync`). Two
repositories carrying one tree with no check between them is how the shipped
image diverges from the tested one. Never edit a file under `image/` here.
Change it in ticks, move `ref` in `sandbox.pin.json`, and run
`go run ./cmd/sandbox sync` — then commit `image/` and `sandbox.pin.json`
together. The bytes did not change in the move, so the pinned ref is
unchanged; only where the vendored copy lives here moved.

## Development

```
go build ./...                    # the ticfac binary
make test-short                   # readers, negative controls, CLI (-timeout 45m)
make test                         # the full suite, same timeout
go run ./cmd/contracts check      # verify the vendored bundle, offline
go run ./cmd/sandbox check        # verify the vendored image context, offline
```

`make test` / `make test-short` pin `-timeout 45m`: `internal/reconcile`'s suite
runs 600-620s even under `-short`, past `go test`'s default 10-minute
per-package timeout. Use these targets (or pass `-timeout` yourself) rather
than a bare `go test ./...`.

## Deploying the factory

`ticfac factory deploy` installs or upgrades the factory in the operator's
own Cloudflare account, from the bundle embedded in the exact binary running
the deploy — the version pin rides the repository (D16). `ticfac factory
setup` is the first-run walk: it climbs the credential ladder one rung at a
time (wrangler, a deployment, a GitHub credential, model access) and verifies
every rung against the live service before storing it; `ticfac factory status`
re-checks all of them live.

`.github/workflows/deploy-factory.yml` runs the same installer in CI on a `v*`
tag, so releases upgrade the factory: configure `CLOUDFLARE_API_TOKEN`,
`CLOUDFLARE_ACCOUNT_ID` and `TICFAC_FACTORY_TOKEN` (the `factory_token` from
`~/.ticfacrc`) as repository secrets to enable it. A repository without them
skips the deploy with a warning naming what is missing — the factory is the
operator's opt-in, not a service this repository runs.

