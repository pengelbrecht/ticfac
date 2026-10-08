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

// A worker's note that is ONE line is its own headline: FirstLine is the
// value's whole text, so the level that drops the value drops nothing of it
// and the headline alone can still push the body past the budget — a pasted
// log, a paragraph with no line break in it, an exception argued in one
// sentence. The most condensed level bounds the headline (tick 8wa's second
// half): the operator's decision surface keeps the worker's first words, the
// status and the full key that names the record, and the rest of the line is
// cut at a rune boundary with a pointer to where it continues. The
// uncondensed levels keep every headline whole — the value is written in full
// under them, and a headline shorter than its own value would say less than
// the body already does.
//
// short: one fixture run to reach the amendments hold, then composition
func TestTheMostCondensedPRBodyBoundsAnAmendmentWhoseWholeValueIsOneLine(t *testing.T) {
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
	filed, err := amendmentsStore(t, f.Repo).Amendments()
	if err != nil || len(filed) != 1 {
		t.Fatalf("amendments %v (%v): the fixture run files one", filed, err)
	}

	// The note a worker proposed: one line, longer than the whole budget, so
	// no level can fit its full text and the headline is all there is to bound.
	const line = "A worker's note on the epic's record, one line with no line break in it, "
	note := strings.Repeat(line, 1600)
	key := amendmentKey(subprocess.TrackerEdit{
		Tick: r.opts.EpicID, Field: subprocess.TrackerFieldNotes, Value: note,
	})
	if _, err := amendmentsStore(t, f.Repo).PutAmendment(runstate.Amendment{
		Key: key, Source: "ticfac-worker", EpicID: r.opts.EpicID,
		Field: runstate.AmendmentFieldNotes, Value: note,
		ProposedBy: "b1", Attempt: 1, ProposedAt: filed[0].ProposedAt,
		Status: runstate.AmendmentPending, Provenance: filed[0].Provenance,
	}); err != nil {
		t.Fatal(err)
	}

	// Below the most condensed level every headline is whole: the value is
	// written under it, and a bounded headline there would understate a body
	// that carries the whole text anyway.
	for condense := 1; condense < prBodyOmitAmendmentText; condense++ {
		body, _, err := r.composePRBodyAt("", condense)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, strings.ReplaceAll(note, "\n", "\n  ")) {
			t.Errorf("level %d omits the amendment's value", condense)
		}
	}

	// The most condensed level bounds the headline: the worker's first words
	// — the prBodyHeadlineChars runes the operator judges the note by — stay on
	// the PR, followed by the cut mark that says the line continues in the
	// record, and nothing past them does.
	body, _, err := r.composePRBodyAt("", prBodyMostCondensed)
	if err != nil {
		t.Fatal(err)
	}
	headline := string([]rune(note)[:prBodyHeadlineChars])
	if !strings.Contains(body, headline+"…") {
		t.Errorf("the most condensed body does not carry the amendment's first %d characters as its headline:\n%s",
			prBodyHeadlineChars, firstN(body, 400))
	}
	if strings.Contains(body, headline+string([]rune(note)[prBodyHeadlineChars])) {
		t.Errorf("the most condensed body carries the amendment's whole one-line value as its headline:\n%s",
			firstN(body, 400))
	}
	for _, want := range []string{"PENDING", key, ".ticfac/runs/" + r.runID + "/amendments/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the most condensed body does not carry %q:\n%s", want, firstN(body, 400))
		}
	}

	// The composed body — the one the PR is written with — fits the budget on
	// the strength of that bound, instead of falling to the forge's last-resort
	// fit and losing the readiness section at its tail.
	readiness := "\n## Readiness\n\n- the fold, the gate and CI, checked by this readying.\n"
	composed, _, err := r.composePRBody(readiness)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(composed) > prBodyBudget {
		t.Errorf("the composed body is %d characters, over the %d budget: it falls to the forge's "+
			"last-resort fit, which truncates the tail where the readiness section sits",
			utf8.RuneCountInString(composed), prBodyBudget)
	}
	if !strings.Contains(composed, "## Readiness") {
		t.Errorf("the composed body lost the readiness section:\n%s", firstN(composed, 400))
	}
	if !strings.Contains(composed, key) {
		t.Errorf("the composed body lost the key that names where the note's full text lives:\n%s",
			firstN(composed, 400))
	}
}

// The headline bound the most condensed level applies (tick 8wa): whole at
// every level below it — the value is written in full under the headline
// there, and a headline shorter than its own value would say less than the
// body already does — and cut to prBodyHeadlineChars runes at it, at a rune
// boundary and marked with the cut that says the line continues in the
// record, because a value that is one line is its own whole headline.
//
// short: pure selection over a record already in memory
func TestBoundHeadlineIsWholeBelowTheMostCondensedLevelAndCutAtIt(t *testing.T) {
	t.Parallel()

	oneLine := runstate.Amendment{Value: strings.Repeat("à", prBodyHeadlineChars+40)}
	for condense := 0; condense < prBodyOmitAmendmentText; condense++ {
		if got := boundHeadline(oneLine, condense); got != oneLine.FirstLine() {
			t.Errorf("level %d cut the headline to %q, want it whole", condense, got)
		}
	}
	want := string([]rune(oneLine.FirstLine())[:prBodyHeadlineChars]) + "…"
	if got := boundHeadline(oneLine, prBodyMostCondensed); got != want {
		t.Errorf("the most condensed headline is %q, want the first %d runes with the cut mark",
			got, prBodyHeadlineChars)
	}
	// The cut is by rune: a body is counted in characters, and one that split
	// a rune would send a malformed character to the forge.
	if !utf8.ValidString(boundHeadline(oneLine, prBodyMostCondensed)) {
		t.Error("the cut headline is not valid UTF-8")
	}
	// A headline already within the bound, and a value whose first line is
	// short because its length is in its other lines, are carried whole.
	short := runstate.Amendment{Value: "tick b1: the slow-check gate is excepted from A1 on the record"}
	if got := boundHeadline(short, prBodyMostCondensed); got != short.FirstLine() {
		t.Errorf("a headline within the bound was cut to %q", got)
	}
	multiLine := runstate.Amendment{Value: "first line\n" + strings.Repeat("à", prBodyHeadlineChars+40)}
	if got := boundHeadline(multiLine, prBodyMostCondensed); got != "first line" {
		t.Errorf("a multi-line value's headline is %q, want its own first line", got)
	}
}
