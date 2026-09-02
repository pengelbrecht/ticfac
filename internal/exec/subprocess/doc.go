// Package subprocess is ticfac's local subprocess executor: the four
// operations of contracts/job-protocol.json — start, inspect, cancel, collect
// — over a git worktree per attempt and a headless runner process
// (claude | codex | pi).
//
// Three properties are the whole design, and each one is a rule this
// repository has already paid for once (SPEC Appendix A, and the bundle's
// contracts/lifecycle-invariants.json):
//
//   - COMPLETION IS DURABLE EVIDENCE ONLY. A process that exited is settled,
//     which is not the same thing as done: the completion contract is the
//     branch and the RESULT report (job-protocol.json `rules.completion_contract`).
//     A settled attempt with no report is its own status — `failed`, terminal,
//     with a message that says so — and is never collapsed into either
//     "succeeded" or "still running". Terminal output is diagnostic material.
//
//   - THE EXECUTOR OWNS THE RESULT PATH. The worker is told an ABSOLUTE path
//     inside its own attempt worktree, derived from JobSpec.artifact_prefix, so
//     a worker that cds to /tmp and writes its report there still writes it
//     where collect reads it. A path the worker computes from its cwd is a
//     report that goes missing exactly when the run went strangely.
//
//   - IDENTITY IS (repo key, job id, attempt). The repo key is the local
//     repository's git common directory, so the same tick id running in two
//     checkouts on one machine gets two worktrees, two branches and two state
//     directories rather than one race. job_id stays OPAQUE — the contract says
//     the reconciler owns its shape — so the attempt number comes from the
//     caller, never from parsing it.
//
// The executor is a HOST, not a protocol participant beyond those four
// operations: which runner to launch, WHICH MODEL to launch it on, the ROLE
// PROMPT the worker prompt opens with, where state lives and which remote to
// push to are all configuration (Options), because the protocol records are
// closed and a field invented here would be a field the reconciler ignores.
// The caller resolves those three from its role profile; this package applies
// them — the model through the runner's own flag, in the one table where the
// runners differ, and a model routed to a runner with no such flag is refused
// rather than dropped.
package subprocess
