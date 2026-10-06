package runstate

import (
	"strings"
	"testing"
)

// The amendment confirmation record (tick 7sn, epic 43y), against a real
// origin: the same CAS shape every decision record here keeps, held by the
// repository itself.
//
//   - a worker-proposed amendment to the epic's own record is filed as
//     PENDING, durably, on origin — create-if-absent keyed on the amended
//     record, field and value, so the same amendment from a later attempt, or
//     re-filed by a resumed run, is ONE confirmation;
//   - the operator's decision is attributed, one decision per amendment,
//     and the only thing a decision may change;
//   - a REJECTED amendment has exactly one decision left: confirmation after
//     the record was repaired. A CONFIRMED one is final — the word the
//     close-out hands over behind is never made twice and never undone.

func testAmendment(key string) Amendment {
	p := testProvenance(PhaseWorker)
	p.TickID, p.Attempt = Ptr("b1"), Ptr(1)
	p.Executor, p.Role = Ptr("local-subprocess"), Ptr("implement-tick")
	return Amendment{
		SchemaVersion: SchemaVersion,
		Key:           key,
		Source:        "ticfac-worker",
		EpicID:        "qeu",
		Field:         AmendmentFieldNotes,
		Value:         "tick b1: the PR-review boot is excepted from A1 on this record",
		ProposedBy:    "b1",
		Attempt:       1,
		ProposedAt:    "2026-10-05T18:25:00Z",
		Status:        AmendmentPending,
		Provenance:    p,
	}
}

// THE ACCEPTANCE CASE: the run applies a worker-proposed note on the epic and
// files the amendment as pending, durably, on origin.
func TestAnAmendmentIsFiledPendingOnOrigin(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)

	amendment := testAmendment("5abf2252")
	outcome, err := s.PutAmendment(amendment)
	if err != nil {
		t.Fatalf("file the amendment: %v", err)
	}
	if outcome != Created {
		t.Fatalf("outcome %s, want %s", outcome, Created)
	}

	// Read back from ORIGIN, not from this writer's view: durable means pushed.
	reader := o.actor("reader", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	reread, ok, err := reader.Amendment("5abf2252")
	if err != nil || !ok {
		t.Fatalf("the amendment is not on origin: %v %v", ok, err)
	}
	if reread.Status != AmendmentPending {
		t.Errorf("status %q, want %q: the operator has not decided, and the record says so", reread.Status,
			AmendmentPending)
	}
	if reread.ProposedBy != "b1" || reread.Attempt != 1 {
		t.Errorf("proposed by %s attempt %d: the record must name whose claim it holds", reread.ProposedBy,
			reread.Attempt)
	}
}

// The dedup: the same amendment re-filed — a later attempt re-proposing it, or
// a resumed run re-filing what a killed one already did — is refused by
// origin and the ORIGINAL stands, including the attempt that first proposed
// it. A DIFFERENT amendment (a different value) is a different record.
func TestTheSameAmendmentIsOneConfirmation(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)

	if _, err := s.PutAmendment(testAmendment("5abf2252")); err != nil {
		t.Fatalf("file: %v", err)
	}
	later := testAmendment("5abf2252")
	later.Attempt, later.ProposedBy = 2, "c3"
	outcome, err := s.PutAmendment(later)
	if err != nil {
		t.Fatalf("re-file: %v", err)
	}
	if outcome.EffectPermitted() {
		t.Fatalf("outcome %s: a re-filed amendment must be refused by the repository, never written twice", outcome)
	}
	standing, ok, err := s.Amendment("5abf2252")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if standing.ProposedBy != "b1" || standing.Attempt != 1 {
		t.Errorf("the original did not stand: proposed by %s attempt %d", standing.ProposedBy, standing.Attempt)
	}

	other := testAmendment("0718333001")
	other.Value = "tick c3: a different note on the epic"
	outcome, err = s.PutAmendment(other)
	if err != nil {
		t.Fatalf("file a different amendment: %v", err)
	}
	if outcome != Created {
		t.Fatalf("outcome %s, want %s: a different amendment is a different confirmation", outcome, Created)
	}
}

// THE OPERATOR'S DECISION: attributed, one decision per amendment, the only
// thing a decision may change — and the rejected amendment's one reopening.
func TestTheOperatorDecidesAnAmendmentOnceAndAttributed(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("operator", testRun)

	for _, key := range []string{"5abf2252", "07183330"} {
		if _, err := s.PutAmendment(testAmendment(key)); err != nil {
			t.Fatalf("file %s: %v", key, err)
		}
	}

	if _, _, err := s.DecideAmendment("5abf2252", AmendmentDecision{Status: AmendmentConfirmed}); err == nil {
		t.Error("an unattributed decision was accepted: a decision nobody can attribute is one nobody can audit")
	}
	if _, _, err := s.DecideAmendment("5abf2252", AmendmentDecision{Status: "maybe", By: "the operator"}); err == nil {
		t.Error("a decision outside the vocabulary was accepted")
	}

	// Confirm one, reject the other.
	outcome, decided, err := s.DecideAmendment("5abf2252",
		AmendmentDecision{Status: AmendmentConfirmed, By: "the operator"})
	if err != nil || outcome != Updated {
		t.Fatalf("confirm: outcome %s err %v", outcome, err)
	}
	if decided.DecidedBy != "the operator" || decided.DecidedAt == "" {
		t.Errorf("the decision is not attributed: %+v", decided)
	}
	if _, _, err := s.DecideAmendment("5abf2252",
		AmendmentDecision{Status: AmendmentRejected, By: "the operator"}); err != nil {
		t.Fatalf("reject a confirmed amendment: %v", err)
	} else if standing, _, err := s.Amendment("5abf2252"); err != nil || standing.Status != AmendmentConfirmed {
		t.Fatalf("a confirmed amendment did not stand as final: %v %v", standing, err)
	}

	outcome, decided, err = s.DecideAmendment("07183330",
		AmendmentDecision{Status: AmendmentRejected, By: "the operator"})
	if err != nil || outcome != Updated {
		t.Fatalf("reject: outcome %s err %v", outcome, err)
	}
	if decided.Status != AmendmentRejected {
		t.Fatalf("the rejection did not land: %+v", decided)
	}
	// The one reopening: a rejected amendment may be confirmed — the record
	// was repaired, or the operator's mind changed — and nothing else.
	if _, _, err := s.DecideAmendment("07183330",
		AmendmentDecision{Status: AmendmentRejected, By: "the operator again"}); err == nil {
		t.Error("rejecting a rejected amendment was accepted: reject-again is no decision at all")
	}
	outcome, decided, err = s.DecideAmendment("07183330",
		AmendmentDecision{Status: AmendmentConfirmed, By: "the operator"})
	if err != nil || outcome != Updated {
		t.Fatalf("confirm after a rejection: outcome %s err %v", outcome, err)
	}
	if decided.Status != AmendmentConfirmed {
		t.Fatalf("the reopening did not land: %+v", decided)
	}

	// Every decision the reader on origin reads back.
	reader := o.actor("reader", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	all, err := reader.Amendments()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("%d amendment(s), want 2", len(all))
	}
	for _, a := range all {
		if a.Status != AmendmentConfirmed {
			t.Errorf("amendment %s is %s on origin: the decision must be durable", shortKeyOf(a.Key), a.Status)
		}
	}
}

// A decided record states its decision in full or not at all — a decision
// half in and half out is unauditable either way.
//
// short: Validate over a record already in memory
func TestAnAmendmentStatesItsDecisionInFullOrNotAtAll(t *testing.T) {
	a := testAmendment("5abf2252")
	a.Status, a.DecidedBy = AmendmentConfirmed, "the operator"
	if err := a.Validate(); err == nil {
		t.Error("a decided amendment with no decided_at was accepted")
	}
	a = testAmendment("5abf2252")
	a.DecidedBy = "the operator"
	if err := a.Validate(); err == nil {
		t.Error("a pending amendment naming a decider was accepted")
	}
	a = testAmendment("5abf2252")
	a.Field = "acceptance_criteria"
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "notes") {
		t.Errorf("an amendment outside the field vocabulary was accepted: %v", err)
	}
	a = testAmendment("5abf2252")
	a.Value = ""
	if err := a.Validate(); err == nil {
		t.Error("an amendment with no value was accepted: the operator decides against the worker's words")
	}
}

// shortKeyOf is the listing's spelling of a key, for a message a person reads.
func shortKeyOf(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}
