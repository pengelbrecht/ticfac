<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-8/yjq`, base `f10404aba55ad81be7d8857b92b0a98b22d0581f`, harness `pi` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# RESULT-yjq — hn6's acceptance list parses as one item: A2–A6 are unaddressable

## What this tick is

yjq's fix is one field of the epic's own tracker record: `.tick/issues/hn6.json`'s
`acceptance_criteria`, re-flowed to one `[A<n>]` item per line. The tick's disposition
is explicit and this attempt did not second-guess it: "The record is the tracker's to
re-flow one [A<n>] item per line; no code change is needed."

## What I verified today

Nothing has changed since the last blocked answer. The field is byte-identical on this
worktree, origin/epic/hn6 (c6ae949) and origin/main: **0 newlines, 6 `[A<n>]` marks in
the text, 1 line-leading.**

I reproduced the defect on the live record with a scratch test (run and deleted; not
committed — a test asserting the defect is not a thing this tree should carry):

    go test -run TestYjqScratchRepro -v ./internal/acceptance/

    hn6 acceptance_criteria as it stands: 6 [A<n>] marks in the text, 1 line-leading, 1 physical line(s)
    Parse(current field) -> 1 item(s): [A1]
    Parse(re-flowed field) -> 6 item(s): [A1 A2 A3 A4 A5 A6]

The unaddressability is live inside the report pipeline too. A scratch finding carrying
`"breaks": {"item": "A2"}` (deleted before this report) is refused by the same reader
the run collects with:

    ticfac-exec-subprocess lint-report <scratch> --role implement-tick --tick yjq --repo /work/repo
    error: findings[0].breaks.item: "A2" is not an acceptance item of the epic; the epic's items are A1

So per-item gate binding, the findings channel and the final review can all currently
reach A1 only — exactly what yjq says.

## The fix, ready to apply

Replace the one-line `acceptance_criteria` of `.tick/issues/hn6.json` with the six lines
below (inside the JSON string they are joined by `\n`, no trailing newline; the words are
unchanged — a newline replaces each `; ` that separates two items, derived mechanically
from the live field by `strings.ReplaceAll(criteria, "; [A", "\n[A")`):

```
[A1] ticfac watch epic-<id> renders the fixed layout: header (progress bar, ticks n/m, elapsed, ETA, health verdict, phase bar, needs-you), a tick table whose rows never reorder with a per-tick pipeline cell, a workers panel with activity, CI and cost lines, and a two-line event tail
[A2] a held run shows the hold and its clearing command in the header; nothing-needs-you is shown when true
[A3] enter on a tick shows attempts with tier, verdict and reason, report summary, gate evidence, diff stats and findings; e opens the full feed
[A4] cost shows metered spend and says 'not metered' for unmeasured spend, never $0.00
[A5] ticfac status --json carries every field the dashboard renders (contract + tests) and the phone page renders the same model
[A6] z7w's Bombadil properties for this layout pass in CI
```

Proven in the reproduction above: `acceptance.Parse` of this text yields A1–A6 in order,
with no parse error. After it lands on epic/hn6 (and main), items A2–A6 become
addressable everywhere the items are read.

## Why this attempt does not apply it

- `.tick/` is a protected prefix for every worker role (internal/exec/subprocess/report.go:174,
  `protectedPrefixes`; the only exemptions at report.go:180 are `.tick/config.md`,
  `.tick/runners.toml`, `.tick/learnings.md`). A committed write there collects as
  `boundary-violation` and is released without carry (internal/reconcile/rejected_work.go):
  attempting it burns the attempt and lands nothing. `tk` is not a worker's tool either.
- The tick says no code change is needed, and the code agrees with the tick: a mid-line
  `[A<n>]` is a reference to an item, never a definition of one (internal/acceptance/acceptance.go:9),
  and that rule is already pinned by `TestParseIgnoresMidLineReferencesAndNonMarks`. Loosening
  the parser so this record's shape parses would be an out-of-scope contract change the reviewer
  cannot attribute to yjq — and the record would still violate the tracker's one-item-per-line
  format.
- Consequently this attempt committed nothing: no source changed, so there was nothing to
  gate and no gate was run. A worker that stops to ask with nothing committed is a
  blocked-first answer (internal/reconcile/dispatch.go:3286), not a failed collect.

## Decisions

- Question: can this attempt make hn6's acceptance list parse as six items? Choice: no
  in-boundary action exists that does; answer BLOCKED with the exact fix rather than
  improvise an out-of-scope change. Reason: the fix is a tracker-record write, and every
  path to it is outside a worker's authority. Standing-order class: none — this is not a
  decide-and-log choice; no default under the standing orders hands a worker the
  tracker's write authority.

## The systemic half is already carried

l89 — "Blocked tracker-record fixes need an orchestrator-applied disposition", promoted
from attempt 5's finding (key ef854dcf…, decision record on the run branch) — proposes
exactly the missing mechanism: a BLOCKED report whose named fix is a tracker-record write
is applied by the run itself through tk and the tick closed, instead of redispatching.
The final review waits behind it. This attempt does not re-file that proposal.

## What the next attempt or a person needs to know

- The re-flow above is the whole fix. Nothing else in yjq needs code or tests.
- If the ladder continues past this answer: the next rung is the ceiling step (the cloud
  implement ceiling is `strong`, .tick/runners.cloud.toml), dispatched to decide under the
  standing orders — but no standing-order class can hand a worker the tracker's write
  authority, so an honest answer from that rung holds for a person at the PR, where the
  held question is listed.
- For the person who disposes: apply the six-line re-flow (or land l89 and let the run do
  it), verify with `acceptance.Parse` that hn6 now yields six items, and the epic's final
  review unblocks; findings can then cite A2–A6.

One out-of-tick defect is reported below in the findings block; it was discovered while
tracing why this attempt's own prompt did not mention the question it was escalated for.

```findings v2
[
  {
    "kind": "defect",
    "title": "The blocked ladder's question never reaches a cloud worker's prompt",
    "severity": "medium",
    "body": "blocked.go's ladder escalates a stopped tick 'with the worker's question and its report in the new prompt' (escalation.go's section), but on the cloud substrate — the only one this epic runs on — that prompt never carries them: the door delivers only the rendered role profile, worker.sh prefers it over the checkout's composition, and `ticfac sandbox worker-prompt` composes no question either. Every escalate/decide rung therefore starts blind (this one re-derived the situation from .ticfac state), and the decide rung's 'decide under the standing orders' instruction cannot reach the worker at all; the same missing composition also drops the local prompt's boundary statement and report-path section. The ladder still converges through the report's STATUS line, so the cost is rungs spent blind and answers that may diverge, not a lost answer.",
    "evidence": "image/worker.sh:572 (the dispatch's role prompt wins over the checkout's composition); internal/exec/cloudflaresandbox/executor.go:92 (Options.Prompt is the only prompt the door carries — no Escalation or PriorReports field exists in that package); internal/exec/subprocess/escalation.go:52 renders the section no cloud prompt carries; internal/reconcile/blocked.go:24 is the ladder that promises it. This attempt's prompt is the bare profiles-cloudflare-sandbox/implement-tick.md, prompt_digest sha256:c25d77e… identical to the stopped attempt's."
  }
]
```

STATUS: BLOCKED — the fix is a one-field re-flow of hn6's acceptance_criteria (the exact six-line text is in this report, proven to parse as A1–A6), .tick/ is a protected prefix no worker may write and the tick itself says no code change is needed, and the field still reads zero newlines on epic/hn6 and main after three earlier blocked answers — the run's own tracker authority or the operator must apply it, after which acceptance.Parse yields six items, findings can cite A2–A6, and the final review unblocks.
