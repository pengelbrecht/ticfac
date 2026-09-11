package reconcile

import (
	"strings"
	"testing"

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
}

// An unrecognised tier label is refused loudly — naming the tick and the
// label — and refused BEFORE anything is claimed or recorded: a weakly typed
// field the tracker cannot validate is a field whose typo can only ever
// surface here, so the refusal must not also spend an attempt on it.
func TestAnUnrecognisedTierLabelIsRefusedLoudlyBeforeAnythingIsClaimed(t *testing.T) {
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
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
	}

	if got := markerTier(t, restarted, "a1", 1); got != "balanced" {
		t.Errorf("the first attempt's marker records tier %q, want balanced: the first attempt starts at the default", got)
	}
	if got := markerTier(t, restarted, "a1", 2); got != "strong" {
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
	if got := markerTier(t, restarted, "a2", 3); got != "balanced" {
		t.Errorf("a2's first attempt recorded tier %q, want balanced: a rung is earned, never assumed", got)
	}
	// And the role jobs ran at base values: no route was declared for them,
	// and a process role is never loaned the work default.
	if got := markerTier(t, restarted, "rv", 5); got != "" {
		t.Errorf("the review job recorded tier %q, want none: no route is declared for it", got)
	}
}

// A repository with no policy runs exactly as it did before this tick: every
// dispatch at the role's base values, no tier recorded, nothing starting high.
func TestNoPolicyMeansBaseValuesEndToEnd(t *testing.T) {
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
