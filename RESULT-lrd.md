<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-8/lrd`, base `ff8ac1e8d84ac31e55a9eea780e601a5dd6945f1`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# lrd

> **NO AGENT REPORT.** The harness exited 0 without writing RESULT-lrd.md. This report
> was written by ticks-worker so the tick's outcome reaches the durable layer at
> all — an absent report is indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/ex6/attempt-8/lrd`: no work commits, and nothing uncommitted
to salvage. The harness exited 0, wrote no report, and nothing landed:
a fault of this container's harness, not a question for anyone. There is no
status line on purpose — the run reads this as missing-result and dispatches
the tick again.
