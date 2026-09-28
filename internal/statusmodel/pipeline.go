package statusmodel

// The per-tick pipeline cell (epic hn6, wave 1 — tick r5i): every tick's
// stages, in the role's own order, each with its own state — the thing a
// dashboard fills left to right (GitHub Actions / Buildkite's shape). Wave 1
// DECLARES the cell: the stage list is the role's and every stage reads
// pending, because which stage a tick is IN is a derivation from the durable
// records that the wave-2 pipeline tick owns. This file is where that tick
// fills it; nothing outside it decides a stage's state.

// decorateTicks lays the pipeline cell on every tick, at its wave-1 empty
// values: the role's own stage list with every stage pending, the findings
// list empty, and the parent, the duration and the tries' tier/reason/
// next_step all null. A renderer must be able to lay the whole tick table
// out from what this leaves behind — that is the acceptance: the shape
// exists and is honest, and the wave-2 ticks fill the values in parallel
// without touching build.go.
func decorateTicks(src Sources, recs Records, m *Model) {
	for wi := range deref(m.Waves) {
		wave := &(*m.Waves)[wi]
		for ti := range wave.Ticks {
			tick := &wave.Ticks[ti]
			tick.Pipeline = pendingPipelineOf(tick.Role)
			tick.Findings = []TickFinding{}
			tick.ParentTickID = nil
			tick.DurationSeconds = nil
			for i := range tick.Tries {
				tick.Tries[i].Tier = nil
				tick.Tries[i].Reason = nil
				tick.Tries[i].NextStep = nil
			}
		}
	}
}

// pendingPipelineOf builds one role's cell with every stage pending. A tick
// with no role is an implement tick; the review and close-out roles carry
// their own stages.
func pendingPipelineOf(role string) []PipelineStage {
	stages := PipelineImplement
	switch role {
	case "review":
		stages = PipelineReview
	case "closeout":
		stages = PipelineCloseout
	}
	cell := make([]PipelineStage, 0, len(stages))
	for _, stage := range stages {
		cell = append(cell, PipelineStage{Stage: stage, State: StageStatePending})
	}
	return cell
}
