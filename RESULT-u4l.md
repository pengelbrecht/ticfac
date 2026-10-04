<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-3/u4l`, base `e368ca75ef239bf2261230f9fcd806db1d3a1650`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# The dashboard layout's properties run in the gate, over 500 seeded models at every pane shape (tick u4l, hn6 attempt 3)

Tick u4l is wave 4's in-gate half of hn6's acceptance A6: the four layout
properties the epic's spec names for the watch dashboard — rows never reorder,
needs-you never empty while a hold exists, never $0.00 for unmetered spend,
the frame fits the pane — asked as properties of the pure renderer
`renderWatchFrame` and checked over generated models, not over the one fixture
`watch_view_test.go` walks. One commit on `tick/hn6/attempt-3/u4l`, head
`defda01`, on base `e368ca7`.

## What changed (commit `defda01`, one new file)

- `internal/cli/watch_props_test.go` — the whole deliverable; no production
  code changed, because **no property found a real renderer bug** (see "What
  the properties found" below):
  - A seeded generator, `genModel(r *rand.Rand)` on `math/rand/v2`: 1–6 waves
    of 1–8 ticks with random states (closed/dispatched/ready), pipelines
    consistent with those states and with the role's own stage list
    (`PipelineImplement` / `PipelineReview` / `PipelineCloseout`), some ticks
    children of an earlier tick, 0–3 attention entries (some `needs_person`,
    some with the real unblock vocabulary — resume, triage, settle), 0–3
    workers standing on dispatched ticks with activity windows, cost lines
    mixing metered and unmetered, 0–5 recent events, health verdicts across
    healthy/degraded/stopped, CI present or absent. The ids are one length
    (`t01`…`t48`) so a row's id cell reads back unambiguously. Every model
    stamps itself against one fixed `GeneratedAt`, so nothing reads a wall
    clock. `advance(m, r)` moves some ticks one step along
    ready → dispatched → closed (pipelines, tries, durations follow) without
    touching the waves, the plan order or the parent links.
  - The four properties, each `func(frame, advanceFrame, m, am, width,
    height, unbounded) error` — wider than the tick's "func(frame, model)" by
    facts the harness holds anyway (documented on the type): **P1** the frame's
    tick ids are a subsequence of plan order, and the ids both frames show
    keep their order across the advance; **P2** a hold is announced, never
    replaced by "needs you: nothing", and the whole hold line — what and
    clearing command — appears wherever the pane seats it, while a run with no
    hold says "needs you: nothing" at height 0 or ≥ 8; **P3** when the cost
    says "not metered" anywhere, no `$0.00` shows, and the cost line reads
    whole wherever the pane seats it on both axes; **P4** no line wider than
    the pane, no frame taller than it.
  - The harness: every property over 500 fixed seeds (reproducible, plus the
    `-seed` flag, which appends one more seed so a reported failure re-runs
    exactly) at widths {0, 30, 47, 63, 80, 99, 120, 200} × heights
    {0, 8, 12, 24, 60}. On failure it prints the seed, the pane shape, a
    reproduce command and the minimal failing model as JSON (a greedy shrink:
    components, then attention and cost entries one at a time, then trailing
    waves and ticks, with dangling parent links and progress counts repaired).
  - Non-vacuity in the gate: `TestDashboardPropertiesCatchSeededBugs` runs
    each property against its named breaker — a wrapper that reverses the
    table rows (P1), one that drops the attention lines (P2), one that
    replaces "not metered" with "$0.00" (P3), one that pads the first line by
    5 columns (P4) — and requires the property to fail for at least one
    generated model, refusing a vacuous check. I verified the refusal fires
    by neutering a breaker and watching the test fail.
  - The "first frame within its bound" property is NOT here, as the tick
    orders: the header comment says so — it is a wall-clock property and
    stays with `status_firstframe_test.go` and z7w.

## What I ran

- `go test -short -timeout 20m -run 'TestDashboardProperties' ./internal/cli/`
  — passes in **2.2–2.5s** (acceptance [2] asks under 5s), also with
  `-seed=777` and `-seed=999`, and under `-race`.
- `go test -short -timeout 20m ./internal/cli/` — passes (31–52s depending on
  host load).
- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — exit 0, every package ok.

## What the properties found (the deliverable's report)

No real renderer bug — `watch_view.go` is untouched. The properties initially
failed in two places, and both turned out to be the naive reading of the
invariant exceeding what a narrow or short pane can physically show, not
renderer defects; the properties now encode the honest gate, and the choice is
documented where it is made:

- A hold's full line is pinned wherever the pane seats it — width 0 (the
  unknown width, which draws everything) always, and any width ≥ the line's
  own measure. In a narrower pane the renderer truncates the line (P4 pins the
  fit, and truncation keeps the line's head), so the property there holds the
  announcement — "needs you:", never "nothing" — because demanding a
  100-column command inside a 30-column pane would demand the impossible, not
  honesty.
- The cost line's "not metered" is pinned the same way, plus the height axis:
  the height fold compresses the middle, and the cost line stands in it, so a
  pane that drops the line cannot be asked to quote it. The harness hands each
  property the frame's uncompressed length at its width (one render per
  seed × width, computed once) as the measure of "the pane seats it".

## What the next tick has to know

- z7w (Bombadil) targets this same layout; its properties should be the same
  four invariants, and the comments here state where each one's gate comes
  from, so the two suites can be argued equivalent rather than re-derived.
- `propRowID` reads the row head `dashTickRow` writes and relies on the
  generator's fixed-length ids; if the row format or the width thresholds in
  `watch_view.go` change, this file and `watch_view_test.go` both have to stay
  green, and the `unbounded` parameter's meaning (the frame's length at
  height 0) has to survive any change to `fitDashboard`.
- The generator never draws a metered zero — a measured zero is honest and
  prints (pinned by `TestDashboardNeverPrintsZeroForUnmetered`), so within
  these models a visible `$0.00` is always fabrication, which is what P3's
  no-fabrication half relies on. If the generator ever draws metered zeros,
  that half needs to become per-segment.
- Runtime headroom against the 5s acceptance is ~2.5s on this host; if
  `renderWatchFrame` gets noticeably slower, the 500 × 40-combo run is the
  first place the gate will feel it.

```findings v2
[
  {
    "kind": "proposal",
    "title": "Narrow panes truncate a hold's clearing command out of the dashboard",
    "severity": "low",
    "body": "hn6 rule 2 says a hold shows in the header with the one command that clears it, but at panes narrower than the hold line the renderer truncates that line and the command falls off (a realistic settle command alone is 40+ columns). u4l's P2 pins the full line wherever the pane seats it and the announcement everywhere, because the command cannot physically fit a 30-column pane as one line. If the narrow-pane case matters in practice, the smallest change is a renderer rule that keeps the command by wrapping it under the announcement — a person's tick, since it touches watch_view.go and two pinned goldens.",
    "evidence": "internal/cli/watch_view.go dashboardAttentionLines + the width truncation in renderWatchFrame; rendered at width 30 the line reads 'needs you: attempt 2 of t2 str'"
  }
]
```

STATUS: DONE
