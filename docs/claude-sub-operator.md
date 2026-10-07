# The claude-sub rung: operator guide

`claude-sub` lets a cloud run bill Claude work to the operator's own Claude
subscription (Claude Max) instead of running every container on Workers AI.
This is the operator's reference for making a token, adding/rotating/removing
it, how failover behaves, how a run selects it, and how to see the pool's
state. Every claim below is checked against `cloudflare/src/claude-sub.ts`,
`cloudflare/src/sandbox-executor.ts` and
`internal/reconcile/runconfig_select.go`.

Nothing here is a secret or an identifier: labels are operator-chosen names
(e.g. `MAX1`), never token values, account ids or URLs.

## Making a token

On the subscription's own account, run:

```
claude setup-token
```

This is interactive and prints an OAuth token for the subscription. It wraps
the token across terminal lines — paste the whole thing; a line break pasted
*inside* the token is still accepted (`normalizeToken` in `claude-sub.ts`
strips all whitespace before use), but copying only part of a wrapped line
is not.

## Adding, rotating, removing a subscription

A subscription is one Worker secret, named `CLAUDE_SUB_TOKEN_<LABEL>`. `LABEL`
must match `^[A-Z0-9_]{1,32}$` — uppercase letters, digits and underscores,
up to 32 characters (`cloudflare/src/claude-sub.ts`). Nothing else in the
factory ever reads the secret, no log prints it, and it never reaches a
container's environment, argv or disk — see "How failover works" below for
what a container gets instead.

From `cloudflare/`, against the production wrangler config (no `-c` flag —
this repository has one Worker, one config):

```
cd cloudflare
pnpm exec wrangler secret put CLAUDE_SUB_TOKEN_MAX1
# paste the token from `claude setup-token` when prompted
```

- **Add**: `wrangler secret put CLAUDE_SUB_TOKEN_<LABEL>` with a new label.
- **Rotate**: `wrangler secret put CLAUDE_SUB_TOKEN_<LABEL>` again, same
  label — the new value replaces the old one.
- **Remove**: `pnpm exec wrangler secret delete CLAUDE_SUB_TOKEN_<LABEL>`.
- **List**: `pnpm exec wrangler secret list` — names only; wrangler never
  prints a secret's value.

Nothing needs to be redeployed for any of this: the pool reads the
`CLAUDE_SUB_TOKEN_*` secret names off the live environment at every lease
(`subscriptionLabels` in `claude-sub.ts`), not at deploy time.

A token that Anthropic refuses (401/403) is benched for 24 hours (see below)
regardless of whether it is rotated in that window — rotating it does not
clear the bench. Use the unbench action (below) after rotating.

## How failover behaves

One Durable Object, `ClaudeSubPool`, holds every subscription's state:
active leases, benches and last-seen rate-limit headers.

- **Lease at job start.** When a dispatch resolves to the subscription rung
  (harness `claude` on the versionless alias `sonnet` or `opus` —
  `isSubscriptionRung`), the boot leases a subscription keyed on the
  attempt's own job id, under a per-subscription concurrency cap
  (`CLAUDE_SUB_MAX_CONCURRENT`, default 2). The lease picks the *usable*
  subscription (not benched, under its cap) with the fewest live leases.
- **Sticky per attempt job id, kept alive by traffic.** A lease is sticky:
  a job that already holds a lease gets the same subscription back on every
  later ask, benched or not, because its container's outbound interception
  is bound to that subscription's label the moment it is installed and
  cannot be swapped mid-flight. **A running job never switches
  subscription.** The proxy refreshes the job's lease as its requests pass,
  at most once a minute (`LEASE_REFRESH_MS`), so `LEASE_TTL_MS` (2 hours)
  measures *silence*, not job length: only a lease whose job has sent
  nothing for 2 hours is reclaimed (`ClaudeSubPoolCore.lease` and the
  proxy's refresh in `claude-sub.ts`).
- **Only inference answers judge the subscription.** The proxy forwards an
  allowlist of routes (`CLAUDE_SUB_ROUTES`) and refuses everything else with
  a 403, never forwarding it. Of what it forwards, only `/v1/messages*`
  answers are classified (`classifyAnswer`); a startup read's status is that
  endpoint's own, never the subscription's.
- **Bench on the quota, until the unified reset.** Two answers mean the
  subscription's quota is spent:
  - a 429 whose `anthropic-ratelimit-unified-status` (or a per-window
    variant other than `overage`) says `rejected`, or that carries a
    parseable unified reset with no status — benched until
    `anthropic-ratelimit-unified-reset`, else `retry-after`, else 60 seconds
    (`THROTTLE_COOLDOWN_MS`; never less than one second, which is only a
    floor);
  - **any** answer, a 200 included, marked overage-in-use (the window is
    spent and Anthropic served the request on usage credits, per token) —
    benched until the unified reset, else the overage reset, else 5 hours
    (`OVERAGE_BENCH_FALLBACK_MS`). That answer is **withheld**: the job is
    handed a synthesized 429 naming the reset instead, so the cloud never
    draws per-token billing past the first such answer.

  A 429 that is *not* the quota (server-side throttling) benches the
  subscription until `retry-after` if the answer carries one, else 60
  seconds. A 401/403 on an inference route (the token itself is refused)
  benches it for 24 hours — until someone rotates it.
- **The retry leases another subscription.** Benching never blocks the job
  that triggered it: that job's own answer (its 429 or its error) is
  returned to the container, and it is the *next* lease — a retry, or a
  different job — that is routed around the benched subscription, to
  whichever other configured subscription is usable.
  A claude-sub job that stopped because its own answer hit the quota is
  collected as infrastructure, not as a failed try: the tick is redispatched
  at the same tier rather than climbing the ladder, and the run feed says the
  subscription's quota ran out under it.
- **Workers AI when none is free.** If every configured subscription is
  benched (`exhausted`) or at its concurrency cap (`busy`), or none is
  configured at all (`none`), the lease fails and the boot falls back to
  the deployment's standing Workers AI pair for *that one job* — never a
  queue, never a wait on the subscription. The next dispatch asks the pool
  again from scratch.

## Selecting the config per epic

`claude-sub` is reached only through a **named run config** that routes the
`claude` harness on `sonnet`/`opus` — it is never the cloud's default
routing. This repository declares two in `.tick/runners.cloud.toml`:
`glm` (Workers AI, the default) and `claude` (the subscription rung,
implement climbing sonnet → opus, review and close-out on opus).

One precedence, resolved once per run (`internal/reconcile/runconfig_select.go`):

1. `--config <name>` on the command line (a person's word for this one run).
2. The epic's own `config:<name>` label (the design's word for every run of
   that epic) — e.g. `config:claude`.
3. The `[configs]` default the runners files declare (the repository's
   standing answer).

A run keeps whatever config it first selected across a resume; `--config`
with a different name on a resume is refused. Selecting a config that routes
the subscription rung with **no subscription token configured on the
factory** is refused at run start (and at `ticfac doctor`, and at the cloud
submission preflight) rather than silently stepping every dispatch down to
Workers AI — the message names the fix: `wrangler secret put
CLAUDE_SUB_TOKEN_<LABEL>`.

## Observing the pool

Two surfaces, both behind the factory's own operator bearer token (never a
run's credential):

- `GET /api/claude-sub` — every configured subscription's label, its active
  leases, its bench (if any) and the last `anthropic-ratelimit-unified-*`
  headers seen — never a token value. `POST
  /api/claude-sub/unbench/<LABEL>` clears a bench early, for use right after
  rotating a token that was benched on a 401/403.
- `GET /api/deployment` carries `claude_sub_labels` — just the configured
  labels, with the rest of the deployment's facts. This is what `ticfac
  factory status` and `ticfac doctor` read to report the rung's state from
  the operator's own machine.
