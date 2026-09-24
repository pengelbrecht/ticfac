package cloudflaresandbox

import (
	"strings"
	"testing"
)

// The harness a dispatch's profile resolved crosses the door (tick 9iz): the
// start request carries it, the door binds the worker to it and names it back
// in the handle, and the attempt record states it — so the harness a run
// records is the harness that ran, not the deployment's RUN_WORKER_HARNESS
// agreeing with it by luck.
//
// short: an httptest door and one state directory.
func TestTheResolvedHarnessCrossesTheDoorAndTheRecordNamesIt(t *testing.T) {
	h := newHarness(t)
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.door.lastStartBody()["harness"]; got != testHarness {
		t.Errorf("the start request carried harness %v, want %q: a door not told the harness binds the "+
			"deployment's own", got, testHarness)
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	if payload.Harness != testHarness {
		t.Errorf("the handle names harness %q, want %q", payload.Harness, testHarness)
	}
	record, err := newStore(payload.State).readAttempt()
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if record.Harness != testHarness {
		t.Errorf("the attempt record names harness %q, want %q: the record is what a trace reads the "+
			"runner from", record.Harness, testHarness)
	}
}

// A door that booted the worker on a harness the dispatch did not name is
// refused, and nothing is recorded: a record naming the requested harness over
// a container bound to another is the same lie the model cross-check exists to
// prevent, one field over.
//
// short: an httptest door and one state directory.
func TestAHandleBootedOnAnotherHarnessIsRefused(t *testing.T) {
	h := newHarness(t)
	h.door.bootedHarness = "omp"
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("Start accepted a handle for a worker booted on a harness the dispatch did not name")
	}
	if !strings.Contains(err.Error(), "omp") || !strings.Contains(err.Error(), testHarness) {
		t.Errorf("the refusal does not name both harnesses: %v", err)
	}
	if st := newStore(h.ex.stateDirFor(h.spec.JobID, 1)); st.exists(fileAttempt) {
		t.Error("an attempt record was written for a worker bound to a harness nobody asked for")
	}
}

// A dispatch with no harness is refused before the door is dialled: the door
// requires one, and a start without it would boot on the factory's own
// standing choice under a record that names nothing.
//
// short: spec validation only; the door is never dialled.
func TestAStartWithNoHarnessIsRefusedBeforeTheDoor(t *testing.T) {
	h := newHarness(t)
	h.ex.opts.Harness = ""
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start with no harness was accepted")
	}
	if !strings.Contains(err.Error(), "harness") {
		t.Errorf("the refusal does not name the missing harness: %v", err)
	}
	if h.door.startCount() != 0 || h.door.statusCount() != 0 {
		t.Errorf("the door was dialled (%d starts, %d reads) for a start the client could refuse itself",
			h.door.startCount(), h.door.statusCount())
	}
}

// The rendered role prompt a dispatch's profile resolved crosses the door too
// (tick 9iz): the start request carries it — the container's own entrypoint
// renders its worker prompt from the checkout's tracker and never sees the
// factory's otherwise — and the attempt record states what was delivered, so
// the `prompt_digest` the reconciler's marker carries names a prompt that
// actually reached the worker.
//
// short: an httptest door and one state directory.
func TestTheRolePromptCrossesTheDoorAndTheRecordNamesIt(t *testing.T) {
	h := newHarness(t)
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, _ := h.door.lastStartBody()["prompt"].(string); got != testPrompt {
		t.Errorf("the start request carried a prompt of %d bytes, want the profile's %d: a door not told "+
			"the prompt boots the checkout's own", len(got), len(testPrompt))
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	record, err := newStore(payload.State).readAttempt()
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if record.Prompt != testPrompt {
		t.Errorf("the attempt record does not state the prompt the dispatch delivered (%d bytes, want %d)",
			len(record.Prompt), len(testPrompt))
	}
}

// A dispatch with no prompt is refused before the door is dialled, for the
// model's and the harness's reason: a start without one would boot a worker on
// a prompt nobody chose, under a record whose prompt_digest names one that never
// ran.
//
// short: spec validation only; the door is never dialled.
func TestAStartWithNoPromptIsRefusedBeforeTheDoor(t *testing.T) {
	h := newHarness(t)
	h.ex.opts.Prompt = ""
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start with no prompt was accepted")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Errorf("the refusal does not name the missing prompt: %v", err)
	}
	if h.door.startCount() != 0 || h.door.statusCount() != 0 {
		t.Errorf("the door was dialled (%d starts, %d reads) for a start the client could refuse itself",
			h.door.startCount(), h.door.statusCount())
	}
}
