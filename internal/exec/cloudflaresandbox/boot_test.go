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

// A HOSTED attempt's handle names the harness its WorkerAgent runs it on —
// [WorkerAgentHarness] — not the harness the dispatch's profile resolved for
// the container its tools run in (tick 4uj): the agent drives the boot, the
// conversation and the finish, so the harness that ran is the agent's, and
// the record a caller keeps names it. The request still carries the profile's
// own harness — the container's boot environment is bound to it until the
// pi-CLI path is deleted (tick jhp) — and Start accepts exactly that one
// mismatch, the same rule the refusal above enforces answered on the hosted
// side: the record names what ran.
//
// short: an httptest door and one state directory.
func TestAHostedHandleNamesTheWorkerAgentHarnessItRanOn(t *testing.T) {
	h := newHarness(t)
	// The door's hosted path names the agent's harness in the handle, the
	// one shape no container boot can produce.
	h.door.bootedHarness = WorkerAgentHarness
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start refused a hosted attempt's handle: %v", err)
	}
	// The request still named the profile's own harness for the container.
	if got := h.door.lastStartBody()["harness"]; got != testHarness {
		t.Errorf("the start request carried harness %v, want the profile's own %q: a hosted attempt's "+
			"container is still booted on the dispatch's harness", got, testHarness)
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	if payload.Harness != WorkerAgentHarness {
		t.Errorf("the handle names harness %q, want the agent's %q", payload.Harness, WorkerAgentHarness)
	}
	record, err := newStore(payload.State).readAttempt()
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if record.Harness != WorkerAgentHarness {
		t.Errorf("the attempt record names harness %q, want the agent's %q: the record is what a "+
			"trace reads the runner from, and the agent is what ran", record.Harness, WorkerAgentHarness)
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
