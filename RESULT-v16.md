<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-9/v16`, base `6e53db7e3e2a4ca240fff37523b8da7b5dddbb42`, harness `pi` exited 143, 0 work commit(s), 0 uncommitted path(s)._

> **CANCELLED BY THE SUPERVISOR** (`run_ended:stopped`). The wave was cancelled while this container was
> still working. Its gateway credential was revoked first, so it could no
> longer make a model call; what it had was salvaged, committed and pushed
> inside the window the supervisor held open before the container was
> destroyed. Whatever is on this branch is therefore PARTIAL by
> construction — the agent did not decide to stop.

# v16

The harness exited 143 without writing RESULT-v16.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

Nothing landed on `tick/hn6/attempt-9/v16`: no work commits, and nothing uncommitted
to salvage. This tick is unimplemented.

STATUS: BLOCKED — the harness exited 143, wrote no report, and nothing landed on tick/hn6/attempt-9/v16; re-dispatch this tick
