package gatewaytrace

import (
	"context"
	"testing"
)

// What one run's rows BELONG to (tick kf4): a gateway cost number is a sum
// over CALLS, and the honest status line has to say which dispatches those
// calls belong to — never silently the whole run. The metering join names
// its attempts (the same key the factory's own proxy stamps, the attempt the
// dispatch carried), the classifier names itself, and a row that names
// neither is from the window before the join named anything: the reader
// classifies what the row states, and the status model states only what the
// classification can defend.

// coverageRow is one log row the metering join could have stamped, with the
// metadata the caller states.
func coverageRow(id string, metadata map[string]string) map[string]any {
	row := logRowJSON(id, "2026-10-05T18:17:31.133Z", 700, 512, 4, 0.0001272)
	row["metadata"] = metadata
	return row
}

// TestCallsReadTheAttemptAndCallerTheMetadataNames: the row's own metadata
// is the only place a call's attribution lives — the metering join stamps
// the attempt the dispatch carried (the factory's own gatewayMetadata
// vocabulary), the classifier stamps a caller, and the reader reads both
// back so the cost line can name what its number covers.
func TestCallsReadTheAttemptAndCallerTheMetadataNames(t *testing.T) {
	t.Parallel()
	client, _ := fakeGateway(t, func(request gatewayRequest) (int, any) {
		return 200, map[string]any{"success": true, "result": []any{
			coverageRow("call-1", map[string]string{"run_id": "run_1", "tick_id": "kf4", "attempt": "3"}),
			coverageRow("call-2", map[string]string{"run_id": "run_1", "caller": "jev"}),
			coverageRow("call-3", map[string]string{"run_id": "run_1"}),
		}}
	})
	calls, err := client.Calls(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("the gateway read failed: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("the read answered %d calls, want the 3 rows", len(calls))
	}
	if calls[0].Attempt != 3 || calls[0].TickID != "kf4" {
		t.Errorf("call-1 carries attempt %d tick %q, want the metadata's own attempt 3 on tick kf4", calls[0].Attempt, calls[0].TickID)
	}
	if calls[0].Caller != "" {
		t.Errorf("call-1 carries caller %q, want none: a worker attempt's row names the attempt, not a caller", calls[0].Caller)
	}
	if calls[1].Attempt != 0 || calls[1].Caller != "jev" {
		t.Errorf("call-2 carries attempt %d caller %q, want no attempt and the classifier's own caller", calls[1].Attempt, calls[1].Caller)
	}
	if calls[2].Attempt != 0 || calls[2].Caller != "" {
		t.Errorf("call-3 carries attempt %d caller %q, want neither: a row from before the join named anything states neither", calls[2].Attempt, calls[2].Caller)
	}
}

// TestCoverageOfNamesWhatTheRowsBelongTo: the roll-up the status model's
// cost line reads — how many calls the number sums, how many distinct
// dispatches those calls name, how many are the run's own calls, and how
// many name nothing at all (the window before the join named its attempts,
// which no per-attempt claim can be made from).
func TestCoverageOfNamesWhatTheRowsBelongTo(t *testing.T) {
	t.Parallel()
	client, _ := fakeGateway(t, func(request gatewayRequest) (int, any) {
		return 200, map[string]any{"success": true, "result": []any{
			// Two calls of one attempt, one call of another.
			coverageRow("call-1", map[string]string{"run_id": "run_1", "attempt": "3"}),
			coverageRow("call-2", map[string]string{"run_id": "run_1", "attempt": "3"}),
			coverageRow("call-3", map[string]string{"run_id": "run_1", "attempt": "7"}),
			// The classifier's own call, and a pre-attempt-tag row.
			coverageRow("call-4", map[string]string{"run_id": "run_1", "caller": "jev"}),
			coverageRow("call-5", map[string]string{"run_id": "run_1"}),
		}}
	})
	calls, err := client.Calls(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("the gateway read failed: %v", err)
	}
	coverage := CoverageOf(calls)
	if coverage.Calls != 5 {
		t.Errorf("coverage counts %d calls, want every row the number sums", coverage.Calls)
	}
	if coverage.Attempts != 2 {
		t.Errorf("coverage counts %d attempts, want the 2 distinct dispatches the rows name (3 and 7, 3 twice)", coverage.Attempts)
	}
	if coverage.OwnCalls != 1 {
		t.Errorf("coverage counts %d own calls, want the 1 row that names the run itself", coverage.OwnCalls)
	}
	if coverage.UnnamedCalls != 1 {
		t.Errorf("coverage counts %d unnamed calls, want the 1 row that names nothing", coverage.UnnamedCalls)
	}

	// No rows at all: everything zero, the shape the caller reads as "the
	// gateway answered nothing" — never a negative or an invented count.
	if empty := CoverageOf(nil); empty != (Coverage{}) {
		t.Errorf("the coverage of no calls is %+v, want the zero value", empty)
	}
}
