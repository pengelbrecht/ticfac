package runconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// The per-world override files (tick 5uo). The operator's shape, verbatim:
// "you can either have runners.toml (common), or runners.cloud.toml,
// runners.local.toml which override if they exist".
//
//	.tick/runners.toml         common to every run
//	.tick/runners.local.toml   optional; overrides for herdr and harness runs
//	.tick/runners.cloud.toml   optional; overrides for the cloud substrate
//
// With neither override file a run reads exactly what it always read. The
// rules, in one place:
//
//   - The same schema in all three files, validated the same way. An
//     override is also validated ON ITS OWN first, so an error in it names
//     it — minus the whole-file requirements ([roles.implement], a role's
//     kind, cross-table command references) an override cannot meet alone;
//     those are checked on the merged document.
//   - The matching override is merged over the common file: TABLES merge
//     field-wise, ARRAYS ([[tier_policy.start]], args) are REPLACED wholesale,
//     because a merged list of rules has no meaning anyone wrote.
//   - A role cell the override declares is applied LAST — over the role's own
//     values and over any tier — and then the override's own tier cell for
//     the asked-for tier, if it declares one. So nothing the common file says
//     about a tier can win over what the override says about the role: a
//     claude frontier tier in runners.toml cannot reach a cloud run whose
//     runners.cloud.toml routes the role to pi (finding ea1a62d3, closed by
//     construction rather than by care).
//   - A tier declared only in runners.local.toml does not exist for a cloud
//     run: the cloud never reads that file.
//   - Under the cloud substrate a role runners.cloud.toml does not declare is
//     refused ([ErrNoCloudRouting]), and a role it does declare must name its
//     kind: the common file's kind was written for a laptop.
//   - The substrate that picks the file is decided from the COMMON file (and
//     TICKS_SUBSTRATE): an override cannot move a run to another world.

// OverrideFileName is the override file a run on sub reads over
// [FileName], or "" when sub names no world (the substrate-blind view, and
// auto, which is a policy rather than a place a run executes).
func OverrideFileName(sub Substrate) string {
	switch sub {
	case SubstrateCloud:
		return ".tick/runners.cloud.toml"
	case SubstrateHerdr, SubstrateHarness:
		return ".tick/runners.local.toml"
	}
	return ""
}

// LoadFor loads the common file at path with the override for sub merged
// over it, when that override exists beside it. A missing common file is an
// error satisfying os.IsNotExist, exactly as [Load]'s is.
func LoadFor(path string, sub Substrate) (*Config, error) {
	common, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := OverrideFileName(sub)
	if name == "" {
		return Parse(common)
	}
	overridePath := filepath.Join(filepath.Dir(path), filepath.Base(name))
	override, err := os.ReadFile(overridePath)
	if errors.Is(err, os.ErrNotExist) {
		return Parse(common)
	}
	if err != nil {
		return nil, err
	}
	return ParseFor(common, override, overridePath, sub)
}

// LoadRepoFor is [LoadRepo] for one substrate: nil, nil when the repository
// has no runners.toml at all.
func LoadRepoFor(repoRoot string, sub Substrate) (*Config, error) {
	cfg, err := LoadFor(filepath.Join(repoRoot, filepath.FromSlash(FileName)), sub)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return cfg, err
}

// ParseFor is [LoadFor] on bytes: common is runners.toml, override the file
// named overridePath (used only to name it in errors and provenance).
func ParseFor(common, override []byte, overridePath string, sub Substrate) (*Config, error) {
	if _, err := Parse(common); err != nil {
		return nil, err
	}
	ov, md, err := parsePartial(override)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", overridePath, err)
	}

	var base, over map[string]any
	if _, err := toml.Decode(string(common), &base); err != nil {
		return nil, ValidationErrors{{Msg: err.Error()}}
	}
	if _, err := toml.Decode(string(override), &over); err != nil {
		return nil, fmt.Errorf("%s: %w", overridePath, ValidationErrors{{Msg: err.Error()}})
	}
	var merged bytes.Buffer
	if err := toml.NewEncoder(&merged).Encode(mergeTables(base, over)); err != nil {
		return nil, fmt.Errorf("merge %s over %s: %w", overridePath, FileName, err)
	}
	cfg, err := Parse(merged.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s merged over %s: %w", overridePath, FileName, err)
	}
	if err := cfg.attachOverride(sub, ov, md, overridePath); err != nil {
		return nil, err
	}
	cfg.OverrideFile = overridePath
	return cfg, nil
}

// mergeTables merges over into base: a table merges key by key, anything
// else — a scalar, an array, an array of tables — is replaced wholesale.
func mergeTables(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bt, bok := out[k].(map[string]any)
		ot, ook := v.(map[string]any)
		if bok && ook {
			out[k] = mergeTables(bt, ot)
			continue
		}
		out[k] = v
	}
	return out
}

// attachOverride records the override's role and tier cells as the
// last-applied overlay for sub (see the file comment for why last).
func (c *Config) attachOverride(sub Substrate, ov *Config, md toml.MetaData, overridePath string) error {
	key := string(sub)
	for _, name := range sortedKeys(ov.Roles) {
		cell := ov.Roles[name]
		role := c.Roles[name]
		if cell == nil || role == nil {
			continue
		}
		if sub == SubstrateCloud && !md.IsDefined("roles", name, "kind") {
			return fmt.Errorf("%s: [roles.%s] must name its kind: under the cloud substrate the common file's kind (%q) was written for a laptop, and a cloud cell that inherits it is how a container reaches a harness nobody chose", overridePath, name, role.Kind)
		}
		if role.Substrates == nil {
			role.Substrates = map[string]*TierVariant{}
		}
		role.Substrates[key] = &TierVariant{Kind: cell.Kind, Model: cell.Model, Effort: cell.Effort, Args: cell.Args}
		if len(cell.Tiers) > 0 {
			if role.SubstrateTiers == nil {
				role.SubstrateTiers = map[string]map[string]*TierVariant{}
			}
			role.SubstrateTiers[key] = cell.Tiers
		}
	}
	return nil
}
