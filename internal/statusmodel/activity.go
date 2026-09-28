package statusmodel

import "time"

// The per-worker activity window (epic hn6, wave 1 — tick r5i): the sparkline
// a dashboard draws, the last action the worker took and when, and the
// nudges the run sent it — wv2's activity signals, which the dashboard's
// workers panel renders (hn6 rule 5). Wave 1 DECLARES the shape and leaves
// it null: nothing measures a window yet, and decorateWorkers is the no-op
// the wave-2 activity tick fills. The production reader hangs on the same
// seam every other source hangs on — Sources.Activity, nil-safe — so the
// fill wires a reader here and nothing outside this package changes.

// ActivityInput is one worker's measured activity window, as its reader
// answers it: the moments the worker was seen doing something, and the last
// action it took. The reader owns the window's width; the model buckets.
type ActivityInput struct {
	Events       []time.Time
	LastAction   string
	LastActionAt time.Time
}

// decorateWorkers is the wave-1 no-op: the workers are already assembled
// (buildWorkers, their gaps, their silence, their last turn) and the
// activity window and the executor's handle stay null until the wave-2
// activity tick reads them. It exists so that fill has a home that is not
// build.go.
func decorateWorkers(src Sources, recs Records, m *Model) {
}

// TranscriptActivity is the production activity reader for a LOCAL run: it
// answers a worker's activity from the runner's own transcript under home.
// Wave 1 returns the stub — a reader that answers nothing, which the model
// states as null activity, the honest "not measured" rather than a guess.
// The signature is the wave-2 shape: the home the runner writes under and
// the (executor, worktree) the Sources seam passes.
func TranscriptActivity(home string) func(executor, worktree string) *ActivityInput {
	return func(executor, worktree string) *ActivityInput {
		return nil
	}
}
