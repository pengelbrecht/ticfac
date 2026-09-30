<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-4/zl1`, base `1f6581d7f0db6de02cdf40888e999e172417e62b`, harness `pi` exited 143, 0 work commit(s), 0 uncommitted path(s)._

> **CANCELLED BY THE SUPERVISOR** (`run_ended:completed`). The wave was cancelled while this container was
> still working. Its gateway credential was revoked first, so it could no
> longer make a model call; what it had was salvaged, committed and pushed
> inside the window the supervisor held open before the container was
> destroyed. Whatever is on this branch is therefore PARTIAL by
> construction — the agent did not decide to stop.

# zl1

The harness exited 143 without writing RESULT-zl1.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/hn6/attempt-4/zl1`: no work commits, and nothing uncommitted
to salvage. This tick is unimplemented.

STATUS: BLOCKED — the harness exited 143, wrote no report, and nothing landed on tick/hn6/attempt-4/zl1; re-dispatch this tick
