package runconfig

import (
	"strconv"
	"strings"
)

// WorkType is one of the five kinds of work a tick's implementation can be
// (tick mrn, epic wne), declared CLOSED the way the tier vocabulary is: the
// names below are the whole enum, a value outside them is a bug rather than a
// fallback, and every reader of the vocabulary refuses such a value instead
// of quietly defaulting it.
//
// THE AXIS is inference under uncertainty — how much of the work is deciding
// what to do rather than doing it. Size and risk were considered and rejected
// when the vocabulary was proposed: a large mechanical edit is still
// mechanical, and a risky one-line change is still one line.
//
//	mechanical    the change is stated, not decided. Done is knowable by
//	              construction.
//	translation   a known pattern applied to new material. Done is knowable by
//	              comparison.
//	construction  build to a stated spec; decisions are local. Done is the
//	              acceptance.
//	diagnosis     cause unknown. Form and discard hypotheses against evidence.
//	              Done is knowable only once the cause is found.
//	design        the shape itself is in question, and the tick's own framing
//	              may be wrong. Done is knowable only by argument.
//
// The order of WorkTypeNames is that axis, least inference first, and is
// load-bearing: adding a third model later moves ONE boundary instead of
// re-classifying anything.
//
// WHY THIS IS NOT the tracker's `type` field. `task`/`bug`/`epic` describes
// the TICKET; this describes the WORK. A bug can be mechanical (a typo with a
// known fix) or diagnosis (an intermittent failure with no repro), and those
// two want different models.
//
// WHY THIS IS NOT a model list. The vocabulary says what the work IS; which
// model a work type is worth is a separate table — the factory's, in
// internal/factory — so that changing a deployment's model lineup cannot
// invalidate a classification recorded against these names. Nothing may write
// a model name where a work type belongs; [WorkType.Valid] refusing a model
// id is that rule's first line.
type WorkType string

// The work types, on the axis of how much must be worked out rather than
// carried out.
const (
	WorkMechanical   WorkType = "mechanical"
	WorkTranslation  WorkType = "translation"
	WorkConstruction WorkType = "construction"
	WorkDiagnosis    WorkType = "diagnosis"
	WorkDesign       WorkType = "design"
)

// WorkTypeNames is the work-type vocabulary in axis order, the way TierNames
// is the tier vocabulary in capability order.
var WorkTypeNames = []WorkType{WorkMechanical, WorkTranslation, WorkConstruction, WorkDiagnosis, WorkDesign}

// Valid reports whether w is one of [WorkTypeNames]. The vocabulary is
// closed: a caller holding a value that fails this has a bug on its hands —
// an invented work type, or a model name written where a work type belongs —
// and must refuse it, never default it.
func (w WorkType) Valid() bool {
	return IsKnownWorkType(string(w))
}

// IsKnownWorkType reports whether name is one of the closed work-type
// vocabulary. It is the work-type twin of the tier vocabulary's isKnownTier.
func IsKnownWorkType(name string) bool {
	for _, one := range WorkTypeNames {
		if string(one) == name {
			return true
		}
	}
	return false
}

// WorkTypeList renders the vocabulary for a message: quoted for a refusal
// that echoes a rejected value back, bare for prose. Both spellings come from
// one place, the way [SubstrateList] does for substrates, so two refusals
// can never enumerate different work types.
func WorkTypeList(quote bool) string {
	parts := make([]string, 0, len(WorkTypeNames))
	for _, one := range WorkTypeNames {
		if quote {
			parts = append(parts, strconv.Quote(string(one)))
		} else {
			parts = append(parts, string(one))
		}
	}
	return strings.Join(parts, ", ")
}
