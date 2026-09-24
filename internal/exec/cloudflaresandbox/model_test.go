package cloudflaresandbox

import (
	"strings"
	"testing"
)

// The model a dispatch's profile resolved crosses the door (tick a08): the
// start request carries it, the door boots the worker on it and names it back
// in the handle, and the attempt record states it — so the model a run
// records is the model that ran, not the factory's own default agreeing with
// it by luck.
//
// short: an httptest door and one state directory.
func TestTheResolvedModelCrossesTheDoorAndTheRecordNamesIt(t *testing.T) {
	h := newHarness(t)
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.door.lastStartBody()["model"]; got != testModel {
		t.Errorf("the start request carried model %v, want %q: a door not told the model boots the factory's own",
			got, testModel)
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	if payload.Model != testModel {
		t.Errorf("the handle names model %q, want %q", payload.Model, testModel)
	}
	record, err := newStore(payload.State).readAttempt()
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if record.Model != testModel {
		t.Errorf("the attempt record names model %q, want %q: the record is what a trace reads the provider from",
			record.Model, testModel)
	}
}

// A door that booted the worker on a model the dispatch did not name is
// refused, and nothing is recorded: a record naming the requested model over a
// container running another is exactly the lie this tick closes.
//
// short: an httptest door and one state directory.
func TestAHandleBootedOnAnotherModelIsRefused(t *testing.T) {
	h := newHarness(t)
	h.door.bootedModel = "workers-ai/@cf/example/other-model"
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("Start accepted a handle for a worker booted on a model the dispatch did not name")
	}
	if !strings.Contains(err.Error(), "other-model") || !strings.Contains(err.Error(), testModel) {
		t.Errorf("the refusal does not name both models: %v", err)
	}
	if st := newStore(h.ex.stateDirFor(h.spec.JobID, 1)); st.exists(fileAttempt) {
		t.Error("an attempt record was written for a worker running a model nobody asked for")
	}
}

// A dispatch with no model is refused before the door is dialled: the door
// requires one, and a start without it would have booted whatever the factory
// defaults to under a record that names nothing.
//
// short: spec validation only; the door is never dialled.
func TestAStartWithNoModelIsRefusedBeforeTheDoor(t *testing.T) {
	h := newHarness(t)
	h.ex.opts.Model = ""
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start with no model was accepted")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Errorf("the refusal does not name the missing model: %v", err)
	}
	if h.door.startCount() != 0 || h.door.statusCount() != 0 {
		t.Errorf("the door was dialled (%d starts, %d reads) for a start the client could refuse itself",
			h.door.startCount(), h.door.statusCount())
	}
}
