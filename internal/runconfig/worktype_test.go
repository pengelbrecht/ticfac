package runconfig

import (
	"strings"
	"testing"
)

// The work-type vocabulary (tick mrn, epic wne): five kinds of work on ONE
// axis — how much of the work is deciding what to do rather than doing it —
// declared CLOSED the way the tier vocabulary already is. A work type that is
// not one of these five is a bug, not a fallback, and every reader of the
// vocabulary has to refuse it rather than quietly default it.
//
// The enum is deliberately NOT a model list. It says what the work IS; which
// model a work type is worth is the factory's table (internal/factory), a
// separate document on purpose, so changing a deployment's model lineup cannot
// invalidate a classification recorded against this vocabulary.

// The five names from the epic's parent description, and nothing else, are
// the vocabulary. The test spells them out rather than deriving them from the
// slice, so a value added to WorkTypeNames without the epic saying so fails
// here rather than passing as a silent extension.
func TestWorkTypeVocabularyIsClosed(t *testing.T) {
	want := []WorkType{
		WorkMechanical,
		WorkTranslation,
		WorkConstruction,
		WorkDiagnosis,
		WorkDesign,
	}
	if len(WorkTypeNames) != len(want) {
		t.Fatalf("WorkTypeNames has %d entries; the closed enum is %d (a work type is added by the epic, never by a reader): %v", len(WorkTypeNames), len(want), WorkTypeNames)
	}
	for i, name := range want {
		if WorkTypeNames[i] != name {
			t.Errorf("WorkTypeNames[%d] = %q; want %q", i, WorkTypeNames[i], name)
		}
		if !name.Valid() {
			t.Errorf("%q must be a valid work type", name)
		}
		if !IsKnownWorkType(string(name)) {
			t.Errorf("IsKnownWorkType(%q) = false; the vocabulary's own names must be known", name)
		}
	}
}

// Refused values. The list is deliberately not exhaustive of every string —
// it is exhaustive of the FAILURES the epic names: an invented type, a
// tier's name (the two vocabularies are neighbors and must not blur), a
// spelling with a separator, an empty string, and the case variants a text
// answerer loves. None of them may fall back to anything: refusal, loudly.
func TestWorkTypeRefusesAnUnknownValue(t *testing.T) {
	for _, name := range []string{
		"docs",        // invented on the spot
		"review",      // a role, not a kind of work
		"economy",     // a tier's name — the other vocabulary
		"medium-high", // what a text model answers when asked for a tier
		"Mechanical",  // capitalised
		"mechanical ", // trailing space
		"",            // the absent answer
	} {
		if WorkType(name).Valid() {
			t.Errorf("WorkType(%q).Valid() = true; the enum is closed and this is not one of its names", name)
		}
		if IsKnownWorkType(name) {
			t.Errorf("IsKnownWorkType(%q) = true; a closed enum refuses this", name)
		}
	}
}

// A MODEL NAME is not a work type. This is the vocabulary's half of the
// epic's structural rule — "nothing may write a model name where a work type
// belongs" — and the factory table's key validation leans on it: a table
// whose key is a model id (a swapped "model=worktype" entry) is refused by
// exactly this refusal. If this test ever fails, the separation that lets a
// model lineup change without invalidating recorded classifications is gone.
func TestAModelNameIsNotAWorkType(t *testing.T) {
	for _, model := range []string{
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash",
		"workers-ai/@cf/deepseek-ai/deepseek-v4-pro-0813",
		"opus",
	} {
		if WorkType(model).Valid() {
			t.Errorf("WorkType(%q).Valid() = true; a model id is never a kind of work", model)
		}
	}
}

// The axis order is load-bearing: "ordered on purpose. Adding a third model
// later moves ONE boundary instead of re-classifying anything". Consumers may
// index WorkTypeNames to say "everything at or above this point", so the
// order must be the epic's axis — inference under uncertainty, least first —
// and not alphabetical, not insertion-accidental.
func TestWorkTypeNamesAreOrderedOnTheInferenceAxis(t *testing.T) {
	got := make([]string, 0, len(WorkTypeNames))
	for _, name := range WorkTypeNames {
		got = append(got, string(name))
	}
	want := "mechanical, translation, construction, diagnosis, design"
	if strings.Join(got, ", ") != want {
		t.Errorf("WorkTypeNames = %q; want the epic's axis order %q", strings.Join(got, ", "), want)
	}
}

// Both spellings of the list come from one function, the way SubstrateList
// does it, so no two refusals can enumerate different vocabularies. A value
// added to the enum without adding it to the list a refusal teaches from is
// a refusal a config author cannot act on.
func TestWorkTypeListRendersBothSpellings(t *testing.T) {
	if bare, quoted := WorkTypeList(false), WorkTypeList(true); !strings.HasPrefix(bare, "mechanical") || !strings.HasPrefix(quoted, `"mechanical"`) {
		t.Errorf("WorkTypeList: bare = %q, quoted = %q; the two spellings must both name the vocabulary in order", bare, quoted)
	}
	for _, one := range WorkTypeNames {
		if !strings.Contains(WorkTypeList(false), string(one)) {
			t.Errorf("WorkTypeList(false) omits %q", one)
		}
		if !strings.Contains(WorkTypeList(true), `"`+string(one)+`"`) {
			t.Errorf("WorkTypeList(true) omits %q quoted", one)
		}
	}
}
