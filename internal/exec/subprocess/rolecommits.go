package subprocess

// The recorded per-role rule for the `no-commits` verdict (tick 19l).
//
// Until the pwp run made the question visible, "a branch with no commits" was
// a failure for EVERY role incidentally — the check knew no role — and the feed
// line for a role job read "closeout-epic answered failed (no-commits)" over a
// report that said DONE_WITH_CONCERNS: the run's verdict attributed to the
// worker. The two facts underneath are different claims by different parties:
//
//   - a role whose deliverable is its ANSWER plus findings (the review) may
//     legitimately commit nothing — an empty branch is what a correct one
//     looks like;
//   - a role whose deliverable includes a CHANGE to the repository it was
//     dispatched over may not — an empty branch is an undelivered deliverable,
//     whatever the answer says.
//
// Which is which is a decision about the ROLE, so it is recorded HERE, per
// role of the contract's closed vocabulary (contracts/job-protocol.json
// $defs.role), and both collects enforce it rather than each deciding for
// itself: classify mints `no-commits` only for a role whose rule says an empty
// branch IS a failure, so a review's empty branch collects as
// ready-to-merge and the reconciler's role-job collect can act on the verdict
// knowing it already carries the role's own rule.
//
// The recorded decision, per role:
//
//   - review-epic — NOT a failure. A review is dispatched read-only, with no
//     push credential at all (the reconciler's source grade), and its whole
//     deliverable is the validated envelope plus its findings. Classifying an
//     empty branch as a failure would refuse every review a run dispatched.
//   - closeout-epic — IS a failure. The close-out is dispatched at the write
//     grade and its deliverable is the record it leaves in the repository —
//     the retro, and the learnings the boundary permits it to write — so a
//     close-out over an empty branch did not do its writing work.
//   - implement-tick — IS a failure. Source and tests are the deliverable; an
//     implementation tick that committed nothing delivered nothing.
//   - plan-epic, plan-repair, resolve-conflict — IS a failure. Each one's
//     deliverable is a change to the repository it was dispatched over, and
//     none of the three is dispatched read-only.
//   - triage-failure, evaluate-goal — IS a failure. Neither runs through a
//     collect in this codebase today (a triage is a recorded decision; an
//     evaluation is an answer the goal flow reads), so the conservative
//     default holds them: a role nobody decided otherwise is work, and a
//     worker that committed nothing is held to the check rather than
//     released from it.
//   - an unknown role — IS a failure, for the same conservative reason: the
//     check is the thing that fails loudly when a new role arrives without a
//     recorded rule, rather than the new role quietly releasing every
//     attempt from the commit requirement.

// NoCommitsIsFailure is the recorded rule: whether an attempt whose branch
// carries no commit beyond its base is a FAILURE for this role. It is the one
// place the decision lives; the executors' collects read it, and the
// reconciler's role-job collect acts on a verdict that already carries it.
func NoCommitsIsFailure(role string) bool {
	switch role {
	case "review-epic":
		return false
	case "plan-epic", "implement-tick", "triage-failure", "plan-repair",
		"resolve-conflict", "closeout-epic", "evaluate-goal":
		return true
	default:
		return true
	}
}
