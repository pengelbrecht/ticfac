package reconcile

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The tracker-edit delivery (hn6 run_d51a, yjq). yjq's deliverable was a
// one-field re-flow of epic hn6's acceptance_criteria: six [A<n>] marks on
// one line, so the parser read one item and A2–A6 were unaddressable. The
// worker boundary rightly forbids .tick/ writes and tk, so three attempts
// answered BLOCKED with the exact text in prose, and the run redispatched it
// a tier up into the same wall — to be held once the ladder was spent.
//
// The orchestrator owns tracker state, so it owns this path: the worker
// PROPOSES the edit in a typed block, and the run validates it against the
// tracker and applies it through its own durable writer, as the attempt's
// delivery — the integrated gate runs over it and the tick closes behind it.

// The fixture epic's acceptance as yjq found hn6's: two marks, one line.
const heldAcceptance = "[A1] Every tick closes behind a green gate; [A2] The fixture epic carries a second item."

// The re-flow the fake runner proposes: the same words, one item per line.
const reflowedAcceptance = "[A1] Every tick closes behind a green gate;\n[A2] The fixture epic carries a second item."

func TestATickWhoseDeliverableIsATrackerEditClosesOnTheRunsOwnWrite(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "tracker_edit"})
	setEpicAcceptance(t, f, heldAcceptance)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "tracker_edit"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — a tick whose deliverable is a tracker edit is delivered by the run's own "+
			"write, not held: %+v", result.State, result.Reason, result.Failure)
	}

	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Ticks["qeu"].AcceptanceCriteria; got != reflowedAcceptance {
		t.Errorf("the epic's acceptance is %q, want the proposed re-flow %q", got, reflowedAcceptance)
	}
	if !strings.Contains(state.Ticks["b1"].Notes, "re-flowed the epic acceptance one item per line") {
		t.Errorf("b1's proposed note was not appended: %q", state.Ticks["b1"].Notes)
	}
	if state.Ticks["b1"].Status != "closed" {
		t.Errorf("b1 is %s, want closed: its delivery was the tracker edit, and the run applied it", state.Ticks["b1"].Status)
	}
	// The record is the tracker's, on origin — written by the run's durable
	// writer, not by a worker's commit.
	record := mustRun(t, f.Repo.Dir, "git", "show", "origin/"+r.IntegrationBranch()+":.tick/issues/qeu.json")
	if !strings.Contains(record, `[A1] Every tick closes behind a green gate;\n[A2]`) {
		t.Errorf("the integration branch's record of qeu does not carry the re-flow:\n%s", record)
	}

	var edited, gated bool
	for _, event := range r.Journal() {
		if event.Tick != "b1" {
			continue
		}
		switch event.Stage {
		case StageTrackerEdited:
			edited = true
			if gated {
				t.Errorf("b1's tracker edit was applied after its gate: the gate runs over what the attempt delivered")
			}
		case StageGatePassed:
			gated = true
		case StageRejected, StageRedispatched:
			t.Errorf("b1 was %s (%s): a tracker-edit delivery is not an empty branch", event.Stage, event.Detail)
		}
	}
	if !edited {
		t.Errorf("no %s line for b1: a tracker write the run made for a worker must be on the feed", StageTrackerEdited)
	}
	if !gated {
		t.Errorf("b1 closed without an integrated gate: a tracker-edit delivery is gated like any other")
	}
}

// The run holds the proposal to the tracker as it stands: an acceptance edit
// that would DROP an item the record marks is refused — never applied — and
// the tick is not closed on it.
func TestATrackerEditThatDropsAnAcceptanceItemIsRefused(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "tracker_edit_drop"})
	setEpicAcceptance(t, f, heldAcceptance)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "tracker_edit_drop", autoResumeCap: -1})
	if err == nil && result != nil && result.State == runstate.StateCompleted {
		t.Fatal("the run completed over a tracker edit that drops an acceptance item")
	}
	state, loadErr := f.Tracker.load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if got := state.Ticks["qeu"].AcceptanceCriteria; got != heldAcceptance {
		t.Errorf("the epic's acceptance became %q: an edit that drops A2 must never be applied", got)
	}
	if state.Ticks["b1"].Status == "closed" {
		t.Error("b1 closed on a refused tracker edit")
	}
	var refused bool
	for _, event := range r.Journal() {
		if event.Tick == "b1" && event.Stage == StageTrackerEditRefused {
			refused = true
			if !strings.Contains(event.Detail, "A2") {
				t.Errorf("the refusal does not name the dropped item: %s", event.Detail)
			}
		}
	}
	if !refused {
		t.Errorf("no %s line for b1", StageTrackerEditRefused)
	}
}

// The blocked shape of the same delivery (hn6 tick l89). A worker whose
// deliverable is a tracker edit cannot write it, and its honest answer is
// BLOCKED — yjq's three attempts all said exactly that, each with the fix in
// hand, and the ladder answered every one by redispatching into the same
// boundary wall. When the blocked answer names the fix as the tracker write
// itself — the typed block — the run applies the proposal instead: the same
// delivery, the same gate, the same close, and the ladder is never entered.
func TestABlockedAnswerWhoseFixIsTheTrackerEditItProposesClosesOnTheRunsOwnWrite(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "tracker_edit_blocked"})
	setEpicAcceptance(t, f, heldAcceptance)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "tracker_edit_blocked"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — a blocked answer whose fix is the tracker edit it proposed is delivered by "+
			"the run's own write, not held or redispatched: %+v", result.State, result.Reason, result.Failure)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Ticks["qeu"].AcceptanceCriteria; got != reflowedAcceptance {
		t.Errorf("the epic's acceptance is %q, want the proposed re-flow %q", got, reflowedAcceptance)
	}
	if !strings.Contains(state.Ticks["b1"].Notes, "re-flowed the epic acceptance one item per line") {
		t.Errorf("b1's proposed note was not appended: %q", state.Ticks["b1"].Notes)
	}
	if state.Ticks["b1"].Status != "closed" {
		t.Errorf("b1 is %s, want closed: the run applied its named fix, which is the tick's delivery", state.Ticks["b1"].Status)
	}
	var edited, gated bool
	for _, event := range r.Journal() {
		if event.Tick != "b1" {
			continue
		}
		switch event.Stage {
		case StageTrackerEdited:
			edited = true
		case StageGatePassed:
			gated = true
		case StageRejected, StageBlockedEscalated, StageBlockedDecide, StageBlockedHeld, StageRedispatched:
			t.Errorf("b1 was %s (%s): the run answers a blocked fix it can apply itself, not the ladder",
				event.Stage, event.Detail)
		}
	}
	if !edited || !gated {
		t.Errorf("b1's edit applied=%v gated=%v: a blocked answer's applied fix is gated like any other delivery", edited, gated)
	}
	// The question never became a blocked-answer record: the ladder did not
	// judge it, so there is nothing for the PR body to list.
	if answers, err := r.blockedAnswers(); err != nil {
		t.Fatal(err)
	} else if len(answers) != 0 {
		t.Errorf("the ladder recorded %+v: applying the named fix is not a blocked answer", answers)
	}
}

// The boundary of the disposition: a blocked answer whose proposed edit the
// run cannot apply (it drops an item the record marks) is a real question —
// the ladder answers it, the fix is not written, and the attempt that named it
// is answered by the run the way every blocked answer is.
func TestABlockedAnswerWhoseProposedEditIsRefusedIsAnsweredByTheLadderNotTheApply(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "tracker_edit_blocked_drop"})
	setEpicAcceptance(t, f, heldAcceptance)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "tracker_edit_blocked_drop"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — a refused edit leaves a real question, which the ladder answers: %+v",
			result.State, result.Reason, result.Failure)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Ticks["qeu"].AcceptanceCriteria; got != heldAcceptance {
		t.Errorf("the epic's acceptance became %q: an edit the run refuses must never be applied", got)
	}
	if state.Ticks["b1"].Status != "closed" {
		t.Errorf("b1 is %s, want closed by the try the ladder dispatched, not by the refused proposal",
			state.Ticks["b1"].Status)
	}
	if got := f.Tracker.count("close:b1"); got != 1 {
		t.Errorf("b1 closed %d times, want once — on the try the ladder dispatched", got)
	}
	refused, ok := journalLine(r, "b1", StageTrackerEditRefused)
	if !ok {
		t.Fatalf("no %s line for b1: the run considered the proposal and refused it, and says so", StageTrackerEditRefused)
	}
	if !strings.Contains(refused, "A2") {
		t.Errorf("the refusal does not name the dropped item: %s", refused)
	}
	if _, ok := journalLine(r, "b1", StageBlockedDecide); !ok {
		t.Errorf("b1's blocked answer was not given to the ladder: its stages are %v", r.Stages("b1"))
	}
	var edited bool
	for _, event := range r.Journal() {
		if event.Tick == "b1" && event.Stage == StageTrackerEdited {
			edited = true
		}
	}
	if edited {
		t.Errorf("a %s line for b1 exists: nothing of the refused proposal is applied", StageTrackerEdited)
	}
}

// The disposition never answers a question the standing orders reserve for a
// person: a blocked answer naming an always-ask class holds, and the valid
// edit beside it is not written — the run does not buy its way past the
// orders by applying what the report happened to carry.
func TestAnAlwaysAskQuestionBesideAValidNamedEditStillHoldsAndIsNotApplied(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: ceilingGate, mode: "tracker_edit_blocked_ask"})
	declareStandingOrders(t, f.Repo)
	setEpicAcceptance(t, f, heldAcceptance)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: ceilingGate, mode: "tracker_edit_blocked_ask"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.Reason != RefusedNeedsHuman {
		t.Fatalf("the run ended %s (%+v), want held as %s", result.State, result.Failure, RefusedNeedsHuman)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Ticks["qeu"].AcceptanceCriteria; got != heldAcceptance {
		t.Errorf("the epic's acceptance became %q: an always-ask question is not answered by applying the edit beside it", got)
	}
	held, ok := journalLine(r, "b1", StageBlockedHeld)
	if !ok || !strings.Contains(held, "credentials") {
		t.Errorf("b1's question did not hold for a person: %q", held)
	}
	for _, call := range []string{"edit:qeu", "edit:b1", "note:b1"} {
		if got := f.Tracker.count(call); got != 0 {
			t.Errorf("%s was written %d times: nothing of the held answer's proposal is applied", call, got)
		}
	}
}

// The triage half: a finding whose whole fix is a tracker edit carries the
// exact change, and the run applies it — the draft triaged FIXED with the
// commit that applied it — rather than absorbing a tick no worker could do.
func TestAFindingWhoseFixIsATrackerEditIsAppliedRatherThanAbsorbed(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "tracker_edit_finding"})
	setEpicAcceptance(t, f, heldAcceptance)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "tracker_edit_finding"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Ticks["qeu"].AcceptanceCriteria; got != reflowedAcceptance {
		t.Errorf("the epic's acceptance is %q, want the finding's edit applied: %q", got, reflowedAcceptance)
	}

	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	if records, err := store.Absorptions(); err != nil {
		t.Fatal(err)
	} else if len(records) != 0 {
		t.Errorf("the finding was absorbed as a tick (%+v): its fix was the tracker edit it carried", records)
	}
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("%d finding draft(s), want 1: %+v", len(findings), findings)
	}
	if findings[0].Status != runstate.FindingFixed || !sha40.MatchString(findings[0].FixedAs) {
		t.Errorf("the finding is %s fixed as %q, want fixed as the commit that applied the edit",
			findings[0].Status, findings[0].FixedAs)
	}
	if !strings.Contains(findings[0].TriagedBy, "ticfac run") {
		t.Errorf("the triage is attributed to %q, want the run", findings[0].TriagedBy)
	}
	var edited bool
	for _, event := range r.Journal() {
		if event.Stage == StageTrackerEdited && event.Tick == "a1" {
			edited = true
		}
		if event.Stage == StageAbsorbed {
			t.Errorf("an absorption was recorded: %s", event.Detail)
		}
	}
	if !edited {
		t.Errorf("no %s line for the finding a1 reported", StageTrackerEdited)
	}
}

// The production tracker (the tk client) has no verb for a field edit, so the
// durable writer rewrites the record the way it adopts and places: one prose
// field replaced, everything else as it was, a field it does not edit refused.
//
// short: one temporary directory; no processes.
func TestTheRecordEditReplacesOneProseFieldAndNothingElse(t *testing.T) {
	t.Parallel()
	tree := &trackerTree{dir: t.TempDir()}
	standing := tk.Tick{ID: "qeu", Title: "the fixture epic", Status: "open", Type: "epic",
		AcceptanceCriteria: heldAcceptance, Description: "the goal", BlockedBy: []string{"x1"}}
	if err := writeTrackerRecord(tree, standing); err != nil {
		t.Fatal(err)
	}
	edited, err := editTickRecord(tree, "qeu", subprocess.TrackerFieldAcceptance, reflowedAcceptance, "2026-10-01T08:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trackerRecordPath(tree.dir, "qeu"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk tk.Tick
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	want := standing
	want.AcceptanceCriteria, want.UpdatedAt = reflowedAcceptance, "2026-10-01T08:00:00Z"
	if !reflect.DeepEqual(onDisk, want) || !reflect.DeepEqual(edited, want) {
		t.Errorf("the record reads back as %+v, want %+v", onDisk, want)
	}
	if _, err := editTickRecord(tree, "qeu", "status", "closed", "2026-10-01T08:00:00Z"); err == nil {
		t.Error("the record edit rewrote a field that is the run's own")
	}
}
