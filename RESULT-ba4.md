<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/bo9/attempt-2/ba4`, base `a9a09ee315eba993214254fd50bbbcf1fbe30f7b`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# ba4 — ticfac run --cloud accepts --config: carry the named config in the factory submission

## What changed

`ticfac run <epic> --cloud --config <name>` is now accepted: the factory's
submission record carries the named config as a `config` field, and the
in-factory orchestrator container forwards it to the `ticfac run-epic` it
execs, which resolves it with the same precedence a local run already
had (flag > epic's `config:` label > `[configs]` default).

- `internal/cli/run.go`: removed the `--cloud` + `--config` refusal.
- `internal/cli/run_cloud.go`: `submitCloudRun`'s submission struct gained a
  `Config` field; `runCloudCommand` passes `*fl.config` into it (previously
  hard-coded `""`) and the "starting" prose names the config, matching
  `--cloud-workers`'s existing wording.
- `internal/cli/cloud_harness_preflight.go`: doc comment updated — the
  subscription preflight already took a `runConfig` parameter for
  `--cloud-workers`; it is now exercised by `--cloud` too.
- `cloudflare/src/runs.ts`: `RunSubmission`, `RunWorkflowParams` and
  `StartRunInput` gained a `config?: string` field; `parseSubmission`
  validates it as a free-text field (like `notify`); `startRun`/`submitRun`
  thread it through. Deliberately left out of the queued-submission record
  (`room.queueSubmission`), matching the existing precedent for `substrate`:
  the everyday CLI surfaces this tick touches never combine `--config` with
  `--queue`.
- `cloudflare/src/sandbox.ts`: `OrchestratorEnvInput` gained `config?:
  string`, exported as `TICKS_CONFIG` by `orchestratorEnv`.
- `cloudflare/src/run-workflow.ts`: the orchestrator container boot forwards
  `params.config` into `orchestratorEnv` for the non-review job.
- `internal/sandboximage/sandboximage.go`: added `EnvConfig = "TICKS_CONFIG"`.
- `image/common.sh`: reads `TICKS_CONFIG` into `run_config`.
- `internal/factory/ticfacentrypoint.go`: the `run`-phase override appends
  `--config "$run_config"` to the `ticfac run-epic` argv when set.
- `image/README.md`: documented `TICKS_CONFIG` in the entrypoint contract
  table.

## Tests

- Go: `internal/cli/run_config_flags_test.go` — replaced the stale
  `TestRunRefusesTheConfigOnACloudSubmission` with
  `TestRunCloudForwardsTheNamedConfig`, asserting the POST body's `config`
  field and the "run config: claude" prose line.
- Go: `internal/factory/ticfacentrypoint_test.go` — added
  `TestOverrideForwardsTheNamedRunConfig` and
  `TestOverrideLeavesOffTheConfigFlagWhenUnset`, running the real staged
  override block against a stubbed `exec`.
- Worker (TS): `cloudflare/test/run-artifacts.test.ts` — added a `parseSubmission`
  describe block for the named config (carries it, defaults to absent,
  refuses a bad value) and two `orchestratorEnv` cases (sets/omits
  `TICKS_CONFIG`).
- Worker (TS): `cloudflare/test/run-routes.test.ts` — added two end-to-end
  `/api/runs` tests asserting the Workflow's `params.config`.

## Commands run

- `go build ./...`
- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 go test ./internal/cli/... ./internal/factory/... ./internal/sandboximage/...`
- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — green
- `cd cloudflare && npx tsc --noEmit` — clean
- `cd cloudflare && npx vitest run test/run-artifacts.test.ts test/run-routes.test.ts test/run-workflow.test.ts test/sandbox.test.ts` — all passed
- `make ts-gate` — green (one pre-existing, unrelated Biome info note in `src/phone.ts`)

Did not run the full `internal/reconcile` suite locally (not touched by this
diff) or the full `vitest run` (it exceeded a 9m50s local timeout in this
sandbox unrelated to this change — ran the specific suites covering the
touched modules instead, all green).

## For the next tick / reviewer

- `g5f`, `rkk`, `u2g` (siblings in the same wave) are untouched by this diff.
- The queued-submission path (`tk cloud run --queue`) does not carry `config`
  through to ignition — consistent with how `substrate` is also refused
  rather than threaded through a queue there today; no CLI surface combines
  `--config` with queuing, so this was left out of scope.
- `ticfac cloud run` (the expert command in `internal/cli/cloud.go`) has no
  `--config` flag and was not touched — the tick's acceptance criterion names
  `ticfac run <epic> --cloud --config claude` specifically.

```findings v2
[]
```

STATUS: DONE
