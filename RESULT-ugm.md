<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-7/ugm`, base `9a103dfebeb6e27e8dab3c225df3f57531e69f8d`, harness `pi-durable` exited 1, 0 work commit(s), 0 uncommitted path(s)._

# ugm

> **NO AGENT REPORT.** The harness exited 1 without writing RESULT-ugm.md. This report
> was written by ticks-worker so the tick's outcome reaches the durable layer at
> all — an absent report is indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/ymf/attempt-7/ugm`: no work commits, and nothing uncommitted
to salvage. The harness exited 1, wrote no report, and nothing landed:
a fault of this container's harness, not a question for anyone. There is no
status line on purpose — the run reads this as missing-result and dispatches
the tick again.
