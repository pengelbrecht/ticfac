# Re-review of epic ilz — AS INTEGRATED (the round after `lpl`'s READY)

Reviewed my checkout `0821a25d` — `origin/epic/ilz` is `44c8817`, and the four
commits between them touch only `.tick/` and `.ticfac/` (`git diff
0821a25d origin/epic/ilz -- . ':(exclude).tick' ':(exclude).ticfac'` is empty),
so the tree that ships is the one I read. `main` is fully merged under the epic:
`git merge-base origin/epic/ilz origin/main` is `e684dfec`, which is `main`'s
head, so the boundary diff is `e684dfec..HEAD`.

The container arrives at depth 1 (backlog tick `2x8` — `git log` saw exactly one
commit); `git fetch --unshallow origin` gave me the history this review needs.

## What the epic integrates

Everything substantive, the two runs' bookkeeping and the tracker excluded:

```
README.md                             |   5 +
docs/claude-sub-operator.md           | 159 +++++++++++
docs/ilz-closeout-retro-2026-10-07.md | 370 +++++++++++++++
3 files changed, 534 insertions(+)
```

No Go, no TypeScript, no stray `RESULT-*.md` on the integrated tree
(`git ls-tree -r --name-only HEAD | grep -i result` matches only
`internal/reconcile/roleresult.go` and its test). The guide names no token
value, account id or factory URL.

## What changed since round 4's READY, and whether it holds

`lpl` judged READY at `f0361100`. The only non-bookkeeping change since is
`84cb108d` — the close-out retro rewritten — merged as `56fe01e1`:

```
docs/ilz-closeout-retro-2026-10-07.md | 374 +++++++++++------------
1 file changed, 214 insertions(+), 160 deletions(-)
```

`docs/claude-sub-operator.md` and `README.md` are byte-identical to what round 4
read. So this round is about the rewritten record, plus my own re-derivation of
the acceptance.

**The rewrite answers round 4's own medium finding (`3gw`) and does not
reintroduce it.** The previous record marked [A1] "MET AS WRITTEN AND REVIEWED,
BROKEN ON THE HEAD THAT SHIPS"; the new one opens by saying it *replaces* that
text, names `555253b` as having corrected all three claims before the head it is
written on, and marks [A1] **MET** (`docs/ilz-closeout-retro-2026-10-07.md:9-13`,
`:86-112`). Its re-measured numbers are the ones I measure: `README.md | 5` and
`docs/claude-sub-operator.md | 159` against merge base `e684dfec` (`:74-79`), and
the guide is 159 lines.

I spot-checked its citations rather than take them, because a close-out record is
itself a claim about the tree, and every one I checked resolves to what it says:

| retro cites | at this head |
|---|---|
| `781ff16`, the head it is written from | a commit, and `git merge-base --is-ancestor 781ff16 HEAD` is true |
| `claude-sub.ts:361-369` overage arm, `:221` `OVERAGE_BENCH_FALLBACK_MS` | both exact |
| `claude-sub.ts:948-966` the withheld answer | exact — cancels the body, returns a `rate_limit_error` 429 |
| `:208` `LEASE_REFRESH_MS`, `:553` `touch`, `:906` the proxy's refresh, `:196-197` "measures SILENCE" | all exact |
| `:229` `LABEL`, `:191` `DEFAULT_MAX_CONCURRENT`, `:227` `AUTH_BENCH_MS`, `:382-393`, `:401-404` | all exact |
| `claude-sub.ts:458-464` `claudeSubProcessEnv`, `worker-boot.ts:668` the conditional spread | both exact |
| `sandbox-executor.ts:1907-1921` the step-down, `:1933-1938` `claudeSubNote` | exact (`claudeSubLeaseForBoot` opens at `:1884`, not the `:1888` the same sentence cites — a four-line slip in a range, not a wrong claim) |
| `index.ts:2006` exports the proxy, `cloudflare/wrangler.toml:166` binds the pool, `staging/wrangler.toml:20` too | all exact |
| `internal/reconcile/infrastructure.go:86` `maxInfrastructureRedispatches = 3` | exact |
| `.tick/runners.cloud.toml:204-206` `[configs.claude.tier_policy.concurrency]` | exact |
| `report.go:193` the `.tick/learnings.md` exemption, `worker-collect.ts:313`, `image/worker.sh:596` | all exact |
| `cloudflare/test/claude-sub.test.ts:290` and `:566` pin the refresh twice | exact — the core's `touch` and the proxy's throttled call |
| Appendix A is 149 lines, inside the 150-line cap | 149; `.tick/learnings.md` is at 150 today |

Its open list (sixteen backlog ticks, not re-filed) matches the tracker, and the
five it marks as moved (`xe9`, `3gw`, `55q`, `k40`, `6a6`) read as it says.

## [A1] re-derived at this head, not taken on four rounds' word

The guide's claims against `cloudflare/src/claude-sub.ts`,
`cloudflare/src/sandbox-executor.ts` and
`internal/reconcile/runconfig_select.go`:

| guide | source | verdict |
|---|---|---|
| `:19` `claude setup-token`; a break pasted inside the token is tolerated | `normalizeToken` strips `\s+`, null when empty (`claude-sub.ts:318-322`) | holds |
| `:30-32` one secret `CLAUDE_SUB_TOKEN_<LABEL>`, `^[A-Z0-9_]{1,32}$` | `TOKEN_SECRET_PREFIX` `:50`, `LABEL` `:229` | holds |
| `:33-35` nothing else reads it, no log prints it, never in a container's env/argv/disk | the container gets `CLAUDE_SUB_PLACEHOLDER` (`:47`, `:458-464`); the proxy logs label/job/route only (`:866`, `:920-930`) | holds |
| `:46-51` add / rotate / remove / list by `wrangler secret put\|delete\|list` | the token is a Worker secret, and the label set is derived from secret *names* | holds |
| `:53-55` nothing redeployed; labels read off the live env at every lease | `ClaudeSubPool.lease` → `subscriptionLabels(this.env)` (`:678-684`, `:300-308`); the proxy reads `env[prefix+label]` per request (`:982-984`) | holds |
| `:57-59` 401/403 benches 24h, rotation does not clear it, use unbench | `AUTH_BENCH_MS` `:227`, `classifyAnswer` `:358-360`, bench keyed by label `:486`, `unbench` `:588-590` | holds |
| `:66-71` lease at job start on the attempt's job id, cap `CLAUDE_SUB_MAX_CONCURRENT` default 2, fewest live leases among usable | `claudeSubLeaseForBoot` (`sandbox-executor.ts:1884-1906`), `maxConcurrent` `:325-328`, `DEFAULT_MAX_CONCURRENT` `:191`, `lease` `:505-536` | holds |
| `:72-81` sticky per job id; interception bound at install, never swapped; TTL measures *silence*, refreshed at most once a minute | `lease`'s reuse arm `:507-510`, `installClaudeSubInterception` fixes props (`factory-sandbox.ts:1093-1096`), `touch` `:550-555`, the proxy's throttle `:906-910` | holds |
| `:82-86` an allowlist, everything else 403 and never forwarded; only `/v1/messages*` answers classified | `CLAUDE_SUB_ROUTES` `:118-128`, `allowedRoute` `:131-135`, the `kind === null` refusal `:869-874`, `kind === "inference"` gate `:916-917` | holds |
| `:87-94` quota = 429 with unified `rejected`, or a per-window `-status: rejected` other than `overage`, or a parseable reset with no status; until reset → `retry-after` → 60s, floored at 1s | `:372-400` verbatim, `THROTTLE_COOLDOWN_MS` `:224`, `Math.max(until, now+1000)` `:396` | holds |
| `:95-100` **any** answer incl. 200 with overage-in-use → quota, until unified reset → overage reset → 5h; the answer **withheld** as a synthesized 429 naming the reset | `overageInUse` `:180-182`, the arm `:361-369`, the withhold `:948-966` | holds |
| `:102-105` a non-quota 429 benches until `retry-after` else 60s; a 401/403 on an inference route 24h | `:401-405`, `:358-360` | holds |
| `:106-110` benching never blocks the triggering job; it is the *next* lease that routes around it | the proxy returns the upstream answer (`:967`), the bench is `waitUntil` bookkeeping (`:932-947`) | holds |
| `:111-114` a quota-stopped claude-sub job is collected as infrastructure, redispatched at the same tier, and the feed says the quota ran out | `recordJobQuota` `:563-572`, `collect.go:238`, `infrastructure.go:153-154` "the tick is dispatched again at the same tier" | holds (the bound of 3 is unstated — backlog `7o4`, below) |
| `:115-120` `exhausted` / `busy` / `none` → Workers AI for that one job, no queue, no wait; next dispatch asks again | `LeaseOutcome` `:287-295`, `lease`'s `best === null` arm `:527-533`, the step-down overriding the pair (`sandbox-executor.ts:1907-1921`) | holds |
| `:124-128` reached only through a named run config; this repo declares `glm` (default) and `claude` (implement sonnet→opus, review and close-out opus) | `.tick/runners.cloud.toml` `[configs] default = "glm"` and `[configs.claude.*]` | holds |
| `:130-137` flag > epic `config:` label > `[configs]` default, resolved once per run | `selectRunConfig` `:216-228`, `configLabelPrefix` `:52`, the doc rule `:18-29` | holds |
| `:138-139` a run keeps its config across a resume; a differing `--config` on resume is refused | `:204-214` | holds |
| `:139-144` a subscription-rung config with no token on the factory is refused at run start, at `doctor` and at the submission preflight, naming `wrangler secret put` | `checkSelectedConfigCanRoute` `:278-299` (message names the command), `internal/cli/doctor.go:435`, `internal/cli/cloud_harness_preflight.go:184` | holds |
| `:148-155` `GET /api/claude-sub` (labels, leases, bench, last unified headers, never a value) and `POST /api/claude-sub/unbench/<LABEL>`, both behind the factory bearer token | `claudeSubRoute` `:749-788`, mounted at `index.ts:1813-1814`; neither path is in `isAuthExempt` (`auth.ts:448-465`) | holds |
| `:156-159` `GET /api/deployment` carries `claude_sub_labels`; `ticfac factory status` and `doctor` read it | `index.ts:263`, `internal/factory/deployed.go:64`, `internal/factory/status.go:486-489`, `internal/cli/doctor.go:625-628` | holds |

Nothing in the guide contradicts the three files at this head. **[A1] MET.**

## The rest of the acceptance

- **[A2] MET.** `README.md:278`, in the factory/cloud section, links
  `docs/claude-sub-operator.md`.
- **[A3] MET, measured here rather than cited.** `nice -n 10 GOTEST_PARALLEL=4
  GOFLAGS=-p=2 make gate` at `0821a25d` — gofmt, `go vet ./...`, `go test -short
  ./...` — exit 0, every package `ok` (`internal/reconcile` 9.6s,
  `internal/exec/subprocess` 68.3s).
- **[A4] MET, and directly readable for this dispatch.** My own container
  environment carries `TICKS_CLAUDE_SUB=1` with no `ANTHROPIC_API_KEY` beside
  it. That variable exists only in `claudeSubProcessEnv()`
  (`claude-sub.ts:458-464`), which `worker-boot.ts:668` spreads **only** when
  the boot holds a lease (`input.claude_sub !== undefined`); a step-down returns
  no `claude_sub` and replaces the pair with the deployment's standing Workers
  AI rung (`sandbox-executor.ts:1907-1921`), so the marker cannot be present on
  a stepped-down job. This review is therefore itself a claude-sub lease, on
  `opus`, as the `claude` config declares for the review role.

## The tests, and what no test covers

The integrated diff is prose only — no Go, no TypeScript — so there is no test
this change could have added, and I am not going to credit a green suite with
exercising it. What the suite *does* cover is the behaviour the guide describes,
and I leaned on it only after re-reading the code: `cloudflare/test/claude-sub.test.ts`
pins the lease's stickiness, the cap, the `exhausted`/`busy`/`none` outcomes, the
refresh at `:290` and `:566`, `classifyAnswer`'s arms and the withheld overage
answer, and `internal/exec/cloudflaresandbox/claude_sub_e2e_test.go` drives the
real pool door. The gap is the one the retro itself names at `:120-121` — **no
gate reads prose**, which is how three verified claims rotted under four green
gates — and it is filed as backlog `n2u` ("nothing re-opens a prose claim when a
fold rewrites the file it was checked against"). I am not re-filing it: it is a
tracked orchestrator gap, not a defect in what this epic ships, and the epic's
own acceptance put the checking on a reader by design.

I could not read CI from this container (`gh` is not installed and the
container holds no credential for it), so I asked GitHub's public check-runs
API directly: `56fe01e1` — the last commit on the branch that changes anything
outside `.tick/`/`.ticfac/`, i.e. the one carrying the retro rewrite — is green
on all eleven checks (`go`, the three `reconcile` shards, `go test (packages)`,
`go race`, `go lint`, `typescript`, `contracts`, `plan`, `release config`). The
commits after it have no check runs because they change only tracker and run
state.

## Findings

None. My tick's notes list no deferred findings.

Every gap I found while re-deriving the guide is already a backlog tick this
epic filed and the retro re-verified — `6a6` (the guide omits `REFUSED_BETAS`, a
non-claude model, and a leased label whose secret was deleted), `3za` (the
Observing section names the two HTTP surfaces but not the per-run `claude_sub`
field in the feed), `z38` (`[configs.claude.tier_policy.concurrency]` unnamed
beside `CLAUDE_SUB_MAX_CONCURRENT`), `hbw` ("one Worker, one config", while
`staging/wrangler.toml:20` also binds the pool — the command it justifies is
still right), `gv3` (`claude-sub.ts:30-32` still claims "Production does none of
these" where `wrangler.toml:166` binds the pool and `index.ts:2006` exports the
proxy), `7o4` (the unstated bound of three infrastructure redispatches) and
`n2u`. Each is one or two sentences of coverage or a stale comment outside this
epic's diff; none contradicts anything an operator types; and re-filing them
would duplicate tracked work against this repository's own rule about deduping
findings.

```findings v2
[]
```

## Judgement

The epic said it would ship the operator documentation the `claude-sub` rung
went live without, link it from the README, keep `make gate` green, and run its
own workers on the subscription rung. As integrated it does all four, and I
verified each from the tree rather than from an earlier round: the guide's
claims hold line by line against the three files [A1] names, `README.md:278`
links it, `make gate` is exit 0 in this container at the shipping head, and this
very dispatch carries the claude-sub marker. The only change since round 4's
READY is the close-out record rewritten to stop contradicting the tree that
carries it — the one medium finding round 4 left — and the replacement's own
citations check out.

REVIEW-VERDICT: READY

STATUS: DONE — reviewed epic/ilz as integrated at 0821a25d against its base e684dfec; re-derived every guide claim against claude-sub.ts, sandbox-executor.ts and runconfig_select.go, checked the rewritten close-out record citation by citation, ran `make gate` (exit 0) and confirmed this dispatch's own claude-sub lease; verdict READY, no findings.
