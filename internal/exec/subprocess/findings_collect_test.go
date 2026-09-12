package subprocess

import "testing"

// The findings channel through a REAL collect: the block a worker wrote in its
// report arrives in the collection the reconciler reads, and in the
// role-result envelope — inside `result`, the one object the contract leaves
// open to the role's own payload (tick 7vn).
//
// The two halves both have to hold, and both are pinned here:
//   - Collection.Findings is the typed list the reconciler acts on;
//   - RoleResult.Result["findings"] is the same list, stated in the envelope so
//     it is durable in the collected record even when nothing acts on it —
//     the envelope carries it whether or not this run was the one to draft it.

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
	if collected.Findings[1].Kind != FindingKindUpstreamTick || collected.Findings[1].Target != "pengelbrecht/ticks" {
		t.Errorf("finding[1] %+v, want an upstream tick routed to pengelbrecht/ticks", collected.Findings[1])
	}

	answer := collected.Result.RoleResult
	if answer == nil {
		t.Fatal("no role-result envelope")
	}
	findings, ok := answer.Result["findings"].([]map[string]any)
	if !ok || len(findings) != 2 {
		t.Fatalf("envelope findings %v", answer.Result["findings"])
	}
	if findings[1]["target"] != "pengelbrecht/ticks" {
		t.Errorf("envelope findings[1] %+v", findings[1])
	}
	if got := answer.Result["findings_problem"]; got != "" {
		t.Errorf("findings_problem %v, want the empty string", got)
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
	}
}
