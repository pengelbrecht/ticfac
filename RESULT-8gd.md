<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/1vn/tap`, base `c60f1474ffda55368f8dd5edee8390c0d8af2a4a`, harness `pi-durable` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# 8gd — Host the cloud PR-review boot on pi-durable

The tick offered two paths: the operator re-confirms the review's omp exception
on the record, or the review conversation moves onto a hosted WorkerAgent. The
code path is the one this epic (ex6) commissioned — "the cloud PR-review boot
on pi-durable" is in its own description — so this attempt implements it. The
review is no longer the one cloud boot that runs a CLI harness on its floor: on
a deployment that hosts worker conversations, its conversation is a
WorkerAgent's and its container runs only the review's two halves.

## What changed

**The contract** (`contracts/worker-boot-contract.json`, bundle re-cut 2.3.0 →
2.4.0, `cloudflare/contracts.pin.json` moved with it): the review's own
boot/finish halves, `review_boot_arg`/`review_boot_command`/`review_boot_marker`
(`ticks-review-boot-ok`)/`review_boot_prompt_begin`/`review_boot_prompt_end` and
`review_finish_arg`/`review_finish_command` — commands of the *orchestrator*
image, because a review boots from the entrypoint the epic orchestrator does.
The marker line keeps the worker's two field names with review meanings, stated
in the contract: `branch=` is the ref that was reviewed, `result=` the findings
path the finish posts. CHANGELOG entry added.

**The image** (`image/entrypoint.sh`): `--boot` runs the review's setup (inputs,
gateway, clone, PR fetched as a ref, model probe — no CLI harness route or
probe, mirroring worker.sh's hosted skip) and prints the handoff: marker line,
prompt between the review's markers, exit 0. `--finish` posts the findings file
through the existing `post_review_findings` and exits with the post's status
(12/EXIT_REVIEW when nothing was posted — the honest failure). A bare boot on
the hosted kind is refused early with the config class (the empty-cmd trap);
`--boot`/`--finish` outside a review, or on a CLI-harness review, are refused
the same way. The all-in-one CLI path is unchanged.

**The harness host** (`harness/src/host/worker-attempt.ts`):
`WorkerAttemptSpec.kind` (`worker` | `review`, default `worker`). A review gets
no workspace in its env — restoring a review from the reviewed ref would check
out hostile code, the one thing the review phase exists not to do — no
`workspaceCheckpointExtension`, no `workerOnYield` report pushback (a review's
findings are not a STATUS'd report; parity with the CLI path), a review-shaped
section, and a `prepareFinish` that restores nothing. Everything else — boot
phase, conversation, wall deadline, stuck watch, finish phase, settlement — is
the worker machinery, unchanged and shared.

**The agent** (`cloudflare/src/worker-agent.ts`): the host's protocol is
selected by the spec's kind — the review's commands and markers, never the
worker's.

**The supervisor** (`cloudflare/src/run-workflow.ts`):
`reviewHarness(run, deployment, hosts)` no longer drops `pi-durable` (only the
deleted `pi`), honors it where conversations are hosted, and on a hosting
deployment the floor itself is the hosted kind. In the review boot, a resolved
hosted kind starts the run's WorkerAgent with a review spec (role `review`,
kind `review`, the same env a CLI review got, the routed Workers-AI model, wall
= the run's wall clock) instead of `ORCHESTRATOR_COMMAND` — no process is
started. The watch observes the agent: its log by cursor, its state mapped onto
the process view (running / completed / failed, absent → gone), same lease
renewal, same budgets and stop records. Trips, out-of-looks and linger drain by
reclaiming the agent; dead and unanswerable ends settle it too, so no agent is
left heartbeating over an abandoned conversation.

**The deployable config** (`cloudflare/wrangler.toml`): `RUN_HARNESS` is unset,
with the reason in its comment — it is the review's FIRST rung, and pinning it
(as this config pinned `omp`) held the review on a CLI no matter what the
worker route said. Unset, the review falls to `RUN_WORKER_HARNESS`
(`pi-durable`) and hosts; the orchestrator container is unchanged (no harness
pin; its entrypoint probes default to omp; its job is `ticfac run-epic`).
`RUN_HARNESS` stays the lever a deployment uses to pin a CLI or claude-sub
review. `phase0-compat.test.ts`'s deployment-var pins were updated to match.

**Docs**: `docs/pi-durable-cloud-run-runbook.md` no longer describes the review
as the CLI-harness exception.

## What I ran

- `make gate` — gofmt, `go vet ./...`, `go test -short ./...` — all green.
- `make ts-gate` — biome, `contracts:check` (16 contracts at bundle 2.4.0),
  `tsc --noEmit` — all green.
- `cloudflare`: `pnpm test` — 80 files, 1870 tests, all green (includes the
  rewritten `worker-boot.test.ts` ladder matrix, the contract pinning of the
  seven new review spellings, the deployment-var pins, and three new hosted
  review supervisor tests: routing/spec/no-process, reclaim on trip, reboot
  to failure).
- `harness`: `pnpm exec vitest run` — 10 files, 97 tests, all green (includes
  two new review-kind host tests: the review's own halves with none of the
  worker machinery, and the finish-without-findings failure).
- `internal/sandboximage`: full package (not short) — green; includes four new
  tests: `--boot` handoff (PR fetched, checkout untouched, no harness, nothing
  posted), `--finish` posting the findings, finish-without-findings = EXIT_REVIEW,
  and the three refusals.
- `internal/contracts/...` and `internal/contracts/parity` — green at bundle
  2.4.0.

## What the next tick has to know

- **On the production deploy, the review is hosted.** The routing needs both
  halves of this change in the same deployment: the image (new `--boot`/
  `--finish` halves) and the config (`RUN_HARNESS` unset). A deploy that ships
  one without the other still works — an unset `RUN_HARNESS` with the old image
  would refuse the review at boot (config class, no silent wrong behavior), and
  the new image with a pinned `RUN_HARNESS` keeps the CLI review — but the
  review only hosts once both land. The factory deploy ships both (one
  deploy-factory run builds image and Worker together), and CI runs the
  deployable config's pins.
- **The review's findings live in the container's /tmp, not on a branch.** A
  container lost between the conversation and the finish has lost them; the
  finish exits 12 and the review reboots (up to `MAX_SANDBOX_BOOTS`), which
  re-runs the review from scratch. That is deliberate — a review commits
  nothing, so there is nothing to restore from — and it is the one recovery
  difference from a worker attempt worth remembering when reading an A2 fault
  injection.
- **A hosted review is watched through its agent, not a process.** The
  supervisor's hosted branch (`run-workflow.ts`, `booted.hosted`) is the place
  to touch for anything that reads a review's process; `observe` and
  `drainAndKill` take the agent, and every ending path settles it.
- **`RUN_HARNESS` semantics are unchanged in code** — it is still the review's
  first rung and the orchestrator's probe harness — but this deployment no
  longer sets it. A deployment that wants the claude-sub review rung still
  selects it by pinning `RUN_HARNESS=claude` + `RUN_MODEL=<alias>`.

```findings v2
[
  {
    "kind": "proposal",
    "title": "A hosted review that ends its turn without findings wastes the run",
    "severity": "low",
    "body": "The hosted review deliberately carries no yield pushback (parity with the CLI review, whose one-pass run has none either): a conversation that ends its turn without writing the findings file settles done, the finish posts nothing, the run stops, and the review must be re-requested. A review-shaped onYield nudge -- one bounded re-prompt naming the findings path, analogous to the worker's workerOnYield -- would catch the common case (a model that answers in chat instead of writing the file) inside the same run.",
    "evidence": "harness/src/host/worker-attempt.ts registryFor (the review branch installs no GenerationTask onYield); image/entrypoint.sh harness_prompt"
  }
]
```

STATUS: DONE
