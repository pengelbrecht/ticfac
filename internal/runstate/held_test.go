package runstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tick f61: one push per step, not one per record.
//
// A held store keeps writing exactly the commits it always wrote — one per
// record, in order, each with its own message — and sends them to origin as
// ONE push when the step releases them. What changes is the number of round
// trips to the forge, not what lands: the checkpoint's history is still a
// commit per state, because a resumed run reads that history (the
// CheckpointHistory readers in reconcile), and a squashed step would erase
// the intermediate states those readers look for.

// countPushes arms origin's post-receive hook to count the pushes it accepts,
// and answers a reader of the count.
func (o *origin) countPushes(t *testing.T) func() int {
	t.Helper()
	counter := filepath.Join(o.root, "pushes-accepted")
	hook := "#!/bin/sh\necho x >> \"" + counter + "\"\n"
	if err := os.WriteFile(filepath.Join(o.bare, "hooks", "post-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() int {
		raw, err := os.ReadFile(counter)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "x")
	}
}

// subjects is the commit messages on origin's branch, oldest first.
func (o *origin) subjects() []string {
	o.t.Helper()
	out := strings.TrimSpace(gitRun(o.t, o.bare, "log", "--reverse", "--format=%s", o.branch))
	return strings.Split(out, "\n")
}

// trackerChange is one `.tick/` record as the tracker would hand it to a held
// store: a blob in the store's object database and the path it goes to.
func trackerChange(t *testing.T, s *Store, path, content string) []TreeChange {
	t.Helper()
	blob, err := s.git.writeBlob([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return []TreeChange{{Path: path, Mode: "100644", Blob: blob}}
}

func TestAHeldStepLandsAsOnePushWithEveryRecordItsOwnCommit(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	s := o.actor("reconciler", testRun)

	if got, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}
	before, commitsBefore := pushes(), o.commits()

	s.Hold()
	if got, err := s.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil || got != Updated {
		t.Fatalf("a held checkpoint: %v %v", got, err)
	}
	var landedAs string
	if _, err := s.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, s, ".tick/issues/a1.json", "{\"id\":\"a1\",\"status\":\"closed\"}\n"), true,
		func(commit string) { landedAs = commit }); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PutCheckpoint(testCheckpoint(StateRunning, "a1 is closed")); err != nil || got != Updated {
		t.Fatalf("the second held checkpoint: %v %v", got, err)
	}

	// Nothing has reached origin while the step is held: a held record is
	// not yet a record, and nothing may act on it as one.
	if got := pushes(); got != before {
		t.Fatalf("%d pushes while the step was held", got-before)
	}
	if got := o.commits(); got != commitsBefore {
		t.Fatalf("origin moved while the step was held: %d commits, was %d", got, commitsBefore)
	}
	if landedAs != "" {
		t.Fatalf("the tracker's record reported landing as %s before the step was released", landedAs)
	}

	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if got := pushes() - before; got != 1 {
		t.Errorf("the step's three records took %d pushes, want 1", got)
	}
	if got := o.commits() - commitsBefore; got != 3 {
		t.Errorf("the step landed %d commits; every record is still its own commit, want 3", got)
	}
	subjects := o.subjects()
	tail := subjects[len(subjects)-3:]
	want := []string{
		"ticfac run " + testRun + ": update " + CheckpointPath(testRun),
		"ticfac run " + testRun + ": close a1",
		"ticfac run " + testRun + ": update " + CheckpointPath(testRun),
	}
	for i := range want {
		if tail[i] != want[i] {
			t.Errorf("commit %d of the step is %q, want %q (the records land in the order they were written)",
				i+1, tail[i], want[i])
		}
	}
	if landedAs == "" {
		t.Error("the tracker's record was never told it landed")
	} else if got := strings.TrimSpace(gitRun(t, o.bare, "log", "-1", "--format=%s", landedAs)); got != want[1] {
		t.Errorf("the tracker's record was told it landed as %s (%q), not its own commit", landedAs, got)
	}

	// A restarted reader sees every intermediate state, not just the last:
	// the checkpoint's history is the record a resume reads rejections and
	// start refusals out of.
	reader := o.actor("reader", testRun)
	history, err := reader.CheckpointHistory()
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, c := range history {
		states = append(states, string(c.State))
	}
	if strings.Join(states, ",") != "admitted,publishing,running" {
		t.Errorf("a fresh reader's checkpoint history is %v, want admitted,publishing,running", states)
	}
	if got := s.Sends(); got != 2 {
		t.Errorf("the store counts %d pushes, want 2 (the create and the step)", got)
	}
}

func TestAHeldRecordIsReadBackBeforeItLands(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)
	if _, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	s.Hold()
	defer s.Abandon()
	if _, err := s.PutCheckpoint(testCheckpoint(StateGating, "gating a1")); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.Checkpoint()
	if err != nil || !ok || c.State != StateGating || c.Sequence != 2 {
		t.Fatalf("the writer reads back %+v %v %v; it must read its own held write", c, ok, err)
	}
	history, err := s.CheckpointHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].State != StateGating {
		t.Errorf("the writer's own history omits its held checkpoint: %d entries", len(history))
	}
}

// A run killed mid-step leaves origin exactly where the step began: the
// state a kill just BEFORE the step's first record already left it in, which
// every resume already reads. Batching removes the intermediate crash states;
// it adds none.
func TestAStepKilledBeforeItsReleaseLandsNothing(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	s := o.actor("reconciler", testRun)
	if _, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	before, commitsBefore := pushes(), o.commits()

	s.Hold()
	if _, err := s.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, s, ".tick/issues/a1.json", "closed\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	// The kill: the process is gone, and nothing it held is sent.
	s.Abandon()

	if pushes() != before || o.commits() != commitsBefore {
		t.Fatalf("a killed step reached origin: %d pushes, %d commits", pushes()-before, o.commits()-commitsBefore)
	}
	reader := o.actor("restarted", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	c, ok, err := reader.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint after the kill: %v", err)
	}
	if c.State != StateAdmitted {
		t.Errorf("a restart reads %s; the step was killed before it landed, so it must read the state before it",
			c.State)
	}
	if out := gitRun(t, o.bare, "ls-tree", "-r", o.branch, "--", ".tick"); strings.TrimSpace(out) != "" {
		t.Errorf("the killed step's tracker record is on origin: %s", out)
	}
}

// A create is a compare-and-swap whose answer gates an effect, so it is never
// held: it lands at once, and carries the records held before it in the same
// push.
func TestACreateInAHeldStepLandsAtOnceCarryingTheRecordsBeforeIt(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	s := o.actor("reconciler", testRun)
	if _, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	before := pushes()

	s.Hold()
	if _, err := s.PutCheckpoint(testCheckpoint(StateDispatching, "dispatching a1 as attempt 1")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PutAttempt(testAttempt(1, "a1")); err != nil || got != Created {
		t.Fatalf("the dispatch marker: %v %v", got, err)
	}
	if got := pushes() - before; got != 1 {
		t.Fatalf("the marker and the checkpoint before it took %d pushes, want exactly 1 — and before Release", got)
	}
	files := o.files()
	if _, ok := files[AttemptPath(testRun, 1)]; !ok {
		t.Error("the dispatch marker is not on origin after its create answered created")
	}
	if c := readCheckpoint(t, o); c.State != StateDispatching {
		t.Errorf("the checkpoint held before the marker is %s on origin, want dispatching", c.State)
	}
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if got := pushes() - before; got != 1 {
		t.Errorf("releasing an empty step pushed: %d pushes", got)
	}
}

// Another writer moving the branch for ANOTHER path is not a conflict: the
// step is rebuilt on the new head and still lands as one push.
func TestAHeldStepIsRebuiltOnAForeignWriteToAnotherPath(t *testing.T) {
	o := newOrigin(t)
	a, b := o.actor("A", testRun), o.actor("B", "r-other")
	if _, err := a.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	a.Hold()
	if _, err := a.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, a, ".tick/issues/a1.json", "closed\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	// B lands a record of its own run in the middle of A's step.
	if got, err := b.PutCheckpoint(Checkpoint{
		RunID: "r-other", EpicID: "qeu", State: StateAdmitted, Reason: "admitted",
		Provenance: func() Provenance { p := testProvenance(PhaseWorker); p.RunID = "r-other"; return p }(),
	}); err != nil || got != Created {
		t.Fatalf("B's write: %v %v", got, err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("a step whose paths nobody else touched was refused: %v", err)
	}
	files := o.files()
	if _, ok := files[CheckpointPath("r-other")]; !ok {
		t.Error("A's rebuilt step clobbered B's record")
	}
	if c := readCheckpoint(t, o); c.State != StatePublishing {
		t.Errorf("A's held checkpoint is %s on origin, want publishing", c.State)
	}
	if out := gitRun(t, o.bare, "show", o.branch+":.tick/issues/a1.json"); strings.TrimSpace(out) != "closed" {
		t.Errorf("A's tracker record is %q on origin", out)
	}
}

// A held checkpoint whose guard is lost is the conflict a per-record write
// would have answered — and since the step's later records were written on
// the strength of it, none of the step lands.
func TestAHeldStepWhoseCheckpointGuardIsLostLandsNothing(t *testing.T) {
	o := newOrigin(t)
	a := o.actor("A", testRun)
	if _, err := a.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	b := o.actor("B", testRun)
	if _, err := b.Fetch(); err != nil {
		t.Fatal(err)
	}

	a.Hold()
	if _, err := a.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, a, ".tick/issues/a1.json", "closed\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	// B advances the SAME run's checkpoint under A.
	if got, err := b.PutCheckpoint(testCheckpoint(StateCancelled, "operator cancelled")); err != nil || got != Updated {
		t.Fatalf("B's checkpoint: %v %v", got, err)
	}

	err := a.Release()
	var conflict *HeldConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("releasing a step whose checkpoint guard was lost answered %v, want a HeldConflict", err)
	}
	if conflict.Outcome != ConflictStaleSHA || conflict.Path != CheckpointPath(testRun) {
		t.Errorf("the conflict names %s on %s, want %s on %s", conflict.Outcome, conflict.Path,
			ConflictStaleSHA, CheckpointPath(testRun))
	}
	if c := readCheckpoint(t, o); c.State != StateCancelled {
		t.Errorf("origin's checkpoint is %s; the stale step got through", c.State)
	}
	if out := gitRun(t, o.bare, "ls-tree", "-r", o.branch, "--", ".tick"); strings.TrimSpace(out) != "" {
		t.Errorf("a step whose guard was lost landed part of itself: %s", out)
	}
	// And the writer is not left believing its own held write.
	if _, err := a.Fetch(); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := a.Checkpoint(); c == nil || c.State != StateCancelled {
		t.Errorf("after the conflict the writer reads %+v, want origin's cancelled", c)
	}
}

// A create whose guard is lost answers conflict_exists exactly as it would
// alone, and the records held before it land without it: they would have
// landed before the create was ever attempted.
func TestAHeldCreateThatLosesItsGuardLetsTheRecordsBeforeItLand(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	a := o.actor("A", testRun)
	if _, err := a.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	b := o.actor("B", testRun)
	if _, err := b.Fetch(); err != nil {
		t.Fatal(err)
	}

	a.Hold()
	defer a.Abandon()
	if _, err := a.StageChanges("ticfac run "+testRun+": note a1",
		trackerChange(t, a, ".tick/issues/a1.json", "noted\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := b.PutAttempt(testAttempt(1, "a1")); err != nil || got != Created {
		t.Fatalf("B's marker: %v %v", got, err)
	}
	before := pushes()
	got, err := a.PutAttempt(testAttempt(1, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	if got != ConflictExists {
		t.Fatalf("A's marker for an attempt B already dispatched answered %q; it must be refused", got)
	}
	if out := gitRun(t, o.bare, "show", o.branch+":.tick/issues/a1.json"); strings.TrimSpace(out) != "noted" {
		t.Errorf("the record held before the refused create is %q on origin, want it landed", out)
	}
	if n := pushes() - before; n != 1 {
		t.Errorf("the refused create and its carried record took %d accepted pushes, want 1", n)
	}
}

// A step's push whose acknowledgement was lost (tick o82) is recognised as
// this writer's own on the retry, exactly as a single record's is.
func TestAHeldStepWhoseAcknowledgementWasLostIsItsOwn(t *testing.T) {
	o := newOrigin(t)
	s, notices := flakyStore(t, o, testRun)
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	s.Hold()
	if _, err := s.StageChanges("ticfac run "+testRun+": note a1",
		trackerChange(t, s, ".tick/issues/a1.json", "noted\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, s, ".tick/issues/a1.json", "closed\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	o.lateLanding(t, "$new")
	if err := s.Release(); err != nil {
		t.Fatalf("a step whose push landed late was not recognised as this writer's own: %v", err)
	}
	if len(*notices) == 0 {
		t.Error("the dropped push was never retried, so this test proved nothing")
	}
	if n := o.commits(); n != 2 {
		t.Errorf("origin carries %d commits for a two-record step, want 2", n)
	}
}

// Re-reading origin inside a step keeps the step's own records on top of what
// origin now holds: the chain is rebased, not sent, and the step still lands
// as one push.
func TestAFetchInsideAHeldStepKeepsTheStepsRecords(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	a, b := o.actor("A", testRun), o.actor("B", "r-other")
	if _, err := a.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	a.Hold()
	if _, err := a.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil {
		t.Fatal(err)
	}
	if got, err := b.PutCheckpoint(Checkpoint{
		RunID: "r-other", EpicID: "qeu", State: StateAdmitted, Reason: "admitted",
		Provenance: func() Provenance { p := testProvenance(PhaseWorker); p.RunID = "r-other"; return p }(),
	}); err != nil || got != Created {
		t.Fatalf("B's write: %v %v", got, err)
	}
	before := pushes()
	if _, err := a.Fetch(); err != nil {
		t.Fatal(err)
	}
	if pushes() != before {
		t.Fatal("a fetch inside a held step pushed the step")
	}
	if c, _, _ := a.Checkpoint(); c == nil || c.State != StatePublishing {
		t.Fatalf("after a fetch the writer reads %+v; it lost its own held checkpoint", c)
	}
	if _, ok, _ := a.Read(CheckpointPath("r-other")); !ok {
		t.Error("the fetch inside the step did not read what origin now holds")
	}
	if _, err := a.PutCheckpoint(testCheckpoint(StateRunning, "a1 is closed")); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if n := pushes() - before; n != 1 {
		t.Errorf("the step took %d pushes after its fetch, want 1", n)
	}
	if c := readCheckpoint(t, o); c.State != StateRunning || c.Sequence != 3 {
		t.Errorf("origin holds %s at %d, want running at 3", c.State, c.Sequence)
	}
	if _, ok := o.files()[CheckpointPath("r-other")]; !ok {
		t.Error("the step clobbered B's record")
	}
}
