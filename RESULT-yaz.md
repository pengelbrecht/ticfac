# Tick yaz: final review of epic ilz (claude-sub operator guide)

## What I reviewed

Epic `ilz` as integrated: `origin/epic/ilz` (head `d26f7fe`) against the base
it was cut from, `30e61c2` (`git merge-base origin/main origin/epic/ilz`). The
substantive diff is two files — `docs/claude-sub-operator.md` (new, 136 lines)
and `README.md` (+5) — plus the controller's own `.ticfac/` and `.tick/`
records. No Go or TypeScript source changed. My working tree at
`tick/ilz/attempt-2/yaz` is identical to the epic head on both substantive
files (`git diff origin/epic/ilz -- README.md docs/` is empty), so what I read
is what the epic ships.

One worker tick, `56z` (closed), merged as `f05bb24`. Its report claims every
statement in the doc is checked against the three cited sources. That claim is
the epic's acceptance criterion A1, so I re-checked the doc claim by claim
against `cloudflare/src/claude-sub.ts`, `cloudflare/src/sandbox-executor.ts`,
`internal/reconcile/runconfig_select.go`, and — for the claims about the
operator surfaces — `cloudflare/src/index.ts`, `cloudflare/wrangler.toml` and
`.tick/runners.cloud.toml`.

## On the tests

There are none to judge, and that is the right answer for this epic rather
than a gap: the diff is prose. The integrated gate (`go`, `ts`) went green at
`f05bb24` (`.ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/evidence/`), but
a green gate says nothing whatsoever about this epic — it would be equally
green over a doc that described the opposite behaviour. The only check that
can fail this epic is reading the prose against the source, which is what the
rest of this report is. I did not re-run `make gate`: no package the gate
covers is touched by the diff, and the integrated run already recorded it
green on the identical tree.

## What is right

Checked and correct against the sources:

- **Label rules.** `^[A-Z0-9_]{1,32}$` matches `LABEL` at
  `cloudflare/src/claude-sub.ts:115`.
- **The wrangler commands.** `cd cloudflare && pnpm exec wrangler secret
  put|delete|list CLAUDE_SUB_TOKEN_<LABEL>` does target the production Worker
  (`cloudflare/wrangler.toml:6`, `name = "ticks-factory"`), which is the one
  that binds `CLAUDE_SUB_POOL` (`cloudflare/wrangler.toml:166`) and exports
  `ClaudeSubProxy` (`cloudflare/src/index.ts:2006`).
- **No redeploy needed.** `ClaudeSubPool.lease` calls
  `subscriptionLabels(this.env)` on every lease (`claude-sub.ts:447-453`), so
  the secret names are read live.
- **The wrapped-token warning.** `normalizeToken` strips all whitespace
  (`claude-sub.ts:175-179`).
- **Lease shape.** Keyed on the attempt's own job id, per-subscription cap
  from `CLAUDE_SUB_MAX_CONCURRENT` default 2, fewest-live-leases choice
  (`claude-sub.ts:100,182-185,329-360`; `sandbox-executor.ts:1825-1858`).
- **"A running job never switches subscription."** True, and for the reason
  the doc gives: the proxy's props are fixed when the interception is
  installed (`claude-sub.ts:18-22,559-561`), and the lease is asked once, at
  boot.
- **401/403 benches for 24 hours, and rotating does not clear it.**
  `AUTH_BENCH_MS` (`claude-sub.ts:113,208-210`); the bench lives in the DO, so
  only `unbench` clears it (`claude-sub.ts:375-377`).
- **Step-down, never a queue.** A failed lease (`none`/`exhausted`/`busy`)
  overrides the pair with the deployment's standing Workers AI pair for that
  one job (`sandbox-executor.ts:1840-1858`).
- **Config precedence.** `--config` > the epic's `config:` label > the
  `[configs]` default; a run keeps its first selection across a resume and a
  resume with a different `--config` is refused
  (`internal/reconcile/runconfig_select.go:204-228`). `.tick/runners.cloud.toml`
  does declare exactly `glm` (default) and `claude`, with implement
  sonnet → opus and review/closeout on opus (lines 113-196).
- **The no-token refusal.** `checkSelectedConfigCanRoute`
  (`runconfig_select.go:278-300`) refuses at run start and names `wrangler
  secret put CLAUDE_SUB_TOKEN_<LABEL>` as the fix; doctor and the cloud
  submission preflight read the same report
  (`internal/cli/config_preflight.go:43-49`,
  `internal/cli/cloud_harness_preflight.go:184`).
- **The operator surfaces.** `GET /api/claude-sub` and `POST
  /api/claude-sub/unbench/<LABEL>` exist (`claude-sub.ts:509-548`,
  `index.ts:1813`), `GET /api/deployment` carries `claude_sub_labels`
  (`index.ts:258-263`), and both are behind the factory bearer token: auth
  runs before routing (`index.ts:1528-1541`) and neither path is run-scoped.
  `ticfac factory status` and `doctor` do read the labels
  (`internal/factory/deploy.go:465-469`, `internal/factory/status_test.go:468`).
- **Nothing secret is published.** The doc names no token, account id or
  factory URL. A2 holds: `README.md:278` names the doc, in the same
  backtick-path form the README's only other `docs/` reference uses
  (`README.md:9`).
- **A4 holds.** Tick `56z`'s container reported `harness claude exited 0` on
  model `sonnet` — a step-down would have overridden the harness to the
  deployment's `RUN_WORKER_HARNESS` (`pi-durable`,
  `cloudflare/wrangler.toml:343`), not run claude at all. And this review job
  itself carries `TICKS_CLAUDE_SUB=1` in its process environment, which
  `worker-boot.ts:659` sets only when `input.claude_sub` is present, i.e. only
  when the lease succeeded. Both rungs of the run leased a subscription.

## What is wrong

The failover section — the part an operator reads to answer "how long is my
subscription out of action, and will my job keep the same one" — is wrong in
three places against `claude-sub.ts`, the very file A1 names. Details in the
findings block below; in summary:

1. `docs/claude-sub-operator.md:82` says a quota bench with no usable reset
   header lands "one second out". It is 60 seconds
   (`THROTTLE_COOLDOWN_MS`); one second is only a floor.
2. `docs/claude-sub-operator.md:83-84` says a non-quota 429 benches "only
   briefly (60 seconds)". When the answer carries `retry-after`, the bench is
   that instead, which can be far longer.
3. `docs/claude-sub-operator.md:73-74` says a job holding a lease gets the
   same subscription back "on every later ask". Only within `LEASE_TTL_MS`
   (2 hours).

Each is a single-sentence fix in one file. I am calling the epic not ready on
them because A1 is not "the doc covers these topics" — it is "every claim
checked against" three named files, and these three claims contradict one of
them. A reviewer who waves through demonstrably false numbers in a document
whose acceptance criterion is per-claim accuracy is the rubber stamp this
review exists to prevent.

Three further findings are backlog, not reasons to block: a false parenthetical
about the repository having one Worker and one config, a stale docstring in
`claude-sub.ts` itself, and a real (pre-existing) gap between the lease TTL and
a worker's wall clock.

```findings v2
[
  {
    "kind": "defect",
    "title": "claude-sub guide's failover section misstates bench durations and lease TTL",
    "severity": "high",
    "body": "Three claims in docs/claude-sub-operator.md contradict cloudflare/src/claude-sub.ts, the file A1 requires them to be checked against. (1) Line 82 says a quota bench with neither a usable unified reset nor retry-after lands \"one second out\"; classifyAnswer computes `reset ?? retryAt ?? now + THROTTLE_COOLDOWN_MS` = 60 seconds, and `Math.max(until, now + 1000)` is only a floor, so an operator is told 1s where the pool means 60s. (2) Lines 83-84 say a 429 that is not a quota rejection benches \"only briefly (60 seconds)\"; the throttle arm returns `retryAt ?? now + THROTTLE_COOLDOWN_MS`, so an answer carrying retry-after benches for that instead, which may be far longer than a minute. (3) Lines 73-74 say a job that holds a lease gets the same subscription back \"on every later ask, benched or not\"; ClaudeSubPoolCore.lease only reuses a held lease while `now - held.at < LEASE_TTL_MS` (2 hours), after which the job is treated as new and may be given a different subscription. The same section also describes the quota test as the unified status (or a per-window variant) reading `rejected`, omitting that a 429 with no unified-status header but a parseable unified reset is also treated as quota, and that windows matching /overage/ are excluded. The guide's operator commands are all correct; it is the explanatory numbers in its most operationally load-bearing section that are not.",
    "breaks": {"item": "A1"},
    "evidence": "docs/claude-sub-operator.md:73-84 against cloudflare/src/claude-sub.ts:107,110,207-241,329-334"
  },
  {
    "kind": "defect",
    "title": "Guide says the repository has one Worker and one wrangler config",
    "severity": "medium",
    "body": "docs/claude-sub-operator.md:37-38 justifies omitting wrangler's -c flag with \"this repository has one Worker, one config\". The repository has several: cloudflare/wrangler.toml (ticks-factory), cloudflare/staging/wrangler.toml (ticks-factory-staging, which also binds CLAUDE_SUB_POOL and is where the rung was first proven), plus cloudflare/staging/agent.wrangler.toml and gateway.wrangler.toml. The command itself is right — from cloudflare/ the default config IS production — so no operator action goes wrong; the stated reason for it is simply false, and it tells an operator that the staging Worker they may also want a token on does not exist.",
    "evidence": "docs/claude-sub-operator.md:37-38 against cloudflare/staging/wrangler.toml:6,20"
  },
  {
    "kind": "defect",
    "title": "claude-sub.ts docstring still says production binds no pool",
    "severity": "medium",
    "body": "cloudflare/src/claude-sub.ts:30-32 states that the rung is off unless a deployment binds CLAUDE_SUB_POOL and exports ClaudeSubProxy, and that \"Production does none of these (only cloudflare/staging does)\". Production now does both: cloudflare/wrangler.toml:166 binds CLAUDE_SUB_POOL and cloudflare/src/index.ts:2006 exports ClaudeSubProxy. Pre-existing, not introduced by this epic, but it matters here specifically: it is the module the new operator guide tells its reader to check the guide against, and it contradicts the guide on whether the rung exists in production at all.",
    "evidence": "cloudflare/src/claude-sub.ts:30-32 against cloudflare/wrangler.toml:166 and cloudflare/src/index.ts:2006"
  },
  {
    "kind": "proposal",
    "title": "Lease TTL of 2h is shorter than a worker's 8h wall clock",
    "severity": "medium",
    "body": "LEASE_TTL_MS is 2 hours (cloudflare/src/claude-sub.ts:107), and its comment reasons about a review-epic job being well under it. A cloud implement worker's wall clock is 8 hours (TICKS_WORKER_TIMEOUT=28740; this run's own attempt record carries wall_seconds 28800), so a long implement job's lease stops counting in activeLeases while the job is still live and still billing the subscription. CLAUDE_SUB_MAX_CONCURRENT is then no longer the cap it reads as: a third job can be handed a subscription that already has two live jobs on it, which is exactly the quota-budget overrun the cap was chosen to prevent. Worth a tick to either raise the TTL above the worker wall clock or refresh the lease while the container lives. Not a reason this epic is not ready — it is pre-existing behaviour and no claim in the new doc depends on it.",
    "evidence": "cloudflare/src/claude-sub.ts:107,420-432 against .ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/attempts/1.json wall_seconds 28800"
  }
]
```

REVIEW-VERDICT: NOT READY — correct the three failover claims in docs/claude-sub-operator.md to match cloudflare/src/claude-sub.ts: the quota bench with no usable reset header is 60 seconds and not one second, a non-quota 429 benches for retry-after when the answer carries one rather than always 60 seconds, and a held lease is only sticky within its 2-hour LEASE_TTL_MS.

STATUS: DONE
