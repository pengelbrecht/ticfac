package runconfig

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Named run configs (tick tda): a repository may declare more than one
// complete routing in its runners files and let each epic choose between
// them — one epic on GLM through Workers AI, another on claude through the
// operator's subscription — instead of one routing serving every run.
//
// The shape, in every runners file (the common one and each override):
//
//	[configs]
//	default = "glm"            # the config a run uses when nothing selects
//
//	[configs.glm]              # one named config: a complete ROUTING
//	[configs.glm.roles.implement]
//	kind = "pi"
//	model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
//	[configs.glm.roles.implement.tiers.economy]
//	model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
//	[configs.glm.tier_policy]  # the config's own ladder and concurrency caps
//	default = "economy"
//	ceiling = "strong"
//
// A named config is a ROUTING and nothing else: roles (with their tiers) and
// a tier policy. Not the gate, not the acceptance evidence table, not the
// substrate, not the findings routes — those are the repository's rules, the
// same for every run of it, and a config that could vary them would be two
// repositories wearing one checkout. The reader refuses any other key under
// [configs.<name>].
//
// Selection is one precedence, resolved once at run start and never per
// dispatch (the reconciler's selectRunConfig): `--config` on the command line
// > the epic's own `config:` label > the declared default. The chosen name
// travels through [LoadForConfig]/[Config.Select], so every reader that
// re-loads the config resolves the SAME cells the run resolved.
//
// Semantics: a named config's cells apply as the LAST overlay — over the
// common file's cells, over the substrate override's, and over every tier —
// because a config IS the routing a run asked for, and nothing beneath it
// may win over the thing that was chosen. Cells the config does not declare
// keep the merged document's values, and a config's [tier_policy], when it
// declares one, REPLACES the merged policy: a ladder is a whole thing, and
// half of one ladder under half of another is a ladder nobody wrote.
//
// A cloud role the merged document declares no cell for is still refused
// ([ErrNoCloudRouting]): a named config selects among the routings a
// repository has declared for the substrate it runs on, it is not a way to
// route a role the repository never routed. That is what makes "an epic on
// claude" a choice with a refusal behind it rather than a hope.

// ConfigNamePattern is the shape of a named config's name: the same closed
// lowercase vocabulary a role name uses, so a config name reads like every
// other key in these files and `--config` is easy to type.
var ConfigNamePattern = rolePattern

// reservedConfigName is the one name a config cannot take: `default` is the
// [configs] table's selector key, not a config.
const reservedConfigName = "default"

// ConfigsTable is the [configs] table's own keys. Today exactly one:
// Default, the named config a run uses when neither the command line nor
// the epic selects one. A [configs] table that declares named configs but
// no default is refused on the merged document — with more than one routing
// to choose between, "nothing selects" is a choice too, and a silent fall
// back to the file's own cells would be a third routing nobody chose.
type ConfigsTable struct {
	Default string `toml:"default"`
}

// NamedConfig is one [configs.<name>] entry: a complete routing, and nothing
// else. Name is filled by the parser (the table's key); every other field is
// exactly what the file declared.
type NamedConfig struct {
	Name       string
	Roles      map[string]*Role `toml:"roles"`
	TierPolicy *TierPolicy      `toml:"tier_policy"`
}

// DeclaresNothing reports whether the config declares neither a role cell
// nor a tier policy — the empty config a typo leaves behind, which is
// refused rather than read as "the file's own cells, named".
func (nc *NamedConfig) DeclaresNothing() bool {
	return nc == nil || (len(nc.Roles) == 0 && nc.TierPolicy == nil)
}

// ErrNoSuchConfig is the refusal a selection of a name the runners files do
// not declare gets. It is a sentinel so a caller can tell a typo'd selection
// from every other routing failure, the way [ErrNoCloudRouting] names a
// missing cell.
var ErrNoSuchConfig = errors.New("runconfig: no such named config")

// parseNamedConfigs reads the [configs] table out of one document: the
// default key, and every named config with its cells validated by the SAME
// shape rules the top-level tables answer to — a config's role cells are
// role cells, and its policy is a policy, or the split between "validated
// here" and "hoped there" is exactly the drift this package exists to close.
//
// partial is parsePartial's stance (an override file validated on its own):
// the whole-document rules — default names a declared config, a config
// exists — are NOT checked there, because half of a [configs] table may live
// in the common file and the other half in the override; the merged document
// checks them where both halves meet in one place.
func parseNamedConfigs(data []byte, partial bool) (*ConfigsTable, map[string]*NamedConfig, ValidationErrors) {
	var errs ValidationErrors
	add := func(path, msg string) { errs = append(errs, ValidationError{Path: path, Msg: msg}) }

	// The raw probe: [configs] as one map, so the default SCALAR and the
	// named TABLES live in one value — the one decode the typed structs
	// cannot express, since a struct field cannot claim `default` beside a
	// map of tables under the same key.
	var raw struct {
		Configs map[string]any `toml:"configs"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		// A syntax error is reported by the caller's own decode with the
		// parser's message; a second opinion here would only duplicate it.
		return nil, nil, nil
	}
	if raw.Configs == nil {
		return nil, nil, nil
	}
	table := &ConfigsTable{}
	if def, ok := raw.Configs[reservedConfigName]; ok {
		s, isString := def.(string)
		switch {
		case !isString:
			add("configs.default", fmt.Sprintf("%v is not a config name — [configs] default names one of the declared configs, a plain lowercase word", def))
		case s == "":
			add("configs.default", "must not be empty — name one of the declared configs")
		default:
			table.Default = s
		}
	}

	named := map[string]*NamedConfig{}
	for name, value := range raw.Configs {
		if name == reservedConfigName {
			continue
		}
		path := "configs." + name
		if !ConfigNamePattern.MatchString(name) {
			add(path, fmt.Sprintf("%q is not a config name (%s)", name, ConfigNamePattern.String()))
			continue
		}
		sub, ok := value.(map[string]any)
		if !ok {
			add(path, fmt.Sprintf("is not a table — a named config is [configs.<name>] carrying roles and a tier policy (found %T)", value))
			continue
		}
		// The typed decode, through a re-encode of just this config's table.
		// Encoding the table on its own — no [configs] wrapper — roots the
		// decode's metadata at the config's content, so the SAME validators
		// the top-level tables answer to (validateRoles, validateTierPolicy,
		// which read paths like roles.<name>.kind) apply to a config's cells
		// unchanged, and the decode's own Undecoded list is the unknown-key
		// report: an `[configs.glm.orchestration]` is refused here, not
		// silently dropped the way a plain struct decode would drop it.
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(sub); err != nil {
			add(path, fmt.Sprintf("could not be read: %v", err))
			continue
		}
		var nc NamedConfig
		md, err := toml.Decode(buf.String(), &nc)
		if err != nil {
			add(path, fmt.Sprintf("is not a well-formed config: %v", err))
			continue
		}
		nc.Name = name
		for _, key := range md.Undecoded() {
			add(path+"."+key.String(),
				"unknown key — a named config is a ROUTING: roles and tier_policy, and nothing else (the gate, the evidence table, the substrate and the findings routes are the repository's, the same for every run)")
		}
		prefix := func(p, msg string) { add(fmt.Sprintf("%s.%s", path, p), msg) }
		// partial role cells (a config's cell may overlay only the model),
		// and a policy validated in full wherever it is declared: a config is
		// the whole of its own ladder.
		validateRoles(&Config{Roles: nc.Roles}, md, prefix, true)
		validateTierPolicy(&Config{TierPolicy: nc.TierPolicy}, md, prefix)
		if nc.DeclaresNothing() {
			add(path, "declares no [roles] and no [tier_policy] — a named config is a complete routing, and one that declares nothing is a typo'd config that would silently run the file's own cells")
		}
		named[name] = &nc
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
	return table, named, errs
}

// wholeDocumentConfigsRules are the [configs] rules a document a run
// actually reads answers to — the rules an override file cannot meet by
// itself, because half of the table may live in the other file: `default`
// names a declared config, and when named configs exist at all, a default
// exists too. Checked from [Parse] on the common document and on the merged
// document [ParseFor] produces.
func wholeDocumentConfigsRules(table *ConfigsTable, named map[string]*NamedConfig) ValidationErrors {
	switch {
	case len(named) == 0 && table.Default != "":
		return ValidationErrors{{Path: "configs.default",
			Msg: fmt.Sprintf("names config %q and [configs] declares none — a default with nothing to default to is a typo'd key", table.Default)}}
	case len(named) > 0 && table.Default == "":
		return ValidationErrors{{Path: "configs.default",
			Msg: fmt.Sprintf("required when [configs] declares named configs — with more than one routing to choose between (%s), the config a run uses when nothing selects is a choice too; declare it as default = \"…\"", configList(cNamedNames(named)))}}
	}
	if _, ok := named[table.Default]; !ok {
		return ValidationErrors{{Path: "configs.default",
			Msg: fmt.Sprintf("names %q, which [configs] does not declare (it declares %s) — the default is the config a run uses when nothing selects, and it must be one a run can select", table.Default, configList(cNamedNames(named)))}}
	}
	return nil
}

func cNamedNames(named map[string]*NamedConfig) []string {
	out := make([]string, 0, len(named))
	for name := range named {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// NamedConfigNames lists the config names the file declares, sorted. It is
// nil-safe: a file that declares none — every file until tick tda — answers
// empty, and every caller's "no named configs" branch is the same branch.
func (c *Config) NamedConfigNames() []string {
	if c == nil || len(c.namedConfigs) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.namedConfigs))
	for name := range c.namedConfigs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// NamedConfig returns one declared config, and whether it is declared.
func (c *Config) NamedConfig(name string) (*NamedConfig, bool) {
	if c == nil || c.namedConfigs == nil {
		return nil, false
	}
	nc, ok := c.namedConfigs[name]
	return nc, ok
}

// DefaultConfigName is the config a run uses when nothing selects one: the
// declared default, or "" when the file declares none (no [configs] table at
// all — the historical single-routing file, where nothing selects because
// there is nothing to select).
func (c *Config) DefaultConfigName() string {
	if c == nil || c.Configs == nil {
		return ""
	}
	return c.Configs.Default
}

// Select returns the config with the named config's cells applied as the
// LAST overlay — over the common file's cells, over the substrate
// override's, and over every tier — and the named config's [tier_policy],
// when it declares one, in place of the merged policy. name "" is the
// no-selection view: the config as loaded, unchanged, which is what every
// reader that does not select resolves against (the gate, the evidence
// table, the findings routes).
//
// The receiver is not modified: the returned config is a copy, so a caller
// holding the merged document can select several configs from it — exactly
// what doctor and the cloud preflight do, resolving every named config to
// check each one routes.
func (c *Config) Select(name string) (*Config, error) {
	if c == nil {
		return nil, ErrNoConfig
	}
	if name == "" {
		return c, nil
	}
	nc, ok := c.namedConfigs[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q (the runners files declare %s)", ErrNoSuchConfig, name, configList(c.NamedConfigNames()))
	}
	out := c.copyForSelect()
	out.SelectedConfig = name
	for _, roleName := range sortedRoleKeys(nc.Roles) {
		cell := nc.Roles[roleName]
		if cell == nil {
			continue
		}
		role := out.Roles[roleName]
		if role == nil {
			// A config may introduce a role cell the merged document does
			// not declare — but never a SUBSTRATE routing: the cloud's
			// fail-closed rule reads the merged document's own cells, so a
			// config-created cell routes only where the base cells do.
			role = &Role{Tiers: map[string]*TierVariant{}}
			out.Roles[roleName] = role
		}
		// The config's cells apply at EVERY layer, so nothing beneath them
		// can win at resolution time (the shape of finding ea1a62d3: a tier
		// applied before an overlay that applied after it):
		//
		//   - the base cell, and each declared tier cell over the role's own;
		//   - each substrate's role cell, and each declared tier cell into
		//     that substrate's tier table — created when the override file
		//     declared no tier there — because the substrate's role cell
		//     applies after the role's own tiers and would otherwise beat a
		//     config tier it was never meant to.
		role.overlayRoleCell(cell)
		role.overlayTiers(cell.Tiers)
		for sub, variant := range role.Substrates {
			if variant == nil {
				continue
			}
			variant.overlayRoleCell(cell)
			for _, tierName := range sortedRoleKeys(cell.Tiers) {
				ct := cell.Tiers[tierName]
				if ct == nil {
					continue
				}
				if role.SubstrateTiers == nil {
					role.SubstrateTiers = map[string]map[string]*TierVariant{}
				}
				st, ok := role.SubstrateTiers[sub]
				if !ok || st == nil {
					st = map[string]*TierVariant{}
					role.SubstrateTiers[sub] = st
				}
				existing, ok := st[tierName]
				if !ok || existing == nil {
					existing = &TierVariant{}
					st[tierName] = existing
				}
				existing.overlayVariant(ct)
			}
		}
	}
	if nc.TierPolicy != nil {
		out.TierPolicy = nc.TierPolicy
	}
	return out, nil
}

// overlayRoleCell applies one config role cell field-wise: scalars override
// when set, args REPLACE when present (presence, not emptiness: `args = []`
// clears).
func (r *Role) overlayRoleCell(src *Role) {
	if src.Kind != "" {
		r.Kind = src.Kind
	}
	if src.Model != "" {
		r.Model = src.Model
	}
	if src.Effort != "" {
		r.Effort = src.Effort
	}
	if src.Args != nil {
		r.Args = src.Args
	}
}

// overlayRoleCell applies one config role cell onto a substrate variant
// field-wise — the same rule the tier overlay applies, over the override
// file's own cell, because the config was chosen and it was not.
func (v *TierVariant) overlayRoleCell(src *Role) {
	if src.Kind != "" {
		v.Kind = src.Kind
	}
	if src.Model != "" {
		v.Model = src.Model
	}
	if src.Effort != "" {
		v.Effort = src.Effort
	}
	if src.Args != nil {
		v.Args = src.Args
	}
}

// overlayVariant merges one declared tier cell onto another field-wise.
func (v *TierVariant) overlayVariant(src *TierVariant) {
	if src.Kind != "" {
		v.Kind = src.Kind
	}
	if src.Model != "" {
		v.Model = src.Model
	}
	if src.Effort != "" {
		v.Effort = src.Effort
	}
	if src.Args != nil {
		v.Args = src.Args
	}
}

// overlayTiers merges the config's declared tier cells onto the role's own:
// each declared tier overlays field-wise onto the role's cell for that tier,
// created when the role declares none — a config's tier is a tier,
// wherever it was declared.
func (r *Role) overlayTiers(tiers map[string]*TierVariant) {
	for _, tier := range sortedVariantKeys(tiers) {
		v := tiers[tier]
		if v == nil {
			continue
		}
		if r.Tiers == nil {
			r.Tiers = map[string]*TierVariant{}
		}
		existing, ok := r.Tiers[tier]
		if !ok || existing == nil {
			existing = &TierVariant{}
			r.Tiers[tier] = existing
		}
		if v.Kind != "" {
			existing.Kind = v.Kind
		}
		if v.Model != "" {
			existing.Model = v.Model
		}
		if v.Effort != "" {
			existing.Effort = v.Effort
		}
		if v.Args != nil {
			existing.Args = v.Args
		}
	}
}

func sortedRoleKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedVariantKeys(m map[string]*TierVariant) []string {
	return sortedRoleKeys(m)
}

// copyForSelect copies the config's mutable maps just deep enough that a
// Select cannot reach back into the merged document: the roles it touches
// are copied with their tiers and substrate overlays, the rest share the
// loaded document's values (Select never writes a role a config does not
// declare).
func (c *Config) copyForSelect() *Config {
	out := *c
	out.Roles = make(map[string]*Role, len(c.Roles))
	for name, role := range c.Roles {
		if role == nil {
			out.Roles[name] = nil
			continue
		}
		copied := *role
		copied.Tiers = copyVariants(role.Tiers)
		copied.Substrates = copyVariants(role.Substrates)
		if role.SubstrateTiers != nil {
			copied.SubstrateTiers = make(map[string]map[string]*TierVariant, len(role.SubstrateTiers))
			for sub, tiers := range role.SubstrateTiers {
				copied.SubstrateTiers[sub] = copyVariants(tiers)
			}
		}
		out.Roles[name] = &copied
	}
	return &out
}

func copyVariants(m map[string]*TierVariant) map[string]*TierVariant {
	if m == nil {
		return nil
	}
	out := make(map[string]*TierVariant, len(m))
	for k, v := range m {
		if v == nil {
			out[k] = nil
			continue
		}
		copied := *v
		out[k] = &copied
	}
	return out
}

// configList renders the declared names for a refusal, with the honest
// answer for the file that declares none.
func configList(names []string) string {
	if len(names) == 0 {
		return "no named configs at all"
	}
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}

// LoadForConfig is [LoadFor] with one named config selected: the common
// file with the substrate's override merged over it, and then the named
// config's cells applied as the last overlay ([Config.Select]). name ""
// loads exactly what [LoadFor] loads — the readers that do not select (the
// gate, the evidence table, the findings routes) keep today's answer.
//
// A name the runners files do not declare is [ErrNoSuchConfig]: an operator
// who asked for claude and silently got the file's own cells would be an
// operator whose epic ran on a routing nobody chose.
func LoadForConfig(path string, sub Substrate, name string) (*Config, error) {
	cfg, err := LoadFor(path, sub)
	if err != nil {
		return nil, err
	}
	return cfg.Select(name)
}

// LoadRepoForConfig is [LoadRepoFor] with one named config selected: nil,
// nil when the repository has no runners.toml at all.
func LoadRepoForConfig(repoRoot string, sub Substrate, name string) (*Config, error) {
	cfg, err := LoadRepoFor(repoRoot, sub)
	if err != nil {
		return nil, err
	}
	return cfg.Select(name)
}
