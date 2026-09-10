package parity

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/ticfac-run-state.json — EXECUTABLE.
//
// The `.ticfac/` layout, the persistence policy (durable means pushed on
// origin) and the compare-and-swap rules, with record schemas, golden and
// negative examples, the `.gitignore` fragment and seven executable CAS
// sequences. It is ticfac's own contract and the one this repository will be
// held to hardest: it says what a run leaves behind and how a restarted
// reconciler reads it.
//
// Four things are executed here rather than described:
//
//   - the three record schemas admit their golden documents and refuse their
//     negative ones, with the pinned refusal;
//   - the evidence example is validated against the OTHER contract's schema,
//     because this file places the record and job-protocol.json defines it;
//   - the copied `$defs` are compared STRUCTURALLY with job-protocol.json's,
//     since the strict subset has no cross-file $ref and a copy nothing
//     compares is how bundle 1.2.0's divergence started;
//   - the `.gitignore` fragment is asserted with `git check-ignore` against
//     THIS repository, because "the fragment is defined" has to mean git
//     applies it.

const runStateFile = "ticfac-run-state.json"

type casStep struct {
	Actor           string         `json:"actor"`
	Op              string         `json:"op"`
	Path            string         `json:"path"`
	Content         map[string]any `json:"content"`
	Expect          string         `json:"expect"`
	EffectPermitted *bool          `json:"effect_permitted"`
}

type runState struct {
	SchemaVersion int    `json:"schema_version"`
	Contract      string `json:"contract"`
	Spec          string `json:"spec"`
	Layout        struct {
		Root             string `json:"root"`
		RunDir           string `json:"run_dir"`
		OneFilePerRecord bool   `json:"one_file_per_record"`
		Entries          []struct {
			Path        string `json:"path"`
			Record      string `json:"record"`
			Committed   bool   `json:"committed"`
			Cardinality string `json:"cardinality"`
			CAS         string `json:"cas"`
			FirstWrite  string `json:"first_write"`
			WrittenOn   string `json:"written_on"`
			Why         string `json:"why"`
		} `json:"entries"`
	} `json:"layout"`
	Boundary struct {
		OnlyWriter      string `json:"only_writer"`
		WorkersWrite    bool   `json:"workers_write"`
		TicksReads      bool   `json:"ticks_reads_ticfac"`
		IsConfiguration bool   `json:"is_configuration"`
	} `json:"boundary"`
	Persistence struct {
		DurableMeans                  string `json:"durable_means"`
		LocalCommitIsDurable          bool   `json:"local_commit_is_durable"`
		WriteCommitPushIsOneOperation bool   `json:"write_commit_push_is_one_operation"`
		CheckpointOn                  string `json:"checkpoint_on"`
		CheckpointOnObservation       bool   `json:"checkpoint_on_observation"`
		ConflictIs                    string `json:"conflict_is"`
		ConflictRetryBlindly          bool   `json:"conflict_retry_blindly"`
	} `json:"persistence"`
	Gitignore struct {
		Target          string   `json:"target"`
		BeginMarker     string   `json:"begin_marker"`
		EndMarker       string   `json:"end_marker"`
		Fragment        []string `json:"fragment"`
		IgnoredExamples []string `json:"ignored_examples"`
		TrackedExamples []string `json:"tracked_examples"`
	} `json:"gitignore"`
	Envelope struct {
		RequiredOnEveryCommittedRecord []string `json:"required_on_every_committed_record"`
		ProvenanceIs                   string   `json:"provenance_is"`
	} `json:"envelope"`
	References struct {
		Evidence struct {
			Record   string `json:"record"`
			SchemaID string `json:"schema_id"`
			Contract string `json:"contract"`
			File     string `json:"file"`
			Pointer  string `json:"pointer"`
			KeyIs    string `json:"key_is"`
		} `json:"evidence"`
	} `json:"references"`
	Defs    map[string]json.RawMessage `json:"$defs"`
	Schemas map[string]json.RawMessage `json:"schemas"`
	Golden  map[string]json.RawMessage `json:"golden"`
	Invalid []struct {
		Record              string          `json:"record"`
		ValidatedAgainst    string          `json:"validated_against"`
		Why                 string          `json:"why"`
		ExpectErrorContains string          `json:"expect_error_contains"`
		Document            json.RawMessage `json:"document"`
	} `json:"invalid"`
	CAS struct {
		Ref                 string            `json:"ref"`
		LocalRefIsAuthority bool              `json:"local_ref_is_authority"`
		Mechanisms          map[string]string `json:"mechanisms"`
		Modes               []struct {
			Mode                      string   `json:"mode"`
			Guard                     string   `json:"guard"`
			OnConflict                string   `json:"on_conflict"`
			EffectPermittedOnConflict bool     `json:"effect_permitted_on_conflict"`
			Records                   []string `json:"records"`
			Why                       string   `json:"why"`
		} `json:"modes"`
		Fake struct {
			Ops []struct {
				Op       string   `json:"op"`
				Does     string   `json:"does"`
				Outcomes []string `json:"outcomes"`
			} `json:"ops"`
			Rules []string `json:"rules"`
		} `json:"fake"`
		Sequences []struct {
			ID    string    `json:"id"`
			Why   string    `json:"why"`
			Steps []casStep `json:"steps"`
			Final struct {
				OriginWrites int                       `json:"origin_writes"`
				Files        map[string]map[string]any `json:"files"`
			} `json:"final"`
		} `json:"sequences"`
	} `json:"cas"`
}

func loadRunState(t *testing.T) (runState, map[string]*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	var c runState
	readContract(t, runStateFile, &c)

	defs := parseDefs(t, c.Defs)
	schemas := make(map[string]*schema.Schema, len(c.Schemas))
	for _, name := range sortedKeys(c.Schemas) {
		schemas[name] = parseSchema(t, "schemas."+name, c.Schemas[name])
	}
	return c, schemas, defs
}

func TestRunStateIdentityAndLayout(t *testing.T) {
	c, _, _ := loadRunState(t)

	if c.SchemaVersion != 1 || c.Contract != "ticfac.run_state" {
		t.Errorf("the contract does not identify itself: %q v%d", c.Contract, c.SchemaVersion)
	}
	if c.Layout.Root != ".ticfac" {
		t.Errorf("layout root = %q; the run state lives in .ticfac/", c.Layout.Root)
	}
	if !c.Layout.OneFilePerRecord {
		t.Error("one file per record is what lets concurrent runs merge cleanly")
	}
	if len(c.Layout.Entries) == 0 {
		t.Fatal("the layout declares no entries")
	}

	modes := map[string]bool{}
	for _, m := range c.CAS.Modes {
		modes[m.Mode] = true
		if m.EffectPermittedOnConflict {
			t.Errorf("cas mode %q permits its effect on conflict; a refused create means another "+
				"reconciler already dispatched, so this one must not", m.Mode)
		}
		if m.Guard == "" || m.OnConflict == "" || len(m.Records) == 0 {
			t.Errorf("cas mode %q is incompletely specified: %+v", m.Mode, m)
		}
	}
	for _, e := range c.Layout.Entries {
		if e.Path == "" {
			t.Errorf("a layout entry names no path: %+v", e)
			continue
		}
		// Only a COMMITTED entry is a record. The two uncommitted ones are the
		// derived index and the log directory, and the contract's point about
		// them is that they are exhaust, not that they have a schema.
		if e.Committed && e.Record == "" {
			t.Errorf("%s is committed and names no record", e.Path)
		}
		if !e.Committed && e.Record != "" {
			t.Errorf("%s is not committed and names record %q; an uncommitted record is not durable", e.Path, e.Record)
		}
		if !strings.HasPrefix(e.Path, c.Layout.Root+"/") {
			t.Errorf("%s is outside the run-state root %s", e.Path, c.Layout.Root)
		}
		if e.Committed && e.CAS == "" {
			t.Errorf("%s is committed and names no compare-and-swap mode", e.Path)
		}
		if e.CAS != "" && !modes[e.CAS] {
			t.Errorf("%s uses cas mode %q, which the contract does not declare", e.Path, e.CAS)
		}
		if e.Why == "" {
			t.Errorf("%s says nothing about why it exists", e.Path)
		}
	}

	// The boundary: one writer, and a worker is not it.
	if c.Boundary.OnlyWriter == "" || c.Boundary.WorkersWrite {
		t.Errorf("the boundary is not closed: only_writer=%q workers_write=%v",
			c.Boundary.OnlyWriter, c.Boundary.WorkersWrite)
	}
}

// Durable means pushed. A checkpoint that exists only in a working tree or a
// local commit does not exist — the lesson of a container that died holding
// its work.
func TestPersistencePolicyIsDurableMeansPushed(t *testing.T) {
	c, _, _ := loadRunState(t)

	if c.Persistence.LocalCommitIsDurable {
		t.Error("a local commit is durable, says the contract; a container that dies leaves its work on origin or nowhere")
	}
	if !c.Persistence.WriteCommitPushIsOneOperation {
		t.Error("write-commit-push must be ONE operation, or a crash between them is a record nobody has")
	}
	if !strings.Contains(strings.ToLower(c.Persistence.DurableMeans), "origin") {
		t.Errorf("durable_means no longer names origin: %q", c.Persistence.DurableMeans)
	}
	if c.Persistence.CheckpointOnObservation {
		t.Error("a checkpoint per observation is one commit per poll")
	}
	if c.Persistence.ConflictRetryBlindly {
		t.Error("a blind retry after a CAS conflict overwrites whatever moved the ref")
	}
	if c.CAS.LocalRefIsAuthority {
		t.Error("the local ref is the authority, says the contract; then the guard is against nothing shared")
	}
	if len(c.CAS.Mechanisms) < 2 {
		t.Errorf("the contract names %d CAS mechanisms; a local host and a Worker reach it differently", len(c.CAS.Mechanisms))
	}
}

func TestRunStateSchemasAdmitTheirGoldenDocuments(t *testing.T) {
	c, schemas, defs := loadRunState(t)

	for _, name := range sortedKeys(schemas) {
		raw, ok := c.Golden[name]
		if !ok {
			t.Errorf("record %s has no golden example", name)
			continue
		}
		if errs := schema.Validate(schemas[name], defs, decodeDocument(t, raw)); len(errs) != 0 {
			t.Errorf("the golden %s is refused by its own schema:\n  %s", name, strings.Join(errs, "\n  "))
		}
	}

	// The envelope, on every committed record: SPEC §10.4 is what makes one
	// loose JSON file readable years later by something that did not write it.
	for _, field := range c.Envelope.RequiredOnEveryCommittedRecord {
		for _, name := range sortedKeys(schemas) {
			required := false
			for _, r := range schemas[name].Required {
				if r == field {
					required = true
				}
			}
			if !required {
				t.Errorf("record %s does not require the envelope field %q", name, field)
			}
		}
	}
}

func TestRunStateRefusesEveryNegativeDocument(t *testing.T) {
	c, schemas, defs := loadRunState(t)
	evidenceSchema, evidenceDefs := jobProtocolEvidenceSchema(t)

	if len(c.Invalid) == 0 {
		t.Fatal("no negative example")
	}
	for _, bad := range c.Invalid {
		s, useDefs := schemas[bad.Record], defs
		if bad.ValidatedAgainst != "" {
			// The evidence record is validated against the contract that
			// DEFINES it, not against a second schema kept here. That is the
			// bundle 2.0.0 rule, followed rather than trusted.
			if bad.ValidatedAgainst != c.References.Evidence.SchemaID {
				t.Errorf("%s: validated_against %q, but this contract references %q",
					bad.Why, bad.ValidatedAgainst, c.References.Evidence.SchemaID)
				continue
			}
			s, useDefs = evidenceSchema, evidenceDefs
		}
		if s == nil {
			t.Errorf("negative example for unknown record %q", bad.Record)
			continue
		}
		errs := schema.Validate(s, useDefs, decodeDocument(t, bad.Document))
		if len(errs) == 0 {
			t.Errorf("a document the contract calls invalid was accepted: %s", bad.Why)
			continue
		}
		if !strings.Contains(strings.Join(errs, "\n"), bad.ExpectErrorContains) {
			t.Errorf("%s\n  refused with %s\n  contract expects %s",
				bad.Why, strings.Join(errs, "\n  "), bad.ExpectErrorContains)
		}
	}
}

// The cross-file half: this contract PLACES the evidence record and
// job-protocol.json DEFINES it, so the golden evidence example here is
// validated against the definition there. Bundle 1.2.0 shipped a document that
// satisfied this file's own schema and none of the real one.
func TestGoldenEvidenceValidatesAgainstTheContractThatDefinesIt(t *testing.T) {
	c, _, _ := loadRunState(t)
	evidenceSchema, evidenceDefs := jobProtocolEvidenceSchema(t)

	ref := c.References.Evidence
	if ref.SchemaID == "" || ref.File == "" || ref.Pointer == "" {
		t.Fatalf("the evidence reference is incomplete: %+v", ref)
	}
	if ref.File != jobProtocolFile {
		t.Errorf("the evidence record is referenced from %q, want %s", ref.File, jobProtocolFile)
	}
	if _, defined := c.Schemas["evidence"]; defined {
		t.Error("this contract defines an evidence schema of its own; one record, one definition")
	}

	raw, ok := c.Golden["evidence"]
	if !ok {
		t.Fatal("there is no golden evidence example to check across the seam")
	}
	if errs := schema.Validate(evidenceSchema, evidenceDefs, decodeDocument(t, raw)); len(errs) != 0 {
		t.Errorf("the golden evidence example is refused by %s, which defines the record:\n  %s",
			jobProtocolFile, strings.Join(errs, "\n  "))
	}

	// The filename and the record's own key name the same thing, so a citation
	// in a JobResult and a file on disk cannot drift apart.
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["key"]; !ok {
		t.Errorf("the golden evidence carries no key, and %s", ref.KeyIs)
	}
}

// jobProtocolEvidenceSchema is the definition of the evidence record, read
// from the contract that owns it.
func jobProtocolEvidenceSchema(t *testing.T) (*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	c, records, defs := loadJobProtocol(t)
	s, ok := records["evidence"]
	if !ok {
		t.Fatalf("%s no longer defines the evidence record", jobProtocolFile)
	}
	if c.Records["evidence"].SchemaID != "ticfac.evidence.v1" {
		t.Fatalf("the evidence record's schema_id moved to %q", c.Records["evidence"].SchemaID)
	}
	return s, defs
}

// The strict subset has no cross-file $ref, so provenance, phase, executor and
// role are COPIED into this contract for its local refs to resolve. A copy
// nothing compares is how the first divergence happened, so the copies are
// compared structurally against the originals.
func TestCopiedDefsAreStructurallyIdenticalToTheirOriginals(t *testing.T) {
	c, _, _ := loadRunState(t)
	jp, _, _ := loadJobProtocol(t)

	if !strings.Contains(c.Envelope.ProvenanceIs, "job-protocol.json") {
		t.Errorf("the envelope no longer says where provenance is defined: %q", c.Envelope.ProvenanceIs)
	}

	shared := 0
	for _, name := range sortedKeys(c.Defs) {
		original, ok := jp.Defs[name]
		if !ok {
			continue // a def this contract owns outright, e.g. tick_state
		}
		shared++
		if !sameJSON(t, c.Defs[name], original) {
			t.Errorf("$defs.%s differs between %s and %s — two spellings of one shape is exactly "+
				"what bundle 1.2.0 shipped:\n  run-state:     %s\n  job-protocol:  %s",
				name, runStateFile, jobProtocolFile, c.Defs[name], original)
		}
	}
	if shared < 4 {
		t.Errorf("only %d $defs are shared between the two contracts; provenance, phase, executor and "+
			"role are copied and must all be compared", shared)
	}
}

func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(left, right)
}

// The `.gitignore` fragment, asserted against THIS repository with git itself.
// ticfac is a ticfac target like any other, and "the fragment is defined" has
// to mean git applies it — a JSON file that merely mentions a pattern ignores
// nothing.
func TestTheGitignoreFragmentIsAppliedByGit(t *testing.T) {
	c, _, _ := loadRunState(t)

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	raw, err := readFile(filepath.Join(root, c.Gitignore.Target))
	if err != nil {
		t.Fatalf("read %s: %v", c.Gitignore.Target, err)
	}
	body := string(raw)
	for _, line := range c.Gitignore.Fragment {
		if !strings.Contains(body, line) {
			t.Errorf("%s does not carry the fragment line %q", c.Gitignore.Target, line)
		}
	}
	if !strings.Contains(body, c.Gitignore.BeginMarker) || !strings.Contains(body, c.Gitignore.EndMarker) {
		t.Errorf("%s does not carry the fragment's markers", c.Gitignore.Target)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	for _, path := range c.Gitignore.IgnoredExamples {
		if !gitIgnores(t, root, path) {
			t.Errorf("%s must be ignored and git tracks it", path)
		}
	}
	for _, path := range c.Gitignore.TrackedExamples {
		if gitIgnores(t, root, path) {
			t.Errorf("%s is the run's durable record and git ignores it — a run that leaves nothing behind", path)
		}
	}
}

func gitIgnores(t *testing.T, root, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", path)
	cmd.Dir = root
	err := cmd.Run()
	if err == nil {
		return true
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", path, err)
	return false
}

// ------------------------------------------------------------- the CAS ---

// The seven sequences, replayed against ticfac's own fake. Each step's outcome
// must be the contract's, and where a step declares `effect_permitted: false`
// the outcome must be a refusal — the loser of a dispatch race must not start
// a job.
func TestCASSequencesReplayAgainstTheFake(t *testing.T) {
	c, _, _ := loadRunState(t)

	if len(c.CAS.Sequences) != 7 {
		t.Errorf("the contract carries %d CAS sequences; bundle 3.0.0 carries 7", len(c.CAS.Sequences))
	}
	declared := map[string]map[string]bool{}
	for _, op := range c.CAS.Fake.Ops {
		outcomes := map[string]bool{}
		for _, o := range op.Outcomes {
			outcomes[o] = true
		}
		declared[op.Op] = outcomes
		if op.Does == "" {
			t.Errorf("cas op %q does not say what it does", op.Op)
		}
	}
	if len(c.CAS.Fake.Rules) == 0 {
		t.Fatal("the fake declares no rules; they are what a second implementation copies")
	}

	reached := map[string]map[string]bool{}
	for _, seq := range c.CAS.Sequences {
		seq := seq
		t.Run(seq.ID, func(t *testing.T) {
			if seq.Why == "" || len(seq.Steps) == 0 {
				t.Fatal("a sequence must say what it proves and have steps")
			}
			f := newCASFake()
			for i, step := range seq.Steps {
				outcomes, ok := declared[step.Op]
				if !ok {
					t.Fatalf("step %d uses op %q, which the fake does not declare", i, step.Op)
				}
				if !outcomes[step.Expect] {
					t.Fatalf("step %d expects %q, which op %q does not declare", i, step.Expect, step.Op)
				}
				got := f.run(t, step)
				if got != step.Expect {
					t.Fatalf("step %d (%s by %s): outcome %q, contract says %q",
						i, step.Op, step.Actor, got, step.Expect)
				}
				if reached[step.Op] == nil {
					reached[step.Op] = map[string]bool{}
				}
				reached[step.Op][got] = true

				// The load-bearing assertion: a refused compare-and-swap means
				// the effect must NOT happen.
				if step.EffectPermitted != nil {
					refused := strings.HasPrefix(got, "conflict_")
					if *step.EffectPermitted && refused {
						t.Fatalf("step %d: the contract permits the effect and the guard refused (%s)", i, got)
					}
					if !*step.EffectPermitted && !refused {
						t.Fatalf("step %d: the contract forbids the effect and the guard allowed it (%s)", i, got)
					}
				}
			}
			if f.originWrites != seq.Final.OriginWrites {
				t.Errorf("origin_writes = %d, contract says %d", f.originWrites, seq.Final.OriginWrites)
			}
			if len(f.origin) != len(seq.Final.Files) {
				t.Errorf("origin holds %d files, contract says %d: %v", len(f.origin), len(seq.Final.Files), sortedKeys(f.origin))
			}
			for path, want := range seq.Final.Files {
				got, ok := f.origin[path]
				if !ok {
					t.Errorf("%s is not on origin", path)
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s holds %v, contract says %v", path, got, want)
				}
			}
		})
	}

	for _, op := range sortedKeys(declared) {
		for _, outcome := range sortedKeys(declared[op]) {
			if !reached[op][outcome] {
				t.Errorf("op %q declares outcome %q, which no sequence reaches — a branch no test has seen",
					op, outcome)
			}
		}
	}
}

// The negative control. A CAS that has stopped guarding does not raise: it
// lets a second reconciler dispatch the same attempt, and the run pays for
// both jobs. So the guard is disabled and every sequence expecting a refusal
// must stop matching the contract.
func TestDisablingTheCASGuardBreaksEverySequenceThatExpectsARefusal(t *testing.T) {
	c, _, _ := loadRunState(t)

	refusing := 0
	for _, seq := range c.CAS.Sequences {
		expectsRefusal := false
		for _, step := range seq.Steps {
			if strings.HasPrefix(step.Expect, "conflict_") {
				expectsRefusal = true
			}
		}
		if !expectsRefusal {
			continue
		}
		refusing++

		f := newCASFake()
		f.guardOff = true
		diverged := false
		for _, step := range seq.Steps {
			if got := f.run(t, step); got != step.Expect {
				diverged = true
			}
		}
		if !diverged && f.originWrites == seq.Final.OriginWrites {
			t.Errorf("%s passes with the compare-and-swap disabled — the guard is not what it tests", seq.ID)
		}
	}
	if refusing < 4 {
		t.Errorf("only %d sequences expect a refusal; the races, the restart and the stale view all do", refusing)
	}
}
