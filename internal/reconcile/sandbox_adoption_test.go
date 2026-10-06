package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// Tick avx: the orchestrator's half of adoption across the sandbox dispatch
// door.
//
// The executor's half is already proven beside the executor itself — Start
// asks the door BY IDENTITY before anything boots, a live container under
// that identity comes back adopted rather than rivalled, and a door the
// client cannot REACH is a transport error, never a `lost` — and this
// package must not import that one (the seam test there forbids the
// dependency from this side), so the same load-bearing semantics are
// restated here as a fixture and driven through the Executor interface the
// reconciler actually sees:
//
//   - a container is addressed by IDENTITY — the run the credential names,
//     the tick, the attempt number — never by anything a dead orchestrator
//     persisted, which is what makes a container adoptable by an incarnation
//     that never dispatched it;
//   - a start under an identity whose container is LIVE adopts it and boots
//     nothing, so "the boot count" is the number a test reads for "a rival
//     was dispatched";
//   - an unreachable factory is an error, never a status: "the route could
//     not be reached" and "no container under this identity" are different
//     answers, and reading the first as the second is how a rival gets
//     dispatched.
//
// The two tests are the acceptance criteria, one each: a restarted
// orchestrator re-derives cold from git — a fresh clone AND a fresh executor
// state root, holding nothing but what is on origin — and must reach the
// same decision a warm process would (Axiom 1), adopting the sandbox that is
// still running its tick; and the same cold re-derivation against a factory
// that cannot be reached must stop as an operational error and dispatch
// nothing, because an outage is not a verdict on the work and never "no
// sandbox running".

// doorExecutorName is the executor name the honoured set and the profiles
// below state: the name the closed contract's executor enum already carries,
// and the one a cloud dispatch's marker records.
const doorExecutorName = "cloudflare-sandbox"

// cloudGate is the fixture's runners.toml for a run whose profiles name the
// sandbox executor. It declares the implement role's routing the way the
// cloud routing this repository's own runners configuration declares it —
// pi on the gateway-backed model — and names the same tree check every
// other fixture gate names.
const cloudGate = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[testing.commands]
tree = { command = "test -f README.md", description = "the merge carries the work" }
`

// doorIdentity is one attempt's identity as the factory addresses it: the
// run the credential names, the tick, the attempt number. Nothing a dead
// orchestrator persisted is part of it — that is the whole difference from
// the local substrates, and the property adoption across this boundary
// rides on.
type doorIdentity struct {
	run     string
	tick    string
	attempt int
}

// fakeSandboxDoor is the factory's sandbox dispatch door as this package's
// stand-in: the two routes the door carries — start one attempt's container
// by identity, read the named container's state — with the boot count kept
// as the observable "a second sandbox was dispatched" is, rather than an
// impression from a journal.
type fakeSandboxDoor struct {
	runID string

	mu         sync.Mutex
	reachable  bool
	boots      int
	adoptions  int
	starts     int
	statusAsks int
	// darkCalls is a BOUNDED outage: the next darkCalls calls fail as
	// unreachable, and the door answers again after them.
	darkCalls  int
	containers map[doorIdentity]*doorContainer
	// reattaches counts settled attempts re-addressed for their collect,
	// and collects the collects the executor was asked for.
	reattaches int
	collects   int
	// refuseReattach makes the client unable to reattach, which is the
	// client before epic hn6's fix: the start refused as settled stands.
	refuseReattach bool
}

// settle finishes the named container's work: it is terminal from now on,
// whatever any orchestrator does.
func (d *fakeSandboxDoor) settle(id doorIdentity, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.containers[id]
	c.state, c.terminal = state, true
}

func (d *fakeSandboxDoor) reattachAndCollectCounts() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reattaches, d.collects
}

// doorContainer is one sandbox the factory booted and owns, still running
// its attempt's work whatever any orchestrator does.
type doorContainer struct {
	name      string
	processID int
	terminal  bool
	state     string
}

func newFakeSandboxDoor(runID string) *fakeSandboxDoor {
	return &fakeSandboxDoor{runID: runID, reachable: true, containers: map[doorIdentity]*doorContainer{}}
}

// goDark makes the factory unreachable: an outage, not a verdict.
func (d *fakeSandboxDoor) goDark() {
	d.mu.Lock()
	d.reachable = false
	d.mu.Unlock()
}

// goDarkFor makes the factory unreachable for its next n calls, then it
// answers again: the outage a resume is for.
func (d *fakeSandboxDoor) goDarkFor(n int) {
	d.mu.Lock()
	d.darkCalls = n
	d.mu.Unlock()
}

// dark says whether this call meets the outage, spending one call of a
// bounded one. The caller holds d.mu.
func (d *fakeSandboxDoor) dark() bool {
	if !d.reachable {
		return true
	}
	if d.darkCalls > 0 {
		d.darkCalls--
		return true
	}
	return false
}

// doorOutage is the door not answering, restated as the real client types it
// (cloudflaresandbox's doorUnreachable): an error that says of itself it is a
// transient remote failure, which is what the supervisor reads it by.
type doorOutage struct{}

func (*doorOutage) Error() string {
	return "the sandbox dispatch door could not be reached: the factory at the run's credential is " +
		"unreachable, which is a transport failure and not an answer about any sandbox"
}

func (*doorOutage) TransientRemote() bool { return true }

func (d *fakeSandboxDoor) bootCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.boots
}

func (d *fakeSandboxDoor) adoptionCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.adoptions
}

// startCount is how many start-route round trips the door served, and
// statusAsks how many state reads: the pair is what "the run went and asked
// the factory" is, as numbers rather than an impression.
func (d *fakeSandboxDoor) startCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts
}

func (d *fakeSandboxDoor) statusAskCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusAsks
}

// jobID is the job id the door mints for an identity, from the run the
// credential names — the same shape the reconciler owns, so a handle the
// door mints is one the dispatch's own records recognise.
func (d *fakeSandboxDoor) jobID(id doorIdentity) string {
	return fmt.Sprintf("run-%s/tick-%s/attempt-%d", id.run, id.tick, id.attempt)
}

// status is the door's state route: the state of the NAMED container. An
// unreachable door is an error and never a status — reading an outage as
// absence is how an attempt gets written off while its container is still
// running — and a door that holds no container under the identity answers
// `lost`: a statement about the observer, not a verdict on the work.
func (d *fakeSandboxDoor) status(id doorIdentity) (*subprocess.JobStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statusAsks++
	if d.dark() {
		return nil, &doorOutage{}
	}
	status := &subprocess.JobStatus{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         d.jobID(id),
		ObservedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	container := d.containers[id]
	if container == nil {
		status.State, status.Terminal = subprocess.StateLost, false
		return status, nil
	}
	status.State, status.Terminal = container.state, container.terminal
	if container.terminal {
		status.Observations = []subprocess.Observation{{
			At: time.Now().UTC().Format(time.RFC3339), Kind: "exited",
			Detail: "the container's work process exited",
		}}
	}
	return status, nil
}

// start is the door's start route: boot one attempt's container, NAMED by
// identity, or adopt the live one already under that name. A start under an
// identity whose container is live boots NO second container beside it —
// that is the property this whole file is about — and a start the door
// cannot see an identity under (an unreachable factory) fails as a
// transport error, never as "no sandbox running".
func (d *fakeSandboxDoor) start(id doorIdentity) (*subprocess.JobHandle, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts++
	if d.dark() {
		return nil, false, &doorOutage{}
	}
	if container := d.containers[id]; container != nil {
		if container.terminal {
			return nil, false, fmt.Errorf("the door refused: attempt %d of %s already settled under this identity: "+
				"a retry is a new attempt number", id.attempt, id.tick)
		}
		// The live container is adopted, never booted over: two containers
		// under one identity is the failure adoption-by-identity exists to
		// prevent.
		d.adoptions++
		return d.handleFor(id, container), true, nil
	}
	d.boots++
	container := &doorContainer{
		name:      fmt.Sprintf("sb-%s-%s-%d", id.run, id.tick, id.attempt),
		processID: 1000 + d.boots,
		state:     subprocess.StateRunning,
	}
	d.containers[id] = container
	return d.handleFor(id, container), false, nil
}

// handleFor mints the handle a start answers with: the closed fields from
// the identity, the door's own addressing in the one open object.
func (d *fakeSandboxDoor) handleFor(id doorIdentity, c *doorContainer) *subprocess.JobHandle {
	return &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         d.jobID(id),
		Attempt:       id.attempt,
		Executor:      doorExecutorName,
		Handle: map[string]any{
			"sandbox": c.name, "process_id": c.processID, "tick_id": id.tick,
		},
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// The client half: the door's executor, built per dispatch by the factory
// below exactly the way internal/cli builds the executors it can honour.
// It restates the real client's contract at the points the reconciler's
// adoption decision depends on, and every deviation from it here would be a
// fixture lying about the substrate the decision is made about.

// doorExecutorName's file names. attempt.json is the name the reconciler's
// own state walk looks for, so a warm restart finds the record the same way
// it finds the local executors'.
const doorAttemptFile = "attempt.json"

// errUnreadableRecord is the record that exists and cannot be read: a handle
// a later leg cannot address, held for a person rather than redispatched
// behind.
var errUnreadableRecord = errors.New("the attempt record cannot be read")

// doorExecutor is one dispatch's client of the door.
type doorExecutor struct {
	door     *fakeSandboxDoor
	runID    string
	attempt  int
	stateDir string
}

func newDoorExecutor(door *fakeSandboxDoor, d Dispatch) *doorExecutor {
	return &doorExecutor{door: door, runID: d.RunID, attempt: d.Attempt, stateDir: d.StateDir}
}

// doorRecord is the durable description of one attempt: the identity a
// restarted leg needs to re-address it, and the handle the door minted,
// frozen. The door needs none of this — it re-addresses by name — but the
// client's own Start decisions do: an attempt whose record exists is one
// whose dispatch once landed, and "the record exists and the door cannot
// address its container" is a hold, never a redispatch.
type doorRecord struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	Attempt       int    `json:"attempt"`
	TickID        string `json:"tick_id"`
	State         string `json:"state"`
	Sandbox       string `json:"sandbox"`
	ProcessID     int    `json:"process_id"`
	IssuedAt      string `json:"issued_at"`

	// Adopted says the door's start route found a live container under
	// this identity and adopted it rather than booting a rival.
	Adopted bool `json:"adopted"`
}

// readRecordAt reads the attempt record in one state directory. Found is
// false and the error nil when no record exists; errUnreadableRecord is the
// record that exists and cannot be read.
func readRecordAt(dir string) (record *doorRecord, found bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, doorAttemptFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	var decoded doorRecord
	if json.Unmarshal(raw, &decoded) != nil || decoded.JobID == "" || decoded.Sandbox == "" ||
		decoded.TickID == "" || decoded.Attempt < 1 {
		return nil, true, errUnreadableRecord
	}
	return &decoded, true, nil
}

// writeRecord lands the record and READS IT BACK: a handle for a record that
// did not land is a job nobody can find.
func (e *doorExecutor) writeRecord(record *doorRecord) error {
	record.SchemaVersion = 1
	record.State = e.stateDir
	if err := os.MkdirAll(e.stateDir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(e.stateDir, doorAttemptFile), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	confirmed, found, err := readRecordAt(e.stateDir)
	if err != nil || !found {
		return fmt.Errorf("the attempt record did not land: %v", err)
	}
	if confirmed.JobID != record.JobID || confirmed.Attempt != record.Attempt || confirmed.Sandbox != record.Sandbox {
		return fmt.Errorf("the attempt record read back as %s attempt %d in %s, not %s attempt %d in %s",
			confirmed.JobID, confirmed.Attempt, confirmed.Sandbox, record.JobID, record.Attempt, record.Sandbox)
	}
	return nil
}

// handle is the protocol handle for the frozen record.
func (r *doorRecord) handle() *subprocess.JobHandle {
	return &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         r.JobID,
		Attempt:       r.Attempt,
		Executor:      doorExecutorName,
		Handle: map[string]any{
			"state": r.State, "sandbox": r.Sandbox, "process_id": r.ProcessID, "tick_id": r.TickID,
		},
		IssuedAt: r.IssuedAt,
	}
}

// tickOfSpec is the tick this job is about, from the spec's own inputs: the
// protocol's way of saying it, and the only one a client may read.
func tickOfSpec(spec *subprocess.JobSpec) string {
	for _, in := range spec.Inputs {
		if in.Kind == "tick" {
			return in.ID
		}
	}
	for _, in := range spec.Inputs {
		return in.ID
	}
	return "job"
}

// Start asks the door to boot one attempt's container and comes back with
// the handle — without waiting for the attempt. The order is the
// load-bearing part:
//
//   - the door is asked BY IDENTITY first, before anything boots: an
//     unreachable door fails the dispatch here, as an operational error,
//     and an identity that already settled is refused — a retry is a new
//     attempt number, and a fresh container over a settled one would be a
//     rival that inherits the name while the work it replaced is the
//     completion contract;
//   - an attempt whose record exists and whose container is live is ADOPTED
//     from the record, with no boot-shaped round trip spent to learn what
//     the record and the status already agree on; a record whose container
//     nobody can address is HELD, never redispatched — the record is durable
//     evidence the dispatch once landed;
//   - an attempt with no record goes through the door's own start route,
//     which adopts a live container under the identity instead of booting a
//     rival beside it — the fresh-host case, where the door is the only
//     thing that still knows the attempt.
//
// A failed start records NOTHING: the door is identity-addressed and
// adoption-safe, so a start that failed before the door answered can be
// retried with no risk of a rival, while a pre-written record would hold the
// attempt for a person forever on a transport blip at dispatch time.
func (e *doorExecutor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	id := doorIdentity{run: e.runID, tick: tickOfSpec(spec), attempt: e.attempt}

	status, err := e.door.status(id)
	if err != nil {
		return nil, err
	}
	if status.Terminal {
		return nil, &subprocess.Refusal{Reason: subprocess.RefusedSettled,
			Message: fmt.Sprintf("attempt %d of %s already settled as %s under this identity: a retry is a new "+
				"attempt number, not this one again", id.attempt, id.tick, status.State)}
	}

	if record, found, rerr := readRecordAt(e.stateDir); rerr != nil {
		// The record EXISTS and this leg cannot read it: held for a person,
		// never redispatched behind an unreadable record.
		return nil, &subprocess.Refusal{Reason: subprocess.RefusedUnknown,
			Message: fmt.Sprintf("the attempt record for %s cannot be read (%v): this attempt is held for a "+
				"person, never redispatched", spec.JobID, rerr)}
	} else if found {
		if status.State == subprocess.StateLost {
			return nil, &subprocess.Refusal{Reason: subprocess.RefusedUnknown,
				Message: fmt.Sprintf("attempt %d of %s is recorded as dispatched but the door cannot address its "+
					"container: it is held, never redispatched", record.Attempt, record.TickID)}
		}
		return record.handle(), nil
	}

	handle, adopted, err := e.door.start(id)
	if err != nil {
		return nil, err
	}
	if handle.JobID != spec.JobID {
		return nil, fmt.Errorf("the door minted handle %s for a start of %s: the credential names a run this job "+
			"id does not, and a container booted under it would not be this attempt's", handle.JobID, spec.JobID)
	}
	payload, _ := handle.Handle["sandbox"].(string)
	processID, _ := handle.Handle["process_id"].(int)
	if err := e.writeRecord(&doorRecord{
		JobID: handle.JobID, Attempt: handle.Attempt, TickID: id.tick,
		Sandbox: payload, ProcessID: processID, IssuedAt: handle.IssuedAt,
		Adopted: adopted,
	}); err != nil {
		return nil, err
	}
	return handle, nil
}

// Inspect re-addresses a handle and reports what can be seen: by identity —
// run from the credential, tick and attempt from the handle or the record a
// minimal handle names — so the answer never depended on the process that
// created the handle. A door this executor cannot REACH is an error, never a
// `lost`.
func (e *doorExecutor) Inspect(h *subprocess.JobHandle, _ string) (*subprocess.JobStatus, error) {
	if h == nil {
		return nil, fmt.Errorf("no handle")
	}
	if h.Executor != "" && h.Executor != doorExecutorName {
		return nil, fmt.Errorf("handle names executor %q; this is %s", h.Executor, doorExecutorName)
	}
	if h.Attempt < 1 {
		return nil, fmt.Errorf("handle carries attempt %d: the door addresses an attempt by a positive integer", h.Attempt)
	}
	var payload struct {
		State   string `json:"state"`
		TickID  string `json:"tick_id"`
		Sandbox string `json:"sandbox"`
	}
	raw, err := json.Marshal(h.Handle)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("handle payload: %w", err)
	}
	tick := payload.TickID
	if tick == "" && payload.State != "" {
		// The minimal {"state": …} handle — the shape the reconciler's
		// adoption path synthesizes from an attempt record — resolves the
		// identity from the record, which is the authority the frozen copy
		// is the copy of.
		record, found, rerr := readRecordAt(payload.State)
		if rerr != nil || !found {
			return nil, fmt.Errorf("no attempt record at %s: %v", payload.State, rerr)
		}
		tick = record.TickID
	}
	if tick == "" {
		return nil, fmt.Errorf("handle payload carries neither a state directory nor a tick id: the door is " +
			"addressed by identity, and this handle states none")
	}
	status, err := e.door.status(doorIdentity{run: e.runID, tick: tick, attempt: h.Attempt})
	if err != nil {
		return nil, err
	}
	if want := h.JobID; want != "" && status.JobID != want {
		return nil, fmt.Errorf("the door answered for %s, not %s: the credential names a run this handle does not",
			status.JobID, want)
	}
	return status, nil
}

// The three operations this fixture's executor does not implement, refused
// typed. Cancel and dispose carry the real executor's DECIDED reasons (tick
// xev) so the tests' teardown paths read what production reads; collect is
// the one place this fake is deliberately narrower than production — the
// real collect reads the durable layer from git (the executor's own
// collect.go, tick xev) and this fixture's tests stop at adoption and never
// collect — so its refusal names the fake, never the seam.
func (e *doorExecutor) Cancel(*subprocess.JobHandle) (*subprocess.CancelAck, error) {
	return nil, &subprocess.Refusal{Reason: "cancel_owned_by_factory", Message: "cancel is the factory's, not this " +
		"executor's (decided, tick xev): the credential a sandbox attempt holds is the run's own gateway token, and " +
		"the container's teardown belongs to the factory that booted it"}
}

// ReattachSettled is the real executor's re-addressing of an attempt that
// settled while no orchestrator watched it: the door says the identity is
// terminal, the record the dead disk held is re-described, and the handle
// comes back for the collect. It never boots anything.
func (e *doorExecutor) ReattachSettled(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	id := doorIdentity{run: e.runID, tick: tickOfSpec(spec), attempt: e.attempt}
	status, err := e.door.status(id)
	if err != nil {
		return nil, err
	}
	if !status.Terminal {
		return nil, fmt.Errorf("attempt %d of %s has not settled", id.attempt, id.tick)
	}
	e.door.mu.Lock()
	if e.door.refuseReattach {
		e.door.mu.Unlock()
		return nil, fmt.Errorf("this client cannot reattach a settled attempt")
	}
	e.door.reattaches++
	container := e.door.containers[id]
	e.door.mu.Unlock()
	record := &doorRecord{JobID: spec.JobID, Attempt: id.attempt, TickID: id.tick,
		Sandbox: container.name, ProcessID: container.processID, IssuedAt: time.Now().UTC().Format(time.RFC3339),
		Adopted: true}
	if err := e.writeRecord(record); err != nil {
		return nil, err
	}
	return record.handle(), nil
}

func (e *doorExecutor) CollectDetail(*subprocess.JobHandle) (*subprocess.Collection, error) {
	e.door.mu.Lock()
	e.door.collects++
	e.door.mu.Unlock()
	return nil, &subprocess.Refusal{Reason: "no_collect_door", Message: "this fixture's executor does not " +
		"collect: the tests it serves stop at adoption, and the real executor reads the durable layer from git " +
		"(tick xev)"}
}

func (e *doorExecutor) Dispose(*subprocess.JobHandle, subprocess.DisposeOptions) error {
	return &subprocess.Refusal{Reason: "nothing_local_to_dispose", Message: "there is nothing on this side of the " +
		"boundary to dispose of (decided, tick xev): the container belongs to the factory that booted it, and the " +
		"branch the work landed on is retired by the close"}
}

// doorOptions builds one reconciler incarnation's options for a run whose
// profiles route every role through the sandbox door: the honoured set the
// production wiring will state, the profile files that name the executor,
// and the executor factory that builds the door's client per dispatch. The
// state root is the caller's, because the tests this serves are about cold
// re-derivation: the second incarnation holds nothing of the first.
func (f *fixture) doorOptions(t *testing.T, repo *testRepo, door *fakeSandboxDoor, stateRoot string, stop func(Event) bool) Options {
	t.Helper()
	profiles := filepath.Join(f.Root, "cloud-profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, role := range profile.EveryRole() {
		writeProfile(t, profiles, role,
			`"executor": "`+doorExecutorName+`", "runner": "pi", "model": "cloudflare-workers-ai/@cf/zai-org/glm-5.3"`)
	}
	opts := f.options(repo, fixtureOptions{stopAfter: stop})
	opts.ProfileDir = profiles
	opts.ExecStateRoot = stateRoot
	opts.Executors = []KnownExecutor{{
		Name: doorExecutorName,
		// The harness names the production wiring states ([CloudRule].Harnesses,
		// the way internal/cli/executor.go does): the profile files above name
		// the runner table's "pi", which resolution binds to the hosted kind
		// "pi-durable" on every dispatch into Cloudflare (tick twa), so a set
		// admitting only "pi" refuses the run at construction.
		Runners:      profile.CloudRule.Harnesses,
		AcceptsModel: func(string) bool { return true },
		// The harness's own poll cadence rather than the cloud's five
		// minutes: a test that waits for anything waits milliseconds.
		PollInterval: 20 * time.Millisecond,
	}}
	opts.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		if d.Profile == nil || d.Profile.Executor != doorExecutorName {
			return nil, Substrate{}, fmt.Errorf(
				"the profile for %s names executor %q, which this test wires no client for: the door is the only "+
					"executor it builds", d.TickID, profileExecutorOf(d))
		}
		return newDoorExecutor(door, d), Substrate{}, nil
	}
	return opts
}

func profileExecutorOf(d Dispatch) string {
	if d.Profile == nil {
		return ""
	}
	return d.Profile.Executor
}

// runDoorIncarnation builds and runs one reconciler incarnation through the
// door, the way f.run runs one through the local executor.
func runDoorIncarnation(t *testing.T, f *fixture, repo *testRepo, door *fakeSandboxDoor, stateRoot string, stop func(Event) bool) (*Reconciler, *Result, error) {
	t.Helper()
	r, err := New(f.doorOptions(t, repo, door, stateRoot, stop))
	if err != nil {
		return nil, nil, err
	}
	result, err := r.RunProtected(context.Background())
	return r, result, err
}

// attemptMarkersOf counts the dispatch markers one tick has on origin, and
// the executor name the newest one records: the rival a test is looking for
// is a second marker, and the identity an adoption went through is the
// marker's own executor field.
func attemptMarkersOf(t *testing.T, store *runstate.Store, tick string) (int, string) {
	t.Helper()
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	count, executor := 0, ""
	for _, attempt := range attempts {
		if attempt.TickID != tick {
			continue
		}
		count++
		if name, ok := attempt.JobHandle["executor"].(string); ok {
			executor = name
		}
	}
	return count, executor
}

// TestAKilledOrchestratorAdoptsTheRunningSandboxByIdentity is the acceptance
// criterion, literally: an orchestrator killed mid-attempt and re-derived
// COLD from git — a fresh clone and a fresh executor state root, holding
// nothing but what is on origin — adopts the sandbox that is still running
// its tick, by identity, rather than dispatching a second one.
func TestAKilledOrchestratorAdoptsTheRunningSandboxByIdentity(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")

	// The first incarnation dispatches a1 into its own sandbox at the
	// factory and is killed the moment the dispatch is recorded —
	// mid-attempt, with the container running at a factory the
	// orchestrator's death does not touch.
	_, _, err := runDoorIncarnation(t, f, f.Repo, door, f.StateRoot, stopAt("a1", StageDispatched))
	killedAfter(t, err, "a1", StageDispatched)
	if boots := door.bootCount(); boots != 1 {
		t.Fatalf("the killed incarnation booted %d containers, want 1", boots)
	}

	// The cold re-derivation. A fresh clone of origin AND a fresh executor
	// state root — a new orchestrator container whose only knowledge of the
	// attempt is the marker on origin — against the SAME factory, whose
	// sandbox is still running it.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold := filepath.Join(f.Root, "cold-state")
	restarted, _, err := runDoorIncarnation(t, f, clone, door, cold, stopAt("a1", StageAdopted))
	killedAfter(t, err, "a1", StageAdopted)

	// ADOPTED, and no rival: the factory booted exactly one container across
	// both incarnations, and the one start the restart made adopted the live
	// one rather than booting beside it.
	if boots := door.bootCount(); boots != 1 {
		t.Errorf("the factory holds %d booted containers, want 1: the restart dispatched a rival sandbox", boots)
	}
	if adopted := door.adoptionCount(); adopted != 1 {
		t.Errorf("the door adopted %d live identities, want 1: the restart reached the factory without adopting "+
			"the container that was running its tick", adopted)
	}

	// The feed says what happened: adopted by identity, never dispatched.
	got := restarted.Stages("a1")
	if contains(got, StageDispatched) {
		t.Errorf("the restart dispatched a1 again: %v", got)
	}
	if !contains(got, StageAdopted) {
		t.Errorf("the restart adopted a1 without saying so: %v", got)
	}

	// And origin says it too: exactly one dispatch marker for the tick,
	// across both incarnations, still naming the executor the dispatch went
	// through.
	store := openRunStore(t, clone.Dir, restarted.IntegrationBranch(), restarted.RunID())
	markers, executor := attemptMarkersOf(t, store, "a1")
	if markers != 1 {
		t.Errorf("%d dispatch markers for a1 on origin, want 1: the restart recorded a dispatch of its own", markers)
	}
	if executor != doorExecutorName {
		t.Errorf("the marker names executor %q, want %q: the identity the adoption went through is the marker's own",
			executor, doorExecutorName)
	}
}

// TestAnAttemptThatSettledWhileNobodyWatchedIsCollectedNotStarted is epic
// hn6's cloud run (run_911b…): ltg's attempt settled as SUCCEEDED while the
// orchestrator that dispatched it was busy stopping for another tick, the
// next orchestrator booted on a fresh disk, and its adoption asked Start —
// which the door refused as "already settled", and the run rejected the
// finished attempt as collect_failed instead of collecting its work. The
// cold re-derivation must reach the collect: no start failure, no new
// container, one reattach, and the executor asked to collect.
func TestAnAttemptThatSettledWhileNobodyWatchedIsCollectedNotStarted(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")

	_, _, err := runDoorIncarnation(t, f, f.Repo, door, f.StateRoot, stopAt("a1", StageDispatched))
	killedAfter(t, err, "a1", StageDispatched)
	// The worker finishes while no orchestrator is watching it.
	var id doorIdentity
	door.mu.Lock()
	for key := range door.containers {
		id = key
	}
	door.mu.Unlock()
	door.settle(id, subprocess.StateSucceeded)

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold := filepath.Join(f.Root, "cold-state")
	restarted, _, _ := runDoorIncarnation(t, f, clone, door, cold, nil)

	got := restarted.Stages("a1")
	if contains(got, StageStartFailed) {
		t.Errorf("the restart tried to START the settled attempt and failed: %v", got)
	}
	if contains(got, StageDispatched) {
		t.Errorf("the restart dispatched a1 again: %v", got)
	}
	if boots := door.bootCount(); boots != 1 {
		t.Errorf("the factory booted %d containers, want 1: a settled attempt is collected, never rebooted", boots)
	}
	reattached, collected := door.reattachAndCollectCounts()
	if reattached != 1 {
		t.Errorf("the settled attempt was reattached %d time(s), want 1", reattached)
	}
	if collected == 0 {
		t.Error("the settled attempt was never collected: its work is the verdict, not a refusal to start")
	}
}

// TestARunThatRejectedASettledAttemptCollectsItOnRestart is the state epic
// hn6's cloud run was left in: a resume STARTED the settled attempt, the
// start was refused as settled, and the refusal was recorded as the
// attempt's rejection — with its work on the container's landing branch and
// nothing on its write ref. A restart must collect it, never redispatch it
// and redo the work.
func TestARunThatRejectedASettledAttemptCollectsItOnRestart(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")

	_, _, err := runDoorIncarnation(t, f, f.Repo, door, f.StateRoot, stopAt("a1", StageDispatched))
	killedAfter(t, err, "a1", StageDispatched)
	var id doorIdentity
	door.mu.Lock()
	for key := range door.containers {
		id = key
	}
	door.refuseReattach = true
	door.mu.Unlock()
	door.settle(id, subprocess.StateSucceeded)

	// The incarnation that rejected it: the settled start refusal stands.
	second := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "second"))
	rejecting, _, _ := runDoorIncarnation(t, f, second, door, filepath.Join(f.Root, "second-state"), nil)
	if got := rejecting.Stages("a1"); !contains(got, StageRejected) {
		t.Fatalf("the fixture did not reach hn6's state (a1 rejected over a settled start): %v", got)
	}

	door.mu.Lock()
	door.refuseReattach = false
	door.mu.Unlock()
	third := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "third"))
	restarted, _, _ := runDoorIncarnation(t, f, third, door, filepath.Join(f.Root, "third-state"), nil)

	got := restarted.Stages("a1")
	if contains(got, StageRedispatched) || contains(got, StageDispatched) {
		t.Errorf("the restart redispatched the settled attempt rather than collecting it: %v", got)
	}
	if boots := door.bootCount(); boots != 1 {
		t.Errorf("the factory booted %d containers, want 1", boots)
	}
	if _, collected := door.reattachAndCollectCounts(); collected == 0 {
		t.Errorf("the restart never collected the settled attempt: %v", got)
	}
}

// TestAnUnreachableFactoryIsNotNoSandboxRunning is the second acceptance
// criterion: the same cold re-derivation against a factory that cannot be
// reached does not read the outage as "no sandbox running" — it stops as an
// operational error and dispatches nothing, because an outage is not a
// verdict on the work and the attempt's container is still running behind it.
func TestAnUnreachableFactoryIsNotNoSandboxRunning(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")

	// The killed incarnation, exactly as the adoption test cuts it: a1's
	// container is live at the factory when the orchestrator dies.
	_, _, err := runDoorIncarnation(t, f, f.Repo, door, f.StateRoot, stopAt("a1", StageDispatched))
	killedAfter(t, err, "a1", StageDispatched)
	if boots := door.bootCount(); boots != 1 {
		t.Fatalf("the killed incarnation booted %d containers, want 1", boots)
	}

	// The factory goes dark while the sandbox runs on.
	door.goDark()

	// The cold re-derivation: fresh clone, fresh state root, and a factory
	// that cannot answer.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold := filepath.Join(f.Root, "cold-state")
	restarted, _, err := runDoorIncarnation(t, f, clone, door, cold, nil)
	if err == nil {
		t.Fatal("a run whose factory it cannot reach ended as though nothing was wrong")
	}
	// An outage is an operational error, never a verdict on the work: the
	// run stops and the next resume asks again, rather than recording the
	// attempt as settled, rejected, or — the failure this test exists for —
	// "nothing running".
	var refusal *Refusal
	if asRefusal(err, &refusal) {
		t.Fatalf("the unreachable factory became a verdict on the work (%s): %v", refusal.Reason, err)
	}
	if !strings.Contains(err.Error(), "could not be reached") {
		t.Errorf("the failure does not say the factory was unreachable: %v", err)
	}

	// The failure was RECORDED as a start failure about the transport, not
	// as silence and not as a settle.
	got := restarted.Stages("a1")
	if !contains(got, StageStartFailed) {
		t.Errorf("the restart left no account of the failed start: %v", got)
	}
	for _, stage := range []string{StageDispatched, StageAdopted, StageRedispatched, StageCollected, StageRejected} {
		if contains(got, stage) {
			t.Errorf("the restart recorded %s for a1 behind an unreachable factory: %v", stage, got)
		}
	}

	// It went and ASKED: the factory was read through its status route
	// before anything was decided, which is the only way "absent" and
	// "unreachable" can be told apart at all — and the one thing an
	// orchestrator that assumed "no sandbox running" would never do.
	if asks := door.statusAskCount(); asks < 1 {
		t.Errorf("the restart never asked the factory about the attempt before failing: %d status reads, and "+
			"without one, 'no sandbox running' would be a guess rather than an outage", asks)
	}
	// And no start ever crossed the outage: nothing was dispatched behind
	// a door that was not there to answer.
	if starts := door.startCount(); starts != 1 {
		t.Errorf("the door served %d start round trips, want the 1 the killed incarnation made: the restart "+
			"dispatched through an outage", starts)
	}

	// And it DISPATCHED NOTHING: no second marker for the tick, no new
	// attempt number for any tick, no boot at a factory that was not there
	// to make one.
	store := openRunStore(t, clone.Dir, restarted.IntegrationBranch(), restarted.RunID())
	markers, _ := attemptMarkersOf(t, store, "a1")
	if markers != 1 {
		t.Errorf("%d dispatch markers for a1 on origin, want 1: the run dispatched over the running sandbox on the "+
			"strength of an outage it read as absence", markers)
	}
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Errorf("origin carries %d dispatch markers, want the 1 the killed incarnation left: the run minted a "+
			"rival attempt while its factory was unreachable", len(attempts))
	}
	if boots := door.bootCount(); boots != 1 {
		t.Errorf("the factory holds %d booted containers, want 1", boots)
	}
	// The tick row is what the killed incarnation left it: an outage
	// recorded no verdict about the work.
	if row := tickRowOf(t, store, "a1"); row == "rejected" {
		t.Errorf("the checkpoint row for a1 reads %q behind an unreachable factory: an outage is not a verdict on the work", row)
	}
}

// TestAFactoryOutageAtStartIsResumedNotHandedToAPerson: epic hn6's second
// cloud run halted over "a stop this run has no classification for" when
// attempt 3's start timed out waiting on the door. An outage is not a verdict
// (the test above) and it is not a decision either: the stop is a transient
// remote failure, the supervisor continues across it, and once the factory
// answers the next incarnation dispatches — one container, no rival.
func TestAFactoryOutageAtStartIsResumedNotHandedToAPerson(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")
	// The first incarnation's start meets the outage; it is over by the time
	// the supervisor continues.
	door.goDarkFor(1)

	r, err := New(f.doorOptions(t, f.Repo, door, f.StateRoot, stopAt("a1", StageAdopted)))
	if err != nil {
		t.Fatal(err)
	}
	// The next incarnation finds the claim and marker the failed start left and
	// takes the attempt up by identity: the door boots it then, and only then.
	killedAfter(t, superviseProtected(r), "a1", StageAdopted)

	events := feedStages(t, f.Repo.Dir, r.RunID())
	resumed := detailOfStage(events, StageResumedAutomatically)
	if n := countStage(events, StageResumedAutomatically); n != 1 {
		t.Fatalf("the run was continued %d time(s), want once across the outage", n)
	}
	if !strings.Contains(resumed, StoppedRemoteTransient) {
		t.Errorf("the outage stopped the run as %q, want %s: an unreachable door is the remote's pipe, "+
			"not a stop for a person", resumed, StoppedRemoteTransient)
	}
	if n := countStage(events, StageSupervisionHalted); n != 0 {
		t.Errorf("the run halted for a person over a factory outage (%d %s line(s))", n, StageSupervisionHalted)
	}
	if n := countStage(events, StageStartFailed); n != 1 {
		t.Errorf("%d %s line(s), want the one failed start", n, StageStartFailed)
	}
	if boots := door.bootCount(); boots != 1 {
		t.Errorf("the factory booted %d containers, want 1", boots)
	}
}

// superviseProtected is Supervise under the harness's simulated kill, the way
// RunProtected is Run under it.
func superviseProtected(r *Reconciler) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cut, ok := recovered.(stopped)
			if !ok {
				panic(recovered)
			}
			err = &killedAt{Event: cut.At}
		}
	}()
	_, err = r.Supervise(context.Background())
	return err
}

// JobsLiveAtFactory is the real executor's statement, restated: the door's
// jobs live in the factory, which answers for them.
func (e *doorExecutor) JobsLiveAtFactory() bool { return true }

// lose makes every container of the named tick read `lost` from now on —
// the door's answer when it cannot see a work process under the identity —
// while nothing about the work is decided.
func (d *fakeSandboxDoor) lose(tick string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, c := range d.containers {
		if id.tick == tick {
			c.state, c.terminal = subprocess.StateLost, false
		}
	}
}

// TestALostCloudAttemptIsReAskedNotHandedToAPerson: epic hn6, run_6d88e3de.
// 7uv's worker container idled out while the orchestrator sat in another
// tick's resolve-conflict wait, the door's next answer for it was `lost`, and
// the run refused attempt_unaddressed on that ONE answer — "nobody can say
// whether it is running" — and halted for a person while zl1's worker was
// still running and other ticks were ready. For an executor whose jobs live
// at the factory, `lost` is the factory's answer for that moment, and the
// factory is the one party that can settle it: the run keeps asking, holds
// only that tick, and the wall clock stays the backstop.
func TestALostCloudAttemptIsReAskedNotHandedToAPerson(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")

	stop := func(e Event) bool {
		if e.Tick != "a1" {
			return false
		}
		if e.Stage == StageDispatched {
			door.lose("a1")
			return false
		}
		return e.Stage == StageWaiting && strings.Contains(e.Detail, "reads lost at the factory")
	}
	r, result, err := runDoorIncarnation(t, f, f.Repo, door, f.StateRoot, stop)
	var refusal *Refusal
	if asRefusal(err, &refusal) {
		t.Fatalf("one `lost` from the factory became a refusal (%s) — a stop for a person: %v", refusal.Reason, err)
	}
	if result != nil && result.Failure != nil {
		t.Fatalf("one `lost` from the factory stopped the run (%s) — a stop for a person: %s",
			result.Failure.Reason, result.Failure.Message)
	}
	killedAfter(t, err, "a1", StageWaiting)
	for _, stage := range []string{StageRejected, StageRedispatched} {
		if contains(r.Stages("a1"), stage) {
			t.Errorf("a1 was %s on a `lost` answer: %v", stage, r.Stages("a1"))
		}
	}
	if boots := door.bootCount(); boots != 1 {
		t.Errorf("the factory booted %d containers, want 1: a `lost` is never a licence to dispatch a rival", boots)
	}
}
