package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The amendments gate (tick 7sn, epic 43y), against a real repository, a real
// origin, the real run-state store and the real tracker-tree publishing path
// — the same harness every other run-level guarantee here is pinned with.
//
// THE SHAPE is the 8em incident: a worker's tracker-edit delivery appends a
// NOTE to the epic's own record — the record the close-out scores the
// acceptance from — declaring an exception the acceptance does not carry. The
// note is applied (a worker must be able to record context where the close-out
// reads it), the tick closes behind its delivery, and the CLOSE-OUT holds on
// the unconfirmed amendment:
//
//  1. a pending amendment holds the close-out's hand-over, naming the note,
//     its proposing tick and the settle command, while the note itself is
//     visibly on the epic's record;
//  2. the operator's CONFIRMATION lifts the hold and the run completes with
//     the note still on the record — confirmed, the amendment reads as the
//     operator's own;
//  3. the operator's REJECTION holds harder: the close-out does not hand over
//     behind an amendment the operator disowned and the record still carries,
//     and only a later confirmation (the record repaired, or the operator
//     revisiting) clears it.

// The note b1 proposes onto the epic, the 8em shape: an exception to A1
// recorded by a worker on the record the acceptance is scored from.
const epicExceptionNote = "tick b1: the slow-check gate is excepted from A1 on the record, now on the record"

// amendmentsStore is the run's amendment records, decided the way the CLI
// decides them: a store over the same repo, remote, branch and run the
// reconciler used.
func amendmentsStore(t *testing.T, repo *testRepo) *runstate.Store {
	t.Helper()
	s, err := runstate.Open(runstate.Options{
		Repo:   repo.Dir,
		Remote: "origin",
		Branch: "epic/qeu",
		RunID:  "r-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	return s
}

// pendingAmendments is how many of the run's amendment records are still
// waiting for the operator.
func pendingAmendments(t *testing.T, s *runstate.Store) []runstate.Amendment {
	t.Helper()
	all, err := s.Amendments()
	if err != nil {
		t.Fatal(err)
	}
	var pending []runstate.Amendment
	for _, a := range all {
		if a.Status != runstate.AmendmentConfirmed {
			pending = append(pending, a)
		}
	}
	return pending
}

// 1. THE HOLD. The run applies the worker's note on the epic, closes the tick
// behind its delivery, and the close-out holds the hand-over on the amendment
// the operator has not confirmed — naming the note, the tick that proposed it
// and the command that settles it.
func TestAWorkerNoteOnTheEpicHoldsTheCloseOutForTheOperator(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "epic_note"})
	repo := f.Repo
	r, result, err := f.run(repo, fixtureOptions{mode: "epic_note"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s — a worker-authored exception on the epic's record is not the "+
			"operator's confirmation, and the close-out must hold: %+v", result.State, result.Reason, result.Failure)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedEpicAmendmentUnconfirmed {
		t.Fatalf("failure %+v, want %s", result.Failure, RefusedEpicAmendmentUnconfirmed)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("failure tick %s, want co: the hold is the close-out's, one decision point at the end",
			result.Failure.TickID)
	}
	for _, want := range []string{
		epicExceptionNote,
		"tick b1",
		"ticfac amendments qeu",
		"never the operator's word",
	} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the hold does not name %q — a hold a person cannot act on is a stall by definition: %s",
				want, result.Failure.Message)
		}
	}

	// The tick that proposed the note CLOSED behind its delivery — the hold is
	// the close-out's, not the tick's, the same shape the findings hold keeps.
	if state, err := f.Tracker.load(); err != nil {
		t.Fatal(err)
	} else if state.Ticks["b1"].Status != "closed" {
		t.Errorf("b1 is %s, want closed: the note was its delivery, applied by the run's own writer",
			state.Ticks["b1"].Status)
	} else if !strings.Contains(state.Ticks["qeu"].Notes, epicExceptionNote) {
		t.Errorf("the epic's notes do not carry the applied note: %q", state.Ticks["qeu"].Notes)
	}

	// The amendment is on the run's record, durably, naming the attempt whose
	// words they are — the operator decides against the worker's own text.
	s := amendmentsStore(t, repo)
	pending := pendingAmendments(t, s)
	if len(pending) != 1 {
		t.Fatalf("%d pending amendment(s), want 1: %+v", len(pending), pending)
	}
	if pending[0].ProposedBy != "b1" || pending[0].Attempt < 1 {
		t.Errorf("the amendment does not name its proposer: %+v", pending[0])
	}
	if pending[0].Value != epicExceptionNote {
		t.Errorf("the amendment does not carry the worker's words verbatim: %q", pending[0].Value)
	}

	// The run's own journal says the amendment was FILED — the write's meaning
	// is not settled, and the record says so where a person reads the run.
	var filed bool
	for _, event := range r.Journal() {
		if event.Tick == "b1" && event.Stage == StageAmendmentFiled {
			filed = true
			if !strings.Contains(event.Detail, "epic") {
				t.Errorf("the filed line does not name the amendment: %s", event.Detail)
			}
		}
	}
	if !filed {
		t.Errorf("no %s line for b1: a worker's note on the epic is filed as awaiting the operator, and "+
			"the feed says so", StageAmendmentFiled)
	}
}

// 2. THE CONFIRMATION. The operator's word lifts the hold: the resume
// completes with the note still on the epic's record — confirmed, the
// amendment reads as the operator's own.
func TestTheOperatorsConfirmationLiftsTheAmendmentsHold(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "epic_note"})
	repo := f.Repo
	if _, result, err := f.run(repo, fixtureOptions{mode: "epic_note"}); err != nil {
		t.Fatalf("run: %v", err)
	} else if result.Failure == nil || result.Failure.Reason != RefusedEpicAmendmentUnconfirmed {
		t.Fatalf("failure %+v, want %s: the fixture run must reach the hold first", result.Failure,
			RefusedEpicAmendmentUnconfirmed)
	}

	s := amendmentsStore(t, repo)
	pending := pendingAmendments(t, s)
	if len(pending) != 1 {
		t.Fatalf("%d pending amendment(s), want 1", len(pending))
	}
	outcome, decided, err := s.DecideAmendment(pending[0].Key,
		runstate.AmendmentDecision{Status: runstate.AmendmentConfirmed, By: "the operator"})
	if err != nil || outcome != runstate.Updated {
		t.Fatalf("confirm the amendment: outcome %s err %v", outcome, err)
	}
	if decided.DecidedBy != "the operator" {
		t.Errorf("the decision is not attributed: %+v", decided)
	}

	_, result, err := f.run(repo, fixtureOptions{mode: "epic_note"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — a confirmed amendment is the operator's own, and the close-out hands "+
			"over behind it: %+v", result.State, result.Reason, result.Failure)
	}
	// The note the operator confirmed STANDS on the epic's record: confirming
	// is keeping the worker's words, now the operator's — not removing them.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Ticks["qeu"].Notes, epicExceptionNote) {
		t.Errorf("the confirmed note is gone from the epic's record: %q", state.Ticks["qeu"].Notes)
	}
	// And the run did not dispatch the close-out's job again for nothing: the
	// resume closes each role tick behind its recorded decision.
	if got := f.Tracker.count("close:co"); got != 1 {
		t.Errorf("co closed %d times", got)
	}
}

// 3. THE REJECTION. The operator disowning the amendment holds the close-out
// harder — the record still carries it — and only a later confirmation (the
// record repaired, or the operator revisiting) clears the hold.
func TestTheOperatorsRejectionHoldsUntilRevisited(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "epic_note"})
	repo := f.Repo
	if _, result, err := f.run(repo, fixtureOptions{mode: "epic_note"}); err != nil {
		t.Fatalf("run: %v", err)
	} else if result.Failure == nil || result.Failure.Reason != RefusedEpicAmendmentUnconfirmed {
		t.Fatalf("failure %+v, want %s: the fixture run must reach the hold first", result.Failure,
			RefusedEpicAmendmentUnconfirmed)
	}

	s := amendmentsStore(t, repo)
	pending := pendingAmendments(t, s)
	if len(pending) != 1 {
		t.Fatalf("%d pending amendment(s), want 1", len(pending))
	}
	if _, _, err := s.DecideAmendment(pending[0].Key,
		runstate.AmendmentDecision{Status: runstate.AmendmentRejected, By: "the operator"}); err != nil {
		t.Fatalf("reject the amendment: %v", err)
	}

	_, result, err := f.run(repo, fixtureOptions{mode: "epic_note"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedEpicAmendmentUnconfirmed {
		t.Fatalf("failure %+v, want %s: a rejected amendment the record still carries holds the close-out",
			result.Failure, RefusedEpicAmendmentUnconfirmed)
	}
	if !strings.Contains(result.Failure.Message, "REJECTED by the operator") {
		t.Errorf("the hold does not name the operator's rejection: %s", result.Failure.Message)
	}

	// The one reopening: confirmation after the record was repaired, or the
	// operator's mind changed. It clears the hold.
	if outcome, _, err := s.DecideAmendment(pending[0].Key,
		runstate.AmendmentDecision{Status: runstate.AmendmentConfirmed, By: "the operator"}); err != nil ||
		outcome != runstate.Updated {
		t.Fatalf("confirm after the rejection: outcome %s err %v", outcome, err)
	}
	if _, result, err := f.run(repo, fixtureOptions{mode: "epic_note"}); err != nil {
		t.Fatalf("resume: %v", err)
	} else if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — the revisited decision must clear the hold: %+v",
			result.State, result.Reason, result.Failure)
	}
}

// The PR body carries the amendments section — the text the operator reads to
// decide, full value like the findings — and the composition stays honest when
// a run filed none.
func TestThePRBodyCarriesTheAmendments(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "epic_note"})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "epic_note"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedEpicAmendmentUnconfirmed {
		t.Fatalf("failure %+v, want %s", result.Failure, RefusedEpicAmendmentUnconfirmed)
	}
	body, _, err := r.composePRBody("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Amendments to the epic's record",
		"PENDING",
		epicExceptionNote,
		"ticfac amendment qeu",
		"never the operator's word",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the PR body does not carry %q — the PR is where the operator decides, and a section "+
				"that omits the worker's words sends the decision against prose nobody scored", want)
		}
	}

	// A run that filed no amendment states the absence rather than staying
	// silent: a body that says nothing reads as "no worker amended the
	// record", which is a claim only the record can make.
	f2 := newFixture(t, fixtureOptions{})
	r2, result2, err := f2.run(f2.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("plain run: %v", err)
	}
	if result2.State != runstate.StateCompleted {
		t.Fatalf("the plain run ended %s: %s: %+v — an epic whose workers never touched its record hands over",
			result2.State, result2.Reason, result2.Failure)
	}
	plain, _, err := r2.composePRBody("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "No worker proposed an amendment to the epic's own record.") {
		t.Errorf("the plain run's PR body does not state the absence of amendments:\n%s", plain)
	}
}
