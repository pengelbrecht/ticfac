package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Three defects found resuming epic run epic-yoh on 2026-09-23, each pinned by
// the test that reproduced it.
//
// The run had stopped with cr4 rejected (merge_failed onto epic/yoh), lkd, ppt
// and gbs dispatched with their workers still running, and a08 ready, under a
// declared width of four. A person merged cr4's attempt head into epic/yoh by
// hand and resumed. The resumed pass adopted gbs, asked the tracker to claim
// a08 and was refused — "4 implementer(s) already in flight (cr4, gbs, lkd,
// ppt)" — and the automatic resume after that treated a08's never-started
// attempt as a failed try and escalated it a tier. lkd, ppt and cr4 never
// appeared in either pass's feed.

// BUG C. A rejected attempt whose head a person then merged is INTEGRATED, and
// finishing it needs nothing from the executor: the run records it as already
// contained, gates the epic head exactly as it would have gated its own merge,
// and closes the tick.
//
// disposition already knew the head was contained, and answered "adopt" — which
// routed the attempt through adopt(): rebuild the executor, look for the
// attempt's state on THIS host, and Inspect it, or START it when no state is
// found. For an attempt the refusal already tore down that is the wrong
// question to ask anybody. On a host that holds its state it happens to work
// (the teardown keeps attempt.json); on a host that does not — a container, a
// restart elsewhere, a substrate that reaped the torn-down attempt — adopt reads
// "the marker landed and the dispatch did not" and starts the worker AGAIN on
// an attempt whose work is already merged, or refuses it as unaddressable and
// holds the tick. Both leave a hand-merged attempt unclosed.
func TestAMergeFailedAttemptAPersonMergedIsIntegratedNotHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	// The merge_failed rejection, made the way TestAMergeRefusalIsHeldForAPerson
	// makes it: the attempt is collected, the integration branch then gains a
	// conflicting edit of the same file, and the resumed run refuses the merge.
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageCollected)})
	killedAfter(t, err, "a1", StageCollected)
	conflictOnIntegrationBranch(t, f.Repo, "work-a1.txt", "a change nobody merged around\n")
	_, refused, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the refusing run did not finish: %v", err)
	}
	if refused.Failure == nil || refused.Failure.Reason != RefusedMerge {
		t.Fatalf("the run failed as %+v, want %s", refused.Failure, RefusedMerge)
	}

	// A person resolves the conflict and merges the attempt's head into the
	// integration branch by hand, as the operator did with cr4.
	marker := attemptMarker(t, f, "a1", 1)
	head := originHeadOf(t, f, branchOf(marker.WriteRef))
	mergeByHand(t, f.Repo, branchOf(marker.WriteRef), "work-a1.txt", "a person's resolution\n")
	if !containsCommit(t, f, head, "origin/epic/qeu") {
		t.Fatal("the hand merge did not put the attempt's head on the integration branch; this fixture proves nothing")
	}

	// The run resumes on a host that holds no executor state for the attempt —
	// the teardown's leftovers are gone, as they are in a fresh container.
	if err := os.Remove(filepath.Join(attemptStateDir(t, f, marker), "attempt.json")); err != nil {
		t.Fatal(err)
	}
	starts := f.startCount(marker.JobID)

	resumed, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if got := f.startCount(marker.JobID); got != starts {
		t.Errorf("the executor was asked to start %s again (%d starts, was %d): an attempt whose work is "+
			"already merged was re-run", marker.JobID, got, starts)
	}
	if !contains(result.Closed, "a1") {
		t.Fatalf("a1 was not closed (the run ended %s: %+v); its stages are %v",
			result.State, result.Failure, resumed.Stages("a1"))
	}
	integrated := false
	for _, event := range resumed.Journal() {
		if event.Tick == "a1" && event.Stage == StageIntegrated && strings.Contains(event.Detail, "already contained") {
			integrated = true
		}
	}
	if !integrated {
		t.Errorf("no %q %s line for a1: %v", "already contained", StageIntegrated, resumed.Stages("a1"))
	}
	if got := resumed.Stages("a1"); !contains(got, StageGatePassed) {
		t.Errorf("a1 was closed without the per-tick gate running on the epic head: %v", got)
	}
	if got := resumed.Stages("a1"); contains(got, StageDispatched) || contains(got, StageRedispatched) {
		t.Errorf("an integrated attempt was dispatched over: %v", got)
	}
	if result.State != runstate.StateCompleted {
		t.Errorf("the resumed run ended %s: %s", result.State, result.Reason)
	}
}

// mergeByHand is a person merging an attempt's branch into the integration
// branch on origin, resolving a conflict in `file` with `resolution` — what the
// operator did with cr4 before resuming epic-yoh.
func mergeByHand(t *testing.T, repo *testRepo, branch, file, resolution string) {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--branch", "epic/qeu", repo.Origin, dir)
	configure(t, dir)
	mustRun(t, dir, "git", "fetch", "--quiet", "origin", refFor(branch)+":refs/remotes/origin/"+branch)
	if !mustRunAllowingFailure(dir, "git", "merge", "--no-ff", "--no-edit", "origin/"+branch) {
		write(t, filepath.Join(dir, file), resolution)
		mustRun(t, dir, "git", "add", file)
		mustRun(t, dir, "git", "commit", "--quiet", "--no-edit")
	}
	mustRun(t, dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	mustRun(t, repo.Dir, "git", "fetch", "--quiet", "origin", "refs/heads/epic/qeu")
}

// BUG B. A claim the tracker REFUSED is not a failed try: the attempt it
// belonged to never started, so it earns no rung on the tier ladder and it is
// not spent.
//
// claimDispatch writes the dispatch marker to origin BEFORE the claim — the
// order the compare-and-swap needs — so a claim_width refusal leaves a marker
// for an attempt that never ran. The run loop then marked the tick "rejected"
// as it does for every refusal, and on resume disposition read "rejected, left
// nothing" as a spent attempt: redispatched, counted as a failure, and
// escalated a rung. On epic-yoh that sent a08's first real try to frontier.
func TestAClaimRefusalEarnsNoRungAndSpendsNoTry(t *testing.T) {
	t.Parallel()
	gate := strings.Replace(tierGate, "version = 2\n", "version = 2\n\n[orchestration]\nmax_parallel = 2\n", 1)
	f := newFixture(t, fixtureOptions{gate: gate})
	// The width a person or another run is holding: one claim is all this
	// tracker allows, so a2's claim is refused however the window counts.
	f.Tracker.refuseClaimsBeyond(1)

	_, held, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("the run should have held: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedClaimWidth || held.Failure.TickID != "a2" {
		t.Fatalf("the run ended %s with %+v, want a %s refusal of a2", held.State, held.Failure, RefusedClaimWidth)
	}
	if got := attemptsFor(t, f, "a2"); got != 1 {
		t.Fatalf("a2 has %d dispatch markers after the refusal, want the 1 the claim was refused behind", got)
	}

	// The other holder finishes; the resumed run dispatches a2 for real.
	f.Tracker.refuseClaimsBeyond(0)
	resumed, result, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s", result.State, result.Reason)
	}
	if got := resumed.Stages("a2"); contains(got, StageRedispatched) {
		t.Errorf("a2's never-started attempt was treated as a spent try and redispatched: %v", got)
	}
	if detail, ok := journalLine(resumed, "a2", StageTierDerived); ok && strings.Contains(detail, "escalated") {
		t.Errorf("a2 escalated on a claim refusal: %q", detail)
	}
	for try := 1; try <= attemptsFor(t, f, "a2"); try++ {
		if got := markerTierOfTry(t, resumed, "a2", try); got != "balanced" {
			t.Errorf("a2 try %d recorded tier %q, want balanced: a refused claim is not a failed attempt", try, got)
		}
	}
	if got := attemptsFor(t, f, "a2"); got != 1 {
		t.Errorf("a2 has %d dispatch markers, want 1: the attempt the claim was refused behind never started, "+
			"so it is the one that runs", got)
	}
}

// BUG A. A resumed pass adopts every attempt it already has in flight BEFORE it
// claims anything new, and counts every claim the tracker holds under the epic
// against the width.
//
// The window counted only what it was holding, and it took ticks on in PLAN
// order — the tracker's layering, priority first — adopting an in-flight
// attempt only when the queue happened to reach its tick. On epic-yoh the plan
// read gbs, a08, cr4, lkd, ppt: gbs was adopted (one claim, by the window's
// count), a08 was admitted into what the window thought were three free slots,
// and the tracker refused the claim because lkd, ppt and cr4 held the other
// three. lkd and ppt were live attempts of this very run; the pass never got
// to them.
//
// The fixture is that shape at width two: a1 and a2 are dispatched and the run
// stops, then the tracker re-layers so a fresh tick b1 sits BETWEEN them. The
// resumed run must adopt a2 before it asks for b1's claim.
func TestAResumeAdoptsEveryInFlightAttemptBeforeClaimingNewWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	f.Tracker.refuseClaimsBeyond(2)

	_, _, err := f.run(f.Repo, fixtureOptions{gate: wideGate, stopAfter: stopAt("a2", StageDispatched)})
	killedAfter(t, err, "a2", StageDispatched)

	// The graph moves under the stopped run: b1 is layered into the first wave,
	// ahead of a2 in the tracker's own order, the way a08 sat ahead of lkd.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Waves = [][]string{{"a1", "b1", "a2"}, {"rv", "co"}}
	state.Order = []string{"a1", "b1", "a2", "rv", "co"}
	f.Tracker.write(t, state)

	resumed, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.Failure != nil && result.Failure.Reason == RefusedClaimWidth {
		t.Fatalf("the resumed run asked the tracker for a claim past the width while a live attempt of its own "+
			"was still unadopted: %s", result.Failure.Message)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %+v", result.State, result.Failure)
	}
	if peak := f.Tracker.peakClaims(); peak > 2 {
		t.Errorf("%d claims were open at once under a width of 2", peak)
	}

	// The order the feed tells: both live attempts adopted before b1 is claimed.
	var adoptedA2, claimedB1 int
	for i, event := range resumed.Journal() {
		switch {
		case event.Tick == "a2" && event.Stage == StageAdopted && adoptedA2 == 0:
			adoptedA2 = i + 1
		case event.Tick == "b1" && event.Stage == StageClaimed && claimedB1 == 0:
			claimedB1 = i + 1
		}
	}
	if adoptedA2 == 0 || claimedB1 == 0 {
		t.Fatalf("the feed is missing a line: a2 adopted at %d, b1 claimed at %d", adoptedA2, claimedB1)
	}
	if claimedB1 < adoptedA2 {
		t.Errorf("b1 was claimed (line %d) before a2's live attempt was adopted (line %d)", claimedB1, adoptedA2)
	}
}

// BUG A's other half: a claim the window is NOT holding still counts against the
// width, because tk counts it. On epic-yoh cr4 was one — rejected, still
// claimed — and the window's count left it out. Here b1 is claimed by a person
// before the run starts; with a width of two the run must never ask for a
// third claim, and it must not stop on a refusal it could have predicted.
func TestAClaimTheWindowIsNotHoldingStillCountsAgainstTheWidth(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	if _, err := f.Tracker.Claim(context.Background(), "b1", "a person"); err != nil {
		t.Fatal(err)
	}
	f.Tracker.refuseClaimsBeyond(2)

	_, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure != nil && result.Failure.Reason == RefusedClaimWidth {
		t.Fatalf("the run asked for a claim the width forbids, counting only what its window held: %s",
			result.Failure.Message)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %+v", result.State, result.Failure)
	}
	if peak := f.Tracker.peakClaims(); peak > 2 {
		t.Errorf("%d claims were open at once under a width of 2", peak)
	}
}
