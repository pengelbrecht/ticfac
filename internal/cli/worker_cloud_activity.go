package cli

// The dashboard's cloud half of the activity line (tick 93n): the same
// one-line excerpt with its age the workers panel already drew for a local
// worker (internal/statusmodel/activity.go), read here from the factory's
// own conversation stream instead of a worktree's transcript file. The
// stream is the one `ticfac watch <run> <tick>` already opens to draw its
// live view — the WorkerAgent's watch socket, cloudWorker.attach — folded by
// the same internal/workerview.Model that view folds it into. A status read
// is not a watch: it opens the socket just long enough for the FIRST events
// frame (pi-durable's whole snapshot, so the newest tool call is already in
// it) and closes, never holding a worker's socket open across a poll.

import (
	"context"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// cloudActivityTimeout bounds one worker's snapshot read: long enough for
// the factory's first frame to land over an ordinary connection, short
// enough that a worker whose socket answers nothing does not stall a status
// read behind it.
const cloudActivityTimeout = 3 * time.Second

// cloudActivityAttacher is the one method cloudWorkerSnapshot needs: a fake
// hands it recorded frames, production hands it a *cloudWorker.
type cloudActivityAttacher interface {
	attach(ctx context.Context) (<-chan workerview.Frame, error)
}

// cloudWorkerActivity is one cloud worker's activity, in the shape
// internal/statusmodel.Sources.RemoteActivity answers: nil when the worker
// cannot be reached or has said nothing yet, the honest not-measured.
func cloudWorkerActivity(ctx context.Context, client *cloudClient, runID, tickID string, attempt int) *statusmodel.ActivityInput {
	if client == nil || attempt <= 0 {
		return nil
	}
	w := &cloudWorker{client: client, runID: runID, tickID: tickID, attempt: attempt}
	return activityFromSnapshot(cloudWorkerSnapshot(ctx, w, cloudActivityTimeout))
}

// cloudWorkerSnapshot opens the worker's watch socket, folds frames into a
// fresh model until the first events frame lands (the whole conversation so
// far) or timeout elapses, then returns whatever the model holds — closing
// the socket either way, through the context the attacher's own attach
// keys its connection's life to. nil only when the socket could not be
// opened at all.
func cloudWorkerSnapshot(ctx context.Context, src cloudActivityAttacher, timeout time.Duration) *workerview.Model {
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
// a guess), and the last tool call — REDACTED and bounded through the same
// pattern set the local transcript reader redacts with, because this line
// is rendered on the dashboard and shipped off-host with the phone snapshot
// exactly like the local one is.
func activityFromSnapshot(m *workerview.Model) *statusmodel.ActivityInput {
	if m == nil {
		return nil
	}
	h := m.Heartbeat
	if h.LastTool == nil && h.LastActivity.IsZero() {
		return nil
	}
	input := &statusmodel.ActivityInput{}
	for _, it := range m.Items {
		if !it.At.IsZero() {
			input.Events = append(input.Events, it.At)
		}
	}
	if h.LastTool != nil {
		action := h.LastTool.Tool
		if h.LastTool.Args != "" {
			action += ": " + h.LastTool.Args
		}
		input.LastAction = subprocess.BoundLine(subprocess.RedactCredentials(action))
		input.LastActionAt = h.LastTool.At
	}
	return input
}
