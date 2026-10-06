<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/umq/attempt-1/dax`, base `37a686d3ee8e5028aa4453c07d4777069833d8bb`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# dax — default new runs to the durable_object path; delete the 0.x machinery

Epic umq, cutover tick. Commit `49955d6` on `tick/umq/attempt-1/dax` (43 files, +1098/−2482).

## What changed

**New runs default to the new path (cloudflare).** The submit-time default in `src/runs.ts` is now `do_v1` (`DEFAULT_RUN_SUBSTRATE` in `src/run-substrate.ts`): every new run gets a `run_substrate` row recording the durable_object substrate and the image pin, asked for or not, so "this run is on the new path" is a fact about the run rather than a guess from its age. An explicit `"sdk0"` ask still survives to ignition as the run's own choice (parseSubmission carries it through instead of dropping it — otherwise the default would silently overwrite it). Queueing rides the default, and the old refusal of `do_v1` + `--queue` is gone (ignition applies the same default); an explicit `sdk0` + `--queue` is now refused, because the parked submission carries no substrate and would ignite on the default. Pre-cutover runs keep their 0.x route: absence of a row still reads `sdk0`. The CLI (`TICFAC_CLOUD_SUBSTRATE`) is unchanged except its comment — unset now means "the factory's default", which is the new path; `sdk0` remains the explicit opt-out while a deployment still serves the 0.x application.

**Deleted with the 0.x application:**
- `rollout_active_grace_period` and its pin test (wrangler.toml; phase0-compat "never lets a container rollout take a live run's container").
- The `[[containers]]` block for the `Sandbox` class (app `ticks-orchestrator`, its image, `max_instances`, `instance_type`). The `SANDBOXES` binding, the class and the migrations stay — pre-cutover records route there and the DO migration history cannot be unwound.
- The `max_instances` mirror check: `VerifyContainerCapacity` + its patterns in `bundle.go`, the deploy's call, `--keep-images` (the flag had no remaining subject), and the mirror pin tests (Go + phase0, replaced by "the ceiling is declared once, in `[vars] FACTORY_MAX_INSTANCES`" — the durable_object policy refuses a platform `max_instances`, so the var is no longer a mirror).
- `internal/factory/rollout_held.go` (+ its tests) and most of `rollout.go`'s confirm wait: the wait loop, `RolloutError`, the health-error parsing, the extension logic, `Result.RolloutConfirmed`/`RolloutHeldBy`, `--skip-rollout-wait`, and the rollout API/token plumbing in `Options`.
- `deploy-factory.yml`'s rollout wait commentary and the "held by live run(s)" summary branch.
- container-capacity.ts's don't-boot workaround: the `RECLAIM_LOOKBACK_MS` time guess that kept the reclaim from asking questions (on the durable_object substrate no question but `startProcess` boots a container, and `isRunning` decides what gets asked; the constant stays for the count window and the sweep re-check).

**What replaced the confirm wait** (`internal/factory/containers.go`, new): `resolveDeploymentImage` parses the FactorySandbox image the deploy pushed out of wrangler's output (anchored on the image repository `ticks-factory-factorysandbox-*`, not the Docker-build noise), and — for the idempotent no-push case, where wrangler prints no image block — falls back to a one-shot read of the application record. The digest is recorded in `factory_deployment_image` as before; when neither source names one, the deploy warns and records nothing rather than failing, because on this policy there is no rollout whose success the digest was evidence of.

**The old app is deleted by the deploy** (`internal/factory/legacyapp.go`, new): after a successful deploy, the 0.x application `ticks-orchestrator` is deleted (`wrangler containers delete <application-id>` from the listing) **only when nothing holds it** — a live run with no substrate row (any pre-cutover run) holds it open, and so does anything unreadable (no deletion on a guess). Its image repository's images are deleted wholesale with it (the ~45 GB that shared the account's 50 GB image storage). Every refusal is a report; the next deploy retries. Deleting it under a live run is exactly the deploy-side container death this migration exists to make impossible, hence the guard.

**"Factory ready" is now claimed after the endpoint verifies** — on the durable_object application there is no rollout whose convergence a wait could confirm: wrangler's image preparation is inside the deploy (the retry for its 15-minute timeout stays), a running container keeps its startup image, and a new one boots what this deploy pushed. The CLI output, `--json` document (dropped `rollout_confirmed`), and the workflow summary were updated together so they cannot disagree.

Docs updated where they described the old machinery: `AGENTS.md` (deploy section), `cloudflare/README.md` (deploy, prune, rollback notes), `docs/pi-durable-cloud-run-runbook.md`, `image/README.md` (ceiling + serialisation note), migration 0023's comment, and the header comments of `factory-sandbox.ts`, `container-capacity.ts`, `env.d.ts`, `sandbox.ts`, `runs.ts`.

## What I ran

- `go test -short -count=1 -timeout 20m ./internal/factory/ ./internal/cli/` — pass.
- `make gate` (gofmt, `go vet ./...`, short suite across the repo) — everything passes except three `internal/exec/subprocess tests that **fail at base too** (verified by stashing the change and re-running at HEAD): `TestInterruptingAHungToolKillsItsGroupAndSaresTheRunner`, `TestAStoppedSupervisorInterruptsTheRunnersDetachedTools`, `TestACancelInterruptsTheRunnersDetachedTools`. They are process-group/pty tests in an area this tick never touches; see the finding below.
- `cloudflare`: full `pnpm exec vitest run` — 1783/1783 pass (77 files); `pnpm exec tsc --noEmit` clean; `pnpm lint` (biome, error-on-warnings) clean; `pnpm contracts:check` ok. `pnpm install --frozen-lockfile` was run first (deps were not installed in this container).
- The deploy path is exercised end to end by the package's own tests against the stateful wrangler stand-in (`internal/factory/testdata/fake-wrangler.sh`), which was extended for the new shape: the deploy output names the FactorySandbox image, the listing carries both applications until the 0.x one is deleted, `containers delete` works, and `d1 execute --json` answers the two reads (live-run count, live pins).

## What the next tick has to know

- **The old container app's actual deletion happens at the next real deploy**, not in this branch: the deploy deletes `ticks-orchestrator` (and empties its image repository) on the first deploy after the last pre-cutover run has ended. I could not run a deploy from this container (no credentials; touching the live factory is out of scope). The first post-merge deploy will print either the deletion or exactly what held it — that line is the A3 evidence to look for.
- **One thing to watch on that deploy:** whether real wrangler's output for the durable_object application carries the digest-pinned image line this repo's fake models. If it does not, the fallback reads the application record; if neither names a digest, the deploy warns, records no image, and still succeeds — runs pin their own image from the DO, so only `factory status`'s image line and `run_image` stamps would be absent. Graceful, but worth confirming once.
- **Rollback story changed:** with the 0.x application deleted and its images gone, rolling back to pre-umq code means redeploying the old commit (wrangler recreates the application and rebuilds/pushes the image fresh, ~10–15 min). No old images are kept for it.
- `TICFAC_CLOUD_SUBSTRATE=sdk0` produces runs that cannot boot once the 0.x application is actually deleted. The opt-out is only meaningful between this code deploying and that deletion.
- The three subprocess test failures are pre-existing on this Linux container (fail at HEAD without this change); do not attribute them to this branch.

## Residual seams noted for the review tick (l76)

- `DEFAULT_SANDBOX_IMAGE = "ticks-orchestrator"` and the declared-image machinery (`resolveSandbox`, `TICKS_SANDBOX_IMAGE` + the container's exit-6 self-check) still describe the deleted 0.x application. The check runs for do_v1 runs too (acquireContext is substrate-blind), so a repository declaring `[sandbox].image = "ticks-orchestrator"` would pass a check that no longer describes what boots. No repository in this repo declares it; the seam needs its own durable_object answer and I did not invent one in a deletion tick — filed below.
- The `Sandbox` class and `SANDBOXES` binding remain in the bundle solely for pre-cutover records and migration history; once no pre-cutover run can exist (after a few weeks), a later tick could consider dropping them.

```findings v2
[
  {
    "kind": "defect",
    "title": "Subprocess interrupt tests fail at base in Linux containers",
    "severity": "low",
    "body": "Three tests in internal/exec/subprocess fail on this Linux container with and without this branch: TestInterruptingAHungToolKillsItsGroupAndSaresTheRunner ('the tool's own child outlived the interrupt'), TestAStoppedSupervisorInterruptsTheRunnersDetachedTools and TestACancelInterruptsTheRunnersDetachedTools. They are process-group/pty behavior tests the tick never touches; they fail at HEAD before the change, so the gate on this host carries a pre-existing red. Related open tick fuf (detached tools whose runner was reaped) covers a neighbor case, not these.",
    "evidence": "go test -short -run 'TestInterruptingAHungToolKillsItsGroupAndSaresTheRunner' ./internal/exec/subprocess/ → interrupt_test.go:144 'the tool's own child outlived the interrupt'; reproduced with the branch stashed (base HEAD 37a686d)"
  },
  {
    "kind": "proposal",
    "title": "Declared-image check still names the deleted 0.x application",
    "severity": "low",
    "body": "DEFAULT_SANDBOX_IMAGE ('ticks-orchestrator') and the declared-image machinery it feeds (acquireContext's resolveSandboxImage, the TICKS_SANDBOX_IMAGE the container self-checks with exit 6) describe an application tick dax deleted. The check runs for do_v1 runs too, so a repository declaring [sandbox].image would be judged against an image no deployment serves, and a declaration matching the old name passes while the run actually boots the FactorySandbox image. The seam needs its own durable_object answer (what a declared image means when a run pins its own); it was out of a deletion tick's scope and no current repository declares one.",
    "evidence": "cloudflare/src/sandbox.ts:57 (DEFAULT_SANDBOX_IMAGE), cloudflare/src/run-workflow.ts:1007 (resolveSandboxImage in acquireContext), phase0-compat 'names the 0.x image the resolved-image machinery still compares against'"
  }
]
```

STATUS: DONE
