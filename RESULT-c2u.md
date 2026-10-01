<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-14/c2u`, base `06f0bf226293dce9f1b2461474d27d68263c55ac`, harness `pi` exited 0, 2 work commit(s), 0 uncommitted path(s)._

<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-14/c2u`, base `06f0bf2bfeea2688bae65bf7a48fdaa5239d1fbb`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Watch gains drill-in keys, the tick view and the full feed (tick c2u, hn6 attempt 14)

The live watch's dashboard (u5n's frame) now answers the keyboard, the event
feed has shrunk to the frame's two-line tail, and the first-use bug — "v7z
rejected x shows no reason and no next step" — is fixed where a person now
reads refused lines. One commit on `tick/hn6/attempt-14/c2u`, head `b13d7b6`
(three files changed, four new).

## What changed (commit `b13d7b6`)

- **`internal/cli/watch_keys.go` (new)** — the interaction, pure and headless:
  - `watchUI{view, selected, scroll}` and `func (u watchUI) key(k string,
    m statusmodel.Model) watchUI`, a pure reducer over the plan order
    (`watchPlanOrder`: waves in order, the tracker's order inside each wave).
    j/down and k/up move the cursor one row and clamp at both ends; from no
    cursor, moving down enters at the first row and moving up stays (the top
    end). enter opens the tick view for the cursor's tick — with nothing
    selected it selects the first in-flight tick (last try `in-flight`, or
    state `dispatched` with no try history), else the plan's first tick. e
    opens the feed view (scroll reset); esc, or q in a drill view, comes back
    to the dashboard. In the feed view j scrolls toward newer lines (clamped
    at the bottom) and k toward older. A selection the plan no longer carries
    (absorbed, replanned away) is dropped, and a tick view standing on one
    comes down. A zero `watchUI` acts as the dashboard.
  - `parseKeys(b []byte) []string`: "\r" → enter, "\x1b" → esc, "\x1b[A"/
    "\x1bOA" → up, "\x1b[B"/"\x1bOB" → down, "\x03" → ctrl-c, plus e/j/k/q;
    every other byte and every complete-but-unknown escape sequence is
    ignored whole.
  - `watchAttachKeys` (the seam the tests fake): when stdin is a terminal,
    `term.MakeRaw` puts it in raw mode and returns the restore; the keys are
    read on their own goroutine (`watchReadKeys`) into a buffered channel,
    holding back a cursor sequence cut across two reads (bounded, so a
    terminal that stalls an introduction cannot hold later bytes hostage).
- **`internal/cli/watch_drill.go` (new)** — the drill views, pure
  render-to-strings:
  - `renderTickView(m, tickID, st, width, height)`: the tick id with its
    gloss/title and its pipeline cell (the dashboard's own `dashPipeline`,
    words at width ≥ 100, glyphs below), one line per try —
    `try N  <tier>  <outcome>  <reason>`, with `— next: <next_step>` on the
    last — then the report summary and `diff: F files +I −D` (U+2212), or
    "report not read" when the report was not read, then the gate evidence
    rows for the tick (check, result — pass green, fail red — head[:8]) and
    the findings (the durable key with its gating tri-state — gating / not
    gating / untriaged — and the title on an indented line, so the 64-hex
    key, the title and the verdict all fit inside the tick's 100-column test
    width). The footer is "[esc] back"; a pane too short keeps the footer and
    counts the rest ("+N more"). A tick the plan no longer carries says so.
  - `renderFeedView(events, tries, model, scroll, width, height)`: the whole
    feed, newest at the bottom, the height as the window scrolled up by
    `scroll` events, every line through the shared one-line form; unknown
    height renders everything.
  - `watchEventLineWith(event, tries, model)`: for stage `rejected` or
    `gate_failed` the line names its reason — the detail's first clause, up
    to the first ":" or 120 characters, cut at a word boundary — and, when
    the model is at hand and the tick's last try carries one, the run's own
    "— next: <next_step>". `watchEventLine` keeps the full-detail form byte
    for byte for the stream path and the tail; `watchEventWho` moved beside
    it as the shared prefix.
- **`internal/cli/watch.go`** — the wiring:
  - `watchLive` attaches the keyboard when it is a terminal (raw mode
    restored on EVERY exit path — return, context cancellation, panic — by
    one `defer`), drains keys in the loop's select, and redraws immediately
    on each one. ctrl-c from any view, and q on the dashboard, end the watch
    exactly as SIGINT does today: the same words and the same exit code, via
    one shared `interrupted` helper the ctx path also uses. q in a drill view
    only comes back down, so a person is never trapped in a raw-mode view.
  - On a TTY the feed is no longer replayed as lines above the block:
    `watchKeepStages` and the keep-line insert are gone — the dashboard's
    two-line tail carries the feed (hn6 rule 6) — and only the attention
    alert keeps its one durable copy per episode above the block (it is not a
    feed line; its test still pins the once-per-episode copy). The loop still
    reads the standing feed every frame: it serves the graph invalidation on
    absorbed/replanned lines, the try counting, and the full feed the drill
    views show; a failed read keeps the lines the feed view had.
  - The non-TTY stream path is untouched byte for byte (its print callback
    never went through `watchEventLine`; the whole stream-path test set
    passes unchanged).
- **`README.md`** — the `ticfac watch` row now describes the dashboard, the
  keys (j/k, enter, e, esc, q) and the unchanged pipe behaviour; the command's
  own Long help says the same in its own words. (The attached view of
  `ticfac run` is watch's own command body, so the keys ride along with it.)

## Deliberate deviations from the tick's letter, and why

- `renderFeedView` carries `model *statusmodel.Model` beside the parameters
  the tick names: the reason and next step need the model at hand, and the
  feed view is the only event-line surface left on the TTY path now the keep
  lines are gone and the two-line tail belongs to `watch_view.go`, which this
  tick may not edit. Without it, item 4 of the tick would have no home at
  all. The next step rides only on refusal lines (the spec's "also print" is
  in the same sentence as the refusal clause; applying it to every line would
  hang next steps off dispatch lines).
- The feed view has no footer (the tick names one only for the tick view);
  esc/q come back, and the README says so.
- `watchKeepStages` is deleted rather than left unused: nothing else read it.
- j from no cursor enters at the first row; k from no cursor stays no cursor
  (the clamp semantics taken literally). Both documented in the code.

## Verification (all foreground, all green)

- `gofmt -l internal/cli/` clean; `go vet ./...` clean.
- The tick's named command: `go test -short -timeout 20m -run
  'TestWatchKeys|TestTickView|TestFeedView|TestRejectedEventLine|TestWatch'
  ./internal/cli/` — ok, including every pre-existing stream-path and
  dashboard-frame test unchanged (`TestDashboardGolden` still byte-for-byte;
  `watch_view.go` untouched).
- The new tests: TestWatchKeysParse (byte sequences → names, unknown escape
  ignored), TestWatchKeysNavigate (j/k across the fixture's five ticks,
  clamped at both ends; enter with no cursor opens the in-flight t2; e then
  esc returns; arrows alias; a dangling cursor is dropped), TestTickView-
  ShowsTriesReasonsAndEvidence and TestTickViewSaysReportNotRead (the
  contract bundle's `dashboard` golden: 46x's refused try with its reason and
  the run's next step, "report not read", the gate row `go fail 4d8c0e9a`;
  060's report summary, `diff: 11 files +214 −60`, its passing gate row and
  its finding with `gating`), TestFeedViewScrolls (30 events, height 10,
  scroll 5 → events 16–25, nothing outside the window), TestRejectedEvent-
  LineNamesReasonAndNextStep (reason clause + next step with the model,
  reason without it, the old form byte-for-byte), TestWatchEventReasonIsThe-
  FirstClause, TestWatchKeysEnterHasNowhereToGo.
- TestWatchOnATerminalTakesKeys (new, in watch_block_test.go): the live loop
  driven by a faked key channel — j puts the cursor on t1, enter opens the
  drill view, esc returns, e opens the whole feed (the feed's oldest line,
  which the two-line tail never shows), q on the dashboard ends the watch
  with the interrupted words and exit 1, and the raw-mode restore is seen to
  run. `fakeTerminal` now also pins the keyboard seam keyless, so the
  terminal-faking tests stay hermetic whatever the host's stdin is.
- `go test -short -timeout 20m -count=1 ./internal/cli/` — ok.
- `make gate` — rc=0.

## What the next tick has to know

- The live view's interrupt exit is still 1 on a live run (held → 3), exactly
  as SIGINT has always answered there; q and ctrl-c share it. The pipe path
  answers 5 for the same interruption and the exit table and the command's
  help both say 5 — see the finding below; I did not change an exit code the
  tick bound to "exactly as SIGINT does today".
- The tail's lines are still the full-detail form (`dashboardTail` in
  `watch_view.go` calls `watchEventLine`, out of this tick's file scope), so
  the tail shows the whole detail and the feed view the clause plus the next
  step — one surface for the glance, one for the drill.
- The drill views do not carry the dashboard's column-drop rules at narrow
  widths; they truncate lines to the width. At width < 66 a finding's gating
  verdict can be truncated away (the 64-hex key leads its line). If a narrow
  drill-in is wanted, that is new work, not a defect in what the tick asked.
- The attached view of `ticfac run` now answers the keys too (it is watch's
  own command body); ctrl-c and q both detach it through the same code the
  SIGINT path always returned, and `run.go`'s `attachRun` already maps both.

```findings v2
[
  {
    "kind": "defect",
    "title": "The live view's interrupted-on-a-live-run exit contradicts the exit table, the command's own help and the pipe path",
    "severity": "low",
    "body": "The exit table (README and `ticfac watch --help`) says 5 running for 'a watch interrupted on a live run — the work continues, nothing is wrong', and the pipe path answers exactly that (finish(agentStateRunning) after watchRunStillAlive). The live path does not: watchLive's ctx.Done branch — the branch a terminal's SIGINT takes, and since c2u also the branch q and Ctrl-C take — returns 1 (failed) when nothing is held and 3 when a hold stands, never 5. A person or script that branches on the code reads a healthy, still-going epic as a failure on a terminal and as running on a pipe, and run.go's attachRun already has to compensate for it (its `ctx.Err() != nil && code == 1` probe fallback exists only because the live view answers 1). Fix shape: in watchLive's interrupt branch, ask the run's own claim — the same watchRunStillAlive the stream path asks — and return the running class when the run is alive and nothing is held, keeping held (3) ahead of it; move run.go's detach mapping with it (attachRun's exitRunning branch already handles 5, so the compensation branch can retire). Reproduces at the base: interrupt a terminal watch of a live run, watch the exit code.",
    "breaks": {"item": "A1", "check": "go"},
    "evidence": "internal/cli/watch.go watchLive select ctx.Done branch (returns 1); stream path: `if watchRunStillAlive(...) { return finish(agentStateRunning, nil) }`; table: README.md exit row 5 and newWatchCommand's Long help; the compensating probe: internal/cli/run.go attachRun"
  },
  {
    "kind": "defect",
    "title": "No golden document anywhere in the bundle carries a non-null next_step, so the field the first-use bug is about has zero fixture coverage for the renderers still to come",
    "severity": "low",
    "body": "All three goldens (dashboard, status_model_running_wave, status_model_completed_awaiting_merge) carry next_step null on every try — tick 378's anchor deliberately stops at reason, because the dashboard golden's one refusal was superseded by a live try and no next step is derivable from it. The consequence is not that anchor's but the bundle's: every renderer downstream of the model — the phone page (A5, i1r), z7w's properties, any future golden test — has no fixture that shows the field rendering, and c2u had to shape a next step in test code to pin the tick view's '— next:' line. Fix shape: a fourth golden (status_model_refused_last_try: a tick whose LAST try is refused, with the run's own reason and next step derivable from its own facts — a later attention entry or a redispatch line) admitted by the schema, so TestEveryGoldenAgreesWithThePipelineDerivation covers the field the way it now covers the cells. This does not touch the dashboard golden's anchor.",
    "breaks": {"item": "A5", "check": "go"},
    "evidence": "contracts/status-model.json golden.{dashboard,status_model_running_wave,status_model_completed_awaiting_merge} — all next_step null; the anchor's rationale internal/statusmodel/contract_test.go (TestTheContractBindsTheDashboardGolden, the try-anchor comment); c2u's shaped fixture internal/cli/watch_drill_test.go"
  }
]
```

STATUS: DONE
