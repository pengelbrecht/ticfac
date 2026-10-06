package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The named-config preflight seams (tick tda): the two questions about the
// factory a config's routability depends on, as seams for the same reason
// every other probe in this package is one — a test must answer each with a
// controlled value, and the production value is the environment's.
//
// One question only: which subscription TOKEN LABELS the deployed factory
// holds. Never the values — a token that reaches a log line is a token
// burned, and the factory's own report carries the labels for exactly this
// reading.

// subscriptionTokensForRun is the reconciler's run-start preflight seam: the
// deployed factory's own answer, read through the same door `ticfac
// factory status` and doctor read it through. It answers the LABELS the
// factory's Worker holds CLAUDE_SUB_TOKEN_<LABEL> secrets for, so a run that
// selected a config riding the claude-sub rung can refuse at start when the
// rung is off — naming the `wrangler secret put` that turns it on — rather
// than silently stepping every dispatch down to Workers AI.
//
// A factory that cannot be asked answers an error, and the reconciler
// treats that as "not this preflight's question" (nil seam): doctor and the
// cloud submission preflight report the factory where the fix is.
func subscriptionTokensForRun() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	facts, _, err := factory.ReadDeployed(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	return facts.ClaudeSubLabels, nil
}

// doctorClaudeSubLabels is doctor's own seam over the same read, so the
// routing check's missing line is testable against a controlled factory
// answer exactly the way every other doctor check is.
var doctorClaudeSubLabels = subscriptionTokensForRun

// sortedConfigNames lists the configs' names in a stable order, for a
// doctor detail line that reads the same on every run.
func sortedConfigNames[V any](counts map[string]V) []string {
	out := make([]string, 0, len(counts))
	for name := range counts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// rungRiders is the per-config answer to "which of this config's jobs ride a
// subscription rung?" — the jobs a missing subscription token would
// silently re-route to Workers AI, keyed by the config that routed them.
type rungRiders map[string][]string

// rungRidersByConfig collects [rungRiders] from a set of per-config routed
// jobs: each job whose resolved worker is a rung's harness/alias pair rides,
// and the config that routed it is named beside it — the config is the thing
// an operator must fix, never the job.
func rungRidersByConfig(jobs []reconcile.RoutedJob) rungRiders {
	out := rungRiders{}
	for _, job := range jobs {
		if job.Profile == nil {
			continue
		}
		if _, rung := profile.SubscriptionRungFor(job.Profile.Runner, job.Profile.Model); !rung {
			continue
		}
		at := job.Role
		if job.Tier != "" {
			at = fmt.Sprintf("%s (tier %s)", job.Role, job.Tier)
		}
		config := job.Config
		if config == "" {
			config = "(the file's own cells)"
		}
		out[config] = append(out[config], at)
	}
	for _, config := range out {
		sort.Strings(config)
	}
	return out
}

// oneLine renders the riders for a refusal: each config with its jobs, in
// config order — "config claude rides implement-tick (tier economy), ...".
func (r rungRiders) oneLine() string {
	var parts []string
	for _, config := range sortedConfigNames(map[string][]string(r)) {
		parts = append(parts, fmt.Sprintf("config %s rides the claude-sub subscription rung for %s", config, strings.Join(r[config], ", ")))
	}
	return strings.Join(parts, "; ")
}
