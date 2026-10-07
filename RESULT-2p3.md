<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-1/2p3`, base `88c0ba451b814aab34bd6f31da38788fbe1e18f7`, harness `pi-durable` exited 1, 0 work commit(s), 0 uncommitted path(s)._

# 2p3

The harness exited 1 without writing RESULT-2p3.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/ex6/attempt-1/2p3`: no work commits, and nothing uncommitted
to salvage. This tick is unimplemented.

STATUS: BLOCKED — the harness exited 1, wrote no report, and nothing landed on tick/ex6/attempt-1/2p3; re-dispatch this tick
