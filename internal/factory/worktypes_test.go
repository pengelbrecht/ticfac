package factory

import (
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The work-type to model table (tick mrn, epic wne): the factory's price
// list. The vocabulary — what the work IS — lives in internal/runconfig and
// is the repository's side of the split; this table — what a work type COSTS
// on THIS deployment — is the factory's side, and lives in the factory's
// own deployment config (wrangler.toml [vars]) beside RUN_WORKER_MODEL and
// SWEEP_MAX_TIER, because the factory owns the gateway and a repository
// cannot name a model it does not serve.
//
// The two documents are deliberately separable: a classification records a
// WORK TYPE, never a model, so this table can be re-tuned without
// invalidating anything already recorded against it.

// The value shape the deployment variable carries, as a full table: one
// worktype=model pair per entry, comma-separated. This is today's committed
// mapping, and the test uses it as its working fixture.
const fullTableValue = "mechanical=cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash," +
	"translation=cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash," +
	"construction=cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash," +
	"diagnosis=cloudflare-workers-ai/@cf/zai-org/glm-5.3," +
	"design=cloudflare-workers-ai/@cf/zai-org/glm-5.3"

// A table that parses maps every work type it names, and Model reports
// nothing — no note, no error — for a type it mapped itself. The note is the
// report channel: a dispatch that fell back to the default without saying so
// is a routing decision nobody can audit.
func TestParseMapsEachDeclaredWorkType(t *testing.T) {
	table, err := ParseWorkTypeModels(fullTableValue)
	if err != nil {
		t.Fatalf("ParseWorkTypeModels: %v", err)
	}
	for _, pair := range [][2]string{
		{"mechanical", "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"},
		{"translation", "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"},
		{"construction", "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"},
		{"diagnosis", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
		{"design", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
	} {
		model, note, err := table.Model(runconfig.WorkType(pair[0]), "the-default")
		if err != nil {
			t.Fatalf("Model(%s): %v", pair[0], err)
		}
		if model != pair[1] {
			t.Errorf("Model(%s) = %q; want the table's own mapping %q", pair[0], model, pair[1])
		}
		if note != "" {
			t.Errorf("Model(%s) reported %q; a mapped work type is served as mapped, with nothing to report", pair[0], note)
		}
	}
}

// The closed enum, enforced at the table's edge: a key the vocabulary does
// not know is a config bug, refused with the value and the legal set in the
// message — never a mapping quietly dropped. "A work type that is not one of
// these is a bug, not a fallback."
func TestParseRefusesAnUnknownWorkType(t *testing.T) {
	_, err := ParseWorkTypeModels("mechanical=x,docs=y")
	if err == nil {
		t.Fatal("a table naming an unknown work type parsed; the closed enum must refuse it")
	}
	for _, want := range []string{"docs", "mechanical", "translation", "construction", "diagnosis", "design"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q; a refusal must teach the legal vocabulary", err.Error(), want)
		}
	}
}

// The structural rule, caught where it would be committed: a MODEL NAME where
// a work type belongs — a pair written backwards — is refused by the closed
// enum at the key. This is what makes the table re-tunable without touching
// recorded classifications: the two positions cannot hold each other's
// values, so swapping a deployment's lineup cannot be read as new vocabulary.
func TestParseRefusesAModelNameWhereAWorkTypeBelongs(t *testing.T) {
	_, err := ParseWorkTypeModels("cloudflare-workers-ai/@cf/zai-org/glm-5.3=mechanical")
	if err == nil {
		t.Fatal("a table key holding a model id parsed; the enum refuses it as a work type")
	}
	if !strings.Contains(err.Error(), "cloudflare-workers-ai/@cf/zai-org/glm-5.3") {
		t.Errorf("refusal %q does not echo the model id it refused", err.Error())
	}
}

// Two models for one work type is a config bug, not a precedence question:
// an operator who wrote both meant one thing, and the table has to say which
// rather than let the last line win.
func TestParseRefusesADuplicateWorkType(t *testing.T) {
	_, err := ParseWorkTypeModels("mechanical=one-model,mechanical=another-model")
	if err == nil {
		t.Fatal("a table mapping one work type twice parsed; a duplicate is refused, not last-wins")
	}
	if !strings.Contains(err.Error(), "mechanical") {
		t.Errorf("refusal %q does not name the duplicated work type", err.Error())
	}
}

// A mapping to nothing is a bug, not an un-mapping. Declining a work type is
// done by leaving it out of the table — which falls back and says so — never
// by writing "type=" and letting the parser invent absence.
func TestParseRefusesAnEmptyModel(t *testing.T) {
	if _, err := ParseWorkTypeModels("construction="); err == nil {
		t.Fatal("an empty model value parsed; declining a type is omission, never an empty pair")
	}
}

// A deployment that sets no table at all degrades, it does not fail: every
// work type falls back to the default and says so. This is the same
// direction as the unmapped-type rule — a dispatch must not die because a
// table nobody extended was missing.
func TestABlankVariableIsAnEmptyTable(t *testing.T) {
	for _, value := range []string{"", "   "} {
		table, err := ParseWorkTypeModels(value)
		if err != nil {
			t.Fatalf("ParseWorkTypeModels(%q): %v — an unset variable is no table, not a config error", value, err)
		}
		model, note, err := table.Model(runconfig.WorkConstruction, "the-default")
		if err != nil {
			t.Fatalf("Model over an empty table: %v", err)
		}
		if model != "the-default" || note == "" {
			t.Errorf("empty table: model = %q, note = %q; want the default AND a note that says so", model, note)
		}
	}
}

// The rule the epic states in one line: "A work type with no mapping falls
// back to the default AND SAYS SO. Failing a dispatch on a table someone
// forgot to extend is the wrong failure." The note is the SAYING SO — it
// names the work type that had no mapping and the default that was served,
// because a fallback a reader cannot see is a silent downgrade.
func TestAnUnmappedWorkTypeFallsBackAndSaysSo(t *testing.T) {
	table, err := ParseWorkTypeModels("mechanical=cheap-model")
	if err != nil {
		t.Fatalf("ParseWorkTypeModels: %v", err)
	}
	model, note, err := table.Model(runconfig.WorkDesign, "the-default")
	if err != nil {
		t.Fatalf("Model(design): %v", err)
	}
	if model != "the-default" {
		t.Errorf("unmapped design served %q; want the default %q", model, "the-default")
	}
	if !strings.Contains(note, "design") || !strings.Contains(note, "the-default") {
		t.Errorf("fallback note %q must name the unmapped work type and the default that was served", note)
	}
}

// An unknown work type asking the table for a model is a BUG, not a missing
// mapping: the enum is closed, so a value outside it never reached the table
// through any honest path. Refused with the value and the vocabulary — never
// the default, which would hide the defect behind a working dispatch.
func TestModelRefusesAnUnknownWorkTypeAsABug(t *testing.T) {
	table, err := ParseWorkTypeModels(fullTableValue)
	if err != nil {
		t.Fatalf("ParseWorkTypeModels: %v", err)
	}
	_, _, err = table.Model(runconfig.WorkType("hardening"), "the-default")
	if err == nil {
		t.Fatal("an unknown work type resolved; the closed enum makes this a bug the table must refuse, not a fallback")
	}
	if !strings.Contains(err.Error(), "hardening") {
		t.Errorf("refusal %q does not name the unknown work type", err.Error())
	}
	if !strings.Contains(err.Error(), string(runconfig.WorkDesign)) {
		t.Errorf("refusal %q does not teach the legal vocabulary", err.Error())
	}
}

// Falling back requires a default to fall back TO. A table with a hole in it
// and no default is not a degrade, it is a dispatch about to run with no
// model at all — refused, so the caller learns which work type was unmapped
// and can fix the table rather than read an empty string as a routing answer.
func TestFallingBackWithoutADefaultIsRefused(t *testing.T) {
	table, err := ParseWorkTypeModels("mechanical=cheap-model")
	if err != nil {
		t.Fatalf("ParseWorkTypeModels: %v", err)
	}
	_, _, err = table.Model(runconfig.WorkDesign, "")
	if err == nil {
		t.Fatal("an unmapped work type with no default resolved to an empty model; that is a bug, not a fallback")
	}
	if !strings.Contains(err.Error(), "design") {
		t.Errorf("refusal %q does not name the work type that had neither mapping nor default", err.Error())
	}
}

// The committed factory config — the acceptance criterion read against the
// real document, the way the gate's drift guards read .tick/runners.toml: the
// deployment this repository ships maps each of the closed enum's five work
// types to a model. The VALUES are this deployment's to change (and the test
// deliberately does not pin which model serves which type); what it pins is
// that the committed table is total over the vocabulary, so a type added to
// the enum without the factory being re-tuned is a visible hole in this
// test, not a silent fallback at dispatch time.
func TestTheCommittedFactoryConfigMapsEveryWorkType(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	table, err := ReadWorkTypeModels(root + "/cloudflare/wrangler.toml")
	if err != nil {
		t.Fatalf("read the committed factory config: %v", err)
	}
	for _, one := range runconfig.WorkTypeNames {
		model, note, err := table.Model(one, "should-not-be-needed")
		if err != nil {
			t.Fatalf("committed config: Model(%s): %v", one, err)
		}
		if model == "" {
			t.Errorf("committed config maps %s to an empty model", one)
		}
		if note != "" {
			t.Errorf("committed config leaves %s unmapped (%q) — the shipped table should be total over the vocabulary, so a fallback is a hole a config change made on purpose", one, note)
		}
	}
}

// The variable the table rides in must be named the same in both halves of
// the handshake: the wrangler.toml [vars] the operator edits, and the Go
// reader this repository tests. A rename on one side is discovered here
// rather than as a table that silently went missing at dispatch.
func TestTheCommittedFactoryConfigNamesTheVariableTheReaderReads(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	tomlBytes, err := os.ReadFile(root + "/cloudflare/wrangler.toml")
	if err != nil {
		t.Fatalf("read the committed factory config: %v", err)
	}
	if !strings.Contains(string(tomlBytes), VarName+" =") {
		t.Errorf("the committed wrangler.toml does not set %s; the Go reader and the deployment document have drifted", VarName)
	}
}
