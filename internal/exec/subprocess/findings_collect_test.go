package subprocess

import "testing"

// The findings channel through a REAL collect: the block a worker wrote in its
// report arrives in the collection the reconciler reads, and in the
// role-result envelope's FIRST-CLASS Findings field (tick 7vn; bundle 4.0.0
// pinned the five closed shapes as $defs.finding instead of letting them ride
// in the open result payload).
//
// The two halves both have to hold, and both are pinned here:
//   - Collection.Findings is the typed list the reconciler acts on;
//   - RoleResult.Findings is the same list, validated by the envelope's own
//     schema, so it is durable in the collected record even when nothing acts
//     on it — and stated ([]) even when the report carried no block at all.
//
// The typed list must NOT also ride in the open payload: the envelope's
// findings are the record now, and a second spelling inside `result` is
// exactly the two-shapes-one-record drift the bundle was cut to retire.

func TestCollectLiftsAReportsFindingsIntoTheEnvelopeAndTheCollection(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "findings"})
	handle := f.Start(f.spec("run-42/tick-abc/attempt-1", "abc"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictReadyToMerge)
	}
	if collected.FindingsProblem != "" {
		t.Fatalf("findings problem %q", collected.FindingsProblem)
	}
	if len(collected.Findings) != 2 {
		t.Fatalf("collection findings %v", collected.Findings)
	}
	if collected.Findings[0].Kind != FindingKindProposedTick || collected.Findings[0].Target != "" {
		t.Errorf("finding[0] %+v, want a proposed tick for this repository", collected.Findings[0])
	}
	// THE DONE EVIDENCE (tick nfo): the linked finding's claim against the
	// epic's definition of done rides the collection — the draft the
	// reconciler files is where the absorption decision reads it — and the
	// finding reported without the fields is carried unlinked, not refused.
	if collected.Findings[0].DoneItem != "A1" || collected.Findings[0].DemonstratingCheck != "go" {
		t.Errorf("finding[0] done evidence is done_item %q, demonstrating_check %q, want A1 and go — "+
			"the claim must reach the reconciler with the finding", collected.Findings[0].DoneItem,
			collected.Findings[0].DemonstratingCheck)
	}
	if collected.Findings[1].DoneItem != "" || collected.Findings[1].DemonstratingCheck != "" {
		t.Errorf("finding[1] done evidence is done_item %q, demonstrating_check %q, want an unlinked finding",
			collected.Findings[1].DoneItem, collected.Findings[1].DemonstratingCheck)
	}
	if collected.Findings[1].Kind != FindingKindUpstreamTick || collected.Findings[1].Target != "pengelbrecht/ticks" {
		t.Errorf("finding[1] %+v, want an upstream tick routed to pengelbrecht/ticks", collected.Findings[1])
	}

	answer := collected.Result.RoleResult
	if answer == nil {
		t.Fatal("no role-result envelope")
	}
	if answer.SchemaVersion != SchemaVersionRoleResult {
		t.Errorf("envelope schema_version %d, want %d — the findings field is a v2 record", answer.SchemaVersion, SchemaVersionRoleResult)
	}
	if len(answer.Findings) != 2 {
		t.Fatalf("envelope findings %v", answer.Findings)
	}
	if answer.Findings[1].Kind != FindingKindUpstreamTick || answer.Findings[1].Target != "pengelbrecht/ticks" {
		t.Errorf("envelope findings[1] %+v, want an upstream tick routed to pengelbrecht/ticks", answer.Findings[1])
	}
	// The envelope's copy is the PINNED five-field record when it is WRITTEN
	// (see TestTheEnvelopeCarriesFindingsAsThePinnedRecord): the evidence
	// rides the block and the draft, and joins the envelope when the bundle
	// adopts it. In memory the same typed list the collection carries.
	if answer.Findings[0].DoneItem != "A1" || answer.Findings[1].DoneItem != "" {
		t.Errorf("the in-memory envelope findings are not the same list the collection carries: %+v", answer.Findings)
	}
	if got := answer.Result["findings_problem"]; got != "" {
		t.Errorf("findings_problem %v, want the empty string", got)
	}
	// The one spelling: the typed list is a field of the record now, and the
	// open payload must not carry a second, unvalidated copy of it.
	if _, still := answer.Result["findings"]; still {
		t.Error("the envelope still carries result[\"findings\"] — the first-class field is the record now, and a second spelling is the drift the bundle was cut to retire")
	}
}

func TestCollectCarriesAnUnparseableBlockAsAProblemNotSilence(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "findings_bad"})
	handle := f.Start(f.spec("run-42/tick-bad/attempt-1", "bad"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if len(collected.Findings) != 0 {
		t.Fatalf("findings %v, want none", collected.Findings)
	}
	if collected.FindingsProblem == "" {
		t.Fatal("the unparseable block was dropped silently — the collection says nothing")
	}
	if answer := collected.Result.RoleResult; answer != nil {
		if got := answer.Result["findings_problem"]; got == "" {
			t.Errorf("the envelope does not name the findings problem: %v", answer.Result)
		}
		// A block that would not parse is a problem the report carries — never
		// an empty list wearing a clean record's clothes: the envelope states
		// BOTH facts, the empty findings and the reason.
		if len(answer.Findings) != 0 {
			t.Errorf("envelope findings %v, want none — the problem is not a finding", answer.Findings)
		}
	}
}
