package reconcile

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The body fits GitHub's limit on a PR body (epic 43y's close-out halted on
// a 422 for a body carrying 75 findings' full text): the composer answers the
// first condensation level within the budget, and the most condensed one when
// none is — the forge fits that as its last resort.
//
// short: pure selection over strings already in memory
func TestCondensePRBodyAnswersTheFirstLevelWithinBudget(t *testing.T) {
	t.Parallel()
	sizes := []int{300, 120, 80, 50}
	var asked []int
	compose := func(condense int) (string, int, error) {
		asked = append(asked, condense)
		return strings.Repeat("—", sizes[condense]), condense, nil
	}
	body, n, err := condensePRBody(150, compose)
	if err != nil || n != 1 || len([]rune(body)) != 120 {
		t.Errorf("budget 150: level %d, %d characters, err %v; want level 1", n, len([]rune(body)), err)
	}
	asked = nil
	if _, n, _ := condensePRBody(400, compose); n != 0 || len(asked) != 1 {
		t.Errorf("a body within budget was condensed: level %d after %v", n, asked)
	}
	if _, n, _ := condensePRBody(10, compose); n != prBodyMostCondensed {
		t.Errorf("no level fits: answered level %d, want the most condensed", n)
	}
}

// A condensed body still carries every finding's title — the close-out's
// read-back check counts on it — names each finding's key, and points at
// where the omitted full text lives. The head is never condensed.
func TestACondensedPRBodyKeepsEveryFindingTitleAndPointsAtItsText(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{proseFindingsForAPerson: true, mode: "finding", pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, _, err := f.run(f.Repo, fixtureOptions{proseFindingsForAPerson: true, mode: "finding", pullRequests: forge})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	findings, err := r.store.Findings()
	if err != nil || len(findings) == 0 {
		t.Fatalf("findings %v (%v): the fixture drafts some", findings, err)
	}
	full, _, err := r.composePRBodyAt("", 0)
	if err != nil {
		t.Fatal(err)
	}
	for condense := prBodyOmitFindingText; condense <= prBodyMostCondensed; condense++ {
		body, n, err := r.composePRBodyAt("", condense)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(findings) {
			t.Errorf("level %d counts %d findings, want %d", condense, n, len(findings))
		}
		if !strings.Contains(full, "Discovered beside the work, reported mechanically.") {
			t.Fatalf("the full body does not carry the finding's text the condensed one omits:\n%s", full)
		}
		if strings.Contains(body, "Discovered beside the work, reported mechanically.") {
			t.Errorf("level %d still carries a finding's full text:\n%s", condense, body)
		}
		if !strings.Contains(body, ".ticfac/runs/"+r.runID+"/findings/") {
			t.Errorf("level %d does not say where the omitted text lives:\n%s", condense, body)
		}
		for _, finding := range findings {
			if got := strings.Count(body, finding.Title); got != 1 {
				t.Errorf("level %d carries the title %q %d times, want 1", condense, finding.Title, got)
			}
			if !strings.Contains(body, finding.Key) {
				t.Errorf("level %d does not name the key %s", condense, finding.Key)
			}
		}
		for _, want := range []string{"## Where to look first", "## The review's verdict", "PR + CI close-out rule"} {
			if !strings.Contains(body, want) {
				t.Errorf("level %d lost %q", condense, want)
			}
		}
	}
}

// The amendments are under the condensation too (tick 8wa): a run whose
// workers proposed long notes onto the epic's record must not push the body
// past the budget at the most condensed level and fall to the forge's
// last-resort fit, which truncates the tail — where the readiness section
// sits. So the most condensed level omits each amendment's full value the way
// the first level omits each finding's text: the status line (the worker's
// headline, the proposing attempt, the operator's decision) stays, and the
// value's full text is replaced by a pointer to the record it lives in, named
// by its full key.
//
// short: one fixture run to reach the amendments hold, then composition
func TestTheMostCondensedPRBodyOmitsAmendmentValuesNotTheTail(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{mode: "epic_note", pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "epic_note", pullRequests: forge})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedEpicAmendmentUnconfirmed {
		t.Fatalf("failure %+v, want %s: the fixture run must reach the amendments hold first",
			result.Failure, RefusedEpicAmendmentUnconfirmed)
	}

	// A second amendment, with a value long enough to push the body past the
	// budget even with the findings and the absorptions condensed — the case
	// the tick exists for. Filed through the same store surface the run files
	// its amendments on, keyed the way the run keys its own, attributed to the
	// same proposing attempt the fixture's note came from.
	filed, err := amendmentsStore(t, f.Repo).Amendments()
	if err != nil || len(filed) != 1 {
		t.Fatalf("amendments %v (%v): the fixture run files one", filed, err)
	}
	long := strings.Repeat("Line of a worker-proposed note on the epic's record.\n", 1300)
	longKey := amendmentKey(subprocess.TrackerEdit{
		Tick: r.opts.EpicID, Field: subprocess.TrackerFieldNotes, Value: long,
	})
	if _, err := amendmentsStore(t, f.Repo).PutAmendment(runstate.Amendment{
		Key: longKey, Source: "ticfac-worker", EpicID: r.opts.EpicID,
		Field: runstate.AmendmentFieldNotes, Value: long,
		ProposedBy: "b1", Attempt: 1, ProposedAt: filed[0].ProposedAt,
		Status: runstate.AmendmentPending, Provenance: filed[0].Provenance,
	}); err != nil {
		t.Fatal(err)
	}
	indented := strings.ReplaceAll(long, "\n", "\n  ")

	full, _, err := r.composePRBodyAt("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, indented) {
		t.Fatal("the full body does not carry the amendment's value the condensed one omits")
	}
	// Every level below the most condensed keeps the full value: the value is
	// the one thing the NEW level drops, not something an earlier one already
	// lost.
	for condense := 1; condense < prBodyOmitAmendmentText; condense++ {
		body, _, err := r.composePRBodyAt("", condense)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, indented) {
			t.Errorf("level %d already omits the amendment's value", condense)
		}
	}

	// The most condensed level omits the value but keeps every amendment's
	// identity — status, headline, full key — and says where the full text
	// lives.
	body, _, err := r.composePRBodyAt("", prBodyMostCondensed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, indented) {
		t.Errorf("the most condensed body still carries the amendment's full value:\n%s", firstN(body, 400))
	}
	for _, want := range []string{
		"PENDING",
		"Line of a worker-proposed note on the epic's record.",
		longKey,
		".ticfac/runs/" + r.runID + "/amendments/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the most condensed body does not carry %q:\n%s", want, body)
		}
	}

	// And the composed body — the one the PR is written with — now fits the
	// budget instead of falling to the forge's last-resort fit, and the
	// readiness section handed in at the end survives it whole.
	readiness := "\n## Readiness\n\n- the fold, the gate and CI, checked by this readying.\n"
	composed, _, err := r.composePRBody(readiness)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(composed) > prBodyBudget {
		t.Errorf("the composed body is %d characters, over the %d budget: it falls to the forge's last-resort "+
			"fit, which truncates the tail where the readiness section sits",
			utf8.RuneCountInString(composed), prBodyBudget)
	}
	if strings.Contains(composed, indented) {
		t.Error("the composed body still carries the amendment's full value")
	}
	if !strings.Contains(composed, "## Readiness") {
		t.Errorf("the composed body lost the readiness section:\n%s", firstN(composed, 400))
	}
}
