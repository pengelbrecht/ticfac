# Spike jvj: cloud jobs on `claude -p` with a Claude subscription token

Tick `jvj` (claude-sub in cloud workers), 2026-10-06. Related: epic `umq`
(FactorySandbox on the `durable_object` scheduling policy, whose container
this rides on) and tick `nwn` (the cloud routing rule: Workers AI models
only).

**Recommendation: build a capped `claude-sub` rung for review and close-out
(sonnet → opus for implement later), at most 2 concurrent jobs per
subscription, stepping down to the Workers AI ladder when no subscription can
take the job.** The token path works on the real platform, and the token never
enters the container. One opus review of a real diff took about 7.5 minutes
and about 1% of the subscription's 5-hour window. Four in parallel ran with no
rate limit. Quota is what limits this, not concurrency. That quota is the
operator's own, shared with their interactive use, so the cap has to stay
small.

## Terms

Operator, 2026-10-06 (recorded on the tick): Anthropic's terms allow using the
subscription this way. The binding constraint is the subscription's usage
quota. Multi-subscription failover (operator, 2026-10-05) is in scope.

## How the token reaches Anthropic and never the container

```
container                                  Worker (staging today)
---------                                  ----------------------
claude -p  ──HTTPS api.anthropic.com──►  ctx.container.interceptOutboundHttps
  CLAUDE_CODE_OAUTH_TOKEN=<placeholder>      │  (installed BEFORE container.start)
  NODE_EXTRA_CA_CERTS=<ephemeral CF CA>      ▼
                                          ClaudeSubProxy (WorkerEntrypoint, ctx.exports)
                                            props = { label, jobId }   ← fixed per job
                                            Authorization: Bearer <env.CLAUDE_SUB_TOKEN_<label>>
                                            + anthropic-beta oauth-2025-04-20
                                            ──► https://api.anthropic.com
                                          ClaudeSubPool (one Durable Object)
                                            leases (cap per subscription), benches, stats
```

- **How the CLI authenticates headless.** With `CLAUDE_CODE_OAUTH_TOKEN` set,
  the claude CLI uses the subscription (OAuth) dialect by itself. It sends
  `Authorization: Bearer <value>` and the `oauth-2025-04-20` beta, and does
  not check the token locally. This was observed against a local echo server
  (CLI 2.1.288) and on staging (2.1.227). So the container gets a
  **placeholder**, and the proxy only swaps the bearer value.
  `apiKeyHelper` is not needed. `ANTHROPIC_API_KEY` must **not** be set
  alongside the placeholder, because an API key wins over OAuth.
- **Interception.** We drive `ctx.container` ourselves (FactorySandbox), not
  the Containers or Sandbox SDK classes. So we use the runtime primitives
  directly: `interceptOutboundHttp(host, fetcher)` and
  `interceptOutboundHttps(host, fetcher)`, where the fetcher is
  `ctx.exports.ClaudeSubProxy({ props })`. This is what
  `@cloudflare/containers`' `outboundByHost` does under the hood. HTTPS
  interception signs with an ephemeral CA that exists only at runtime, at
  `/etc/cloudflare/certs/cloudflare-containers-ca.crt`. The native (Bun) CLI
  binary accepts it through `NODE_EXTRA_CA_CERTS`. No `update-ca-certificates`
  and no image change are needed.
- **The token is a Worker secret.** It is never in the container's env, argv
  or disk. On staging, the env shows only the placeholder, and
  `grep -r sk-ant-` over /etc, /root, /workspace and /tmp finds nothing.
- **One token per job.** The props are fixed when the interception is
  installed, before `container.start`. FactorySandbox records the binding and
  refuses a second, different binding for a running container ("a job never
  switches tokens"). A Durable Object restart re-installs the same binding.
- **N tokens.** Each subscription has its own secret:
  `CLAUDE_SUB_TOKEN_<LABEL>`. The pool leases the usable subscription with the
  fewest live leases under `CLAUDE_SUB_MAX_CONCURRENT`. Leases are sticky per
  job id, and a 2-hour TTL backstops a job that is never released.
- **Exhaustion.** Every answer is classified by `classifyAnswer`:
  - 429 with the unified limiter **rejecting** (`anthropic-ratelimit-unified-status:
    rejected`, or a `-5h-`/`-7d-status: rejected`): the subscription is benched
    until `anthropic-ratelimit-unified-reset`.
  - Any other 429: throttling, benched for `retry-after` or 60 s.
  - 401/403: the token is refused, benched for 24 h until it is rotated.

  The answer goes back to the job unchanged. The job ends on its own 429, and
  the **next** lease steps down. A job is never moved to another token
  mid-flight.

Code: `cloudflare/src/claude-sub.ts` (proxy, pool, classifier),
`cloudflare/src/factory-sandbox.ts` (`claudeSub` boot option, the
interception install, the switch refusal), `cloudflare/src/staging.ts`
(`/s/<name>/start?sub=1`, `/pool`, `/pool/unbench/<label>`, `/pool/shape`),
`cloudflare/staging/` (the pool binding, and the CLI in the staging image).
Tests: `cloudflare/test/claude-sub.test.ts`, plus the claude-sub block in
`cloudflare/test/factory-sandbox.test.ts`.

**Off by default.** Production's `wrangler.toml` binds no `CLAUDE_SUB_POOL`,
its main module does not export `ClaudeSubProxy`, and it has no token secret.
A `claudeSub` boot there fails closed at boot ("needs ClaudeSubProxy
exported"). It can never run without a credential, and it never runs with one
nobody granted.

## Measurements (staging, 2026-10-06)

The job is the real `profiles/review-epic.md` prompt, plus a short "this job"
section. It reviews commit `782ccd5cb` (PR #241, 133 lines over 2 Go files)
with no tracker, using the commit message as the definition of done. It runs
`claude -p --model <alias> --permission-mode bypassPermissions --output-format
json` with `IS_SANDBOX=1` in a fresh `standard-3` FactorySandbox container
(Debian, the CLI at 2.1.227 = the factory image's pin, git). Clone time is
measured separately (about 5 s). "Cost" is the CLI's API-equivalent figure.
The subscription pays nothing per token.

| run | model (alias → id) | wall in container | turns | output tok | cache read | API-equiv $ | verdict | 429s |
|---|---|---|---|---|---|---|---|---|
| 1 job | `opus` → claude-opus-5 | 446 s | 31 | 26.3k | 0.95M | 1.70 | READY, 3 medium + 3 low findings | 0 |
| 1 job | `sonnet` → claude-sonnet-5 | 316 s | 35 | 22.1k | 2.33M | 1.47 | READY, 2 low proposals | 0 |
| 2 parallel | `opus` | 476 s / 429 s | 36 / 25 | 31.3k / 25.9k | 1.81M / 0.80M | 2.48 / 1.58 | READY / READY | 0 |
| 4 parallel | `opus` | 291 / 585 / 359 / 358 s | 28 / 57 / 30 / 34 | 19.2k / 33.6k / 23.6k / 23.9k | 0.82M / 3.24M / 1.14M / 1.77M | 1.35 / 3.19 / 1.71 / 2.05 | READY ×4 | 0 |

**Quota, from the unified headers on the proxy's last answer.** The 5-hour
window is the binding window: `representative-claim: five_hour`, and overage
is disabled for the org (`overage-status: rejected`,
`overage-disabled-reason: org_level_disabled`), so there is no paid spill-over.

- After 1 opus + 1 sonnet in a fresh 5-hour window: 5h utilization 0.04, 7d
  utilization 0.04.
- After 2 more opus: 5h 0.05, 7d 0.05.
- After 4 parallel opus: 5h utilization 0.12 (from 0.05), 7d utilization 0.06.
- In total: 8 review jobs (7 opus, 1 sonnet) and 272 proxied requests, with no 429.

The operator's own interactive use shares these windows, so a per-job figure
is approximate: about 1.5–2% of the 5-hour window per opus review of this size (4 parallel: +7 points for 4 jobs).

**Latency** is dominated by the model's turns (25–36 tool turns at roughly
13 s each), not by the path. The interception adds one Worker hop per request.
`duration_api_ms` is about 99.7% of `duration_ms`, so the hop is not visible.
Two in parallel ran no slower than one.

**Concurrency ceiling.** None was found up to 4. Four opus jobs on one
subscription at once got no 429 and no throttling (`limited: 0` over 272
requests). Their wall times (291–585 s) are in line with the single job. The
585 s job did 57 turns, and its `duration_api_ms` (502 s) trails its
`duration_ms` by 82 s, which is CLI-side time, not waiting on the limiter. I
stopped the ramp at 4 on purpose. The scope was 1/2/4, and the subscription is
the operator's own, so I did not push further to force a 429. The ceiling that
will bite is the **quota**, not a concurrency limit: 4 opus reviews moved the
5-hour window from 0.05 to 0.12, about 1.75% each. That puts roughly 55 such
reviews in an otherwise idle 5-hour window, and fewer while the operator is
working.

**What a refusal looks like (observed).**

- **Bad token** (the first real secret was pasted with a line break inside
  it): HTTP 401, CLI result `"Failed to authenticate. API Error: 401 OAuth
  access token is invalid."`, `api_error_status: 401`, exit 1 in about 2 s.
  The pool benched it as `auth`. Fix: the proxy now strips all whitespace
  from a token secret (`normalizeToken`). `claude setup-token` wraps the
  token across terminal lines, and the paste keeps the break.
- **Quota 429**: not reached in this spike (see the ceiling note). Its shape
  is the one the classifier reads: 429 with
  `anthropic-ratelimit-unified-status: rejected`, a unified reset (epoch
  seconds) and `retry-after`. Against a stub that answered exactly that, the
  CLI exited 1 within 80 ms with `api_error_status: 429` and `"API Error:
  Server is temporarily limiting requests (not your usage limit)"`, and
  `terminal_reason: api_error`. It did **not** retry a 3600 s retry-after.
  So a quota hit ends the job quickly and visibly. It does not stall it.

## Design: the `claude-sub` rung

1. **Models: aliases, never ids** (operator, 2026-10-06). The cloud claude
   ladder is `sonnet` → `opus`:
   - implement starts on `sonnet` and climbs to `opus`;
   - review-epic and close-out run on `opus`.

   Config names only the claude CLI's versionless aliases, so they always pick
   the latest model, and never a pinned model id. **Caveat found here:** the
   alias resolves inside the CLI binary, so "latest" means the latest **the
   pinned CLI knows**. CLI 2.1.227 (the factory image's `CLAUDE_CODE_VERSION`)
   maps `opus` to `claude-opus-5` and `sonnet` to `claude-sonnet-5`. CLI
   2.1.288 maps `opus` to `claude-opus-5-5`. So the aliases only stay current
   if the image's CLI pin is bumped. Make that bump routine (a dependency
   update), or install the CLI unpinned at image build.
2. **Cap: 2 concurrent jobs per subscription** (`CLAUDE_SUB_MAX_CONCURRENT=2`).
   This is not a platform limit: 4 parallel ran clean. It is a quota budget.
   At about 1–2% of the 5-hour window per opus review, 2 at a time cannot
   drain the window before the operator notices, and it leaves the operator
   interactive headroom.
3. **Lease at dispatch, release at collect.** The dispatcher leases with the
   job id when it boots a claude-sub worker. If the lease fails, the role is
   resolved at its Workers AI rung **for this job** (no wait, no stall). The
   lease outcome carries `reason` (`none` / `busy` / `exhausted`) and
   `retry_at`. Release happens when the attempt is collected. The TTL is only
   a backstop.
4. **Step-down on a quota answer mid-job.** The job fails fast with
   `api_error_status: 429`. The pool has already benched the subscription
   until its reset. The attempt's retry is dispatched normally, and its lease
   fails over to another subscription or to the Workers AI ladder. The retry
   is a new job, so this is not a token switch inside a job.
5. **Multi-subscription.** One secret per subscription. Labels name nothing
   private (`MAX1`, `MAX2`).
6. **Observability.** `/pool` (a factory route in production) shows leases,
   benches and the last unified headers. When every subscription is benched,
   the run feed should say "claude-sub exhausted until <reset>; on Workers AI".

### The one-place routing rule change (tick nwn)

Today `internal/profile/cloudworker.go`'s `CloudRule` admits a role in
Cloudflare only when its FINAL resolved worker is a Workers AI model on a
Workers AI harness (`enforceWorkersAI`). The claude-sub rung changes the rule
from "Workers AI models only" to "**no per-token billing in the cloud**". It
stays in one place:

```go
var CloudRule = struct{ ... }{
    ModelNamespaces: []string{"cloudflare-workers-ai/", "workers-ai/", "@cf/"},
    Harnesses:       []string{"pi", "pi-durable"},
    Executors:       []string{"cloudflare-sandbox"},
    // NEW: a subscription-billed rung. Admitted only as kind "claude" with a
    // versionless alias, and only when the deployment has a claude-sub pool
    // (the factory reports it); never an id, never ANTHROPIC_API_KEY.
    SubscriptionRungs: []SubscriptionRung{{Harness: "claude", Models: []string{"sonnet", "opus"}}},
}
```

`enforceWorkersAI` becomes `enforceCloudBilling`. A resolved worker passes when
it is a Workers AI worker (today's rule) **or** a subscription rung. Every
cloud cell that names a subscription rung must name a Workers AI fallback
cell beside it (the step-down target), and run start refuses a cell without
one. In `image/common.sh`, the `claude` kind gains a `claude-sub` route: it
exports the placeholder and `NODE_EXTRA_CA_CERTS`, and does **not** export
`ANTHROPIC_API_KEY`/`ANTHROPIC_BASE_URL`. The executor's
`IsWorkersAIModel(payload.Model)` check
(`internal/exec/cloudflaresandbox/executor.go`) moves to the same predicate.
These are follow-up ticks. This spike lands only the Worker side, off by
default.

## Follow-ups

- Routing rule (nwn successor): `enforceCloudBilling` plus subscription rungs,
  each with a mandatory Workers AI fallback cell.
- Factory wiring: bind `CLAUDE_SUB_POOL`, export `ClaudeSubProxy` from
  `src/index.ts`, lease in sandbox-dispatch, release at collect, add `/pool`
  to the status page and run feed.
- Image: `claude-sub` route in `image/common.sh`, and a routine bump of
  `CLAUDE_CODE_VERSION` so the aliases track the latest models.
- Measure the real quota 429 once it happens in use (shape, the reset, and
  whether `-5h-status: rejected` arrives before the overall status).

## Reproducing on staging

```
cd cloudflare
pnpm exec wrangler deploy -c staging/wrangler.toml
claude setup-token                                   # operator, interactive
pnpm exec wrangler secret put CLAUDE_SUB_TOKEN_MAX1 -c staging/wrangler.toml
# then, with the staging token:
POST /s/<name>/start?sub=1&cmd=<script>   GET /s/<name>/proc/<id>   GET /s/<name>/read/<id>
GET /pool   POST /pool/unbench/<label>   GET /pool/shape   POST /s/<name>/destroy
```
