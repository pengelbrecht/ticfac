// Package runstate is the `.ticfac/` run-state store: the reader and writer of
// the records a reconciler leaves behind, implemented exactly as
// contracts/ticfac-run-state.json specifies them.
//
// Four records, one file each, under `.ticfac/runs/<run-id>/`:
//
//	checkpoint.json        one per run, sha-guarded update, written on STATE CHANGE
//	attempts/<n>.json      one per dispatch, create-if-absent
//	decisions/<n>.json     one per role-job exchange, create-if-absent
//	evidence/<key>.json    one per evidence key, create-if-absent
//
// The evidence record is PLACED here and DEFINED by contracts/job-protocol.json
// (`ticfac.evidence.v1`, nested provenance, closed). This package therefore
// owns where the file goes and how it is written, and copies no schema.
//
// # Durable means pushed
//
// A local commit is not durable — the lesson of a sandbox that dies holding its
// work. Every write here is commit-and-push as ONE operation on the EpicRun
// integration branch, so there is no window in which a record exists in a
// working tree and nowhere else. The store never touches a working tree at all:
// it builds a tree with git plumbing over the fetched origin commit and pushes
// it. The only local ref it ever moves is the integration branch, and only from
// CommitLocal, which exists to make the contract's "a local commit is not
// durable" sequence executable.
//
// # The compare-and-swap is against origin
//
// Two modes, and each record kind gets exactly one of them
// (contracts/ticfac-run-state.json §cas.modes):
//
//   - create_if_absent — the path must be absent from the ORIGIN ref. The
//     file's existence is the proof the effect already happened, so a refused
//     create means another reconciler already dispatched and this one must not.
//   - sha_guarded_update — origin's blob sha for the path must equal the one
//     this writer fetched. A refusal means the writer's view is stale.
//
// On a local host the guard is `git push --force-with-lease`. The lease is on
// the branch ref, which is coarser than the per-path guard the contract states,
// so a refused lease is re-examined against origin: if the PATH guard has
// genuinely been lost the outcome is the contract's typed conflict, and if the
// ref merely moved underneath us for some other path the commit is rebuilt on
// the new head. That is the difference between reconciling and retrying
// blindly, which the contract forbids.
//
// A conflict is a typed Outcome, never an error: ConflictExists,
// ConflictStaleSHA and ConflictMissingBase are answers about the world, and the
// caller must not perform the effect they guard. Errors are reserved for a git
// that would not run.
//
// # Terminal state
//
// A run that reaches completed, failed or cancelled gets the tag
// `ticfac/run-<run-id>` on origin, so the full history stays reachable for a
// post-mortem after the run branch is deleted — and a failed run's history is
// as reachable as a successful one's.
package runstate
