// Package acceptance turns a container's definition of done from a paragraph
// into ENUMERABLE ITEMS, and refuses to decide anything for a container whose
// acceptance carries none.
//
// Absorption — the gvc epic's whole subject — is decided by the done: whether
// an epic may absorb a defect it discovers is the question "can the done still
// be demonstrated while the finding stands", and that question has nothing to
// point at when the done is prose. So the done has to be a THING: items with
// stable ids, each one a discrete, testable fact.
//
// WHERE THE IDS COME FROM. The [A<n>] marks are written by a person, at goal
// design time, into the container's acceptance criteria (ticks' goal-design.md:
// the fact sheet). Parse reads them; it never invents them. An id is stable
// and unique — within one container Parse enforces uniqueness, and across
// containers the author keeps them unique because [evidence.acceptance] is a
// single namespace: a reused id silently rebinds a fact to another container's
// command. An acceptance with no marks parses to zero items, and zero items is
// the refusal's to answer for, not the parser's: prose is not an error, it is
// an undecidable done.
//
// THE TWO ENDS MEET HERE. One end is the marks in the tracker's acceptance
// criteria; the other is [evidence.acceptance] in .tick/runners.toml, which
// maps an item id to the id of the one command that proves it
// (internal/runconfig carries and validates that table). Resolve joins them:
// an item whose id is mapped is RUNNABLE, carrying the command id; an item
// with no mapping is UNVERIFIED. That is a state, deliberately not a boolean —
// collapsing "no proof ran" into "false" would silently make every unrunnable
// item non-gating, which is the false negative gvc exists to prevent.
// UNVERIFIED is the state that later decides classifier versus oracle: an
// oracle (pzp) runs the runnable items, a classifier (bse) predicts over the
// unverified ones, and neither may read "not proven" as "not required".
//
// THE REFUSAL, which this package ships next to the parser so the absorption
// machinery cannot ship without it: an epic whose acceptance carries no items
// cannot decide absorption at all. Decide returns a Refusal for it — refuse to
// absorb, and say why — rather than guessing on prose. A run that would
// otherwise absorb a finding into an epic with an unmarked done must take the
// refusal path, and the refusal's text names exactly what is missing and how
// to fix it.
//
// What this package is in the gvc epic: klq's enumeration and its refusal —
// the THING and the refusal to decide without one. Running the runnable items
// is pzp, predicting the unverified ones is bse, absorbing a gating finding is
// npq, and none of them import each other's judgement. Nothing here runs a
// command, asks a model, or writes anything anywhere: Decide reads text and a
// table and answers.
package acceptance
