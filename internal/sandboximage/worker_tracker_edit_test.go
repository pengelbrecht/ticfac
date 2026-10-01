//go:build !windows

package sandboximage

import (
	"strings"
	"testing"
)

// A tick whose deliverable is a tracker edit (hn6 yjq, PR #186): the worker
// commits nothing — `.tick/` is never a worker's to write — and PROPOSES the
// edit in a tracker-edits block, which the run validates and applies as the
// attempt's delivery. The container's no-work exit (10) said the opposite of
// what happened; a DONE answer carrying the block is a delivery, and exits 0.
// Whether the proposal holds is collect's to decide, as for any delivery.

const trackerEditsResult = "```tracker-edits\n" +
	`[{"tick": "1vn", "field": "acceptance_criteria", "value": "[A1] one;\n[A2] two"}]` + "\n```\n\n"

func TestWorkerWhoseOnlyDeliveryIsATrackerEditExitsZero(t *testing.T) {
	f := newWorkerFixture(t)
	delete(f.env, "TICKS_TEST_WORKER_COMMIT")
	f.env["TICKS_TEST_WORKER_RESULT"] = trackerEditsResult + "STATUS: DONE"

	out, code := f.run()
	if code != 0 {
		t.Fatalf("a worker that proposed a tracker edit and answered DONE gave exit %d, want 0:\n%s", code, out)
	}
	mustContain(t, out, "tracker-edits", "the container saying what the delivery is")
	report, ok := f.remoteFile(WorkerBranch(f.epic, f.tick), WorkerResultFile(f.tick))
	if !ok || !strings.Contains(report, "```tracker-edits") {
		t.Fatalf("the report carrying the proposal did not reach origin:\n%s", report)
	}
}

// A block behind an answer that is NOT done — the worker stopped to ask — is
// no delivery: the run applies a proposal only behind a DONE, so the
// container's account stays the no-work exit.
func TestWorkerThatAskedWithATrackerEditBlockIsStillNoWork(t *testing.T) {
	f := newWorkerFixture(t)
	delete(f.env, "TICKS_TEST_WORKER_COMMIT")
	f.env["TICKS_TEST_WORKER_RESULT"] = trackerEditsResult + "STATUS: BLOCKED — should the run apply this"

	out, code := f.run()
	if code != ExitWorkerNoWork {
		t.Fatalf("a BLOCKED answer with a tracker-edits block gave exit %d, want %d:\n%s", code, ExitWorkerNoWork, out)
	}
}
