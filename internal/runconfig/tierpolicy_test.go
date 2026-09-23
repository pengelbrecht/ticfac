package runconfig

import (
	"reflect"
	"strings"
	"testing"
)

// The [tier_policy] table: what a valid declaration looks like, everything a
// malformed one is refused for, and — the point of the table — the DERIVATION
// it declares, which has to be a pure function or it is nothing at all.
//
// The document under test is always a whole runners.toml, because a policy
// never travels alone: it sits beside the roles it routes, and the reader has
// to see both.

const policyDoc = `
version = 2

[roles.implement]
kind = "pi"
model = "glm-5.3"

[roles.implement.tiers.economy]
model = "glm-5.3-flash"

[roles.implement.tiers.balanced]
model = "glm-5.3"

[roles.implement.tiers.strong]
model = "glm-5.3"
effort = "high"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.tiers.frontier]
model = "opus"

[tier_policy]
default = "balanced"
ceiling = "strong"
label_prefix = "tier:"

[[tier_policy.start]]
tier = "economy"
types = ["task"]
max_priority = 3
max_blocks = 0

[[tier_policy.start]]
tier = "economy"
roles = ["implement", "implement-tick"]
wave = 1

[tier_policy.roles]
review = "frontier"
closeout-epic = ""

[tier_policy.concurrency]
economy = 4
balanced = 2
strong = 1

[tier_policy.rate_limit]
response = "backoff-and-retry"
max_attempts = 8
max_delay_ms = 90000

[testing.commands]
tree = { command = "true", description = "the tree" }
`

func loadPolicy(t *testing.T, document string) *TierPolicy {
	t.Helper()
	cfg, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.TierPolicy == nil {
		t.Fatal("the loaded config carries no [tier_policy]")
	}
	return cfg.TierPolicy
}

// The whole table loads, every cell reads back as written, and the start
// rules keep their file order — first match wins is a claim about order, so
// order has to survive the round trip.
func TestATierPolicyLoadsCellForCell(t *testing.T) {
	p := loadPolicy(t, policyDoc)
	if p.Default != TierBalanced {
		t.Errorf("default = %q", string(p.Default))
	}
	if p.Ceiling != TierStrong {
		t.Errorf("ceiling = %q", string(p.Ceiling))
	}
	if p.LabelPrefix != "tier:" {
		t.Errorf("label_prefix = %q", p.LabelPrefix)
	}
	if len(p.Start) != 2 {
		t.Fatalf("start rules = %d, want 2 in file order", len(p.Start))
	}
	if p.Start[0].Tier != TierEconomy || len(p.Start[0].Types) != 1 {
		t.Errorf("start[0] = %+v", p.Start[0])
	}
	if p.Start[0].MaxPriority == nil || *p.Start[0].MaxPriority != 3 {
		t.Errorf("start[0].max_priority = %+v", p.Start[0].MaxPriority)
	}
	if p.Start[0].MaxBlocks == nil || *p.Start[0].MaxBlocks != 0 {
		t.Errorf("start[0].max_blocks = %+v", p.Start[0].MaxBlocks)
	}
	if p.Start[1].Wave == nil || *p.Start[1].Wave != 1 {
		t.Errorf("start[1].wave = %+v", p.Start[1].Wave)
	}
	if p.Roles["review"] != TierFrontier || p.Roles["closeout-epic"] != "" {
		t.Errorf("roles = %+v", p.Roles)
	}
	if p.Concurrency["balanced"] != 2 || p.Concurrency["strong"] != 1 {
		t.Errorf("concurrency = %+v", p.Concurrency)
	}
	if p.RateLimit == nil || p.RateLimit.Response != "backoff-and-retry" || p.RateLimit.MaxAttempts != 8 || p.RateLimit.MaxDelayMs != 90000 {
		t.Errorf("rate_limit = %+v", p.RateLimit)
	}
}

// Every way a policy can be wrong is refused, naming the cell. These are the
// refusals that make the table EVALUATED rather than interpreted: a default
// nobody declared, a ceiling under the start, a start rule above the default.
func TestAMalformedTierPolicyIsRefusedNamingTheCell(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"no default", "[tier_policy]\nceiling = \"strong\"\n", "tier_policy.default"},
		{"unknown default tier", "[tier_policy]\ndefault = \"premium\"\n", `is not one of`},
		{"ceiling below default", "[tier_policy]\ndefault = \"balanced\"\nceiling = \"economy\"\n", "below the default"},
		{"unknown ceiling tier", "[tier_policy]\ndefault = \"balanced\"\nceiling = \"unlimited\"\n", "is not one of"},
		{"zero step", "[tier_policy]\ndefault = \"balanced\"\nstep = 0\n", "not a ladder step"},
		{"empty label prefix", "[tier_policy]\ndefault = \"balanced\"\nlabel_prefix = \"\"\n", "must not be empty"},
		{"rule above the default", "[tier_policy]\ndefault = \"balanced\"\n\n[[tier_policy.start]]\ntier = \"strong\"\n", "nothing starts high"},
		{"rule without a tier", "[tier_policy]\ndefault = \"balanced\"\n\n[[tier_policy.start]]\ntypes = [\"task\"]\n", "tier_policy.start[0].tier"},
		{"rule with an unknown tier", "[tier_policy]\ndefault = \"balanced\"\n\n[[tier_policy.start]]\ntier = \"premium\"\n", "is not one of"},
		{"rule with an empty type list", "[tier_policy]\ndefault = \"balanced\"\n\n[[tier_policy.start]]\ntier = \"economy\"\ntypes = []\n", "must not be empty"},
		{"rule with an empty priority bound", "[tier_policy]\ndefault = \"balanced\"\n\n[[tier_policy.start]]\ntier = \"economy\"\nmin_priority = 4\nmax_priority = 2\n", "the bound is empty"},
		{"rule with wave zero", "[tier_policy]\ndefault = \"balanced\"\n\n[[tier_policy.start]]\ntier = \"economy\"\nwave = 0\n", "waves are 1-based"},
		{"role nobody can match", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.roles]\nimplements = \"strong\"\n", "not a role this policy can be asked about"},
		{"role routed to an unknown tier", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.roles]\nreview = \"ultra\"\n", "is not one of"},
		{"concurrency for an unknown tier", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.concurrency]\npremium = 4\n", "is not one of"},
		{"concurrency of zero", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.concurrency]\nstrong = 0\n", "not a width"},
		{"rate limit answers immediately", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.rate_limit]\nresponse = \"retry-immediately\"\n", "backoff-and-retry is the only one"},
		{"rate limit without patience", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.rate_limit]\nmax_delay_ms = 100\n", "not a backoff ceiling"},
		{"rate limit with no attempts", "[tier_policy]\ndefault = \"balanced\"\n\n[tier_policy.rate_limit]\nmax_attempts = 0\n", "not a retry budget"},
		// The classification-routing cells (tick s45): every way a promotion
		// nobody bounded or a cell nobody can evaluate can be written.
		{"dear work types with no tier to promote to", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"diagnosis\", \"design\"]\n", "tier_policy.dear_tier"},
		{"a dear tier no mass can reach", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_tier = \"balanced\"\n", "tier_policy.dear_work_types"},
		{"an empty dear work-type list", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = []\ndear_tier = \"balanced\"\n", "must not be empty"},
		{"a work type off the closed enum", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\", \"refactoring\"]\ndear_tier = \"balanced\"\n", "is not one of the work types"},
		{"a model id where a work type belongs", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"glm-5.3\"]\ndear_tier = \"balanced\"\n", "is not one of the work types"},
		{"a work type named twice", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\", \"design\"]\ndear_tier = \"balanced\"\n", "named twice"},
		{"a dear tier that is not a tier", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\"]\ndear_tier = \"premium\"\n", "is not one of"},
		{"an empty dear tier", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\"]\ndear_tier = \"\"\n", "required \u2014 a promotion with no tier"},
		{"a dear tier below the default is a demotion", "[tier_policy]\ndefault = \"balanced\"\nceiling = \"strong\"\ndear_work_types = [\"design\"]\ndear_tier = \"economy\"\n", "at or below the default"},
		{"a dear tier above the ceiling", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\"]\ndear_tier = \"frontier\"\n", "above the ceiling"},
		{"a dear tier above a default ceiling", "[tier_policy]\ndefault = \"balanced\"\ndear_work_types = [\"design\"]\ndear_tier = \"strong\"\n", "above the ceiling"},
		{"a mass threshold of zero", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\"]\ndear_tier = \"balanced\"\nmass_threshold = 0\n", "not a probability-mass threshold"},
		{"a mass threshold above one", "[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\ndear_work_types = [\"design\"]\ndear_tier = \"balanced\"\nmass_threshold = 1.5\n", "not a probability-mass threshold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := "version = 2\n\n[roles.implement]\nkind = \"pi\"\n\n" + tc.doc
			_, err := Parse([]byte(doc))
			if err == nil {
				t.Fatal("the malformed policy was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to name %q", err.Error(), tc.want)
			}
		})
	}
}

// A runners.toml WITHOUT the table still loads and yields a nil policy — a
// repository that declares no policy runs at base values, and that is a
// legitimate stance, not an error.
func TestAnAbsentTierPolicyIsLegitimate(t *testing.T) {
	cfg, err := Parse([]byte("version = 2\n\n[roles.implement]\nkind = \"pi\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TierPolicy != nil {
		t.Error("a file with no [tier_policy] carried one anyway")
	}
}

// ---------------------------------------------------------- the derivation ---

func workFacts() TickFacts {
	return TickFacts{
		TickID: "a1", Priority: 2, Type: "task", Role: "implement-tick",
		Labels: []string{"backend"}, Wave: 2, Blocks: 3,
	}
}

// THE purity property, exactly as the acceptance criterion asks it: the same
// tick at the same attempt under the same policy derives the same tier —
// twice, and byte for byte, reason included.
func TestTheSameTickAtTheSameAttemptDerivesTheSameTierTwice(t *testing.T) {
	p := loadPolicy(t, policyDoc)
	for _, attempt := range []DeriveAttempt{{Number: 1}, {Number: 2, Failed: 1}, {Number: 5, Failed: 4}} {
		first, err := p.Derive(workFacts(), attempt)
		if err != nil {
			t.Fatalf("derive at %+v: %v", attempt, err)
		}
		second, err := p.Derive(workFacts(), attempt)
		if err != nil {
			t.Fatalf("derive again at %+v: %v", attempt, err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("the derivation is not a function: %+v then %+v", first, second)
		}
	}
}

// The table test: priority, type, role and graph position (wave, blocks) are
// all inputs a declared rule can route on, and each one moves the answer.
func TestTheDerivationRoutesOnEveryDeclaredFact(t *testing.T) {
	p := loadPolicy(t, policyDoc)

	cases := []struct {
		name  string
		facts TickFacts
		want  Tier
	}{
		// priority: 2 is urgent, 3 is not — the max_priority bound.
		{"an urgent task starts at the default", TickFacts{TickID: "t", Priority: 2, Type: "task", Role: "implement-tick", Wave: 2, Blocks: 1}, TierBalanced},
		{"a low-priority task nothing blocks starts economy", TickFacts{TickID: "t", Priority: 3, Type: "task", Role: "implement-tick", Wave: 2, Blocks: 0}, TierEconomy},
		// type: a bug is not a task, so the type rule does not match.
		{"a low-priority bug does not match the task rule", TickFacts{TickID: "t", Priority: 3, Type: "bug", Role: "implement-tick", Wave: 2, Blocks: 0}, TierBalanced},
		// role: the process role route, both spellings, and the work ladder.
		{"review runs at its declared route", TickFacts{TickID: "t", Priority: 1, Type: "epic", Role: "review-epic", Wave: 3, Blocks: 9}, TierFrontier},
		{"closeout runs at the role's own base values", TickFacts{TickID: "t", Priority: 1, Type: "epic", Role: "closeout-epic", Wave: 3, Blocks: 9}, ""},
		{"an un-routed process role is never loaned a work tier", TickFacts{TickID: "t", Priority: 1, Type: "epic", Role: "plan-epic", Wave: 1, Blocks: 0}, ""},
		// graph position: wave 1 matches the wave rule even when the type
		// rule does not, and blocks count against the economy rule.
		{"a wave-1 implement tick starts economy", TickFacts{TickID: "t", Priority: 1, Type: "bug", Role: "implement-tick", Wave: 1, Blocks: 9}, TierEconomy},
		{"a wave-2 tick that blocks three does not", TickFacts{TickID: "t", Priority: 3, Type: "task", Role: "implement-tick", Wave: 2, Blocks: 3}, TierBalanced},
		{"an unplaced wave is never a wave-1 match", TickFacts{TickID: "t", Priority: 3, Type: "bug", Role: "implement-tick", Wave: 0, Blocks: 0}, TierBalanced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.Derive(tc.facts, DeriveAttempt{Number: 1})
			if err != nil {
				t.Fatal(err)
			}
			if got.Tier != tc.want {
				t.Errorf("derived %q (%s), want %q", string(got.Tier), got.Reason, string(tc.want))
			}
		})
	}
}

// The ladder: a first attempt starts at the start tier; only a failed attempt
// escalates; one step per failure; the ceiling is named and never passed; and
// at the ceiling the next actor is a person, not a bigger model.
func TestTheLadderStartsLowAndEarnsItsRungs(t *testing.T) {
	p := loadPolicy(t, policyDoc)
	facts := TickFacts{TickID: "t", Priority: 1, Type: "bug", Role: "implement-tick", Wave: 2, Blocks: 1}

	// The same facts at attempt 1 and at attempt 2 differ only by the failure.
	first, err := p.Derive(facts, DeriveAttempt{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Tier != TierBalanced {
		t.Fatalf("a first attempt starts at %q, want the default", string(first.Tier))
	}
	second, err := p.Derive(facts, DeriveAttempt{Number: 2, Failed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.Tier != TierStrong {
		t.Fatalf("one failed attempt escalates to %q (%s), want strong", string(second.Tier), second.Reason)
	}
	// The ceiling holds: however many failures, the ladder is over.
	at, err := p.Derive(facts, DeriveAttempt{Number: 9, Failed: 8})
	if err != nil {
		t.Fatal(err)
	}
	if at.Tier != TierStrong {
		t.Fatalf("eight failures derive %q, want the ceiling strong: past it the next actor is a person", string(at.Tier))
	}
	if !strings.Contains(at.Reason, "the ladder is over") {
		t.Errorf("the at-ceiling reason does not say the ladder is over: %q", at.Reason)
	}

	// A ceiling equal to the default is a ladder with no rungs: the second
	// failure is a person's, said in the record rather than paid for.
	flat := loadPolicy(t, strings.ReplaceAll(policyDoc, "ceiling = \"strong\"", ""))
	out, err := flat.Derive(facts, DeriveAttempt{Number: 2, Failed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != TierBalanced || !strings.Contains(out.Reason, "the next actor is a person") {
		t.Errorf("a ceilingless policy escalated: %q (%q)", string(out.Tier), out.Reason)
	}

	// A step of two climbs two rungs per failure — and the operator's ladder
	// for this epic (flash → 5.3, effort within a model) is what the ceiling
	// bounds, not a generic "one bigger model".
	strided := loadPolicy(t, strings.ReplaceAll(policyDoc, "ceiling = \"strong\"", "ceiling = \"frontier\"\nstep = 2"))
	out, err = strided.Derive(facts, DeriveAttempt{Number: 2, Failed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != TierFrontier {
		t.Errorf("a step of two after one failure derives %q, want frontier", string(out.Tier))
	}
}

// The label override: honoured, loud when the label is not a tier, and loud
// when two of them disagree — a weakly typed field's only safety is the
// refusal, never a silent fall-back to the default.
func TestTheLabelOverrideIsHonouredOrRefusedLoudly(t *testing.T) {
	p := loadPolicy(t, policyDoc)

	withLabel := func(labels ...string) TickFacts {
		facts := workFacts()
		facts.Labels = append([]string{}, labels...)
		return facts
	}

	overridden, err := p.Derive(withLabel("backend", "tier:economy"), DeriveAttempt{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.Tier != TierEconomy {
		t.Errorf("an override label derived %q, want economy", string(overridden.Tier))
	}
	// An override pins the tick: the ladder does not run over the top of it.
	still, err := p.Derive(withLabel("tier:strong"), DeriveAttempt{Number: 4, Failed: 3})
	if err != nil {
		t.Fatal(err)
	}
	if still.Tier != TierStrong || !strings.Contains(still.Reason, "overrides the ladder") {
		t.Errorf("a failed attempt moved an overridden tick: %q (%q)", string(still.Tier), still.Reason)
	}

	_, err = p.Derive(withLabel("tier:premium"), DeriveAttempt{Number: 1})
	if err == nil || !strings.Contains(err.Error(), "a1 carries label \"tier:premium\"") || !strings.Contains(err.Error(), "economy, balanced, strong, frontier") {
		t.Errorf("an unknown tier label was not refused loudly naming the tick and the label: %v", err)
	}

	_, err = p.Derive(withLabel("tier:strong", "tier:economy"), DeriveAttempt{Number: 1})
	if err == nil || !strings.Contains(err.Error(), "2 tier labels") {
		t.Errorf("two tier labels on one tick were not refused: %v", err)
	}

	// The namespace is the policy's: a label outside it is just a label.
	untouched, err := p.Derive(withLabel("runs-at-frontier"), DeriveAttempt{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Tier != TierBalanced {
		t.Errorf("a label outside the namespace moved the tier: %q", string(untouched.Tier))
	}

	// With no policy declared at all, the tier vocabulary is still closed: a
	// label naming a KNOWN tier is an explicit route, and the dispatch refuses
	// it only if the roles table does not declare the tier. A label naming an
	// unknown one is refused here regardless of policy — this refusal is the
	// only place a weakly typed label's typo can ever surface.
	nopolicy, err := (*TierPolicy)(nil).Derive(withLabel("tier:strong"), DeriveAttempt{Number: 1})
	if err != nil || nopolicy.Tier != TierStrong {
		t.Errorf("a known tier label under no policy was not honoured: %+v, %v", nopolicy, err)
	}
	_, err = (*TierPolicy)(nil).Derive(withLabel("tier:premium"), DeriveAttempt{Number: 1})
	if err == nil || !strings.Contains(err.Error(), "a1 carries label \"tier:premium\"") {
		t.Errorf("an unknown tier label under no policy was not refused: %v", err)
	}
}

// No policy, no facts-routing: every dispatch runs at the role's own base
// values. That is the honest default, and it is why nothing can start high
// in a repository that never declared a ladder.
func TestNoPolicyMeansNoOverlay(t *testing.T) {
	var p *TierPolicy
	out, err := p.Derive(workFacts(), DeriveAttempt{Number: 3, Failed: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tier != "" || !strings.Contains(out.Reason, "no [tier_policy] is declared") {
		t.Errorf("no policy derived %q (%q)", string(out.Tier), out.Reason)
	}
}

// The attempt inputs are validated: a rung is earned only by an attempt that
// existed, and attempt numbers are 1-based. A caller that cannot say which
// attempt it is on is a caller that cannot derive a tier.
func TestTheAttemptInputsAreValidated(t *testing.T) {
	p := loadPolicy(t, policyDoc)
	for _, attempt := range []DeriveAttempt{{Number: 0}, {Number: 2, Failed: 2}, {Number: 1, Failed: -1}} {
		if _, err := p.Derive(workFacts(), attempt); err == nil {
			t.Errorf("attempt %+v was accepted", attempt)
		}
	}
}

// ------------------------------------------------------ concurrency and 429 ---

// WaveWidth: the width of a wave is the TIGHTEST per-tier bound among the
// tiers it routes to, never wider than the host's own cap. A tier with no
// bound narrows nothing. No policy narrows nothing.
func TestAWaveNarrowsToTheTightestTierBound(t *testing.T) {
	p := loadPolicy(t, policyDoc)

	cases := []struct {
		name  string
		host  int
		tiers []Tier
		want  int
	}{
		{"four economy wide is fine", 4, []Tier{TierEconomy, TierEconomy, TierEconomy, TierEconomy}, 4},
		{"one strong tick narrows the whole wave", 4, []Tier{TierEconomy, TierEconomy, TierEconomy, TierStrong}, 1},
		{"balanced caps at two", 4, []Tier{TierBalanced, TierBalanced, TierBalanced}, 2},
		{"the host cap still wins when tighter", 1, []Tier{TierEconomy, TierEconomy}, 1},
		{"a tier with no bound narrows nothing", 4, []Tier{TierFrontier, TierEconomy}, 4},
		{"no host cap means the bounds are the cap", 0, []Tier{TierBalanced, TierEconomy}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.WaveWidth(tc.host, tc.tiers); got != tc.want {
				t.Errorf("width = %d, want %d", got, tc.want)
			}
		})
	}

	var none *TierPolicy
	if got := none.WaveWidth(4, []Tier{TierStrong}); got != 4 {
		t.Errorf("no policy narrowed a wave to %d", got)
	}
}

// The declared rate-limit stance, and its defaults — which are the 2026-09-11
// pi configuration on epic av8, not numbers invented here: eight attempts,
// ninety seconds of patience. A 429 is a pause; three immediate retries are
// how it became a dead worker.
func TestTheRateLimitStanceDefaultsToBackoffAndRetry(t *testing.T) {
	p := loadPolicy(t, policyDoc)
	stance := p.RateLimitOrDefault()
	if stance.Response != "backoff-and-retry" || stance.MaxAttempts != 8 || stance.MaxDelayMs != 90000 {
		t.Errorf("stance = %+v", stance)
	}

	var none *TierPolicy
	if stance := none.RateLimitOrDefault(); stance.Response != "backoff-and-retry" {
		t.Errorf("the default response is %q, not the one answer the wave-2 finding demands", stance.Response)
	}

	declared := loadPolicy(t, strings.ReplaceAll(policyDoc,
		"response = \"backoff-and-retry\"\nmax_attempts = 8\nmax_delay_ms = 90000",
		"max_attempts = 3"))
	if stance := declared.RateLimitOrDefault(); stance.MaxAttempts != 3 || stance.MaxDelayMs != 90000 {
		t.Errorf("a partial declaration did not default the rest: %+v", stance)
	}
}

// --------------------------------------------- the mass rule (tick s45, wne) ---
//
// THE ROUTING RULE the recorded classification feeds: probability mass over
// the policy's dear work types against a configurable threshold — not the
// argmax, and not the argmax with a confidence floor underneath it. The gate
// under test is the operator's own lineup in miniature: a cheap default, a
// dear tier one rung up that the classifier may start a first attempt at, and
// a ceiling one rung above that.

const massPolicyDoc = `
version = 2

[roles.implement]
kind = "pi"
model = "glm-5.3"

[roles.implement.tiers.economy]
model = "glm-5.3-flash"

[roles.implement.tiers.balanced]
model = "glm-5.3"

[roles.implement.tiers.strong]
model = "glm-5.3"
effort = "high"

[tier_policy]
default = "economy"
ceiling = "strong"
dear_work_types = ["diagnosis", "design"]
dear_tier = "balanced"
mass_threshold = 0.50

[[tier_policy.start]]
tier = "economy"
types = ["task"]
max_priority = 3
max_blocks = 0

[testing.commands]
tree = { command = "true", description = "the tree" }
`

// distribution builds one recorded classification's input: a full map over
// the closed enum, the shape the decision record reads back as.
func distribution(mechanical, translation, construction, diagnosis, design float64) *DeriveClassification {
	return &DeriveClassification{Probabilities: map[WorkType]float64{
		WorkMechanical:   mechanical,
		WorkTranslation:  translation,
		WorkConstruction: construction,
		WorkDiagnosis:    diagnosis,
		WorkDesign:       design,
	}}
}

// The cells load cell for cell, and the threshold's default is the
// PROVISIONAL 0.50 — a starting point from one measurement, not a finding,
// which is why it is the accessor's documented job to say so wherever the
// number is read.
func TestTheMassRoutingCellsLoadCellForCell(t *testing.T) {
	p := loadPolicy(t, massPolicyDoc)
	if len(p.DearWorkTypes) != 2 || p.DearWorkTypes[0] != WorkDiagnosis || p.DearWorkTypes[1] != WorkDesign {
		t.Errorf("dear_work_types = %v, want diagnosis and design in file order", p.DearWorkTypes)
	}
	if p.DearTier != TierBalanced {
		t.Errorf("dear_tier = %q, want balanced", string(p.DearTier))
	}
	if p.MassThresholdOrDefault() != 0.50 {
		t.Errorf("mass_threshold = %v, want the declared 0.50", p.MassThresholdOrDefault())
	}
	undeclared := loadPolicy(t, strings.ReplaceAll(massPolicyDoc, "mass_threshold = 0.50", ""))
	if undeclared.MassThresholdOrDefault() != 0.50 {
		t.Errorf("an undeclared threshold defaulted to %v, want the provisional 0.50", undeclared.MassThresholdOrDefault())
	}
}

// THE RULE ITSELF. The dear tier is chosen when the mass on the dear work
// types clears the threshold — strictly greater — and nothing else about the
// distribution matters: not the argmax, not the confidence, not how split
// the distribution is. The measured cases are all here: the split tick
// (0.45 design / 0.42 construction) is the one the argmax-with-a-floor shape
// would have discarded, and the mass rule spends.
func TestTheMassRuleRoutesOnProbabilityMassNotTheArgmax(t *testing.T) {
	p := loadPolicy(t, massPolicyDoc)

	cases := []struct {
		name           string
		classification *DeriveClassification
		want           Tier
		wantReason     string
	}{
		{"a design tick clears on its own", distribution(0.02, 0.03, 0.15, 0.05, 0.55), TierBalanced, "0.60 of probability mass"},
		{"a diagnosis tick clears on its own", distribution(0.02, 0.03, 0.15, 0.70, 0.05), TierBalanced, "0.75 of probability mass"},
		{"mass exactly at the threshold does not clear", distribution(0.10, 0.03, 0.42, 0.05, 0.45), TierEconomy, ""},
		{"the argmax is not the rule: construction wins the argmax and still routes cheap under the threshold",
			distribution(0.05, 0.03, 0.45, 0.07, 0.40), TierEconomy, ""},
		{"the split tick spends its 0.45 of design once the threshold is reached for",
			distribution(0.03, 0.05, 0.42, 0.00, 0.45), TierBalanced, "0.45 of probability mass"},
		{"a no-answer distribution routes nothing", &DeriveClassification{}, TierEconomy, ""},
		{"an absent classification falls back to the start policy", nil, TierEconomy, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The split tick only clears once the threshold is re-tuned —
			// which is the thing a recorded distribution is FOR (a threshold
			// change re-evaluated against runs that already happened).
			policy := p
			if strings.Contains(tc.name, "once the threshold is reached for") {
				policy = loadPolicy(t, strings.ReplaceAll(massPolicyDoc, "mass_threshold = 0.50", "mass_threshold = 0.40"))
			}
			got, err := policy.DeriveClassified(workFacts(), DeriveAttempt{Number: 1}, tc.classification)
			if err != nil {
				t.Fatal(err)
			}
			if got.Tier != tc.want {
				t.Errorf("derived %q (%s), want %q", string(got.Tier), got.Reason, string(tc.want))
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("the reason %q does not say %q", got.Reason, tc.wantReason)
			}
		})
	}

	// A policy that routes no classification anywhere — the whole trio
	// omitted — is the legitimate stance: the same distribution promotes
	// nothing, because the config, not the classifier, decides what a work
	// type costs.
	plain := loadPolicy(t, strings.ReplaceAll(massPolicyDoc,
		"dear_work_types = [\"diagnosis\", \"design\"]\ndear_tier = \"balanced\"\nmass_threshold = 0.50", ""))
	got, err := plain.DeriveClassified(workFacts(), DeriveAttempt{Number: 1}, distribution(0, 0, 0, 0.3, 0.7))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierEconomy {
		t.Errorf("a policy that declares no dear routing promoted to %q", string(got.Tier))
	}

	// And no policy at all is more legitimate still: base values, whatever
	// the classifier said, because there is no table to ask what a work
	// type is worth.
	var none *TierPolicy
	got, err = none.DeriveClassified(workFacts(), DeriveAttempt{Number: 1}, distribution(0, 0, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != "" || !strings.Contains(got.Reason, "no [tier_policy] is declared") {
		t.Errorf("no policy derived %q (%q), want base values", string(got.Tier), got.Reason)
	}
}

// The one thing the rule must NOT be able to walk around: the start rules,
// the override label and the process-role route all still outrank or bound
// the classifier, exactly as before — the narrowing admits the classifier as
// a START, never as a fourth authority.
func TestTheMassRuleMovesOnlyTheStart(t *testing.T) {
	p := loadPolicy(t, massPolicyDoc)
	clears := distribution(0, 0, 0, 0.1, 0.9)

	// The label override outranks the classifier: an operator's explicit pin,
	// and the loud refusal for a typo'd one is unchanged.
	got, err := p.DeriveClassified(TickFacts{
		TickID: "t", Priority: 2, Type: "task", Role: "implement-tick",
		Labels: []string{"tier:economy"}, Wave: 2, Blocks: 3,
	}, DeriveAttempt{Number: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierEconomy || !strings.Contains(got.Reason, "overrides the ladder") {
		t.Errorf("a label did not outrank a clearing classification: %q (%q)", string(got.Tier), got.Reason)
	}
	if _, err := p.DeriveClassified(TickFacts{
		TickID: "t", Priority: 2, Type: "task", Role: "implement-tick",
		Labels: []string{"tier:premium"}, Wave: 2, Blocks: 3,
	}, DeriveAttempt{Number: 1}, clears); err == nil {
		t.Error("an unrecognised label was accepted because a classification was present")
	}

	// A start rule that matches is the start policy's answer about this
	// tick, and the classification is the better judgement that REPLACES it:
	// the start policy is what an absent or below-threshold classification
	// "falls back to", so a rule — a structural proxy — does not outvote a
	// recorded distribution. (The rule below routes economy; the clearing
	// classification starts the tick at the dear tier above it.)
	blocked := TickFacts{TickID: "t", Priority: 3, Type: "task", Role: "implement-tick", Wave: 2, Blocks: 0}
	got, err = p.DeriveClassified(blocked, DeriveAttempt{Number: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierBalanced || !strings.Contains(got.Reason, "probability mass") {
		t.Errorf("a clearing classification did not replace the matching start rule: %q (%q)", string(got.Tier), got.Reason)
	}
	// The same facts with NO classification take the rule — the fallback is
	// exactly the start policy, which is the degradation the rule exists for.
	got, err = p.DeriveClassified(blocked, DeriveAttempt{Number: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierEconomy || !strings.Contains(got.Reason, "start rule 1") {
		t.Errorf("without a classification the start rule did not route: %q (%q)", string(got.Tier), got.Reason)
	}
}

// THE LADDER UNDERNEATH, which is the reason promotion is safe at all: the
// classification picks a START, a failed attempt still earns a rung above
// whatever the classifier chose, and the ceiling still bounds the result —
// at the ceiling the next actor is a person, not a bigger model.
func TestAMassRoutedStartStillEarnsItsRungsBoundedByTheCeiling(t *testing.T) {
	p := loadPolicy(t, massPolicyDoc)
	clears := distribution(0, 0, 0, 0.1, 0.9)

	// First attempt: the classified start.
	got, err := p.DeriveClassified(workFacts(), DeriveAttempt{Number: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierBalanced || !strings.Contains(got.Reason, "provisional mass threshold 0.50") {
		t.Fatalf("a clearing classification started at %q (%q), want the dear tier and the provisional threshold named", string(got.Tier), got.Reason)
	}
	// One failed attempt: a rung ABOVE the classified start.
	got, err = p.DeriveClassified(workFacts(), DeriveAttempt{Number: 2, Failed: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierStrong || !strings.Contains(got.Reason, "escalated 1 rung(s) after 1 failed attempt(s)") {
		t.Errorf("a failed attempt did not earn its rung above the classified start: %q (%q)", string(got.Tier), got.Reason)
	}
	// However many more failures: the ceiling holds, and says the ladder is
	// over.
	got, err = p.DeriveClassified(workFacts(), DeriveAttempt{Number: 9, Failed: 8}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierStrong || !strings.Contains(got.Reason, "the ladder is over") {
		t.Errorf("eight failures derived %q (%q), want the ceiling and the ladder over", string(got.Tier), got.Reason)
	}

	// A dear tier AT the ceiling is a classified start with no rungs above
	// it: the second failure is a person's, exactly as an earned one is.
	atCeiling := loadPolicy(t, strings.ReplaceAll(massPolicyDoc, "dear_tier = \"balanced\"", "dear_tier = \"strong\""))
	got, err = atCeiling.DeriveClassified(workFacts(), DeriveAttempt{Number: 2, Failed: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierStrong || !strings.Contains(got.Reason, "the next actor is a person") {
		t.Errorf("a classified start at the ceiling escalated: %q (%q)", string(got.Tier), got.Reason)
	}

	// And the ceiling bounds the RESULT mechanically, not only at load: a
	// hand-built policy whose dear tier sits above its ceiling is clamped
	// down, because a ceiling a promotion can walk around is not a ceiling.
	handBuilt := loadPolicy(t, massPolicyDoc)
	handBuilt.DearTier = TierFrontier
	got, err = handBuilt.DeriveClassified(workFacts(), DeriveAttempt{Number: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != TierStrong || !strings.Contains(got.Reason, "which still bounds the result") {
		t.Errorf("a dear tier above the ceiling derived %q (%q), want the ceiling", string(got.Tier), got.Reason)
	}

	// Purity, the acceptance's own shape: the same tick, the same attempt,
	// the same classification, twice — byte for byte, reason included.
	first, err := p.DeriveClassified(workFacts(), DeriveAttempt{Number: 2, Failed: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.DeriveClassified(workFacts(), DeriveAttempt{Number: 2, Failed: 1}, clears)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the classified derivation is not a function: %+v then %+v", first, second)
	}
}
