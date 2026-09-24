// Package profile resolves the ROLE PROFILE a job is dispatched with.
//
// A profile is exactly four things — executor, runner, model, prompt — and in
// Phase 1 it is nothing else (SPEC §4.5). That rule is the point of the
// package: an option smuggled into a profile is an option no attempt record
// names and no evidence digest covers, and a run nobody can reproduce is a run
// whose verdicts mean less than they look like they mean. So the profile file
// is decoded STRICTLY, a fifth field is refused rather than ignored, and what
// is resolved is digested into the provenance every record carries.
//
// Three sources meet here, in one order:
//
//   - profiles/<role>.json in this repository, versioned, plus the prompt file
//     it names. It is compiled into the binary for the reason contracts.pin.json
//     is: a profile read off disk at run time could disagree with the binary
//     beside it, and the profile is what a record's provenance cites.
//
//   - `[roles.<name>]` in the TARGET repository's `.tick/runners.toml`, read the
//     way `tk herd` reads it, so an operator keeps configuring runs in one
//     place. It routes the runner (`kind`) and the model, and nothing else: the
//     other two fields of a profile are not a repository's to set.
//
//   - a tier, `[roles.<name>.tiers.<tier>]`, as an overlay on that role, and
//     a substrate's override file (ticks 84z, 5uo) — `.tick/runners.cloud.toml`
//     or `.tick/runners.local.toml` — whose role cell applies LAST, over the
//     role and any tier: the axis a cloud container resolves on, so the
//     common file can keep a frontier review for local runs and the cloud
//     file a container-runnable worker for cloud ones.
//
// What comes out records where each of those came from, because "which profile
// was this run made under" is a question an attempt record has to be able to
// answer months later.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	ticfac "github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// ErrNoCloudRouting is the refusal a cloud run gets for a role the target
// repository declares no cloud routing for (tick 84z). It is the same
// sentinel the config reader refuses with ([runconfig.ErrNoCloudRouting]),
// re-exported so a caller that asks a profile gets one truth to match on,
// not two spellings of one rule in two packages.
var ErrNoCloudRouting = runconfig.ErrNoCloudRouting

// SchemaVersion is the version every profile file carries.
const SchemaVersion = 1

// DirName is where the profiles live, relative to the repository root.
const DirName = "profiles"

// Roles are the three role profiles Phase 1 ships, in the order the reconciler
// dispatches them. They are job-protocol.json's role names, not the tracker's
// shorter ones: the profile is named for the job it configures.
var Roles = []string{"implement-tick", "review-epic", "closeout-epic"}

// runnersConfigRoles maps a job-protocol role onto the names `.tick/runners.toml`
// files spell it with, most specific first. ticks' own files say `implement`,
// `review` and `closeout`; a file that spells the full role name is read too,
// because an operator who wrote the longer name meant the same thing.
var runnersConfigRoles = map[string][]string{
	"implement-tick": {"implement-tick", "implement"},
	"review-epic":    {"review-epic", "review"},
	"closeout-epic":  {"closeout-epic", "closeout", "close-out"},
}

// RunnersRoleCandidates returns the roles-table names a profile role routes
// through, first declared first — the same candidates route() walks. A caller
// that needs the roles-table entry behind a profile (to compile spawn argv
// from its effort and args, say) resolves it with the same order and so cannot
// disagree with the routing that produced the profile.
func RunnersRoleCandidates(role string) []string {
	return runnersConfigRoles[role]
}

// Options say where a profile is resolved from.
type Options struct {
	// Dir is a profiles directory on disk. Empty resolves the copy compiled
	// into this binary, which is the production path.
	Dir string

	// RunnersConfig is the TARGET repository's `.tick/runners.toml`, whose
	// `[roles.*]` routes the runner and the model. Empty, or a file that is not
	// there, is no routing rather than an error.
	RunnersConfig string

	// Tier selects a `[roles.<name>.tiers.<tier>]` overlay. A tier the config
	// does not declare is REFUSED: an operator who asked for the economy tier
	// and silently got the default paid for the default without being told.
	Tier string

	// Substrate is the substrate the run executes on, which picks the
	// override file merged over RunnersConfig (ticks 84z, 5uo).
	// Empty is the substrate-blind resolution this package has always done;
	// `cloud`, `herdr` and `harness` resolve against their cells. Under
	// `cloud` the overlay is REQUIRED: a role the config declares no cloud
	// cell for is REFUSED naming the role, never silently fallen back to the
	// role's own values or the profile as shipped — the shipped review and
	// close-out profiles ARE claude processes, and that fall back is how a
	// cloud run reaches one nobody chose.
	Substrate string
}

// Provenance is where a resolved profile came from. It travels into the attempt
// record so that a run can be reproduced from what it says rather than from
// what a reader assumes was configured at the time.
type Provenance struct {
	// Source is the profile file, PromptSource the prompt beside it.
	Source       string
	PromptSource string

	// Routed names the table that changed the runner or the model, empty when
	// nothing did. Silence in a config is not a routing, and a provenance that
	// claimed one would be a provenance that lies quietly.
	Routed string

	// Tier is the overlay that was applied, empty when none was asked for.
	Tier string

	// Substrate is the substrate the routing resolved against, empty when no
	// substrate was named (tick 84z). It travels beside Tier because a cloud
	// review and a local one are different judgements, and an attempt record
	// that could not tell them apart would be evidence about neither.
	Substrate string

	// Digest is over what was RESOLVED — role, version and the four fields —
	// so two runs of the same profile file under different routing do not
	// digest the same.
	Digest string
}

// Profile is one resolved role profile.
type Profile struct {
	Role    string
	Version string

	// The four, and no fifth (SPEC §4.5).
	Executor string
	Runner   string
	Model    string
	Prompt   string

	Provenance
}

// Fields is the profile as the four things it is. It exists so that "exactly
// executor, runner, model and prompt" is something a test can count rather than
// a comment somebody has to believe.
func (p *Profile) Fields() map[string]string {
	return map[string]string{
		"executor": p.Executor,
		"runner":   p.Runner,
		"model":    p.Model,
		"prompt":   p.Prompt,
	}
}

// String names the profile the way a log line should: what it is and which
// version of it.
func (p *Profile) String() string {
	return fmt.Sprintf("%s@%s (%s/%s, %s)", p.Role, p.Version, p.Executor, p.Runner, p.Model)
}

// file is the on-disk profile, decoded strictly. `prompt` names a file beside
// it rather than carrying the text, because a prompt is prose and prose in a
// JSON string is prose nobody reviews.
type file struct {
	SchemaVersion int    `json:"schema_version"`
	Role          string `json:"role"`
	Version       string `json:"version"`
	Executor      string `json:"executor"`
	Runner        string `json:"runner"`
	Model         string `json:"model"`
	Prompt        string `json:"prompt"`
}

// ResolveAll resolves every Phase 1 role under one set of options. A run
// resolves all three up front so that a profile that does not exist is a
// refusal at construction and not a surprise three ticks into an epic.
func ResolveAll(opts Options) (map[string]*Profile, error) {
	out := make(map[string]*Profile, len(Roles))
	for _, role := range Roles {
		resolved, err := Resolve(role, opts)
		if err != nil {
			return nil, err
		}
		out[role] = resolved
	}
	return out, nil
}

// Resolve reads one role's profile, applies the target repository's routing,
// and digests what came out.
func Resolve(role string, opts Options) (*Profile, error) {
	if _, known := runnersConfigRoles[role]; !known {
		return nil, fmt.Errorf("profile: %q is not a role this phase ships a profile for (%s)",
			role, strings.Join(Roles, ", "))
	}

	source, base, err := profileSource(opts.Dir)
	if err != nil {
		return nil, err
	}
	name := role + ".json"
	raw, err := fs.ReadFile(source, name)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", role, err)
	}

	var decoded file
	if err := strictUnmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("profile %s (%s): %w: a profile is exactly {executor, runner, model, prompt}, "+
			"and a field beside those four is one no record names and no digest covers",
			role, path.Join(base, name), err)
	}
	if decoded.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("profile %s declares schema_version %d, want %d", role, decoded.SchemaVersion, SchemaVersion)
	}
	if decoded.Role != role {
		return nil, fmt.Errorf("profile %s is filed as %s: a profile that does not name its own role cannot be cited by one",
			name, decoded.Role)
	}
	for field, value := range map[string]string{
		"version": decoded.Version, "executor": decoded.Executor,
		"runner": decoded.Runner, "model": decoded.Model, "prompt": decoded.Prompt,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("profile %s declares no %s", role, field)
		}
	}

	prompt, err := fs.ReadFile(source, decoded.Prompt)
	if err != nil {
		return nil, fmt.Errorf("profile %s names the prompt %s and there is none: a role dispatched without its "+
			"prompt is a role in name only (%w)", role, decoded.Prompt, err)
	}

	resolved := &Profile{
		Role:     role,
		Version:  decoded.Version,
		Executor: decoded.Executor,
		Runner:   decoded.Runner,
		Model:    decoded.Model,
		Prompt:   string(prompt),
		Provenance: Provenance{
			Source:       path.Join(base, name),
			PromptSource: path.Join(base, decoded.Prompt),
			Tier:         opts.Tier,
			Substrate:    opts.Substrate,
		},
	}

	if err := validateSubstrate(opts.Substrate, role); err != nil {
		return nil, err
	}
	if err := route(resolved, opts); err != nil {
		return nil, err
	}
	// The cloud rule on the FINAL resolved worker (tick nwn), after every
	// overlay route applied — never on one input layer. It keys on WHAT RUNS
	// IN CLOUDFLARE (tick 78v), not on the substrate alone: the cloud
	// substrate, and any executor that dispatches its workers into Cloudflare
	// — a run whose substrate is local, selecting the cloudflare-sandbox
	// executor by its --profiles, boots its workers in a Cloudflare container
	// under local routing, and the rule is about what runs in Cloudflare.
	if opts.Substrate == string(runconfig.SubstrateCloud) || dispatchesIntoCloudflare(resolved.Executor) {
		if err := enforceWorkersAI(resolved, opts.Tier); err != nil {
			return nil, fmt.Errorf("profile %s: %w", role, err)
		}
	}
	resolved.Digest = digest(resolved)
	return resolved, nil
}

// validateSubstrate refuses a substrate value a profile cannot be resolved
// against: auto is a policy a decision procedure resolves, never the
// substrate a run executes on, and anything else must be one of the
// vocabulary's values.
func validateSubstrate(substrate, role string) error {
	if substrate == "" {
		return nil
	}
	sub := runconfig.Substrate(substrate)
	if sub == runconfig.SubstrateAuto {
		return fmt.Errorf("profile %s: substrate %q is a policy, not a substrate: resolve the substrate (runconfig.Decide) before routing a profile against it", role, substrate)
	}
	if !sub.Valid() {
		return fmt.Errorf("profile %s: %q is not a substrate a profile can be routed for (want one of %s)", role, substrate, runconfig.SubstrateList(true))
	}
	return nil
}

// route applies the target repository's `[roles.*]`, the substrate overlay
// and the tier overlay on top of them. It touches the runner and the model
// only: the executor is the host's and the prompt is this repository's, and
// neither is a target repository's to redefine.
func route(p *Profile, opts Options) error {
	if opts.RunnersConfig == "" {
		if opts.Tier != "" {
			return fmt.Errorf("profile %s: tier %q was asked for and no runner configuration was named to declare it",
				p.Role, opts.Tier)
		}
		// The cloud substrate has no silent answer when there is no config at
		// all: the profile as shipped is the only routing there is, and the
		// shipped review and close-out profiles ARE claude processes — the
		// exact thing a container must refuse to start (tick 84z).
		if opts.Substrate == string(runconfig.SubstrateCloud) {
			return fmt.Errorf("profile %s: %w: no runner configuration was named, so nowhere declares the cloud routing role %q needs — a cloud run refuses rather than dispatching the profile as shipped",
				p.Role, ErrNoCloudRouting, p.Role)
		}
		return nil
	}
	roles, err := ReadRolesFor(opts.RunnersConfig, runconfig.Substrate(opts.Substrate))
	if err != nil {
		return fmt.Errorf("profile %s: %w", p.Role, err)
	}

	role, name, found := Role{}, "", false
	for _, candidate := range runnersConfigRoles[p.Role] {
		if declared, ok := roles[candidate]; ok {
			role, name, found = declared, candidate, true
			break
		}
	}
	if !found {
		if opts.Tier != "" {
			return fmt.Errorf("profile %s: tier %q was asked for and %s declares no role it could overlay",
				p.Role, opts.Tier, opts.RunnersConfig)
		}
		if opts.Substrate == string(runconfig.SubstrateCloud) {
			return fmt.Errorf("profile %s: %w: %s declares no role %q could be routed under, so %s can declare no cell for it — a cloud run refuses rather than dispatching the profile as shipped",
				p.Role, ErrNoCloudRouting, opts.RunnersConfig, p.Role, runconfig.OverrideFileName(runconfig.SubstrateCloud))
		}
		return nil
	}

	routed := []string{}
	apply := func(kind, model string) {
		if kind != "" {
			p.Runner = kind
		}
		if model != "" {
			p.Model = model
		}
	}
	if role.Kind != "" || role.Model != "" {
		apply(role.Kind, role.Model)
		routed = append(routed, fmt.Sprintf("%s [roles.%s]", opts.RunnersConfig, name))
	}

	if opts.Tier != "" {
		overlay, ok := role.Tiers[opts.Tier]
		_, subOK := role.SubstrateTiers[opts.Substrate][opts.Tier]
		if !ok && !subOK {
			return fmt.Errorf("profile %s: [roles.%s] declares no tier %q (%s): a tier that silently falls back to "+
				"the role is a tier an operator paid for and did not get", p.Role, name, opts.Tier, declaredTiers(role, opts.Substrate))
		}
		if ok {
			apply(overlay.Kind, overlay.Model)
			routed = append(routed, fmt.Sprintf("[roles.%s.tiers.%s]", name, opts.Tier))
		}
	}

	// The substrate override applies LAST (tick 5uo): its role cell over the
	// role's own values AND any tier, then its own tier cell. The common
	// cells say what a LOCAL run pays for; the override says what this run
	// executes on, and nothing the common file says about a tier can win
	// over it — a claude frontier tier cannot reach a cloud run whose
	// runners.cloud.toml routes the role to pi (finding ea1a62d3). Under the
	// cloud substrate the override cell is REQUIRED — absent cloud routing is
	// a refusal naming the role, never a fall back to the role's own values,
	// which name a harness a container cannot run at all (tick 84z).
	if opts.Substrate != "" {
		overlay, declared := role.Substrates[opts.Substrate]
		if !declared {
			if opts.Substrate == string(runconfig.SubstrateCloud) {
				return fmt.Errorf("profile %s: %w: %s declares no [roles.%s] cell — a cloud run refuses rather than falling back to the role's own %q/%q",
					p.Role, ErrNoCloudRouting, runconfig.OverrideFileName(runconfig.SubstrateCloud), name, role.Kind, role.Model)
			}
		} else {
			apply(overlay.Kind, overlay.Model)
			routed = append(routed, fmt.Sprintf("%s [roles.%s]", role.OverrideFile, name))
			if opts.Tier != "" {
				if tv, ok := role.SubstrateTiers[opts.Substrate][opts.Tier]; ok {
					apply(tv.Kind, tv.Model)
					routed = append(routed, fmt.Sprintf("%s [roles.%s.tiers.%s]", role.OverrideFile, name, opts.Tier))
				}
			}
		}
	}
	p.Routed = strings.Join(routed, " + ")
	return nil
}

func declaredTiers(role Role, substrate string) string {
	seen := map[string]bool{}
	for name := range role.Tiers {
		seen[name] = true
	}
	for name := range role.SubstrateTiers[substrate] {
		seen[name] = true
	}
	if len(seen) == 0 {
		return "it declares no tiers at all"
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return "it declares " + strings.Join(names, ", ")
}

// profileSource is the directory profiles are read from: the copy compiled into
// this binary, or one on disk when a caller names it.
func profileSource(dir string) (fs.FS, string, error) {
	if dir == "" {
		sub, err := fs.Sub(ticfac.ProfilesFS, DirName)
		if err != nil {
			return nil, "", fmt.Errorf("profile: the compiled-in %s/ is unreadable: %w", DirName, err)
		}
		return sub, DirName, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("profile: %s is not a profiles directory: %v", dir, err)
	}
	return os.DirFS(dir), filepath.ToSlash(dir), nil
}

// digest is over what was resolved, in a fixed order, with each part's length
// in front of it so that two different splits cannot digest the same.
func digest(p *Profile) string {
	sum := sha256.New()
	for _, part := range []string{p.Role, p.Version, p.Executor, p.Runner, p.Model, p.Prompt} {
		fmt.Fprintf(sum, "%d:%s\n", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}

// strictUnmarshal is `additionalProperties: false` in Go: a field the type
// cannot express is refused rather than dropped.
func strictUnmarshal(raw []byte, into any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}
