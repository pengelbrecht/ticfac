// Package gating decides whether a discovered finding gates an epic's
// definition of done where the done CANNOT YET BE RUN — the prediction half of
// the gvc epic's two-tier decision (tick bse).
//
// THE TWO TIERS, and why this package exists at all. Running the done is an
// oracle only for acceptance items that are RUNNABLE NOW: an item bound to a
// command in [evidence.acceptance] is proved by running it, and that verdict
// is authoritative — no classifier overrides it (that oracle is pzp). The
// case that defeats the oracle is not exotic; it is the ordinary one mid-epic.
// An item cannot be checked yet because the epic has not built the thing it is
// about, and — harder — a finding can be ITSELF A PREDICTION: nothing is red,
// no test failed, and deciding whether it gates the done is reasoning about
// prose against prose. That is what a classifier is for, and what running
// something cannot do. This package predicts over the UNVERIFIED items
// (internal/acceptance's third state, never collapsed into false), and a
// runnable item is not even offered to it.
//
// THE QUESTION, which is Jev-shaped on purpose: one Choice over THIS EPIC'S
// OWN acceptance items plus 'none' — a closed enum per epic, Jev's sweet spot
// — asked through the generalised jev core (the same wire the work-type
// classification rides, which tick bse generalised for exactly this). The
// answer doubles as the item id that absorption has to name, which the epic's
// acceptance requires for free.
//
// THE THRESHOLD IS DELIBERATELY CHEAP, and the asymmetry that sets it lives
// in code beside the number, where someone will later be tempted to tune it
// the other way: a false positive costs the epic some work it did not need; a
// false negative closes an epic whose goal is unmet, in a factory where nobody
// is watching — the failure this epic exists to prevent. Those are not the
// same cost, so the bar for gating sits well below a coin flip and the
// decision spends the probability MASS on the items rather than the argmax: a
// classifier that says "probably fine" still absorbs when the mass says
// otherwise.
//
// THE VERDICT IS RECORDED AS PREDICTED, never as observed. A retro that cannot
// tell a guess from a measurement cannot report honestly, and scoring
// predictions against what the done later did (jlv) is impossible if the two
// are indistinguishable — so [Verdict.Basis] is a typed field, and this
// package only ever writes BasisPredicted. The OBSERVED value exists here so
// the oracle's record is the same shape; it is the oracle's to write.
//
// THE FALLBACK: a classifier that cannot be reached, refuses, or answers
// nothing means NO PREDICTION EXISTS — and the decision absorbs anyway rather
// than deferring, because deferring would stop an unattended run on the one
// actor only a person can play, and that stop is the more expensive failure by
// the same asymmetry. The fallback is recorded honestly too: still a
// prediction, never an observation, with [Verdict.Fallback] naming why no
// model made it.
//
// What this package is NOT: it does not run anything (pzp), it does not absorb
// anything (npq), and it writes nothing anywhere — the caller records the
// verdict on the run branch. An epic whose acceptance carries no items never
// reaches a question at all: enumerating a done is klq's, and refusing to
// decide without one is the refusal's, not this package's.
package gating
