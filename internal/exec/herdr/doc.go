// Package herdr is ticfac's Herdr executor: the same five operations
// internal/reconcile already knows — Start, Inspect, CollectDetail, Cancel,
// Dispose — over a herdr workspace per attempt and an agent herdr owns.
//
// It is the second implementation of the seam Phase 1 defined. The protocol
// records (JobSpec, JobHandle, JobStatus, CancelAck, JobResult) and the
// collect vocabulary are the ones internal/exec/subprocess owns, because they
// are the CONTRACT's, not that executor's; this package imports them rather
// than re-spelling them, so the two executors cannot disagree about a record
// with nothing failing. Everything herdr-specific — workspace id, pane id,
// agent name, herdr's protocol and server version — lives inside
// JobHandle.Handle, the one open object in the contract, and inside this
// package. The reconciler learns nothing about panes, workspaces or
// protocols; a test in this package pins that internal/reconcile carries no
// herdr code, and asserts this Executor satisfies reconcile.Executor without
// that interface having moved.
//
// # The three lessons this executor carries, from ticks' own herd machinery
//
// ticks' spawn/wait/reconcile/cleanup packages were read for this tick and
// deliberately NOT ported — ticfac's reconciler already owns waves, adoption,
// disposition and cleanup. What came across are the hard-won details:
//
//   - THE PANE-BUSY RETRY AND THE READINESS POLL (spawn). The root pane
//     worktree.create hands back is not a usable shell for the first few
//     hundred milliseconds of its life: agent.start fails with
//     `agent_pane_busy` and succeeds on a retry, and a launch that answers
//     `launch_pending` has to be sampled for `interactive_ready` because the
//     status reaches idle before readiness does. Start absorbs both, bounded
//     by the caller's startup budget rather than a fixed attempt count.
//   - DISPATCH IS CONFIRMED, NOT FIRE-AND-FORGET (spawn). After submitting the
//     prompt, Start waits for the agent to reach `working` once. A
//     confirmation that times out is an OBSERVATION, not a failure — a
//     trivial tick can finish before `working` is ever rendered.
//   - A SUBSTRATE FAILURE DECIDES NOTHING (x6j's line, held here as shape).
//     No content gate reads the pane back: agent_prompt_stalled and herdr
//     going quiet are recorded as operational observations and never as a
//     verdict. Inspect answers `lost` — not terminal — when herdr cannot be
//     asked; a settlement is recorded only when herdr POSITIVELY answers
//     that the agent is gone. And teardown refuses to act on an unanswered
//     liveness question: herdr not answering is not evidence that tearing an
//     agent down is safe.
//   - THE WALL CLOCK IS ENFORCED, NOT NOTICED (gwc). The settlement deadline
//     is the reconciler's, inherited unchanged; the STOP is this
//     executor's, because herdr owns the agent's process. The enforcement
//     runs inside observe (wall.go): past spec.Limits.WallSeconds a live
//     agent is interrupted through herdr's own surface and recorded as
//     STOPPED AT ITS WALL CLOCK — a distinct verdict from merely settled,
//     in the same closed failure vocabulary the local executor collects.
//     A herdr too old for the interrupt surface never sees a bounded
//     dispatch at all: Start refuses it, naming the bound and the herdr
//     version, because a limit nothing enforces is a limit the operator
//     trusts wrongly.
//
// # What is deliberately NOT here
//
// Each of these is its own tick, building on this seam:
//
//   - 2xu: the verdict comes from durable evidence only. CollectDetail here
//     already makes ZERO herdr calls — by construction, like ticks' collect
//     package — so that tick's fixture (every herdr call errors) can be
//     written against it.
//   - p6b: DONE. The artifact boundary is enforced in both layers — the git
//     exclude Start writes into the worktree before the agent launches, and
//     collect's backstop over the branch diff — mirroring the local
//     executor's git.go and collect.go.
//   - x6j: every substrate failure classified operational-or-verdict, and
//     resume across an upgrade held for a person.
//   - 5hz: Dispose reclaims by identity when a recorded workspace id is
//     stale. Here disposal uses the recorded id, tolerating "already gone".
package herdr
