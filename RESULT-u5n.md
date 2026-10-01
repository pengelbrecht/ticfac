<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-4/u5n`, base `07ea92912b6f7a6f8d484a64fc1c9972bb037f4d`, harness `pi` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# RESULT — tick hn6: watch renders the dashboard frame

## Shape

`renderWatchFrame` draws the whole epic for a person, as one frame that
fits the pane it is given: no alternate screen, no assumed pane size, and
rows that never move.

Order, top to bottom:

1. attention, only when a person is needed — one line per hold, each
   naming the command that moves it on
2. the run's identity: epic id and title, alive, elapsed, cost
3. the lifecycle: waves in order, the active one expanded — one fixed
   row per tick, its cells state, tier, pipeline, time and attempts
4. the run's wait, with how long it has waited
5. the run's wait, with how long it has waited

## Cells

The pipeline cell fills left to right. A done stage is plain, the
active one bold with "●", a failed one red with "✗", and every pending
stage after the live one is one dim "…". The row's time and attempts
come from the worker's own records, never parsed prose, so a tick
never moves and a glancing read is always right.

## Status

Renderer + tests are written in this workspace. The renderer is a pure
function of the status model plus width and height, so the frame is
assertable headless: same model, same pane, same bytes.
