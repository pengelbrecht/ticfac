package statusmodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// The worker's harness-kind reader (epic hn6, tick 5uq): which agent kind
// the executor launched for one (tick, attempt) — the one fact that says
// which transcript layout the worker writes, and so the key the activity
// seam's mapping reads.
//
// The durable attempt record cannot name it: its provenance carries the
// MODEL and the EXECUTOR, and neither says which harness runs the worker —
// the bare alias "opus" that every claude review and closeout dispatch
// carries names the model, and "herdr" names the executor that runs any
// kind. The activity window used to be addressed by the model string and
// mapped by whether it names claude, so a live claude worker's transcript
// was never read: the panel showed an empty sparkline for the only worker
// of a close-out while its Claude Code transcript existed on the machine.
//
// The kind lives where the worker does: the attempt record every executor
// of this protocol keeps in the dispatch's state directory — the same
// record the handle reader walks (handle.go, zl1). herdr spells it `kind`
// (the herdr agent kind), the local supervisor `runner` (claude | codex |
// pi). Read through the Sources.Runner seam, it is the LIVE word: it wins
// over the durable record's model-or-executor spelling, exactly as the
// handle reader's name wins over the marker's frozen copy.

// WorkerRunner is the production harness-kind reader for a LOCAL run: it
// answers one (tick, attempt) worker's agent kind from the attempt record
// in the dispatch's state directory on this machine — located by walking
// for the record rather than recomputing any executor's internal naming,
// exactly as the handle reader locates the same directory. Nil when nothing
// on this machine names the kind: no state directory, no record, a record
// that names another tick, or one that spells no kind — never a guess from
// the model or the job id, which name anything but the harness.
//
// Nothing is cached, for the same reason the handle reader caches
// nothing: a live worker's record is on this machine from the dispatch,
// and a per-frame file read is what a two-second redraw can afford.
func WorkerRunner(runID string) func(tickID string, attempt int) *string {
	return func(tickID string, attempt int) *string {
		for _, root := range reportStateRoots() {
			state, ok := findAttemptState(filepath.Join(root, runID, tickID, strconv.Itoa(attempt)))
			if !ok || !attemptNamesTick(state, tickID) {
				continue
			}
			if kind, ok := attemptRunnerKind(state); ok {
				return &kind
			}
		}
		return nil
	}
}

// attemptRunnerKind reads the harness kind out of the executor's own
// attempt record in one state directory. The record is read LOOSELY, the
// way the rest of this package walks the same file: this is a reader of
// the executors' shared file name, not an importer of their shapes — herdr
// spells the agent kind `kind`, the local supervisor the runner it
// launched `runner`, and a record spelling neither names no harness.
func attemptRunnerKind(state string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(state, "attempt.json"))
	if err != nil {
		return "", false
	}
	var record struct {
		Kind   string `json:"kind"`
		Runner string `json:"runner"`
	}
	if json.Unmarshal(raw, &record) != nil {
		return "", false
	}
	switch {
	case record.Kind != "":
		return record.Kind, true
	case record.Runner != "":
		return record.Runner, true
	}
	return "", false
}
