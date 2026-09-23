package runconfig

import (
	"errors"
	"fmt"
)

// ErrNoConfig is returned by [Config.Resolve] when there is no config to
// resolve against. Routing is a config-only concept: without
// `.tick/runners.toml` the adapter's own tier mapping applies and there is
// nothing here to answer with.
var ErrNoConfig = errors.New("herd/config: no .tick/runners.toml")

// ErrNoCloudRouting is the refusal a cloud run gets for a role nobody
// declared cloud routing for (tick 84z). Under the cloud substrate the base
// cell is not a default — it is a routing for a different world, written for
// runs whose economics a container does not share — so its silent use is how
// a cloud run reaches a harness nobody chose. The refusal names the role and
// the `[roles.<name>.substrates.cloud]` cell that is missing; declaring that
// cell is the whole fix.
var ErrNoCloudRouting = errors.New("herd/config: no cloud routing declared for the role")

// Worker is the resolved routing for one role at one tier: the two explicit
// dimensions plus the escape hatch, with every inheritance step already
// applied.
//
// Empty Model or Effort means "the kind's own default" — the spawner emits no
// flag for them. Nil Args means the same for native args.
type Worker struct {
	// Role is the role that was asked for.
	Role string
	// ResolvedRole is the role the values came from. It differs from Role
	// only when Role had no entry and fell back to `implement`.
	ResolvedRole string
	// Tier is the tier that was asked for; "" when the caller named none.
	Tier Tier
	// TierApplied reports whether a `tiers.<Tier>` table actually contributed.
	// A tier the role does not define is not an error — it simply leaves the
	// role's own values in place.
	TierApplied bool
	// Substrate is the substrate the values were resolved for; "" when the
	// caller asked for the substrate-blind view.
	Substrate Substrate
	// SubstrateApplied reports whether a `substrates.<Substrate>` table
	// actually contributed. Under the cloud substrate it is always true —
	// absent cloud routing is a refusal, never a missing overlay silently
	// skipped.
	SubstrateApplied bool

	Kind   string
	Model  string
	Effort Effort
	Args   []string
}

// Resolve applies runners-config.md's resolution order for a tick with role
// R and chosen tier T, substrate-BLIND: the [roles] table exactly as written,
// with no substrate overlay applied. It is the historical view and the seam
// ticks' internal/sandbox reads through; a caller that knows which substrate
// the run executes on resolves through [Config.ResolveOn] instead, which is
// this order with the substrate overlay between the role's own values and
// any tier:
//
//  1. roles.R.substrates.<substrate> when the run's substrate declares one →
//     applied field-wise over the role's own values. Under the cloud
//     substrate an ABSENT overlay is a refusal naming the role
//     ([ErrNoCloudRouting]), never a fall back to the base cell — that fall
//     back is how a cloud run reached a claude process nobody chose. The
//     herdr and harness substrates have no such refusal: the base cells
//     were written for them, and an overlay is optional refinement there.
//  2. roles.R.tiers.T if present → for each of kind, model, effort, args
//     independently: the tier's value if it sets one, else the
//     substrate-adjusted role's. The tier is the more specific axis, so it
//     applies last.
//  3. Otherwise roles.R.kind + roles.R.model + roles.R.effort + roles.R.args.
//  4. If role R has no entry, resolve against `implement` by the same steps.
//
// kind, model and effort are scalars and override field-wise: a tier that
// sets only effort = "high" keeps the role's kind and model. Args replace,
// they never merge — a tier's args supersede the role's wholesale, because
// merging two argv lists whose flags may conflict is not well-defined.
//
// tier may be "" for "no tier chosen". An unknown tier name is an error, not
// a silent no-op.
func (c *Config) Resolve(role string, tier Tier) (Worker, error) {
	return c.ResolveOn("", role, tier)
}

// ResolveOn is [Config.Resolve] for one substrate: the per-substrate overlay
// (tick 84z) applies between the role's own values and any tier overlay, and
// — under the cloud substrate only — its absence is a refusal naming the
// role rather than a fall back. See [Resolve] for the order and
// [ErrNoCloudRouting] for the refusal.
func (c *Config) ResolveOn(sub Substrate, role string, tier Tier) (Worker, error) {
	if sub == "" {
		w, _, entry, err := c.resolveBase(role, tier)
		if err != nil {
			return Worker{}, err
		}
		return c.applyTier(w, entry, tier)
	}
	if sub == SubstrateAuto {
		return Worker{}, fmt.Errorf("herd/config: substrate %q is a policy, not a substrate: resolve the substrate (runconfig.Decide) before resolving roles against it", string(sub))
	}
	if !sub.Valid() {
		return Worker{}, fmt.Errorf("herd/config: %q is not a substrate a role can be resolved against (want one of %s)", string(sub), SubstrateList(true))
	}
	if c == nil || len(c.Roles) == 0 {
		// A cloud run with no routing at all is the loudest version of the
		// same refusal: the only routing that exists without a config is
		// whatever ships as written, and nothing here can say what that
		// would be safe to run in a container.
		if sub == SubstrateCloud {
			return Worker{}, fmt.Errorf("%w: no %s at all, so role %q has nowhere its cloud routing could be declared", ErrNoCloudRouting, FileName, role)
		}
		return Worker{}, ErrNoConfig
	}

	w, resolved, entry, err := c.resolveBase(role, tier)
	if err != nil {
		return Worker{}, err
	}
	variant := entry.Substrates[string(sub)]
	if variant == nil {
		if sub != SubstrateCloud {
			// herdr and harness: the base cells were written for the world
			// the run executes in, and an overlay is optional refinement.
			return w, nil
		}
		return Worker{}, fmt.Errorf("%w: [roles.%s] declares no [roles.%s.substrates.cloud] cell for role %q — a cloud run refuses rather than falling back to the role's own %q/%q", ErrNoCloudRouting, resolved, resolved, role, entry.Kind, entry.Model)
	}
	w.Substrate = sub
	w.SubstrateApplied = true
	if variant.Kind != "" {
		w.Kind = variant.Kind
	}
	if variant.Model != "" {
		w.Model = variant.Model
	}
	if variant.Effort != "" {
		w.Effort = variant.Effort
	}
	// Args REPLACE, never merge — the tier overlay's rule, verbatim.
	if variant.Args != nil {
		w.Args = variant.Args
	}
	// The tier is the more specific axis: it applies over the substrate
	// overlay, so a per-tier model still narrows a cloud role.
	return c.applyTier(w, entry, tier)
}

// resolveBase is the substrate-blind half of resolution: the role entry
// lookup (with the `implement` fallback) and the role's own values. The tier
// overlay is applied by [applyTier], separately, because the substrate
// overlay sits between the two.
func (c *Config) resolveBase(role string, tier Tier) (Worker, string, *Role, error) {
	if c == nil || len(c.Roles) == 0 {
		return Worker{}, "", nil, ErrNoConfig
	}
	if tier != "" && !isKnownTier(string(tier)) {
		return Worker{}, "", nil, fmt.Errorf("herd/config: unknown tier %q (want one of economy, balanced, strong, frontier)", string(tier))
	}

	resolved := role
	entry, ok := c.Roles[role]
	if !ok || entry == nil {
		resolved = RoleImplement
		entry, ok = c.Roles[RoleImplement]
		if !ok || entry == nil {
			return Worker{}, "", nil, fmt.Errorf("herd/config: role %q has no entry and [roles.implement] is missing", role)
		}
	}

	w := Worker{
		Role:         role,
		ResolvedRole: resolved,
		Tier:         tier,
		Kind:         entry.Kind,
		Model:        entry.Model,
		Effort:       entry.Effort,
		Args:         entry.Args,
	}
	return w, resolved, entry, nil
}

// applyTier applies the `tiers.<Tier>` overlay to a worker that already
// carries the role's own values — and, when one applied, the substrate
// overlay's. A tier the role does not define is not an error — it simply
// leaves those values in place.
func (c *Config) applyTier(w Worker, entry *Role, tier Tier) (Worker, error) {
	if tier != "" {
		if variant, ok := entry.Tiers[string(tier)]; ok && variant != nil {
			w.TierApplied = true
			if variant.Kind != "" {
				w.Kind = variant.Kind
			}
			if variant.Model != "" {
				w.Model = variant.Model
			}
			if variant.Effort != "" {
				w.Effort = variant.Effort
			}
			// Args REPLACE, never merge. Presence, not emptiness, is the
			// test: a tier that sets `args = []` deliberately clears the
			// role's args.
			if variant.Args != nil {
				w.Args = variant.Args
			}
		}
	}
	return w, nil
}

// Label names the cell the values actually came from, the way the fail-closed
// refusal messages do: `roles.implement.tiers.strong`, or `roles.review` when
// no overlay contributed. Overlays are named in the order they apply — the
// substrate cell inside the tier — so the label is always a real config path.
// Pointing at the table the user must edit is the whole job of the label, so a
// requested-but-undefined tier is not named.
func (w Worker) Label() string {
	base := "roles." + w.ResolvedRole
	if w.SubstrateApplied {
		base += ".substrates." + string(w.Substrate)
	}
	if !w.TierApplied {
		return base
	}
	return base + ".tiers." + string(w.Tier)
}
