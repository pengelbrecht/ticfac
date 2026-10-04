package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The drafts every ENDED earlier run of this epic left untriaged (tick d23).
//
// A run that dies before its close-out leaves its findings PROPOSED under a
// run id nothing will ever decide them: the close-out's findings gate reads
// only the run's own store, and a run resumed under a NEW id never gated on
// the dead run's drafts — they could silently never gate anything, while a
// person's decision about them stood invisible to needs-you. The claim
// takeover already adopts the drafts of a holder whose claim this run takes
// (takeover.go); the drafts of a run that holds no claim this run takes over
// — it died between dispatches, or after its ticks closed — had no path in.
//
// The decision this file pins is ADOPT, at the run's start: every ended
// sibling run's untriaged proposals become this run's own drafts before
// anything is planned, so the run's own rules decide them
// (decideUndecidedFindings) and its close-out's gate holds for a person what
// the rules cannot — the same funnel every finding of its own rides. Gating
// on the foreign drafts instead would hold a person over what the run's own
// rules already answer, the step backward the absorption channel exists not
// to take.

// seedSibling writes one earlier run's records onto the integration branch
// the way a run that ended leaves them: its checkpoint, and the findings its
// attempts drafted.
func seedSibling(t *testing.T, repo *testRepo, runID, epicID string, state runstate.State, findings ...runstate.Finding) {
	t.Helper()
	seed := openRunStore(t, repo.Dir, "epic/qeu", runID)
	if _, err := seed.PutCheckpoint(runstate.Checkpoint{
		RunID:  runID,
		EpicID: epicID,
		State:  state,
		Reason: "seeded: the run stopped at " + string(state),
		Provenance: runstate.Provenance{
			RunID: runID, SourceRef: "refs/heads/epic/qeu",
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a", Phase: runstate.PhaseWorker,
		},
	}); err != nil {
		t.Fatalf("seed run %s's checkpoint: %v", runID, err)
	}
	for _, finding := range findings {
		if _, err := seed.PutFinding(finding); err != nil {
			t.Fatalf("seed run %s's finding %s: %v", runID, finding.Key, err)
		}
	}
}

// seededFinding is one sibling run's untriaged draft, as its own collect
// would have filed it.
func seededFinding(key, runID string) runstate.Finding {
	return runstate.Finding{
		Key:            key,
		Source:         findingSource,
		DiscoveredFrom: "run-" + runID + "/tick-a1/attempt-1",
		Kind:           "defect",
		Title:          "A finding the dead run left untriaged",
		Body:           "Discovered beside the work, reported mechanically.",
		Severity:       "high",
		TickID:         "a1",
		Attempt:        1,
		Status:         runstate.FindingProposed,
		ProposedAt:     "2026-09-20T00:00:00Z",
		Provenance: runstate.Provenance{
			RunID: runID, SourceRef: "refs/heads/epic/qeu",
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a", Phase: runstate.PhaseWorker,
		},
	}
}

// TestARunUnderANewIdAdoptsADeadRunsDraftsAndGatesItsCloseOutOnThem: the
// acceptance of the adopt decision, end to end. A dead sibling run's draft —
// one no claim takeover would reach, because the sibling holds no claim —
// is taken into this run's own store at its start, with its discovery kept,
// and the close-out's findings gate then holds the hand-over over it: the
// inherited draft gates exactly as a finding of this run's own would.
//
// The two siblings whose drafts must NOT come along: one that is still
// going (its drafts are its own to decide), and one of ANOTHER epic whose
// records ride the same branch (the base branch folds foreign runs in).
func TestARunUnderANewIdAdoptsADeadRunsDraftsAndGatesItsCloseOutOnThem(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{}) // mode report: this run's own workers draft nothing

	const (
		deadKey      = "d23dea4d000000000000000000000000000000000000000000000000000000001"
		liveKey      = "d2311ve4d0000000000000000000000000000000000000000000000000000002"
		elsewhereKey = "d23e1se4d000000000000000000000000000000000000000000000000000003"
	)
	// The integration branch the sibling runs' records live on: the branch a
	// run creates at its boot, created here directly so the seeding below is
	// the only thing that has ever written to it.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	seedSibling(t, f.Repo, "r-dead", "qeu", runstate.StateFailed, seededFinding(deadKey, "r-dead"))
	seedSibling(t, f.Repo, "r-live", "qeu", runstate.StateRunning, seededFinding(liveKey, "r-live"))
	seedSibling(t, f.Repo, "r-elsewhere", "zso", runstate.StateFailed, seededFinding(elsewhereKey, "r-elsewhere"))

	// The new run: a fresh run id (the resumed-under-a-new-id shape), over an
	// epic whose acceptance is prose, with the drafts left for a person — so
	// the one thing that can stop it is the close-out's findings gate.
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", proseFindingsForAPerson: true})
	if err != nil {
		t.Fatalf("the run under the new id did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil ||
		result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("the run ended %s (failure %+v), want the close-out held over the DEAD run's draft: "+
			"without adopting it, the inherited draft gates nothing, forever — the gap this tick closes",
			result.State, result.Failure)
	}
	if !strings.Contains(result.Failure.Message, "A finding the dead run left untriaged") {
		t.Errorf("the hold does not name the inherited finding: %s", result.Failure.Message)
	}

	// The draft is IN this run's own store now — the one store its close-out
	// gates on — still proposed, with its discovery preserved: the dead run's
	// attempt, never this run's numbering.
	mine := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), "r-next")
	adopted, ok, err := mine.Finding(deadKey)
	if err != nil || !ok {
		t.Fatalf("the dead run's draft is not in this run's store: %v %v", ok, err)
	}
	if adopted.Status != runstate.FindingProposed {
		t.Errorf("the adopted draft is %s, want still proposed: the decision is the close-out's",
			adopted.Status)
	}
	if adopted.DiscoveredFrom != "run-r-dead/tick-a1/attempt-1" {
		t.Errorf("the adopted draft's discovery reads %q, want the dead run's own attempt",
			adopted.DiscoveredFrom)
	}
	for _, key := range []string{liveKey, elsewhereKey} {
		if _, ok, err := mine.Finding(key); err != nil || ok {
			t.Errorf("finding %s of a run that is not this epic's to adopt was taken over: %v %v",
				key, ok, err)
		}
	}

	// The adoption is SAID, naming the run it came from: an adoption nobody
	// can see is indistinguishable from a draft that moved by itself.
	line, ok := journalLine(r, "a1", StageFindingAdopted)
	if !ok {
		t.Fatalf("no %s line in the feed:\n%s", StageFindingAdopted, journalText(r))
	}
	for _, want := range []string{"r-dead", deadKey, "A finding the dead run left untriaged"} {
		if !strings.Contains(line, want) {
			t.Errorf("the %s line does not name %q: %s", StageFindingAdopted, want, line)
		}
	}
}

// TestADraftAnotherRunAlreadyDecidedIsNotAdoptedAgain: the cross-run dedup
// holds on the adoption path too. A key is the finding's identity across
// runs, so a draft another run already decided — promoted, or discarded by a
// person — is a decision that stands where it was made: adopting it would
// decide it again, and a promotion would mint the same finding as a second
// tick (hn6's oro and log, the double-promotion dupes.go exists to close).
func TestADraftAnotherRunAlreadyDecidedIsNotAdoptedAgain(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	const key = "d23dec1d00000000000000000000000000000000000000000000000000000001"
	// The integration branch, as above: nothing but the sibling records is
	// ever written to it before the run under test.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	seedSibling(t, f.Repo, "r-dead", "qeu", runstate.StateFailed, seededFinding(key, "r-dead"))
	// The deciding run: it drafted the same finding and a person discarded it.
	seedSibling(t, f.Repo, "r-decider", "qeu", runstate.StateFailed, seededFinding(key, "r-decider"))
	decider := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-decider")
	if _, _, err := decider.TriageFinding(key, runstate.Triage{
		Status: runstate.FindingDiscarded, By: "an operator",
	}); err != nil {
		t.Fatalf("decide the second run's copy of the finding: %v", err)
	}

	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next"})
	if err != nil {
		t.Fatalf("the run under the new id did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (failure %+v), want completed: a finding another run already "+
			"decided gates nothing", result.State, result.Failure)
	}

	// Nothing was adopted: the decision stands in the run that made it.
	mine := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), "r-next")
	if _, ok, err := mine.Finding(key); err != nil || ok {
		t.Fatalf("a finding another run already decided was adopted (%v, %v): a decision is never "+
			"made twice", ok, err)
	}

	// And the link is SAID, naming both runs, so a person reading the feed
	// sees where the decision stands rather than a draft that vanished.
	line, ok := journalLine(r, "a1", StageFindingDuplicate)
	if !ok {
		t.Fatalf("no %s line in the feed:\n%s", StageFindingDuplicate, journalText(r))
	}
	for _, want := range []string{"r-dead", "r-decider", key} {
		if !strings.Contains(line, want) {
			t.Errorf("the %s line does not name %q: %s", StageFindingDuplicate, want, line)
		}
	}
}
