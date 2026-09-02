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
//   - a tier, `[roles.<name>.tiers.<tier>]`, as an overlay on that role.
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
)

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
		},
	}

	if err := route(resolved, opts); err != nil {
		return nil, err
	}
	resolved.Digest = digest(resolved)
	return resolved, nil
}

// route applies the target repository's `[roles.*]` and the tier overlay on top
// of it. It touches the runner and the model only: the executor is the host's
// and the prompt is this repository's, and neither is a target repository's to
// redefine.
func route(p *Profile, opts Options) error {
	if opts.RunnersConfig == "" {
		if opts.Tier != "" {
			return fmt.Errorf("profile %s: tier %q was asked for and no runner configuration was named to declare it",
				p.Role, opts.Tier)
		}
		return nil
	}
	roles, err := ReadRoles(opts.RunnersConfig)
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
		if !ok {
			return fmt.Errorf("profile %s: [roles.%s] declares no tier %q (%s): a tier that silently falls back to "+
				"the role is a tier an operator paid for and did not get", p.Role, name, opts.Tier, declaredTiers(role))
		}
		apply(overlay.Kind, overlay.Model)
		routed = append(routed, fmt.Sprintf("[roles.%s.tiers.%s]", name, opts.Tier))
	}
	p.Routed = strings.Join(routed, " + ")
	return nil
}

func declaredTiers(role Role) string {
	if len(role.Tiers) == 0 {
		return "it declares no tiers at all"
	}
	names := make([]string, 0, len(role.Tiers))
	for name := range role.Tiers {
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
