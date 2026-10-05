# The cloud half of [A4], post-merge: the owned runbook (tick kkt, epic 43y)

Epic 43y's [A4] asks for "a real hn6-style cloud epic run and a local epic run
complete on pi-durable". The local half ran on 2026-10-05 and is recorded in
`docs/pi-durable-local-epic-run-2026-10-05.md` (tick qdg). The cloud half
cannot: the factory deploys **main only** (AGENTS.md, `deploy-factory.yml`),
main is strictly behind epic/43y until the close-out merge, and the final
review gates the merge — so the run is sequenced **after** the merge by
construction, and this runbook is how it does not fall through the crack
afterwards. Without an owner it closes at best *predicted*; this document is
the ownership.

**Who owns it:** the operator, from a post-merge checkout. Not a dispatched
worker: starting a cloud run spends money on the operator's Cloudflare
account and touches a live external system, and `.tick/config.md`'s standing
orders put both in the *always ask* column. A tick inside this run can only
ship what makes the runbook mechanically safe — the harness preflight below —
and write the runbook down.

## Preconditions, in the order they unblock

1. **The close-out merge.** epic/43y merges to main through the run's own
   PR + CI close-out (`.tick/config.md`). This carries, in one merge: the
   pi-durable image (jhp — `image/common.sh`'s kind set is
   `omp | claude | pi-durable`), the hosted WorkerAgent (xd3, 4uj), and the
   cloud overlay flip (twa — `.tick/runners.cloud.toml`'s role cells to
   `kind = "pi-durable"`), because they are all children of the same epic.
2. **The deploy.** `ticfac factory wait-deployed <merge sha>` — exit 0.
   Never a hand-rolled `gh run list` loop: a deploy superseded by a newer
   main commit is normal and the loop never ends (AGENTS.md). The
   preflight below will also say this if you forget.
3. **Forge access for runs of THIS repository.** The PR rule
   (`.tick/config.md`) requires the factory to push and open PRs; until the
   GitHub App rung lands (epic dm6) that is not available factory-side.
   Either dm6 has landed, or run the cloud epic against a repository that
   declares no PR rule.

## The run

```
ticfac run hn6 --cloud        # hn6 is the operator's named candidate, or the next feasible epic
```

The command pushes the current branch, submits to the factory, attaches the
live view (Ctrl-C detaches without stopping; run it again to re-attach or,
once finished, to resume). Every worker of the run boots a factory sandbox
container on pi-durable: its conversation runs in the factory's WorkerAgent
Durable Object, its tools in the container, its substrate recorded per
dispatch on the run branch.

## What "observed" means for [A4]'s cloud half

The cloud half is **observed** — not predicted — when all three hold:

1. The preflight passed: the factory reported `harness_kinds` containing
   `pi-durable` (i.e. the answering factory is the post-merge deploy) and
   the branch's cloud routing resolved to shipped kinds.
2. The run completed: `run_state: completed` in its `ticfac.run-epic.v1`
   document, every tick closed behind the integrated gate.
3. Every dispatch of the run records its workers on `pi-durable` containers
   (`harness: pi-durable` in the attempt records) — a cloud run whose workers
   ran `omp` or `claude` completes fine and observes nothing about [A4].

Record the result beside qdg's local-half record (a doc under `docs/`, plus
the run's artifact space), and note in the epic's tracker record that [A4]'s
cloud half moved from *predicted* to *observed*.

## The guard this tick shipped: the harness preflight

`ticfac run <epic> --cloud` refuses, **at the knee, before anything is pushed
or booted**, a submission whose branch routes a cloud job to a harness kind
the deployed factory's image does not ship — the state this repository was
in between the image change and twa's overlay flip, where every container of
a run died at boot with `unknown harness kind 'pi'`:

- **The branch half** is HEAD's `.tick/runners.toml` with the
  `.tick/runners.cloud.toml` overlay, resolved exactly the way the run's own
  containers resolve it (`reconcile.CheckRouting` on the embedded cloud
  profile set: every role, every tier the ladder can reach, the on-demand
  jobs at the ceiling). A routing that does not resolve is refused here too,
  earlier than the run's own start refusal.
- **The factory half** is the factory's own answer: `GET /api/deployment`'s
  `harness_kinds`, the kind set `image/common.sh` accepts (pinned to the
  shell by a parity test). A factory that predates the field reports
  nothing — the preflight warns with the missing answer and
  `ticfac factory wait-deployed`, and proceeds; an unreadable factory is the
  same honest "cannot ask", never a refusal on transport.
- A repo with no `.tick/runners.toml` routes on the factory's defaults and
  is skipped silently.
- The expert `ticfac cloud run` submits without the preflight: the expert
  verbs are the operator's escape hatch, as with their other flags.

The refusal names both of the runbook's fixes in order — flip the
`.tick/runners.cloud.toml` cells to a kind the image ships, or
`ticfac factory wait-deployed <merge sha>` for the deploy that ships the
kind — so the trap the runbook exists for cannot be executed by accident.
