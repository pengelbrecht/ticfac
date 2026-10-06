package runstate

import (
	"errors"
	"testing"
)

// Tick d9d's mark: a foreign record this binary cannot decode — one written
// by a NEWER binary (a field this one cannot express, refused by the closed
// schemas) or corrupt — must be distinguishable from a read that failed
// operationally. The boot sweep and the claim staleness both read OTHER
// runs' checkpoints off the shared integration branch, and the difference is
// the whole decision: an unreadable record says nothing and the reader moves
// on, a broken read is a fault to fail on. Before the mark existed the two
// were one error, and one unreadable checkpoint aborted every boot of the
// epic at its start, forever — a record no run of the epic can fix.
func TestAnUnreadableForeignRecordIsMarkedSoACallerCanTellItFromAFault(t *testing.T) {
	o := newOrigin(t)
	editor := o.actor("editor", "r-edit")

	// A newer binary's checkpoint: every field this binary knows, plus one it
	// cannot express — the exact shape a factory ahead of the local binary
	// leaves behind on a shared integration branch.
	const newer = `{"schema_version": 3, "run_id": "r-newer", "epic_id": "zso",
	 "state": "failed", "reason": "the factory recorded the run failed",
	 "factory_epoch": "written by a binary newer than this one"}`
	if _, err := editor.CreateIfAbsent(CheckpointPath("r-newer"), []byte(newer)); err != nil {
		t.Fatalf("seed the newer binary's checkpoint: %v", err)
	}
	// The corrupt twin: bytes no binary can read.
	if _, err := editor.CreateIfAbsent(CheckpointPath("r-corrupt"), []byte("} not a checkpoint")); err != nil {
		t.Fatalf("seed a corrupt checkpoint: %v", err)
	}

	reader := o.actor("reader", "r-read")
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"r-newer", "r-corrupt"} {
		_, _, err := reader.ForeignCheckpoint(runID)
		if err == nil || !errors.Is(err, ErrUnreadable) {
			t.Errorf("run %s's unreadable checkpoint reads %v, want it marked %q: a caller must be able "+
				"to tell a record that says nothing from a read that failed", runID, err, ErrUnreadable)
		}
	}

	// The mark belongs to the RECORD, not to every error: a run id that never
	// reaches a path is a refusal, and a reader that treated it as unreadable
	// would skip a sibling on a bad argument instead of being told about it.
	if _, _, err := reader.ForeignCheckpoint("../escape"); err == nil || errors.Is(err, ErrUnreadable) {
		t.Errorf("the segment refusal reads %v, want an error WITHOUT the %q mark", err, ErrUnreadable)
	}
}
