package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Tick f61: one push per step, not one per record.

// originPush is one push origin accepted: per ref, the subjects of the commits
// it brought, oldest first.
type originPush struct {
	refs map[string][]string
}

func (p originPush) subjects(ref string) []string { return p.refs[ref] }

// recordPushes arms the fixture origin's post-receive hook to log every push
// it accepts — the commits each one brought, per ref — and answers a reader of
// the log.
func recordPushes(t *testing.T, f *fixture) func() []originPush {
	t.Helper()
	log := filepath.Join(f.Root, "origin-pushes.log")
	hook := "#!/bin/sh\n" +
		"echo PUSH >> '" + log + "'\n" +
		"while read old new ref; do\n" +
		"  case \"$new\" in 0000000000000000000000000000000000000000) continue ;; esac\n" +
		"  case \"$old\" in\n" +
		"    0000000000000000000000000000000000000000) git log -1 --format=\"$ref	%s\" \"$new\" >> '" + log + "' ;;\n" +
		"    *) git log --reverse --format=\"$ref	%s\" \"$old..$new\" >> '" + log + "' ;;\n" +
		"  esac\n" +
		"done\n"
	if err := os.WriteFile(filepath.Join(f.Repo.Origin, "hooks", "post-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() []originPush {
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("origin logged no push: %v", err)
		}
		var pushes []originPush
		for _, line := range strings.Split(string(raw), "\n") {
			switch {
			case line == "PUSH":
				pushes = append(pushes, originPush{refs: map[string][]string{}})
			case line == "" || len(pushes) == 0:
			default:
				ref, subject, _ := strings.Cut(line, "\t")
				last := pushes[len(pushes)-1]
				last.refs[ref] = append(last.refs[ref], subject)
			}
		}
		return pushes
	}
}

const fixtureEpicRef = "refs/heads/epic/qeu"

// pushOf is the index of the first push to the epic branch that carried a
// commit whose subject ends with suffix, or -1.
func pushOf(pushes []originPush, suffix string) int {
	for i, p := range pushes {
		for _, subject := range p.subjects(fixtureEpicRef) {
			if strings.HasSuffix(subject, suffix) {
				return i
			}
		}
	}
	return -1
}

func TestAFixtureEpicLandsEachStepInOnePush(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	pushes := recordPushes(t, f)

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	log := pushes()

	epicPushes, epicCommits := 0, 0
	for _, p := range log {
		if n := len(p.subjects(fixtureEpicRef)); n > 0 {
			epicPushes++
			epicCommits += n
		}
	}
	t.Logf("the fixture epic's records: %d commits in %d pushes to the integration branch (%d pushes in all)",
		epicCommits, epicPushes, len(log))

	checkpoint := "update " + runstate.CheckpointPath("r-fixture")
	for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
		// The close is one step: the publishing checkpoint, the note, the
		// close and the closed checkpoint, in that order, in one push.
		at := pushOf(log, ": close "+tick)
		if at < 0 {
			t.Errorf("no push carried the close of %s", tick)
			continue
		}
		got := log[at].subjects(fixtureEpicRef)
		want := []string{checkpoint, "note " + tick, "close " + tick, checkpoint}
		if !endsWithInOrder(got, want) {
			t.Errorf("the close of %s took more than one push: the push that carried it is %q, want it to end "+
				"with %q", tick, got, want)
		}

		// The claim lands before the work it claims starts: before the
		// worker's own branch exists on origin.
		claim := pushOf(log, ": claim "+tick+" for ticfac-test")
		if claim < 0 {
			t.Errorf("no push carried the claim of %s", tick)
			continue
		}
		for i, p := range log {
			for ref := range p.refs {
				if strings.Contains(ref, "/tick-"+tick+"/") && i < claim {
					t.Errorf("%s's worker pushed %s (push %d) before its claim landed (push %d)", tick, ref, i, claim)
				}
			}
		}
	}

	// The dispatching checkpoint rides with the dispatch marker.
	for _, attempt := range []string{"1", "2", "3", "4", "5"} {
		at := pushOf(log, "create "+runstate.AttemptPath("r-fixture", mustAtoi(t, attempt)))
		if at < 0 {
			t.Errorf("no push carried attempt %s's marker", attempt)
			continue
		}
		got := log[at].subjects(fixtureEpicRef)
		if len(got) != 2 || !strings.HasSuffix(got[0], checkpoint) {
			t.Errorf("attempt %s's marker landed as %q; the dispatching checkpoint must ride with it", attempt, got)
		}
	}

	// Measured on this fixture before tick f61: 72 pushes in all, 66 of them
	// to the integration branch, one per record. The bound is the measured
	// after, so a change that quietly goes back to a push per record fails
	// here rather than in a forge's rate limit.
	if epicPushes > 46 {
		t.Errorf("%d pushes to the integration branch; one push per step measured 46 or fewer", epicPushes)
	}
}

// endsWithInOrder reports whether every subject in got ends with the matching
// suffix in want, aligned at the end.
func endsWithInOrder(got, want []string) bool {
	if len(got) < len(want) {
		return false
	}
	tail := got[len(got)-len(want):]
	for i := range want {
		if !strings.HasSuffix(tail[i], want[i]) {
			return false
		}
	}
	return true
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A run killed in the middle of a held step leaves origin where the step
// began — none of the step, never part of it — and the resumed run reads that
// state and finishes the close. The cut is after the close's feed line and
// before the step's push: the publishing checkpoint, the note, the close and
// the closed checkpoint are all written and none is sent.
func TestARunKilledMidStepResumesFromTheStateBeforeTheStep(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	pushes := recordPushes(t, f)

	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageClosed)})
	killedAfter(t, err, "a1", StageClosed)

	for _, suffix := range []string{": note a1", ": close a1"} {
		if at := pushOf(pushes(), suffix); at >= 0 {
			t.Errorf("the killed step's %q reached origin (push %d): a held step lands whole or not at all",
				strings.TrimPrefix(suffix, ": "), at)
		}
	}
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint on origin after the kill: %v", err)
	}
	if strings.Contains(checkpoint.Reason, "closing a1") || strings.Contains(checkpoint.Reason, "a1 is closed") {
		t.Errorf("origin's checkpoint is the killed step's own (%q): part of the step landed", checkpoint.Reason)
	}

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatalf("the restart did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
	}
	if row := tickRowOf(t, openRunStore(t, clone.Dir, restarted.IntegrationBranch(), restarted.RunID()), "a1"); row != "closed" {
		t.Errorf("after the restart the checkpoint row for a1 reads %q, want closed", row)
	}
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Errorf("a1 was closed %d times across the two incarnations", got)
	}
	for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("after the restart %s is %s", tick, current.Status)
		}
	}
}
