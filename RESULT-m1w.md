<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-17/m1w`, base `bb8f5b101522d775d7a2f771524ce79a5a854a87`, harness `pi-durable` exited 0, 0 work commit(s), 5 uncommitted path(s), salvaged into their own commit._

# m1w

The harness exited 0 without writing RESULT-m1w.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

The harness exited 0 and a tree ticks-worker salvaged into its own commit landed on `tick/ex6/attempt-17/m1w`. The work is
there and reviewable on its own merits; what is missing is the agent's account
of it. This is not an unimplemented tick — review the branch. Running it again
would redo work that is already on the branch.

STATUS: DONE_WITH_CONCERNS — the harness exited 0 and a tree ticks-worker salvaged into its own commit landed on tick/ex6/attempt-17/m1w, but no agent report exists; review the branch, and note nothing describes the work but the diff
