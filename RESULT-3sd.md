<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-3/3sd`, base `ee4ad6b817be5b934764ed528dd63686b040ceea`, harness `pi-durable` exited 1, 0 work commit(s), 0 uncommitted path(s)._

# 3sd

The harness exited 1 without writing RESULT-3sd.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/ex6/attempt-3/3sd`: no work commits, and nothing uncommitted
to salvage. This tick is unimplemented.

STATUS: BLOCKED — the harness exited 1, wrote no report, and nothing landed on tick/ex6/attempt-3/3sd; re-dispatch this tick
