<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-5/yjq-run_5c7c16d199414da2a8a40d97b524e987`, base `6f76ad9f4ced6d78e1089524fbfcc98004057022`, harness `pi` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# yjq (attempt 5, run_5c7c16d) — the re-flow proposed as the delivery, for the run's own writer to apply

The tick asks for one thing: epic hn6's `acceptance_criteria` re-flowed one `[A<n>]` item per
line, because `acceptance.Parse` takes a mark only where a line begins with one — six marks on one
line read as the single item A1 and A2–A6 are unaddressable. No code change is needed; the record
is the tracker's to re-flow. Three earlier attempts answered BLOCKED with the exact fix in prose
because a worker cannot write `.tick/`; l89 made the orchestrator the applier, and this prompt now
carries the `tracker-edits` block. So this attempt's delivery is the proposal below, held to every
check the run itself makes, and nothing else. This worktree commits nothing — the edit, applied
through the run's own writer, IS the delivery.

## What this attempt verified at this tree (re-derived, not copied from the prior reports)

1. **The defect is live, on the dispatch head and on the remote.** The record at this worktree's
   HEAD (`6f76ad9f`, the run's own dispatch head) and at `origin/epic/hn6` (`6aaa3625`, read with
   `--no-write-fetch-head`, no refs written) both carry `acceptance_criteria` with **0 newlines**,
   6 `[A<n>]` marks, 1 line-leading — `updated_at` still `2026-09-28T21:54:51`. The production
   parser (`internal/acceptance`, `attemptedMark` = `^[ \t]*\[A([^\]]*)\]`) reads **exactly one
   item, A1**.
2. **The broken record still reaches the findings channel.** A scratch report whose finding claims
   `breaks: {"item": "A2", "check": "go"}` is refused by the reader the run itself collects with —
   `ticfac-exec-subprocess lint-report … --tick yjq` exits 1 with
   `"A2" is not an acceptance item of the epic; the epic's items are A1`. Until the record is
   re-flowed, no worker can even claim A2–A6 as done-evidence.
3. **The proposal passes every check the run makes of it**, run against the production reader
   (a temporary test package, `internal/yjqverify`, deleted after it ran — nothing committed):
   - `acceptance.Parse` on the proposed value returns **six items, A1–A6, no error**, each item's
     text its own fact (no mark nested inside another item's text);
   - `subprocess.TrackerEdit.Validate` returns nil (shape, tick id, field, size, control chars);
   - `subprocess.DroppedAcceptanceItems(record, proposal)` is **empty** — every item the record
     marks is an item the proposal carries, so no refusal for a dropped item;
   - `subprocess.TrackerEditAgainst(edit, <this checkout>)` returns `""` — the checkout's own
     answer accepts it;
   - the proposal is **bit-for-bit the re-flow of the record as it stands**: a newline where the
     space before each mid-line mark stood — same 816 bytes, token-identical
     (`' '.join(held.split()) == ' '.join(reflowed.split())`), no word added, none dropped, no
     trailing spaces.
4. **The proposal unblocks A2–A6 in the run's own reader.** The same scratch report that failed
   above passes (`exit 0`, `ok: the report passes the check`) when linted against a scratch COPY of
   the tracker (in /tmp, the real `.tick/` untouched) carrying the proposal applied — the claim
   `breaks.item: A2` reads, because `epicItems` parses the epic's items from the re-flowed field.
5. **Nothing else needs to change once it lands** (re-confirmed at this tree): the review binds
   items fresh from the record (`internal/reconcile/pr_reviewer.go`, `doneEvidence` parses the
   epic's acceptance on every pass) and the close-out scores against the record as it stands NOW
   (`internal/reconcile/score.go`, `acceptance.Decide`), so A2–A6 become addressable the moment
   the run writes the re-flow.

## The delivery — the proposed tracker edit

One edit, one field: hn6's `acceptance_criteria`, re-flowed one `[A<n>]` item per line. The value
below is derived from the record as it stands (see verification 3), not hand-copied — same words,
same order, same punctuation; only the five separating spaces became newlines.

```tracker-edits
[
  {
    "tick": "hn6",
    "field": "acceptance_criteria",
    "value": "[A1] ticfac watch epic-<id> renders the fixed layout: header (progress bar, ticks n/m, elapsed, ETA, health verdict, phase bar, needs-you), a tick table whose rows never reorder with a per-tick pipeline cell, a workers panel with activity, CI and cost lines, and a two-line event tail;\n[A2] a held run shows the hold and its clearing command in the header; nothing-needs-you is shown when true;\n[A3] enter on a tick shows attempts with tier, verdict and reason, report summary, gate evidence, diff stats and findings; e opens the full feed;\n[A4] cost shows metered spend and says 'not metered' for unmeasured spend, never $0.00;\n[A5] ticfac status --json carries every field the dashboard renders (contract + tests) and the phone page renders the same model;\n[A6] z7w's Bombadil properties for this layout pass in CI"
  }
]
```

An edit already in place is a no-op in the run's writer (`applyTrackerEdit` answers "already in
place; nothing is written twice"), so a proposal that lands after an identical one is safely
skipped, and the collect holds it to the tracker as it stands at the write.

## What was run (all foreground, output read)

- `go test -v -count=1 ./internal/yjqverify/` — the three verification tests above, against the
  production parser and the run's own edit checks: PASS (defect reproduced as [A1]-only; proposal
  pinned as the bit-for-bit re-flow; every run-side check green).
- `ticfac-exec-subprocess lint-report /tmp/scratch-a2/report.md --role implement-tick --tick yjq
  --repo /work/repo` — exit 1, `"A2" is not an acceptance item of the epic; the epic's items are
  A1`; the same against the scratch copy carrying the proposal — exit 0.
- `nice make gate GOTEST_PARALLEL=4 GOFLAGS="-p=2"` — gofmt, `go vet ./...` and the short suite
  across the repository: **exit 0**, `internal/yjqverify 0.006s` green in the whole-repo pass.
- `ticfac-exec-subprocess lint-report RESULT-yjq.md --role implement-tick --tick yjq` — exit 0
  (this report, checked with the same reader the run collects it with).

## What the next tick has to know

- The delivery is the proposal, not a branch: this attempt committed nothing, by design. The run
  applies the edit through its own writer, gates the result and closes yjq over it; nothing here
  needs merging.
- The observable that it landed: `hn6`'s `acceptance_criteria` carries 5 newlines and
  `acceptance.Parse` on it returns six items A1–A6; a worker-visible proof is that `lint-report`
  stops refusing `breaks.item: A2..A6` claims with "the epic's items are A1".
- If yjq is re-dispatched after the edit has landed: read `.tick/issues/hn6.json`; ≥5 newlines
  means the fix is in place — answer DONE pointing at the landed record (an identical proposal is
  the writer's no-op, so re-proposing it is safe too).

```findings v2
[]
```

STATUS: DONE
