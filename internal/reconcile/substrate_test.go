package reconcile

import (
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The executor a run dispatches through is the one its RESOLVED PROFILE names
// (tick to1, epic av8) — not a constant the reconciler assumes. A run whose
// profiles name the second executor dispatches every attempt through THAT
// one, and every record the dispatch produces says so: the marker's executor
// and substrate, the attempt record's provenance, the gate's evidence — and
// the run-level checkpoint, produced by no executor, states null.
//
// The second executor here is the FIXTURE's stand-in: the reconciler only
// asks the factory, so a factory that reports a substrate and routes on the
// profile is all the run needs to be driven through it. The real herdr
// factory lives in internal/cli (where herdr may be named); this test pins
// the reconciler's half of the contract from below.
func TestADispatchGoesThroughTheExecutorItsProfileNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, role := range profile.Roles {
		writeProfile(t, dir, role, `"executor": "herdr", "runner": "claude", "model": "sonnet"`)
	}
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.ProfileDir = dir
	opts.Executors = []KnownExecutor{{
		Name:         "herdr",
		Runners:      runconfig.KnownKinds(),
		AcceptsModel: func(string) bool { return true },
	}}

	// The factory routes on the profile, exactly as internal/cli does: a
	// dispatch whose profile names the second executor goes through the
	// SECOND executor and reports the substrate its build observed.
	var mu sync.Mutex
	throughSecond, throughLocal := 0, 0
	opts.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		if d.Profile == nil || d.Profile.Executor != "herdr" {
			mu.Lock()
			throughLocal++
			mu.Unlock()
			return f.newExecutor(d)
		}
		executor, _, err := f.newExecutor(d)
		if err != nil {
			return nil, Substrate{}, err
		}
		mu.Lock()
		throughSecond++
		mu.Unlock()
		return executor, Substrate{Protocol: 22, ServerVersion: "0.9.0-test"}, nil
	}

	// Built from the MUTATED options, not f.run (which rebuilds the fixture's
	// own): the routing factory and the honoured set are the thing under test.
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	// Every dispatch — three implementer attempts, the review and the
	// closeout — went through the executor its profile named, none through
	// the local one.
	mu.Lock()
	second, local := throughSecond, throughLocal
	mu.Unlock()
	if second != 5 {
		t.Errorf("%d dispatches went through the profile's executor, want all 5", second)
	}
	if local != 0 {
		t.Errorf("%d dispatches went through the local executor under profiles naming another one", local)
	}

	// The marker, the closed record and the gate's evidence all name the
	// executor the run actually used, and the substrate its build observed.
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 5 {
		t.Fatalf("%d attempt records for five dispatches", len(attempts))
	}
	for _, attempt := range attempts {
		marker := handleFromMap(attempt.JobHandle)
		if marker.Executor != "herdr" {
			t.Errorf("%s's marker names executor %q, want herdr: the marker is the durable fact a restart adopts by", attempt.TickID, marker.Executor)
		}
		if marker.SubstrateProtocol != 22 || marker.SubstrateServerVersion != "0.9.0-test" {
			t.Errorf("%s's marker states substrate %d/%q, want 22/0.9.0-test: a later leg must record what the dispatch ran on, not what it would observe today",
				attempt.TickID, marker.SubstrateProtocol, marker.SubstrateServerVersion)
		}
		p := attempt.Provenance
		if p.Executor == nil || *p.Executor != "herdr" {
			t.Errorf("%s's provenance names executor %v, want herdr: a record naming an executor the run did not use is provenance that lies", attempt.TickID, p.Executor)
		}
		if p.SubstrateProtocol == nil || *p.SubstrateProtocol != 22 || p.SubstrateServerVersion == nil || *p.SubstrateServerVersion != "0.9.0-test" {
			t.Errorf("%s's provenance states substrate %v/%v, want 22/0.9.0-test — the closed object is where a run spanning a substrate upgrade is diagnosed from",
				attempt.TickID, p.SubstrateProtocol, p.SubstrateServerVersion)
		}
	}

	// The gate's evidence over the implementer's work names the same executor
	// and the same substrate: the gate evaluated the work of a dispatch
	// through that executor, and its record says so.
	for _, key := range r.store.EvidenceKeys() {
		evidence, ok, err := r.store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("evidence %s: ok=%v err=%v", key, ok, err)
		}
		if evidence.Provenance.Role == nil || *evidence.Provenance.Role != "implement-tick" {
			continue
		}
		if evidence.Provenance.Executor == nil || *evidence.Provenance.Executor != "herdr" {
			t.Errorf("the gate's evidence names executor %v, want herdr — the gate evaluated a herdr attempt", evidence.Provenance.Executor)
		}
		if evidence.Provenance.SubstrateProtocol == nil || *evidence.Provenance.SubstrateProtocol != 22 {
			t.Errorf("the gate's evidence states no substrate protocol, want 22")
		}
		if evidence.Provenance.SubstrateServerVersion == nil || *evidence.Provenance.SubstrateServerVersion != "0.9.0-test" {
			t.Errorf("the gate's evidence states no substrate server version, want 0.9.0-test")
		}
	}

	// The run-level checkpoint is a reconciler-side record: no one executor
	// produced it, and it says so rather than naming one.
	checkpoint, ok, err := r.store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("the run wrote no checkpoint: ok=%v err=%v", ok, err)
	}
	if checkpoint.Provenance.Executor != nil {
		t.Errorf("the checkpoint names executor %v, want null: a run may dispatch different roles through different executors, and a checkpoint is not one executor's record", checkpoint.Provenance.Executor)
	}
	if checkpoint.Provenance.SubstrateProtocol != nil || checkpoint.Provenance.SubstrateServerVersion != nil {
		t.Errorf("the checkpoint states a substrate (%v/%v), want null: no dispatch produced it", checkpoint.Provenance.SubstrateProtocol, checkpoint.Provenance.SubstrateServerVersion)
	}
}

// The honoured set decides which executor a profile may name (tick to1): a
// build that can build the second executor admits profiles naming it, and
// still refuses a profile naming one it cannot build — the refusal's purpose
// intact, its question changed from "is it the only one" to "can this build
// honour it".
func TestTheHonouredSetDecidesWhichExecutorAProfileMayName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, role := range profile.Roles {
		writeProfile(t, dir, role, `"executor": "herdr", "runner": "claude", "model": "sonnet"`)
	}
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.ProfileDir = dir
	opts.Executors = []KnownExecutor{{
		Name:         "herdr",
		Runners:      runconfig.KnownKinds(),
		AcceptsModel: func(string) bool { return true },
	}}

	// Admitted: the build honours what the profile names.
	if _, err := New(opts); err != nil {
		t.Fatalf("a profile naming an executor this build honours was refused: %v", err)
	}

	// An executor in the contract's enum that this build cannot build is
	// refused by name, at construction, before anything is claimed.
	//
	// The example is "cloudflare-computer", deliberately NOT the
	// "cloudflare-sandbox" it used to be: that name became an executor
	// ticfac actually ships with tick k4s (the sandbox compatibility executor
	// the Workflow-hosted reconciler dispatches through, cloudflare/src/
	// sandbox-executor.ts), so it can no longer serve as the example of a
	// name this repository refuses — the second executor in the enum is the
	// one name left that nothing builds.
	writeProfile(t, dir, "implement-tick", `"executor": "cloudflare-computer", "runner": "claude", "model": "sonnet"`)
	_, err := New(opts)
	if err == nil {
		t.Fatal("a profile naming an executor this build cannot build was accepted")
	}
	if !strings.Contains(err.Error(), "cloudflare-computer") || !strings.Contains(err.Error(), "provenance that lies") {
		t.Errorf("the refusal does not name what it refused and why: %v", err)
	}

	// A runner the named executor cannot launch — a kind outside the config's
	// matrix — is refused the same way, naming the runner. The runner a job is
	// dispatched with is what the ROUTING says, not only what the profile
	// ships (tick wgi), so the unlaunchable one has to be routed to be seen.
	const geminiRoutedGate = `version = 2

[roles.implement]
kind = "gemini"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`
	gemini := newFixture(t, fixtureOptions{gate: geminiRoutedGate})
	gopts := gemini.options(gemini.Repo, fixtureOptions{})
	gopts.ProfileDir = dir
	gopts.Executors = opts.Executors
	writeProfile(t, dir, "implement-tick", `"executor": "herdr", "runner": "claude", "model": "sonnet"`)
	_, err = New(gopts)
	if err == nil {
		t.Fatal("a profile naming a runner the honoured executor cannot launch was accepted")
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("the refusal does not name the runner: %v", err)
	}
}
