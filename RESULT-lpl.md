<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ilz/attempt-1/lpl`, base `f0361100656d48fc9e6d152bcadc4a7588c1b02f`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Re-review of epic ilz, round 4 — AS INTEGRATED

Reviewed `epic/ilz` at `1ff1dde4` (my checkout, `f0361100`, is its ancestor and
holds the same `docs/` and `README.md` bytes — `git diff HEAD origin/epic/ilz --
docs README.md` is empty) against the base the epic is cut from. `main` is fully
merged into the epic: `git merge-base origin/epic/ilz origin/main` is
`e684dfec`, which *is* `main`'s head, so the boundary diff is `e684dfec..epic/ilz`.

The container arrives at depth 1 (backlog tick `2x8`); `git fetch --depth=200
origin refs/heads/epic/ilz refs/heads/main` gave me the history this review
needs.

## What the epic integrates

Everything substantive, bookkeeping excluded:

```
README.md                             |   5 +
docs/claude-sub-operator.md           | 159 +++++++++++
docs/ilz-closeout-retro-2026-10-07.md | 316 ++++++++++++++
3 files changed, 480 insertions(+)
```

No Go, no TypeScript, no stray `RESULT-*.md` on the integrated tree
(`git ls-tree -r --name-only origin/epic/ilz` carries none). The guide names no
token value, account id or factory URL.

## The round-3 blocker is fixed, and I re-derived it rather than take it

Round 3 (`vnp`) judged NOT READY for three failover claims that went stale when
#250–#253 merged under the epic. `555253ba` ("docs: claude-sub guide's failover
section matches claude-sub.ts after #250-#253") answers all three, and each one
holds against `cloudflare/src/claude-sub.ts` as it stands at the integration
head:

- **the overage arm is in the quota bullet.** `docs/claude-sub-operator.md:95-100`
  — "**any** answer, a 200 included, marked overage-in-use … benched until the
  unified reset, else the overage reset, else 5 hours
  (`OVERAGE_BENCH_FALLBACK_MS`)" — is `classifyAnswer`'s overage arm verbatim:
  the check runs before the `status !== 429` return, and `until` is
  `parseReset(unified-reset) ?? parseReset(unified-overage-reset) ?? now +
  OVERAGE_BENCH_FALLBACK_MS` (`claude-sub.ts:180-181, 221, 361-370`).
- **the withheld answer is stated.** `docs/claude-sub-operator.md:98-100` says
  the answer is withheld and the job handed a synthesized 429 naming the reset;
  `claude-sub.ts:948-966` cancels the upstream body and returns a 429 carrying
  `anthropic-ratelimit-unified-reset` and `retry-after` from `bench.until`.
- **the stale-lease promise is replaced by the refresh.**
  `docs/claude-sub-operator.md:77-81` — the proxy refreshes at most once a
  minute (`LEASE_REFRESH_MS`) so `LEASE_TTL_MS` measures *silence* — matches
  `ClaudeSubPoolCore.touch` (`:550-555`), the proxy's throttled `pool.touch`
  (`:906-910`) and `activeLeases`' TTL drop (`:654`). The round-2 fix it
  supersedes (the 60s `THROTTLE_COOLDOWN_MS` fallback, `Math.max(…, now+1000)`
  as a floor only) survives intact at `:92-94`.

## Every other claim of the guide, checked against the cited source

Re-derived at the integration head, not taken on an earlier round's word:

| guide | source | verdict |
|---|---|---|
| `LABEL` is `^[A-Z0-9_]{1,32}$` (`:31`) | `claude-sub.ts:229` | holds |
| `normalizeToken` strips all whitespace (`:24-25`) | `:318-322` | holds |
| the pool reads secret names at every lease, nothing to redeploy (`:53-55`) | `ClaudeSubPool.lease` → `subscriptionLabels(this.env)` `:300-308, 678-684` | holds |
| 401/403 benches 24h and rotating does not clear it; use unbench (`:57-59`) | `AUTH_BENCH_MS` `:227`, bench keyed on the label `:581-585`, `unbench` `:588-590` | holds |
| lease keyed on the attempt's own job id, cap `CLAUDE_SUB_MAX_CONCURRENT` default 2, fewest live leases wins (`:66-71`) | `claudeSubLeaseForBoot` → `specJobID(spec)` (`sandbox-executor.ts:1897-1903`), `DEFAULT_MAX_CONCURRENT` `:191`, `lease` `:516-526` | holds |
| only inference answers judge the subscription; everything off the allowlist is a 403, never forwarded (`:82-86`) | `CLAUDE_SUB_ROUTES` `:118-128`, `allowedRoute` `:131-135`, the `kind === null` refusal `:869-874`, `bench = kind === "inference" ? classifyAnswer(…) : null` `:916-917` | holds |
| a non-quota 429 benches until `retry-after` else 60s (`:102-104`) | `:401-405` | holds |
| the window-rejection test excludes `/overage/`; a reset with no status is the quota (`:89-92`) | `:386-392` | holds |
| a mid-job quota stop is collected as infrastructure, same tier, no rung spent, and the feed says so (`:111-114`) | `jobQuota` → `claude_sub_quota` observation (`sandbox-dispatch.ts:798-819`), `claudeSubQuotaFault` (`exitclass.go:105-114`), `MidJob` arm (`reconcile/infrastructure.go:141-156`) | holds, with the bound omitted — finding 2 |
| step-down to the deployment's standing Workers AI pair for that one job on `none`/`exhausted`/`busy` (`:115-120`) | `LeaseOutcome` `:287-295`, `lease`'s `benchedCount === labels.length` arm `:527-533`, `claudeSubLeaseForBoot`'s override `:1909-1922` | holds |
| `--config` > the epic's `config:` label > the `[configs]` default; a resume keeps the first selection and a differing flag is refused (`:130-139`) | `selectRunConfig` `runconfig_select.go:204-228` | holds |
| a claude config with no subscription on the factory is refused at run start, at doctor and at the submission preflight, naming `wrangler secret put CLAUDE_SUB_TOKEN_<LABEL>` (`:139-144`) | `checkSelectedConfigCanRoute` `:278-299` | holds |
| `glm` default / `claude` = sonnet→opus implement, opus review and close-out (`:124-128`) | `.tick/runners.cloud.toml:113-198` | holds |
| `GET /api/claude-sub`, `POST /api/claude-sub/unbench/<LABEL>`, `GET /api/deployment`'s `claude_sub_labels`, both behind the factory bearer, never a token value (`:146-159`) | `claudeSubRoute` `:749-788`, `index.ts:263, 1813-1814`, and the pre-routing `authenticateFactoryRequest` for every non-exempt path `index.ts:1528-1541` | holds |

## The acceptance criteria

- **[A1] the guide exists and every claim checks out — MET.**
  `docs/claude-sub-operator.md`, 159 lines, covers token-making,
  add/list/rotate/remove with the `wrangler` commands against the production
  config, the label rule, failover, per-epic selection and observation. The
  table above is the claim-by-claim check at the integration head, including
  the three the merge broke. One parenthetical is still wrong and is already a
  filed backlog tick, not a new finding: `:37-38`'s "this repository has one
  Worker, one config" (`hbw`) — `cloudflare/staging/wrangler.toml` and
  `harness/wrangler.toml` also exist. The *command* it justifies (`cd
  cloudflare`, no `-c`) is right, which is why it is backlog.
- **[A2] README links it — MET.** `README.md:277-280`, in the factory/cloud
  section right after the deploy-secrets paragraph.
- **[A3] `make gate` passes — MET, run here on this tree.**
  `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` → exit 0; gofmt clean, `go vet
  ./...` clean, 53 Go packages `ok`/no-test-files, zero `FAIL`.
- **[A4] the run's workers ran on claude via the subscription rung — MET, by
  the records on the branch.** Every worker dispatch resolved a subscription-rung
  pair on the cloud executor: `attempts/1.json` and `3.json` are `model: sonnet`
  / `role: implement-tick`, `2.json`, `4.json` and `7.json` are `opus` /
  `review-epic`, `5-6.json` `opus` / `closeout-epic`, all
  `executor: cloudflare-sandbox`; and this review's own
  `run_18eafdaa…/attempts/1.json` is `model: opus`, `role: review-epic`. A
  stepped-down boot is a *different* pair — `claudeSubLeaseForBoot` overrides
  harness and model with `RUN_WORKER_HARNESS`/`RUN_WORKER_MODEL`, the
  deployment's Workers AI standing choices (`sandbox-executor.ts:1909-1922`) —
  so a lease, not a step-down, is what these records are consistent with. The
  lease's own label is not on an attempt record, which is the already-filed
  `hyw`; the retro's second proof (this container's `TICKS_CLAUDE_SUB=1`, which
  only `claudeSubProcessEnv()` sets and only a leased boot spreads) is sound and
  I confirmed `claudeSubProcessEnv` sets it at `claude-sub.ts:458-465`.

## The tests

The epic's diff is prose only, so no test exercises it — and that is the honest
state rather than a gap this review can close inside the epic. Two things make
it acceptable here:

- the behaviour the guide now describes *is* pinned, by tests that arrived with
  #250–#253 rather than with this epic: `cloudflare/test/claude-sub.test.ts`
  drives `touch`/`LEASE_REFRESH_MS` (`:298-320, 570`), the overage-in-use bench
  and its `false` case (`:370-381`), the withheld-answer path (`:521`) and the
  `overage-status: rejected` org (`:97-102`);
- the real gap — nothing re-opens a prose claim when a merge rewrites the file
  it was checked against, which is exactly how this epic needed four review
  rounds — is already the filed backlog tick `n2u`, and the retro's own
  learnings compaction folds the rule in. I am not re-filing it.

What I would not accept is a claim in the guide that no source backs; the table
above is my check that there is none.

## Findings

Two, neither a reason the epic is not ready. My tick's notes list no deferred
findings.

```findings v2
[
  {
    "kind": "defect",
    "title": "Close-out retro declares [A1] broken on a head where it is fixed",
    "severity": "medium",
    "body": "docs/ilz-closeout-retro-2026-10-07.md ships on the same tree as the fix that answers it: it marks [A1] \"MET AS WRITTEN AND REVIEWED, BROKEN ON THE HEAD THAT SHIPS\", lists the three failover claims as \"now wrong on the integration head\", and closes with \"Two things nothing has answered … the three stale guide claims above, which break [A1] on the head that merges to main\". 555253ba corrected all three before this head, so the integrated tree carries a durable record contradicting its own deliverable. Its supporting numbers moved with the fix too: the contribution stat block says 2 files / 148 insertions against merge base 4cf4d59 (now 3 files / 480 insertions against e684dfec), the guide is 159 lines rather than 143, and its citations of guide lines 72-80, 81-93 and 94-96 point at text the fix rewrote. One dated line saying the claims were corrected in 555253ba would settle it.",
    "evidence": "docs/ilz-closeout-retro-2026-10-07.md:64-83 and :135-139, against 555253ba and docs/claude-sub-operator.md:72-114"
  },
  {
    "kind": "defect",
    "title": "Failover section omits the three-redispatch bound on a quota stop",
    "severity": "low",
    "body": "The guide tells an operator that a claude-sub job stopped by its own quota answer \"is redispatched at the same tier rather than climbing the ladder\", with no bound. reconcile's MidJob arm takes from the same counter as every other infrastructure failure, maxInfrastructureRedispatches = 3, and the fourth in a row refuses the run (RefusedInfrastructure) with \"The run stops here; no rung of the ladder was spent\". An operator whose single subscription is spent sees the run stop after three redispatches and has nothing in the guide to read it against.",
    "evidence": "docs/claude-sub-operator.md:111-114 against internal/reconcile/infrastructure.go:86 and :141-156"
  }
]
```

## Judgement

The epic said it would ship the operator documentation the `claude-sub` rung
went live without, link it from the README, keep `make gate` green, and run its
own workers on the subscription rung. As integrated it does all four. The one
thing that made it NOT READY three rounds running — explanatory numbers in the
failover section drifting from `cloudflare/src/claude-sub.ts` — is fixed and
re-derived above against the head that ships, and the two findings I am left
with are a stale close-out record and one omitted bound, neither of which makes
the guide wrong about anything an operator types.

REVIEW-VERDICT: READY

STATUS: DONE — reviewed epic/ilz at 1ff1dde4 against its base e684dfec, claim by claim against the cited sources; ran `make gate` (exit 0); verdict READY with two non-blocking findings (one medium, one low).
