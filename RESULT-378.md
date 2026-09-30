<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-5/378`, base `912dc2c7f2a30b0135485552f37a3307dfff62e3`, harness `pi` exited 1, 0 work commit(s), 5 uncommitted path(s), salvaged into their own commit._

# 378

The harness exited 1 without writing RESULT-378.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

The harness exited 1 and a tree ticks-worker salvaged into its own commit landed on `tick/hn6/attempt-5/378`. That is
partial work, not an empty branch: review what landed before deciding anything,
because running this tick again from the base would discard it.

STATUS: NEEDS_CONTEXT — the harness exited 1 and wrote no report, but a tree ticks-worker salvaged into its own commit landed on tick/hn6/attempt-5/378; a human has to review what is there before this tick is run again
