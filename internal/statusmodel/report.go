package statusmodel

// The per-tick report drill-in (epic hn6, wave 1 — tick r5i): what a person
// reads when they press enter on a tick — the report's own summary and its
// diff stats (hn6 rule 6). Wave 1 DECLARES the shape and leaves it null on
// every tick: null is the honest "the report was not read", and the model
// says exactly that. decorateReports is the no-op the wave-2 report tick
// fills; the production reader hangs on Sources.Report, nil-safe, and the
// fill cannot invent a field because the contract already pins this one.

// ReportInput is one (tick, attempt) report as its reader answers it: the
// STATUS-bearing summary the report opens with, the diff's three counts,
// and whether the diff was read at all — the fact that separates "no
// changes" from "not looked", which the model owes the reader.
type ReportInput struct {
	Summary    string
	Files      int
	Insertions int
	Deletions  int
	DiffRead   bool
}

// decorateReports is the wave-1 no-op: every tick's report stays null until
// the wave-2 report tick reads the attempt branches' reports. It exists so
// that fill has a home that is not build.go.
func decorateReports(src Sources, m *Model) {
}

// AttemptReports is the production report reader: it answers a (tick,
// attempt) report from the run's records on the repo it is given. Wave 1
// returns the stub — a reader that answers nothing, which the model states
// as report null. The signature is the wave-2 shape: the repo and the run
// whose attempt branches carry the reports.
func AttemptReports(repo, runID string) func(tickID string, attempt int) *ReportInput {
	return func(tickID string, attempt int) *ReportInput {
		return nil
	}
}
