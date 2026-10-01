package reconcile

import (
	"errors"
	"testing"
)

// permanentStart is a substrate's typed "this start can never succeed under
// this identity" — what the cloud executor's door refusal
// invalid_sandbox_name says (cloudflaresandbox's PermanentStart).
type permanentStart struct{}

func (permanentStart) Error() string {
	return "the sandbox dispatch door refused (422 invalid_sandbox_name): Sandbox ID must be 1-63 characters long."
}
func (permanentStart) PermanentStart() bool { return true }

// hn6 run_ee8e: both retries of 378's resolve job were refused at start for a
// container name the SDK would not take, each counted an operational failure
// worth another job, until the tick's resolve allowance was spent in six
// seconds. A start the substrate calls permanent is the stop on its merits,
// once — not a job that "never answered".
//
// short: a pure function over errors
func TestAStartTheSubstrateCallsPermanentIsNotRetried(t *testing.T) {
	t.Parallel()
	if startIsOperational(permanentStart{}) {
		t.Error("a permanent start refusal was read as an operational failure to retry")
	}
	if startIsOperational(fmtWrap(permanentStart{})) {
		t.Error("a wrapped permanent start refusal was read as an operational failure to retry")
	}
	if !startIsOperational(errors.New("the substrate failed to launch it")) {
		t.Error("a plain start failure is no longer retried")
	}
}

func fmtWrap(err error) error { return &wrapped{err} }

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "start: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }
