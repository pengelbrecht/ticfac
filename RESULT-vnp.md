<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ilz/attempt-7/vnp`, base `d56dedb8dba985927fd50624dd182e383af8902d`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Re-review of epic ilz, round 3, at the integration head

Epic `ilz` set out to ship one thing: `docs/claude-sub-operator.md`, an
operator guide to the claude-sub rung whose **every claim is checked against
`cloudflare/src/claude-sub.ts`, `cloudflare/src/sandbox-executor.ts` and
`internal/reconcile/runconfig_select.go`** ([A1]), linked from the README
([A2]), with `make gate` green ([A3]), the run's own workers riding the
subscription rung ([A4]).

Round 2 (`0n3`, decision 4) judged it READY at `72a61b88`. After that the run
merged `main` into `epic/ilz` (`bfedc21`) and ran the close-out twice. I was
dispatched because the tree moved, and the run lands on my verdict.

## What I read

The checkout arrives shallow (`.git/shallow`, one commit); `git fetch
--unshallow origin` restored the history, so I had a real boundary diff rather
than the integrated tree alone (this is backlog tick `2x8`).

The epic's whole substantive contribution against the merge base of `main` and
this branch (`4cf4d59`):

```
README.md                             |   5 +
docs/claude-sub-operator.md           | 143 +++++
docs/ilz-closeout-retro-2026-10-07.md | 316 +++++
```

No Go, no TypeScript, no stray `RESULT-*.md` on the integrated tree, no
`.tick/learnings.md` commit (refused by the container's boundary — tick `9sy`;
the compaction rides the retro's Appendix A instead). I then read the guide
claim by claim against the three files [A1] names, as they stand at this head —
not as they stood when round 2 passed them.

## The deferred finding, decided

My tick's notes carry no DEFERRED FINDINGS block, but the close-out (`kzs`
try 2) reported one high-severity finding naming a done item it breaks, which
the run deferred to me as backlog tick `xe9`: *"Guide's failover claims went
stale when main merged under the epic"*. I re-derived all three of its claims
from the files themselves. **All three hold, and together they are a reason
this epic is not ready**, because the prose IS the deliverable and [A1] is
exactly the promise that the prose survives a reading of `claude-sub.ts`:

1. **The quota arm is 429-shaped only, and a whole arm is missing.** The guide
   (`docs/claude-sub-operator.md:81-93`) defines a quota bench as a *429* whose
   unified status (or a non-`overage` per-window variant) reads `rejected`, or
   whose unified status is absent beside a parseable reset. That half is exact
   (`claude-sub.ts:382-399`). What it omits is `overageInUse`: **any** answer, a
   200 included, carrying `anthropic-ratelimit-unified-overage-in-use: true` is
   a quota bench until the unified or overage reset, falling back to
   `OVERAGE_BENCH_FALLBACK_MS` — **five hours**, not the "60 seconds out if
   neither is usable" the guide names (`claude-sub.ts:180-182,221,361-369`). An
   operator reading `GET /api/claude-sub` and finding `reason: "quota"`, five
   hours out, beside `last_status: 200` has nothing in the guide to read, and
   the fallback number the guide does give is wrong by a factor of 300 for that
   arm.
2. **"Benching never blocks the job that triggered it" is false in exactly
   that case.** The guide (`:94-98`) promises that the triggering job's own
   answer — "its 429 or its error" — is returned to the container and only the
   *next* lease is routed around the bench. For an overage answer the proxy
   cancels the upstream body and returns a *synthesized* 429 carrying its own
   `retry-after`, so the CLI stops (`claude-sub.ts:948-971`); `ClaudeSubProxy`'s
   own docstring now reads "unchanged (but for an overage answer, which is
   withheld)" (`:975-979`). A job whose request **succeeded** on usage credits
   is handed a failure it never received from Anthropic — the one case where
   benching does block the job that caused it, and the guide says it cannot
   happen.
3. **The bolded stickiness promise was falsified by the lease refresh.** The
   guide (`:72-80`) says the 2-hour `LEASE_TTL_MS` runs from "when it was first
   taken" and promises, in bold, that "a job whose lease goes stale past 2 hours
   is treated as new on its next ask and may be handed a different one".
   `ClaudeSubPoolCore.lease` compares `now - held.at` (`claude-sub.ts:508`), and
   `touch` rewrites `held.at` as the job's traffic passes, at most once a minute
   (`:208,547-556`, called from the proxy at `:906`). So the TTL measures
   **silence**, not the job's length — `LEASE_TTL_MS`'s own comment says so in
   those words (`:193-201`) — and a live job's lease never goes stale at all.
   The bolded sentence is the one an operator would plan a rotation or a quota
   post-mortem around, and it describes behaviour this code no longer has.

This is not a pre-existing wart the epic inherited: the integration head is
what merges to `main`, and on that head the guide's most load-bearing section
misdescribes behaviour an operator would act on. One re-read of
`cloudflare/src/claude-sub.ts` against the failover section closes all three,
and should fold the overage arm into the quota bullet rather than leave it
unmentioned.

## What else I checked, and found right

Re-derived from the files at this head, not taken on an earlier round's word:

- `LABEL` = `^[A-Z0-9_]{1,32}$` and `subscriptionLabels` reading the live env's
  secret *names* at every lease, so nothing needs redeploying
  (`claude-sub.ts:229,300-308,681,707`); `normalizeToken` stripping all
  whitespace, so a wrapped paste survives (`:318`).
- The add/rotate/remove/list commands, and that wrangler never prints a value.
- `DEFAULT_MAX_CONCURRENT` = 2 (`:191`); the lease picking the unbenched,
  under-cap subscription with the fewest live leases, and its
  `exhausted`/`busy`/`none` outcomes (`:505-535`); the step-down to the
  deployment's standing Workers AI pair for that one job, never a queue
  (`sandbox-executor.ts:1884-1922`).
- `AUTH_BENCH_MS` 24h, and that a bench is keyed by label so rotating does not
  clear it — hence the unbench action (`:227,588,766-783`).
- Both observation surfaces, verbatim: `GET /api/claude-sub` and `POST
  /api/claude-sub/unbench/<LABEL>` (POST enforced, unknown label 404'd) at
  `claude-sub.ts:749-788`, and `claude_sub_labels` on `GET /api/deployment` at
  `index.ts:263`.
- The config precedence — `--config` > the epic's `config:` label > the
  `[configs]` default, resolved once per run, kept across a resume and refused
  under a different name (`internal/reconcile/runconfig_select.go:17-27,
  159-212`) — and the two configs `.tick/runners.cloud.toml` declares, `glm`
  (default) and `claude` (implement sonnet → opus, review and close-out opus).
  The merge does not touch this file, and the guide is right about it.
- The guide names no URL, no account id and no token value. Nor does the retro.

**[A2] MET** — `README.md:277-280`, in the factory/cloud section.

**[A3] MET** — I ran `make gate` on this tree myself: exit 0, 47 packages `ok`,
gofmt and `go vet` clean.

**[A4] MET, directly** — this review container's own environment carries
`TICKS_CLAUDE_SUB=1`, which exists only in the process environment
`worker-boot.ts` spreads when the boot *held* a claude-sub lease; a stepped-down
job cannot have it. Every worker report's container-facts line reads `harness
claude exited 0`.

## The tests

The epic adds no code, so it owes no test — but the test question here is sharper
than usual and worth stating as the failure this review exists to catch: **the
gate cannot see this epic's deliverable at all.** `make gate` is gofmt, `go vet`
and the short Go suite; the TypeScript half is contracts and types. Nothing in
this repository reads a document. That is precisely how [A1] passed green twice
on a head whose cited source had been rewritten by 503 lines underneath it. It
is not a hole in the gate so much as a hole in what follows a merge, and it is
already tracked as backlog tick `n2u` — I am not re-filing it, but it is the
reason a NOT READY here could not have been caught by any check the run ran.

The claude-sub code's own tests are main's, not this epic's; `k40` (untested
stale-lease reassignment) and `55q` (lease TTL vs the worker wall clock) were
both overtaken by `LEASE_REFRESH_MS` landing in the merge, as tick `adn` says.

## The other backlog findings

I re-read the nine open backlog ticks this run promoted (`9sy`, `hbw`, `gv3`,
`z38`, `2x8`, `am6`, `hyw`, `55q`, `k40`, plus `n2u` and `adn`). None of them is
a reason this epic is not ready, and I am not re-filing any: each is either
about machinery this epic did not set out to change, or an aside whose
operator-facing instruction is still correct. `9sy` is high severity and real,
but it is the container boundary's defect, not the guide's, and no acceptance
item of this epic depends on it.

## My own findings

Two, both backlog, both omissions in sections [A1] names rather than false
statements, and both separate from the blocking one.

```findings v2
[
  {
    "kind": "defect",
    "title": "Guide's failover claims went stale when main merged under the epic",
    "severity": "high",
    "body": "Three claims in docs/claude-sub-operator.md's failover section are wrong on the integration head, after bfedc21 merged main and rewrote cloudflare/src/claude-sub.ts — the first file [A1] requires every claim checked against — with nothing re-reading the guide. (1) The quota test at lines 81-93 is 429-shaped only and omits overageInUse: any answer, a 200 included, carrying anthropic-ratelimit-unified-overage-in-use: true is a quota bench until the unified or overage reset, falling back to OVERAGE_BENCH_FALLBACK_MS = 5 hours, not the 60 seconds the guide names (claude-sub.ts:180-182,221,361-369). (2) Lines 94-98 promise benching never blocks the triggering job and that its own answer reaches the container; for an overage answer the proxy cancels the upstream body and returns a synthesized 429 instead, so a request that SUCCEEDED on credits is handed a failure (claude-sub.ts:948-971, and ClaudeSubProxy's docstring at :975-979). (3) Lines 72-80 measure LEASE_TTL_MS from when the lease was first taken and promise in bold that a lease stale past two hours is treated as new; the proxy now refreshes a live job's lease once a minute (LEASE_REFRESH_MS, claude-sub.ts:208,547-556,906) and the TTL's own comment says it measures SILENCE, not the job's length, so a live job's lease never goes stale. The operator commands are all correct; one re-read of claude-sub.ts against the failover section fixes all three, and should fold the overage arm into the quota bullet rather than leave it unmentioned.",
    "breaks": {"item": "A1"},
    "evidence": "docs/claude-sub-operator.md:72-98 against cloudflare/src/claude-sub.ts:180-182,193-201,221,361-369,508,547-556,906,948-979"
  },
  {
    "kind": "defect",
    "title": "Guide's failover section never mentions the proxy's refusal arms",
    "severity": "medium",
    "body": "The guide describes failover as lease, bench, retry, step-down, and never mentions that the proxy is an allowlist that refuses before the token is attached: a non-allowlisted method+path, a per-token-billing beta (REFUSED_BETAS) or a request naming a non-claude model all come back 403 permission_error, and a leased label with no secret value comes back 503 (claude-sub.ts:860-901). It also says flatly that a 401/403 benches the subscription for 24 hours, where only INFERENCE answers are ever classified — a startup read's 401/403 benches nothing (claude-sub.ts:111-115,916-917). An operator whose container's claude CLI is failing on 403s reads a guide that says the only 403 in the rung is a refused token, and reaches for a rotation that will not help.",
    "evidence": "docs/claude-sub-operator.md:61-104 against cloudflare/src/claude-sub.ts:111-128,142,860-901,916-917"
  },
  {
    "kind": "defect",
    "title": "Observing section omits the per-run view of the rung",
    "severity": "medium",
    "body": "A1 requires the guide to cover observing the pool, checked against sandbox-executor.ts among others, and the two HTTP surfaces it names are correct — but they show the POOL, never which rung a given attempt actually ran on. claudeSubNote (sandbox-executor.ts:1925-1952) puts claude_sub: {state, label} or {state: stepped_down, reason, retry_at} on the job handle and into the run feed, which is what surfaces 'claude-sub stepped down: every subscription is exhausted until <reset>; on Workers AI' in ticfac watch. That is the answer to the first question an operator of this rung asks — did my run bill the subscription, or quietly step down — and the guide does not point at it. One bullet in Observing the pool closes it.",
    "evidence": "docs/claude-sub-operator.md:130-143 against cloudflare/src/sandbox-executor.ts:1903-1952"
  }
]
```

## Judgement

[A2], [A3] and [A4] are met, and I verified each on this tree rather than
reading them off an earlier round. [A1] is not met on the head that merges to
`main`. The epic's single deliverable is prose whose acceptance is that every
claim survives a reading of three named files; the run then merged `main`
underneath it, rewriting the first of those files, and three claims in the
guide's most operationally load-bearing section now describe behaviour the code
does not have — a missing quota arm with a five-hour bench, a promise that
benching never blocks the triggering job in the one case where it does, and a
bolded lease-staleness promise the refresh falsified. An operator following the
guide today would mis-diagnose both a spent window and a dead worker. The
close-out saw this and said so; I confirm it from the files.

One re-read of `cloudflare/src/claude-sub.ts` against
`docs/claude-sub-operator.md:72-98` makes the epic ready. Nothing else does.

REVIEW-VERDICT: NOT READY — correct the three stale failover claims in docs/claude-sub-operator.md against cloudflare/src/claude-sub.ts as it stands at the integration head: take the overageInUse arm (any status, including 200, benched until the unified or overage reset and 5 hours otherwise) into the quota bullet, say that an overage answer is withheld and the job handed a synthesized 429, and replace the "first taken"/2-hour staleness promise with the lease refresh (LEASE_REFRESH_MS) that makes LEASE_TTL_MS measure silence.

STATUS: DONE
