<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-4/lck`, base `207931e48e9dcdd8f5f4661090bc8aa2fa138e75`, harness `pi-durable` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# RESULT-lck — Status model: plain-language tick status, epic phase track, needs-you, ticks grouped by state

## What changed (commit 987870187 on `tick/ymf/attempt-4/lck`)

All of the tick's deliverables live in `internal/statusmodel`, versioned through
`ticfac status --json`, so watch (ugm), the phone page and any later renderer
read one answer:

- **Per-tick status word** (`tick.status`, required): derived in a new file
  `statusword.go` from the same records the pipeline cell (`pipeline.go`) is
  derived from — never a second opinion about the work. The vocabulary is the
  design doc's: `up next` (queued; its wave not reached or its blockers
  closed), `waiting: X` (blocked behind open work — the tracker's own
  `blocked_by` edges, `X is held` when the blocker holds for a person),
  `claimed` (the run took it, nothing of the attempt seen), `writing code` /
  `reviewing` / `closing out` (a live attempt, per role), `testing` (its gate
  running), `merged` / `done` (finished, per role), `waiting for CI` (the
  close-out's gate on the PR), `held: <reason>` (the hold's own words, from
  the same hold the needs-you entry reads), `failed: <reason>` (the refusal
  line's own sentence; `failed` bare when nothing states why), and — one word
  the design doc's list does not name — `merging` for the moment between an
  all-pass gate and the integrate it triggers (the design list has no word for
  "gate done, merge pending"; inventing nothing was not an option, and every
  other candidate — `testing`, `merged`, `waiting for CI` — says something
  false there). On a run that is not going and holds no live attempt for the
  tick, the working words become `waiting: <the ending's words>` (`the run is
  stopped`, `the run failed`, `the run is not going`), because nothing is
  working on them.
- **Inline exception note** (`tick.exception`, required-and-null): `attempt N`
  (the current work is not the first try, the h58 try number), `model
  escalated` (the tier differs from the first try's, or the model changed),
  `stalled Nm` (a standing worker whose branch/worktree idle passed the same
  threshold the run itself warns at — `reconcile.DefaultStallWarnAfter`) —
  joined with ", " only when they apply, and **never on a finished tick** (a
  merged/done row stays calm; its history is its try list).
- **Epic phase track** (`lifecycle.track` + `lifecycle.here`): the lifecycle's
  six phases folded into the design's five operator words — building (plan +
  waves together) → reviewing → closing out → PR & CI → merged — each in the
  phase states. `here` is the you-are-here index: the newest active step, else
  the first not-done, else the last (a merged epic is here at merged, a fresh
  one at building). The `wave i of n` beside the marker is the existing
  `lifecycle.wave`.
- **Tick groups** (`model.groups`, required-and-null when the tracker could
  not be read): NOW / DONE / UP NEXT / HELD, tick ids in the waves' own order.
  Mapping (stated in the contract so no renderer re-derives it): HELD takes
  `held:` and — on a not-going run — `failed`; DONE takes `merged`/`done`;
  UP NEXT takes `up next` and `waiting:` (the stopped-run override included);
  NOW takes everything else, a failure the going run is recovering from
  included.
- **Needs-you**: already versioned — `attention[].unblock_command` carries the
  exact clearing command. A held tick's status words the *same* hold line that
  entry carries (one hold, one wording, the tick-eli rule), pinned by
  `TestAHeldTickPointsAtTheSameNeedsYouCommand`.

Contract + bundle: `contracts/status-model.json` gained the four fields, two
new negative documents (a tick missing `status`; a track label outside the
closed label vocabulary), updated `why`, and all six goldens carry the new
fields populated; `contracts/bundle.json` re-cut at **2.4.0** with a fresh
digest and ledger entry, CHANGELOG entry added, `cloudflare/contracts.pin.json`
moved to 2.4.0. The model's `schema_version` stays 1 (additive, the way the
hn6 dashboard fields were).

Tests: `statusword_test.go` — per status word (18 cases: all twelve design
words + `merging`, the hold-priority rules, hold-from-hold/held/settled/
prior-run lines, the stopped/failed/dead-run overrides, the standing-attempt
exception), per exception component, per group bucket, and the phase track
table (8 mappings); `statusword_golden_test.go` — every golden's status words,
exceptions, track, `here` and groups held to what the derivation produces from
the document's own facts; `internal/cli/status_model_statuswords_test.go` —
`ticfac status --json` over a real checkout fixture carries the words, the
track and the groups.

## What I ran

- `go test ./internal/statusmodel/ ./internal/contracts/...` — ok (unit +
  golden guards + round-trip + builder binding + negatives).
- `go test ./internal/cli/` — ok (51s; includes the new wiring test).
- `make gate` — exit 0, 48 packages ok, no failures (ran twice, after the last
  change too). `go run ./cmd/contracts check` — offline bundle verification ok.
- `cloudflare`: `pnpm exec vitest run test/status-model.test.ts
  test/phone-page.test.ts test/status-classify.test.ts` — 39 tests pass;
  `pnpm lint` clean; the goldens validate on the TypeScript side too.
- `go vet ./...` clean, `gofmt` clean.

## What the next tick has to know

- **ugm renders from the model, nothing new to derive**: `tick.status` +
  `tick.exception` replace the TIER/ATTEMPTS columns and the pipeline dots
  (keep the drill-down reading `tick.pipeline`/`tries`), `lifecycle.track` +
  `lifecycle.here` are the one phase line with its marker (compose the
  "here (wave i of n)" from `lifecycle.wave`), `model.groups` gives the four
  groups in wave order, and the needs-you box is `attention[].unblock_command`
  (already exact). The status word carries a reason after `held:`/`failed:` —
  it is cut to 160 chars on a word boundary; truncate for the column, don't
  re-derive.
- **Bundle collision ahead (93n, b13 same wave)**: this attempt re-cut the
  contract bundle at 2.4.0 and moved the cloudflare pin. 93n (per-tick
  activity) and b13 (cost) will also touch `status-model.json`/`bundle.json`.
  The ledger refuses a re-cut at an unchanged version, so the resolve of that
  merge must be a **new cut (2.5.0)** carrying the union — not a hand-merged
  digest at 2.4.0. My golden edits are mechanical per-tick insertions
  (`status`/`exception` after each tick's `state` line, `track`/`here` after
  the lifecycle's `wave`, `groups` before `waits_on`), so a union resolve can
  replay them against the merged fixture.
- **Status-word decisions a reviewer may question** (all tested, all stated in
  the contract's field descriptions): (1) `merging` is the one word added
  beyond the design doc's list — the design list has no word for gate-done/
  not-yet-integrated; updating `docs/design/watch-redesign-2026-10.md`'s
  status-word sentence to include it is a one-line doc follow-up.
  (2) A refused tick groups to NOW while the run is going (the run is
  recovering it) and to HELD when the run is not going (only a person can
  move it). (3) The `waiting:` override on a stopped run lands in UP NEXT,
  not HELD — the run-level needs-you already carries the resume; HELD stays
  the per-tick needs-a-person bucket.
- **Holds read the same lines the waits read**: `run_held` stands until a
  resume answers it (the model's existing rule), a struck `held` attempt
  clears on its settle, and a prior run's hold arrives through the shared
  `standingPriorHolds` answer — so a row and the header cannot disagree.
- 47j (feed sentences) is untouched by this work; the `[e]` raw view and the
  feed itself are unchanged.

## Findings

```findings v2
[
  {
    "kind": "proposal",
    "title": "Phone page can now render the shared status words and tick groups",
    "severity": "low",
    "body": "The status model now carries tick.status, tick.exception, lifecycle.track/here and model.groups (this tick), but the factory's phone status page still renders the unlabelled pipeline dots and derives nothing of the new vocabulary. Rendering the same words and groups there is the reason the fields are versioned rather than computed in watch_view.go, and it is a small additive change to cloudflare/src/status.ts plus the page template.",
    "evidence": "cloudflare/src/status.ts types the pipeline cell but reads none of status/exception/track/groups; internal/statusmodel/statusword.go produces them"
  }
]
```

STATUS: DONE
