package runconfig

import (
	"fmt"
	"sort"
	"strings"
)

// The DERIVATION half of [tier_policy]: a pure function from (tick facts,
// attempt number, policy) to a tier. Nothing in this file reads a clock, rolls
// a die or asks a model — that is what makes the policy testable rather than
// aspirational, and what makes an over-tiered run a configuration defect that
// can be named instead of a judgement call that can only be regretted.
//
// THE LADDER (the decision, logged — tick 5eq):
//
//	A first attempt starts at the START tier: the first matching
//	[[tier_policy.start]] rule, else the Default. No rule may name a tier
//	above the Default — nothing starts high on a description. Expense is
//	earned by failure.
//
//	Each FAILED prior attempt of the same tick earns Step rungs (default one)
//	upward from the start tier, and never past the Ceiling. At the Ceiling the
//	ladder is over by declaration: the next actor is a person, not a bigger
//	model.
//
// WHAT "FAILED" MEANS for the ladder — and the three things it deliberately
// does not mean:
//
//	FAILED: an attempt of this tick that the run REJECTED and left nothing
//	mergeable — the redispatch disposition. The worker was dispatched, it
//	had its chance, and the work it left did not pass. That is evidence about
//	the work, and evidence is the only thing that buys a rung.
//
//	NOT failed — a REFUSAL the run made before or around the worker (an
//	unaddressed or wiped attempt, a held unit, a collect the executor could
//	not address): no evidence about the work was produced, so no rung is
//	earned. Those attempts are adopted or held, not redispatched at a dearer
//	tier. The one durable exception: an attempt a PERSON released via settle
//	earns nothing either — the human was the actor.
//
//	NOT failed — a GATE FAILURE over work already merged: the attempt is
//	ADOPTED and re-gated at the SAME tier, because the gate is about the tree
//	and the check, not about the model that wrote the tree. Fixing the check
//	is a person's move; paying for a bigger model to await it is not a
//	routing decision at all.
//
//	NOT failed, and stated as a limitation — a worker that escalated to a
//	HUMAN (a BLOCKED or NEEDS_CONTEXT answer) leaves the same durable shape
//	as one whose work failed, so a redispatch after a human has answered
//	earns a rung like any other. The ladder is bounded by the Ceiling, so a
//	human escalation costs at most (Ceiling − start) rungs, and past the
//	Ceiling the answer is a person — which is where a human escalation was
//	always going.
//
// OVERRIDE: a label in the policy's namespace (default "tier:") pins the tier
// for that tick, whatever the ladder says. A label is a weakly typed field —
// the tracker stores it as an opaque string and cannot validate it — so the
// refusal for an unrecognised one is LOUD, naming the tick and the label,
// and never a silent fall-back to the default: a typo that quietly dispatched
// economy would be an override nobody can see failed. Two tier labels on one
// tick are refused the same way — an operator who wrote both meant one thing
// and the file has to say which.
//
// THE BUDGET CLAMP is not an input. Derivation routes MODELS; the budget
// clamps DOLLARS; neither silently modifies the other. A dispatch at a
// derived tier is issued the effective budget whatever the tier, and a budget
// in force is stated beside the tier in the dispatch's own record rather than
// downgrading anything quietly.

// TickFacts are the tracker facts one derivation is a function of — and
// nothing else. Every field is a fact the tracker already carries and the tk
// client already decodes; routing adds nothing to a tick.
type TickFacts struct {
	TickID   string
	Priority int
	Type     string
	// Role is the tick's process role in job-protocol spelling
	// ("implement-tick", "review-epic", "closeout-epic", "plan-epic").
	Role   string
	Labels []string
	// Wave is the tick's wave in the epic graph, 1-based. 0 means the graph
	// placed it nowhere, and a rule that names a wave does not match it — an
	// unknown position never earns a cheap tier by guessing.
	Wave int
	// Blocks is how many ticks this one blocks.
	Blocks int
}

// DeriveAttempt is the dispatch's own state as the derivation sees it. Both
// numbers are durable facts of the run, so the same tick at the same attempt
// state under the same policy always derives the same tier.
type DeriveAttempt struct {
	// Number is the 1-based attempt number of THIS dispatch.
	Number int
	// Failed is how many prior attempts of this tick FAILED by the ladder's
	// definition (see the package comment) — the redispatch disposition, not
	// every rejection. It can never exceed Number-1: a rung is earned only
	// by an attempt that existed.
	Failed int
}

// DeriveOutcome is what a derivation concluded, and — because "why was this
// expensive" is the question the tier exists to make answerable — why.
type DeriveOutcome struct {
	// Tier is the derived tier. The empty string is a real answer: "no
	// overlay, the role's own base values", which is every dispatch's tier
	// when no [tier_policy] is declared and every unlisted process role's.
	Tier Tier
	// Reason names the ladder's answer in one line, for the dispatch record.
	Reason string
}

// tierLabel is one label in the policy's namespace, parsed: the label as
// written, and the tier it names.
type tierLabel struct {
	label string
	tier  Tier
}

// Derive is the pure function: (tick facts, attempt, policy) → tier.
//
// A nil policy is legitimate and answers the empty tier for every tick that
// carries no tier label: a repository that declares no policy runs every
// dispatch at the role's own base values — which is also why nothing can
// start high in it. A tick that DOES carry a tier label under a nil policy is
// refused loudly rather than interpreted against a policy that does not exist.
func (p *TierPolicy) Derive(facts TickFacts, attempt DeriveAttempt) (DeriveOutcome, error) {
	if attempt.Number < 1 {
		return DeriveOutcome{}, fmt.Errorf("tier derivation for %s: attempt number %d is not 1-based", facts.TickID, attempt.Number)
	}
	if attempt.Failed < 0 || attempt.Failed > attempt.Number-1 {
		return DeriveOutcome{}, fmt.Errorf("tier derivation for %s: %d failed prior attempts at attempt number %d cannot be (a rung is earned only by an attempt that existed)",
			facts.TickID, attempt.Failed, attempt.Number)
	}

	// The override first: it is the one input that outranks the ladder, and
	// the one input whose failure mode must be loudest.
	label, err := p.overrideLabel(facts)
	if err != nil {
		return DeriveOutcome{}, err
	}
	if label.tier != "" {
		// The override outranks the ladder under a nil policy too: the tier
		// vocabulary is closed regardless of whether a repository declares a
		// ladder, so a KNOWN tier on a label is an explicit route and an
		// UNKNOWN one is the loud refusal below. Whether the target repo's
		// roles table declares the tier is the dispatch's question, refused
		// there with the tick and the label in the message.
		return DeriveOutcome{Tier: label.tier, Reason: fmt.Sprintf("label %q overrides the ladder for this tick", label.label)}, nil
	}

	if p == nil {
		return DeriveOutcome{Tier: "", Reason: "no [tier_policy] is declared: the dispatch runs at the role's own base values"}, nil
	}

	// A process role with a declared route runs at it, and the ladder does
	// not run: the route is policy, evaluated, not a hunch escalated.
	if tier, ok := p.roleTier(facts.Role); ok {
		return DeriveOutcome{Tier: tier, Reason: fmt.Sprintf("the declared route for role %s", facts.Role)}, nil
	}

	// The START tier: the first matching rule, else — for WORK only — the
	// Default. A process role with no route and no rule naming it runs at the
	// role's own base values: the default is where WORK starts, and a review
	// or a closeout is a declared route or it is the base profile, never a
	// work tier the roles table was never asked to declare.
	start, rule, matched := p.startTier(facts)
	if !matched && !isWorkRole(facts.Role) {
		return DeriveOutcome{Tier: "", Reason: fmt.Sprintf("role %s has no [tier_policy.roles] route and no start rule names it: it runs at the role's own base values", facts.Role)}, nil
	}
	if !matched {
		start, rule = p.Default, "the policy default"
	}
	step := p.StepOrDefault()
	ceiling := p.CeilingOrDefault()

	if attempt.Failed == 0 {
		return DeriveOutcome{Tier: start, Reason: "the first attempt starts at " + rule}, nil
	}
	if tierIndex(start) >= tierIndex(ceiling) {
		return DeriveOutcome{Tier: start, Reason: fmt.Sprintf("%s, and %d failed attempt(s) cannot escalate past the ceiling %q: the next actor is a person",
			rule, attempt.Failed, string(ceiling))}, nil
	}
	rungs := attempt.Failed * step
	if rungs > tierIndex(ceiling)-tierIndex(start) {
		escalated := ceiling
		return DeriveOutcome{Tier: escalated, Reason: fmt.Sprintf("%s, escalated to the ceiling %q after %d failed attempt(s): the ladder is over, and the next actor is a person",
			rule, string(ceiling), attempt.Failed)}, nil
	}
	escalated := TierNames[tierIndex(start)+rungs]
	return DeriveOutcome{Tier: escalated, Reason: fmt.Sprintf("%s, escalated %d rung(s) after %d failed attempt(s) (ceiling %q)",
		rule, rungs, attempt.Failed, string(ceiling))}, nil
}

// overrideLabel reads the tick's labels for the policy's namespace. Exactly
// one tier label may be present; anything else is a refusal naming the tick
// and the label.
func (p *TierPolicy) overrideLabel(facts TickFacts) (tierLabel, error) {
	prefix := p.LabelPrefixOrDefault()
	var found []tierLabel
	for _, raw := range facts.Labels {
		value, ok := strings.CutPrefix(raw, prefix)
		if !ok || value == "" {
			continue
		}
		found = append(found, tierLabel{label: raw, tier: Tier(value)})
	}
	if len(found) == 0 {
		return tierLabel{}, nil
	}
	if len(found) > 1 {
		labels := make([]string, 0, len(found))
		for _, one := range found {
			labels = append(labels, fmt.Sprintf("%q", one.label))
		}
		return tierLabel{}, fmt.Errorf("tick %s carries %d tier labels (%s): exactly one may name a tier, or a dispatch cannot say which of them it honoured",
			facts.TickID, len(found), strings.Join(labels, ", "))
	}
	one := found[0]
	if !isKnownTier(string(one.tier)) {
		return tierLabel{}, fmt.Errorf("tick %s carries label %q, which is not one of the tiers %s: a tier label is a weakly typed field the tracker cannot validate, so this refusal is the only place a typo can surface — fix the label",
			facts.TickID, one.label, tierList())
	}
	return one, nil
}

// roleTier reads the declared route for a process role. Both spellings the
// ecosystem uses are accepted, as everywhere else roles are named.
func (p *TierPolicy) roleTier(role string) (Tier, bool) {
	for _, name := range roleAliases(role) {
		if tier, ok := p.Roles[name]; ok {
			return tier, true
		}
	}
	return "", false
}

// roleAliases spells a job-protocol role every way the runners.toml ecosystem
// spells it, most specific first.
func roleAliases(role string) []string {
	switch role {
	case "implement-tick":
		return []string{"implement-tick", "implement"}
	case "review-epic":
		return []string{"review-epic", "review"}
	case "closeout-epic":
		return []string{"closeout-epic", "closeout", "close-out"}
	case "plan-epic":
		return []string{"plan-epic", "plan"}
	default:
		return []string{role}
	}
}

// startTier is the START tier: the first matching rule's tier when one
// matches, else the Default. The third return says which. The restriction —
// no rule may name a tier above the Default — is enforced at load, so the
// ladder can only ever start where a first attempt is allowed to.
func (p *TierPolicy) startTier(facts TickFacts) (Tier, string, bool) {
	for i, rule := range p.Start {
		if rule == nil || !rule.matches(facts) {
			continue
		}
		return rule.Tier, fmt.Sprintf("start rule %d of [tier_policy.start]", i+1), true
	}
	return p.Default, "the policy default", false
}

// isWorkRole reports whether a role is WORK — the thing the Default is the
// default OF. Everything else (review-epic, closeout-epic, plan-epic…) is a
// process role: routed explicitly or run at its base values, never loaned a
// work tier. The empty role is work: an unclassified task is a task.
func isWorkRole(role string) bool {
	return role == "" || role == "implement-tick"
}

// matches reports whether every stated dimension of the rule matches the
// tick's facts. A dimension that is not stated does not constrain; a rule
// that states nothing matches everything and is the policy's own business.
func (r *TierStartRule) matches(facts TickFacts) bool {
	if len(r.Types) > 0 && !containsFold(r.Types, facts.Type) {
		return false
	}
	if len(r.Roles) > 0 {
		aliases := roleAliases(facts.Role)
		matched := false
		for _, stated := range r.Roles {
			for _, alias := range aliases {
				if strings.EqualFold(stated, alias) {
					matched = true
				}
			}
		}
		if !matched {
			return false
		}
	}
	if r.MinPriority != nil && facts.Priority < *r.MinPriority {
		return false
	}
	if r.MaxPriority != nil && facts.Priority > *r.MaxPriority {
		return false
	}
	if r.Wave != nil && (facts.Wave == 0 || facts.Wave != *r.Wave) {
		return false
	}
	if r.MinBlocks != nil && facts.Blocks < *r.MinBlocks {
		return false
	}
	if r.MaxBlocks != nil && facts.Blocks > *r.MaxBlocks {
		return false
	}
	return true
}

// WaveWidth narrows a host-wide dispatch width by the per-tier concurrency
// bounds the policy declares. The width of a wave is the TIGHTEST bound among
// the tiers its ticks route to: a wave carrying one frontier tick and three
// economy ones is not a four-wide wave for a provider whose frontier tier is
// capped at one. The bounds are per tier because a tier names a model and a
// model names a provider — the wave-2 finding that one host-wide number
// describes the wrong axis entirely.
//
// host is the host-wide cap ([orchestration].max_parallel); 0 means the host
// declares none, and the policy's own bounds are then the whole answer. A
// tier with no declared bound narrows nothing. A nil policy narrows nothing:
// without a policy there is one host-wide number and this function is the
// identity on it.
func (p *TierPolicy) WaveWidth(host int, tiers []Tier) int {
	if p == nil {
		return host
	}
	width := host
	present := map[Tier]bool{}
	for _, tier := range tiers {
		present[tier] = true
	}
	names := make([]Tier, 0, len(present))
	for tier := range present {
		names = append(names, tier)
	}
	sort.Slice(names, func(i, j int) bool { return tierIndex(names[i]) < tierIndex(names[j]) })
	narrowed := false
	for _, tier := range names {
		bound, ok := p.Concurrency[string(tier)]
		if !ok {
			continue
		}
		if !narrowed || bound < width {
			width, narrowed = bound, true
		}
	}
	if !narrowed {
		return host
	}
	if host > 0 && width > host {
		return host
	}
	return width
}

// StepOrDefault is the declared rungs per failed attempt, defaulting to one.
func (p *TierPolicy) StepOrDefault() int {
	if p == nil || p.Step <= 0 {
		return 1
	}
	return p.Step
}

// CeilingOrDefault is the named ceiling escalation never passes, defaulting
// to the Default — a ladder whose top is its bottom, whose second failure is
// a person's to answer.
func (p *TierPolicy) CeilingOrDefault() Tier {
	if p == nil {
		return ""
	}
	if p.Ceiling == "" {
		return p.Default
	}
	return p.Ceiling
}

// LabelPrefixOrDefault is the label namespace tier overrides ride in,
// defaulting to "tier:".
func (p *TierPolicy) LabelPrefixOrDefault() string {
	if p == nil || p.LabelPrefix == "" {
		return "tier:"
	}
	return p.LabelPrefix
}

// RateLimitOrDefault is the declared rate-limit stance, with the defaults
// the 2026-09-11 pi configuration on epic av8 established (8 attempts, 90s
// ceiling). The response is never defaulted to anything but
// "backoff-and-retry": it is the one value this package will enforce, and a
// default of silence would let a run answer a 429 with the immediate retries
// that made wave 2's worker dead.
func (p *TierPolicy) RateLimitOrDefault() TierRateLimit {
	stance := TierRateLimit{Response: "backoff-and-retry", MaxAttempts: 8, MaxDelayMs: 90000}
	if p == nil {
		return stance
	}
	if p.RateLimit == nil {
		return stance
	}
	if p.RateLimit.Response != "" {
		stance.Response = p.RateLimit.Response
	}
	if p.RateLimit.MaxAttempts > 0 {
		stance.MaxAttempts = p.RateLimit.MaxAttempts
	}
	if p.RateLimit.MaxDelayMs > 0 {
		stance.MaxDelayMs = p.RateLimit.MaxDelayMs
	}
	return stance
}

// tierIndex is a tier's position in the capability order, or -1 when the name
// is not a tier. Callers that have validated the name may index TierNames
// with it.
func tierIndex(tier Tier) int {
	for i, known := range TierNames {
		if tier == known {
			return i
		}
	}
	return -1
}

func containsFold(values []string, value string) bool {
	for _, one := range values {
		if strings.EqualFold(one, value) {
			return true
		}
	}
	return false
}

func tierList() string {
	parts := make([]string, 0, len(TierNames))
	for _, tier := range TierNames {
		parts = append(parts, string(tier))
	}
	return strings.Join(parts, ", ")
}
