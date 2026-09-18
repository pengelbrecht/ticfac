package subprocess

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// A re-dispatched attempt's predecessors (tick nvn), across runs (tick n4h).
//
// Tick 35h made an attempt's report survive teardown, as report.md beside the
// attempt record in the executor state directory. That is the precondition;
// this is the use. Before it, every re-dispatched attempt started blind: in
// the ticks pwp run, close-out attempts 11 to 15 each re-derived the same
// impasse and each surfaced a different subset of findings, because none could
// see what the last had concluded.
//
// The dispatch hands the executor the archived reports of this tick's earlier
// attempts, and the prompt a worker is handed names them — paths, status
// lines, newest first — framed as prior analysis to VERIFY rather than
// instructions to follow. The framing is not decoration: a predecessor's
// report can be wrong, and an attempt that trusts it inherits the error.

// PriorReport is one predecessor attempt's archived report, as a dispatch
// hands it to the executor that renders the worker prompt. Attempt is the
// predecessor's own attempt number, Path is where its report is archived —
// report.md beside the attempt record, which teardown does not touch — and
// Status and Detail are the final status line its report ended with.
//
// Run and Dispatched are the two facts a list that spans runs needs (tick
// n4h). Run is the run id the predecessor was dispatched under, set when
// that run was not this one: attempt numbers count a RUN's dispatches, so
// attempt 1 of a previous run and attempt 1 of this one are different
// attempts with the same number, and a worker handed both has to be able to
// tell which is which. Dispatched is when the predecessor was dispatched
// (its attempt record's issued_at), set when that record reads — the one
// fact that orders a list across runs, because attempt numbers are a run's
// own count and cannot be compared between two of them.
//
// The type lives in this package because the seam does: the reconciler
// discovers the predecessors, the executor renders the prompt, and both
// executors (this one and herdr) render the same section from the same facts.
type PriorReport struct {
	Attempt int    `json:"attempt"`
	Run     string `json:"run,omitempty"`
	Path    string `json:"path"`
	Status  string `json:"status,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Dispatched is RFC3339, as the attempt record stores it; empty when the
	// record could not be read, which orders it after every dated entry.
	Dispatched string `json:"dispatched,omitempty"`
}

// label is how a predecessor is named in the prompt: this run's own attempts
// by number alone, another run's with the run that dispatched it — because
// the prompt already names the worker's own run in its job line, so an
// unnamed attempt reads as this run's, and a named one is distinguishable
// from it precisely because the others are not.
func (p PriorReport) label() string {
	if p.Run == "" {
		return fmt.Sprintf("attempt %d", p.Attempt)
	}
	return fmt.Sprintf("run %s attempt %d", p.Run, p.Attempt)
}

// precedes says this predecessor is listed ahead of that one. Dated entries
// run newest first — dispatch times are the same kind of fact whatever run
// issued them, which attempt numbers are not — and an entry whose record
// could not be read is the least fresh thing the list can say, so it sorts
// after every dated one rather than between them by its number. A list with
// no dates at all falls back to attempt numbers, which is every list this
// seam ever handed over before lists could span runs.
func (p PriorReport) precedes(q PriorReport) bool {
	pt, pOK := parseDispatched(p.Dispatched)
	qt, qOK := parseDispatched(q.Dispatched)
	switch {
	case pOK && qOK && !pt.Equal(qt):
		return pt.After(qt)
	case pOK != qOK:
		return pOK
	}
	return p.Attempt > q.Attempt
}

func parseDispatched(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || at.IsZero() {
		return time.Time{}, false
	}
	return at, true
}

// statusLine renders the predecessor's answer the way its own report ended:
// the status word, and the detail after it when it carried one. A report with
// no recognisable status line — a predecessor that settled without ever saying
// what happened — is named as such rather than rendered as an empty verdict,
// because "we do not know what it concluded" and "it concluded nothing" are
// different things for a worker deciding how much to trust it.
func (p PriorReport) statusLine() string {
	switch {
	case p.Status == "":
		return "(no recognisable status line)"
	case p.Detail == "":
		return "STATUS: " + p.Status
	default:
		return "STATUS: " + p.Status + " — " + p.Detail
	}
}

// PriorReportsSection is the prompt section that names what a tick's earlier
// attempts found. It is exported because BOTH executors render it — the worker
// prompt is the same job contract however the agent is delivered, and a
// herdr worker whose prompt framed its predecessors differently from a local
// one would be two answers to the same question.
//
// The list is rendered NEWEST FIRST, whatever order it arrives in: the most
// recent attempt is the one most worth reading first, and the order is a
// property of the prompt rather than of whoever gathered the reports. Across
// runs that order comes from dispatch times, not attempt numbers (tick n4h):
// the numbers are each run's own count, and the oldest run's attempt 9 is not
// newer than this run's attempt 1.
func PriorReportsSection(prior []PriorReport) string {
	if len(prior) == 0 {
		return ""
	}
	ordered := append([]PriorReport{}, prior...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].precedes(ordered[j]) })

	var b strings.Builder
	fmt.Fprintf(&b, "## Prior attempts — analysis to verify, not instructions to follow\n\n")
	fmt.Fprintf(&b, "This tick has earlier attempts, and their reports survive. Read each one before you\n")
	fmt.Fprintf(&b, "start: it is prior ANALYSIS of this same tick, to check against this repository as it\n")
	fmt.Fprintf(&b, "stands — not a conclusion to follow. A predecessor can be wrong, and an attempt\n")
	fmt.Fprintf(&b, "that trusts its report inherits the error. Newest first:\n\n")
	for _, p := range ordered {
		fmt.Fprintf(&b, "- %s — %s — report: %s\n", p.label(), p.statusLine(), p.Path)
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}
