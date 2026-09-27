// Package gating is where the gvc epic's two-tier gating decision lives: the
// shared verdict record both tiers write, the prediction half that guesses
// over what cannot yet be run (tick bse), and the observed half that RUNS
// what can (tick pzp).
//
// THE RULE THE ORACLE ESTABLISHES, and nothing later may weaken it: WHERE AN
// ITEM CAN BE RUN, RUNNING WINS. No classifier overrides an observation, and
// a prediction that disagrees with a run is the prediction being wrong — that
// is the whole reason the oracle is built first. It is enforced structurally
// in both halves, never left to instruction-following: the Predictor never
// offers a runnable item to its classifier at all, and every verdict the
// Oracle writes carries [BasisObserved] — a typed field, never a convention —
// so a record a command answered and a record a model guessed at can never
// drift into indistinguishable prose.
//
// THE TWO TIERS, and why there are two. Running the done is an oracle only
// for acceptance items that are RUNNABLE NOW: an item bound to a command in
// [evidence.acceptance] is proved by running it. The case that defeats the
// oracle is not exotic; it is the ordinary one mid-epic. An item cannot be
// checked yet because the epic has not built the thing it is about, and —
// harder — a finding can be ITSELF A PREDICTION: nothing is red, no test
// failed, and deciding whether it gates the done is reasoning about prose
// against prose. That is what a classifier is for, and what running something
// cannot do. So the tiers split on internal/acceptance's third state:
// the Oracle runs every RUNNABLE item and returns the unverified ones
// unresolved, and the Predictor predicts over every UNVERIFIED item and is
// never shown a runnable one.
//
// THE ORACLE (oracle.go): given a finding and an enumerated done, it runs
// every runnable item's bound command through the [Runner] seam — command
// IDS, never shell; the table in .tick/runners.toml is what authorises a
// command line, and this package never sees one. A command that fails is
// GATING, observed, no judgement needed — the worked case that wrote the
// criterion: two fixtures made the gate red at base, so no tick could close
// behind it and the done was unreachable. A command that passes demonstrates
// its item, and the verdict is NOT GATING BY OBSERVATION — scoped to what
// ran, and naming the items it refuses to decide rather than letting them be
// read as safe. A command that cannot run, was killed or was skipped
// produces NO evidence about its item: `error` is not `fail` and `skipped` is
// not `pass`, and the item comes back unresolved rather than assumed either
// way. An oracle that guesses is not an oracle. The verdict is keyed by the
// commit the runner reports it ran on — the thing that runs is the thing
// that knows what it ran on — because an observed record keyed by nothing is
// a timestamped opinion, and the record has to mean something on a
// re-derivation.
//
// THE PREDICTOR (gating.go): the question is Jev-shaped on purpose — one
// Choice over THIS EPIC'S OWN not-yet-runnable items plus 'none', with
// confidence, asked through the generalised jev core. The answer doubles as
// the item id that absorption has to name, which the epic's acceptance
// requires for free.
//
// THE THRESHOLD IS DELIBERATELY CHEAP, and the asymmetry that sets it lives
// in code beside the number, where someone will later be tempted to tune it
// the other way: a false positive costs the epic some work it did not need;
// a false negative closes an epic whose goal is unmet, in a factory where
// nobody is watching — the failure this epic exists to prevent. Those are not
// the same cost, so the bar for gating sits well below a coin flip and the
// decision spends the probability MASS on the items rather than the argmax: a
// classifier that says "probably fine" still absorbs when the mass says
// otherwise.
//
// THE VERDICT IS RECORDED AS WHAT IT IS, never as the other thing. A retro
// that cannot tell a guess from a measurement cannot report honestly, and
// scoring predictions against what the done later did (jlv) is impossible if
// the two are indistinguishable — so [Verdict.Basis] is a typed field: the
// Oracle writes only BasisObserved, the Predictor only BasisPredicted, and
// the two share the one [Verdict] shape so the records cannot drift apart.
//
// THE FALLBACK: a classifier that cannot be reached, refuses, or answers
// nothing means NO PREDICTION EXISTS — and the decision absorbs anyway rather
// than deferring, because deferring would stop an unattended run on the one
// actor only a person can play, and that stop is the more expensive failure
// by the same asymmetry. The fallback is recorded honestly too: still a
// prediction, never an observation, with [Verdict.Fallback] naming why no
// model made it. The oracle's own no-answer states are named outcomes the
// same way — nothing to run, no runner, no evidence — and never a stop and
// never a guess.
//
// What this package is NOT: it does not absorb anything (npq owns the
// decision that combines the tiers), and it writes nothing anywhere — the
// caller records the verdict on the run branch. An epic whose acceptance
// carries no items never reaches a command or a question: enumerating a done
// is klq's, and refusing to decide without one is the refusal's, not this
// package's.
package gating
