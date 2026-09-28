package subprocess

import (
	"fmt"
	"strings"
)

// An earlier attempt that stopped to ask (tick tyd).
//
// A worker that answers BLOCKED or NEEDS_CONTEXT used to stop the run for a
// person. Under the hand-off model the next human touch point is the ready
// PR, not a stopped run: the question goes to a STRONGER worker one tier up,
// and at the tier ceiling to a worker told to decide it under the
// repository's standing orders and log the decision. Only a question in a
// class the standing orders reserve for a person ("always ask") holds.
//
// The section below is how the re-dispatched worker is told. It is rendered by
// both executors from the same facts, like the prior-reports section, because
// the job contract is one contract however the agent is delivered.

// Escalation is what the dispatch hands the executor about the earlier attempt
// that stopped to ask. Attempt, Status and Question are the stopped worker's
// own answer; Report is where its archived report is (a host path, never
// recorded on origin); Tier is the tier this attempt was dispatched at.
//
// AtCeiling says the ladder is over: this dispatch is at the policy's ceiling
// (or the run has no ladder), so the instruction is to decide under the
// standing orders rather than to get past the question with a stronger model.
// StandingOrders is the repository's own standing-orders text, quoted into the
// prompt so the decision is made against the orders as they stand.
type Escalation struct {
	Attempt        int    `json:"attempt"`
	Status         string `json:"status"`
	Question       string `json:"question"`
	Report         string `json:"report,omitempty"`
	Tier           string `json:"tier,omitempty"`
	AtCeiling      bool   `json:"at_ceiling,omitempty"`
	StandingOrders string `json:"standing_orders,omitempty"`
}

// EscalationDecisionsHeading is the heading a decide-and-log worker logs its
// decisions under in its report: the run points the PR's reader at it.
const EscalationDecisionsHeading = "## Decisions"

// EscalationSection renders the prompt section for a dispatch that follows an
// attempt which stopped to ask. Empty when there is none.
func EscalationSection(e *Escalation) string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## An earlier attempt stopped to ask — decide and proceed\n\n")
	question := strings.TrimSpace(e.Question)
	if question == "" {
		question = "(it gave no reason)"
	}
	fmt.Fprintf(&b, "An earlier attempt stopped because: attempt %d answered STATUS: %s — %s\n", e.Attempt, e.Status, question)
	if e.Report != "" {
		fmt.Fprintf(&b, "Its report is at %s — read it first.\n", e.Report)
	}
	fmt.Fprintf(&b, "\nWhatever that attempt committed is already on your branch: continue from it rather than\n")
	fmt.Fprintf(&b, "redoing it. Nobody will answer the question for you mid-run: the next person to look is\n")
	fmt.Fprintf(&b, "the reviewer of the epic's PR. So decide and proceed, and say in your report what you\n")
	fmt.Fprintf(&b, "decided and why.\n\n")
	if !e.AtCeiling {
		fmt.Fprintf(&b, "You were dispatched one tier up (tier %s) to get past it. Look the answer up first —\n", e.Tier)
		fmt.Fprintf(&b, "the codebase, its history, .tick/config.md and the tick usually hold it.\n\n")
		return b.String()
	}
	fmt.Fprintf(&b, "You are at the top of the tier ladder, so the repository's STANDING ORDERS decide who\n")
	fmt.Fprintf(&b, "answers. A question in a class the orders mark \"decide and log\" is yours: decide it\n")
	fmt.Fprintf(&b, "under the orders' default, and log each decision in your report under a\n")
	fmt.Fprintf(&b, "`%s` heading — one line each: the question, your choice, the reason, and the\n", EscalationDecisionsHeading)
	fmt.Fprintf(&b, "standing-order class it falls under. Only a question in an \"always ask\" class may be\n")
	fmt.Fprintf(&b, "answered BLOCKED again, and that answer must name the class.\n\n")
	if orders := strings.TrimSpace(e.StandingOrders); orders != "" {
		fmt.Fprintf(&b, "The standing orders, from .tick/config.md:\n\n")
		for _, line := range strings.Split(orders, "\n") {
			fmt.Fprintf(&b, "> %s\n", line)
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}
