package runstate

import (
	"reflect"
	"strings"
	"testing"
)

// The record layer, against a real origin: what a reconciler actually calls.
//
// The CAS sequences prove the guard. These prove the store puts each record
// kind under the right one, writes a checkpoint only when the run's state
// changed, places the run tag at terminal state, and leaves behind something a
// restarted reconciler can read.

const testRun = "r-2f9c"

func testProvenance(phase Phase) Provenance {
	return Provenance{
		RunID:                 testRun,
		TickID:                nil,
		Attempt:               nil,
		SourceRef:             "refs/heads/main",
		SourceSHA:             "acb08b9493dd8647918efbebac27079c64339946",
		IntegrationRef:        Ptr("refs/heads/" + testBranch),
		Phase:                 phase,
		Executor:              nil,
		WorkspaceID:           nil,
		Backend:               nil,
		Role:                  nil,
		ProfileDigest:         nil,
		Model:                 nil,
		ContextManifestDigest: nil,
	}
}

func testCheckpoint(state State, reason string) Checkpoint {
	return Checkpoint{
		EpicID:     "692",
		State:      state,
		Reason:     reason,
		Provenance: testProvenance(PhasePostWave),
	}
}

func testAttempt(n int, tick string) Attempt {
	p := testProvenance(PhaseWorker)
	p.TickID, p.Attempt = Ptr(tick), Ptr(n)
	p.Executor, p.Role = Ptr("local-subprocess"), Ptr("implement-tick")
	p.WorkspaceID, p.Backend = Ptr("wt-"+tick), Ptr("subprocess")
	return Attempt{
		Attempt:      n,
		TickID:       tick,
		DispatchedAt: "2026-09-02T09:41:02Z",
		JobHandle:    map[string]any{"executor": "local-subprocess", "handle": "pid:48211"},
		Provenance:   p,
	}
}

func testEvidence(key string, result string) Evidence {
	p := testProvenance(PhaseIntegrated)
	p.Executor, p.Backend, p.WorkspaceID = Ptr("local-subprocess"), Ptr("subprocess"), Ptr("wt-gate-1")
	return Evidence{
		Key:        key,
		Provenance: p,
		Check:      Check{ID: key, Kind: "command", Command: []string{"go", "test", "./..."}},
		StartedAt:  "2026-09-02T10:16:00Z",
		FinishedAt: "2026-09-02T10:19:12Z",
		ExitCode:   Ptr(0),
		Output: Output{Artifact: &ArtifactOutput{
			Mode: "artifact", URI: "ticfac://runs/" + testRun + "/logs/" + key + ".txt",
			ContentDigest: "sha256:c41e7b", Redacted: true, Bytes: 4096,
		}},
		Result:         result,
		Acceptance:     "required",
		ContentDigest:  "sha256:c41e7b",
		PersistenceURI: "ticfac://runs/" + testRun + "/evidence/" + key + ".json",
	}
}

func testDecision(n int, verdict string) Decision {
	p := testProvenance(PhaseReview)
	p.Role, p.Model, p.ProfileDigest = Ptr("review-epic"), Ptr("claude-opus-5"), Ptr("sha256:3f1c0a")
	return Decision{
		Decision:    n,
		Role:        "review-epic",
		Request:     map[string]any{"question": "is the frontier ready for wave 2?"},
		Response:    map[string]any{"verdict": verdict},
		Validated:   true,
		RequestedAt: "2026-09-02T10:15:01Z",
		AnsweredAt:  "2026-09-02T10:15:44Z",
		Provenance:  p,
	}
}

// The first checkpoint is a create; every later one is guarded on the sha the
// writer fetched. A reconciler holding a stale view is refused, re-fetches, and
// reconciles from what is actually there.
func TestCheckpointsAdvanceUnderTheShaGuard(t *testing.T) {
	o := newOrigin(t)
	a, b := o.actor("A", testRun), o.actor("B", testRun)

	if got, err := a.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}
	if _, err := b.Fetch(); err != nil {
		t.Fatal(err)
	}
	if got, err := a.PutCheckpoint(testCheckpoint(StateDispatching, "wave 1 dispatching")); err != nil || got != Updated {
		t.Fatalf("A's second checkpoint: %v %v", got, err)
	}

	// B's view is now stale: it has not seen A's advance.
	got, err := b.PutCheckpoint(testCheckpoint(StateCancelled, "operator cancelled"))
	if err != nil {
		t.Fatal(err)
	}
	if got != ConflictStaleSHA {
		t.Fatalf("a stale writer's checkpoint was %q; it must be refused, or it overwrites whoever advanced the run", got)
	}
	if got.EffectPermitted() {
		t.Error("a refused checkpoint reported its effect as permitted")
	}
	if tags := o.tags(); len(tags) != 0 {
		t.Errorf("a REFUSED terminal checkpoint placed %v", tags)
	}
	onOrigin := readCheckpoint(t, o)
	if onOrigin.State != StateDispatching || onOrigin.Sequence != 2 {
		t.Errorf("origin holds %s at sequence %d; the stale writer got through", onOrigin.State, onOrigin.Sequence)
	}

	// Re-fetch and reconcile from what is actually there — the sequence
	// continues from A's, not from B's stale idea of it.
	if _, err := b.Fetch(); err != nil {
		t.Fatal(err)
	}
	if got, err := b.PutCheckpoint(testCheckpoint(StateCancelled, "operator cancelled")); err != nil || got != Updated {
		t.Fatalf("B after re-fetching: %v %v", got, err)
	}
	final := readCheckpoint(t, o)
	if final.State != StateCancelled || final.Sequence != 3 {
		t.Errorf("origin holds %s at sequence %d, want cancelled at 3", final.State, final.Sequence)
	}
	if o.commits() != 3 {
		t.Errorf("origin carries %d commits for three state changes", o.commits())
	}
}

// Checkpoint on state change, not on observation. At ten checkpoints an hour
// the cost is negligible, and at one commit per poll it is not.
func TestAnObservationWritesNothing(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("A", testRun)

	running := testCheckpoint(StateRunning, "3 of 4 ticks dispatched")
	running.Ticks = []TickState{{TickID: "mrq", State: "dispatched", Attempt: 1}}
	if got, err := s.PutCheckpoint(running); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}

	for i := 0; i < 3; i++ {
		got, err := s.PutCheckpoint(running)
		if err != nil {
			t.Fatal(err)
		}
		if got != NoChange {
			t.Fatalf("poll %d wrote %q; a poll that learns nothing writes nothing", i, got)
		}
	}
	if o.commits() != 1 {
		t.Errorf("origin carries %d commits after one state change and three polls", o.commits())
	}

	// A tick moving IS a state change, even with the run's own state unchanged.
	advanced := running
	advanced.Ticks = []TickState{{TickID: "mrq", State: "reported", Attempt: 1}}
	if got, err := s.PutCheckpoint(advanced); err != nil || got != Updated {
		t.Fatalf("a tick advancing: %v %v", got, err)
	}
	if got := readCheckpoint(t, o); got.Sequence != 2 {
		t.Errorf("the sequence is %d after two state changes", got.Sequence)
	}
}

// Existence IS the idempotency marker. Two reconcilers race the same dispatch;
// the second is refused by the repository, and must not start a job.
func TestADispatchMarkerIsCreatedOnce(t *testing.T) {
	o := newOrigin(t)
	a, b := o.actor("A", testRun), o.actor("B", testRun)
	for _, s := range []*Store{a, b} {
		if _, err := s.Fetch(); err != nil {
			t.Fatal(err)
		}
	}

	mine := testAttempt(1, "mrq")
	mine.JobHandle = map[string]any{"handle": "pid:A"}
	if got, err := a.PutAttempt(mine); err != nil || got != Created {
		t.Fatalf("A's dispatch: %v %v", got, err)
	}

	theirs := testAttempt(1, "mrq")
	theirs.JobHandle = map[string]any{"handle": "pid:B"}
	got, err := b.PutAttempt(theirs)
	if err != nil {
		t.Fatal(err)
	}
	if got != ConflictExists || got.EffectPermitted() {
		t.Fatalf("B's racing dispatch was %q (effect permitted: %v); the loser must not start a job",
			got, got.EffectPermitted())
	}

	// A retry is a different path, so it is never blocked by attempt 1.
	if got, err := a.PutAttempt(testAttempt(2, "mrq")); err != nil || got != Created {
		t.Fatalf("attempt 2: %v %v", got, err)
	}
	// And a restarted reconciler replaying attempt 2 is refused.
	if got, err := a.PutAttempt(testAttempt(2, "mrq")); err != nil || got != ConflictExists {
		t.Fatalf("a replayed attempt 2: %v %v", got, err)
	}

	fresh := o.actor("C", testRun)
	if _, err := fresh.Fetch(); err != nil {
		t.Fatal(err)
	}
	first, ok, err := fresh.Attempt(1)
	if err != nil || !ok {
		t.Fatalf("read attempt 1 back: %v %v", ok, err)
	}
	if first.JobHandle["handle"] != "pid:A" {
		t.Errorf("attempt 1 on origin holds %v; the loser overwrote the winner", first.JobHandle)
	}
}

// Evidence is a record of something that already happened, and a validated
// decision is a thing a model was paid for once. Neither can be overwritten.
func TestEvidenceAndDecisionsLandOnce(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("A", testRun)

	if got, err := s.PutEvidence(testEvidence("gate-integrated-go-test", "pass")); err != nil || got != Created {
		t.Fatalf("the evidence: %v %v", got, err)
	}
	if got, err := s.PutEvidence(testEvidence("gate-integrated-go-test", "fail")); err != nil || got != ConflictExists {
		t.Fatalf("evidence written twice: %v %v", got, err)
	}
	if got, err := s.PutDecision(testDecision(1, "proceed")); err != nil || got != Created {
		t.Fatalf("the decision: %v %v", got, err)
	}
	if got, err := s.PutDecision(testDecision(1, "hold")); err != nil || got != ConflictExists {
		t.Fatalf("decision written twice: %v %v", got, err)
	}

	fresh := o.actor("B", testRun)
	if _, err := fresh.Fetch(); err != nil {
		t.Fatal(err)
	}
	e, ok, err := fresh.Evidence("gate-integrated-go-test")
	if err != nil || !ok {
		t.Fatalf("read the evidence back: %v %v", ok, err)
	}
	if e.Result != "pass" {
		t.Errorf("the evidence on origin says %q; the second write got through", e.Result)
	}
	d, ok, err := fresh.Decision(1)
	if err != nil || !ok {
		t.Fatalf("read the decision back: %v %v", ok, err)
	}
	if d.Response["verdict"] != "proceed" {
		t.Errorf("the decision on origin says %v", d.Response)
	}
}

// Terminal state places `ticfac/run-<run-id>` on origin, so the full history
// stays reachable for a post-mortem without living in the target's log — and a
// failed or cancelled run's is as reachable as a successful one's.
func TestTerminalStatePlacesTheRunTag(t *testing.T) {
	for _, state := range []State{StateCompleted, StateFailed, StateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			o := newOrigin(t)
			s := o.actor("A", testRun)

			if _, err := s.PutCheckpoint(testCheckpoint(StateRunning, "wave 1 running")); err != nil {
				t.Fatal(err)
			}
			if _, placed, err := s.RunTag(); err != nil || placed {
				t.Fatalf("the tag is on origin before the run is over (%v %v)", placed, err)
			}

			if got, err := s.PutCheckpoint(testCheckpoint(state, "the run is over")); err != nil || got != Updated {
				t.Fatalf("the terminal checkpoint: %v %v", got, err)
			}
			sha, placed, err := s.RunTag()
			if err != nil {
				t.Fatal(err)
			}
			if !placed {
				t.Fatalf("no %s on origin at terminal state %s", TagName(testRun), state)
			}
			if want := strings.TrimSpace(gitRun(t, o.bare, "rev-parse", o.branch)); sha != want {
				t.Errorf("the tag is at %s and the run's last commit is %s", sha, want)
			}
			if got := o.tags(); !reflect.DeepEqual(got, []string{TagName(testRun)}) {
				t.Errorf("origin carries tags %v", got)
			}

			// A restart replaying the terminal checkpoint writes nothing and
			// leaves the tag where it is.
			before := o.commits()
			if got, err := s.PutCheckpoint(testCheckpoint(state, "the run is over")); err != nil || got != NoChange {
				t.Fatalf("replaying the terminal checkpoint: %v %v", got, err)
			}
			if o.commits() != before {
				t.Error("replaying the terminal checkpoint wrote a commit")
			}
			if _, placed, err := s.RunTag(); err != nil || !placed {
				t.Errorf("the tag went away on a replay (%v %v)", placed, err)
			}
		})
	}
}

// What a restarted reconciler does: recovery is a fetch, and then this.
func TestARestartedReconcilerReadsTheRunBack(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("A", testRun)

	if _, err := s.PutCheckpoint(testCheckpoint(StateDispatching, "wave 1")); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		if _, err := s.PutAttempt(testAttempt(n, "mrq")); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"gate-worker-go-vet", "gate-integrated-go-test"} {
		if _, err := s.PutEvidence(testEvidence(key, "pass")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutDecision(testDecision(1, "proceed")); err != nil {
		t.Fatal(err)
	}

	restarted := o.actor("A-restarted", testRun)
	if _, err := restarted.Fetch(); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := restarted.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("read the checkpoint back: %v %v", ok, err)
	}
	if checkpoint.State != StateDispatching || checkpoint.Sequence != 1 {
		t.Errorf("the checkpoint reads back as %s at %d", checkpoint.State, checkpoint.Sequence)
	}
	attempts, err := restarted.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Attempt != 1 || attempts[1].Attempt != 2 {
		t.Errorf("the attempts read back as %+v", attempts)
	}
	if got := restarted.EvidenceKeys(); !reflect.DeepEqual(got, []string{"gate-integrated-go-test", "gate-worker-go-vet"}) {
		t.Errorf("the evidence keys read back as %v", got)
	}
	decisions, err := restarted.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || !decisions[0].Validated {
		t.Errorf("the decisions read back as %+v", decisions)
	}
}

// What lands on origin is validated by the bundle, not by this package's idea
// of the bundle. These are the bytes a reader years from now will find.
func TestWhatLandsOnOriginValidatesAgainstTheBundle(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("A", testRun)

	if _, err := s.PutCheckpoint(testCheckpoint(StateGating, "wave 1 fan-in complete")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAttempt(testAttempt(1, "mrq")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDecision(testDecision(1, "proceed")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutEvidence(testEvidence("gate-integrated-go-test", "pass")); err != nil {
		t.Fatal(err)
	}

	schemas, defs := contractSchemas(t)
	evidence, evidenceDefs := evidenceSchema(t)
	for path, s := range map[string]struct {
		schema string
	}{
		CheckpointPath(testRun):  {"checkpoint"},
		AttemptPath(testRun, 1):  {"attempt"},
		DecisionPath(testRun, 1): {"decision"},
	} {
		raw := []byte(gitRun(t, o.bare, "show", o.branch+":"+path))
		if errs := validateDocument(t, schemas[s.schema], defs, raw); len(errs) != 0 {
			t.Errorf("%s on origin is refused by schemas.%s:\n  %s", path, s.schema, strings.Join(errs, "\n  "))
		}
	}
	raw := []byte(gitRun(t, o.bare, "show", o.branch+":"+EvidencePath(testRun, "gate-integrated-go-test")))
	if errs := validateDocument(t, evidence, evidenceDefs, raw); len(errs) != 0 {
		t.Errorf("the evidence on origin is refused by %s:\n  %s", jobProtocolFile, strings.Join(errs, "\n  "))
	}
}

// A record whose provenance names another run, or a key that would escape the
// run directory, is refused before anything is written. The run id and the
// evidence key reach this store from a tracker and a check definition, and a
// record placed in the wrong run's directory is worse than no record.
func TestTheStoreRefusesRecordsThatDoNotBelongToItsRun(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("A", testRun)

	elsewhere := testAttempt(1, "mrq")
	elsewhere.Provenance.RunID = "r-other"
	if _, err := s.PutAttempt(elsewhere); err == nil {
		t.Error("an attempt whose provenance names another run was written into this one's directory")
	}

	escaping := testEvidence("../../etc/passwd", "pass")
	if _, err := s.PutEvidence(escaping); err == nil {
		t.Error("an evidence key that escapes the run directory was accepted")
	}
	unvalidated := testDecision(1, "proceed")
	unvalidated.Validated = false
	if _, err := s.PutDecision(unvalidated); err == nil {
		t.Error("an unvalidated decision was written; that is how a hallucinated wave gets dispatched")
	}
	if o.commits() != 0 {
		t.Errorf("origin carries %d commits after three refusals", o.commits())
	}
}

func readCheckpoint(t *testing.T, o *origin) Checkpoint {
	t.Helper()
	raw := gitRun(t, o.bare, "show", o.branch+":"+CheckpointPath(testRun))
	var c Checkpoint
	if err := decodeRecord([]byte(raw), &c); err != nil {
		t.Fatalf("the checkpoint on origin: %v", err)
	}
	return c
}
