package subprocess

import "testing"

// The recorded rule (tick 19l): every role of the contract's closed
// vocabulary has a DECIDED answer to "is an empty branch a failure?", and a
// role the rule does not know is held to the check rather than released from
// it. The decision is the review's alone to be released: its deliverable is
// the answer plus findings, not a change.
func TestEveryRoleHasARecordedNoCommitsRule(t *testing.T) {
	t.Parallel()
	released := map[string]bool{"review-epic": false}
	for _, role := range roles {
		want, decided := released[role]
		if !decided {
			want = true
		}
		if got := NoCommitsIsFailure(role); got != want {
			t.Errorf("NoCommitsIsFailure(%s) = %t, want %t: a role of the closed vocabulary "+
				"carries a rule nobody recorded, or one recorded the other way", role, got, want)
		}
	}
}

// The conservative default: a role the rule has never heard of — a new role
// arriving in a contract this code has not adopted yet — is WORK, and work
// commits. A default that released unknown roles would let one new role
// quietly drop the commit requirement for every attempt dispatched under it.
func TestAnUnknownRolesNoCommitsIsAFailure(t *testing.T) {
	t.Parallel()
	if !NoCommitsIsFailure("some-future-role") {
		t.Error("an unknown role was released from the no-commits check: the default must fail loudly")
	}
}

// The one release, stated as its own test so removing it is a decision this
// file sees: a review's deliverable is its answer, and its grade takes the
// push credential away, so an empty branch is what a correct review looks
// like.
func TestAReviewsNoCommitsIsNotAFailure(t *testing.T) {
	t.Parallel()
	if NoCommitsIsFailure("review-epic") {
		t.Error("a review's empty branch is a failure: every review a run dispatches would be refused")
	}
}
