<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ilz/attempt-6/kzs`, base `e147ce8fe624696a853192d05552d0478a276eea`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Close-out of epic ilz — the claude-sub operator guide

Epic `ilz`: "Operator guide for claude on the subscription (first claude-config
cloud run)" — the smoke run of the `claude` config in the cloud, deliberately
small, whose own workers were to ride the subscription rung. Written from the
integration branch `epic/ilz` (head `23d7a1a`, substantive head `bfedc21`), the
run's records under `.ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/`, and
the ticks' own reports. Not from the plan.

The durable record is committed at `docs/ilz-closeout-retro-2026-10-07.md`,
which carries the learnings compaction as its appendix. This report is the
answer and the findings.

## What it set out to deliver, and what it delivered

The deliverable was the operator documentation `v5t` shipped the rung without.
It shipped exactly that and nothing else. Measured against the merge base of
`main` and `epic/ilz` (`4cf4d59`), the whole substantive diff is two files:

```
README.md                   |   5 ++
docs/claude-sub-operator.md | 143 ++++++++++++++++++++++++++++++++++++++++++++
2 files changed, 148 insertions(+)
```

No Go, no TypeScript, nothing under `.tick/`, and no stray `RESULT-*.md` on the
integrated tree (`git ls-tree --name-only origin/epic/ilz | grep -i result` is
empty). The guide names no URL, no account id and no token value. Everything
else on the branch is the run's own records and the tracker's.

### Against the acceptance criteria

**[A1] the guide, every claim checked against `cloudflare/src/claude-sub.ts`,
`cloudflare/src/sandbox-executor.ts` and `internal/reconcile/runconfig_select.go`
— met as written and reviewed, and BROKEN ON THE HEAD THAT SHIPS.** Two review
rounds read the document claim by claim; round 1 (`yaz`) returned NOT READY on
four wrong claims in the failover section, `5gm` fixed them, and round 2 (`0n3`)
re-checked the whole document and passed it at `e834273`. Then `bfedc21` merged
`main` into `epic/ilz`, carrying PRs #250–#253 and **+503 lines of
`cloudflare/src/claude-sub.ts`** — the first file [A1] names — and nothing
re-read the guide against it. Three claims in its failover section are now wrong
on the integration head. I found these by re-reading the guide against the
post-merge file, not by trusting either review:

1. **The quota arm omits `overageInUse` entirely.** Any answer, *a 200
   included*, carrying `anthropic-ratelimit-unified-overage-in-use: true` is now
   a `quota` bench until the unified or overage reset, falling back to
   `OVERAGE_BENCH_FALLBACK_MS` = 5 hours (`claude-sub.ts:180-181,220-221,
   361-369`). The guide's quota test (lines 81-93) is 429-shaped only, so an
   operator looking at `/api/claude-sub` and seeing a five-hour quota bench with
   no 429 anywhere has nothing in the guide that explains it — and nothing that
   tells them the cloud refuses per-token overage billing by design.
2. **"Benching never blocks the job that triggered it: that job's own answer …
   is returned to the container"** (lines 94-96) is false for exactly that case.
   The proxy cancels the upstream body and returns a *synthesized* 429 instead
   (`claude-sub.ts:947-971`); `ClaudeSubProxy`'s own docstring now reads
   "unchanged (but for an overage answer, which is withheld)". A job handed a
   200 it never sees, stopping on a 429 Anthropic never sent, is the opposite of
   what the guide promises.
3. **The stickiness sentence** (lines 72-80) measures `LEASE_TTL_MS` from "when
   it was first taken" and promises, in bold, that a lease stale past two hours
   is treated as new on its next ask. The proxy now refreshes a live job's lease
   at most once a minute (`LEASE_REFRESH_MS`, `claude-sub.ts:208,550-555,906`)
   and the TTL's own comment says it "measures SILENCE, not the job's length".
   This is the very sentence round 1 made `5gm` write.

Everything else re-reads as the guide says, re-derived here rather than taken on
the review's word: the `LABEL` regex and `subscriptionLabels` reading the env at
every lease (`claude-sub.ts:229,300-308`), `normalizeToken` stripping whitespace
(`:318-322`), `DEFAULT_MAX_CONCURRENT` = 2 (`:191`), `AUTH_BENCH_MS` 24h
(`:227`), the `/overage/` window exclusion and the
`unified === "" && reset !== null` quota case (`:382-393`), the lease's
fewest-live choice and its `exhausted`/`busy`/`none` outcomes (`:505-535`), the
step-down overriding the boot pair (`sandbox-executor.ts:1884-1923`), both
observation surfaces (`index.ts:263,1813-1814` and `claude-sub.ts:749-788`), and
the config precedence, which the merge does not touch at all
(`runconfig_select.go`). The commands an operator types are all correct; it is
the explanatory half of the most load-bearing section that rotted.

**[A2] README links it — met.** `README.md:277-280`, in the factory/cloud
section, right after the deploy-secrets paragraph.

**[A3] `make gate` passes — met, on the post-merge head.** The integrated gate
ran four times and passed every time; the last pair is keyed to `bfedc21`, the
head carrying the `main` merge: `evidence/gate-0n3-4-go.json` and
`gate-0n3-4-ts.json`, both `exit_code: 0`, `result: pass`. CI on the epic PR
#254 is green per the run's checkpoint at sequence 37 (I cannot read it
directly: there is no `gh` in this container). What that does **not** cover is
worth stating, because it is why [A1] rotted under a green gate: the gate's
TypeScript half is contracts and types, not behaviour, and no gate reads prose.

**[A4] the run's workers ran on claude via the subscription rung — met, and
directly readable rather than inferred.** The item named "the run's events",
which is a Worker `console.log`; the final review called it unverifiable and
handed it here. Two independent reads answer it:

- **This container's own environment carries `TICKS_CLAUDE_SUB=1`.** That
  variable exists in exactly one place, `claudeSubProcessEnv()`
  (`claude-sub.ts:458-464`), and `worker-boot.ts:668` spreads it **only** when
  the boot holds a lease (`input.claude_sub === undefined ? {} : …`). A failed
  lease returns no `claude_sub` and overrides the pair with the deployment's
  standing Workers AI rung (`sandbox-executor.ts:1884-1923`), so the variable
  cannot be set on a stepped-down job. This close-out holds a lease.
- **Every worker report's container-facts line reads `harness claude` exited 0**
  — `7f27fdc8f:RESULT-56z.md`, `9a223db9b:RESULT-yaz.md`, `e65624d7b:RESULT-5gm.md`,
  `e67cc316f:RESULT-0n3.md` (read the annotated commit, not the agent's own: the
  facts line is prepended after the harness exits). All four resolved `claude`
  on a versionless alias, which is `isSubscriptionRung` true.

The gap the item fell into is already closed on `main` by this epic's own merge:
`claudeSubNote` — whose docstring opens "finding: the step-down's reason was a
console line only" — now puts `claude_sub: {state, label, reason, retry_at}` on
the handle and into the run feed as `StageClaudeSubSteppedDown`.

I could not read the live pool to corroborate any of it: no factory is
configured in this container (`ticfac factory status` → "No factory is
configured"), which is also why no labels or leases are quoted here.

## How the run went

Six dispatches, 06:35 → 08:18 UTC on 2026-10-07.

| # | tick | role | model | outcome |
|---|---|---|---|---|
| 1 | `56z` | implement-tick | sonnet | the guide and the README link; merged, gate green. Taken over from run `run_d2359e14`, whose checkpoint read `failed` |
| 2 | `yaz` | review-epic | opus | **NOT READY** — four claims in the failover section contradict `claude-sub.ts` |
| 3 | `5gm` | implement-tick | sonnet | the four claims corrected; merged, gate green |
| 4 | `0n3` | review-epic | opus | **READY**, two non-blocking findings |
| 5 | `kzs` | closeout-epic | opus | **rejected, `no-commits`** |
| 6 | `kzs` | closeout-epic | opus | this close-out |

**Attempt 5 is the run's own lesson, and I am not repeating it.** It analysed
the `.tick/` wall correctly, put its learnings compaction in its report, and
committed nothing; the collect refused it with "a report is not a deliverable"
(checkpoint sequence 31). The rule is recorded: for `closeout-epic`,
`NoCommitsIsFailure` is true, because "its deliverable is the record it leaves
in the repository — the retro, and the learnings the boundary permits it to
write" (`internal/exec/subprocess/rolecommits.go:32-35`). The repository already
had the answer in two precedents — `docs/umq-closeout-retro-2026-10-06.md` and
`docs/v5t-closeout-retro-2026-10-07.md` — and attempt 5 used neither. This
close-out commits the retro there and carries the compaction as its appendix.

One boundary attempt in the whole run, in `56z`: the agent ran `tk tree ilz`,
the guard refused it, and the report carries the banner. `tree` is not a
subcommand `tk 0.32.0` has, so the call could not have written anything, and
nothing under `.tick/` reached that branch. That is tick `am6`.

### What the run absorbed, and on what basis

Ten findings, from `absorptions/`. One was absorbed into the epic: `5gm`, the
high-severity "failover section misstates bench durations and lease TTL". Its
two-stage record reads oddly and is worth stating: the run first filed it as an
owned backlog tick at 07:11 — the epic's implementation ticks were all closed,
and a finding the epic's work did not need is not absorbed on the reporter's
word once that work is done — then absorbed it two minutes later because the
same review returned NOT READY and named it blocking. One judgement, two
records. It was fixed inside the epic and re-reviewed, which is the right
outcome. The other nine are owned backlog ticks on the `backlog-default` basis:
medium or low, with no done item named. None was absorbed on
`worker-asserted-high`.

There is no `amendments/` directory for this run and the epic's tracker record
carries no notes, so **no worker claimed an exception to any acceptance item** —
nothing here is awaiting an operator's confirmation.

## What is left open

Nine backlog ticks the run already promoted. I read each against the current
head and re-file none of them, but two have moved and a person should know:

- **`9sy`** (high) — the container's pre-commit hook (`image/worker.sh:534`) and
  `worker-collect.ts:313,530` refuse the `.tick/learnings.md` that
  `subprocess.OutsideBoundary` exempts (`report.go:193,223-239`), so a cloud
  close-out cannot write the file its role names. Verified still true in this
  container: the hook is installed and refuses it. `report.go:197-204`'s own
  comment records that this drift already cost one whole close-out five
  attempts; it has now cost a second one a dispatch. Note for whoever takes it:
  the Go collect that actually judges a cloudflare-sandbox attempt *does* honour
  the exemption (`cloudflaresandbox/collect.go:127`), so the hook's stated
  reason — "refused wholesale by collect" — is false for this one path, and
  `boundary_attempts()` (`image/worker.sh:580-583`) is a third site that needs
  the same list.
- **`hbw`** (medium) — the guide says "this repository has one Worker, one
  config"; `cloudflare/staging/wrangler.toml` also binds `CLAUDE_SUB_POOL`. The
  command it justifies is right, so no operator action goes wrong.
- **`gv3`** (medium) — `claude-sub.ts:30-32` still says "Production does none of
  these"; `wrangler.toml:166` binds the pool and `index.ts:2006` exports the
  proxy. Re-verified wrong at the integration head, and it matters here because
  that file is the one the guide tells its reader to check the guide against.
- **`z38`** (medium) — the guide names `CLAUDE_SUB_MAX_CONCURRENT` but not
  `[configs.claude.tier_policy.concurrency]` (economy 2, strong 1), which is
  what actually bounds how many claude workers this repository runs at once.
- **`2x8`** (medium) — review and close-out containers get a depth-1 checkout,
  unannounced. True here too (`.git/shallow` present); `git fetch --unshallow
  origin` opened it in one command, which is how this close-out read the diff
  and the other attempts' branches at all.
- **`am6`** (low) — the boundary banner calls any refused `tk` invocation an
  attempted write.
- **`hyw`** (low) — no run-level attempt record names the subscription a lease
  billed. **Its remedy site is not where the tick points:** the merge added
  `claude_sub` to the executor's own record
  (`cloudflaresandbox/record.go:210,214-221`), but
  `.ticfac/runs/*/attempts/*.json`'s `job_handle` is `attemptHandle`, written
  **before** the dispatch by construction (`reconcile/dispatch.go:46-57`), so it
  cannot carry a lease label. Whoever takes `hyw` should read that first.
- **`55q`** (medium) — **obsolete, close it rather than work it.** It asked for a
  lease TTL above a worker's 8h wall clock; `LEASE_REFRESH_MS` on `main` is
  precisely that fix, and `LEASE_TTL_MS`'s comment now reasons about silence
  rather than job length.
- **`k40`** (low) — **premise moved.** It asked for a test pinning stale-lease
  reassignment; with the refresh, a live job's lease no longer goes stale, so
  what wants pinning is the refresh, and the guide should stop promising the
  reassignment (finding 1 below).

Three findings of my own are in the block at the end. The first is the one that
matters: the epic's own definition of done is broken on the head that merges to
`main`, by a merge the run performed between the review and this close-out.

## What was learned

Three lessons, compacted into `.tick/learnings.md` and committed as Appendix A
of `docs/ilz-closeout-retro-2026-10-07.md` rather than into the file itself —
the hook refuses the path, see `9sy`. The compaction is the whole file, 150
lines, the hard cap exactly: each lesson folds into an existing entry, and it
pays for itself by merging the two pi/GLM gateway entries, folding the
redirect-test gotcha in beside them, moving the lone "Waiting and watching"
entry into Orchestration, and merging the tracker-state entry into the boundary
entry.

**Planning an epic. Problem:** ilz's [A4] named "the run's *events* show the
claude-sub lease" — a Worker `console.log`, which its own final review declared
unverifiable and handed to the close-out. **Cause:** the item named a log line
rather than a record, and nobody followed the one side effect that persists: a
step-down overrides the boot's harness/model pair, and the container prints that
pair into every report. The proof was durable all along, and `main` has since
added a handle field that answers it outright. **Rule:** an acceptance item
about a RUN names the PERSISTED record that answers it — a handle field, an
attempt record — never a log line.

**Reviews and repairs. Problem:** `56z` reported every claim checked against its
source; round 1 found four wrong in the one load-bearing section (a `??` chain's
fallback read as its `Math.max` floor, 1s for 60s; an unconditional 60s where
the code returns `retry-after`; an unbounded stickiness claim). Those were
fixed, round 2 passed the document — and then `main` merged under it and three
claims went stale again, under a green gate every time. **Cause:** prose
transcribing an expression loses which term is the default and which the floor,
and a document's only check is a reading, so a merge that rewrites the cited
file silently invalidates work no test covers. **Rule:** a CLAIM, like a tree
state, is accepted on the INTEGRATED head and not the branch; a doc quotes the
expression behind each number it states, so the review checks a transcription
rather than a paraphrase; and a merge of `main` into the integration branch
re-opens every prose claim about a file it touched.

**Orchestration and boundaries. Problem:** attempt 5 burnt a dispatch on
`no-commits` because the one repository write its role names is refused by the
container hook — while the collect that judged it allows that exact path, and
the repository already had two precedents for the document a close-out commits
instead. **Cause:** one boundary with four enforcers (the Go reader, the hook,
the sweep's reporting, `worker-collect.ts`) of which only the first reads the
exemption list, and a role whose deliverable was never written down where its
agent would find it. **Rule:** every enforcer of a boundary reads
`ExemptFromBoundary()`; a tick lives where its code is AND its worker can write,
checked at planning; and a close-out COMMITS its learnings.

## What I ran

- `gofmt -l . | grep -v '^contracts/' | (! grep .)` — clean.
- `go vet ./...` — exit 0 (`GOFLAGS=-p=2`, `nice`, on this shared host).
- **Not** the short suite. This close-out adds one markdown file and changes no
  Go and no TypeScript; the integrated gate's go and ts records are exit 0 on
  `bfedc21`, the head that ships; and no test in the tree enumerates `docs/` (the
  three that read a doc name it: `internal/cli/runbook_fault_evidence_test.go`
  and kin). That is the gap, stated rather than implied.
- `ticfac factory status` — "No factory is configured", so no live pool read.
- I reopened no implementation and edited nothing the epic delivered. The two
  files I write are this report and the retro record.

```findings v2
[
  {
    "kind": "defect",
    "title": "Guide's failover claims went stale when main merged under the epic",
    "severity": "high",
    "body": "bfedc21 merged main into epic/ilz after round 2 passed docs/claude-sub-operator.md claim by claim at e834273, rewriting cloudflare/src/claude-sub.ts by 503 lines — the first file [A1] requires every claim checked against — and nothing re-read the guide. Three claims in its failover section are now wrong on the head that merges to main. (1) The quota arm omits overageInUse: any answer, a 200 included, carrying anthropic-ratelimit-unified-overage-in-use: true is a quota bench until the unified or overage reset, falling back to OVERAGE_BENCH_FALLBACK_MS = 5 hours (claude-sub.ts:180-181,220-221,361-369), while the guide's quota test at lines 81-93 is 429-shaped only — an operator seeing a five-hour quota bench with no 429 has nothing to read. (2) Lines 94-96 say benching never blocks the job that triggered it and its own answer is returned to the container; for an overage answer the proxy cancels the upstream body and returns a synthesized 429 instead (claude-sub.ts:947-971), and ClaudeSubProxy's docstring now says 'unchanged (but for an overage answer, which is withheld)'. (3) Lines 72-80 measure LEASE_TTL_MS from when the lease was first taken and promise in bold that a lease stale past two hours is treated as new; the proxy now refreshes a live job's lease once a minute (LEASE_REFRESH_MS, claude-sub.ts:208,550-555,906) and the TTL's comment says it measures SILENCE, not the job's length. The operator commands are all correct; one re-read of the file fixes all three, and it should also take the overage arm into the quota bullet rather than leaving it unmentioned.",
    "breaks": {"item": "A1", "check": "git diff e834273c8d32 bfedc21c168d -- cloudflare/src/claude-sub.ts"},
    "evidence": "docs/claude-sub-operator.md:72-104 against cloudflare/src/claude-sub.ts:180-181,220-221,361-369,550-555,906,947-977"
  },
  {
    "kind": "proposal",
    "title": "Nothing re-opens a prose claim when a merge rewrites its cited source",
    "severity": "medium",
    "body": "ilz's whole deliverable was prose whose acceptance is 'every claim checked against' three named source files. The run then merged main into the integration branch, rewriting the first of those files by 503 lines, re-ran the integrated gate (go and ts, both exit 0 on bfedc21) and opened the epic PR — and the guide was never re-read, because no check in this repository reads a document. The gate's ts half is contracts and types by design, so this is not a gap in the gate but a gap in what follows a merge. Worth a tick: when an epic's acceptance names source files a deliverable is checked against, a merge into the integration branch that touches one of them re-opens the check — as a declared close-out step, as a review round, or as a tracked list of (doc, cited file) pairs something can diff.",
    "evidence": ".ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/evidence/gate-0n3-4-go.json (exit 0 on bfedc21) against the 503-line claude-sub.ts rewrite that merge carried"
  },
  {
    "kind": "proposal",
    "title": "Close 55q and rescope k40: LEASE_REFRESH_MS already landed on main",
    "severity": "low",
    "body": "Two backlog ticks this run filed were overtaken by the main merge it then took. 55q asks for a lease TTL above a cloud worker's 8h wall clock; LEASE_REFRESH_MS (claude-sub.ts:208) is exactly that fix — the proxy refreshes a live job's lease once a minute and LEASE_TTL_MS's comment now reasons about silence rather than job length — so 55q is obsolete and wants closing, not working. k40 asks for a test pinning the stale-lease reassignment the guide promises; with the refresh, a live job's lease no longer goes stale, so the behaviour worth pinning is the refresh and the guide should stop promising the reassignment (see the high finding). Both are cheap tracker hygiene, and both will otherwise be picked up and worked against the code as it was a day ago.",
    "evidence": "cloudflare/src/claude-sub.ts:193-208,550-555,906 against .tick/issues/55q.json and .tick/issues/k40.json"
  }
]
```

STATUS: DONE_WITH_CONCERNS — the retro is delivered and committed at `docs/ilz-closeout-retro-2026-10-07.md`, [A2] [A3] [A4] are met and [A4] is now verifiable from the records rather than a log; but [A1] is broken on the head that merges to main, because the run merged main under the guide after its last review and nothing re-read it (first finding, high, breaks A1), and the learnings compaction rides in that document's appendix instead of `.tick/learnings.md` because the container hook refuses the path the write boundary exempts (tick `9sy`).
