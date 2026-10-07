package cloudflaresandbox

import (
	"strings"
	"testing"
	"time"
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

// newExecutorWithModel points a FRESH executor at the door on a model the
// caller names — the restarted incarnation whose profile now resolves a
// DIFFERENT model, which is the adoption case the door's truthfulness is
// about (tick dyo).
func (h *harness) newExecutorWithModel(state, model string) *Executor {
	h.Helper()
	ex, err := New(Options{
		FactoryURL: h.door.URL(),
		Token:      "run-r1-token",
		EpicID:     "xte",
		BaseRef:    "epic/xte",
		Title:      "A cloudflare-sandbox executor that returns a handle, not a result",
		Model:      model,
		Harness:    testHarness,
		Prompt:     testPrompt,
		Attempt:    1,
		StateDir:   state,
		Now:        func() time.Time { return time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		h.Fatalf("New: %v", err)
	}
	h.ex = ex
	return ex
}

// An adoption tells the truth about the running container's model (tick dyo):
// the door reads the recorded boot and names THAT, so the Go checks — the
// model the dispatch resolved (a08) and nwn's Workers-AI-only rule — fire for
// an adopted attempt exactly as they do for a fresh boot, where before the
// fix the door echoed the request back and neither could ever fire here.
//
// short: an httptest door and two state directories.
func TestAnAdoptionOnTheDispatchedModelIsRecordedAsAdopted(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	if _, err := h.start("keh"); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// The fresh-clone case: the previous incarnation's state is gone, and the
	// door is the only thing that still knows the attempt — still running on
	// the model ITS dispatch booted it on.
	fresh := newHarnessAt(t, h.door)
	handle, err := fresh.start("keh")
	if err != nil {
		t.Fatalf("fresh-host Start: %v", err)
	}
	if handle.JobID != h.spec.JobID {
		t.Errorf("the adopted handle's job id is %q, want %q", handle.JobID, h.spec.JobID)
	}
	record, err := newStore(fresh.ex.stateDirFor(fresh.spec.JobID, 1)).readAttempt()
	if err != nil {
		t.Fatalf("read the adopted record: %v", err)
	}
	if !record.Adopted {
		t.Error("the record does not say the dispatch adopted the running container")
	}
	if record.Model != testModel {
		t.Errorf("the adopted record names model %q, want the running container's %q: the record is what "+
			"every trace reads the provider from", record.Model, testModel)
	}
}

// The acceptance criterion's refusal: a restarted incarnation whose profile
// now resolves a DIFFERENT model must not record the adoption — the container
// still running the tick is on the model its own dispatch booted it on, and a
// record naming the new dispatch's model over that container is a provenance
// that lies. The check that fires is a08's, on the door's truthful answer.
//
// short: an httptest door and two state directories.
func TestAnAdoptionOnAnotherModelIsRefused(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	if _, err := h.start("keh"); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// A config edited between incarnations: the same attempt, resolved today
	// to a different model. Still a Workers AI model — the refusal below is
	// about the LIE the record would carry, not the rule.
	const otherModel = "workers-ai/@cf/example/another-model"
	fresh := newHarnessAt(t, h.door)
	fresh.newExecutorWithModel(t.TempDir(), otherModel)
	_, err := fresh.start("keh")
	if err == nil {
		t.Fatal("an adoption of a container running another model was accepted")
	}
	if !strings.Contains(err.Error(), otherModel) || !strings.Contains(err.Error(), testModel) {
		t.Errorf("the refusal does not name both models: %v", err)
	}
	if st := newStore(fresh.ex.stateDirFor(fresh.spec.JobID, 1)); st.exists(fileAttempt) {
		t.Error("a record was written for a worker running a model the dispatch did not resolve")
	}
}

// nwn's rule applies to an adopted attempt: a container an older deployment
// booted on a model outside the Workers AI namespace — claude, the one model
// the operator rule exists to keep out of the cloud — is refused by its OWN
// sentence, naming the namespace, and nothing is recorded.
//
// short: an httptest door and one state directory.
func TestAnAdoptionOnAModelOutsideWorkersAIIsRefused(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	// A live work process booted before the rule, on the one model the rule
	// exists for. The door reports it truthfully — and the Go check fires.
	h.door.adoptRunning("keh", 1, "claude/sonnet")

	_, err := h.start("keh")
	if err == nil {
		t.Fatal("an adoption of a container running a model outside the Workers AI namespace was accepted")
	}
	if !strings.Contains(err.Error(), "claude/sonnet") {
		t.Errorf("the refusal does not name the running container's model: %v", err)
	}
	if !strings.Contains(err.Error(), "Workers AI") {
		t.Errorf("the refusal does not name the rule it fired: %v", err)
	}
	if st := newStore(h.ex.stateDirFor(h.spec.JobID, 1)); st.exists(fileAttempt) {
		t.Error("a record was written for a worker outside the Workers AI namespace")
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

// The subscription rung crosses the door (tick 6fv): a dispatch whose profile
// resolved claude on a versionless alias — the claude-sub rung the cloud
// billing rule admits — starts, and the record names the pairing that ran.
// The door check is the rule's own predicate (profile.CloudBillingAllows), so
// the rung that [Resolve] admits is the rung the executor accepts; anything
// else would be a resolution the cloud dispatches and its own executor
// refuses.
//
// short: an httptest door and one state directory.
func TestAStartOnTheSubscriptionRungIsAccepted(t *testing.T) {
	h := newHarness(t)
	h.newExecutorOnRung(t.TempDir(), "claude", "opus")
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("a start on the claude/opus subscription rung was refused: %v", err)
	}
	if got := h.door.lastStartBody()["model"]; got != "opus" {
		t.Errorf("the start request carried model %v, want the alias %q", got, "opus")
	}
	if got := h.door.lastStartBody()["harness"]; got != "claude" {
		t.Errorf("the start request carried harness %v, want %q", got, "claude")
	}
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	if payload.Harness != "claude" || payload.Model != "opus" {
		t.Errorf("the handle names %s/%s, want the claude/opus rung it booted on", payload.Harness, payload.Model)
	}
	record, err := newStore(payload.State).readAttempt()
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if record.Harness != "claude" || record.Model != "opus" {
		t.Errorf("the record names %s/%s, want the claude/opus rung it ran on", record.Harness, record.Model)
	}
}

// The rung's own harness on a PINNED id is refused by the same predicate: a
// pinned claude model bills per token, and the door check is what keeps a
// start that recorded it from ever doing so.
//
// short: an httptest door and one state directory.
func TestAStartOnAPinnedClaudeModelIsRefused(t *testing.T) {
	h := newHarness(t)
	h.newExecutorOnRung(t.TempDir(), "claude", "claude-opus-5-5")
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a start on a pinned claude model id was accepted")
	}
	if !strings.Contains(err.Error(), "claude-opus-5-5") {
		t.Errorf("the refusal does not name the model it refused: %v", err)
	}
	if !strings.Contains(err.Error(), "billing rule") {
		t.Errorf("the refusal does not name the rule that fired: %v", err)
	}
}

// The hosted door's defect (tick yhe), pinned as the client's half of it: a
// door that answers a claude/sonnet rung start with the WORKER AGENT's
// harness — pi-durable, the name a hosted attempt's handle carries whatever
// the dispatch resolved — names a pairing the cloud billing rule refuses,
// and the client refuses it BEFORE any record is written. A claude-sub job
// is the container's own claude worker under the interception; the handle
// that proves it ran there names the rung's own pair, which
// [TestAStartOnTheSubscriptionRungIsAccepted] shows the client accepts —
// and the real-door end-to-end test in claude_sub_e2e_test.go shows the bound
// WORKER_AGENTS door now produces.
//
// short: an httptest door and one state directory.
func TestAHostedHandleOnTheSubscriptionRungIsRefused(t *testing.T) {
	h := newHarness(t)
	h.newExecutorOnRung(t.TempDir(), "claude", "sonnet")
	// The door a hosted deployment answered with before the routing: the
	// agent's harness over the rung's alias, on a start that leased.
	h.door.bootedHarness = WorkerAgentHarness
	_, err := h.start("keh")
	if err == nil {
		t.Fatal("a rung start answered with the agent's harness was accepted: pi-durable/sonnet is a pairing the cloud billing rule refuses")
	}
	if !strings.Contains(err.Error(), "pi-durable/sonnet") {
		t.Errorf("the refusal does not name the pairing it refused: %v", err)
	}
	if !strings.Contains(err.Error(), "billing rule") {
		t.Errorf("the refusal does not name the rule that fired: %v", err)
	}
	if st := newStore(h.ex.stateDirFor(h.spec.JobID, 1)); st.exists(fileAttempt) {
		t.Error("an attempt record was written for a pairing the cloud billing rule refuses")
	}
}

// newExecutorOnRung points a FRESH executor at the door on a subscription
// rung's harness/alias pair — the dispatch whose profile a claude-sub config
// resolved.
func (h *harness) newExecutorOnRung(state, harness, model string) {
	h.Helper()
	ex, err := New(Options{
		FactoryURL: h.door.URL(),
		Token:      "run-r1-token",
		EpicID:     "xte",
		BaseRef:    "epic/xte",
		Title:      "A cloudflare-sandbox executor that returns a handle, not a result",
		Model:      model,
		Harness:    harness,
		Prompt:     testPrompt,
		Attempt:    1,
		StateDir:   state,
		StuckAfter: 15 * time.Minute,
		Now:        func() time.Time { return time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		h.Fatalf("New: %v", err)
	}
	h.ex = ex
}
