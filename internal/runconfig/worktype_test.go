package runconfig

import "testing"

// The closed work-type enum (epic wne, 2026-09-22): the KIND OF WORK a tick
// is, which Jev classifies and the factory's table prices. The vocabulary is
// closed the way the tier vocabulary is — anything outside it is a defect,
// not a fallback — and these tests are the refusal that keeps it that way.

func TestWorkTypeVocabularyIsClosedAndOrdered(t *testing.T) {
	want := []WorkType{WorkMechanical, WorkTranslation, WorkConstruction, WorkDiagnosis, WorkDesign}
	if len(WorkTypeNames) != len(want) {
		t.Fatalf("the work-type enum has %d values, want %d (%v)", len(WorkTypeNames), len(want), WorkTypeNames)
	}
	for i, one := range want {
		if WorkTypeNames[i] != one {
			t.Fatalf("WorkTypeNames[%d] = %q, want %q: the order is the axis of inference under uncertainty, cheapest first", i, WorkTypeNames[i], one)
		}
		if !IsKnownWorkType(string(one)) {
			t.Errorf("IsKnownWorkType(%q) = false: every enum value must be known", one)
		}
	}
}

func TestIsKnownWorkTypeRefusesEverythingElse(t *testing.T) {
	// The enum has no right answer for "read a diff and judge it" — that is
	// why role ticks are never classified — and no value may be invented.
	for _, name := range []string{"", "polish", "review", "Design", "DESIGN", "construction ", "0"} {
		if IsKnownWorkType(name) {
			t.Errorf("IsKnownWorkType(%q) = true: the work-type enum is closed, and this is not on it", name)
		}
	}
}
