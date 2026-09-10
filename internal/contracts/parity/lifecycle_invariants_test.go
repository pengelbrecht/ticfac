package parity

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// contracts/lifecycle-invariants.json — EXECUTABLE.
//
// SPEC Appendix A's thirteen invariants as a conformance suite, each earned by
// a live failure. It is a claim about who has to run it as much as about what
// it asserts: `gate.applies_to` names the reconciler and every executor the
// SPEC plans, and all of those are built HERE. This package is where ticfac
// starts passing it; the reconciler and the local subprocess executor go on to
// pass the same suite through the same fixture.
//
// Two differences from ticks' reader, both forced by the split and both
// deliberate:
//
//   - `today` names files in ticks and in the factory. ticks' reader greps
//     them; this one cannot, and inventing a copy of those files to grep would
//     be worse than not checking. So the cross-reference is checked for SHAPE
//     here (every invariant names a site, every site names symbols and a note),
//     which is exactly what the vitest reader does for the same reason.
//   - `thresholds.substrate` names constants in the factory's TypeScript and
//     in a shell script. The RELATION Appendix A #4 is about is asserted here;
//     the equality to each constant stays with the readers that can reach them.
//
// Everything else — the harness, the sequences, and the one-guard-at-a-time
// negative control — runs in full.

const lifecycleFile = "lifecycle-invariants.json"

type lifecycleStep struct {
	Op     string `json:"op"`
	Expect string `json:"expect"`

	Job           string         `json:"job"`
	By            string         `json:"by"`
	As            string         `json:"as"`
	Tick          string         `json:"tick"`
	Path          string         `json:"path"`
	EvidencePath  string         `json:"evidence_path"`
	Actor         string         `json:"actor"`
	Content       map[string]any `json:"content"`
	SilentlyDrops bool           `json:"silently_drops"`
	Ms            int64          `json:"ms"`
	CapMs         int64          `json:"cap_ms"`
	Class         string         `json:"class"`
	Message       string         `json:"message"`
	Unit          string         `json:"unit"`
	Requested     float64        `json:"requested"`
	Ceiling       float64        `json:"ceiling"`
	Key           string         `json:"key"`

	Fingerprint map[string]string `json:"fingerprint"`
	Target      map[string]string `json:"target"`
}

type lifecycleBudget struct {
	Requested float64 `json:"requested"`
	Ceiling   float64 `json:"ceiling"`
	Effective float64 `json:"effective"`
	Reported  float64 `json:"reported"`
	Clamped   bool    `json:"clamped"`
}

// lifecycleFinal is the assertion after a sequence. Every field is optional: a
// sequence asserts the part of the model its invariant is about, and asserting
// the rest would make each sequence a test of the fake instead of the rule.
type lifecycleFinal struct {
	BootedJobs        []string                  `json:"booted_jobs"`
	IssuedCredentials []string                  `json:"issued_credentials"`
	TornDown          []string                  `json:"torn_down"`
	Liveness          map[string]string         `json:"liveness"`
	StepSpentMs       *int64                    `json:"step_spent_ms"`
	Origin            map[string]map[string]any `json:"origin"`
	Dispatches        map[string]int            `json:"dispatches"`
	Settled           []string                  `json:"settled"`
	ReportedClasses   []string                  `json:"reported_classes"`
	BoundaryReports   []string                  `json:"boundary_reports"`
	ReleasedBy        map[string]string         `json:"released_by"`
	Budget            *lifecycleBudget          `json:"budget"`
	EvidenceKeys      []string                  `json:"evidence_keys"`
	PublishedKeys     []string                  `json:"published_keys"`
}

type lifecycleSequence struct {
	ID    string          `json:"id"`
	Why   string          `json:"why"`
	Steps []lifecycleStep `json:"steps"`
	Final lifecycleFinal  `json:"final"`
}

type lifecycleSite struct {
	File    string   `json:"file"`
	Symbols []string `json:"symbols"`
	Note    string   `json:"note"`
}

type invariant struct {
	ID         string              `json:"id"`
	Number     int                 `json:"number"`
	Name       string              `json:"name"`
	Title      string              `json:"title"`
	Statement  string              `json:"statement"`
	EarnedFrom string              `json:"earned_from"`
	Guards     []string            `json:"guards"`
	Today      []lifecycleSite     `json:"today"`
	Sequences  []lifecycleSequence `json:"sequences"`
}

type substrateSource struct {
	File   string `json:"file"`
	Symbol string `json:"symbol"`
	Form   string `json:"form"`
	Note   string `json:"note"`
}

type lifecycleThresholds struct {
	WipeThresholdMs int64 `json:"wipe_threshold_ms"`
	MaxPollMs       int64 `json:"max_poll_ms"`
	PushIntervalMs  int64 `json:"push_interval_ms"`
	StepCapMs       int64 `json:"step_cap_ms"`

	Substrate map[string]substrateSource `json:"substrate"`
}

type lifecycleContract struct {
	SchemaVersion int      `json:"schema_version"`
	Contract      string   `json:"contract"`
	Spec          string   `json:"spec"`
	SpecSections  []string `json:"spec_sections"`
	Why           []string `json:"why"`

	Gate struct {
		Statement      string   `json:"statement"`
		AppliesTo      []string `json:"applies_to"`
		NotAStyleGuide string   `json:"not_a_style_guide"`
		WhyOneSuite    string   `json:"why_one_suite"`
	} `json:"gate"`

	Harness struct {
		Why               []string            `json:"why"`
		State             []string            `json:"state"`
		Thresholds        lifecycleThresholds `json:"thresholds"`
		ProtectedPrefixes struct {
			Prefixes []string `json:"prefixes"`
			Why      []string `json:"why"`
		} `json:"protected_prefixes"`
		Rules []string `json:"rules"`
		Ops   []struct {
			Op       string   `json:"op"`
			Does     string   `json:"does"`
			Args     []string `json:"args"`
			Outcomes []string `json:"outcomes"`
		} `json:"ops"`
		Guards []struct {
			Guard    string `json:"guard"`
			Enforces string `json:"enforces"`
			Off      string `json:"off"`
		} `json:"guards"`
		FingerprintFields struct {
			DefinedBy struct {
				Contract string `json:"contract"`
				File     string `json:"file"`
				Pointer  string `json:"pointer"`
				SchemaID string `json:"schema_id"`
			} `json:"defined_by"`
			Fields []struct {
				AppendixA       string `json:"appendix_a"`
				ProvenanceField string `json:"provenance_field"`
			} `json:"fields"`
		} `json:"fingerprint_fields"`
	} `json:"harness"`

	Invariants []invariant `json:"invariants"`
}

func loadLifecycle(t *testing.T) lifecycleContract {
	t.Helper()
	var c lifecycleContract
	readContract(t, lifecycleFile, &c)
	return c
}

// byID finds one invariant. Each of the thirteen named tests below calls it,
// so a fixture that loses an invariant fails as thirteen missing tests rather
// than as a table that quietly got shorter.
func byID(t *testing.T, id string) (lifecycleContract, invariant) {
	t.Helper()
	c := loadLifecycle(t)
	for _, inv := range c.Invariants {
		if inv.ID == id {
			return c, inv
		}
	}
	t.Fatalf("%s declares no invariant %s", lifecycleFile, id)
	return c, invariant{}
}

// ------------------------------------------------------------- the shape ---

func TestLifecycleContractIdentifiesItself(t *testing.T) {
	c := loadLifecycle(t)
	if c.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", c.SchemaVersion)
	}
	if c.Contract != "ticfac.lifecycle_invariants" {
		t.Errorf("contract = %q, want ticfac.lifecycle_invariants", c.Contract)
	}
	if c.Spec == "" || len(c.SpecSections) == 0 {
		t.Error("the contract must name the spec and the sections it freezes")
	}
	if len(c.Why) == 0 || len(c.Harness.Why) == 0 {
		t.Error("both the contract and its harness must say why they exist")
	}
}

// The gate is the deliverable as much as the sequences are: it is a claim
// about who has to run the suite, and ticfac is who.
func TestGateNamesWhoMustPassItAndBindsTicfac(t *testing.T) {
	c := loadLifecycle(t)
	if c.Gate.Statement == "" || c.Gate.NotAStyleGuide == "" || c.Gate.WhyOneSuite == "" {
		t.Error("the gate must state itself, and say why it is not waivable and why it is one suite")
	}
	if len(c.Gate.AppliesTo) < 3 {
		t.Errorf("the gate names %d subjects; it must name the reconciler and each executor the SPEC plans",
			len(c.Gate.AppliesTo))
	}
	for _, who := range c.Gate.AppliesTo {
		if !strings.Contains(who, "Phase") {
			t.Errorf("gate subject %q does not say which SPEC phase brings it", who)
		}
	}
	// The two this repository builds in Phase 1, named by the gate.
	joined := strings.ToLower(strings.Join(c.Gate.AppliesTo, " | "))
	for _, subject := range []string{"reconciler", "executor"} {
		if !strings.Contains(joined, subject) {
			t.Errorf("the gate does not name the %s; it is what this repository is building", subject)
		}
	}
}

// Thirteen, numbered one to thirteen, each with the Appendix A statement it
// encodes and the live failure that earned it. An invariant with no
// `earned_from` is guidance wearing a conformance test's clothes.
func TestThirteenInvariantsOnePerAppendixAEntry(t *testing.T) {
	c := loadLifecycle(t)

	if len(c.Invariants) != 13 {
		t.Fatalf("the contract declares %d invariants; Appendix A has 13", len(c.Invariants))
	}

	seen := map[int]bool{}
	names := map[string]bool{}
	for i, inv := range c.Invariants {
		if inv.Number != i+1 {
			t.Errorf("invariant %d is numbered %d; the order must be Appendix A's", i+1, inv.Number)
		}
		if inv.ID != fmt.Sprintf("A%d", inv.Number) {
			t.Errorf("invariant %d has id %q, want A%d", inv.Number, inv.ID, inv.Number)
		}
		if seen[inv.Number] {
			t.Errorf("invariant number %d appears twice", inv.Number)
		}
		seen[inv.Number] = true
		if names[inv.Name] {
			t.Errorf("invariant name %q appears twice", inv.Name)
		}
		names[inv.Name] = true
		if inv.Name == "" || strings.ToLower(inv.Name) != inv.Name || strings.Contains(inv.Name, " ") {
			t.Errorf("%s: name %q must be a kebab-case test name", inv.ID, inv.Name)
		}
		if inv.Title == "" || inv.Statement == "" {
			t.Errorf("%s must carry Appendix A's title and statement", inv.ID)
		}
		if len(inv.EarnedFrom) < 60 {
			t.Errorf("%s: earned_from is %d characters; each invariant names the live failure that earned it",
				inv.ID, len(inv.EarnedFrom))
		}
		if len(inv.Guards) == 0 {
			t.Errorf("%s names no guard, so nothing can prove its sequences test anything", inv.ID)
		}
		if len(inv.Sequences) == 0 {
			t.Errorf("%s has no sequence, so it is not runnable", inv.ID)
		}
		for _, seq := range inv.Sequences {
			if seq.ID == "" || seq.Why == "" {
				t.Errorf("%s: every sequence must have an id and say what it proves", inv.ID)
			}
			if len(seq.Steps) == 0 {
				t.Errorf("%s/%s has no steps", inv.ID, seq.ID)
			}
		}
	}
}

// §9.2 preserves the symbols when run-workflow.ts is decomposed; `today`
// preserves the reasons. ticfac cannot open those files — they are in ticks and
// in the factory — so this reader checks the cross-reference's SHAPE, exactly
// as the vitest reader does and for the same reason. The existence check stays
// with the reader that has the files.
func TestEveryInvariantCrossReferencesWhereItLivesToday(t *testing.T) {
	c := loadLifecycle(t)

	for _, inv := range c.Invariants {
		if len(inv.Today) == 0 {
			t.Errorf("%s names no implementation it was extracted from", inv.ID)
			continue
		}
		for _, s := range inv.Today {
			if s.File == "" || len(s.Symbols) == 0 {
				t.Errorf("%s: a `today` site must name a file and at least one symbol", inv.ID)
			}
			if s.Note == "" {
				t.Errorf("%s: %s carries no note saying what the symbols do about this invariant", inv.ID, s.File)
			}
			for _, symbol := range s.Symbols {
				if strings.TrimSpace(symbol) == "" {
					t.Errorf("%s: %s lists an empty symbol", inv.ID, s.File)
				}
			}
		}
	}
}

// ------------------------------------------------------ the op vocabulary ---

// Every op and outcome a sequence uses is declared, and every declared op and
// outcome is reached — in one guard mode or the other. The guard-off answers
// (`recorded`, `stuck_awaiting_claimer`, `reported_requested`, a clock's
// `released`) are exactly what a WRONG implementation produces, so the
// vocabulary is closed over both modes on purpose.
func TestFixtureUsesOnlyDeclaredOpsAndOutcomes(t *testing.T) {
	c := loadLifecycle(t)

	declared := map[string]map[string]bool{}
	for _, op := range c.Harness.Ops {
		if op.Does == "" {
			t.Errorf("op %q does not say what it does", op.Op)
		}
		outcomes := map[string]bool{}
		for _, o := range op.Outcomes {
			outcomes[o] = true
		}
		declared[op.Op] = outcomes
	}
	if len(declared) == 0 {
		t.Fatal("the harness declares no ops")
	}
	if len(c.Harness.Rules) == 0 || len(c.Harness.State) == 0 {
		t.Fatal("the harness's state and rules are what a second implementation copies; they are missing")
	}

	used := map[string]map[string]bool{}
	reach := func(op, outcome string) {
		if used[op] == nil {
			used[op] = map[string]bool{}
		}
		used[op][outcome] = true
	}

	for _, inv := range c.Invariants {
		for _, seq := range inv.Sequences {
			for i, s := range seq.Steps {
				outcomes, ok := declared[s.Op]
				if !ok {
					t.Errorf("%s/%s step %d uses undeclared op %q", inv.ID, seq.ID, i, s.Op)
					continue
				}
				if !outcomes[s.Expect] {
					t.Errorf("%s/%s step %d expects %q, which op %q does not declare",
						inv.ID, seq.ID, i, s.Expect, s.Op)
				}
				reach(s.Op, s.Expect)
			}

			h := newLifecycleHarness(c)
			for _, g := range inv.Guards {
				h.off[g] = true
			}
			for _, s := range seq.Steps {
				reach(s.Op, h.run(t, s))
			}
		}
	}

	for _, op := range sortedKeys(declared) {
		if used[op] == nil {
			t.Errorf("op %q is declared but no sequence uses it", op)
			continue
		}
		for _, outcome := range sortedKeys(declared[op]) {
			if !used[op][outcome] {
				t.Errorf("op %q declares outcome %q, which no sequence reaches in either guard mode", op, outcome)
			}
		}
		for outcome := range used[op] {
			if !declared[op][outcome] {
				t.Errorf("op %q produced undeclared outcome %q", op, outcome)
			}
		}
	}
}

// Every guard belongs to an invariant and every invariant's guards exist.
func TestGuardsAndInvariantsAccountForEachOther(t *testing.T) {
	c := loadLifecycle(t)

	declared := map[string]bool{}
	for _, g := range c.Harness.Guards {
		if g.Enforces == "" || g.Off == "" {
			t.Errorf("guard %q must say what it enforces and what happens with it off", g.Guard)
		}
		if declared[g.Guard] {
			t.Errorf("guard %q is declared twice", g.Guard)
		}
		declared[g.Guard] = true
	}

	claimed := map[string]bool{}
	for _, inv := range c.Invariants {
		for _, g := range inv.Guards {
			if !declared[g] {
				t.Errorf("%s names guard %q, which the harness does not declare", inv.ID, g)
			}
			if claimed[g] {
				t.Errorf("guard %q is claimed by more than one invariant; a guard belongs to the rule it enforces", g)
			}
			claimed[g] = true
		}
	}
	for _, g := range sortedKeys(declared) {
		if !claimed[g] {
			t.Errorf("guard %q is declared but no invariant claims it", g)
		}
	}
}

// A4's whole point: the relationship between the poll cadence and the
// substrate's wipe threshold is pinned in ONE place, not recomputed in two
// files. The equality to each named constant belongs to the readers that can
// reach them; the relation is asserted here, and so is the completeness of the
// mapping — a threshold that stops naming a substrate constant is a number
// describing a host nobody checked.
func TestPollCadenceIsPinnedUnderTheWipeThreshold(t *testing.T) {
	c := loadLifecycle(t)
	th := c.Harness.Thresholds

	if th.WipeThresholdMs <= 0 || th.MaxPollMs <= 0 || th.PushIntervalMs <= 0 || th.StepCapMs <= 0 {
		t.Fatalf("every threshold must be positive: %+v", th)
	}
	if th.MaxPollMs >= th.WipeThresholdMs {
		t.Errorf("max_poll_ms %d is not under wipe_threshold_ms %d — polling is not a keepalive",
			th.MaxPollMs, th.WipeThresholdMs)
	}
	if th.MaxPollMs*2 > th.WipeThresholdMs {
		t.Errorf("max_poll_ms %d leaves no margin under wipe_threshold_ms %d; Appendix A #4 says WELL under",
			th.MaxPollMs, th.WipeThresholdMs)
	}
	if th.PushIntervalMs >= th.MaxPollMs {
		t.Errorf("push_interval_ms %d is not under max_poll_ms %d; a job's work must reach origin more often than the reconciler looks",
			th.PushIntervalMs, th.MaxPollMs)
	}

	for _, name := range []string{"wipe_threshold_ms", "max_poll_ms", "push_interval_ms", "step_cap_ms"} {
		src, ok := th.Substrate[name]
		if !ok {
			t.Errorf("threshold %s names no substrate constant, so nothing ties it to the host it describes", name)
			continue
		}
		if src.File == "" || src.Symbol == "" || src.Form == "" || src.Note == "" {
			t.Errorf("threshold %s: substrate source is incomplete: %+v", name, src)
		}
	}
	if len(th.Substrate) != 4 {
		t.Errorf("substrate maps %d thresholds; there are four", len(th.Substrate))
	}
}

// A10's boundary lives in the fixture, not in the harness. ticfac's copy reads
// it from there, so the two implementations cannot drift apart with every
// sequence still green.
func TestProtectedPrefixesLiveInTheContract(t *testing.T) {
	c := loadLifecycle(t)
	prefixes := c.Harness.ProtectedPrefixes.Prefixes

	if len(prefixes) == 0 {
		t.Fatal("the harness declares no protected prefixes, so A10's boundary is back in the reader")
	}
	if len(c.Harness.ProtectedPrefixes.Why) != len(prefixes) {
		t.Errorf("%d protected prefixes and %d reasons; each says which authority it protects",
			len(prefixes), len(c.Harness.ProtectedPrefixes.Why))
	}
	for _, prefix := range prefixes {
		if !strings.HasSuffix(prefix, "/") {
			t.Errorf("protected prefix %q is not a directory prefix; a bare name matches a file that merely starts with it", prefix)
		}
	}

	_, a10 := byID(t, "A10")
	refused, permitted := 0, 0
	for _, seq := range a10.Sequences {
		for _, s := range seq.Steps {
			if s.Op != "attempt_boundary_write" {
				continue
			}
			under := false
			for _, prefix := range prefixes {
				if strings.HasPrefix(s.Path, prefix) {
					under = true
					break
				}
			}
			switch s.Expect {
			case "refused_and_reported":
				refused++
				if !under {
					t.Errorf("A10 expects %s to be refused, but it is under no declared prefix %v", s.Path, prefixes)
				}
			case "permitted":
				permitted++
				if under {
					t.Errorf("A10 expects %s to be permitted, but it is under a declared prefix", s.Path)
				}
			}
		}
	}
	if refused < len(prefixes) || permitted == 0 {
		t.Errorf("A10 exercises %d refusals and %d permitted writes; each prefix needs a refusal and the boundary needs a path outside it",
			refused, permitted)
	}
}

// ---------------------------------------------------------- the cross-file ---

// A13's fingerprint fields are NOT defined here. They are the provenance object
// of the bundle's one evidence record, and this contract maps Appendix A's four
// English names onto it. The mapping is followed rather than trusted.
func TestFingerprintFieldsResolveToTheEvidenceRecordsProvenance(t *testing.T) {
	c := loadLifecycle(t)
	ff := c.Harness.FingerprintFields

	if ff.DefinedBy.File != jobProtocolFile || ff.DefinedBy.Pointer != "#/$defs/provenance" {
		t.Fatalf("fingerprint_fields must point at %s #/$defs/provenance, got %s %s",
			jobProtocolFile, ff.DefinedBy.File, ff.DefinedBy.Pointer)
	}
	if len(ff.Fields) != 4 {
		t.Fatalf("Appendix A #13 names four fingerprint fields; the contract maps %d", len(ff.Fields))
	}

	jp, _, _ := loadJobProtocol(t)
	if got := jp.Records["evidence"].SchemaID; got != ff.DefinedBy.SchemaID {
		t.Errorf("this contract names schema_id %q; %s's evidence record is %q",
			ff.DefinedBy.SchemaID, jobProtocolFile, got)
	}

	raw, ok := jp.Defs["provenance"]
	if !ok {
		t.Fatalf("%s has no $defs.provenance", jobProtocolFile)
	}
	var prov struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &prov); err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, name := range prov.Required {
		required[name] = true
	}
	for _, f := range ff.Fields {
		if f.AppendixA == "" {
			t.Errorf("fingerprint field %q does not say which Appendix A name it carries", f.ProvenanceField)
		}
		if _, ok := prov.Properties[f.ProvenanceField]; !ok {
			t.Errorf("fingerprint field %q is not a property of %s $defs.provenance", f.ProvenanceField, jobProtocolFile)
		}
		if !required[f.ProvenanceField] {
			t.Errorf("fingerprint field %q is not REQUIRED by $defs.provenance; evidence that may omit it is not fingerprinted",
				f.ProvenanceField)
		}
	}
}

// ------------------------------------------------------------- the runner ---

// runSequences replays every sequence of one invariant against a fresh fake
// and checks each step's outcome and the final state.
func runSequences(t *testing.T, c lifecycleContract, inv invariant) {
	t.Helper()
	for _, seq := range inv.Sequences {
		seq := seq
		t.Run(seq.ID, func(t *testing.T) {
			h := newLifecycleHarness(c)
			for i, s := range seq.Steps {
				got := h.run(t, s)
				if got != s.Expect {
					t.Fatalf("step %d (%s): outcome %q, contract says %q", i, s.Op, got, s.Expect)
				}
			}
			for _, m := range finalMismatches(h, seq.Final) {
				t.Error(m)
			}
		})
	}
}

// disablingTheGuardBreaksIt is the negative control, run ONE GUARD AT A TIME.
// Turning an invariant's guards off together cannot see a dead guard: A1 and
// A13 have two each, and the first one's divergence would satisfy the whole
// control while the second could have stopped enforcing anything.
//
// Each guard also carries a BLAST RADIUS assertion: with it off, every OTHER
// invariant still passes. That makes "a guard belongs to the rule it enforces"
// executable rather than a naming convention.
func disablingTheGuardBreaksIt(t *testing.T, c lifecycleContract, inv invariant) {
	t.Helper()

	for _, guard := range inv.Guards {
		guard := guard
		t.Run("without_"+guard, func(t *testing.T) {
			broke := false
			for _, seq := range inv.Sequences {
				h := newLifecycleHarness(c)
				h.off[guard] = true
				diverged := false
				for _, s := range seq.Steps {
					if got := h.run(t, s); got != s.Expect {
						diverged = true
					}
				}
				if diverged || !finalMatches(h, seq.Final) {
					broke = true
				}
			}
			if !broke {
				t.Errorf("%s passes with %s disabled — that guard is not what its sequences are testing",
					inv.ID, guard)
			}
			otherInvariantsStayGreen(t, c, inv.ID, guard)
		})
	}
}

func otherInvariantsStayGreen(t *testing.T, c lifecycleContract, owner, guard string) {
	t.Helper()
	for _, other := range c.Invariants {
		if other.ID == owner {
			continue
		}
		for _, seq := range other.Sequences {
			h := newLifecycleHarness(c)
			h.off[guard] = true
			for i, s := range seq.Steps {
				if got := h.run(t, s); got != s.Expect {
					t.Errorf("%s/%s step %d (%s) answers %q with %s's guard %s disabled, contract says %q — the guard is shared",
						other.ID, seq.ID, i, s.Op, got, owner, guard, s.Expect)
				}
			}
			for _, m := range finalMismatches(h, seq.Final) {
				t.Errorf("%s/%s final state moved when %s's guard %s was disabled: %s",
					other.ID, seq.ID, owner, guard, m)
			}
		}
	}
}
