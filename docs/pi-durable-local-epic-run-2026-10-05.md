# A real local epic run on pi-durable, completed 2026-10-05 (tick qdg, epic 43y)

This is the durable record of epic 43y's [A4] **local** half: a real local
epic run — a whole `ticfac run-epic`, claim → dispatch → collect → integrate →
gate → review → closeout — completing with **every worker on the pi-durable
harness**. The cloud half of [A4] is sequenced after the close-out merge by the
tick's own text ("the cloud half after the factory carries the epic's code"):
the factory deploys only from main (AGENTS.md), main does not yet carry this
epic, and tick `twa` (the `.tick/runners.cloud.toml` kind flip) is still open.
The runbook for the cloud half is in the qdg attempt report beside this
record's provenance (archived with the attempt), not repeated here.

## What ran, on what code

- **Orchestrator binary**: `ticfac` built from this repository at
  `d7e29d73f` — the integrated epic/43y tree the tick's attempt was cut from
  (the only commits past it on epic/43y are the run's own `.ticfac/`
  bookkeeping). Foreground form: `ticfac run-epic dmo …`, 2026-10-05
  12:45–13:27 UTC.
- **Worker harness**: the pi-durable Node host (`harness/src/local/main.ts`)
  from the same tree, its conversation in per-attempt local SQLite, driven by
  the local-subprocess executor with `$TICFAC_HARNESS_DIR` naming the harness
  package. Every dispatch's attempt record on the run branch shows
  `executor: local-subprocess`, `model: cloudflare-workers-ai/@cf/zai-org/
  glm-5.3` — there is no claude, no codex, no pi-CLI anywhere in the run.
- **Target repository**: `slugdemo` — a small, real Go repository (stdlib
  only) seeded with a real two-tick epic (`dmo`: build `internal/slug`
  test-first) plus its review and closeout role ticks, its own
  `.tick/runners.toml` declaring `substrate = "harness"` and every role on
  kind `pi`, its own gate in `[testing.commands]`, and a local bare origin.
  A scratch target was used because the harness package must live in the
  repository the executor works against (or `$TICFAC_HARNESS_DIR`), and
  because running a second epic against this repository's own tracker would
  collide with the live run that owns it. The epic's work is real: actual
  failing-test-first Go, written and gated by the workers.
- **Credentials**: the operator's Workers AI credential the local rung
  already reads (`~/.pi/agent/auth.json`, tick hpk) and the operator's
  Cloudflare API token for the Jev classifier — the same credentials every
  local worker of this run's own ticks uses. No factory, no sandbox, no
  forge: the target repo declares no PR rule, so close-out integration is
  the product default (a person merges the integration branch).

## The run, as it happened (2026-10-05, all times UTC)

| dispatch | tick | role | harness | outcome |
|---|---|---|---|---|
| #1 | t01 `internal/slug: Slug(s) with table tests` | implement-tick | pi-durable, GLM-5.3 | DONE first try, 12:53–13:00: test-first (red `undefined: Slug` → green), 10 subtests, merged `d834956`, gate green, closed |
| #2 | t02 `internal/slug: Join(parts...)` | implement-tick | pi-durable, GLM-5.3 | DONE first try, 13:00–13:06: red `undefined: Join` → green, 5 subtests, merged `fc56af5`, gate green, closed |
| #3 | r01 `Final review of the slug library epic diff` | review-epic (read-only) | pi-durable, GLM-5.3 | DONE, 13:07–13:15: re-ran the gate itself, scored [A1][A2][A3] met, one LOW finding (filed as backlog tick d56 by the run), **REVIEW-VERDICT: READY** |
| #4 | c01 `Close out the slug library epic: retro` | closeout-epic | pi-durable, GLM-5.3 | DONE, 13:16–13:26: verified both merges + gate, wrote `.tick/learnings.md` (2 lessons), integrated |

The run ended `done`: *"every tick of dmo is closed behind the integrated
gate"* (`ticfac.run-epic.v1`, `run_state: completed`). The person's fold
(the product default without a PR rule) followed: `main` fast-forwarded to
the integration tip `599139c`, and `make gate` passes on `main`.

What makes this evidence of the durable rung, not just of the orchestrator:

- each worker's `observations.jsonl` records `pi runner, pid …` and wip
  branch pushes during the turn (the per-round checkpoint path from tick xd3,
  retired by the finish phase per tick nou);
- the review ran read-only under a pinned no-push grade and still delivered
  a real verdict (tick x8e's fix is what let it run at all — a read-only
  worker carries no remote in its `worker.json`);
- every merge point has run-branch gate evidence (`gate-t01-1-go.json`,
  `gate-t02-2-go.json`, `gate-c01-4-go.json`, all `pass`).

## Where the full evidence lives

The complete bundle — the 66-line event feed, all four worker reports, the
three gate-evidence records, the review's findings block, the closeout's
learnings, the final run document, and the two source files the workers
wrote — is archived in the run's artifact space for this attempt:
`runs/epic-43y/qdg/evidence/` in the epic-43y run's checkout (not committed;
reports are run state by this repository's own rule). The scratch repository,
its origin and the run's state root are beside it under
`runs/epic-43y/qdg/scratch/`.

## What this does and does not claim

- **Claims**: a real local epic run completed on the integrated pi-durable
  tree, with every implement and role worker on the durable harness, real
  model calls, real git integration, real gates, a typed review verdict and
  a close-out — [A4]'s local half, and the local shape of [A1] (no worker on
  any non-durable path).
- **Does not claim**: the cloud half ([A4] is half-met until the post-merge
  cloud run executes); the mid-tool kill and mid-turn container-loss proofs
  ([A2] has its own staging proofs, `harness/proof/`); that a demo-scale
  epic exercises the same conflict-and-retry surface a ticfac-repo epic does.
