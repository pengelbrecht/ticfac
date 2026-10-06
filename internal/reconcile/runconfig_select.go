package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The run's config selection (tick tda): which named run config — one of
// the [configs.<name>] tables the target repository's runners files declare
// — this run routes every dispatch under, and where the choice came from.
//
// One precedence, resolved ONCE at construction and never per dispatch:
//
//	--config on the command line
//	  > the epic's own `config:` label (set when the epic is designed)
//	    > the [configs] default the runners files declare
//
// The precedence is the operator's own design (tick tda): a flag is a
// person's word for this one run, an epic's label is the design's word for
// every run of that epic, and the default is the repository's word for every
// run that said neither. The flag overriding the epic's choice is the point:
// a run diagnosing a config's behaviour wants the other config without
// editing the tracker.
//
// The selection is a construction fact, not a per-dispatch judgement, for the
// same reason the pinned tier is: a config changes which harness runs the
// work and what it costs, and a run that could not say WHICH config routed a
// given attempt is a run whose escalation and cost numbers mean less than
// they look like they mean. The chosen name is therefore resolved before any
// profile is, threaded through every resolution the run makes
// (profile.Options.Config), and named in the feed at the start of every
// incarnation — the durable record the status model reads the selection from.
//
// A repository whose runners files declare no named configs at all — every
// file until tick tda — selects nothing and runs exactly as it always did:
// the merged document's own cells, the historical single routing. But a
// repository that DOES declare them refuses a selection of a name nobody
// declared ([runconfig.ErrNoSuchConfig]) rather than falling back to the
// file's own cells, because an operator who asked for claude and silently
// got GLM would be an operator whose epic ran on a routing nobody chose.

// configLabelPrefix is the label namespace an epic's own config choice rides
// in, the way tier overrides ride "tier:" and file declarations "touch:".
// A label is a weakly typed field the tracker cannot validate, so it is
// parsed here or nowhere.
const configLabelPrefix = "config:"

// RunConfigSelection is the config a run routes under, and the source of the
// choice — provenance the run states about itself, the way it states its
// substrate and its pinned tier.
type RunConfigSelection struct {
	// Name is the selected config, "" when the runners files declare no
	// named configs at all (the historical single-routing file).
	Name string
	// Source names where the choice came from, for the feed line and the
	// status model: "the --config flag", "the epic's config: label", or
	// "the [configs] default" — "" beside an empty Name.
	Source string
}

// Selected reports whether a named config was selected at all.
func (s RunConfigSelection) Selected() bool { return s.Name != "" }

// Detail is the selection as one feed line: what routes the run and who
// chose it, so a reader of the run's own journal can tell a designed choice
// (the epic's label) from a diagnostic one (the flag) from the repository's
// standing answer (the default).
func (s RunConfigSelection) Detail() string {
	if !s.Selected() {
		return ""
	}
	return fmt.Sprintf("run config %s — selected by %s", s.Name, s.Source)
}

// parseConfigLabels reads an epic's `config:<name>` labels: the first one,
// and an error when the epic carries two that disagree — one epic has one
// config, and a run that had to guess which would be a run guessing at what
// the whole epic runs on.
func parseConfigLabels(epicID string, labels []string) (string, error) {
	found, chosen := false, ""
	for _, raw := range labels {
		value, ok := strings.CutPrefix(raw, configLabelPrefix)
		if !ok {
			continue
		}
		name := strings.TrimSpace(value)
		if found && name != chosen {
			return "", fmt.Errorf("epic %s carries two %s labels (%s and %s): one epic has one config", epicID, configLabelPrefix, chosen, name)
		}
		found, chosen = true, name
	}
	return chosen, nil
}

// selectRunConfig resolves the precedence for one run: the flag over the
// epic's label over the default. The named configs it chooses between are
// the merged document's own — the common file with the substrate's override
// merged over it, exactly what the run's profile resolutions read.
func selectRunConfig(opts Options, substrate runconfig.Substrate, epicLabels []string) (RunConfigSelection, error) {
	cfg, err := runconfig.LoadFor(opts.GateConfig, substrate)
	if err != nil {
		return RunConfigSelection{}, err
	}
	declared := cfg.NamedConfigNames()

	fromLabel, labelErr := parseConfigLabels(opts.EpicID, epicLabels)
	if labelErr != nil {
		return RunConfigSelection{}, labelErr
	}

	// The whole-document rules already guarantee a default when configs are
	// declared; the case-missing arm is a file the loader refused, which is
	// a construction error before this one.
	if len(declared) == 0 {
		switch {
		case opts.RunConfig != "":
			return RunConfigSelection{}, fmt.Errorf("reconcile: --config %s names a run config, and the runners files declare no named configs at all — declare [configs.%s] in .tick/runners.toml (or the substrate's override file), or run without the flag", opts.RunConfig, opts.RunConfig)
		case fromLabel != "":
			return RunConfigSelection{}, fmt.Errorf("reconcile: epic %s carries the label %s%s, and the runners files declare no named configs at all — declare [configs.%s] in .tick/runners.toml (or the substrate's override file), or remove the label", opts.EpicID, configLabelPrefix, fromLabel, fromLabel)
		}
		// No configs declared, nothing selecting: the historical
		// single-routing run, exactly as before tick tda.
		return RunConfigSelection{}, nil
	}

	chosen, source := "", ""
	switch {
	case opts.RunConfig != "":
		chosen, source = opts.RunConfig, "the --config flag"
	case fromLabel != "":
		chosen, source = fromLabel, fmt.Sprintf("the epic's %s label", configLabelPrefix)
	default:
		chosen, source = cfg.DefaultConfigName(), "the [configs] default"
	}
	if _, ok := cfg.NamedConfig(chosen); !ok {
		return RunConfigSelection{}, fmt.Errorf("reconcile: the run config %q is not one the runners files declare (%s) — %s named it; declare [configs.%s] or fix the selection", chosen, strings.Join(declared, ", "), source, chosen)
	}
	return RunConfigSelection{Name: chosen, Source: source}, nil
}

// epicConfigLabels reads the epic's own labels through the tracker, at
// construction, so the selection is a fact about the run before anything is
// dispatched. A tracker read that fails is a construction refusal: a
// `config:` label this run cannot read is a selection it cannot resolve.
func epicConfigLabels(ctx context.Context, opts Options) ([]string, error) {
	epic, err := opts.Tracker.Show(ctx, opts.EpicID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: read the epic %s for its own config label: %w", opts.EpicID, err)
	}
	return epic.Labels, nil
}

// StageConfigSelected is the feed line a run's chosen config writes at the
// start of every incarnation (tick tda): run-level, like `resumed`, because
// the selection belongs to the run and not to any one tick's dispatch — and
// written every incarnation because the status model derives the config
// from the LAST such line, so a resume states the config it still runs on
// rather than inheriting a line from a feed a fresh clone may not hold.
const StageConfigSelected = "config_selected"

// checkSelectedConfigCanRoute is the run-start half of the preflight (tick
// tda): the selected config's every resolved worker must be routable — and
// a config whose workers ride a subscription rung needs the factory to hold
// at least one subscription token, because a run that asked for claude and
// found no subscription configured is a run that would silently step every
// dispatch down to Workers AI, which is the "paid for X and got Y" failure
// the rung's own fallback (all leases busy) exists to handle and this check
// exists not to be confused with.
//
// The subscription answer comes from the seam the CLI wires to the deployed
// factory's own report (factory.ReadDeployed — labels only, never values).
// A nil seam leaves the check to the surfaces that CAN ask: doctor and the
// cloud submission preflight, which read the same report for the same
// reason; the factory's own in-cloud orchestrator is the authority itself
// and does not preflight its own secrets through a second door.
func checkSelectedConfigCanRoute(opts Options, selection RunConfigSelection, jobs []RoutedJob) error {
	if !selection.Selected() {
		return nil
	}
	rungRoles := rungRiders(jobs)
	if len(rungRoles) == 0 || opts.SubscriptionTokens == nil {
		return nil
	}
	labels, err := opts.SubscriptionTokens()
	if err != nil {
		// A factory that cannot be asked is not a fact about the config:
		// doctor and the submission preflight report it where the fix is,
		// and the run's own dispatches step down to Workers AI exactly as
		// the rung's fallback designs. The check is a preflight, not a
		// dependency on a network read at construction.
		return nil
	}
	if len(labels) > 0 {
		return nil
	}
	return fmt.Errorf("reconcile: the selected run config %q routes %s on the subscription rung (claude on its versionless aliases), and no subscription is configured on the factory — the run would silently step every one of those dispatches down to Workers AI. Put the rung on: `wrangler secret put CLAUDE_SUB_TOKEN_<LABEL>` on the factory (the labels the factory answers with are what a lease is taken against)",
		selection.Name, strings.Join(rungRoles, ", "))
}

// rungRiders lists the "role (tier)" strings of the jobs whose resolved
// worker rides a subscription rung — the jobs a missing subscription token
// would silently re-route.
func rungRiders(jobs []RoutedJob) []string {
	var out []string
	for _, job := range jobs {
		if job.Profile == nil {
			continue
		}
		if _, rung := profile.SubscriptionRungFor(job.Profile.Runner, job.Profile.Model); rung {
			at := job.Role
			if job.Tier != "" {
				at = fmt.Sprintf("%s (tier %s)", job.Role, job.Tier)
			}
			out = append(out, at)
		}
	}
	return out
}

// resolvedJobsForPreflight collects the jobs a run's construction resolved
// — every role at its own values and at every derivable tier, plus the
// on-demand ones — into the [RoutedJob] shape the selected-config preflight
// reads. It is a view over what construction already resolved, not a second
// resolution: the question is whether the config the run selected can serve
// the workers it just built, and the answer must be about those exact
// profiles.
func resolvedJobsForPreflight(profiles map[string]*profile.Profile, tierProfiles map[string]map[string]*profile.Profile, onDemand []RoutedJob) []RoutedJob {
	jobs := append([]RoutedJob{}, onDemand...)
	for _, role := range profile.Roles {
		jobs = append(jobs, RoutedJob{Role: role, Profile: profiles[role]})
		for tier, p := range tierProfiles[role] {
			if tier == "" || p == nil {
				// The "" entry IS the role's own values — the job appended
				// above — and a nil profile is nothing to route.
				continue
			}
			jobs = append(jobs, RoutedJob{Role: role, Tier: tier, Profile: p})
		}
	}
	return jobs
}
