# Close-out of epic ilz — the claude-sub operator guide

Epic `ilz`: "Operator guide for claude on the subscription (first claude-config
cloud run)" — the smoke run of the claude config in the cloud, deliberately
small, whose own workers were to ride the subscription rung. Integration branch
`epic/ilz` at `12d0a775`, cut from `30e61c2`.

## What it set out to deliver, and what it delivered

The deliverable was the operator documentation `v5t` shipped the rung without.
It shipped exactly that and nothing else. The whole substantive diff, base to
integration head, is two files:

```
README.md                   |   5 ++
docs/claude-sub-operator.md | 143 +++++++++++++++++++++
```

Everything else on the branch is the run's own records (`.ticfac/runs/…`) and
the tracker's (`.tick/issues`, `.tick/activity`). No Go, no TypeScript, no
stray `RESULT-*.md` on the integrated tree — I checked
(`git diff --name-only 30e61c2..origin/epic/ilz | grep -v '^\.tick'` returns
those two paths only).

The guide covers the six topics A1 names: making a token, add/rotate/remove/list
with the `wrangler secret` commands and the `LABEL` rules, how failover behaves,
how a run selects the config, and how to observe the pool. It names no URL, no
account id and no token.

### Against the acceptance criteria

**[A1] the guide, every claim checked against the three cited sources — met.**
Two review rounds read it claim by claim; round 1 (`yaz`) found four wrong
claims in one section and round 2 (`0n3`) re-checked the whole document and
tabulated eighteen claims as correct. I did not take that table on trust: I
re-derived the load-bearing half myself against `cloudflare/src/claude-sub.ts`
— the `LABEL` regex (`:115`), `subscriptionLabels` reading the env at every
lease (`:157-165`, `:447-453`), `normalizeToken` stripping whitespace
(`:175-179`), `DEFAULT_MAX_CONCURRENT = 2` (`:100`), `AUTH_BENCH_MS` 24h
(`:113`), the three arms of `classifyAnswer` (`:207-241`) including the
`unified === "" && reset !== null` quota case and the `/overage/` exclusion,
and `ClaudeSubPoolCore.lease`'s `now - held.at < LEASE_TTL_MS` (`:331-334`) —
plus `runconfig_select.go`'s precedence (`:204-213`), the no-subscription
refusal (`:298`) and its two companions in `internal/cli/doctor.go:435-440`
and `internal/cli/cloud_harness_preflight.go:184-188`, and the two observation
surfaces in `index.ts:258-263` and `:1813-1815`. Every one reads as the guide
says.

**[A2] README links it — met.** `README.md:277-280`, in the factory/cloud
section, right after the deploy-secrets paragraph.

**[A3] `make gate` passes — met, on the integrated head.** The integrated gate
ran twice and passed both times: `evidence/gate-5gm-3-go.json` and
`gate-5gm-3-ts.json`, exit 0 on the final integration sha `e834273`, and the
same pair on `f05bb24` for `56z`. The run's checkpoint records CI green on the
epic PR #254. I re-ran the free half here (`gofmt -l . | grep -v '^contracts/'`
— clean) and did not re-run the suite: this epic changes no Go and no
TypeScript, and the host is shared.

**[A4] the run's workers ran on claude via the subscription rung — met, and it
IS verifiable from the records.** The final review reported it could not confirm
this and handed it to the close-out, on the grounds that the lease/step-down
decision is only a Worker log line. It is confirmable from what the run
persisted, through the step-down's own side effect:

1. All four attempts resolved the `claude` harness on a versionless alias —
   `56z`/`5gm` on `sonnet`, `yaz`/`0n3` on `opus` (`attempts/1-4.json`), which
   is `isSubscriptionRung` (`claude-sub.ts:82`) true.
2. A failed lease does not merely log: `claudeSubLeaseForBoot` **overrides the
   pair** with the deployment's standing Workers AI rung and returns
   (`sandbox-executor.ts:1849-1858`), and that override happens before the boot
   env is built — the spread at `sandbox-executor.ts:1788` is what
   `worker-boot.ts:666` binds as `TICKS_HARNESS`.
3. The container reads that variable (`image/common.sh:119`) and prints it into
   the report's container-facts line (`image/worker.sh:1338`).
4. All four container-facts lines say **`harness claude` exited 0**:
   `7f27fdc8f:RESULT-56z.md`, `9a223db9b:RESULT-yaz.md`,
   `e65624d7b:RESULT-5gm.md`, `e67cc316f:RESULT-0n3.md`.

A step-down would have printed the deployment's standing harness there, not
`claude`; and `claude` on a Workers AI model dies `EXIT_MODEL` at
`image/common.sh:418` rather than running. So every dispatch of this run held a
claude-sub lease. (Read the annotated commit of each report, not the agent's
own first commit of it — the facts line is prepended after the harness exits.)

I could not read the pool's live state to corroborate it: no factory is
configured in this container (`ticfac factory status` → "No factory is
configured"), which is also why I am not quoting labels or leases.

## How the run went

Four dispatches, 06:35 → 07:50 UTC on 2026-10-07, two review rounds.

| # | tick | role | model | outcome |
| --- | --- | --- | --- | --- |
| 1 | `56z` | implement | sonnet | guide + README link, merged `f05bb24`, gate green |
| 2 | `yaz` | review | opus | NOT READY — three failover claims wrong |
| 3 | `5gm` | implement | sonnet | the three claims fixed (and a fourth omission), merged `e834273`, gate green |
| 4 | `0n3` | review | opus | READY |

One boundary attempt, in `56z`: the agent ran `tk tree ilz`, the guard refused
it and the report carries the banner. Nothing under `.tick/` reached that
branch. Worth knowing for the smoke-run record: `tree` is not a subcommand
`tk 0.32.0` has, so the attempt could not have written anything — see the
finding below about how the banner describes it.

### What the run absorbed, and on what basis

Six findings, from `absorptions/`:

- **`5gm`** — "failover section misstates bench durations and lease TTL",
  severity high, basis **`reviewer`**, gating, placement `before-review`. Its
  two-stage history is worth stating plainly, because the tracker alone reads
  oddly: the reporter rated it high and named A1, but every implementation tick
  was already closed, so the run first filed it as an owned backlog tick
  (07:11) under the rule that a finding the epic's work did not need is not
  absorbed on the reporter's word once that work is done — and then absorbed it
  two minutes later (07:13) because the same review returned NOT READY and named
  it blocking. One judgement, two records. It was fixed inside the epic and
  re-reviewed, which is the right outcome.
- **`hbw`, `gv3`, `55q`, `z38`, `k40`** — basis **`backlog-default`**, none
  gating: medium or low, with no done item named. Each is an owned backlog tick,
  listed below.

No finding was absorbed on `worker-asserted-high`. There is no `amendments/`
directory for this run and the epic's own tracker record carries no notes, so
**no worker claimed an exception to any acceptance item** — there is nothing
here awaiting an operator's confirmation.

## What is left open

Five findings the run already promoted to owned backlog ticks. I read each and
re-file none of them (they exist; a duplicate would be noise):

- **`hbw`** (medium) — `docs/claude-sub-operator.md:37-38` justifies omitting
  wrangler's `-c` with "this repository has one Worker, one config". False:
  `cloudflare/staging/wrangler.toml` exists and binds `CLAUDE_SUB_POOL` too.
  The instruction it justifies is right, so no operator action goes wrong.
- **`gv3`** (medium) — `claude-sub.ts:30-32` still says production binds no
  pool, while `cloudflare/wrangler.toml:166` binds it and `index.ts:2006`
  exports the proxy. It matters here because that file is the one the new guide
  tells its reader to check the guide against.
- **`55q`** (medium) — `LEASE_TTL_MS` is 2h and a cloud worker's wall clock is
  8h, so a long implement job's lease stops counting in `activeLeases` while it
  is still billing; `CLAUDE_SUB_MAX_CONCURRENT` is then not the cap it reads as.
  A real design question about the pool, not about the guide.
- **`z38`** (medium) — the guide names the factory's per-subscription cap but
  not `[configs.claude.tier_policy.concurrency]` (economy 2, strong 1), which is
  what actually bounds how many claude workers this repository runs at once.
- **`k40`** (low) — no case pins the stale-lease reassignment the corrected
  stickiness claim now promises an operator.

Four findings of my own are in the block at the end. The first is the one that
matters: **this close-out could not do the part of its job that writes to the
repository**, and the cause is a boundary enforced in three places that do not
agree.

## What was learned

I could not commit these into `.tick/learnings.md`. The Go reader exempts that
file for exactly this purpose (`internal/exec/subprocess/report.go:190`), and my
own prompt instructs me to compact learnings into it — but the container's
pre-commit hook refuses any staged `.tick/` path outright
(`image/worker.sh:534`), and the cloud collect turns any `.tick/` path into a
`boundary-violation` verdict that refuses the whole branch
(`cloudflare/src/worker-collect.ts:313,530`), which would take this report with
it. Bypassing the guard with `--no-verify` is not mine to do. So the compaction
is below, ready to apply by hand or by the next close-out that runs where the
guard is not installed. **Note that `.tick/learnings.md` is at its 150-line hard
cap, so applying these three means compacting three existing entries.**

**Planning an epic.** **Problem:** ilz's [A4] — "the run's events show the
claude-sub lease, not a step-down" — was declared unverifiable by its own final
review, which handed it to the close-out, because the lease decision is a Worker
`console.log` nobody can read afterwards. **Cause:** the acceptance item named
the log line rather than a record, and nobody followed the step-down's side
effect: it overrides the boot's harness/model pair, and the container prints
that pair into every report's container-facts line. The proof was persisted all
along. **Rule:** an acceptance item about a RUN names the persisted record that
answers it, never a Worker log; to tell a leased claude run from a stepped-down
one, read `harness` on the report's container-facts line — `claude` means the
lease was granted, the deployment's standing harness means it was not.

**Reviews and repairs.** **Problem:** `56z` reported "every statement in the doc
is checked against the cited source", and the review found four claims wrong in
the one operationally load-bearing section — a `??` chain's fallback swapped for
its `Math.max` floor (1s for 60s), an unconditional 60s where the code returns
`retry-after`, and an unbounded stickiness claim. **Cause:** prose transcribing
an expression loses which term is the default and which is the floor, and a
docs-only gate executes nothing, so no check but a reading exists — the suite
that pins those very numbers (`cloudflare/test/claude-sub.test.ts`) runs only in
CI's `typescript` job, which a docs-only PR never triggers. **Rule:** a document
stating a number that comes from code quotes the expression beside it, in the
doc or in the report, so the review checks a transcription rather than a
paraphrase; and plan a review round for prose-against-source, because the
reading IS the test.

**Boundaries this repo pays to learn.** **Problem:** the first cloud close-out
could not write the learnings file its own prompt points it at, and round 2's
reviewer declared the boundary diff unavailable in a depth-1 checkout that
`git fetch --unshallow origin` opens in one command (it did, here). **Cause:**
one boundary defined in three places — the Go reader exempts
`.tick/learnings.md`, the container hook and the TS collect refuse all of
`.tick/` — and a checkout whose depth nothing states to the agent whose job
needs the history. **Rule:** a boundary with more than one enforcer is tested
across every one of them, with `report.go`'s exemption list as the fixture; and
a review or close-out runs `git fetch --unshallow origin` before concluding it
cannot read the diff.

## What I ran

- `gofmt -l . | grep -v '^contracts/' | (! grep .)` — clean.
- `ticfac factory status` — "No factory is configured" (so no live pool read).
- No suite re-run: this close-out changes no code, the integrated gate's two
  evidence records are exit 0 on the final head, and the host is shared.
- I added no code and reopened nothing. The only file I write is this report.

```findings v2
[
  {
    "kind": "defect",
    "title": "Boundary guard refuses .tick/learnings.md the Go reader exempts",
    "severity": "high",
    "body": "The closeout-epic role is instructed to compact learnings into .tick/learnings.md, and internal/exec/subprocess/report.go:190 exempts that path from the tracker boundary for exactly that reason. Two other enforcers of the same boundary ignore the exemption: the container's pre-commit hook refuses any staged path under .tick/ (image/worker.sh:534), and the cloud collect makes any .tick/ path a boundary-violation verdict that refuses the whole branch (cloudflare/src/worker-collect.ts:313 and :530) — which would discard the close-out's report along with it. So a cloud close-out cannot perform the one part of its job that writes to the repository; this one did not. report.go:197-204's own comment records that this same drift already cost a whole close-out five attempts, and it was closed on the prompt side only. Fix: honour ExemptFromBoundary() in the hook and in worker-collect.ts, with report.go's list as the single fixture both are tested against.",
    "evidence": "image/worker.sh:534 and cloudflare/src/worker-collect.ts:313,530 against internal/exec/subprocess/report.go:190"
  },
  {
    "kind": "defect",
    "title": "Review and close-out containers get a depth-1 checkout, unannounced",
    "severity": "medium",
    "body": "The cloud checkout is a shallow clone with one commit and no other refs (.git/shallow is present; git rev-list --count HEAD = 1). Round 2 of this epic's review reported it therefore had no boundary diff and read only the integrated tree, while round 1 had compared against origin/epic/ilz and the merge base — the same role, two different pictures of what it could read. `git fetch --unshallow origin` restores the full history in one command; it did so in this close-out container. Nothing in the boot or the prompt tells a review or close-out agent that its checkout is shallow or that unshallowing is available, and image/worker.sh:1112-1126 shows the container already reasons about the shallow boundary for its own counts. Fix: fetch the depth these roles need at boot, or state the shallow checkout and the unshallow command in the roles' prompts.",
    "evidence": "RESULT-0n3.md \"What I could read, and what I could not\" against RESULT-yaz.md \"What I reviewed\"; image/worker.sh:1112-1126"
  },
  {
    "kind": "defect",
    "title": "Boundary banner reports any refused tk call as an attempted write",
    "severity": "low",
    "body": "The banner prepended to a report when the guard fires says, unconditionally, \"This agent tried to write tracker state\" (image/worker.sh:1303). Tick 56z's banner fired for `tk tree ilz` — `tree` is not a subcommand tk 0.32.0 has, so the call could not have written anything, and the guard's own comment at image/worker.sh:456-465 says what it refuses is a WRITE to this checkout's tracker \"and only that\". Every non-allowlisted invocation, including a typo and any read-only subcommand added to tk after the allowlist was written, is escalated to a human as misconduct. Fix: let the banner distinguish a refused unknown-or-read invocation from a staged .tick/ write, or pass an unknown subcommand through and let tk's own usage answer it.",
    "evidence": "image/worker.sh:1303 and the allowlist at image/worker.sh:481-482; the banner in RESULT-56z.md (commit 7f27fdc8f)"
  },
  {
    "kind": "proposal",
    "title": "Attempt records never name the subscription a claude-sub lease billed",
    "severity": "low",
    "body": "A dispatch's attempt record (.ticfac/runs/<run>/attempts/<n>.json) carries the executor, harness and model but not the claude-sub label the boot leased, though the boot knows it (sandbox-executor.ts:1788 carries claude_sub: {label, jobId}). An operator asking which subscription paid for which attempt — the question behind rotating or removing one, and behind any quota post-mortem — has no answer in the run's own records; today the rung is only inferable from the harness on the report's container-facts line, which says a lease was granted but not by whom. Recording the label on the attempt record would make that readable, and would make an A4-shaped acceptance item checkable directly instead of by inference.",
    "evidence": ".ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/attempts/1.json against cloudflare/src/sandbox-executor.ts:1788"
  }
]
```

STATUS: DONE_WITH_CONCERNS — the retro, the absorption account and the A4 verification are complete, but the learnings compaction is in this report instead of `.tick/learnings.md`: the container's pre-commit hook and the cloud collect refuse the path the Go reader exempts (first finding), so committing it would have refused this whole branch. Apply the three learnings by hand, compacting three existing entries to stay under the 150-line cap.
