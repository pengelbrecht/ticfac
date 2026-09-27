package gating

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
)

// The oracle half of the two-tier decision, tested against the same fixture
// the prediction half is: gvc's own done, with A2 bound to the one command
// that proves it and A1, A3 unverified. The oracle's runs are scripted at the
// Runner seam — this package never sees a shell, so neither does the test.

// baseCommit is what the fake runner says every command ran on: the oracle
// neither knows nor checks HOW the runner knows, only that a run keyed by no
// commit is a timestamped opinion rather than a record.
const baseCommit = "f00dcafef00dcafef00dcafef00dcafef00dcafe"

// fakeRunner stands in for the thing that runs a declared command at the seam:
// scripted answers per command id, scripted failures, and every ask captured
// in order — so a test can prove WHICH items the oracle ran, how many times,
// and which it never offered.
type fakeRunner struct {
	answers map[string]Run
	errs    map[string]error
	asks    []string
}

func (f *fakeRunner) Run(_ context.Context, command string) (Run, error) {
	f.asks = append(f.asks, command)
	if err := f.errs[command]; err != nil {
		return Run{}, err
	}
	return f.answers[command], nil
}

// errRunnerDead stands in for a runner that could not run its command at all
// — the seam's failure mode, distinct from a command that ran and answered
// `error`.
var errRunnerDead = errors.New("the runner died")

func passOn(commit string) Run {
	return Run{
		Commit:     commit,
		Result:     ResultPass,
		ExitCode:   0,
		Stdout:     "ok",
		StartedAt:  "2026-09-25T10:00:00Z",
		FinishedAt: "2026-09-25T10:01:00Z",
	}
}

// THE ORACLE RUNS EVERY RUNNABLE ITEM AND ONLY THOSE. The runnable A2's
// command is asked for exactly once; the unverified A1 and A3 are never
// offered to the runner at all — they are not the oracle's to run — and they
// come back in the record as unresolved, not as observations and not as
// silently dropped.
func TestTheOracleRunsEveryRunnableItemAndOnlyThose(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": passOn(baseCommit)}}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed == nil {
		t.Fatal("Observe returned no observed verdict over a done with a runnable item")
	}
	if len(runner.asks) != 1 || runner.asks[0] != "go" {
		t.Fatalf("the runner was asked for %v, want exactly [go]: the runnable item's bound command, once", runner.asks)
	}
	if len(observed.Items) != 1 {
		t.Fatalf("%d observations, want the one runnable item A2", len(observed.Items))
	}
	if observed.Items[0].ItemID != "A2" {
		t.Errorf("the observation is about %q, want A2", observed.Items[0].ItemID)
	}
	if len(observed.Unresolved) != 2 {
		t.Fatalf("%d unresolved items, want the unverified A1 and A3", len(observed.Unresolved))
	}
	if observed.Unresolved[0].ItemID != "A1" || observed.Unresolved[1].ItemID != "A3" {
		t.Errorf("the unresolved items are %v, want [A1 A3] in document order", observed.Unresolved)
	}
	for _, item := range observed.Unresolved {
		if !strings.Contains(item.Reason, "not runnable") && !strings.Contains(item.Reason, "no command") {
			t.Errorf("the unresolved reason for %s does not say the item cannot be run: %q", item.ItemID, item.Reason)
		}
		if strings.Contains(item.Reason, "does not gate") || strings.Contains(item.Reason, "gating") {
			t.Errorf("the unresolved reason for %s assumes a verdict: %q", item.ItemID, item.Reason)
		}
	}
}

// THE WORKED CASE the tick names first: two fixtures made the gate red at
// base, so no tick could close and the done was unreachable. That is GATING,
// observed, no judgement needed — the failing command IS the evidence, the
// item it proves is named, and no classifier is consulted and none may
// override it.
func TestAFailingCommandIsAnObservedGatingVerdict(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": {
		Commit:     baseCommit,
		Result:     ResultFail,
		ExitCode:   1,
		Stderr:     "--- FAIL: TestGateIsGreen (gate_test.go:9)\nFAIL\n",
		FinishedAt: "2026-09-25T10:02:00Z",
	}}}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed == nil {
		t.Fatal("Observe returned no verdict over a failed command")
	}
	if !observed.Gating {
		t.Fatalf("a failing command produced a non-gating verdict: %+v", observed.Verdict)
	}
	if observed.Basis != BasisObserved {
		t.Fatalf("the verdict's basis is %q, want %q: a run is a measurement, whatever a classifier might have said", observed.Basis, BasisObserved)
	}
	if observed.ItemID != "A2" {
		t.Errorf("the verdict names item %q, want A2 — the item the failing command proves", observed.ItemID)
	}
	if observed.Items[0].Gating != true {
		t.Errorf("the observation for A2 is not gating: %+v", observed.Items[0])
	}
	evidence := observed.Items[0].Evidence
	if evidence.Check.ID != "go" || evidence.Check.Kind != "command" {
		t.Errorf("the evidence's check is %+v, want the bound command id go of kind command", evidence.Check)
	}
	if evidence.Result != "fail" || evidence.ExitCode != 1 {
		t.Errorf("the evidence says result=%q exit=%d, want fail and 1", evidence.Result, evidence.ExitCode)
	}
	if !strings.Contains(evidence.Output.Stderr, "--- FAIL") {
		t.Errorf("the evidence's stderr does not carry the command's own output: %+v", evidence.Output)
	}
	if !strings.Contains(observed.Reason, "no judgement") {
		t.Errorf("the reason %q does not say the verdict needed no judgement — it is an observation", observed.Reason)
	}
	if !strings.Contains(observed.Reason, shortCommit(baseCommit)) {
		t.Errorf("the reason %q does not name the commit the failing command ran on", observed.Reason)
	}
}

// The other side: every runnable item's command passes, and the finding is
// NOT GATING by observation — the done is reachable as far as anything can
// observe, and the reason says so while naming what the verdict does NOT
// decide rather than leaving the unverified items to be read as safe.
func TestAPassingCommandIsAnObservedNotGatingVerdict(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": passOn(baseCommit)}}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Gating {
		t.Fatalf("a passing command produced a gating verdict: %+v", observed.Verdict)
	}
	if observed.Basis != BasisObserved {
		t.Fatalf("the verdict's basis is %q, want %q", observed.Basis, BasisObserved)
	}
	if observed.ItemID != "" {
		t.Errorf("a non-gating verdict names item %q, want none", observed.ItemID)
	}
	if !strings.Contains(observed.Reason, "reachable") {
		t.Errorf("the reason %q does not say the done is reachable as far as observed", observed.Reason)
	}
	for _, id := range []string{"A1", "A3"} {
		if !strings.Contains(observed.Reason, id) {
			t.Errorf("the reason %q does not name %s among what the verdict refuses to decide", observed.Reason, id)
		}
	}
	if !strings.Contains(observed.Reason, "rather than assumed either way") {
		t.Errorf("the reason %q does not say the unresolved items come back unresolved rather than assumed either way", observed.Reason)
	}
}

// EVERY runnable item runs, even after one fails: the record is complete —
// the absorption and the retro read which items were observed broken, not
// only that one was — and the named item is the FIRST broken one in document
// order, deterministically, with every broken one in the reason.
func TestEveryRunnableItemRunsEvenAfterAFailure(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{
		"go": {Commit: baseCommit, Result: ResultFail, ExitCode: 1, Stderr: "FAIL"},
		"ts": {Commit: baseCommit, Result: ResultFail, ExitCode: 2, Stderr: "1 test failed"},
	}}
	oracle := NewOracle(runner)
	done := doneOf(t, criteria, map[string]string{"A2": "go", "A3": "ts"})
	observed, _, err := oracle.Observe(context.Background(), theFinding, done)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(runner.asks) != 2 || runner.asks[0] != "go" || runner.asks[1] != "ts" {
		t.Fatalf("the runner was asked for %v, want [go ts] — every runnable item, in document order", runner.asks)
	}
	if len(observed.Items) != 2 {
		t.Fatalf("%d observations, want both runnable items", len(observed.Items))
	}
	if observed.ItemID != "A2" {
		t.Errorf("the verdict names %q, want A2 — the first broken item in document order", observed.ItemID)
	}
	for _, id := range []string{"A2", "A3"} {
		if !strings.Contains(observed.Reason, id) {
			t.Errorf("the reason %q does not name every observed-broken item: missing %s", observed.Reason, id)
		}
	}
}

// THE KEY: the verdict is keyed by the commit it ran on, so the record means
// something on a re-derivation rather than being a timestamped opinion. The
// commit is what the RUNNER reports — the thing that runs is the thing that
// knows what it ran on — and the record carries it through the JSON round
// trip beside the basis that makes it a measurement.
func TestTheVerdictIsKeyedByTheCommitItRanOn(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": passOn(baseCommit)}}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Commit != baseCommit {
		t.Errorf("the record is keyed by %q, want the commit the runner reported", observed.Commit)
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		t.Fatalf("marshal the observed verdict: %v", err)
	}
	var record struct {
		Commit string `json:"commit"`
		Basis  Basis  `json:"basis"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("re-read the observed verdict: %v", err)
	}
	if record.Commit != baseCommit {
		t.Errorf("the round-tripped record is keyed by %q, want %q", record.Commit, baseCommit)
	}
	if record.Basis != BasisObserved {
		t.Errorf("the round-tripped record carries basis %q, want %q", record.Basis, BasisObserved)
	}
}

// A runner that reports a run with NO commit is a violated seam, not a
// verdict: an observed record keyed by nothing says "once, at some point" —
// the timestamped opinion the key exists to prevent.
func TestARunnerReportingNoCommitIsRefused(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": passOn("")}}
	oracle := NewOracle(runner)
	_, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err == nil {
		t.Fatal("a run keyed by no commit was observed rather than refused")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("the refusal %q does not name the missing commit", err)
	}
}

// A verdict about two trees is not a verdict at all: the runner reporting
// different commits for different items mid-observation means the tree moved
// under the oracle, and the record would be about no one thing.
func TestARunnerReportingTwoCommitsIsRefused(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{
		"go": passOn(baseCommit),
		"ts": passOn("0123456789abcdef0123456789abcdef01234567"),
	}}
	oracle := NewOracle(runner)
	_, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go", "A3": "ts"}))
	if err == nil {
		t.Fatal("runs on two different commits were folded into one verdict rather than refused")
	}
	if !strings.Contains(err.Error(), "one tree") && !strings.Contains(err.Error(), "different commits") {
		t.Errorf("the refusal %q does not name that an observed verdict is about one tree", err)
	}
}

// A command that cannot run is NOT a fail and NOT a pass: it produced no
// evidence about the item at all, so the item comes back unresolved with the
// failure named — never assumed either way — while the items that did run
// still say what they saw.
func TestACommandThatCannotRunComesBackUnresolved(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		answers: map[string]Run{"go": passOn(baseCommit)},
		errs:    map[string]error{"ts": errRunnerDead},
	}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go", "A3": "ts"}))
	if err != nil {
		t.Fatalf("a command that could not run stopped the oracle (%v); it is unresolved, not a stop", err)
	}
	if observed == nil {
		t.Fatal("an observation with one item answered produced no verdict")
	}
	if observed.Gating {
		t.Fatalf("a passed item and an unrun one produced a gating verdict: %+v", observed.Verdict)
	}
	var unresolved *Unresolved
	for i := range observed.Unresolved {
		if observed.Unresolved[i].ItemID == "A3" {
			unresolved = &observed.Unresolved[i]
		}
	}
	if unresolved == nil {
		t.Fatalf("A3 is not in the unresolved items: %+v", observed.Unresolved)
	}
	if !strings.Contains(unresolved.Reason, "could not run") || !strings.Contains(unresolved.Reason, "the runner died") {
		t.Errorf("the unresolved reason for A3 (%q) does not name the failure", unresolved.Reason)
	}
	if !strings.Contains(observed.Reason, "A3") {
		t.Errorf("the verdict's reason %q does not name A3 among what it refuses to decide", observed.Reason)
	}
}

// A run that reports the gate's own `error` result — the command was killed,
// never answered — is the same epistemic state: no evidence about the item,
// unresolved, not assumed.
func TestAnErroredRunProducesNoVerdictForItsItem(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{
		"go": passOn(baseCommit),
		"ts": {Commit: baseCommit, Result: ResultError, Stderr: "the bound was reached"},
	}}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go", "A3": "ts"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed == nil {
		t.Fatal("Observe returned no verdict")
	}
	var hasA3 bool
	for _, item := range observed.Unresolved {
		if item.ItemID == "A3" {
			hasA3 = true
			if !strings.Contains(item.Reason, "produced no evidence") && !strings.Contains(item.Reason, "error") {
				t.Errorf("the unresolved reason for A3 does not name the error run: %q", item.Reason)
			}
		}
	}
	if !hasA3 {
		t.Fatalf("an errored run of A3 is neither observed nor unresolved: %+v", observed)
	}
	for _, item := range observed.Items {
		if item.ItemID == "A3" {
			t.Errorf("an errored run produced an observation for A3: %+v", item)
		}
	}
}

// When EVERY runnable command could not run, no observation exists at all and
// the oracle says so rather than handing back an empty verdict that reads as
// "nothing found": nothing was observed, and that is a named outcome.
func TestNothingObservedWhenNoCommandCouldRun(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{errs: map[string]error{"go": errRunnerDead}}
	oracle := NewOracle(runner)
	observed, nothing, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed != nil {
		t.Fatalf("an oracle that ran nothing produced a verdict: %+v", observed)
	}
	if nothing == "" {
		t.Fatal("an oracle that observed nothing produced no named reason")
	}
	if !strings.Contains(nothing, "the runner died") {
		t.Errorf("the reason %q does not name why nothing could run", nothing)
	}
}

// Where NO item is runnable, nothing is the oracle's to run — the same named
// handover the prediction half makes in the other direction — and the runner
// is never asked for anything.
func TestNothingToObserveWhenEveryItemIsTheClassifiers(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	oracle := NewOracle(runner)
	observed, nothing, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, nil))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed != nil {
		t.Fatalf("the oracle observed over an unverified done: %+v", observed)
	}
	if nothing == "" {
		t.Fatal("a done with no runnable items produced no named reason")
	}
	for _, want := range []string{"predict", "unverified"} {
		if !strings.Contains(nothing, want) {
			t.Errorf("the reason %q does not name %q", nothing, want)
		}
	}
	if len(runner.asks) != 0 {
		t.Errorf("the runner was asked for %v over unverified items, want never", runner.asks)
	}
}

// A done with no items never reaches a command: enumerating it is klq's
// refusal, and the oracle does not silently stand in for the refusal by
// observing nothing into a verdict.
func TestNothingToObserveWhenTheDoneCarriesNoItems(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	oracle := NewOracle(runner)
	observed, nothing, err := oracle.Observe(context.Background(), theFinding, acceptance.Done{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed != nil {
		t.Fatalf("an empty done was observed against: %+v", observed)
	}
	if nothing == "" {
		t.Fatal("an empty done produced no named reason")
	}
	if !strings.Contains(nothing, "refus") {
		t.Errorf("the reason %q does not name that the refusal owns this case, not an observation", nothing)
	}
	if len(runner.asks) != 0 {
		t.Errorf("the runner was asked for %v over no items, want never", runner.asks)
	}
}

// An oracle with no runner wired is a no-observation, never a stop and never
// a panic: the runnable items stay unresolved and the decision falls to the
// prediction tier's documented fallback, which errs toward absorbing — the
// safe direction by the stated asymmetry.
func TestAnOracleWithoutARunnerObservesNothing(t *testing.T) {
	t.Parallel()

	oracle := NewOracle(nil)
	observed, nothing, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("a nil runner returned an error (%v); observing nothing is a named outcome, not a stop", err)
	}
	if observed != nil {
		t.Fatalf("an oracle with no runner produced a verdict: %+v", observed)
	}
	if nothing == "" {
		t.Fatal("a nil runner produced no named reason")
	}
	if !strings.Contains(nothing, "no runner") {
		t.Errorf("the reason %q does not say no runner is configured", nothing)
	}
}

// Caller defects and violated seams are hard errors, not verdicts: a finding
// with no id cannot be keyed, a runner answering a result outside the gate's
// own vocabulary is a broken seam, and a cancelled context is a cancelled
// observation — guessing past any of them would be a decision wearing a
// defect's costume.
func TestCallerDefectsAndViolatedSeamsAreRefused(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": passOn(baseCommit)}}
	oracle := NewOracle(runner)
	if _, _, err := oracle.Observe(context.Background(), Finding{Title: "no id"}, doneOf(t, criteria, map[string]string{"A2": "go"})); err == nil {
		t.Error("a finding with no id was observed rather than refused")
	}
	if len(runner.asks) != 0 {
		t.Errorf("a caller defect made %d runner calls; defects are refused before the wire", len(runner.asks))
	}

	weird := &fakeRunner{answers: map[string]Run{"go": {Commit: baseCommit, Result: Result("maybe")}}}
	if _, _, err := NewOracle(weird).Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"})); err == nil {
		t.Error("a runner answering a result outside the gate's own vocabulary was observed rather than refused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := NewOracle(runner).Observe(ctx, theFinding, doneOf(t, criteria, map[string]string{"A2": "go"})); err == nil {
		t.Error("a cancelled context produced a verdict rather than a cancelled observation")
	}
}

// The evidence takes the SHAPE the gate's evidence record already takes —
// check, exit code, output mode inline with the gate's own bound — so an
// operator reading the run branch reads one shape, and output the command
// drowned the record in is truncated at that bound with the truncation said.
func TestTheEvidenceTakesTheGateEvidenceShape(t *testing.T) {
	t.Parallel()

	loud := strings.Repeat("x", maxInlineOutput*2)
	runner := &fakeRunner{answers: map[string]Run{"go": {
		Commit: baseCommit, Result: ResultPass, ExitCode: 0, Stdout: loud,
	}}}
	oracle := NewOracle(runner)
	observed, _, err := oracle.Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	evidence := observed.Items[0].Evidence
	if evidence.Output.Mode != "inline" {
		t.Errorf("the output mode is %q, want inline — the shape gate evidence takes", evidence.Output.Mode)
	}
	if evidence.Output.MaxBytes != maxInlineOutput {
		t.Errorf("the output's bound is %d, want the gate's own %d", evidence.Output.MaxBytes, maxInlineOutput)
	}
	if !evidence.Output.Truncated {
		t.Error("output past the bound was not marked truncated")
	}
	if len(evidence.Output.Stdout) != maxInlineOutput {
		t.Errorf("the recorded stdout is %d bytes, want it bounded at %d", len(evidence.Output.Stdout), maxInlineOutput)
	}

	raw, err := json.Marshal(observed)
	if err != nil {
		t.Fatalf("marshal the observed verdict: %v", err)
	}
	var record struct {
		Items []struct {
			Evidence struct {
				Check    struct{ ID, Kind string } `json:"check"`
				ExitCode int                       `json:"exit_code"`
				Result   string                    `json:"result"`
			} `json:"evidence"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("re-read the observed verdict: %v", err)
	}
	if len(record.Items) != 1 {
		t.Fatalf("the record carries %d items, want 1", len(record.Items))
	}
	if record.Items[0].Evidence.Check.ID != "go" || record.Items[0].Evidence.Check.Kind != "command" {
		t.Errorf("the evidence's check is %+v, want the command id and kind the gate's record spells", record.Items[0].Evidence.Check)
	}
	if record.Items[0].Evidence.ExitCode != 0 || record.Items[0].Evidence.Result != "pass" {
		t.Errorf("the evidence says exit=%d result=%q", record.Items[0].Evidence.ExitCode, record.Items[0].Evidence.Result)
	}
}

// An observation and a prediction about the same finding read as themselves
// through one round trip: the two halves of the two-tier decision share the
// one verdict shape, and no retro has to infer which half wrote a record.
func TestAnObservedRecordAndAPredictedRecordShareOneShape(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{answers: map[string]Run{"go": {
		Commit: baseCommit, Result: ResultFail, ExitCode: 1, Stderr: "FAIL",
	}}}
	observed, _, err := NewOracle(runner).Observe(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	predicted := Verdict{FindingID: findingID, Gating: true, ItemID: "A1", Basis: BasisPredicted}

	observedJSON, err := json.Marshal(observed)
	if err != nil {
		t.Fatalf("marshal the observed record: %v", err)
	}
	predictedJSON, err := json.Marshal(predicted)
	if err != nil {
		t.Fatalf("marshal the predicted record: %v", err)
	}
	var o, p struct {
		Basis Basis `json:"basis"`
	}
	if err := json.Unmarshal(observedJSON, &o); err != nil {
		t.Fatalf("re-read the observed record: %v", err)
	}
	if err := json.Unmarshal(predictedJSON, &p); err != nil {
		t.Fatalf("re-read the predicted record: %v", err)
	}
	if o.Basis != BasisObserved || p.Basis != BasisPredicted {
		t.Errorf("the records read bases %q and %q, want %q and %q", o.Basis, p.Basis, BasisObserved, BasisPredicted)
	}
	if strings.Contains(string(predictedJSON), string(BasisObserved)) {
		t.Errorf("the predicted record carries %q: a prediction is never recorded as observed\n%s", BasisObserved, predictedJSON)
	}
	if !strings.Contains(string(observedJSON), string(BasisObserved)) {
		t.Errorf("the observed record does not carry its basis:\n%s", observedJSON)
	}
}
