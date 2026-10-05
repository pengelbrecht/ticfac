package reconcile

import (
	"strings"
	"testing"

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
	sizes := []int{300, 120, 50}
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
