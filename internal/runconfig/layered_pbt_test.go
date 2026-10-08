package runconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// Properties of the per-world override files (tick 89g, over the seam tick
// 5uo landed): runners.local.toml and runners.cloud.toml merged over
// runners.toml for their substrate.
//
// The semantics are three sentences, and each is a property:
//
//   - TABLES MERGE FIELD-WISE: a field the common file declares that the
//     override does not name survives with its own value, at every depth —
//     the override overlays, it does not erase.
//   - EVERYTHING ELSE IS REPLACED WHOLESALE: an array the override declares
//     is the merged array in full, because a merged list of rules has no
//     meaning anyone wrote.
//   - THE OVERRIDE'S ROLE CELL APPLIES LAST: after the role's own values and
//     after any tier — which is finding ea1a62d3's fix, and the property the
//     whole file exists for: a claude frontier tier in runners.toml can never
//     reach a cloud run whose runners.cloud.toml routes the role to pi.
//
// The first is checked twice, deliberately: once against mergeTables over
// generated TOML-shaped values, where the merge is the whole of the answer;
// and once end-to-end through LoadFor, where the merge is one step of a
// pipeline that could lose the property anywhere else. The end-to-end
// property also states the two facts that keep the overlay honest: with no
// override file on disk a run reads exactly what it always read, and the
// OTHER world's file is invisible to this one's run.

// ------------------------------------------------------- the pure merge ---

// pbtGenValue draws one TOML-decoder-shaped value: a scalar, an array of
// tables, or a nested table.
func pbtGenValue(tc hegel.TestCase, depth int) any {
	switch hegel.Draw(tc, hegel.Integers(0, 3)) {
	case 0:
		return hegel.Draw(tc, hegel.Text().MinSize(0).MaxSize(24))
	case 1:
		return hegel.Draw(tc, hegel.Integers(-9, 99))
	case 2:
		if depth < 2 {
			tables := make([]map[string]any, hegel.Draw(tc, hegel.Integers(1, 2)))
			for i := range tables {
				tables[i] = pbtGenTable(tc, depth+1)
			}
			return tables
		}
		fallthrough
	default:
		if depth < 2 {
			return pbtGenTable(tc, depth+1)
		}
		return hegel.Draw(tc, hegel.Booleans())
	}
}

// pbtGenTable draws one table: keys from a small pool, each with a drawn
// value, no key twice.
func pbtGenTable(tc hegel.TestCase, depth int) map[string]any {
	table := map[string]any{}
	for _, key := range []string{"a", "b", "c"} {
		if !hegel.Draw(tc, hegel.WeightedBooleans(0.6)) {
			continue
		}
		table[key] = pbtGenValue(tc, depth)
	}
	return table
}

// pbtAssertMerged states the merge contract at one depth: every key of base
// that over does not name survives, every key over names wins, and only two
// tables merge — anything else replaces.
func pbtAssertMerged(ht *hegel.T, base, over, out any, where string) {
	bt, bok := base.(map[string]any)
	ot, ook := over.(map[string]any)
	if !bok || !ook {
		// Not both tables: the override replaces wholesale, base and all.
		if !reflect.DeepEqual(out, over) {
			ht.Fatalf("%s: a non-table merged instead of being replaced:\nbase %#v\nover %#v\nout  %#v",
				where, base, over, out)
		}
		return
	}
	got, ok := out.(map[string]any)
	if !ok {
		ht.Fatalf("%s: two tables merged into a %T:\nbase %#v\nover %#v", where, out, base, over)
		return
	}
	for key, value := range bt {
		if _, named := ot[key]; named {
			continue
		}
		if !reflect.DeepEqual(got[key], value) {
			ht.Fatalf("%s.%s: a field the override does not name was erased:\nbase %#v\nover %#v\nout  %#v",
				where, key, value, ot[key], got[key])
		}
	}
	for key, value := range ot {
		pbtAssertMerged(ht, bt[key], value, got[key], where+"."+key)
	}
}

func TestPBTTablesMergeFieldwiseAndEverythingElseReplaces(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		base, over := pbtGenTable(ht, 0), pbtGenTable(ht, 0)
		// The interesting cases are where the two agree on a key: draw over
		// until it names at least one key the base has.
		shared := false
		for key := range over {
			if _, ok := base[key]; ok {
				shared = true
			}
		}
		ht.Assume(shared)
		out := mergeTables(base, over)
		pbtAssertMerged(ht, base, over, out, "merged")
	}, hegel.WithTestCases(200))
}

// -------------------------------------------------- the end-to-end layer ---

// The pools the generated configs draw from: the spellings the shipped
// fixtures use, so every generated config is one the reader accepts.
var (
	pbtTierNames    = []Tier{TierEconomy, TierStrong, TierFrontier}
	pbtTierAt       = map[Tier]int{TierEconomy: 0, TierStrong: 1, TierFrontier: 2}
	pbtPiModels     = []string{"cloudflare-workers-ai/@cf/zai-org/glm-5.3", "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"}
	pbtClaudeModels = []string{"opus", "sonnet"}
	pbtRoles        = []string{"implement", "review"}
	pbtEfforts      = []Effort{EffortLow, EffortMedium, EffortHigh}
)

// pbtKindModel draws a kind and a model that belongs to it.
func pbtKindModel(tc hegel.TestCase) (string, string) {
	if hegel.Draw(tc, hegel.Booleans()) {
		return "pi", hegel.Draw(tc, hegel.SampledFrom(pbtPiModels))
	}
	return "claude", hegel.Draw(tc, hegel.SampledFrom(pbtClaudeModels))
}

// pbtCell writes one role or tier cell, and reports the model it declared.
func pbtCell(b *strings.Builder, tc hegel.TestCase, header string) string {
	kind, model := pbtKindModel(tc)
	fmt.Fprintf(b, "\n[%s]\nkind = %q\nmodel = %q\n", header, kind, model)
	if hegel.Draw(tc, hegel.Booleans()) {
		fmt.Fprintf(b, "effort = %q\n", hegel.Draw(tc, hegel.SampledFrom(pbtEfforts)))
	}
	return model
}

// pbtConfig is a drawn runners file and what the generator knows it declared,
// so the properties can be stated about what was WRITTEN rather than re-parsed
// from what was kept.
type pbtConfig struct {
	text    string
	role    map[string]string // role -> the model its own cell declared
	tier    map[string]string // role+"/"+tier -> the model that tier declared
	policy  bool              // [tier_policy] declared
	def     Tier              // the policy's declared default
	ceil    Tier              // the policy's declared ceiling, when ceiling
	ceiling bool
	starts  int // how many [[tier_policy.start]] rules
}

// pbtGenConfig draws a whole runners file. defCap is the highest tier a
// declared DEFAULT may name — the reader refuses a ceiling under the default,
// and the ceiling that survives a merge can come from the OTHER file, so the
// override is drawn under a cap the common file's ceiling sets ("" for none:
// every tier).
func pbtGenConfig(tc hegel.TestCase, defCap Tier) pbtConfig {
	var b strings.Builder
	b.WriteString("version = 2\n")
	cfg := pbtConfig{role: map[string]string{}, tier: map[string]string{}}
	for _, role := range pbtRoles {
		cfg.role[role] = pbtCell(&b, tc, "roles."+role)
		for _, tier := range pbtTierNames {
			if !hegel.Draw(tc, hegel.WeightedBooleans(0.5)) {
				continue
			}
			cfg.tier[role+"/"+string(tier)] = pbtCell(&b, tc, "roles."+role+".tiers."+string(tier))
		}
	}
	b.WriteString("\n[testing.commands]\ntree = { command = \"test -f README.md\" }\n")
	admittedDefaults := pbtTierNames
	if defCap != "" {
		admittedDefaults = pbtTierNames[:pbtTierAt[defCap]+1]
	}
	if cfg.policy = hegel.Draw(tc, hegel.Booleans()); cfg.policy {
		cfg.def = hegel.Draw(tc, hegel.SampledFrom(admittedDefaults))
		fmt.Fprintf(&b, "\n[tier_policy]\ndefault = %q\n", cfg.def)
		if cfg.ceiling = hegel.Draw(tc, hegel.Booleans()); cfg.ceiling {
			cfg.ceil = hegel.Draw(tc, hegel.SampledFrom(pbtTierNames[pbtTierAt[cfg.def]:]))
			fmt.Fprintf(&b, "ceiling = %q\n", cfg.ceil)
		}
		// No start rule may name a tier above the default, so the rules draw
		// from the tiers the declared default admits.
		admitted := pbtTierNames[:pbtTierAt[cfg.def]+1]
		cfg.starts = hegel.Draw(tc, hegel.Integers(1, 2))
		for range cfg.starts {
			fmt.Fprintf(&b, "\n[[tier_policy.start]]\ntier = %q\ntypes = [\"chore\"]\n",
				hegel.Draw(tc, hegel.SampledFrom(admitted)))
		}
	}
	cfg.text = b.String()
	return cfg
}

// pbtWriteConfig writes the drawn files into a fresh .tick and returns the
// common file's path.
func pbtWriteConfig(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".tick")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "runners.toml")
}

func TestPBTTheOverrideFileOverlaysItNeverErases(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		common, other := pbtGenConfig(ht, ""), pbtGenConfig(ht, "")
		defCap := TierFrontier
		if common.ceiling {
			defCap = common.ceil
		}
		over := pbtGenConfig(ht, defCap)
		// The interesting case is an override that names something the
		// common file also has: a role cell, a tier cell or the policy.
		ht.Assume(len(over.role) > 0 || over.policy)
		sub, name, otherName := SubstrateCloud, "runners.cloud.toml", "runners.local.toml"
		if local := hegel.Draw(ht, hegel.Booleans()); local {
			sub, name, otherName = SubstrateHerdr, "runners.local.toml", "runners.cloud.toml"
		}

		// With no override on disk, a run reads exactly what it always read:
		// LoadFor over a directory with one file is Parse over those bytes.
		plain := pbtWriteConfig(t, map[string]string{"runners.toml": common.text})
		base, err := LoadFor(plain, sub)
		if err != nil {
			ht.Fatalf("the common file alone does not load: %v\n%s", err, common.text)
		}
		reference, err := Parse([]byte(common.text))
		if err != nil {
			ht.Fatalf("the common file does not parse: %v\n%s", err, common.text)
		}
		if !pbtSameSurface(base, reference, sub) {
			ht.Fatalf("a directory with no override file did not read exactly the common file:\n%s", common.text)
		}
		layered := pbtWriteConfig(t, map[string]string{"runners.toml": common.text, name: over.text})
		merged, err := LoadFor(layered, sub)
		if err != nil {
			ht.Fatalf("the drawn files do not load: %v\n--- common\n%s\n--- override\n%s", err, common.text, over.text)
		}

		// TABLES MERGE FIELD-WISE, ARRAYS REPLACE: the merged tier policy is
		// the common's where the override is silent, the override's where it
		// speaks, and its start rules are replaced, never concatenated.
		if common.policy || over.policy {
			if merged.TierPolicy == nil {
				ht.Fatalf("a policy one of the files declares was erased:\n--- common\n%s\n--- override\n%s",
					common.text, over.text)
			}
			wantDef, wantStarts := common.def, common.starts
			if over.policy {
				wantDef, wantStarts = over.def, over.starts
			}
			if merged.TierPolicy.Default != wantDef {
				ht.Fatalf("the merged policy's default is %q, want %q:\n--- common\n%s\n--- override\n%s",
					merged.TierPolicy.Default, wantDef, common.text, over.text)
			}
			if len(merged.TierPolicy.Start) != wantStarts {
				ht.Fatalf("%d start rules survived a merge that must replace: the common declared %d, the override %d"+
					"\n--- common\n%s\n--- override\n%s",
					len(merged.TierPolicy.Start), common.starts, over.starts, common.text, over.text)
			}
			wantCeil, haveCeil := common.ceil, common.ceiling
			if over.ceiling {
				wantCeil, haveCeil = over.ceil, true
			}
			if haveCeil && merged.TierPolicy.Ceiling != wantCeil {
				ht.Fatalf("the merged policy's ceiling is %q, want %q:\n--- common\n%s\n--- override\n%s",
					merged.TierPolicy.Ceiling, wantCeil, common.text, over.text)
			}
		}

		// THE OVERRIDE'S ROLE CELL APPLIES LAST: whatever tier the common file
		// declares for the role, the override's own cell for it wins, and the
		// override's own tier cell wins over both. A tier the common file
		// declares that the override does not is the case ea1a62d3 was: the
		// common's claude frontier must not reach a cloud run whose override
		// routes the role to pi.
		for _, role := range pbtRoles {
			roleCell, hasRoleCell := over.role[role]
			for _, tier := range append([]Tier{""}, pbtTierNames...) {
				w, err := merged.ResolveOn(sub, role, tier)
				switch {
				case err != nil:
					// A refusal is always allowed — a tier the override's
					// role cell does not carry, and (cloud) a role the cloud
					// file omits — but only where the generator drew one.
					if sub == SubstrateCloud && hasRoleCell {
						ht.Fatalf("%s at tier %q was refused under a cloud file that routes it: %v"+
							"\n--- common\n%s\n--- override\n%s", role, tier, err, common.text, over.text)
					}
					continue
				case hasRoleCell:
					want := roleCell
					if ownTier, ok := over.tier[role+"/"+string(tier)]; ok {
						want = ownTier
					}
					if w.Model != want {
						ht.Fatalf("%s at tier %q resolved to %s/%s, want the override's %q: the override's "+
							"role cell must apply after the common file's tier\n--- common\n%s\n--- override\n%s",
							role, tier, w.Kind, w.Model, want, common.text, over.text)
					}
				case sub == SubstrateCloud:
					// A cloud run may not fall back to the common file's
					// routing for a role the cloud file omits: the base cell
					// was written for a laptop, and using it is how a container
					// reaches a harness nobody chose (tick 84z).
					ht.Fatalf("%s at tier %q resolved on the cloud substrate for a role the cloud file "+
						"does not route (%s/%s): the refusal is ErrNoCloudRouting\n--- common\n%s\n--- override\n%s",
						role, tier, w.Kind, w.Model, common.text, over.text)
				}
			}
		}

		// THE OTHER WORLD'S FILE IS INVISIBLE: adding the file a DIFFERENT
		// substrate reads changes nothing about this substrate's run — the
		// cloud never reads runners.local.toml, and herdr never reads the
		// cloud's.
		withOther := pbtWriteConfig(t, map[string]string{
			"runners.toml": common.text, name: over.text, otherName: other.text})
		touched, err := LoadFor(withOther, sub)
		if err != nil {
			ht.Fatalf("a directory also carrying %s refused a %s run: %v", otherName, sub, err)
		}
		if !pbtSameSurface(merged, touched, sub) {
			ht.Fatalf("%s changed what a %s run reads:\n--- common\n%s\n--- override\n%s\n--- other world\n%s",
				otherName, sub, common.text, over.text, other.text)
		}
	}, hegel.WithTestCases(200))
}

// pbtSameSurface compares two loaded configs on everything a run reads: the
// tier policy, the override file it recorded, and the resolution of every
// role at every tier — which is the surface the layering exists to change.
func pbtSameSurface(a, b *Config, sub Substrate) bool {
	if (a.TierPolicy == nil) != (b.TierPolicy == nil) {
		return false
	}
	// The override file's NAME is the surface; its directory is wherever the
	// test wrote the files this case drew, and two loads of the same files in
	// two directories read the same thing.
	if filepath.Base(a.OverrideFile) != filepath.Base(b.OverrideFile) {
		return false
	}
	if a.TierPolicy != nil && !reflect.DeepEqual(*a.TierPolicy, *b.TierPolicy) {
		return false
	}
	for _, role := range pbtRoles {
		for _, tier := range append([]Tier{""}, pbtTierNames...) {
			wa, errA := a.ResolveOn(sub, role, tier)
			wb, errB := b.ResolveOn(sub, role, tier)
			if (errA == nil) != (errB == nil) {
				return false
			}
			if errA != nil || errB != nil {
				if errA == nil || errB == nil || errA.Error() != errB.Error() {
					return false
				}
				continue
			}
			if !reflect.DeepEqual(wa, wb) {
				return false
			}
		}
	}
	return true
}
