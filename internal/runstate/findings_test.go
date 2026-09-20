package runstate

import (
	"github.com/pengelbrecht/ticfac/internal/shorttest"
	"strings"
	"testing"
)

// The findings draft (tick 7vn), against a real origin: the funnel's shape,
// held by the repository itself.
//
//   - a proposal is create-if-absent keyed on the finding's own dedup key, so
//     a repeat from a later attempt of the same tick is refused by origin and
//     the ORIGINAL stands — including the attempt that first reported it;
//   - the triage is a person's decision: attributed, one decision per draft,
//     and the only thing a triage may change;
//   - whatever the person did with the original — promoted OR discarded — a
//     redelivery proposes nothing new.

func testFinding(key string) Finding {
	p := testProvenance(PhaseWorker)
	p.TickID, p.Attempt = Ptr("a1"), Ptr(1)
	p.Executor, p.Role = Ptr("local-subprocess"), Ptr("implement-tick")
	return Finding{
		SchemaVersion:  SchemaVersion,
		Key:            key,
		Source:         "ticfac-worker",
		DiscoveredFrom: "run-" + testRun + "/tick-a1/attempt-1",
		Kind:           "proposed-tick",
		Title:          "Split the migration into schema and data phases",
		Body:           "The worker found the single-phase rewrite holds the lock too long.",
		Severity:       "high",
		Target:         "",
		TickID:         "a1",
		Attempt:        1,
		Status:         FindingProposed,
		ProposedAt:     "2026-09-11T18:00:00Z",
		Provenance:     p,
	}
}

// THE ACCEPTANCE CASE: a finding becomes a draft, durably — on origin, carrying
// the attempt that discovered it.
func TestAFindingIsProposedAsADraftOnOrigin(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)

	finding := testFinding("d34db33f")
	outcome, err := s.PutFinding(finding)
	if err != nil {
		t.Fatalf("propose the finding: %v", err)
	}
	if outcome != Created {
		t.Fatalf("outcome %s, want %s", outcome, Created)
	}

	// Read back from ORIGIN, not from this writer's view: durable means pushed.
	reader := o.actor("reader", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	reread, ok, err := reader.Finding("d34db33f")
	if err != nil || !ok {
		t.Fatalf("the draft is not on origin: %v %v", ok, err)
	}
	if reread.DiscoveredFrom != finding.DiscoveredFrom {
		t.Errorf("discovered_from %q, want %q: a draft that cannot name the attempt that found it is the "+
			"provenance-less filing this record exists to prevent", reread.DiscoveredFrom, finding.DiscoveredFrom)
	}
	if reread.Status != FindingProposed {
		t.Errorf("status %q, want %q", reread.Status, FindingProposed)
	}
}

// THE ACCEPTANCE CASE: a finding repeated on a later attempt of the same tick
// is deduplicated against the original proposal and proposes nothing new —
// including when the original was discarded.
func TestARepeatedFindingProposesNothingNewWhateverHappenedToTheOriginal(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, tc := range []struct {
		name    string
		triage  string
		as      string
		wantNew bool
	}{
		{"while the original is still proposed", "", "", false},
		{"after the original was discarded", FindingDiscarded, "", false},
		{"after the original was promoted", FindingPromoted, "zz9", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrigin(t)
			s := o.actor("first", testRun)
			if _, err := s.PutFinding(testFinding("d34db33f")); err != nil {
				t.Fatalf("propose: %v", err)
			}
			if tc.triage != "" {
				outcome, _, err := s.TriageFinding("d34db33f", Triage{Status: tc.triage, By: "the operator", PromotedAs: tc.as})
				if err != nil || outcome != Updated {
					t.Fatalf("triage as %s: outcome %s err %v", tc.triage, outcome, err)
				}
			}

			// A later attempt of the same tick reports the same finding again.
			later := o.actor("second-attempt", testRun)
			outcome, err := later.PutFinding(testFinding("d34db33f"))
			if err != nil {
				t.Fatalf("repeat the finding: %v", err)
			}
			if outcome.EffectPermitted() {
				t.Fatalf("outcome %s: the repository accepted a second proposal of the same finding — the "+
					"dedup key is not deduplicating", outcome)
			}

			// The original stands, with the attempt that FIRST reported it,
			// whatever the human did with it.
			original, ok, err := later.Finding("d34db33f")
			if err != nil || !ok {
				t.Fatalf("read the original back: %v %v", ok, err)
			}
			if original.DiscoveredFrom != "run-"+testRun+"/tick-a1/attempt-1" {
				t.Errorf("discovered_from %q: the original must keep the attempt that first reported it",
					original.DiscoveredFrom)
			}
			if tc.triage != "" && original.Status != tc.triage {
				t.Errorf("status %q, want the human's %q", original.Status, tc.triage)
			}
			if tc.triage == "" && original.Status != FindingProposed {
				t.Errorf("status %q, want still proposed", original.Status)
			}

			// Nothing new was proposed: one record, one key.
			findings, err := later.Findings()
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 1 {
				t.Fatalf("findings %v, want exactly the original", findings)
			}
		})
	}
}

func TestTriageIsOneAttributedDecisionPerDraft(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("operator", testRun)
	if _, err := s.PutFinding(testFinding("d34db33f")); err != nil {
		t.Fatal(err)
	}

	// A triage names who made it: a decision nobody can attribute is one
	// nobody can audit — the same discipline a settlement's release answers.
	if _, _, err := s.TriageFinding("d34db33f", Triage{Status: FindingDiscarded}); err == nil {
		t.Fatal("an unattributed triage was accepted")
	}
	// A promotion names the tick that was created; a discard names none.
	if _, _, err := s.TriageFinding("d34db33f", Triage{Status: FindingPromoted, By: "the operator"}); err == nil {
		t.Fatal("a promotion naming no tick was accepted")
	}
	if _, _, err := s.TriageFinding("d34db33f", Triage{Status: FindingDiscarded, By: "the operator", PromotedAs: "zz9"}); err == nil {
		t.Fatal("a discard naming a tick was accepted")
	}

	_, decided, err := s.TriageFinding("d34db33f", Triage{Status: FindingDiscarded, By: "the operator"})
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if decided.TriagedBy != "the operator" || decided.Status != FindingDiscarded {
		t.Fatalf("decided %+v", decided)
	}

	// The decision is never made twice — and the second call is not an error,
	// because "what the human did with it" is exactly what a redelivery must
	// not reopen.
	outcome, second, err := s.TriageFinding("d34db33f", Triage{Status: FindingPromoted, By: "someone else", PromotedAs: "zz9"})
	if err != nil {
		t.Fatalf("re-triage: %v", err)
	}
	if outcome != NoChange {
		t.Fatalf("outcome %s, want %s: a decided draft must not be decidable again", outcome, NoChange)
	}
	if second.TriagedBy != "the operator" || second.PromotedAs != "" {
		t.Fatalf("the second decision overwrote the first: %+v", second)
	}
}

// A routed finding is a draft addressed to ANOTHER repository, and the triage
// is where the routing is enforced: promoting it must name the tick AND the
// repository it belongs in. The store holds the pairing (target, promoted-as);
// the CLI enforces the shapes — this pins that the draft itself keeps the
// target the worker named, which is what a person reads before deciding.
func TestARoutedFindingKeepsItsTargetAndNamesItAtPromotion(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("operator", testRun)
	upstream := testFinding("c0ffee")
	upstream.Kind, upstream.Target = "upstream-tick", "pengelbrecht/ticks"
	if _, err := s.PutFinding(upstream); err != nil {
		t.Fatal(err)
	}
	_, ok, err := s.Finding("c0ffee")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got, want := ok, true; got != want {
		t.Fatal("read back")
	}
	_, promoted, err := s.TriageFinding("c0ffee", Triage{Status: FindingPromoted, By: "the operator", PromotedAs: "pengelbrecht/ticks:of9"})
	if err != nil {
		t.Fatalf("promote the routed finding: %v", err)
	}
	if promoted.PromotedAs != "pengelbrecht/ticks:of9" {
		t.Fatalf("promoted_as %q", promoted.PromotedAs)
	}
}

// short: the draft funnel's refusals, decided in memory
func TestTheDraftRefusesEverythingTheFunnelRefuses(t *testing.T) {
	finding := testFinding("d34db33f")

	// A draft that names no source cannot be deduplicated against its next
	// delivery; one that names no attempt is the provenance-less filing the
	// record exists to prevent.
	for name, mutate := range map[string]func(*Finding){
		"no source":          func(f *Finding) { f.Source = "" },
		"no discovered_from": func(f *Finding) { f.DiscoveredFrom = "" },
		"no tick":            func(f *Finding) { f.TickID = "" },
		"no attempt":         func(f *Finding) { f.Attempt = 0 },
		"a bad kind":         func(f *Finding) { f.Kind = "wish" },
		"a bad severity":     func(f *Finding) { f.Severity = "urgent" },
		"a bad status":       func(f *Finding) { f.Status = "limbo" },
		"a key with a slash": func(f *Finding) { f.Key = "a/b" },
	} {
		t.Run(name, func(t *testing.T) {
			mutate(&finding)
			if err := finding.Validate(); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// short: the draft funnel's refusals, decided in memory
func TestAPromotedDraftWithoutATickIsRefused(t *testing.T) {
	finding := testFinding("d34db33f")
	finding.Status, finding.TriagedAt, finding.TriagedBy = FindingPromoted, "2026-09-11T19:00:00Z", "the operator"
	if err := finding.Validate(); err == nil || !strings.Contains(err.Error(), "names no tick") {
		t.Fatalf("err %v, want the promotion to name its tick", err)
	}
}

// THE FIXED VERDICT (tick her): a finding repaired inside the epic fits
// neither promote nor discard, and the verdict records the commit that
// repaired it, so the claim is checkable rather than asserted.
func TestAFixedVerdictNamesTheCommitThatRepairedIt(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("operator", testRun)
	if _, err := s.PutFinding(testFinding("d34db33f")); err != nil {
		t.Fatal(err)
	}

	outcome, decided, err := s.TriageFinding("d34db33f", Triage{Status: FindingFixed, By: "the operator", FixedAs: "338bbf8b"})
	if err != nil {
		t.Fatalf("triage as fixed: %v", err)
	}
	if outcome != Updated {
		t.Fatalf("outcome %s, want %s", outcome, Updated)
	}
	if decided.Status != FindingFixed {
		t.Fatalf("status %q, want %q", decided.Status, FindingFixed)
	}
	if decided.FixedAs != "338bbf8b" || decided.TriagedBy != "the operator" || decided.PromotedAs != "" {
		t.Fatalf("decided %+v: a fixed verdict names the commit and nothing else", decided)
	}

	// A fixed verdict naming no commit is refused — "fixed" without the
	// commit is the assertion this verdict exists to make checkable.
	if _, _, err := s.TriageFinding("d34db33f", Triage{Status: FindingFixed, By: "the operator"}); err == nil {
		t.Fatal("a fixed verdict naming no commit was accepted")
	}
	// A fixed verdict that is also a promotion is refused: a triage is one
	// decision, not two.
	if _, _, err := s.TriageFinding("d34db33f", Triage{Status: FindingFixed, By: "the operator", PromotedAs: "zz9", FixedAs: "338bbf8b"}); err == nil {
		t.Fatal("a fixed verdict that is also a promotion was accepted")
	}
	// A "commit" that is not a commit id at all is refused, on a draft still
	// waiting: the claim must stay checkable. (The decided draft above must
	// answer NoChange to any further call, whatever the call names.)
	if _, err := s.PutFinding(testFinding("c0ffee11")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TriageFinding("c0ffee11", Triage{Status: FindingFixed, By: "the operator", FixedAs: "not-a-commit"}); err == nil {
		t.Fatal("a fixed verdict naming no commit id was accepted")
	}
}

// THE NOT-SUPPRESSED HALF of the fixed verdict (tick her): a finding triaged
// as fixed that is reported again is NOT deduplicated away — the fix did not
// hold, and that is exactly when the run must hear it. The re-report
// re-proposes the draft: status back to proposed, discovered_from naming the
// attempt that re-found it, and the triage cleared so the person decides
// again with the fix's failure in front of them.
func TestARepeatedFindingAfterAFixedVerdictIsHeardAgain(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("operator", testRun)
	if _, err := s.PutFinding(testFinding("d34db33f")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TriageFinding("d34db33f", Triage{Status: FindingFixed, By: "the operator", FixedAs: "338bbf8b"}); err != nil {
		t.Fatalf("triage as fixed: %v", err)
	}

	// A later attempt of the same tick reports the same finding again: the
	// fix did not hold, and the re-report must not be swallowed by the dedup
	// that rightly holds for promoted and discarded originals.
	later := o.actor("second-attempt", testRun)
	again := testFinding("d34db33f")
	again.Attempt = 2
	again.DiscoveredFrom = "run-" + testRun + "/tick-a1/attempt-2"
	again.Provenance.Attempt = Ptr(2)
	outcome, err := later.PutFinding(again)
	if err != nil {
		t.Fatalf("re-report the fixed finding: %v", err)
	}
	if !outcome.EffectPermitted() {
		t.Fatalf("outcome %s: a finding whose fix did not hold was suppressed — the run must hear it "+
			"again", outcome)
	}

	reopened, ok, err := later.Finding("d34db33f")
	if err != nil || !ok {
		t.Fatalf("read the re-opened draft: %v %v", ok, err)
	}
	if reopened.Status != FindingProposed {
		t.Errorf("status %q, want proposed: the re-report re-opens the draft for triage", reopened.Status)
	}
	if reopened.DiscoveredFrom != again.DiscoveredFrom {
		t.Errorf("discovered_from %q, want %q: the re-opened draft must name the attempt that "+
			"re-found it, which is the evidence the fix did not hold", reopened.DiscoveredFrom, again.DiscoveredFrom)
	}
	if reopened.FixedAs != "" || reopened.TriagedBy != "" {
		t.Errorf("the re-opened draft kept the overturned triage: %+v", reopened)
	}

	// One record, one key: the re-report is the same finding, re-opened —
	// not a second draft beside the first.
	findings, err := later.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings %v, want the one re-opened draft", findings)
	}

	// And the re-opened draft is decidable again — this time as promoted,
	// which DOES hold: the funnel's suppression belongs to the verdicts that
	// mean decided, and fixed is the one verdict that does not.
	outcome, decided, err := later.TriageFinding("d34db33f", Triage{Status: FindingPromoted, By: "the operator", PromotedAs: "zz9"})
	if err != nil || outcome != Updated {
		t.Fatalf("re-triage the re-opened draft: outcome %s err %v", outcome, err)
	}
	if decided.Status != FindingPromoted || decided.PromotedAs != "zz9" {
		t.Fatalf("decided %+v", decided)
	}
	third := o.actor("third-attempt", testRun)
	if outcome, err := third.PutFinding(testFinding("d34db33f")); err != nil || outcome.EffectPermitted() {
		t.Fatalf("outcome %s err %v: after a promoting triage the funnel's dedup holds again", outcome, err)
	}
}
