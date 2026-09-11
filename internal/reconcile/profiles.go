package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/config"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The profile side of a dispatch: which profile a role is dispatched under,
// what that profile is allowed to be, and what it decides about the job.
//
// A profile is exactly {executor, runner, model, prompt} (SPEC §4.5, Phase 1),
// and this is where those four meet the protocol. The runner is executor
// CONFIGURATION — the JobSpec's records are closed, so a runner field invented
// on this side would be a field the executor's contract does not have. The
// model and the prompt are carried in the profile's digest and in the
// provenance of every record the dispatch produces, which is what makes "under
// which profile was this decided" answerable after the fact.

// profileFor is the profile one role is dispatched under. A role this phase
// ships no profile for is implement-tick's — the same rule RoleOf applies to a
// task nobody classified: unclassified work is work.
func (r *Reconciler) profileFor(role string) *profile.Profile {
	if p, ok := r.profiles[role]; ok && p != nil {
		return p
	}
	return r.profiles["implement-tick"]
}

// ------------------------------------------------- the tier derivation (5eq) ---

// profileForTier is the profile one role is dispatched under AT ONE TIER.
// The empty tier is the role's own base values — every dispatch's tier when
// no policy is declared. Anything the construction-time pre-resolution could
// see is answered from it; a tier it could not (a label override naming a
// tier the policy never routes to) is resolved here, on the dispatch's own
// authority, and a failure of that resolution is the caller's LOUD refusal:
// a tick carrying a tier label the roles table declares nothing for is a
// config error naming the tick and the label, never a silent fall-back.
func (r *Reconciler) profileForTier(role, tier string) (*profile.Profile, error) {
	if tier == "" {
		return r.profileFor(role), nil
	}
	// A role this phase ships no profile for is implement-tick's — the same
	// fallback profileFor applies, so a tier rides on the profile the dispatch
	// actually uses rather than one that does not exist.
	if _, ok := r.profiles[role]; !ok {
		role = "implement-tick"
	}
	if per, ok := r.tierProfiles[role]; ok {
		if p, ok := per[tier]; ok && p != nil {
			return p, nil
		}
	}
	// Not pre-resolved: either the run pins a tier (--tier, no policy) or the
	// tier came off a tick label the policy never names. Both resolve here so
	// the refusal names the config that failed, not the moment it failed; the
	// caller — which knows the tick and the label — makes it loud.
	p, err := profile.Resolve(role, profile.Options{
		Dir: r.opts.ProfileDir, RunnersConfig: r.opts.GateConfig, Tier: tier,
	})
	if err != nil {
		return nil, err
	}
	if err := usableProfile(p); err != nil {
		return nil, err
	}
	if r.tierProfiles == nil {
		r.tierProfiles = map[string]map[string]*profile.Profile{}
	}
	if per, ok := r.tierProfiles[role]; ok {
		per[tier] = p
	} else {
		r.tierProfiles[role] = map[string]*profile.Profile{tier: p}
	}
	return p, nil
}

// deriveTier is where the orchestrator stops CHOOSING a tier and starts
// DERIVING one (tick 5eq): a pure function of the tick's facts, the attempt's
// own durable state, and the declared policy. The only thing that outranks it
// is an operator's explicit pin (--tier), which is recorded as exactly that.
//
// The BUDGET is deliberately not an input, and that is the answer to "which
// wins": neither. The tier routes MODELS, the budget clamps DOLLARS, and a
// dispatch at a derived tier is issued the effective budget whatever the
// tier. Where the two meet — a clamped budget beside an escalated tier — the
// dispatch's own record states both numbers rather than downgrading either.
func (r *Reconciler) deriveTier(entry planEntry, number, failed int) (string, string, error) {
	if r.pinnedTier != "" {
		return r.pinnedTier, "the operator pinned this tier for every dispatch of the run (--tier); the ladder does not run", nil
	}
	outcome, err := r.tierPolicy.Derive(
		config.TickFacts{
			TickID: entry.TickID, Priority: entry.Priority, Type: entry.Type,
			Role: entry.Role, Labels: entry.Labels, Wave: entry.Wave, Blocks: entry.Blocks,
		},
		config.DeriveAttempt{Number: number, Failed: failed},
	)
	if err != nil {
		return "", "", err
	}
	return string(outcome.Tier), outcome.Reason, nil
}

// derivableTiers is the set of tiers the declared policy can ever route a
// dispatch of ONE ROLE to, for construction-time pre-resolution: every start
// the role can legitimately take plus the rungs a failed attempt can climb
// from it, and the declared route when there is one. An empty set is
// legitimate — no reachable overlays, base profiles only.
//
// The set is computed PER ROLE, because a policy is only honest when what it
// pre-resolves is what it can actually derive: pre-resolving the work default
// for a review role would demand a [roles.review.tiers.balanced] the
// operator was never asked to declare, and refusing a run over it would be a
// refusal nothing in the policy justified.
func (r *Reconciler) derivableTiers(role string) (map[config.Tier]bool, error) {
	out := map[config.Tier]bool{}
	p := r.tierPolicy
	if p == nil {
		return out, nil
	}
	if !isKnownTier(string(p.Default)) {
		return nil, fmt.Errorf("[tier_policy] declares no valid default tier, so no dispatch of this run can be routed")
	}
	ceilingIdx := tierIndexOf(p.CeilingOrDefault())
	if ceilingIdx < 0 {
		return nil, fmt.Errorf("[tier_policy] declares a ceiling this vocabulary has no tier for")
	}
	addLadder := func(start config.Tier) {
		for i := tierIndexOf(start); i >= 0 && i <= ceilingIdx; i++ {
			out[config.TierNames[i]] = true
		}
	}
	// The rungs a FAILED attempt can climb from each start a rule can give
	// this role — a rule matches it when it states no roles or states one of
	// the role's spellings.
	for _, rule := range p.Start {
		if rule == nil {
			continue
		}
		if !ruleCouldMatchRole(rule, role) {
			continue
		}
		addLadder(rule.Tier)
	}
	// The declared route, when there is one — a route is pinned, no ladder.
	// The lookup goes through the role's every spelling, the same aliases the
	// derivation resolves through.
	route, routed := config.Tier(""), false
	for _, name := range roleAliasNames(role) {
		if tier, ok := p.Roles[name]; ok {
			route, routed = tier, true
		}
	}
	if routed && route != "" {
		out[route] = true
	} else if routed { // an explicit empty route: base values, deliberately
	} else if role == "" || role == "implement-tick" {
		// Only WORK starts at the default; a process role without a route
		// runs at base values (tierpolicy.go's isWorkRole rule).
		addLadder(p.Default)
	}
	return out, nil
}

// ruleCouldMatchRole reports whether a start rule's roles dimension can
// include this role: a rule that states no roles matches every tick, and one
// that states roles matches the ones it names — in either spelling.
func ruleCouldMatchRole(rule *config.TierStartRule, role string) bool {
	if len(rule.Roles) == 0 {
		return true
	}
	for _, stated := range rule.Roles {
		if strings.EqualFold(stated, role) {
			return true
		}
	}
	// The aliases: a rule naming "implement" matches implement-tick, one
	// naming "review" matches review-epic, and so on — the same aliases the
	// derivation itself resolves through config.roleAliases, mirrored here
	// because that map is the config package's own.
	for _, alias := range roleAliasNames(role) {
		for _, stated := range rule.Roles {
			if strings.EqualFold(stated, alias) {
				return true
			}
		}
	}
	return false
}

func roleAliasNames(role string) []string {
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
		return nil
	}
}

// recordTierPolicy writes the run-level tier lines into the journal, at
// admission, where an operator reads them while the run can still be
// cancelled cheaply: the rate-limit stance the policy declares, the per-wave
// width the per-tier bounds narrow, and — when the operator pinned a tier —
// the statement that the ladder does not run. The reconciler itself
// dispatches at concurrency one, so the narrowed width is a number this
// record makes legible for the hosts (and the operator driving them) that
// carry the wave's real parallelism.
func (r *Reconciler) recordTierPolicy(plan []planEntry) {
	if r.pinnedTier != "" {
		r.record("", StageRunFinished, "the operator pinned tier %q for every dispatch of this run (--tier): the per-tick derivation and its ladder do not run", r.pinnedTier)
		return
	}
	if r.tierPolicy == nil {
		return
	}
	stance := r.tierPolicy.RateLimitOrDefault()
	r.record("", StageRunFinished,
		"the declared tier policy answers a provider rate limit by backing off and retrying (up to %d attempts, at most %dms apart): a 429 is a pause, not a death — an attempt that still cannot settle is a refusal for a person, never an abandoned worker",
		stance.MaxAttempts, stance.MaxDelayMs)

	// Per-wave width: derive each wave's tiers as a first attempt would (the
	// width is a planning number; a wave's dispatches may escalate later, and
	// an escalated rung narrows nothing retroactively).
	byWave := map[int][]config.Tier{}
	for _, entry := range plan {
		outcome, err := r.tierPolicy.Derive(config.TickFacts{
			TickID: entry.TickID, Priority: entry.Priority, Type: entry.Type,
			Role: entry.Role, Labels: entry.Labels, Wave: entry.Wave, Blocks: entry.Blocks,
		}, config.DeriveAttempt{Number: 1})
		if err != nil {
			// A bad label is refused at the tick's own dispatch, loudly; the
			// planning line does not pre-empt it with a partial answer.
			continue
		}
		byWave[entry.Wave] = append(byWave[entry.Wave], outcome.Tier)
	}
	waves := make([]int, 0, len(byWave))
	for wave := range byWave {
		waves = append(waves, wave)
	}
	sort.Ints(waves)
	for _, wave := range waves {
		tiers := byWave[wave]
		width := r.tierPolicy.WaveWidth(r.hostWidth, tiers)
		if width == r.hostWidth {
			continue // no declared bound narrows this wave
		}
		if r.hostWidth > 0 {
			r.record("", StageRunFinished,
				"wave %d routes %d dispatch(es) across tiers the policy bounds, so the declared width of %d narrows to %d per [tier_policy.concurrency]",
				wave, len(tiers), r.hostWidth, width)
		} else {
			r.record("", StageRunFinished,
				"wave %d routes %d dispatch(es) across tiers the policy bounds, and [orchestration].max_parallel declares no host width, so the per-tier bounds alone cap it at %d",
				wave, len(tiers), width)
		}
	}
}

// profileOfMarker is the profile the dispatch a marker records ran under:
// the role at the TIER the dispatch derived (or pinned), so the gate's
// fingerprint and the evidence it places name the profile that actually
// dispatched the attempt rather than the role's base one. A fingerprint that
// named a profile the dispatch never used would be a fingerprint nobody
// could reproduce from the attempt's own record.
func (r *Reconciler) profileOfMarker(marker attemptHandle) (*profile.Profile, error) {
	return r.profileForTier(marker.Role, marker.Tier)
}

func tierIndexOf(tier config.Tier) int {
	for i, known := range config.TierNames {
		if tier == known {
			return i
		}
	}
	return -1
}

func isKnownTier(name string) bool {
	for _, tier := range config.TierNames {
		if string(tier) == name {
			return true
		}
	}
	return false
}

// usableProfile refuses a profile this build cannot honour, at construction.
//
// Both refusals are about the same thing: a profile is only worth recording if
// the run actually happened under it. A profile naming `herdr` that ran on the
// local subprocess executor would be provenance that lies, and a runner nothing
// here can launch is a dispatch that fails after the tracker has been claimed.
func usableProfile(p *profile.Profile) error {
	if p == nil {
		return fmt.Errorf("no profile resolved: nothing says which executor, runner, model and prompt a job is dispatched with")
	}
	if p.Executor != subprocess.ExecutorName {
		return fmt.Errorf("profile %s names executor %q and this phase has one, %s: a record naming an executor the "+
			"run did not use is provenance that lies", p.Role, p.Executor, subprocess.ExecutorName)
	}
	known := subprocess.KnownRunners()
	found := false
	for _, name := range known {
		if name == p.Runner {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("profile %s names runner %q, which is not one of %s: a dispatch that discovers this after "+
			"the tick is claimed has claimed a tick nothing will work on", p.Role, p.Runner, strings.Join(known, ", "))
	}
	// The model has to be APPLICABLE, not merely recorded. A runner this
	// executor cannot tell which model to use, handed one, would run its own
	// default while the attempt record, the evidence and the provenance all
	// named something else — so it is refused here, before a tick is claimed.
	if p.Model != "" && !runnerAcceptsModel(p.Runner) {
		return fmt.Errorf("profile %s routes model %q to runner %q, which this executor cannot tell which model to "+
			"use: a model recorded as applied and silently not applied is provenance that lies", p.Role, p.Model, p.Runner)
	}
	return nil
}

// runnerAcceptsModel is the executor's answer, behind a variable so the guard
// above can be exercised. Every runner this executor knows takes a model today,
// so that refusal has no reachable case in production — and a guard no test can
// reach is a guard nobody knows works.
var runnerAcceptsModel = subprocess.RunnerAcceptsModel

// promptDigest is the digest of the role prompt a dispatch was made with. It is
// what the marker carries instead of the prompt itself: the text is in the
// profile and in the executor's own attempt record, and what the run state
// needs is a value two records can be compared on.
func promptDigest(p *profile.Profile) string {
	if p == nil {
		return ""
	}
	return digestOf("role-prompt", p.Prompt)
}

// profileSetDigest is the digest of ALL the profiles a run was made under. A
// checkpoint is not about one role, so it names the set rather than picking one
// role's profile to stand for the run.
func profileSetDigest(profiles map[string]*profile.Profile) string {
	roles := make([]string, 0, len(profiles))
	for role := range profiles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles)*2)
	for _, role := range roles {
		parts = append(parts, role, profiles[role].Digest)
	}
	return digestOf(append([]string{"profiles"}, parts...)...)
}

// ------------------------------------------------------------ role jobs ---

// isRoleJob reports whether a tick's deliverable is an ANSWER rather than a
// change: review and closeout are jobs like any other, run through the same
// executor, but what the reconciler acts on is the role-result envelope they
// return and not a branch it merges.
func isRoleJob(role string) bool {
	return role == "review-epic" || role == "closeout-epic"
}

// sourceGradeFor is the grade the host issues source access at.
//
// SPEC §6.3 puts review-epic at the epic boundary, running READ-ONLY against
// the integrated ref: the executor issues it no push credential and launches
// its runner with every source credential stripped and its git configuration
// pinned so no push resolves to a remote (internal/exec/subprocess/grade.go),
// so a review that tried to advance a ref is refused by the issuer rather than by
// the model's good manners. A closeout writes — a retro and the learnings it
// compacts are its output — so it is dispatched at the write grade and its work
// is integrated and gated like any other.
func sourceGradeFor(role string) string {
	if role == "review-epic" {
		return "read-only"
	}
	return "write"
}

// outputSchemaFor is the role-specific contract the job's role_result.result
// must satisfy. It is named in the SPEC so that the reconciler validates
// against what it ASKED for rather than against what came back.
func outputSchemaFor(role string) string {
	return "ticfac.job-result." + role + ".v1"
}

// controllerBase is the state a role job is dispatched at: the integration
// branch as origin has it NOW, not the base the run started from. A review of
// an epic that reads the epic's pre-run state reviews something nobody
// integrated.
func (r *Reconciler) controllerBase() string {
	if err := r.git.fetch(r.branch); err != nil {
		return r.base
	}
	head, err := r.git.remoteHead(r.branch)
	if err != nil || head == "" {
		return r.base
	}
	if _, err := r.git.resolve(head); err != nil {
		return r.base
	}
	return head
}

// ------------------------------------------------------- the provenance ---

// attemptProvenance is what a dispatch's records say they were produced under.
//
// Three fields the run-level provenance leaves null are stated here, and each
// is a claim the contract has a slot for precisely because a record that cannot
// make it is not evidence: the ROLE that was dispatched, the MODEL the profile
// routed to, and the digest of THAT role's profile rather than of the run's set.
//
// The TIER a dispatch was derived under (tick 5eq) has no slot in the closed
// provenance object — $defs.provenance in the pinned contract bundle is
// additionalProperties:false with a fixed fourteen fields, so adding one is a
// bundle bump on the ticks side, not an edit here. Until that bump exists the
// tier is recorded in TWO places a dispatch already controls honestly: the
// attempt marker's open handle (attemptHandle.Tier, beside the model and the
// prompt digest, which sit there for exactly this reason) and this record's
// ProfileDigest — the digest is over the TIER-RESOLVED profile, so two
// dispatches at different tiers never digest the same, which answers "is
// this the same profile" but not "which tier was this". That question is
// what the marker's own tier field answers.
func (r *Reconciler) attemptProvenance(d Dispatch) runstate.Provenance {
	provenance := r.provenance(&d.TickID, &d.Attempt, phaseFor(d.Role), d.BaseSHA)
	role := d.Role
	provenance.Role = &role
	if d.Profile != nil {
		model, digest := d.Profile.Model, d.Profile.Digest
		provenance.Model = &model
		provenance.ProfileDigest = &digest
	}
	return provenance
}

// phaseFor is the gate vocabulary's phase for a dispatch. The same command
// against the same ref at a different phase answers a different question, so a
// review's records say `review` and a closeout's say `closeout` rather than all
// three saying `worker`.
func phaseFor(role string) runstate.Phase {
	switch role {
	case "review-epic":
		return runstate.PhaseReview
	case "closeout-epic":
		return runstate.PhaseCloseout
	default:
		return runstate.PhaseWorker
	}
}
