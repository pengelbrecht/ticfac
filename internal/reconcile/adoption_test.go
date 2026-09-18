package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Adoption picks the LATEST attempt of a tick, never the first one in stored
// order (tick w1c).
//
// Appendix A #6 says an attempt under this identity has already been
// dispatched, so it is ADOPTED — it does not say the OLDEST is the one. When
// several attempts of one tick survive, the adoptable one is the LATEST: a
// later attempt only exists because the run rejected the earlier one, so the
// earlier ones are spent by definition. A resume that walked the attempts in
// stored order acted on the FIRST one whose disposition was neither redispatch
// nor hold — and a disposition is a function of durable state that changes
// between incarnations, so an attempt the run had already skipped could
// resurface as adoptable and steal the resume from the newer attempt whose
// answer was the better one.
//
// The second rule the fix must keep: a HELD attempt stops the run wherever it
// sits in the order, so the pass that looks for the newest adoptable attempt
// must not return at the first adoptable one it sees — a held attempt below it
// would be skipped in favour of a newer adoptable one exactly as wrongly as
// the old loop skipped a newer attempt for an older.

// archivedReport is one attempt's report as it survives teardown: report.md
// beside the attempt record (tick 35h), the file priorReports hands the next
// worker. It is how a test says which answer an attempt really carried.
func archivedReport(t *testing.T, f *fixture, marker attemptHandle) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(attemptStateDir(t, f, marker), subprocess.FileReportArchive))
	if err != nil {
		t.Fatalf("read the archived report of attempt %d of %s: %v", marker.Attempt, marker.TickID, err)
	}
	return string(raw)
}

// a1AttemptCount is how many dispatch markers the run's state carries for one
// tick, read from origin the way a restart reads it.
func a1AttemptCount(t *testing.T, f *fixture, tick string) int {
	t.Helper()
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, attempt := range attempts {
		if attempt.TickID == tick {
			count++
		}
	}
	return count
}

// Two surviving attempts of one tick, both adoptable, carrying different
// reports: the resume adopts the NEWER one and closes the tick on its answer,
// never on the older attempt's.
func TestAdoptionTakesTheLatestAttemptOfATickNotTheFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "blocked-first"})

	// Incarnation one: a1's attempt 1 answers BLOCKED and commits nothing. The
	// run rejects it — settled, nothing on any ref — and is cut the moment
	// the rejection is durable.
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "blocked-first", stopAfter: stopAt("a1", StageRejected)})
	killedAfter(t, err, "a1", StageRejected)

	// Incarnation two: the spent attempt is redispatched and a NEW attempt of
	// a1 commits work and answers DONE. The run is cut the moment that
	// attempt SETTLES — after its commits and its report reached origin,
	// before anything collects them.
	_, _, err = f.run(f.Repo, fixtureOptions{mode: "blocked-first", stopAfter: stopAt("a1", StageWaiting)})
	killedAfter(t, err, "a1", StageWaiting)

	// Both attempts are ADOPTABLE on this resume: attempt 2's own dispatch
	// overwrote the checkpoint's tick state, so attempt 1's rejection no
	// longer reads as rejected and neither disposition is redispatch. Whichever
	// the run adopts, it closes the tick on that attempt's answer.
	f.Runner = fakeRunnerArgv(t, "report")
	resumed, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resume did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resume ended %s: %s", result.State, result.Reason)
	}
	if !contains(result.Closed, "a1") {
		t.Fatalf("a1 was not closed; its stages are %v", resumed.Stages("a1"))
	}

	// The adoption took the NEWER attempt. The feed line carries the attempt
	// it is about, and the close must stand on attempt 2's DONE — never on
	// attempt 1's BLOCKED, which the run had already rejected.
	events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
	if err != nil {
		t.Fatalf("read the run's feed: %v", err)
	}
	adopted := false
	for _, event := range events {
		if event.Stage != StageAdopted || event.TickID == nil || *event.TickID != "a1" {
			continue
		}
		adopted = true
		if event.Attempt == nil || *event.Attempt != 2 {
			t.Errorf("the adopted attempt of a1 is %v, want 2: a resume adopts the LATEST attempt of a tick, "+
				"never the first one in stored order", event.Attempt)
		}
	}
	if !adopted {
		t.Fatalf("no %s line for a1 on the feed; its stages are %v", StageAdopted, resumed.Stages("a1"))
	}

	// The premise the acceptance names, proven on the two answers as they
	// were archived: the attempts carried DIFFERENT reports — the older
	// answered BLOCKED, the newer DONE — and the close stood on the newer.
	older := archivedReport(t, f, attemptMarker(t, f, "a1", 1))
	newer := archivedReport(t, f, attemptMarker(t, f, "a1", 2))
	if !strings.Contains(older, "BLOCKED") || !strings.Contains(newer, "DONE") {
		t.Fatalf("the two attempts of a1 do not carry different reports:\n--- attempt 1 ---\n%s\n--- attempt 2 ---\n%s",
			older, newer)
	}

	// And the tick was closed on the newer attempt's WORK: attempt 2's head
	// reached the integration branch, which is the answer its report carried.
	newest := attemptMarker(t, f, "a1", 2)
	if !containsCommit(t, f, originHeadOf(t, f, branchOf(newest.WriteRef)), "origin/epic/qeu") {
		t.Errorf("the integration branch does not carry the newer attempt's work, so the close was not on its answer")
	}

	// Nothing new was dispatched for a1: the two attempts the tick had are the
	// two it still has, and the resume adopted rather than paid again.
	if got := resumed.Stages("a1"); contains(got, StageDispatched) {
		t.Errorf("the resume dispatched a third attempt of a1 over two surviving ones: %v", got)
	}
	if count := a1AttemptCount(t, f, "a1"); count != 2 {
		t.Errorf("a1 has %d dispatch markers after the resume, want 2", count)
	}
}

// A held attempt stops the run wherever it sits in the order: the pass that
// looks for the newest adoptable attempt may not skip an older attempt whose
// commits are still there — the work is the only copy of what a person has to
// look at, and adopting past it would close the tick without ever saying so.
func TestAHeldAttemptStopsTheRunEvenWhenANewerAttemptIsAdoptable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "blocked-first", gate: failingGate})

	// Attempt 1 answers BLOCKED and commits nothing: rejected with nothing
	// anywhere, the run fails, and the attempt's disposition on a resume is
	// redispatch — which is what lets a second attempt be dispatched at all.
	_, first, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the first run did not finish: %v", err)
	}
	if first.State != runstate.StateFailed || first.Failure == nil || first.Failure.Reason != RefusedCollect {
		t.Fatalf("the first run ended %s (%+v), want %s", first.State, first.Failure, RefusedCollect)
	}

	// Attempt 2 commits, answers DONE and merges onto the integration branch —
	// and the gate refuses it, so the tick is rejected with attempt 2's work
	// integrated: exactly the disposition that makes it adoptable later.
	_, second, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the second run did not finish: %v", err)
	}
	if second.State != runstate.StateFailed || second.Failure == nil || second.Failure.Reason != RefusedGate {
		t.Fatalf("the second run ended %s (%+v), want %s", second.State, second.Failure, RefusedGate)
	}

	// The premise's other half, proven rather than assumed: the NEWER attempt
	// really is adoptable — its work is on the integration branch — while the
	// tick's state is rejected.
	newest := attemptMarker(t, f, "a1", 2)
	if !containsCommit(t, f, originHeadOf(t, f, branchOf(newest.WriteRef)), "origin/epic/qeu") {
		t.Fatal("attempt 2's work is not on the integration branch; the newer attempt is not adoptable")
	}

	// The state a pass could skip: attempt 1's work arrives LATE — a push that
	// landed after the run had already moved past the attempt, the shape of a
	// worktree somebody resumed finishing its push, or a person putting work
	// back. Its disposition changes from the redispatch it was when attempt 2
	// was dispatched to hold: rejected, commits on its write ref, nothing
	// merged.
	spent := attemptMarker(t, f, "a1", 1)
	lateWorkOnto(t, f, spent, "late-a1.txt", "work that outlived its rejection\n")

	// The resume: the newest attempt is adoptable, but the run STOPS on the
	// held one rather than adopting past it — the same single pass evaluates
	// every disposition, and a hold is a stop wherever it sits.
	heldRun, held, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resume did not finish: %v", err)
	}
	if held.State != runstate.StateFailed || held.Failure == nil || held.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the resume ended %s (%+v), want %s", held.State, held.Failure, RefusedRejectedWork)
	}
	if !strings.Contains(held.Failure.Message, "attempt 1 of a1") {
		t.Errorf("the refusal does not name the held attempt: %s", held.Failure.Message)
	}

	// Nothing was adopted, collected or closed: the newer adoptable attempt
	// did not carry the run past the hold.
	if got := heldRun.Stages("a1"); contains(got, StageAdopted) || contains(got, StageCollected) || contains(got, StageClosed) {
		t.Errorf("the resume moved past a held attempt: %v", got)
	}
	if contains(held.Closed, "a1") {
		t.Errorf("a1 was closed behind a hold: %+v", held.Closed)
	}
	if count := a1AttemptCount(t, f, "a1"); count != 2 {
		t.Errorf("a1 has %d dispatch markers after the resume, want 2", count)
	}
}

// lateWorkOnto puts one commit beyond an attempt's base onto its write ref on
// origin: the shape of work that lands there AFTER the run has already moved
// past the attempt, which is the only way an attempt's durable disposition
// changes between incarnations. It exists so a test can hand the reconciler
// the two-attempt state the adoption rule is about without inventing a third
// incarnation to produce it.
func lateWorkOnto(t *testing.T, f *fixture, marker attemptHandle, file, content string) {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--no-checkout", f.Repo.Origin, dir)
	configure(t, dir)
	mustRun(t, dir, "git", "checkout", "--quiet", "--detach", marker.BaseSHA)
	writeAndCommit(t, dir, file, content, "work that outlived its rejection")
	mustRun(t, dir, "git", "push", "--quiet", f.Repo.Origin, "HEAD:"+marker.WriteRef)
}
