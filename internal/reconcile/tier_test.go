package reconcile

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The tier derivation, end to end through the real dispatch path (tick 5eq):
// the declared policy in the target repo's runners.toml, the tick's facts
// from the tracker, the derived tier on the dispatch marker origin carries,
// the label override and its loud refusal, and the escalation a failed
// attempt earns across a restart.
//
// The gate under test declares the whole ladder the operator's av8 notes
// describe in miniature: a default, a ceiling one rung above it, and an
// economy tier for the overrides and the cheap work. The models are the
// fixture's own — what is asserted is WHICH tier was derived, and the marker
// is where that answer is durable.

const tierGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.economy]
model = "haiku"

[roles.implement.tiers.balanced]
model = "sonnet"

[roles.implement.tiers.strong]
model = "opus"

[tier_policy]
default = "balanced"
ceiling = "strong"

[tier_policy.rate_limit]
response = "backoff-and-retry"
max_attempts = 8
max_delay_ms = 90000

[tier_policy.concurrency]
economy = 4
balanced = 2
strong = 1

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// retick edits one tick in the fake tracker's own state file — the way a
// person edits a tick before a run dispatches it.
func (f *fixture) retick(t *testing.T, id string, apply func(*tk.Tick)) {
	t.Helper()
	f.Tracker.mu.Lock()
	defer f.Tracker.mu.Unlock()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	tick := state.Ticks[id]
	apply(&tick)
	state.Ticks[id] = tick
	if err := f.Tracker.save(state); err != nil {
		t.Fatal(err)
	}
}

// markerTier reads the tier one attempt's marker on origin records, so the
// assertion is about the DURABLE record and not this incarnation's memory.
// markerTierOfTry is the tier recorded on a tick's Nth TRY, where the tries of
// one tick are its attempts in the order they were dispatched.
//
// A try is not an attempt number. Attempt numbers are run-wide — they count the
// run's dispatches, which is what makes them unique and what the branch, the
// marker and `ticfac settle` are named by — so which number a tick's second try
// happens to get depends on how many OTHER dispatches the run made first. Once
// the run dispatches a window of ticks rather than one at a time, that is no
// longer a fixed offset, and a test that hardcoded "attempt 2" was pinning an
// artifact of running one tick at a time rather than the ladder it is about.
func markerTierOfTry(t *testing.T, r *Reconciler, tickID string, try int) string {
	t.Helper()
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	var mine []runstate.Attempt
	for _, record := range attempts {
		if record.TickID == tickID {
			mine = append(mine, record)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Attempt < mine[j].Attempt })
	if try < 1 || try > len(mine) {
		t.Fatalf("%s has %d attempt(s) on origin, want at least %d", tickID, len(mine), try)
	}
	tier, _ := mine[try-1].JobHandle["tier"].(string)
	return tier
}

func markerTier(t *testing.T, r *Reconciler, tickID string, attempt int) string {
	t.Helper()
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range attempts {
		if record.TickID == tickID && record.Attempt == attempt {
			tier, _ := record.JobHandle["tier"].(string)
			return tier
		}
	}
	t.Fatalf("no attempt %d of %s is recorded on origin", attempt, tickID)
	return ""
}

func journalLine(r *Reconciler, tick, stage string) (string, bool) {
	for _, event := range r.Journal() {
		if event.Tick == tick && event.Stage == stage {
			return event.Detail, true
		}
	}
	return "", false
}

// Every dispatch records its tier — on the marker origin carries, in the
// profile the dispatch was built from, and in the run's own journal — and a
// tick with a label overrides the default while a tick without one takes it.
func TestEveryDispatchRecordsItsDerivedTier(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"chore", "tier:economy"} })

	r, result, err := f.run(f.Repo, fixtureOptions{budget: 10, ceiling: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	// The label overrode the default, and the dispatch carried it.
	dispatch := f.dispatch("a1")
	if dispatch.Profile == nil || dispatch.Profile.Provenance.Tier != "economy" {
		t.Fatalf("a1 dispatched under %+v, want the economy tier overlay", dispatch.Profile)
	}
	if dispatch.Profile.Model != "haiku" {
		t.Errorf("the economy overlay routed the model %q, want haiku", dispatch.Profile.Model)
	}
	if got := markerTier(t, r, "a1", 1); got != "economy" {
		t.Errorf("the marker on origin records tier %q, want economy", got)
	}

	// The tick without a label took the default.
	if dispatch := f.dispatch("b1"); dispatch.Profile == nil || dispatch.Profile.Provenance.Tier != "balanced" {
		t.Errorf("b1 dispatched under %+v, want the default balanced tier", dispatch.Profile)
	}
	if got := markerTier(t, r, "b1", 3); got != "balanced" {
		t.Errorf("the marker on origin records tier %q for b1, want balanced", got)
	}

	// The derivation is IN the journal, with the budget stated beside it —
	// the two numbers an operator auditing an expense read together.
	detail, ok := journalLine(r, "a1", StageTierDerived)
	if !ok {
		t.Fatal("the dispatch recorded no tier derivation")
	}
	if !strings.Contains(detail, `tier "economy"`) || !strings.Contains(detail, "label") {
		t.Errorf("the derivation record does not name the tier and why: %q", detail)
	}
	if !strings.Contains(detail, "the effective budget $5.00") {
		t.Errorf("the derivation record does not state the clamped budget beside the tier: %q", detail)
	}

	// The run-level stance lines: the rate-limit answer, and the wave the
	// per-tier bounds narrowed.
	var rateLimit, narrowed bool
	for _, event := range r.Journal() {
		if strings.Contains(event.Detail, "backing off and retrying (up to 8 attempts, at most 90000ms apart)") {
			rateLimit = true
		}
		if strings.Contains(event.Detail, "wave 1 routes 2 dispatch(es)") &&
			(strings.Contains(event.Detail, "narrows to 2") || strings.Contains(event.Detail, "cap it at 2")) {
			narrowed = true
		}
	}
	if !rateLimit {
		t.Error("the run never said what it does on a provider rate limit")
	}
	if !narrowed {
		t.Error("the run never said the per-tier bounds narrow its wave width")
	}

	// And those stance lines must not END the run. They were written as
	// run_finished, so a run declaring a tier policy told its feed subscribers
	// it had finished at admission. The feed test's exactly-one-terminal-line
	// assertion never saw it because its fixture declares no policy; this
	// fixture does.
	journal := r.Journal()
	var terminal []int
	for i, event := range journal {
		if event.Stage == StageRunFinished {
			terminal = append(terminal, i)
		}
	}
	if len(terminal) != 1 || terminal[0] != len(journal)-1 {
		t.Errorf("a run with a tier policy has run_finished at %v of %d journal lines, want exactly one, last: "+
			"a policy statement is not the run ending", terminal, len(journal))
	}
}

// An unrecognised tier label is refused loudly — naming the tick and the
// label — and refused BEFORE anything is claimed or recorded: a weakly typed
// field the tracker cannot validate is a field whose typo can only ever
// surface here, so the refusal must not also spend an attempt on it.
func TestAnUnrecognisedTierLabelIsRefusedLoudlyBeforeAnythingIsClaimed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"tier:premium"} })

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s, want a refusal", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedTierLabel {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedTierLabel)
	}
	if !strings.Contains(result.Failure.Message, "a1") || !strings.Contains(result.Failure.Message, "tier:premium") {
		t.Errorf("the refusal does not name the tick and the label: %q", result.Failure.Message)
	}

	// Nothing was spent: no attempt marker, no claim.
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range attempts {
		if record.TickID == "a1" {
			t.Errorf("an unrecognised label still recorded attempt %d", record.Attempt)
		}
	}
	if got := f.Tracker.count("claim:a1"); got != 0 {
		t.Errorf("a tick with an unrecognised tier label was claimed %d time(s)", got)
	}
}

// The ladder, earned the only way a rung can be earned: a first attempt at
// the default, a failed attempt, and a redispatch that derives one rung
// higher — recorded on the NEW attempt's marker, with the old one still
// saying what it used.
func TestAFailedAttemptEarnsTheNextRung(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate, mode: "blocked-first"})
	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first run ended %s, want the blocked first attempt to stop it: %s",
			result.State, result.Reason)
	}

	// The restart: a fresh clone holding nothing but what is on origin, the
	// same run id — and a redispatch of a1 that derives from the FAILURE, not
	// from a hunch.
	clone := cloneRepo(t, f.Repo.Origin, f.Root+"/restarted")
	restarted, result, err := f.run(clone, fixtureOptions{mode: "blocked-first"})
	if err != nil {
		t.Fatal(err)
	}
	// blocked-first keys on the tick's OWN first try (tick vw0), so the
	// restart settles a1's redispatch — the ladder under test — and then
	// dispatches a2 for the first time, whose own first try blocks exactly as
	// a1's did and refuses the run again. The assertions below read the
	// markers that landed; the restart completing was never one of them.
	if result.State != runstate.StateFailed {
		t.Fatalf("the restart ended %s, want a2's own first try to refuse it after a1's redispatch: %s",
			result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.TickID != "a2" {
		t.Fatalf("the restart's refusal is %+v, want a2's first try", result.Failure)
	}

	if got := markerTierOfTry(t, restarted, "a1", 1); got != "balanced" {
		t.Errorf("the first attempt's marker records tier %q, want balanced: the first attempt starts at the default", got)
	}
	if got := markerTierOfTry(t, restarted, "a1", 2); got != "strong" {
		t.Errorf("the redispatch's marker records tier %q, want strong: one failed attempt earns one rung", got)
	}
	dispatch := f.dispatch("a1")
	if dispatch.Profile == nil || dispatch.Profile.Model != "opus" {
		t.Errorf("the redispatch routed the model %q, want the strong tier's", dispatch.Profile.Model)
	}
	detail, ok := journalLine(restarted, "a1", StageTierDerived)
	if !ok {
		t.Fatal("the redispatch recorded no tier derivation")
	}
	if !strings.Contains(detail, "escalated 1 rung(s) after 1 failed attempt(s)") {
		t.Errorf("the derivation record does not name the rung and what earned it: %q", detail)
	}

	// The rest of the run never escalated: a2's first attempt is at the
	// default, because it did not fail anything.
	if got := markerTierOfTry(t, restarted, "a2", 1); got != "balanced" {
		t.Errorf("a2's first attempt recorded tier %q, want balanced: a rung is earned, never assumed", got)
	}
	// a2's refused first try left nothing, so one more run in plain report
	// mode settles the rest of the epic. The role jobs ran at base values:
	// no route was declared for them, and a process role is never loaned the
	// work default.
	f.Runner = fakeRunnerArgv(t, "report")
	settled, done, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done.State != runstate.StateCompleted {
		t.Fatalf("the settling run ended %s: %s", done.State, done.Reason)
	}
	if got := markerTierOfTry(t, settled, "rv", 1); got != "" {
		t.Errorf("the review job recorded tier %q, want none: no route is declared for it", got)
	}
}

// A repository with no policy runs exactly as it did before this tick: every
// dispatch at the role's base values, no tier recorded, nothing starting high.
func TestNoPolicyMeansBaseValuesEndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{}) // the stock passingGate: no [tier_policy]

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if dispatch := f.dispatch("a1"); dispatch.Profile == nil || dispatch.Profile.Provenance.Tier != "" {
		t.Errorf("a1 dispatched under %+v, want the base profile with no overlay", dispatch.Profile)
	}
	if got := markerTier(t, r, "a1", 1); got != "" {
		t.Errorf("the marker records tier %q, want none", got)
	}
	if _, ok := journalLine(r, "a1", StageTierDerived); !ok {
		t.Error("even a base-values dispatch should say so in the run's record")
	}
}

// The tier is in the CLOSED provenance object origin carries (bundle 4.1.1),
// not only on the marker's open handle: the attempt record and the gate's
// evidence — the two durable records an auditor reads — both state the rung
// that routed the model, so an over-tiered run is auditable from provenance
// alone. That is the audit gap tick 5eq left open and this bundle closed.
func TestAnAttemptsProvenanceStatesItsDerivedTier(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"tier:economy"} })

	r, result, err := f.run(f.Repo, fixtureOptions{budget: 10, ceiling: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	// The attempt marker's provenance — the closed object, not the open
	// handle — states the tier the dispatch was derived under.
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	var a1, b1 *runstate.Attempt
	for i := range attempts {
		switch attempts[i].TickID {
		case "a1":
			a1 = &attempts[i]
		case "b1":
			b1 = &attempts[i]
		}
	}
	if a1 == nil || b1 == nil {
		t.Fatalf("no attempt markers for a1/b1 among %v", attempts)
	}
	if a1.Provenance.Tier == nil || *a1.Provenance.Tier != "economy" {
		t.Errorf("a1's provenance states tier %v, want economy — the audit reads the record, not the marker's open handle", a1.Provenance.Tier)
	}
	if b1.Provenance.Tier == nil || *b1.Provenance.Tier != "balanced" {
		t.Errorf("b1's provenance states tier %v, want balanced (the policy default)", b1.Provenance.Tier)
	}

	// The gate's evidence over each tick says the same thing: the tier that
	// routed the attempt the gate is over, in the record the gate leaves
	// behind for a person auditing the merge — per tick, because a gate is
	// over one tick's work at the tier that tick's attempt earned.
	wantTier := map[string]string{"a1": "economy", "a2": "balanced", "b1": "balanced"}
	for _, key := range r.store.EvidenceKeys() {
		evidence, ok, err := r.store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("evidence %s: ok=%v err=%v", key, ok, err)
		}
		if evidence.Provenance.Role == nil || *evidence.Provenance.Role != "implement-tick" {
			continue // not the integrated gate over the implementer's work
		}
		tickID := ""
		if evidence.Provenance.TickID != nil {
			tickID = *evidence.Provenance.TickID
		}
		want, expected := wantTier[tickID]
		if !expected {
			t.Fatalf("gate evidence %s names tick %q, which the test does not know", key, tickID)
		}
		if evidence.Provenance.Tier == nil || *evidence.Provenance.Tier != want {
			t.Errorf("the gate evidence %s over %s states tier %v, want %s — the rung that routed the attempt the gate is over",
				key, tickID, evidence.Provenance.Tier, want)
		}
	}
}

// ------------------------------------------ the mass rule (tick s45, epic wne) ---
//
// THE ROUTED DISPATCH, end to end through the real dispatch path: the
// recorded classification — the decision record the exchange wrote on the
// run branch — is an input to the same derivation that derives every other
// tier, and what it moves is the START. The gate under test is the
// operator's own intent in miniature: a cheap default every unclassified
// first attempt falls back to, a dear tier one rung up that a clearing mass
// starts at, and a ceiling one rung above that a failure escalates into.

const massGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.economy]
model = "haiku"

[roles.implement.tiers.balanced]
model = "sonnet"

[roles.implement.tiers.strong]
model = "opus"

[tier_policy]
default = "economy"
ceiling = "strong"
dear_work_types = ["diagnosis", "design"]
dear_tier = "balanced"
mass_threshold = 0.50

[tier_policy.rate_limit]
response = "backoff-and-retry"
max_attempts = 8
max_delay_ms = 90000

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// massAnswer is one classifier answer for one tick, in the measured shape:
// the full distribution over the closed enum, the choice it argues for, and
// the model identity that gave it.
func massAnswer(tick string, choice runconfig.WorkType, probabilities map[runconfig.WorkType]float64) jev.Result {
	return jev.Result{
		Classifications: map[string]jev.Classification{tick: {
			TickID: tick, Choice: choice, Confidence: 0.9, Probabilities: probabilities,
		}},
		Model: "jev-1", Usage: jev.Usage{InputTokens: 1200, OutputTokens: 40, CostUSD: 0.00005},
	}
}

// distributionOf spells one answer's probabilities the way the record and
// the routing rule both read them.
func distributionOf(mechanical, translation, construction, diagnosis, design float64) map[runconfig.WorkType]float64 {
	return map[runconfig.WorkType]float64{
		runconfig.WorkMechanical:   mechanical,
		runconfig.WorkTranslation:  translation,
		runconfig.WorkConstruction: construction,
		runconfig.WorkDiagnosis:    diagnosis,
		runconfig.WorkDesign:       design,
	}
}

// A dispatch selects its model from the RECORDED DISTRIBUTION: the mass on
// the dear work types clears the provisional threshold for a1 and starts it
// at the dear tier; a2's mass does not clear and it falls back to the start
// policy's default; b1's mass sits exactly AT the threshold and does not
// clear, because the rule is strictly greater. Every marker says so on
// origin, and every derivation is in the journal with the mass, the
// threshold and the word provisional — the one place a person tuning the
// number reads what it still is.
func TestAMassRoutedDispatchSelectsItsModelFromTheRecord(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: massGate})
	classifier := &countingClassifier{answer: func(tick string) jev.Result {
		switch tick {
		case "a1": // 0.45 design + 0.10 diagnosis = 0.55 — clears 0.50.
			return massAnswer(tick, runconfig.WorkDesign, distributionOf(0.02, 0.03, 0.40, 0.10, 0.45))
		case "b1": // 0.45 design + 0.05 diagnosis = 0.50 — exactly at, never over.
			return massAnswer(tick, runconfig.WorkDesign, distributionOf(0.02, 0.03, 0.42, 0.05, 0.45))
		default: // a2: 0.30 design — under the threshold.
			return massAnswer(tick, runconfig.WorkConstruction, distributionOf(0.05, 0.05, 0.60, 0.00, 0.30))
		}
	}}
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	// The dispatch selected its MODEL through the tier the mass routed to:
	// a1 at the dear tier's model, the rest at the default's.
	if got := markerTierOfTry(t, r, "a1", 1); got != "balanced" {
		t.Errorf("a1's marker records tier %q, want balanced: 0.55 of mass on diagnosis, design clears 0.50", got)
	}
	if dispatch := f.dispatch("a1"); dispatch.Profile == nil || dispatch.Profile.Model != "sonnet" {
		t.Errorf("a1 dispatched at model %v, want the dear tier's sonnet", dispatch.Profile)
	}
	if got := markerTierOfTry(t, r, "a2", 1); got != "economy" {
		t.Errorf("a2's marker records tier %q, want economy: 0.30 of mass does not clear, and the start policy is the fallback", got)
	}
	if dispatch := f.dispatch("a2"); dispatch.Profile == nil || dispatch.Profile.Model != "haiku" {
		t.Errorf("a2 dispatched at model %v, want the default economy's haiku", dispatch.Profile)
	}
	if got := markerTierOfTry(t, r, "b1", 1); got != "economy" {
		t.Errorf("b1's marker records tier %q, want economy: 0.50 is AT the threshold, and the rule is strictly greater", got)
	}

	// The derivation is IN the journal, with the mass, the threshold, and
	// the word that says the number is not settled.
	detail, ok := journalLine(r, "a1", StageTierDerived)
	if !ok {
		t.Fatal("a1's dispatch recorded no tier derivation")
	}
	if !strings.Contains(detail, "0.55 of probability mass on the dear work types (diagnosis, design)") ||
		!strings.Contains(detail, "provisional mass threshold 0.50") {
		t.Errorf("the derivation record does not name the mass and the provisional threshold: %q", detail)
	}
	// And the run-level stance said so at admission, where an operator reads
	// it while the run can still be cancelled cheaply.
	var stance bool
	for _, event := range r.Journal() {
		if strings.Contains(event.Detail, "the mass threshold is PROVISIONAL") &&
			strings.Contains(event.Detail, "dear work types (diagnosis, design)") {
			stance = true
		}
	}
	if !stance {
		t.Error("the run never stated its mass-routing stance at admission")
	}

	// The role ticks were never classified and never routed by it: rv and co
	// have no declared route, so they ran at their own base values.
	if got := markerTierOfTry(t, r, "rv", 1); got != "" {
		t.Errorf("the review job recorded tier %q, want none: a role-carrying tick is never classified", got)
	}
	asked := classifier.askedIDs()
	sort.Strings(asked)
	if strings.Join(asked, ",") != "a1,a2,b1" {
		t.Errorf("the classifier was asked %v, want the three role-less ticks once each", asked)
	}
}

// THE LADDER UNDERNEATH, which is the reason promotion is safe at all: the
// classification picks a START, a failed attempt still earns a rung above
// whatever the classifier chose, and the restart reads the record rather
// than re-asking — so the escalation is derived from the same distribution
// the first attempt was, and the cold pass reaches the same ladder.
func TestAFailedAttemptEarnsARungAboveTheClassifiedStart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: massGate, mode: "blocked-first"})
	// a1 clears the threshold; a2 and b1 do not — the ladder under test is
	// a1's, and the fallback under test is theirs. Every incarnation answers
	// per tick exactly the same way, so any answer a cold one was allowed to
	// give would agree — what is asserted is that it was never ASKED for a
	// tick the run already paid for, not that it could not have answered.
	answer := func(tick string) jev.Result {
		if tick == "a1" {
			return massAnswer(tick, runconfig.WorkDesign, distributionOf(0.02, 0.03, 0.40, 0.10, 0.45))
		}
		return massAnswer(tick, runconfig.WorkConstruction, distributionOf(0.05, 0.05, 0.60, 0.00, 0.30))
	}
	classifier := &countingClassifier{answer: answer}
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first run ended %s, want the blocked first attempt to stop it: %s", result.State, result.Reason)
	}
	if got := markerTierOfTry(t, r, "a1", 1); got != "balanced" {
		t.Errorf("a1's first attempt recorded tier %q, want the classified start balanced", got)
	}

	// The restart: a fresh clone, the same run id — and a classifier that
	// would answer again, to prove it is never asked. a1's redispatch reads
	// the RECORD, derives one rung above the classified start, and settles;
	// then a2's own first try blocks exactly as a1's did and refuses the run.
	clone := cloneRepo(t, f.Repo.Origin, f.Root+"/restarted")
	coldClassifier := &countingClassifier{answer: answer}
	coldOpts := f.options(clone, fixtureOptions{})
	coldOpts.Classifier = coldClassifier
	restarted, err := New(coldOpts)
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the restart ended %s, want a2's own first try to refuse it: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.TickID != "a2" {
		t.Fatalf("the restart's refusal is %+v, want a2's first try", result.Failure)
	}

	if got := markerTierOfTry(t, restarted, "a1", 2); got != "strong" {
		t.Errorf("a1's redispatch recorded tier %q, want strong: one failed attempt earns its rung ABOVE the classified start", got)
	}
	detail, ok := journalLine(restarted, "a1", StageTierDerived)
	if !ok {
		t.Fatal("the redispatch recorded no tier derivation")
	}
	if !strings.Contains(detail, "escalated 1 rung(s) after 1 failed attempt(s)") {
		t.Errorf("the redispatch's derivation does not name the rung and what earned it: %q", detail)
	}
	if got := markerTierOfTry(t, restarted, "a2", 1); got != "economy" {
		t.Errorf("a2's first attempt recorded tier %q, want economy: a rung is earned, never assumed", got)
	}
	// The restart was never re-asked for a1 — the record is the answer —
	// and it WAS asked for a2, whose first dispatch is its own.
	if ids := coldClassifier.askedIDs(); strings.Join(ids, ",") != "a2" {
		t.Errorf("the restart's classifier was asked %v; want a2 only — a1's record is read, never re-paid", ids)
	}

	// The settling run: report mode finishes the epic, and b1's first
	// dispatch is classified live — its mass does not clear, so it takes the
	// start policy, the fallback for a below-threshold distribution.
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
		t.Fatal(err)
	}
	if done.State != runstate.StateCompleted {
		t.Fatalf("the settling run ended %s: %s", done.State, done.Reason)
	}
	if got := markerTierOfTry(t, settled, "b1", 1); got != "economy" {
		t.Errorf("b1's first attempt recorded tier %q, want economy: the start policy is the fallback for a below-threshold mass", got)
	}
	if ids := settleClassifier.askedIDs(); strings.Join(ids, ",") != "b1" {
		t.Errorf("the settling run's classifier was asked %v, want b1 only — a1 and a2 read their records", ids)
	}
}

// DEGRADATION, not failure, through the whole dispatch path: a run with no
// classifier at all — and a run whose classifier was unreachable — route
// every first attempt at the start policy exactly as they did before the
// classification existed, and the no-answer is a RECORD the run finished on
// rather than a stop.
func TestAnAbsentOrNoAnswerClassificationFallsBackToTheStartPolicy(t *testing.T) {
	t.Parallel()

	// No classifier configured at all: the feature degrades to today's
	// behaviour, which is the whole point of the fallback's shape.
	f := newFixture(t, fixtureOptions{gate: massGate})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run without a classifier ended %s: %s", result.State, result.Reason)
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		if got := markerTierOfTry(t, r, tick, 1); got != "economy" {
			t.Errorf("%s's marker records tier %q, want economy: no classification means the start policy, nothing else", tick, got)
		}
	}
	for _, event := range r.Journal() {
		if event.Stage == StageClassified {
			t.Errorf("a run with no classifier classified %s: %q", event.Tick, event.Detail)
		}
	}

	// An unreachable classifier is a recorded no-answer, and the run
	// ROUTES on it — at the start policy, for the same reason the warm
	// process did, which is what makes the cold re-derivation agree.
	g := newFixture(t, fixtureOptions{gate: massGate})
	unavailable := &countingClassifier{answer: func(string) jev.Result {
		return jev.Result{Unavailable: "the classifier could not be reached: connection refused"}
	}}
	opts := g.options(g.Repo, fixtureOptions{})
	opts.Classifier = unavailable
	gone, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err = gone.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("a run with an unreachable classifier ended %s, want a completed degrade: %s", result.State, result.Reason)
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		if got := markerTierOfTry(t, gone, tick, 1); got != "economy" {
			t.Errorf("%s's marker records tier %q, want economy: a recorded no-answer falls back to the start policy", tick, got)
		}
	}
	var noAnswer bool
	for _, event := range gone.Journal() {
		if event.Stage == StageClassified && strings.Contains(event.Detail, "no answer") {
			noAnswer = true
		}
	}
	if !noAnswer {
		t.Error("the journal never said the classifier gave no answer")
	}
}
