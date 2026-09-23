package reconcile

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The classification exchange (tick w9b, epic wne): Jev asked once per
// role-less tick, the answer — the full distribution and the model identity —
// written to the run branch as a decision record, and every later pass reading
// the record rather than re-asking.

// countingClassifier is the classifier seam's test double: it counts calls,
// remembers what it was asked, and answers from a function. The function is
// per tick id because the exchange asks one tick per call.
type countingClassifier struct {
	mu     sync.Mutex
	calls  int
	asked  []string
	answer func(tick string) jev.Result
}

func (c *countingClassifier) Classify(_ context.Context, ticks []jev.Tick) (jev.Result, error) {
	c.mu.Lock()
	c.calls++
	for _, one := range ticks {
		c.asked = append(c.asked, one.ID)
	}
	answer := c.answer
	c.mu.Unlock()
	if answer == nil {
		return jev.Result{}, nil
	}
	if len(ticks) == 0 {
		return answer(""), nil
	}
	return answer(ticks[0].ID), nil
}

func (c *countingClassifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingClassifier) askedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.asked...)
}

// classificationAnswer is the measured shape one answered tick takes: a full
// distribution over the enum and the model that gave it. The mass on the dear
// work types (design + diagnosis) is 0.45 — under the provisional 0.50
// threshold and over 0.40, so the record is the thing a threshold change is
// re-evaluated against, which is why it must carry the distribution.
func classificationAnswer(tick string) jev.Result {
	return jev.Result{
		Classifications: map[string]jev.Classification{tick: {
			TickID:     tick,
			Choice:     runconfig.WorkConstruction,
			Confidence: 0.42,
			Probabilities: map[runconfig.WorkType]float64{
				runconfig.WorkMechanical:   0.10,
				runconfig.WorkTranslation:  0.03,
				runconfig.WorkConstruction: 0.42,
				runconfig.WorkDiagnosis:    0.05,
				runconfig.WorkDesign:       0.40,
			},
		}},
		Model: "jev-1",
		Usage: jev.Usage{InputTokens: 1200, OutputTokens: 40, CostUSD: 0.00005},
	}
}

// classificationDecision builds the decision record a warm exchange would
// write for one tick, through the same shapes the exchange writes — so the
// reader's test reads what the writer's test wrote.
func classificationDecision(t *testing.T, number int, tick string, response classificationResponse) *runstate.Decision {
	t.Helper()
	responseMap, err := asRecordMap(response)
	if err != nil {
		t.Fatal(err)
	}
	tickID := tick
	return &runstate.Decision{
		SchemaVersion: runstate.SchemaVersion,
		Decision:      number, Role: runstate.RoleClassifyTick,
		Request:   map[string]any{"tick_id": tick, "epic_id": "qeu", "role": runstate.RoleClassifyTick},
		Response:  responseMap,
		Validated: true, RequestedAt: "2026-09-22T16:13:00Z", AnsweredAt: "2026-09-22T16:13:00Z",
		Provenance: ProvenanceForTest(tickID),
	}
}

// ProvenanceForTest is the provenance a classification carries: run, tick,
// the integration branch, phase worker — and role NULL, because no dispatch
// produced it and the role enum has no value for the classifier.
func ProvenanceForTest(tick string) runstate.Provenance {
	return runstate.Provenance{
		RunID: "r-fixture", TickID: &tick, Attempt: nil,
		SourceRef: "refs/heads/epic/qeu", SourceSHA: "acb08b9493dd8647918efbebac27079c64339946",
		IntegrationRef: runstate.Ptr("refs/heads/epic/qeu"), Phase: runstate.PhaseWorker,
		Executor: nil, WorkspaceID: nil, Backend: nil, Role: nil, Tier: nil,
		ProfileDigest: runstate.Ptr("sha256:profiles"), Model: runstate.Ptr("jev-1"),
		ContextManifestDigest: runstate.Ptr("sha256:gate"),
	}
}

// A recorded classification reads back as the FULL DISTRIBUTION it says it
// is — the payload the routing rule consumes, complete enough that a
// different threshold can be re-evaluated against the record alone — and the
// reader refuses every shape that is not one coherent answer, because a
// record two readers disagree about is not evidence.
// short: pure record round-trips, no repository is built
func TestARecordedClassificationReReadsItsFullDistribution(t *testing.T) {
	t.Parallel()
	decision := classificationDecision(t, 1, "a1", classificationResponse{
		Choice: "construction", Confidence: 0.42,
		Probabilities: map[string]float64{
			"mechanical": 0.10, "translation": 0.03, "construction": 0.42, "diagnosis": 0.05, "design": 0.40,
		},
		Model: "jev-1", Usage: jev.Usage{InputTokens: 1200, OutputTokens: 40, CostUSD: 0.00005},
	})
	if decision.Validate() != nil {
		t.Fatalf("a classification decision record does not validate: %v", decision.Validate())
	}
	if classificationTickOf(decision) != "a1" {
		t.Fatalf("the record is about tick %q", classificationTickOf(decision))
	}

	read, err := classificationOfDecision(decision)
	if err != nil {
		t.Fatalf("the record does not read back as a classification: %v", err)
	}
	want := map[runconfig.WorkType]float64{
		runconfig.WorkMechanical:   0.10,
		runconfig.WorkTranslation:  0.03,
		runconfig.WorkConstruction: 0.42,
		runconfig.WorkDiagnosis:    0.05,
		runconfig.WorkDesign:       0.40,
	}
	if !reflect.DeepEqual(read.Probabilities, want) {
		t.Errorf("the distribution did not survive the record:\n  want %v\n  got  %v", want, read.Probabilities)
	}
	// The threshold re-evaluation the record exists for: the mass on the dear
	// work types is readable from the record alone, and 0.45 routes dear at a
	// 0.40 threshold and cheap at 0.50 — a record that could only say
	// "construction" could answer neither.
	dear := read.Probabilities[runconfig.WorkDesign] + read.Probabilities[runconfig.WorkDiagnosis]
	if dear != 0.45 {
		t.Errorf("the mass on the dear work types is %v, want 0.45", dear)
	}
	if read.Choice != runconfig.WorkConstruction || read.Confidence != 0.42 || read.Model != "jev-1" {
		t.Errorf("the record lost its choice, confidence or model identity: %+v", read)
	}
}

// A no-answer is recorded like an answer — the exchange happened once, and its
// OUTCOME is what a cold re-derivation needs to reproduce the same fallback.
// short: pure record round-trips, no repository is built
func TestARecordedNoAnswerReadsBackAsTheReasonItIs(t *testing.T) {
	t.Parallel()
	read, err := classificationOfDecision(classificationDecision(t, 2, "b1", classificationResponse{
		Model: "", NoAnswer: "the classifier could not be reached: connection refused",
	}))
	if err != nil {
		t.Fatalf("a recorded no-answer did not read back: %v", err)
	}
	if read.NoAnswer == "" || read.Probabilities != nil || read.Choice != "" {
		t.Errorf("the no-answer read back as %+v", read)
	}
}

// short: pure record round-trips, no repository is built
func TestClassificationRecordsThatAreNotOneCoherentAnswerAreRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		why   string
		shape classificationResponse
	}{
		{"no distribution and no no-answer", classificationResponse{Model: "jev-1"}},
		{"both a distribution and a no-answer", classificationResponse{
			Model: "jev-1", NoAnswer: "unreachable",
			Probabilities: map[string]float64{"design": 1.0}, Choice: "design"}},
		{"a probability off the closed enum", classificationResponse{
			Model: "jev-1", Choice: "design", Probabilities: map[string]float64{"design": 0.9, "medium-high": 0.1}}},
		{"a choice off the closed enum", classificationResponse{
			Model: "jev-1", Choice: "claude-opus-5", Probabilities: map[string]float64{"design": 1.0}}},
		{"no model identity", classificationResponse{
			Choice: "design", Probabilities: map[string]float64{"design": 1.0}}},
	}
	for _, tc := range cases {
		if _, err := classificationOfDecision(classificationDecision(t, 1, "a1", tc.shape)); err == nil {
			t.Errorf("the record with %s was accepted", tc.why)
		}
	}

	// A response the reader does not know — a shape from a future schema — is
	// refused rather than partially read, the same strictness the role
	// exchange holds its envelope to.
	d := classificationDecision(t, 1, "a1", classificationResponse{Model: "jev-1", Choice: "design",
		Probabilities: map[string]float64{"design": 1.0}})
	d.Response["verdict"] = "proceed"
	if _, err := classificationOfDecision(d); err == nil {
		t.Error("a response carrying a field this reader does not know was accepted")
	}

	// And a record of another exchange is not a classification, whatever its
	// response says.
	role := classificationDecision(t, 1, "a1", classificationResponse{Model: "jev-1", Choice: "design",
		Probabilities: map[string]float64{"design": 1.0}})
	role.Role = "review-epic"
	if _, err := classificationOfDecision(role); err == nil {
		t.Error("a role-job decision read back as a classification")
	}
}

// wireClassification prepares a reconciler for the exchange WITHOUT running
// the epic: the branch the records land on, the store that lands them, and
// the tracker the tick text is read from — the same three Run sets up.
func wireClassification(t *testing.T, r *Reconciler, f *fixture) {
	t.Helper()
	base, err := r.git.ensureRemoteBranch(r.branch, r.opts.BaseRef)
	if err != nil {
		t.Fatalf("prepare the integration branch %s: %v", r.branch, err)
	}
	r.base, r.baseRef = base, refFor(r.branch)
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.branch, RunID: r.runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.store = store
	r.tracker = f.Tracker.In(f.Repo.Dir)
	if _, err := r.store.Fetch(); err != nil {
		t.Fatal(err)
	}
}

// The exchange, asked from cold: a role-less tick is asked once, the answer
// lands on the run branch as a decision record, and the SAME record — read
// through a fresh store on a fresh clone, with the classifier absent — is what
// a cold re-derivation sees. A role-carrying tick is never sent at all.
func TestClassificationIsAskedOnceAndLandsOnTheRunBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	classifier := &countingClassifier{answer: classificationAnswer}
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, r, f)

	entry := planEntry{TickID: "a1", Role: "implement-tick"}
	classification, err := r.classificationFor(context.Background(), entry, true)
	if err != nil {
		t.Fatalf("the exchange refused a role-less tick: %v", err)
	}
	if classification == nil {
		t.Fatal("a role-less tick with a configured classifier got no classification")
	}
	if classifier.count() != 1 {
		t.Fatalf("the classifier was called %d times for one tick", classifier.count())
	}
	if classification.Choice != runconfig.WorkConstruction || classification.Model != "jev-1" {
		t.Errorf("the classification read back as %+v", classification)
	}

	// Role-carrying ticks answer (nil, nil) and are never sent: rv is review,
	// co is closeout, and the enum has no right answer for either.
	for _, role := range []string{"review-epic", "closeout-epic"} {
		answer, err := r.classificationFor(context.Background(), planEntry{TickID: "rv", Role: role}, true)
		if err != nil || answer != nil {
			t.Errorf("a %s tick was classified (%v, %v)", role, answer, err)
		}
	}
	if ids := classifier.askedIDs(); strings.Join(ids, ",") != "a1" {
		t.Errorf("the classifier was asked %v; the role ticks must never be sent", ids)
	}

	// The record is on the run branch, where a cold re-derivation reads it:
	// a FRESH CLONE, a fresh store, and no classifier at all.
	cold := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "cold"))
	coldStore, err := runstate.Open(runstate.Options{
		Repo: cold.Dir, Remote: "origin", Branch: r.branch, RunID: r.runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coldStore.Fetch(); err != nil {
		t.Fatal(err)
	}
	decisions, err := coldStore.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("%d decisions on the cold run branch, want the one classification", len(decisions))
	}
	record := decisions[0]
	if record.Role != runstate.RoleClassifyTick || classificationTickOf(&record) != "a1" {
		t.Fatalf("the decision on the run branch is %+v", record)
	}
	if record.Provenance.Role != nil {
		t.Errorf("the classification's provenance claims role %q: no dispatch produced it, and the role enum has no value for the classifier", *record.Provenance.Role)
	}
	if record.Provenance.Model == nil || *record.Provenance.Model != "jev-1" {
		t.Error("the record's provenance names no model identity")
	}
	// The request carried the tick's own text, so a person reading the record
	// later sees what was asked without resolving the tick id elsewhere.
	if title, _ := record.Request["title"].(string); title != "tick a1" {
		t.Errorf("the request carries title %q", title)
	}
	// The response's distribution, re-read through the reader a cold
	// re-derivation uses, is the full distribution the warm process got.
	coldRead, err := classificationOfDecision(&record)
	if err != nil {
		t.Fatalf("the record on the cold run branch does not read back: %v", err)
	}
	if len(coldRead.Probabilities) != len(classification.Probabilities) {
		t.Errorf("the cold record carries %d probabilities, the warm answer carried %d",
			len(coldRead.Probabilities), len(classification.Probabilities))
	}
	for workType, mass := range classification.Probabilities {
		if coldRead.Probabilities[workType] != mass {
			t.Errorf("the cold record says %s is %v, the warm answer said %v", workType, coldRead.Probabilities[workType], mass)
		}
	}
}

// The once of "asked once per tick": a later pass — and a cold re-derivation —
// READ the record instead of calling the classifier, whether or not that pass
// could ask.
func TestALaterPassReadsTheRecordInsteadOfReAsking(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	classifier := &countingClassifier{answer: classificationAnswer}
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, r, f)

	entry := planEntry{TickID: "a1", Role: "implement-tick"}
	warm, err := r.classificationFor(context.Background(), entry, true)
	if err != nil {
		t.Fatal(err)
	}
	if classifier.count() != 1 {
		t.Fatalf("the warm pass called the classifier %d times", classifier.count())
	}

	// The warm process reads its own record on the second attempt — a later
	// dispatch (firstDispatch false), which is the shape the sj2 gate must not
	// turn into a skip: the record is read wherever the attempt sits.
	again, err := r.classificationFor(context.Background(), entry, false)
	if err != nil {
		t.Fatal(err)
	}
	if classifier.count() != 1 {
		t.Fatalf("the second pass re-asked the classifier (%d calls)", classifier.count())
	}
	if !reflect.DeepEqual(warm, again) {
		t.Errorf("the second pass read a different classification: %v then %v", warm, again)
	}

	// A cold incarnation — a fresh reconciler under the same run id, pointed
	// at the same origin — reads the record and asks nothing, even though its
	// classifier is configured and willing.
	coldOpts := f.options(f.Repo, fixtureOptions{})
	coldClassifier := &countingClassifier{answer: classificationAnswer}
	coldOpts.Classifier = coldClassifier
	cold, err := New(coldOpts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, cold, f)
	read, err := cold.classificationFor(context.Background(), entry, false)
	if err != nil {
		t.Fatalf("the cold pass was refused reading the record: %v", err)
	}
	if coldClassifier.count() != 0 {
		t.Errorf("the cold pass re-asked a model the run already paid (%d calls)", coldClassifier.count())
	}
	if !reflect.DeepEqual(read, warm) {
		t.Errorf("the cold pass reached a different classification than the warm process:\n  warm %v\n  cold %v", warm, read)
	}
}

// Degradation, not failure: an unreachable, refused or never-configured
// classifier is a recorded no-answer, the run routes at the start policy
// exactly as it did before the exchange existed — and the no-answer is a
// RECORD, so a later pass falls back for the same reason the warm process did
// rather than re-asking and maybe getting a different answer.
func TestAnUnavailableClassifierRecordsItsNoAnswerAndNothingReAsksIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	unavailable := func(string) jev.Result {
		return jev.Result{Unavailable: "the classifier could not be reached: connection refused"}
	}
	classifier := &countingClassifier{answer: unavailable}
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, r, f)

	entry := planEntry{TickID: "a1", Role: "implement-tick"}
	answer, err := r.classificationFor(context.Background(), entry, true)
	if err != nil {
		t.Fatalf("an unavailable classifier stopped the run: %v", err)
	}
	if answer == nil || answer.NoAnswer == "" {
		t.Fatalf("the unavailable classifier was not a recorded no-answer: %+v", answer)
	}
	if classifier.count() != 1 {
		t.Fatalf("the classifier was called %d times", classifier.count())
	}

	// The second pass — a later dispatch — reads the no-answer and asks nothing.
	again, err := r.classificationFor(context.Background(), entry, false)
	if err != nil {
		t.Fatal(err)
	}
	if classifier.count() != 1 {
		t.Errorf("the second pass re-asked an unavailable classifier (%d calls)", classifier.count())
	}
	if again == nil || again.NoAnswer == "" || again.Probabilities != nil {
		t.Errorf("the second pass read a different outcome: %+v", again)
	}

	// A cold incarnation with NO classifier at all still reads the record —
	// the record is the answer, not the capability to ask.
	coldOpts := f.options(f.Repo, fixtureOptions{})
	cold, err := New(coldOpts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, cold, f)
	read, err := cold.classificationFor(context.Background(), entry, false)
	if err != nil {
		t.Fatal(err)
	}
	if read == nil || read.NoAnswer == "" {
		t.Errorf("an incarnation with no classifier did not read the recorded no-answer: %+v", read)
	}

	// And a tick with no record and no classifier classifies nothing at all:
	// the run degrades to the start policy, which is today's behaviour.
	absent, err := cold.classificationFor(context.Background(), planEntry{TickID: "b1", Role: "implement-tick"}, true)
	if err != nil || absent != nil {
		t.Errorf("an unconfigured run classified anyway: (%v, %v)", absent, err)
	}
}

// A record that exists but cannot be read back is a refusal, never a re-ask:
// re-asking would make a cold re-derivation reach a different answer than the
// warm process — an axiom 1 violation wearing the costume of a cache miss.
func TestAnUnreadableClassificationRecordIsRefusedNeverReAsked(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	classifier := &countingClassifier{answer: classificationAnswer}
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, r, f)

	// A record for a1 that exists on the run branch but says nothing this
	// reader recognises — a response with no distribution and no no-answer.
	tickID := "a1"
	responseMap, err := asRecordMap(map[string]any{"verdict": "proceed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.PutDecision(runstate.Decision{
		Decision: 1, Role: runstate.RoleClassifyTick,
		Request:   map[string]any{"tick_id": "a1", "epic_id": r.opts.EpicID, "role": runstate.RoleClassifyTick},
		Response:  responseMap,
		Validated: true, RequestedAt: "2026-09-22T16:13:00Z", AnsweredAt: "2026-09-22T16:13:00Z",
		Provenance: ProvenanceForTest(tickID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Fetch(); err != nil {
		t.Fatal(err)
	}

	_, err = r.classificationFor(context.Background(), planEntry{TickID: "a1", Role: "implement-tick"}, true)
	if err == nil {
		t.Fatal("an unreadable classification record was walked around")
	}
	var refusal *Refusal
	if !asRefusal(err, &refusal) || refusal.Reason != RefusedClassification {
		t.Fatalf("the unreadable record was not a classification refusal: %v", err)
	}
	if classifier.count() != 0 {
		t.Errorf("the classifier was re-asked %d times over an unreadable record: the record is the authority, not the cache", classifier.count())
	}
}

// The exchange rides a real run: every role-less tick of the plan is
// classified once at its first dispatch, the records land on the run branch
// beside the run's own decisions, the role ticks are never asked, and the run
// still finishes.
func TestAWholeRunClassifiesEveryRoleLessTickOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	classifier := &countingClassifier{answer: classificationAnswer}
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	// Asked exactly once per role-less tick — a1, a2, b1 — and never for the
	// role ticks rv and co.
	asked := classifier.askedIDs()
	sort.Strings(asked)
	if strings.Join(asked, ",") != "a1,a2,b1" {
		t.Errorf("the classifier was asked %v; want a1, a2 and b1, each once", asked)
	}
	if classifier.count() != 3 {
		t.Errorf("the classifier was called %d times for three role-less ticks", classifier.count())
	}

	// Three classification records on the run branch, and the journal said
	// so where a person reading the run finds it.
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.IntegrationBranch(), RunID: r.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	classified := 0
	for i := range decisions {
		if decisions[i].Role == runstate.RoleClassifyTick {
			classified++
			if _, err := classificationOfDecision(&decisions[i]); err != nil {
				t.Errorf("the classification record for %s does not read back: %v", classificationTickOf(&decisions[i]), err)
			}
		}
	}
	if classified != 3 {
		t.Errorf("%d classification records on the run branch, want three", classified)
	}
	var stages []string
	for _, event := range r.Journal() {
		if event.Stage == StageClassified {
			stages = append(stages, event.Tick)
		}
	}
	sort.Strings(stages)
	if strings.Join(stages, ",") != "a1,a2,b1" {
		t.Errorf("the journal says %v were classified, want a1, a2, b1", stages)
	}
}

// THE FIRST-DISPATCH BOUND (tick sj2), at the exchange itself: a tick whose
// first dispatch is past and whose record does not exist is never asked. The
// first attempt was planned under nothing, and asking after the fact would
// make the re-derivation of this very run dispatch the later attempt on an
// answer the first attempt never had — an axiom 1 violation wearing the
// costume of a cache miss. Nothing is asked and nothing is recorded; the
// bound is the dispatch's position, not the classifier's willingness.
func TestALaterAttemptWithNoRecordIsNeverAsked(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	classifier := &countingClassifier{answer: classificationAnswer}
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	wireClassification(t, r, f)

	// A later attempt — the tick's first dispatch is past — with a
	// configured, willing classifier and no record: no classification.
	entry := planEntry{TickID: "a1", Role: "implement-tick"}
	answer, err := r.classificationFor(context.Background(), entry, false)
	if err != nil {
		t.Fatalf("the later attempt was refused rather than degraded: %v", err)
	}
	if answer != nil {
		t.Fatalf("a later attempt with no record classified anyway: %+v", answer)
	}
	if calls := classifier.count(); calls != 0 {
		t.Fatalf("the classifier was called %d time(s) by a later attempt: asking past the first dispatch would "+
			"make the re-derivation of this run reach a different dispatch than the run it reconstructs", calls)
	}
	// Nothing was recorded either: no exchange happened, so the run branch
	// carries nothing that would let a re-derivation read an answer the first
	// attempt never had.
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("%d decision(s) landed on the run branch for an exchange that never happened", len(decisions))
	}

	// The same tick at its first dispatch IS asked — the bound is the
	// dispatch's position, not a suppression of the exchange itself.
	first, err := r.classificationFor(context.Background(), entry, true)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || classifier.count() != 1 {
		t.Fatalf("the first dispatch was not asked: %+v after %d call(s)", first, classifier.count())
	}
}

// THE BOUND, END TO END through the real dispatch path: a tick whose first
// dispatch happened under NO classifier — so no record exists — is
// redispatched by a later incarnation that HAS one, and the later attempt
// neither asks nor records: it routes at the start policy exactly as its
// first attempt did. Ticks whose first dispatch is still ahead (a2, then b1)
// are asked as always, so the bound suppresses only the past. On the old
// exchange the redispatch would have asked, answered 0.55 of dear mass, and
// started a rung above the dear tier — a dispatch the re-derivation of this
// run could never reproduce, because the first attempt it reconstructs
// beside it had no answer at all.
func TestALaterAttemptWithNoClassificationRecordFallsBackWithoutAsking(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: massGate, mode: "blocked-first"})
	// a1's answer WOULD clear the mass threshold (0.55) — the point is that
	// the later attempt never sends it, not that it could not answer.
	answer := func(tick string) jev.Result {
		if tick == "a1" {
			return massAnswer(tick, runconfig.WorkDesign, distributionOf(0.02, 0.03, 0.40, 0.10, 0.45))
		}
		return massAnswer(tick, runconfig.WorkConstruction, distributionOf(0.05, 0.05, 0.60, 0.00, 0.30))
	}

	// Incarnation one: no classifier configured. a1's own first try blocks
	// with nothing committed, the run rejects it and stops — a1 was dispatched
	// under nothing, and no classification record exists.
	first, firstResult, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the first run: %v", err)
	}
	if firstResult.State != runstate.StateFailed || firstResult.Failure == nil || firstResult.Failure.TickID != "a1" {
		t.Fatalf("the first run ended %s (%+v), want a1's blocked first try to stop it",
			firstResult.State, firstResult.Failure)
	}
	if got := markerTierOfTry(t, first, "a1", 1); got != "economy" {
		t.Errorf("a1's first attempt recorded tier %q, want economy: with no classifier the start policy is the fallback", got)
	}

	// Incarnation two: a fresh clone, the same run id, and a classifier that
	// would answer — to prove the redispatch is never allowed to ask.
	clone := cloneRepo(t, f.Repo.Origin, f.Root+"/restarted")
	laterClassifier := &countingClassifier{answer: answer}
	laterOpts := f.options(clone, fixtureOptions{})
	laterOpts.Classifier = laterClassifier
	later, err := New(laterOpts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := later.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the second run: %v", err)
	}
	if second.State != runstate.StateFailed || second.Failure == nil || second.Failure.TickID != "a2" {
		t.Fatalf("the second run ended %s (%+v), want a2's own first try to stop it after a1 settled",
			second.State, second.Failure)
	}

	// a1's redispatch was never asked — a2's own first dispatch was, and it
	// is the only ask of the incarnation.
	if ids := laterClassifier.askedIDs(); strings.Join(ids, ",") != "a2" {
		t.Fatalf("the second run asked the classifier for %v; want a2 only — a1's redispatch has no record and "+
			"must not ask", ids)
	}
	// The redispatch routed at the START POLICY the first attempt fell back
	// to, one rung up the ladder for the failed attempt — NOT at the dear
	// tier a rung above the one its unsent answer would have started.
	if got := markerTierOfTry(t, later, "a1", 2); got != "balanced" {
		t.Errorf("a1's redispatch recorded tier %q, want balanced: the policy default escalated one rung — "+
			"the fallback, not the mass rule", got)
	}
	detail, ok := journalLine(later, "a1", StageTierDerived)
	if !ok {
		t.Fatal("a1's redispatch recorded no tier derivation")
	}
	if !strings.Contains(detail, "the policy default") || !strings.Contains(detail, "escalated 1 rung(s) after 1 failed attempt(s)") {
		t.Errorf("a1's redispatch derivation does not name the policy start it fell back to: %q", detail)
	}
	if _, classified := journalLine(later, "a1", StageClassified); classified {
		t.Error("a1 was classified at a later attempt: the journal says the exchange ran after the first dispatch")
	}
	// No record landed either — the run branch carries no classification for
	// a1 that a re-derivation could mistake for an answer the first attempt
	// had.
	if _, err := later.store.Fetch(); err != nil {
		t.Fatal(err)
	}
	decisions, err := later.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for i := range decisions {
		if decisions[i].Role == runstate.RoleClassifyTick && classificationTickOf(&decisions[i]) == "a1" {
			t.Errorf("a classification record for a1 is on the run branch, written by an attempt past the first dispatch")
		}
	}

	// The settling run: report mode finishes the epic. a2's redispatch reads
	// the record its own FIRST try wrote (never re-asked), and b1's first
	// dispatch is asked live — the first dispatch of a tick is still the
	// exchange's home, whatever happened to another tick's past.
	f.Runner = fakeRunnerArgv(t, "report")
	settleClassifier := &countingClassifier{answer: answer}
	settleOpts := f.options(clone, fixtureOptions{})
	settleOpts.Classifier = settleClassifier
	settled, err := New(settleOpts)
	if err != nil {
		t.Fatal(err)
	}
	done, err := settled.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the settling run: %v", err)
	}
	if done.State != runstate.StateCompleted {
		t.Fatalf("the settling run ended %s: %s", done.State, done.Reason)
	}
	if ids := settleClassifier.askedIDs(); strings.Join(ids, ",") != "b1" {
		t.Errorf("the settling run asked the classifier for %v; want b1 only — a2 reads the record its own first try wrote", ids)
	}
	// a1 closed on work dispatched under the fallback, never on an answer a
	// later attempt bought behind the run's back.
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Errorf("a1 closed %d time(s), want 1", got)
	}
}
