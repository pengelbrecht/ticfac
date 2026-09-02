// Package reconcile is ticfac's reconciler: one epic, at concurrency one.
//
// It reads an epic's graph through the tk client, claims the next dispatchable
// tick, starts a job through an executor behind
// contracts/job-protocol.json's four operations, inspects on a bounded
// cadence, collects, boundary-checks, merges the attempt branch into the
// EpicRun integration branch, runs the integrated gate the target repository
// declares, closes the tick through the tracker, and only then cleans the
// attempt up.
//
// Four rules shape every line of it, and each is a rule this repository has
// already paid for once (SPEC Appendix A, contracts/lifecycle-invariants.json):
//
//   - EVERY EFFECT IS PRECEDED BY THE COMPARE-AND-SWAP THAT PROVES IT HAS NOT
//     HAPPENED. The dispatch marker is created on origin BEFORE the job is
//     started, so a reconciler that lost the race is refused by the repository
//     rather than by a lock it might have lost. The gate's evidence record is
//     created before its verdict is published. The tracker is the authority on
//     whether a tick is closed, so closure is guarded by reading it.
//
//   - AN IN-FLIGHT STATE IS SETTLED BY WHOEVER FINDS IT NEXT, from durable
//     evidence, never by trusting the claimer to come back. A restarted
//     reconciler on a fresh clone reads `.ticfac/` from origin, adopts a live
//     attempt BY IDENTITY, holds an attempt nobody can address, and settles a
//     finished one from the branch and the report.
//
//   - THE ONLY SHELL THIS PACKAGE RUNS IS THE TARGET REPOSITORY'S DECLARED
//     GATE — `[testing.commands]` in `.tick/runners.toml`, read by the
//     deliberately minimal reader in toml.go. Nothing else in this package
//     authorises a command line, which is why that reader refuses every other
//     table rather than parsing it.
//
//   - LONG WAITS ARE SPREAD ACROSS BOUNDED STEPS, and each leg re-derives its
//     state from durable facts rather than from the previous leg's memory.
//     Polling IS the keepalive, at a cadence pinned well under the substrate's
//     wipe threshold.
//
// Two things a run decides before it dispatches anything, and records so that
// what it decided outlives it:
//
//   - THE ROLE PROFILE. Every job is dispatched under a profile that is exactly
//     {executor, runner, model, prompt} — SPEC §4.5's Phase 1 rule, and nothing
//     else — resolved by internal/profile from this repository's `profiles/`
//     directory and routed by `[roles.*]` in the TARGET repository's
//     `.tick/runners.toml`, so an operator configures a run in the one place
//     they already configure `tk herd`. Three of the four reach the executor as
//     host configuration — the runner it launches, the model it launches on,
//     and the role prompt the worker prompt opens with — and the fourth, the
//     executor name, is what this phase checks it can honour. The role, the
//     model and the profile's digest go into the provenance of every record the
//     dispatch produces, and the dispatch marker records that the model and the
//     prompt were APPLIED, which is what makes "under which profile was this
//     decided, and did it actually run that way" answerable later. That reader
//     is internal/profile's own and NOT this package's gate reader: routing may
//     not become a second way to authorise a command line.
//
//   - REVIEW AND CLOSEOUT ARE JOBS, on the same executor, dispatched at the
//     CONTROLLER's state — the integration branch as origin has it — and the
//     review read-only, so its inability to write a ref is the issuer's
//     decision rather than the model's manners. What the reconciler acts on is
//     their role-result envelope (contracts/job-protocol.json), VALIDATED
//     before anything is decided on it and recorded as a decision. A malformed
//     envelope fails closed: the process tick stays open, because acting on an
//     answer nobody could validate is how an unchecked model response becomes a
//     verdict.
//
// The five lifecycle invariants the local subprocess executor names as not its
// own — A3 (the step cap), A4 (poll as keepalive), A11 (a struck-out unit is
// released by a person), A12 (budgets reported after clamping) and A13
// (evidence fingerprinted, publication checks freshness) — are this package's,
// and lifecycle_test.go replays the fixture's own sequences against the real
// reconciler for each of them.
package reconcile
