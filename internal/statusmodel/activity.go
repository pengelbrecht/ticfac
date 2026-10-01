package statusmodel

import (
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The per-worker activity window (epic hn6, wave 2 — tick ltg): the sparkline
// a dashboard draws, the last action the worker took and when, and the
// nudges the run sent it — wv2's activity signals, which the dashboard's
// workers panel renders (hn6 rule 5). Everything is measured, nothing
// inferred: the events and the last action come from the worker's own
// session transcript (the same reader internal/exec/subprocess keeps for the
// stuck watch, read over its whole tail here), the nudges are the run's own
// typed feed lines, and the handle is the worker's own name wherever this
// machine spells it (tick zl1) — the executor's attempt record read through
// the Sources.Handle seam first, the durable marker's job handle under it —
// the word a person uses to find the worker on the machine. Where nothing
// measured a worker and nobody nudged it, the window is null: the honest
// not-measured, not a zeroed shape that renders as quiet-since-forever.

// The window the model states: ten one-minute buckets over the last ten
// minutes, oldest first — the shape a renderer draws one glyph per bucket.
const (
	activityWindowSeconds = 600
	activityBucketCount   = 10
)

// ActivityInput is one worker's measured activity window, as its reader
// answers it: the moments the worker was seen doing something, and the last
// action it took. The reader owns the window's width; the model buckets the
// moments against its own clock.
type ActivityInput struct {
	Events       []time.Time
	LastAction   string
	LastActionAt time.Time
}

// decorateWorkers fills the workers' activity windows and their executor
// handles, from the durable records and the transcript reader. The workers
// are already assembled (buildWorkers: their gaps, their silence, their
// last turn); this reads the two facts the panel adds on top of them.
//
// The nudges are counted for every worker whether or not its transcript
// could be read: a nudge is the run's own typed line about the (tick,
// attempt), not a measurement of the worker, and a nudged worker whose
// harness keeps nothing this machine can read still shows the nudge — with
// empty buckets, because nothing measured a window for it.
func decorateWorkers(src Sources, recs Records, m *Model) {
	if m.Workers == nil {
		return
	}
	// The dispatch markers by (tick, attempt): the attempt's own provenance
	// names the runner, and its job handle names the worker.
	attempts := map[string]runstate.Attempt{}
	for _, a := range recs.Attempts {
		attempts[fmt.Sprintf("%s#%d", a.TickID, a.Attempt)] = a
	}
	// The stuck nudges per (tick, attempt): counts of the feed's own typed
	// lines, never a parse of prose.
	nudges := map[string]int{}
	for _, e := range src.Feed {
		if e.Stage != reconcile.StageStuckNudged || e.TickID == nil || e.Attempt == nil {
			continue
		}
		nudges[fmt.Sprintf("%s#%d", *e.TickID, *e.Attempt)]++
	}

	for i := range *m.Workers {
		w := &(*m.Workers)[i]
		key := fmt.Sprintf("%s#%d", w.TickID, w.Attempt)
		var input *ActivityInput
		if src.Activity != nil && w.Worktree != "" {
			input = src.Activity(runnerOf(attempts[key]), w.Worktree)
		}
		n := nudges[key]
		switch {
		case input != nil:
			activity := &WorkerActivity{
				WindowSeconds: activityWindowSeconds,
				Buckets:       activityBuckets(input.Events, src.Now),
				Nudges:        n,
			}
			if input.LastAction != "" {
				action := input.LastAction
				activity.LastAction = &action
			}
			if !input.LastActionAt.IsZero() {
				at := input.LastActionAt.UTC().Format(time.RFC3339)
				activity.LastActionAt = &at
			}
			w.Activity = activity
		case n > 0:
			// Nothing measured a window, but the run's own nudge is a fact
			// the panel shows: empty buckets, never a guessed window.
			w.Activity = &WorkerActivity{WindowSeconds: activityWindowSeconds, Buckets: []int{}, Nudges: n}
		}
		if attempt, ok := attempts[key]; ok {
			w.Handle = handleOf(attempt.JobHandle)
		}
		// The worker's name, from wherever this machine spells it (zl1): the
		// injected reader first — the executor's own attempt record in the
		// dispatch's state directory, herdr's agent name or a supervisor's
		// pid, the LIVE word a person finds the worker by — over the durable
		// record's dispatch-time copy read above, which no executor writes
		// today (the marker is cut before the start) but which is honoured
		// wherever a record does spell it, and answers wherever the reader
		// says nothing.
		if src.Handle != nil {
			if named := src.Handle(w.TickID, w.Attempt); named != nil {
				w.Handle = named
			}
		}
	}
}

// activityBuckets buckets one worker's moments into the window the model
// states: activityBucketCount one-minute counts of the events in
// (now-window, now], oldest first. An event outside the window — older, or a
// stamp from a clock ahead of the model's own — counts nowhere.
func activityBuckets(events []time.Time, now time.Time) []int {
	buckets := make([]int, activityBucketCount)
	window := time.Duration(activityWindowSeconds) * time.Second
	for _, at := range events {
		age := now.Sub(at)
		if age < 0 || age >= window {
			continue
		}
		index := activityBucketCount - 1 - int(age/time.Minute)
		if index < 0 || index >= activityBucketCount {
			continue
		}
		buckets[index]++
	}
	return buckets
}

// runnerOf is the string the activity seam is addressed by for one worker:
// the attempt's own MODEL when its provenance names one — the model is what
// says which harness runs the worker ("claude opus" writes Claude Code's
// transcript layout, everything else pi's) — else the executor. The two
// spellings name the same fact from two record vintages, and
// TranscriptActivity's mapping accepts either.
func runnerOf(a runstate.Attempt) string {
	if a.Provenance.Model != nil && *a.Provenance.Model != "" {
		return *a.Provenance.Model
	}
	if a.Provenance.Executor != nil {
		return *a.Provenance.Executor
	}
	return ""
}

// handleOf reads the executor's own name for a worker off the attempt
// record's job handle map — a herdr agent name ("tick-v7z-a11"), a pane id,
// wherever the record spells one. The job handle is the job protocol's one
// open object: the names ride in the nested "handle" object when the record
// carries the executor's own handle (herdr spells them agent_name and
// pane_id), and the bare keys are honoured wherever a record carries them
// directly. Nil when nothing names the worker — never a guess from the job
// id, which is the run's identity for the attempt, not a word a person can
// find the worker on the machine by.
func handleOf(jobHandle map[string]any) *string {
	if jobHandle == nil {
		return nil
	}
	named := []map[string]any{}
	if inner, ok := jobHandle["handle"].(map[string]any); ok {
		named = append(named, inner)
	}
	named = append(named, jobHandle)
	for _, m := range named {
		for _, key := range []string{"agent", "agent_name", "pane", "pane_id", "name"} {
			if s, ok := m[key].(string); ok && s != "" {
				return &s
			}
		}
	}
	return nil
}

// TranscriptActivity is the production activity reader for a LOCAL run: it
// answers a worker's activity from its harness's own session transcript
// under home — every dated line's stamp, and the last tool call as one
// line. The reader owns nothing about the window: it answers moments, and
// the model buckets them against its own one clock (src.Now).
//
// The first parameter is the worker's runner, addressed by its model or its
// executor (see runnerOf): a string naming claude reads Claude Code's own
// layout, everything else reads pi's.
func TranscriptActivity(home string) func(executor, worktree string) *ActivityInput {
	return func(executor, worktree string) *ActivityInput {
		events, ok := subprocess.ReadTranscriptEvents(home, transcriptKind(executor), worktree)
		if !ok {
			return nil
		}
		return &ActivityInput{
			Events:       events.Events,
			LastAction:   events.LastToolCall,
			LastActionAt: events.LastToolAt,
		}
	}
}

// transcriptKind maps the executor-or-model string the Sources seam passes
// to the harness whose transcript layout is read: a string naming claude
// reads claude's, everything else reads pi's.
func transcriptKind(executorOrModel string) string {
	if strings.Contains(strings.ToLower(executorOrModel), "claude") {
		return "claude"
	}
	return "pi"
}
