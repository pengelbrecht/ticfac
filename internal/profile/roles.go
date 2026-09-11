package profile

import (
	"fmt"
	"os"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The `[roles.<name>]` reader, as an adapter over ticfac's execution-half
// reader of `.tick/runners.toml` (internal/runconfig — the split, and the
// decision behind it, are that package's doc.go).
//
// This file USED to be a second hand-rolled parser: a line reader that knew
// two keys and silently passed over the rest. Two readers of one table inside
// one repository is the drift hazard this epic's learnings name, and the split
// made it untenable — with the execution half genuinely ticfac's, the roles
// table is read by the validated reader or the split is a renaming. So the
// parser is gone; what remains is the ADAPTER, and its job is narrowness of a
// different kind:
//
//   - it exposes only the two fields a Phase 1 profile routes on (kind and
//     model). The validated reader also parses `effort`, `args` and `harness`
//     — a profile has nowhere to put them, and the herdr executor ticks that
//     will consume them import the config package directly, not through here;
//   - it keeps this package's own semantics for what a profile does with a
//     routing: the alias candidates (implement-tick before implement), and
//     the tier refusal — a tier an operator asked for that the config does not
//     declare is still a refusal here, not a silent fall back to the role.
//
// The honesty the adapter adds: a runners.toml that fails validation anywhere
// in the EXECUTION half now fails profile resolution. A file tk refuses is a
// file a run must refuse too — that is what "read the way `tk herd` reads it"
// has always meant, and it used to be true only for the two keys the old
// parser knew. The tracker half ([signals], [sweeps]) is the other reader's
// and is tolerated, exactly as in the config package.

// Role is one `[roles.<name>]` declaration as a profile routes on it: the two
// fields a profile has anywhere to put, plus the tier overlays that can change
// them.
type Role struct {
	Name  string
	Kind  string
	Model string
	Tiers map[string]Role
}

// ReadRoles reads `[roles.*]` from a runners.toml file. A missing file is NOT
// an error: a repository that declares no routing has declared none, and the
// caller decides what that means — here it means the profiles ship as written.
// A file that exists and fails validation IS an error; see the package comment.
func ReadRoles(path string) (map[string]Role, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]Role{}, nil
		}
		return nil, fmt.Errorf("read the runner routing: %w", err)
	}
	cfg, err := runconfig.Load(path)
	if err != nil {
		return nil, fmt.Errorf("read the runner routing: %w", err)
	}
	return rolesFrom(cfg), nil
}

// ParseRoles reads `[roles.*]` out of a runners.toml document.
func ParseRoles(document string) (map[string]Role, error) {
	cfg, err := runconfig.Parse([]byte(document))
	if err != nil {
		return nil, err
	}
	return rolesFrom(cfg), nil
}

// rolesFrom adapts the validated config's roles onto the profile's two-field
// view of them. Kind and model only: the other keys are parsed and validated
// by the reader, and routing on them is the executor's job, not a profile's.
func rolesFrom(cfg *runconfig.Config) map[string]Role {
	if cfg == nil {
		return map[string]Role{}
	}
	out := make(map[string]Role, len(cfg.Roles))
	for name, role := range cfg.Roles {
		if role == nil {
			continue
		}
		entry := Role{Name: name, Kind: role.Kind, Model: role.Model}
		if len(role.Tiers) > 0 {
			entry.Tiers = make(map[string]Role, len(role.Tiers))
			for tier, variant := range role.Tiers {
				if variant == nil {
					continue
				}
				entry.Tiers[tier] = Role{Name: tier, Kind: variant.Kind, Model: variant.Model}
			}
		}
		out[name] = entry
	}
	return out
}
