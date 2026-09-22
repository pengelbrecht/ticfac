package runconfig

// WorkType is the KIND OF WORK a tick is, on the closed five-value enum the
// wne epic declared (operator, 2026-09-22) — deliberately not a model and not
// a tier. A classifier judges what the work IS; a table decides what that is
// worth, so changing model policy never invalidates a recorded classification.
//
// The axis is INFERENCE UNDER UNCERTAINTY: how much of the work is deciding
// what to do rather than doing it. Size and risk were considered and
// rejected — a large mechanical edit is still mechanical, and a risky
// one-line change is still one line. The order below is the axis, cheapest
// first, and it is load-bearing: adding a third model later moves ONE
// boundary instead of re-classifying anything, and the recorded
// classifications stay valid.
//
// This vocabulary is shared by the epic's ticks: the classifier (internal/jev)
// asks it, the factory's work-type-to-model table prices it, and routing
// spends probability mass over it. A work type that is not one of these is a
// bug, not a fallback — the enum is closed the way the tier vocabulary is
// ([IsKnownWorkType] refuses anything else).
type WorkType string

// The work types, ordered on the axis above.
const (
	// WorkMechanical: the change is stated, not decided. Done is knowable by
	// construction.
	WorkMechanical WorkType = "mechanical"
	// WorkTranslation: a known pattern applied to new material. Done is
	// knowable by comparison.
	WorkTranslation WorkType = "translation"
	// WorkConstruction: build to a stated spec; decisions are local. Done is
	// the acceptance.
	WorkConstruction WorkType = "construction"
	// WorkDiagnosis: cause unknown. Form and discard hypotheses against
	// evidence. Done is knowable only once the cause is found.
	WorkDiagnosis WorkType = "diagnosis"
	// WorkDesign: the shape itself is in question, and the tick's own framing
	// may be wrong. Done is knowable only by argument.
	WorkDesign WorkType = "design"
)

// WorkTypeNames is the work-type vocabulary in enum order. This slice is the
// one authority for the closed set; nothing may hand-name a work type the
// slice does not carry.
var WorkTypeNames = []WorkType{WorkMechanical, WorkTranslation, WorkConstruction, WorkDiagnosis, WorkDesign}

// IsKnownWorkType reports whether name is one of the work types on the enum.
// The refusal is the point: a probability on an unknown work type is a
// protocol change to be reported, not a judgement call to be rounded off.
func IsKnownWorkType(name string) bool {
	for _, one := range WorkTypeNames {
		if string(one) == name {
			return true
		}
	}
	return false
}
