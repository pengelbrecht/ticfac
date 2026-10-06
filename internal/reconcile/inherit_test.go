package reconcile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// ------------------------------------------- tick d9d: unreadable siblings ---

// makeCheckpointUnreadable overwrites a sibling run's seeded checkpoint with
// bytes this binary cannot decode, through the store's raw record primitives:
// the document is one this binary cannot produce, which is the point.
func makeCheckpointUnreadable(t *testing.T, repo *testRepo, runID, document string) {
	t.Helper()
	editor := openRunStore(t, repo.Dir, "epic/qeu", "r-edit")
	if _, err := editor.UpdateIfSHA(runstate.CheckpointPath(runID), []byte(document)); err != nil {
		t.Fatalf("overwrite run %s's checkpoint with one this binary cannot read: %v", runID, err)
	}
}

// newerBinaryCheckpoint is run runID's seeded checkpoint with one field a
// NEWER binary writes and this one does not know: the shape a factory ahead
// of the local binary leaves on a shared integration branch, which the
// closed schemas refuse rather than guess at.
func newerBinaryCheckpoint(t *testing.T, repo *testRepo, runID string) string {
	t.Helper()
	editor := openRunStore(t, repo.Dir, "epic/qeu", "r-edit")
	raw, ok, err := editor.Read(runstate.CheckpointPath(runID))
	if err != nil || !ok {
		t.Fatalf("read run %s's seeded checkpoint: %v %v", runID, ok, err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("run %s's seeded checkpoint is not JSON: %v", runID, err)
	}
	record["factory_epoch"] = "written by a binary newer than this one"
	edited, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(edited) + "\n"
}

// titledSiblingFinding is seededFinding with its own title, so a test can
// tell one sibling's draft from another's in the records that name them.
func titledSiblingFinding(key, runID, title string) runstate.Finding {
	finding := seededFinding(key, runID)
	finding.Title = title
	return finding
}

// TestAnUnreadableSiblingCheckpointStopsNoBoot (tick d9d): the boot sweep
// reads every run's checkpoint on the integration branch — other epics'
// runs ride the same branch, folded in from the base — and decodeRecord
// refuses a field this binary cannot express. One unreadable checkpoint,
// wherever it came from, used to abort Run at its boot: a factory-written
// checkpoint newer than the local binary, or one corrupt record, stopped
// EVERY run of the epic at its start, forever, over a record no run of this
// epic can fix. The sweep's own rule answers instead: a checkpoint that
// cannot say which epic it is, or that its run ended, is a sibling not
// ended, so its drafts stay where they stand — said on the feed, because a
// skip nobody can see is indistinguishable from a sweep that never ran —
// and the readable siblings are still swept, adopted exactly as they were.
func TestAnUnreadableSiblingCheckpointStopsNoBoot(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	const (
		deadKey    = "d9ddea4d00000000000000000000000000000000000000000000000000000001"
		newerKey   = "d9dnewer0000000000000000000000000000000000000000000000000000002"
		corruptKey = "d9dcorr0000000000000000000000000000000000000000000000000000003"
	)
	// The integration branch, as the first inherit test builds it: nothing
	// but the sibling records is ever written to it before the run under test.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	// The READABLE dead sibling, whose draft the sweep must still adopt.
	seedSibling(t, f.Repo, "r-dead", "qeu", runstate.StateFailed,
		titledSiblingFinding(deadKey, "r-dead", "the dead run's readable draft"))
	// The unreadable pair: another EPIC's run written by a newer binary (the
	// breadth — other epics' records are read before the epic filter can
	// spare them), and a corrupt checkpoint of THIS epic's own.
	seedSibling(t, f.Repo, "r-newer", "zso", runstate.StateFailed,
		titledSiblingFinding(newerKey, "r-newer", "the newer binary's draft"))
	seedSibling(t, f.Repo, "r-corrupt", "qeu", runstate.StateFailed,
		titledSiblingFinding(corruptKey, "r-corrupt", "the corrupt run's draft"))
	makeCheckpointUnreadable(t, f.Repo, "r-newer", newerBinaryCheckpoint(t, f.Repo, "r-newer"))
	makeCheckpointUnreadable(t, f.Repo, "r-corrupt", "{ not a checkpoint at all\n")

	// A run whose close-out is held over the one ADOPTED draft — the readable
	// dead sibling's — so the outcome proves the sweep ran and CHOSE, rather
	// than a boot that never got anywhere at all.
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", proseFindingsForAPerson: true})
	if err != nil {
		t.Fatalf("the run under the new id did not finish — one unreadable sibling checkpoint must not stop a boot: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil ||
		result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("the run ended %s (failure %+v), want the close-out held over the READABLE dead sibling's "+
			"draft: the sweep must still sweep what it can read", result.State, result.Failure)
	}
	if !strings.Contains(result.Failure.Message, "the dead run's readable draft") {
		t.Errorf("the hold does not name the readable sibling's draft: %s", result.Failure.Message)
	}
	for _, title := range []string{"the newer binary's draft", "the corrupt run's draft"} {
		if strings.Contains(result.Failure.Message, title) {
			t.Errorf("the hold names %q: an unreadable sibling's draft is never adopted, so it gates nothing", title)
		}
	}

	// The readable sibling's draft is IN this run's own store; the unreadable
	// siblings' drafts are left exactly where they stood.
	mine := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), "r-next")
	if _, ok, err := mine.Finding(deadKey); err != nil || !ok {
		t.Fatalf("the readable dead sibling's draft is not in this run's store: %v %v", ok, err)
	}
	for _, key := range []string{newerKey, corruptKey} {
		if _, ok, err := mine.Finding(key); err != nil || ok {
			t.Errorf("finding %s of a sibling whose checkpoint cannot be read was taken over: %v %v", key, ok, err)
		}
	}

	// And the skip is SAID, naming each unreadable run.
	var skipped []string
	for _, event := range r.Journal() {
		if event.Stage == StageInheritUnreadable {
			skipped = append(skipped, event.Detail)
		}
	}
	if len(skipped) != 2 {
		t.Fatalf("the sweep left %d %s lines, want one per unreadable sibling:\n%s",
			len(skipped), StageInheritUnreadable, journalText(r))
	}
	for _, runID := range []string{"r-newer", "r-corrupt"} {
		found := false
		for _, detail := range skipped {
			if strings.Contains(detail, runID) {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s line names run %s: an unreadable sibling nobody can see was skipped", StageInheritUnreadable, runID)
		}
	}
}

// gatingHost is a fake holder host that answers only once every ask has
// ARRIVED: the first ask blocks waiting for the second, so the test can tell
// asks made CONCURRENTLY (both arrive while the first is still waiting) from
// asks made one at a time (the first blocks alone until the test gives up).
type gatingHost struct {
	arrived chan string
	release chan struct{}
}

func (h *gatingHost) ask(_ context.Context, runID string) HolderState {
	h.arrived <- runID
	<-h.release
	return HolderState{Verdict: HolderDead, Evidence: "the fake host says run " + runID + " ended"}
}

// TestTheBootSweepAsksTheHostsConcurrently (tick d9d): each non-terminal
// sibling costs the boot a round trip to another host — the factory, the
// process table — bounded by that host's own client timeout. A serial sweep
// pays every sibling's latency before the run plans anything; the asks go out
// at once, and the answers are folded back in the sweep's own run-id order.
func TestTheBootSweepAsksTheHostsConcurrently(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	const (
		oneKey = "d9d0ne4000000000000000000000000000000000000000000000000000000001"
		twoKey = "d9dtw0400000000000000000000000000000000000000000000000000000002"
	)
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	// Two non-terminal siblings of this epic — runs that died without ever
	// writing a terminal word, the exact siblings only their HOST can speak
	// for — each with a draft waiting on the answer.
	seedSibling(t, f.Repo, "r-one", "qeu", runstate.StateRunning,
		titledSiblingFinding(oneKey, "r-one", "the first dead sibling's draft"))
	seedSibling(t, f.Repo, "r-two", "qeu", runstate.StateRunning,
		titledSiblingFinding(twoKey, "r-two", "the second dead sibling's draft"))

	// The bound the test waits for an ask to arrive: generous for a local
	// fake, and a WAIT ON A CONDITION (an arrival), never on work done.
	const arrivalBound = 30 * time.Second
	host := &gatingHost{arrived: make(chan string, 4), release: make(chan struct{})}
	type bootOutcome struct {
		r      *Reconciler
		result *Result
		err    error
	}
	done := make(chan bootOutcome, 1)
	go func() {
		r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", claimHolder: host.ask})
		done <- bootOutcome{r, result, err}
	}()

	var arrivals []string
	for range 2 {
		select {
		case runID := <-host.arrived:
			arrivals = append(arrivals, runID)
		case <-time.After(arrivalBound):
			close(host.release) // let any blocked ask finish rather than leak the run
			t.Fatalf("only %d host ask(s) arrived in %s: the boot sweep asks siblings' hosts one at a time",
				len(arrivals), arrivalBound)
		}
	}
	// Both asks are in flight at once — the first never returned before the
	// second arrived — which is the concurrency this test exists to pin.
	close(host.release)

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("the run did not finish: %v", out.err)
		}
		if out.result.State != runstate.StateCompleted {
			t.Fatalf("the run ended %s (failure %+v), want completed: both dead siblings' drafts were adopted and decided",
				out.result.State, out.result.Failure)
		}
		mine := openRunStore(t, f.Repo.Dir, out.r.IntegrationBranch(), "r-next")
		for _, key := range []string{oneKey, twoKey} {
			if _, ok, err := mine.Finding(key); err != nil || !ok {
				t.Errorf("finding %s of a host-dead sibling is not in this run's store: %v %v — both answers must be folded back into the sweep",
					key, ok, err)
			}
		}
	case <-time.After(5 * time.Minute):
		t.Fatalf("the run never finished after both host asks were released")
	}
}
