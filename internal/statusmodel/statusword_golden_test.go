package statusmodel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The status-word half of the golden discipline (epic ymf, tick lck): the
// goldens are rendering fixtures, not Build outputs, so what keeps them
// saying what the derivation produces is the rule read back over them — the
// same shape TestEveryGoldenAgreesWithThePipelineDerination holds the cells
// to, for the fields this tick added. Each rule is faithful to the
// production code and reads only what the golden document itself states —
// the rendered status words, the cells behind them, the try history, the
// liveness answer, the workers panel and the recent tail:
//
//   - the lifecycle's track is phaseTrackOf's own fold of the phases, and
//     here is its marker — demanded of every golden, field for field;
//   - a tick's status is the word its cell's live stage makes the derivation
//     say, with the three cases the document cannot see skipped rather than
//     guessed at: a hold (the feed is not in the document), a blocker the
//     tracker named (the graph is not in it), and the exact ending a
//     stopped run's override words (the prefix is demanded, the phrase is
//     the records' to state);
//   - a finished tick's exception is null, and the note's attempt and
//     escalation components are demanded and refused exactly where the try
//     history states them;
//   - the groups are the words bucketed by the mapping the contract states,
//     including the failed-tick rule that reads the run's own ending off
//     the liveness state and the recent tail — the same view of the feed
//     the document carries.
func TestEveryGoldenAgreesWithTheStatusWords(t *testing.T) {
	t.Parallel()
	_, _, goldens := bundleFixture(t)

	for name, raw := range goldens {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model Model
			if err := json.Unmarshal(raw, &model); err != nil {
				t.Fatalf("golden %s does not decode into the Go Model: %v", name, err)
			}

			// The track: demanded field for field of every golden.
			track, here := phaseTrackOf(model.Lifecycle.Phases)
			if !reflect.DeepEqual(track, model.Lifecycle.Track) {
				t.Errorf("the track is %+v, want the phases' own fold %+v", model.Lifecycle.Track, track)
			}
			if model.Lifecycle.Here != here {
				t.Errorf("the marker is at %d, want %d", model.Lifecycle.Here, here)
			}

			// The not-going fact the failed-tick group rule reads, from the
			// document's own durable words.
			notGoing := !model.Liveness.Alive && !goldenEndingIs(model, "completed", "cancelled")

			standing := map[string]bool{}
			idleOf := map[string]int64{}
			for _, worker := range derefWorkers(model.Workers) {
				key := tryKey(worker.TickID, worker.Attempt)
				standing[key] = true
				idle := int64(0)
				if worker.BranchIdleSeconds != nil {
					idle = *worker.BranchIdleSeconds
				}
				if worker.WorktreeIdleSeconds != nil && *worker.WorktreeIdleSeconds > idle {
					idle = *worker.WorktreeIdleSeconds
				}
				idleOf[key] = idle
			}

			statuses := map[string]string{}
			for _, wave := range deref(model.Waves) {
				for _, tick := range wave.Ticks {
					statuses[tick.TickID] = tick.Status
					goldenStatusWord(t, name, tick, model.Liveness.Alive, standing)
					goldenException(t, name, tick, idleOf[tryKey(tick.TickID, intValue(tick.Attempt))])
				}
			}

			// The groups: the words bucketed by the contract's mapping.
			if model.Waves == nil {
				if model.Groups != nil {
					t.Errorf("a golden with no waves carries groups %+v, want null", model.Groups)
				}
				return
			}
			if model.Groups == nil {
				t.Fatal("the golden carries waves but no groups")
			}
			want := &TickGroups{Now: []string{}, Done: []string{}, UpNext: []string{}, Held: []string{}}
			for _, wave := range deref(model.Waves) {
				for _, tick := range wave.Ticks {
					status := statuses[tick.TickID]
					switch {
					case strings.HasPrefix(status, WordHeldPrefix):
						want.Held = append(want.Held, tick.TickID)
					case status == WordFailed || strings.HasPrefix(status, WordFailedPrefix):
						if notGoing {
							want.Held = append(want.Held, tick.TickID)
						} else {
							want.Now = append(want.Now, tick.TickID)
						}
					case status == WordMerged || status == WordDone:
						want.Done = append(want.Done, tick.TickID)
					case status == WordUpNext || strings.HasPrefix(status, WordWaitingPrefix):
						want.UpNext = append(want.UpNext, tick.TickID)
					default:
						want.Now = append(want.Now, tick.TickID)
					}
				}
			}
			if !reflect.DeepEqual(model.Groups, want) {
				t.Errorf("the groups are %+v, want the words' own bucketing %+v", *model.Groups, *want)
			}
		})
	}
}

// goldenEndingIs answers whether the document's own durable words state the
// named ending: the liveness state when it names one, else the terminal line
// the recent tail carries — the same view of the feed the document holds.
func goldenEndingIs(model Model, endings ...string) bool {
	for _, ending := range endings {
		switch model.Liveness.State {
		case ending:
			return true
		}
		for i := len(model.Recent) - 1; i >= 0; i-- {
			event := model.Recent[i]
			if event.Stage != "run_finished" && event.Stage != "run_died" {
				continue
			}
			if classifyEnding(event.Stage, event.Detail) == ending {
				return true
			}
			break
		}
	}
	return false
}

// goldenStatusWord holds one golden tick's status to the word its cell makes
// the derivation say. The three cases the document cannot see — a hold, a
// named blocker, the exact ending of a stopped run — are skipped or checked
// by prefix, never guessed at.
func goldenStatusWord(t *testing.T, golden string, tick Tick, alive bool, standing map[string]bool) {
	t.Helper()
	if strings.HasPrefix(tick.Status, WordHeldPrefix) {
		// A hold is the feed's fact; the document carries none.
		return
	}
	stage, state := liveStageOf(tick.Pipeline)
	want := ""
	switch {
	case stage == "":
		// Every stage done: the cell's last stop names the word.
		if len(tick.Pipeline) > 0 && tick.Pipeline[len(tick.Pipeline)-1].Stage == StageMerged {
			want = WordMerged
		} else {
			want = WordDone
		}
	case state == StageStateFailed:
		switch stage {
		case StageCI:
			want = WordFailedPrefix + "CI is red on the epic PR"
		default:
			current := 0
			if tick.Attempt != nil {
				current = *tick.Attempt
			}
			reason := ""
			for i := range tick.Tries {
				if tick.Tries[i].Attempt == current && tick.Tries[i].Reason != nil {
					reason = *tick.Tries[i].Reason
				}
			}
			reason = strings.TrimPrefix(reason, "gate_failed: ")
			reason = strings.TrimPrefix(reason, "rejected: ")
			if reason != "" {
				want = WordFailedPrefix + reason
			} else {
				want = WordFailed
			}
		}
	case stage == StageClaim:
		// Up next, or the blocked word a graph edge the document does not
		// carry names — either is the derivation's answer here.
		if tick.Status != WordUpNext && !strings.HasPrefix(tick.Status, WordWaitingPrefix) {
			t.Errorf("golden %s: tick %s's unclaimed status is %q, want %q or a %q word",
				golden, tick.TickID, tick.Status, WordUpNext, WordWaitingPrefix)
			return
		}
		want = tick.Status
	case stage == StageWork || stage == StageReview:
		if state == StageStatePending {
			want = WordClaimed
		} else {
			want = activeWorkWord(tick.Role, stage)
		}
	case stage == StageGate:
		want = WordTesting
	case stage == StageCI:
		want = WordWaitingForCI
	default:
		want = WordMerging
	}
	// The stopped-run override: a working word is only true while something
	// can still work. The phrase is the records'; the prefix is the rule's.
	if want != tick.Status && isActiveWord(want) {
		current := 0
		if tick.Attempt != nil {
			current = *tick.Attempt
		}
		if !alive && !standing[tryKey(tick.TickID, current)] {
			if !strings.HasPrefix(tick.Status, WordWaitingPrefix) {
				t.Errorf("golden %s: tick %s's status is %q, want a %q word: nothing can move it",
					golden, tick.TickID, tick.Status, WordWaitingPrefix)
			}
			return
		}
	}
	if tick.Status != want {
		t.Errorf("golden %s: tick %s's status is %q, want %q", golden, tick.TickID, tick.Status, want)
	}
}

// goldenException holds one golden tick's exception note to what the try
// history and the workers panel state about it. idle is the census's own gap
// for the tick's current attempt in seconds, -1 when it carries none.
func goldenException(t *testing.T, golden string, tick Tick, idle int64) {
	t.Helper()
	finished := tick.State == tickClosed || tick.State == tickIntegrated
	if finished {
		if tick.Exception != nil {
			t.Errorf("golden %s: finished tick %s carries exception %q, want null: a done row stays calm",
				golden, tick.TickID, *tick.Exception)
		}
		return
	}
	note := ""
	if tick.Exception != nil {
		note = *tick.Exception
	}
	// attempt N: exactly when the current work is not the first try.
	multi := tick.Try != nil && *tick.Try > 1
	if multi != strings.Contains(note, ExceptionAttempt+" ") {
		t.Errorf("golden %s: tick %s's exception is %q, want the attempt component %t (try %v)",
			golden, tick.TickID, note, multi, tick.Try)
	}
	// model escalated: demanded when the tiers differ, refused when they
	// agree — the models a document cannot compare are never guessed at.
	escalated := false
	comparable := false
	if len(tick.Tries) > 0 && tick.Tries[0].Tier != nil && tick.Tier != nil {
		comparable = true
		escalated = *tick.Tries[0].Tier != *tick.Tier
	}
	if comparable && escalated != strings.Contains(note, ExceptionEscalate) {
		t.Errorf("golden %s: tick %s's exception is %q, want the escalation component %t (tiers %v → %v)",
			golden, tick.TickID, note, escalated, tick.Tries[0].Tier, tick.Tier)
	}
	// stalled: demanded and refused on the census's own measurement.
	if idle >= 0 {
		status := tick.Status
		working := status == WordWritingCode || status == WordReviewing || status == WordClosingOut
		stalled := working && idle >= int64(stallThreshold.Seconds())
		if stalled != strings.Contains(note, ExceptionStalled+" ") {
			t.Errorf("golden %s: tick %s's exception is %q, want the stalled component %t (idle %ds)",
				golden, tick.TickID, note, stalled, idle)
		}
	}
}

// intValue is a nullable integer's value, 0 when nil.
func intValue(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}
