package cli

// The activity line's two halves (tick 93n): the same one-line excerpt with
// its age the workers panel draws, read per worker from its own conversation
// stream — a CLOUD worker's from the factory's watch socket, a LOCAL
// durable worker's from the watch door its runner listens on (the same door
// `ticfac watch <run> <tick>` opens; a local run's pi-durable worker keeps
// no session transcript file its worktree could name, so the transcript
// reader comes up empty and this answers in its place — a claude worker's
// transcript answers first and the door is never opened). Both streams are
// folded by the same internal/workerview.Model the live view folds them
// into. A status read is not a watch: it opens the stream just long enough
// for the FIRST events frame (pi-durable's whole snapshot, so the newest
// tool call is already in it) and closes, never holding a worker's stream
// open across a poll.

import (
	"context"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// activitySnapshotTimeout bounds one worker's snapshot read: long enough for
// the factory's first frame to land over an ordinary connection, short
// enough that a worker whose stream answers nothing does not stall a status
// read behind it.
const activitySnapshotTimeout = 3 * time.Second

// watchAttacher is the one method a worker's snapshot read needs: a fake
// hands it recorded frames, production hands it a local *localWorker (its
// door) or a *cloudWorker (its factory socket).
type watchAttacher interface {
	attach(ctx context.Context) (<-chan workerview.Frame, error)
}

// workerActivity is one worker's activity, in the shape
// internal/statusmodel.Sources.RemoteActivity answers, for EITHER host: a
// cloud run's worker through the factory's watch socket, a local run's
// through its own door. nil when the worker cannot be reached or has said
// nothing yet, the honest not-measured.
func workerActivity(ctx context.Context, client *cloudClient, runID, tickID string, attempt int) *statusmodel.ActivityInput {
	if attempt <= 0 {
		return nil
	}
	if client != nil {
		return cloudWorkerActivity(ctx, client, runID, tickID, attempt)
	}
	return localWorkerActivity(ctx, runID, tickID, attempt)
}

// cloudWorkerActivity is one CLOUD worker's activity: its factory watch
// socket, opened just long enough for the first events frame and closed.
func cloudWorkerActivity(ctx context.Context, client *cloudClient, runID, tickID string, attempt int) *statusmodel.ActivityInput {
	if client == nil {
		return nil
	}
	w := &cloudWorker{client: client, runID: runID, tickID: tickID, attempt: attempt}
	return activityFromSnapshot(workerSnapshot(ctx, w, activitySnapshotTimeout))
}

// localWorkerActivity is one LOCAL worker's activity: the watch door its
// runner listens on, found under the same state roots the handle and runner
// readers walk. A worker whose door is not listening right now — between one
// runner process and its next — answers nil, the honest not-measured.
func localWorkerActivity(ctx context.Context, runID, tickID string, attempt int) *statusmodel.ActivityInput {
	for _, root := range statusmodel.ExecStateRoots() {
		door, err := subprocess.FindWorkerDoor(root, runID, tickID, attempt)
		if err != nil {
			continue
		}
		input := activityFromSnapshot(workerSnapshot(ctx, &localWorker{runID: runID, door: door}, activitySnapshotTimeout))
		if input != nil {
			return input
		}
	}
	return nil
}

// workerSnapshot opens a worker's watch stream, folds frames into a fresh
// model until the first events frame lands (the whole conversation so far)
// or timeout elapses, then returns whatever the model holds — closing the
// stream either way, through the context the attacher's own attach keys its
// connection's life to. nil only when the stream could not be opened at all.
func workerSnapshot(ctx context.Context, src watchAttacher, timeout time.Duration) *workerview.Model {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	frames, err := src.attach(ctx)
	if err != nil {
		return nil
	}
	model := workerview.New()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return model
			}
			if err := model.Apply(f, time.Now()); err != nil {
				continue
			}
			if f.Type == workerview.FrameEvents {
				return model
			}
		case <-ctx.Done():
			return model
		}
	}
}

// activityFromSnapshot reads a folded model's heartbeat into the shape the
// status model wants: every settled item's own stamp for the sparkline (the
// snapshot carries the whole conversation, so this is the real history, not
// a guess), and the worker's newest act (tick 93n) — its last tool call, or
// its latest assistant sentence when the newest act was speaking, whichever
// the conversation stamps later — REDACTED and bounded through the same
// pattern set the local transcript reader redacts with, because this line is
// rendered on the dashboard and shipped off-host with the phone snapshot
// exactly like the local one is.
func activityFromSnapshot(m *workerview.Model) *statusmodel.ActivityInput {
	if m == nil {
		return nil
	}
	h := m.Heartbeat
	lastTextAt, lastText := lastSentenceIn(m.Items)
	if h.LastTool == nil && lastText == "" && h.LastActivity.IsZero() {
		return nil
	}
	input := &statusmodel.ActivityInput{}
	for _, it := range m.Items {
		if !it.At.IsZero() {
			input.Events = append(input.Events, it.At)
		}
	}
	if h.LastTool != nil && (lastText == "" || !h.LastTool.At.Before(lastTextAt)) {
		action := h.LastTool.Tool
		if h.LastTool.Args != "" {
			action += ": " + h.LastTool.Args
		}
		input.LastAction = subprocess.BoundLine(subprocess.RedactCredentials(action))
		input.LastActionAt = h.LastTool.At
	} else if lastText != "" {
		input.LastAction = lastText
		input.LastActionAt = lastTextAt
	}
	return input
}

// lastSentenceIn answers the newest assistant answer one folded conversation
// holds: the model's own words, flattened to one line, REDACTED and bounded
// through the same pass the tool call goes through. Zero time and "" when
// the conversation holds no dated answer at all.
func lastSentenceIn(items []workerview.Item) (time.Time, string) {
	var at time.Time
	text := ""
	for i := range items {
		it := &items[i]
		if it.Kind != workerview.KindText || it.At.IsZero() || it.Text == "" {
			continue
		}
		if at.IsZero() || it.At.After(at) {
			at, text = it.At, it.Text
		}
	}
	if text == "" {
		return time.Time{}, ""
	}
	return at, subprocess.BoundLine(subprocess.RedactCredentials(text))
}
