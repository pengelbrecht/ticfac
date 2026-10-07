<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-6/2pn`, base `532a8cc9cace67355e1b94aac508e1f2fa581ccf`, harness `pi-durable` exited 1, 0 work commit(s), 0 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `532a8cc9cace67355e1b94aac508e1f2fa581ccf` is the head of the work it continued, which was cut from `177cfc3965afce7842ce168d7af19ef9cfce61a7`; its work commits are counted from the carried head._

<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-2/2pn`, base `177cfc3965afce7842ce168d7af19ef9cfce61a7`, harness `pi-durable` exited 1, 0 work commit(s), 0 uncommitted path(s)._

# 2pn

The harness exited 1 without writing RESULT-2pn.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/ex6/attempt-2/2pn`: no work commits, and nothing uncommitted
to salvage. This tick is unimplemented.

STATUS: BLOCKED — the harness exited 1, wrote no report, and nothing landed on tick/ex6/attempt-2/2pn; re-dispatch this tick
