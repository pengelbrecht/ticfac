package runstate

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The seven CAS sequences of contracts/ticfac-run-state.json, replayed against
// a REAL bare origin.
//
// internal/contracts/parity replays them over an in-memory fake, which is a
// model of the one git behaviour the rules depend on. This is the same
// sequences over the thing itself: every outcome below comes from git accepting
// or refusing a `--force-with-lease` push, and every final state is read back
// out of the bare repository.

// runSequence drives one fixture sequence, giving each named actor its own
// clone and its own store. It returns the stores so a caller can count pushes.
func runSequence(t *testing.T, o *origin, seq casSequence) map[string]*Store {
	t.Helper()
	actors := map[string]*Store{}
	store := func(name string) *Store {
		if s, ok := actors[name]; ok {
			return s
		}
		actors[name] = o.actor(name, sequenceRunID(seq))
		return actors[name]
	}

	for i, step := range seq.Steps {
		var (
			got Outcome
			err error
		)
		content := encodeStep(t, step)
		s := store(step.Actor)
		switch step.Op {
		case "fetch":
			got, err = s.Fetch()
		case "observe":
			got, err = s.Observe()
		case "commit_local":
			got, err = s.CommitLocal(step.Path, content)
		case "create_if_absent":
			got, err = s.CreateIfAbsent(step.Path, content)
		case "update_if_sha":
			got, err = s.UpdateIfSHA(step.Path, content)
		default:
			t.Fatalf("step %d uses op %q, which this store does not implement", i, step.Op)
		}
		if err != nil {
			t.Fatalf("step %d (%s by %s): %v", i, step.Op, step.Actor, err)
		}
		if string(got) != step.Expect {
			t.Fatalf("step %d (%s by %s): outcome %q, contract says %q",
				i, step.Op, step.Actor, got, step.Expect)
		}
		// The load-bearing assertion: a refused compare-and-swap means the
		// effect must NOT happen.
		if step.EffectPermitted != nil && got.EffectPermitted() != *step.EffectPermitted {
			t.Fatalf("step %d: the contract says effect_permitted=%v and the guard said %v (%s)",
				i, *step.EffectPermitted, got.EffectPermitted(), got)
		}
	}
	return actors
}

// sequenceRunID reads the run the sequence's paths are about, so the store's
// own run id matches the records the fixture writes.
func sequenceRunID(seq casSequence) string {
	for _, step := range seq.Steps {
		if rest, ok := strings.CutPrefix(step.Path, Root+"/runs/"); ok {
			if id, _, ok := strings.Cut(rest, "/"); ok {
				return id
			}
		}
	}
	return "r-unknown"
}

func encodeStep(t *testing.T, step casStep) []byte {
	t.Helper()
	if step.Content == nil {
		return nil
	}
	raw, err := json.Marshal(step.Content)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCASSequencesRunAgainstARealOrigin(t *testing.T) {
	c := loadContract(t)
	if len(c.CAS.Sequences) != 7 {
		t.Errorf("the contract carries %d CAS sequences; bundle 3.0.0 carries 7", len(c.CAS.Sequences))
	}

	declared := map[string]map[string]bool{}
	for _, op := range c.CAS.Fake.Ops {
		declared[op.Op] = map[string]bool{}
		for _, outcome := range op.Outcomes {
			declared[op.Op][outcome] = true
		}
	}
	reached := map[string]map[string]bool{}

	for _, seq := range c.CAS.Sequences {
		t.Run(seq.ID, func(t *testing.T) {
			o := newOrigin(t)
			actors := runSequence(t, o, seq)

			for _, step := range seq.Steps {
				if reached[step.Op] == nil {
					reached[step.Op] = map[string]bool{}
				}
				reached[step.Op][step.Expect] = true
			}

			pushes := 0
			for _, s := range actors {
				pushes += s.Pushes()
			}
			if pushes != seq.Final.OriginWrites {
				t.Errorf("the stores pushed %d times, contract says %d", pushes, seq.Final.OriginWrites)
			}
			// And the same number counted from origin itself, because a store
			// counting its own successes is not evidence that origin moved.
			if got := o.commits(); got != seq.Final.OriginWrites {
				t.Errorf("origin carries %d run-state commits, contract says %d", got, seq.Final.OriginWrites)
			}

			files := o.files()
			if len(files) != len(seq.Final.Files) {
				t.Errorf("origin holds %d records, contract says %d: %v",
					len(files), len(seq.Final.Files), sortedPaths(files))
			}
			for path, want := range seq.Final.Files {
				got, ok := files[path]
				if !ok {
					t.Errorf("%s is not on origin", path)
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s holds %v, contract says %v", path, got, want)
				}
			}
		})
	}

	for op, outcomes := range declared {
		for outcome := range outcomes {
			if !reached[op][outcome] {
				t.Errorf("op %q declares outcome %q, which no sequence reaches against real git", op, outcome)
			}
		}
	}
}

// The negative control. A compare-and-swap that has stopped guarding does not
// raise: it lets a second reconciler dispatch the same attempt, and the run
// pays for both jobs. So the guard is switched off and every sequence that
// expects a refusal must stop matching the contract.
func TestDisablingTheGuardBreaksEverySequenceThatExpectsARefusal(t *testing.T) {
	c := loadContract(t)

	refusing := 0
	for _, seq := range c.CAS.Sequences {
		expectsRefusal := false
		for _, step := range seq.Steps {
			if strings.HasPrefix(step.Expect, "conflict_") {
				expectsRefusal = true
			}
		}
		if !expectsRefusal {
			continue
		}
		refusing++

		t.Run(seq.ID, func(t *testing.T) {
			o := newOrigin(t)
			actors := map[string]*Store{}
			store := func(name string) *Store {
				if s, ok := actors[name]; ok {
					return s
				}
				s := o.actor(name, sequenceRunID(seq))
				s.guardOff = true
				actors[name] = s
				return s
			}

			diverged := false
			for _, step := range seq.Steps {
				s := store(step.Actor)
				content := encodeStep(t, step)
				var (
					got Outcome
					err error
				)
				switch step.Op {
				case "fetch":
					got, err = s.Fetch()
				case "observe":
					got, err = s.Observe()
				case "commit_local":
					got, err = s.CommitLocal(step.Path, content)
				case "create_if_absent":
					got, err = s.CreateIfAbsent(step.Path, content)
				case "update_if_sha":
					got, err = s.UpdateIfSHA(step.Path, content)
				}
				if err != nil {
					t.Fatalf("%s by %s with the guard off: %v", step.Op, step.Actor, err)
				}
				if string(got) != step.Expect {
					diverged = true
				}
			}
			if !diverged && o.commits() == seq.Final.OriginWrites {
				t.Errorf("%s passes with the compare-and-swap disabled — the guard is not what it tests", seq.ID)
			}
		})
	}
	if refusing < 4 {
		t.Errorf("only %d sequences expect a refusal; the races, the restart and the stale view all do", refusing)
	}
}

// Durable means pushed, and this is the assertion that the phrase has teeth: a
// local commit moves the local ref and origin does not have the record.
func TestALocalCommitLeavesNothingOnOrigin(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("A", "r-2f9c")
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}

	before := gitRun(t, o.bare, "rev-parse", o.branch)
	if _, err := s.CommitLocal(CheckpointPath("r-2f9c"), []byte(`{"sequence":1}`)); err != nil {
		t.Fatal(err)
	}
	if after := gitRun(t, o.bare, "rev-parse", o.branch); after != before {
		t.Error("a local commit moved origin's ref; then durable would mean committed, and a dying container would take the record with it")
	}
	if len(o.files()) != 0 {
		t.Errorf("origin holds %v after a local commit only", sortedPaths(o.files()))
	}
	if s.Pushes() != 0 {
		t.Errorf("the store counted %d pushes for a local commit", s.Pushes())
	}
}

// The lease is on the branch ref and the contract's guard is on a path, so a
// concurrent write to a DIFFERENT path must not be reported as a conflict: it
// is a commit to rebuild on the new head. This is the difference between
// reconciling and retrying blindly, and no fixture sequence reaches it because
// the fake has no ref.
func TestAConcurrentWriteToAnotherPathIsNotAConflict(t *testing.T) {
	o := newOrigin(t)
	a, b := o.actor("A", "r-2f9c"), o.actor("B", "r-2f9c")
	for _, s := range []*Store{a, b} {
		if _, err := s.Fetch(); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := a.CreateIfAbsent(AttemptPath("r-2f9c", 1), []byte(`{"attempt":1}`)); err != nil || got != Created {
		t.Fatalf("A's attempt 1: %v %v", got, err)
	}
	// B's view is now stale at the REF level and current at the PATH level.
	got, err := b.CreateIfAbsent(AttemptPath("r-2f9c", 2), []byte(`{"attempt":2}`))
	if err != nil {
		t.Fatalf("B's attempt 2: %v", err)
	}
	if got != Created {
		t.Fatalf("B's attempt 2 was refused with %q; nobody wrote that path, so B's dispatch is not a replay", got)
	}
	if n := len(o.files()); n != 2 {
		t.Errorf("origin holds %d records, want both attempts: %v", n, sortedPaths(o.files()))
	}
}

// The same rebuild, for a sha-guarded update: the ref moved for another path,
// the checkpoint's blob did not, and the writer's view is therefore NOT stale.
// Reporting a conflict here would make a reconciler re-plan every time any
// other record landed.
func TestAnUpdateIsNotStaleWhenAnotherPathMovedTheRef(t *testing.T) {
	o := newOrigin(t)
	a, b := o.actor("A", "r-2f9c"), o.actor("B", "r-2f9c")

	if _, err := a.Fetch(); err != nil {
		t.Fatal(err)
	}
	if got, err := a.CreateIfAbsent(CheckpointPath("r-2f9c"), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}
	// B fetches the checkpoint, then A writes a DIFFERENT path.
	if _, err := b.Fetch(); err != nil {
		t.Fatal(err)
	}
	if got, err := a.CreateIfAbsent(AttemptPath("r-2f9c", 1), []byte(`{"attempt":1}`)); err != nil || got != Created {
		t.Fatalf("the attempt: %v %v", got, err)
	}

	got, err := b.UpdateIfSHA(CheckpointPath("r-2f9c"), []byte(`{"sequence":2}`))
	if err != nil {
		t.Fatalf("B's update: %v", err)
	}
	if got != Updated {
		t.Fatalf("B's update was %q; nobody touched the checkpoint, so B's view of it is not stale", got)
	}
	files := o.files()
	if len(files) != 2 {
		t.Errorf("origin holds %v", sortedPaths(files))
	}
	if seq := files[CheckpointPath("r-2f9c")]["sequence"]; seq != float64(2) {
		t.Errorf("the checkpoint on origin is at sequence %v", seq)
	}
	if files[AttemptPath("r-2f9c", 1)] == nil {
		t.Error("rebuilding B's commit dropped the attempt A had written")
	}
}

// The create guard is against ORIGIN, not against what this writer happens to
// remember. A writer that rebuilt a commit on a head it learned about mid-push
// knows a NEWER ref and an OLDER set of paths; if the guard trusted the second,
// a replayed dispatch would sail past a lease that has nothing to say about
// paths, and overwrite the attempt marker another reconciler is already running
// a job against.
func TestACreateIsRefusedByOriginEvenAfterRebuildingOnANewHead(t *testing.T) {
	o := newOrigin(t)
	a, b := o.actor("A", "r-2f9c"), o.actor("B", "r-2f9c")
	for _, s := range []*Store{a, b} {
		if _, err := s.Fetch(); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := a.CreateIfAbsent(AttemptPath("r-2f9c", 1), []byte(`{"attempt":1,"dispatched_by":"A"}`)); err != nil || got != Created {
		t.Fatalf("A's attempt 1: %v %v", got, err)
	}
	// B's push is rebuilt on A's head, so B now knows a ref it never fetched.
	if got, err := b.CreateIfAbsent(AttemptPath("r-2f9c", 2), []byte(`{"attempt":2,"dispatched_by":"B"}`)); err != nil || got != Created {
		t.Fatalf("B's attempt 2: %v %v", got, err)
	}

	got, err := b.CreateIfAbsent(AttemptPath("r-2f9c", 1), []byte(`{"attempt":1,"dispatched_by":"B"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != ConflictExists || got.EffectPermitted() {
		t.Fatalf("B's replay of attempt 1 was %q; A already dispatched it, so B must be refused", got)
	}
	if by := o.files()[AttemptPath("r-2f9c", 1)]["dispatched_by"]; by != "A" {
		t.Errorf("attempt 1 on origin was dispatched by %v; the marker of a running job was overwritten", by)
	}
}

func sortedPaths(files map[string]map[string]any) []string {
	out := make([]string, 0, len(files))
	for path := range files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
