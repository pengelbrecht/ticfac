package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Restart: the reconciler is killed at three points a crash could genuinely
// land on, and the next incarnation starts from a FRESH CLONE holding nothing
// but what is on origin.
//
// The three cuts are the three windows the design is about:
//
//	after the dispatch      — the marker is on origin and a job is running.
//	                          The restart must ADOPT it, never redispatch.
//	after the collection    — the work is collected and nothing is closed.
//	                          The restart must not close twice, and must not
//	                          close without re-establishing the gate.
//	after the gate, before  — the evidence is durable and the tracker still
//	the close                 says open. The restart must close exactly once,
//	                          and must never have closed at the moment of the
//	                          cut: a close recorded before the tracker has it
//	                          is a false close.
func TestARestartFromAFreshCloneNeitherRedispatchesNorFalselyCloses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		stage string
	}{
		{"after the dispatch", StageDispatched},
		{"after the collection and before the close", StageCollected},
		{"after the gate and before the close", StageGatePassed},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, fixtureOptions{})

			// The cut. Nothing after it runs: no deferred cleanup, no close.
			killed := fixtureOptions{stopAfter: stopAt("a1", testCase.stage)}
			_, _, err := f.run(f.Repo, killed)
			killedAfter(t, err, "a1", testCase.stage)

			// The tracker is the authority on closure, and at the cut it must
			// not say a1 is closed — a stage recorded before the tracker has
			// it would be a false close.
			current, err := f.Tracker.Show(context.Background(), "a1")
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == "closed" {
				t.Fatalf("a1 was closed before the run reached its close (cut after %s)", testCase.stage)
			}

			// A fresh clone: everything the next incarnation knows, it reads
			// from origin.
			clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
			restarted, result, err := f.run(clone, fixtureOptions{})
			if err != nil {
				t.Fatalf("the restart did not finish: %v", err)
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
			}

			// No duplicate attempt: a1 was dispatched once, by the incarnation
			// that died, and the restart adopted it.
			store, err := runstate.Open(runstate.Options{
				Repo: clone.Dir, Remote: "origin", Branch: restarted.IntegrationBranch(), RunID: restarted.RunID(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Fetch(); err != nil {
				t.Fatal(err)
			}
			attempts, err := store.Attempts()
			if err != nil {
				t.Fatal(err)
			}
			forA1 := 0
			for _, attempt := range attempts {
				if attempt.TickID == "a1" {
					forA1++
				}
			}
			if forA1 != 1 {
				t.Errorf("%d dispatch markers for a1; the restart dispatched it again", forA1)
			}
			if got := restarted.Stages("a1"); contains(got, StageDispatched) {
				t.Errorf("the restart dispatched a1 again: %v", got)
			}
			if got := restarted.Stages("a1"); !contains(got, StageAdopted) && !contains(got, StageSkipped) {
				t.Errorf("the restart neither adopted nor skipped a1: %v", got)
			}

			// No false close, and no double close: exactly one close reached
			// the tracker across both incarnations.
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
		})
	}
}

// The gate is paid for once. A restart that already has the evidence re-reads
// it rather than running the check again — create-if-absent is what makes that
// true, and a record that could be overwritten would not be evidence.
//
// EvidenceKeys alone cannot prove this: it is a set, so a duplicate key can
// never show up in it twice regardless of whether the guard actually held.
// The gate command here appends to a counter file OUTSIDE any git worktree —
// so it survives across the reconciler's disposable temp worktrees — and the
// restart is only proven not to have re-run the check if that file still says
// one run.
func TestARestartReusesTheGateEvidenceItAlreadyPaidFor(t *testing.T) {
	t.Parallel()
	counter := filepath.Join(t.TempDir(), "gate-runs.count")
	command := fmt.Sprintf("printf x >> %s && test -f README.md && ls work-*.txt >/dev/null", counter)
	gate := fmt.Sprintf(`version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = %q, description = "counts every time it actually ran" }
`, command)

	f := newFixture(t, fixtureOptions{gate: gate})
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageGatePassed)})
	killedAfter(t, err, "a1", StageGatePassed)

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
	}

	store, err := runstate.Open(runstate.Options{
		Repo: clone.Dir, Remote: "origin", Branch: restarted.IntegrationBranch(), RunID: restarted.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	key := evidenceKey("a1", 1, "tree")
	evidence, ok, err := store.Evidence(key)
	if err != nil || !ok {
		t.Fatalf("the gate's evidence for a1 is not on origin: %v", err)
	}
	if evidence.Result != "pass" {
		t.Errorf("evidence %s is %s", key, evidence.Result)
	}
	// One record, and only one: the restart did not mint a second.
	keys := store.EvidenceKeys()
	seen := 0
	for _, k := range keys {
		if k == key {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("evidence keys %v carry %s %d times", keys, key, seen)
	}

	// Every tick in the wave runs the same "tree" check once, so the check's
	// physical execution count — the counter file, appended to from OUTSIDE
	// any git worktree so it survives the reconciler's disposable ones —
	// must equal the number of "-tree" evidence records the whole run (both
	// incarnations) landed on origin. If create-if-absent's guard were
	// skipped on the restart, a1's check would run a second time and this
	// would be one command execution too many for the same one evidence key.
	gateRuns := 0
	for _, k := range keys {
		if strings.HasSuffix(k, "-tree") {
			gateRuns++
		}
	}
	ran, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("the gate check's counter file was never written: %v", err)
	}
	if len(ran) != gateRuns {
		t.Errorf("the gate check physically ran %d time(s) for %d recorded gate checks; "+
			"the restart re-ran a check whose evidence it already had", len(ran), gateRuns)
	}
}

// The close is never backed by evidence from a gate that is no longer the
// declared one — on the resume path too (tick 0dc).
//
// The incident this pins: a run gated a tick under one declared command and
// was correctly refused as stale when the operator changed .tick/runners.toml
// mid-run; the RESUME — an incarnation that adopted the already-merged
// attempt and re-ran nothing — then re-read the identical record, published
// its verdict and closed the tick forty seconds later. The reuse judgement in
// gateEvidenceKey compared only the source tree, so on that path the digest
// was decorative: the freshness index registered the fingerprint the run
// WOULD have used, and the publication check compared that against a target
// built the same way. Both paths must ask the same question — and here the
// resumed run either re-runs the check under the gate declared now, or does
// not close the tick. The command counts its own executions into a file
// outside any worktree, so a close on re-used evidence is a count, not an
// impression.
func TestAResumedRunDoesNotCloseATickOnGateEvidenceFromAChangedCommand(t *testing.T) {
	t.Parallel()
	counter := filepath.Join(t.TempDir(), "gate-runs.count")
	counting := fmt.Sprintf("printf x >> %s && test -f README.md && ls work-*.txt >/dev/null", counter)
	gate := fmt.Sprintf(`version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = %q, description = "counts every time it actually ran" }
`, counting)

	f := newFixture(t, fixtureOptions{gate: gate})
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageGatePassed)})
	killedAfter(t, err, "a1", StageGatePassed)

	// What the dead incarnation left: a passing record under the plain key,
	// carrying the digest of the gate that ran it.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	key := evidenceKey("a1", 1, "tree")
	first, ok, err := store.Evidence(key)
	if err != nil || !ok {
		t.Fatalf("the dead incarnation left no gate evidence for a1 under %s: %v", key, err)
	}
	if first.Provenance.ContextManifestDigest == nil {
		t.Fatalf("evidence %s states no context_manifest_digest", key)
	}
	if first.Result != "pass" {
		t.Fatalf("evidence %s is %s; this fixture proves nothing about reuse", key, first.Result)
	}

	// The operator changes the declared gate mid-run — the incident's edit —
	// committed to the branch a resumed checkout reads, so the fresh clone
	// below starts from the changed declaration.
	changed := strings.Replace(gate, counting, counting+" && true", 1)
	write(t, filepath.Join(f.Repo.Dir, ".tick", "runners.toml"), changed)
	mustRun(t, f.Repo.Dir, "git", "add", ".tick/runners.toml")
	mustRun(t, f.Repo.Dir, "git", "commit", "--quiet", "-m", "the declared gate changes mid-run", "--", ".tick/runners.toml")
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "main")

	declared, err := ReadGateCommands(filepath.Join(f.Repo.Dir, ".tick", "runners.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if declared.Digest() == *first.Provenance.ContextManifestDigest {
		t.Fatal("the changed gate and the recorded evidence share a digest; this proves nothing about staleness")
	}

	// The resume: a fresh clone, everything it knows read from origin.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s", result.State, result.Reason)
	}
	if !contains(result.Closed, "a1") {
		t.Fatalf("the resumed run did not close a1: %v", result.Closed)
	}

	store = openRunStore(t, clone.Dir, restarted.IntegrationBranch(), restarted.RunID())

	// The record from the gate that ran still stands, unoverwritten: evidence
	// is never rewritten, and the refusal here is about PUBLISHING it.
	original, ok, err := store.Evidence(key)
	if err != nil || !ok {
		t.Fatalf("the original evidence under %s is gone: %v", key, err)
	}
	if original.StartedAt != first.StartedAt || original.FinishedAt != first.FinishedAt {
		t.Errorf("the record from the gate that ran was overwritten: %s -> %s", first.StartedAt, original.StartedAt)
	}

	// The close is not backed by it. Some record of a1's check in THIS run's
	// evidence carries the digest of the gate declared now — the resumed run
	// re-ran the check under the changed command — and at the incident no such
	// record existed: the tick closed on the old record alone.
	var backed *runstate.Evidence
	for _, k := range store.EvidenceKeys() {
		if !strings.HasPrefix(k, key) {
			continue
		}
		candidate, ok, err := store.Evidence(k)
		if err != nil || !ok {
			t.Fatalf("read evidence %s: %v", k, err)
		}
		if candidate.Provenance.ContextManifestDigest == nil {
			continue
		}
		if *candidate.Provenance.ContextManifestDigest == declared.Digest() {
			backed = candidate
		}
	}
	if backed == nil {
		t.Fatalf("a1 closed with no evidence carrying the digest of the gate declared now (%s): "+
			"the identical record from the gate that ran (%s) was published instead",
			short(declared.Digest()), short(*first.Provenance.ContextManifestDigest))
	}

	// And the backing evidence really is about the changed command, produced
	// after the dead incarnation's record — not the old record re-labelled.
	want := []string{"sh", "-c", counting + " && true"}
	if !reflect.DeepEqual(backed.Check.Command, want) {
		t.Errorf("the close is backed by a check that ran %q, want the changed command %q",
			strings.Join(backed.Check.Command, " "), strings.Join(want, " "))
	}
	late, err := time.Parse(time.RFC3339, backed.StartedAt)
	if err != nil {
		t.Fatalf("the backing evidence's started_at is not a timestamp: %v", err)
	}
	dead, err := time.Parse(time.RFC3339, first.FinishedAt)
	if err != nil {
		t.Fatalf("the dead incarnation's finished_at is not a timestamp: %v", err)
	}
	if !late.After(dead) {
		t.Errorf("the evidence backing the close started at %s, before the dead incarnation's gate had "+
			"even finished at %s: the old record was published", backed.StartedAt, first.FinishedAt)
	}

	// The tick closed exactly once, behind a gate that ran.
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Errorf("a1 was closed %d times across the two incarnations", got)
	}
}

// The close-to-checkpoint window (tick 48q): a run publishes a tick's close
// through the tracker and only afterwards writes the checkpoint row that says
// the tick is closed. A process that dies between the two leaves the two
// authorities disagreeing — the tracker says closed, the checkpoint row still
// says integrated — and the resumed run's plan DROPS the tick (planFrom skips
// whatever the tracker has closed), so the "already closed" settlement in
// processTick never runs for it and nothing ever writes the row: a tick stuck
// at integrated is a tick a future resume may try to finish again.
//
// The cut is exactly in the window: the killed incarnation is stopped after
// the StageClosed record — which the tracker's published close precedes and
// the row-writing checkpoint follows — so the tracker already says closed and
// the last durable checkpoint is the one at the head of closeTick, whose row
// reads integrated.
func TestARestartSettlesTheCheckpointRowOfACloseTheDeadIncarnationPublished(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageClosed)})
	killedAfter(t, err, "a1", StageClosed)

	// The tracker is the authority on closure, and at the cut it already says
	// closed: the close is published, so the window is entered.
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Fatalf("the cut is not in the window: a1 is %s in the tracker, so the close was never published",
			current.Status)
	}

	// And the checkpoint row does not know: the two authorities disagree at
	// the cut, which is what makes this a window a crash can land in rather
	// than a defect in only one of them.
	if row := tickRowOf(t, openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture"), "a1"); row != "integrated" {
		t.Fatalf("the cut is not in the window: the checkpoint row for a1 reads %q, want integrated",
			row)
	}

	// A fresh clone: everything the next incarnation knows, it reads from
	// origin.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatalf("the restart did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
	}

	// The row is settled from the tracker's own answer — the same compare-
	// and-swap processTick runs for a tick the plan still carries, applied to
	// the tick the plan dropped because it was already closed.
	store := openRunStore(t, clone.Dir, restarted.IntegrationBranch(), restarted.RunID())
	if row := tickRowOf(t, store, "a1"); row != "closed" {
		t.Errorf("the checkpoint row for a1 reads %q after the restart; a tick the tracker closed must "+
			"read closed in the checkpoint a future resume decides from", row)
	}
	if !contains(result.Closed, "a1") {
		t.Errorf("the restart's result does not count a1 closed: %v", result.Closed)
	}

	// The settlement is idempotent: nothing was closed twice, and the restart
	// never touched the dead incarnation's attempt.
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Errorf("a1 was closed %d times across the two incarnations", got)
	}
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	forA1 := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			forA1++
		}
	}
	if forA1 != 1 {
		t.Errorf("%d dispatch markers for a1; the restart dispatched the closed tick again", forA1)
	}
}

// tickRowOf is one tick's state as the checkpoint on origin has it.
func tickRowOf(t *testing.T, store *runstate.Store, tick string) string {
	t.Helper()
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint on origin: %v", err)
	}
	for _, ts := range checkpoint.Ticks {
		if ts.TickID == tick {
			return ts.State
		}
	}
	return ""
}
