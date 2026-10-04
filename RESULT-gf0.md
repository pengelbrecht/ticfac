<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-6/gf0`, base `81ad2e572fed34b243bacf974655bab021493a73`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# implement-tick: gf0 — watch's settle alert spells a dash attempt for a never-dispatched tick

Branch `tick/hn6/attempt-6/gf0`, one commit: `9d9c89a`.

## The defect, reproduced first

`ticfac watch`'s stream path (stdout not a terminal) alerts a human on every
`run_held` line. The holds that fire before the tick's first dispatch —
`claim_width`, `foreign_claim`, `absorption_depth_exceeded` — carry a NULL
`attempt` (the reconciler's `attemptOf` answers nil for a tick it never
dispatched), so the alert's settle branch fell to its inline format over the
`-` placeholder and named:

    ticfac settle wne w9b - --release "<who>"

— a command the settle CLI refuses ("-" is not an attempt number), and the
wrong verb besides: those holds are facts about the world and the bound, not
attempts a person releases. All four of the new watch tests reproduced exactly
that output before the fix (run test-first; read the failures, then made them
pass).

## The fix: one per-hold-kind decision, spelled once, read everywhere

The per-kind decision is `statusmodel.HoldClearingCommand` (with
`statusmodel.HoldReason`, the refusal reason every `run_held` line's detail
leads with — a field of the line, since all three `StageRunHeld` writers use
one shape, `<reason>: <message>`):

- **finding_untriaged, absorption_depth_exceeded** → the triage addressed to
  the holding run's own store. The finding the bound refused to absorb is
  still a person's to decide — absorb, file, fixed, discard — and the
  refusal's own message names the other road (raise with `--absorption-depth`
  and run again). The REASON decides, never the attempt: even an
  absorption_depth line that carries one (the reporting tick's) is not
  released by it.
- **claim_width, foreign_claim** → the run again, addressed by the host the
  run lives on (`ticfac run-epic <epic>` locally, `ticfac run <epic> --cloud`
  for a cloud run): they end when the holder's tick closes or a slot frees,
  never by a release, and a resumed run re-derives and proceeds the moment
  they do.
- **every other hold** → the settle release the attempt addresses (unchanged).
- **unknown reason + no attempt** → no command at all; the alert says what it
  knows and no more. No command is better than a wrong one a person copies.

Every surface that names a clearing command now reads that one function, so
no two can drift (the repo's own "a command spelled in two places is two
commands" rule):

- `internal/cli/watch.go` — the alert: per-kind sentences, command from the
  decision; the inline dash spelling is deleted.
- `internal/statusmodel/build.go` `buildWaits` — the needs-you header entry,
  which previously left these holds with NO clearing command (that was A2's
  gap: "shows the hold and its clearing command in the header"). Also
  `priorHoldCommand`, now a one-line delegate.
- `internal/statusmodel/pipeline.go` `attentionCommand` — the rows' next
  steps.

The plain `statusmodel.SettleCommand` — orphaned by the shared decision, and
a second spelling whose `--run-id` rule drifted from the reconciler's own
(`reconcile.SettleReleaseCommand`) — is removed. One consumer-visible
consequence, deliberate and reviewed: a prior run's hold under the epic
spelling now names the bare settle (the flag spelled `--run-id epic-<id>`
redundantly — the default already addresses that store); prior holds under
other ids (cloud `run_<hex>`, the shapes every existing test pins) are
byte-identical.

The existing subtest that pinned "a hold that names no attempt carries no
release command" (a prior run's absorption-bound hold) moved with the
decision: that hold now names the triage, which is the point of the tick, and
a new subtest pins the nil branch on a reason the closed set does not know.

## What I ran (foreground, read the output)

- `go test ./internal/cli/ -run 'TestWatchHoldAlertFor' -count=1` — failed on
  the new tests first (the dash settle reproduced verbatim), passes after.
- `go test ./internal/cli/ -run TestWatch -count=1 -timeout 20m` — the
  demonstrating check the absorption record names; ok, 48 tests.
- `go test ./internal/statusmodel/ -count=1` — ok (one existing subtest
  updated to the new decision, as above).
- `go test ./internal/cli/ -count=1 -timeout 20m` — ok.
- `make gate` (gofmt, `go vet ./...`, the whole-repo short suite,
  `GOTEST_PARALLEL=4`) — exit 0, twice (after the last edit too).
- `internal/reconcile` untouched by this diff, so the full 20-30m suite was
  not run (per AGENTS.md); its short suite ran inside the gate, green.

## What the next tick has to know

- New hold kinds: add one case to `statusmodel.HoldClearingCommand`, and —
  only if the alert's wording should differ — one sentence case to watch.go's
  switch. The reason prefix every `run_held` line leads with is the key;
  `statusmodel.HoldReason` reads it.
- The dashboard header now shows a clearing command for world/bound holds
  because `buildWaits` fills `UnblockCommand`; nothing in the view code
  changed — it renders whatever the model carries.

```findings v2
[
  {
    "kind": "defect",
    "title": "land_review_not_ready hold names a release that does not clear it",
    "severity": "low",
    "body": "The final-review hold fires after the review attempt was dispatched, so it carries an attempt and the per-kind decision names the settle release for it — but releasing the review attempt clears nothing: the next resume re-reads the NOT READY verdict from the PR and holds again. The refusal's own message names the real moves (fix and run again, merge the PR by hand, or close it), so the hold's clearing command is arguably the resume, the same shape the world holds got here.",
    "target": "pengelbrecht/ticfac",
    "breaks": {"item": "A2", "check": "go test ./internal/cli/ -run TestWatch"},
    "evidence": "internal/reconcile/land.go:501 (the refusal prose) — the command named is the settle for the review attempt, which no run_held line's release disposes"
  }
]
```

STATUS: DONE
