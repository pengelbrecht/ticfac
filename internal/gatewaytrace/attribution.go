package gatewaytrace

import "strconv"

// What the run's gateway rows BELONG to (tick kf4): a gateway cost number is
// a sum over CALLS — one row per model exchange, never one per attempt — so
// a status line that states the number beside a count of attempts is
// claiming a coverage the rows have to be able to name. The metering join
// names its attempts (the attempt key, the factory's own gatewayMetadata
// vocabulary), the classifier names itself (the caller key, the same run
// tag mechanism tick 24u made), and a row that names neither is from the
// window before the join named anything: this roll-up is the honest
// attribution a cost line can state, and nothing it does not count can be
// claimed.

// Coverage is what one run's rows say about what its gateway number covers.
type Coverage struct {
	// Calls is every row the number sums, whichever owner it names.
	Calls int `json:"calls"`
	// Attempts is how many DISTINCT dispatches the rows name — the count of
	// the attempt values the metering join stamped, not of the rows (one
	// attempt owns many calls). Zero when no row names an attempt.
	Attempts int `json:"attempts"`
	// OwnCalls is how many rows name the run itself rather than any
	// dispatch — the classifier's own calls. Measured money that belongs to
	// no worker attempt, but money the run did spend.
	OwnCalls int `json:"own_calls"`
	// UnnamedCalls is how many rows name neither an attempt nor a caller:
	// rows from before the join named its attempts, whose owner no
	// per-attempt claim can be made from. Their spend is in the number; the
	// attempts they belong to cannot be named.
	UnnamedCalls int `json:"unnamed_calls"`
}

// CoverageOf rolls one run's calls up into what its number can honestly
// claim: which dispatches the rows join, which rows are the run's own
// calls, and which rows predate the join's per-attempt names. A row that
// names BOTH an attempt and a caller reads as the attempt's — the metering
// join never stamps a caller, so a row that carries both is read by the
// stronger, more specific name.
func CoverageOf(calls []Call) Coverage {
	coverage := Coverage{Calls: len(calls)}
	attempts := make(map[int]bool, len(calls))
	for _, call := range calls {
		switch {
		case call.Attempt > 0:
			attempts[call.Attempt] = true
		case call.Caller != "":
			coverage.OwnCalls++
		default:
			coverage.UnnamedCalls++
		}
	}
	coverage.Attempts = len(attempts)
	return coverage
}

// attemptNumberOf reads the attempt a row's metadata names, the same key
// the factory's own proxy stamps (as a string, the shape the logs store
// every metadata value in). Zero — never an error — when the row names no
// attempt or names one that is not a number: a row no number can be read
// from is a row that names no attempt.
func attemptNumberOf(metadata map[string]string) int {
	attempt, err := strconv.Atoi(metadata["attempt"])
	if err != nil || attempt <= 0 {
		return 0
	}
	return attempt
}
