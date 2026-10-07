# Epic v5t closed out: Claude on the subscription in cloud runs, chosen per epic

The retro of epic `v5t` from its close-out (tick `9uc`, run `epic-v5t`). It is written from the
integration branch `epic/v5t`, the run's records under `.ticfac/runs/epic-v5t/`, and the ticks'
own records. Plans and memory are not sources here. The learnings it compacts are in
`.tick/learnings.md`.

## What the epic set out to do

Take jvj's spike (`docs/spikes/jvj-claude-sub-cloud.md`) to production and let each epic pick
its cloud routing. An epic labelled `config: claude` runs its workers on the operator's Claude
subscription: implement on sonnet, climbing to opus; review and close-out on opus; versionless
aliases only. The token is never in a container, and the job steps down to Workers AI when no
subscription is free. Every other epic keeps the GLM routing unchanged. The cloud rule becomes
"no per-token billing in the cloud".

## How the run went

| tick | role | model, tries | outcome |
|---|---|---|---|
| 6fv | implement | GLM 5.3, 1 | merged `200fdd02`: CloudRule becomes the billing rule; pool binding, leases at dispatch/collect, `/api/claude-sub`; image placeholder route; deploy/status/doctor name the labels, never values; the weekly `claude-cli-pin` workflow |
| tda | implement | GLM 5.3 → opus, 2 | try 1 refused whole: boundary violation on `.tick/runners.cloud.toml`. Try 2 merged `3c4a5295`: `[configs.<name>]`, `--config` > epic label > default, doctor/preflight checks, the config named in status/watch/the epic PR, schema in sync |
| yck | implement (absorbed from tda) | GLM 5.3 → opus, 2 | both tries BLOCKED, correctly: the only deliverable was outside the worker boundary. The ceiling hold went to a person, and an operator agent appended the configs (`d962642a`) |
| 54l | review | opus | **NOT READY**, two blocking findings: the hosted (WorkerAgent) door ignored the rung (yhe), and the Go executor refused the factory's Workers AI step-down (y38) |
| y38 | implement | GLM 5.3, 1 | merged `008fce3c`: the executor accepts exactly the rung's declared fallback, namespace-normalised, and records the pair that ran |
| yhe | implement | GLM 5.3, 1 | merged `25243468` through an opus resolve of `model_test.go` against y38: a claude-sub job goes to its own container on a hosted deployment, with a real-door Go e2e test |
| 8rm | review, round 2 | opus | **READY** |

Seven implement attempts ran: five on GLM, two on opus. Every GLM attempt that could deliver did
so on its first try (6fv, y38, yhe). The two escalations both came from the worker boundary, not
from model ability. The run produced 18 findings. Three entered the epic: yck while the work was
under way, then y38 and yhe through the NOT READY. One went upstream (`pengelbrecht/ticks:gnw`).
Fourteen became backlog ticks: k20, lrb, lxo, eov, 8qi, jwq, 5ww, oqn, 2zs, bw7, uuk, km9, 0iu,
o3f.

## Delivered against the definition of done

- **[A1] Built, tested across the seam, and not yet demonstrated live.** The tree routes a
  `config: claude` epic's workers to the subscription rung. The token is injected by the factory
  and never placed in a container. When no lease is free, the job steps down to GLM 5.3, and
  the Go executor now accepts that step-down. On a hosted deployment the job runs in its own
  container. `TestTheRealDoorRunsAClaudeSubJobOnAHostedDeployment` drives the real door, and
  `claude_sub_parity_test.go` guards the spellings both languages share. The run that [A1]
  describes has not happened, and it cannot happen before merge, because the factory deploys
  main only. It also needs three things:
  - the operator sets at least one `CLAUDE_SUB_TOKEN_<LABEL>` Worker secret (6fv's text names it as
    the operator's, but no acceptance item does);
  - **8qi**: the image still pins claude CLI 2.1.227, so `opus` and `sonnet` resolve to the 5
    models, not 5.5, until the weekly pin PR merges;
  - **uuk**: a claude job that outlives the 2h lease TTL can be shadowed by a rival hosted
    worker on a re-Start.
- **[A2] Met by construction.** `[configs] default = "glm"` declares the same cells the cloud
  file always had, a local run is not refused for a cloud label (`f7a9eee4`), and guards hold
  the real file. No separate live GLM cloud run was made under the new code.
- **[A3] Met.** `status`, `watch` (on its cost line) and the epic PR name the run's config. A
  run is one config (`f7a9eee4`), so the run's escalation and cost lines are per config, and two
  epics are compared by reading their two status outputs side by side.
- **[A4] Met.** CI run 37567480913 on `25243468`, the last source change, passed every job: go
  (packages and reconcile 1–3), race, lint, contracts, typescript and release config. Later heads
  touch only `.ticfac/` and `.tick/`, and CI skipped them. Main is folded in: the merge base is
  main's tip.

## What is left open

- **The live proof of [A1].** This is the next epic (see the report's proposal). Its first
  ticks are 8qi plus the operator's secret. Next comes a named tick inside that epic that runs a
  real `config: claude` epic in the cloud and a GLM epic beside it. Then come uuk, eov (a
  claude config with no subscription token passes silently when the factory can't be asked),
  lrb (`--cloud --config` is refused; only the label selects) and km9 (a real-door test for the
  step-down).
- **The worker boundary, for the third time.** 7vp (43y's twa) and lxo (here) are the same
  wall: a routing-file deliverable cannot be written by any dispatched role. The run still
  dispatches such a tick. yck spent two workers and a hold on it.
- **Tidy-ups.** o3f should close bw7 (y38 already fixed it in the tree). There are also k20,
  jwq, 5ww, oqn, 2zs and 0iu.
- **djo (Phase 5b, subscription seats).** The Claude half is now superseded by jvj and v5t. It
  needs an operator decision: rescope it to Codex seats or close it.

## What the retro changes in `.tick/learnings.md`

- The production-entry-point rule now names v5t. 6fv's door tests unbound `WORKER_AGENTS`,
  which production binds, and the rung was dead on the real door until review. Tests of an
  entry point bind what production binds.
- One blocking finding per seam: the run makes one tick per finding, so y38 and yhe split one
  step-down seam, needed a conflict resolve, and filed a duplicate high finding (bw7).
- Boundary artifacts are checked at planning, and a routing cell is a named operator step,
  never a dispatched tick.
- A TS↔Go seam gets a parity guard over its spellings and a Go test that drives the real door
  (`cloudflare-workers-ai/…` vs `workers-ai/…` was green on both sides' fakes).
