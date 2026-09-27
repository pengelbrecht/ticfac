package runstate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Tick o82: a run-state write whose acknowledgement was lost.
//
// epic-gvc, 2026-09-25: the reconciler pushed its 'collecting' checkpoint, the
// push LANDED on origin (commit 5137b27d), and the reconciler never heard so —
// the network between them flapped. The retry then found the branch already
// moved, re-examined the per-path guard, found the checkpoint's blob no longer
// the one it had fetched, and reported conflict_stale_sha: "the run state
// moved under this reconciler". It had moved under it by its own hand. That is
// an unclassified stop, and the run halted for a person.
//
// Nothing here is a model: the store pushes to a real bare repository, and the
// repository's own pre-receive hook is what makes the network misbehave. On
// the first push it kills the receive-pack that ran it — the client sees the
// connection die mid-push, which the retry bound classifies as transient and
// waits through. On the second push it lands the write the first push carried
// before letting the second one through, which is what a remote that committed
// the first push LATE looks like from the second: the advertisement said the
// ref was at the old head, and by the time the update ran it was not. The
// second push is refused, exactly as the one in epic-gvc was.

// lateLanding arms origin's pre-receive hook. land is the shell expression
// for the commit the second push finds already on the branch: "$new" is the
// commit the push itself carries (the writer's own lost-ack write), and
// anything else is a write by somebody else that raced the retry.
func (o *origin) lateLanding(t *testing.T, land string) {
	t.Helper()
	o.landingHook(t, land, true)
}

// landingHook is lateLanding with the dropped acknowledgement optional:
// without it, the FIRST push finds land already on the branch, and this
// writer has no push whose outcome it could not know.
func (o *origin) landingHook(t *testing.T, land string, dropFirst bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the ack-dropping hook is a /bin/sh script")
	}
	counter := filepath.Join(o.root, "pushes-seen")
	if !dropFirst {
		// Start the count past the push that would have been dropped.
		if err := os.WriteFile(counter, []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// git refuses ref updates from inside the quarantine a pre-receive hook
	// runs in; lifting GIT_QUARANTINE_PATH leaves the object directories as
	// they are, so the pushed commit is still readable, and the objects are
	// migrated into origin as soon as the hook exits 0.
	hook := "#!/bin/sh\n" +
		"n=$(cat \"" + counter + "\" 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"printf '%s\\n' \"$n\" > \"" + counter + "\"\n" +
		"read old new ref\n" +
		"case \"$n\" in\n" +
		"1) kill -9 $PPID; exit 1 ;;\n" +
		"2) land=" + land + "\n" +
		"   env -u GIT_QUARANTINE_PATH git update-ref \"$ref\" \"$land\" \"$old\" || exit 1 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(o.bare, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
}

// flakyStore is a store whose retry bound waits no wall-clock time and says
// every retry out loud, so a test can see the flap it arranged was retried.
func flakyStore(t *testing.T, o *origin, runID string) (*Store, *[]RemoteRetryNotice) {
	t.Helper()
	s := o.actor("a", runID)
	var notices []RemoteRetryNotice
	s.git.retry = RemoteRetry{
		Attempts: 4,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) {},
		Report:   func(n RemoteRetryNotice) { notices = append(notices, n) },
	}
	return s, &notices
}

// TestALostAckCheckpointIsTheWritersOwnAndTheRunContinues is the incident: a
// checkpoint update whose first push landed without an acknowledgement is
// recognised as this writer's own write, and the run writes on.
func TestALostAckCheckpointIsTheWritersOwnAndTheRunContinues(t *testing.T) {
	o := newOrigin(t)
	s, notices := flakyStore(t, o, testRun)
	if got, err := s.PutCheckpoint(testCheckpoint(StateRunning, "the run is running")); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}

	o.lateLanding(t, `"$new"`)
	got, err := s.PutCheckpoint(testCheckpoint(StateCollecting, "the wave is being collected"))
	if err != nil {
		t.Fatalf("the lost-ack checkpoint: %v", err)
	}
	if got != Updated {
		t.Fatalf("the checkpoint whose ack was lost came back %q: the run halts on its own write", got)
	}
	if len(*notices) == 0 {
		t.Fatal("the push was never retried: the hook did not drop the first acknowledgement")
	}
	if c, ok, err := s.Checkpoint(); err != nil || !ok || c.State != StateCollecting {
		t.Fatalf("this writer's view after its own lost-ack write: %+v %v %v", c, ok, err)
	}

	// And the run goes on: the next write is guarded on the checkpoint that
	// landed, and lands on top of it.
	if got, err := s.PutCheckpoint(testCheckpoint(StateRunning, "the run is running")); err != nil || got != Updated {
		t.Fatalf("the write after the lost ack: %v %v", got, err)
	}
	if c := o.files()[CheckpointPath(testRun)]; c["state"] != string(StateRunning) || c["sequence"] != float64(3) {
		t.Fatalf("origin's checkpoint after the run went on: %v", c)
	}
}

// TestALostAckCreateIsTheWritersOwn is the same seam on the create path: an
// attempt marker whose push landed unacknowledged is Created — NOT
// conflict_exists, which would tell the reconciler another one dispatched it.
func TestALostAckCreateIsTheWritersOwn(t *testing.T) {
	o := newOrigin(t)
	s, _ := flakyStore(t, o, testRun)
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	o.lateLanding(t, `"$new"`)
	got, err := s.CreateIfAbsent(AttemptPath(testRun, 1), []byte(`{"attempt":1}`))
	if err != nil || got != Created {
		t.Fatalf("the lost-ack create came back %q %v", got, err)
	}
	if s.Pushes() != 1 {
		t.Errorf("pushes = %d, want the one write that landed", s.Pushes())
	}
}

// TestAContentIdenticalLostAckIsTheWritersOwn: what landed is not the very
// commit object this writer built, but it is its write — the same tree, on the
// same parent, with the same message — after a push whose outcome this writer
// could not know.
func TestAContentIdenticalLostAckIsTheWritersOwn(t *testing.T) {
	o := newOrigin(t)
	s, _ := flakyStore(t, o, testRun)
	path := CheckpointPath(testRun)
	if got, err := s.CreateIfAbsent(path, []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("seed: %v %v", got, err)
	}
	o.lateLanding(t, contentIdentical)
	if got, err := s.UpdateIfSHA(path, []byte(`{"sequence":2}`)); err != nil || got != Updated {
		t.Fatalf("the content-identical lost-ack write came back %q %v", got, err)
	}
}

// contentIdentical is the pushed commit's twin: its tree, its parent and its
// message, committed at another time, so it is a different object.
const contentIdentical = `$(GIT_COMMITTER_DATE='2001-01-01T00:00:00Z' git commit-tree "$new^{tree}" -p "$old" -m "$(git log -1 --format=%B "$new")")`

// TestAContentIdenticalWriteWithNoLostAckStillRefuses is the gate on the rule
// above. With no push whose outcome this writer could not know, there is no
// write of its own that a twin could be — so a twin is somebody else's, and
// for a create-if-absent marker a byte-identical record from another
// reconciler is exactly what the guard exists to refuse.
func TestAContentIdenticalWriteWithNoLostAckStillRefuses(t *testing.T) {
	o := newOrigin(t)
	s, notices := flakyStore(t, o, testRun)
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	o.landingHook(t, contentIdentical, false)
	got, err := s.CreateIfAbsent(AttemptPath(testRun, 1), []byte(`{"attempt":1}`))
	if err != nil || got != ConflictExists {
		t.Fatalf("a twin with no lost ack came back %q %v, want conflict_exists", got, err)
	}
	if len(*notices) != 0 {
		t.Fatalf("the push was retried (%+v): this test needs a writer with no uncertain push", *notices)
	}
}

// TestAForeignChangeAfterALostAckStillRefuses is the other direction, and the
// one that keeps the guard a guard: the first push was uncertain, but what is
// on origin now is somebody ELSE's checkpoint. That is a genuine loss of the
// per-path guard and it still refuses as conflict_stale_sha.
func TestAForeignChangeAfterALostAckStillRefuses(t *testing.T) {
	o := newOrigin(t)
	s, notices := flakyStore(t, o, testRun)
	path := CheckpointPath(testRun)
	if got, err := s.CreateIfAbsent(path, []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("seed: %v %v", got, err)
	}

	// A second reconciler's write of the same path, built on the same head and
	// parked on origin under another name, so the hook can land it.
	other := o.actor("b", testRun)
	if _, err := other.Fetch(); err != nil {
		t.Fatal(err)
	}
	blob, err := other.git.writeBlob([]byte(`{"sequence":2,"by":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.git.commitWithFile(other.Head(), path, blob, other.message("update", path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.git.run("push", "origin", foreign+":refs/heads/parked"); err != nil {
		t.Fatal(err)
	}

	o.lateLanding(t, foreign)
	got, err := s.UpdateIfSHA(path, []byte(`{"sequence":2,"by":"a"}`))
	if err != nil {
		t.Fatalf("the foreign change: %v", err)
	}
	if got != ConflictStaleSHA {
		t.Fatalf("somebody else's checkpoint on origin came back %q: a foreign write was taken as this writer's own", got)
	}
	if len(*notices) == 0 {
		t.Fatal("the push was never retried: the hook did not drop the first acknowledgement")
	}
	if c := o.files()[path]; c["by"] != "b" {
		t.Fatalf("origin's checkpoint is %v, want b's", c)
	}
	if s.Head() == foreign {
		t.Error("the refused writer adopted the foreign head as its own")
	}
}
