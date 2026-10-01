<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-3/0rx`, base `ddf3cec1f5096fd8da9886f573a3c15a910c316c`, harness `pi` exited 143, 0 work commit(s), 3 uncommitted path(s), salvaged into their own commit._

> **CANCELLED BY THE SUPERVISOR** (`run_ended:failed`). The wave was cancelled while this container was
> still working. Its gateway credential was revoked first, so it could no
> longer make a model call; what it had was salvaged, committed and pushed
> inside the window the supervisor held open before the container was
> destroyed. Whatever is on this branch is therefore PARTIAL by
> construction — the agent did not decide to stop.

# 0rx

The harness exited 143 without writing RESULT-0rx.md. This report was written by ticks-worker so
the tick's outcome reaches the durable layer at all — an absent report is
indistinguishable from a container that never ran.

Nothing here is the agent's own account of the work; there is none.

The harness exited 143 and a tree ticks-worker salvaged into its own commit landed on `tick/hn6/attempt-3/0rx`. That is
partial work, not an empty branch: review what landed before deciding anything,
because running this tick again from the base would discard it.

STATUS: NEEDS_CONTEXT — the harness exited 143 and wrote no report, but a tree ticks-worker salvaged into its own commit landed on tick/hn6/attempt-3/0rx; a human has to review what is there before this tick is run again
