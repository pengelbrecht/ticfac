// Package notify chimes when a run's herdr worker blocks or a wave settles —
// the operator surface `tk herd notify` provided before ticks' pwp deleted the
// herd surface (tick glb owns it here now).
//
// Ported from ticks' internal/herd/notify (deleted in ticks at 0b5fabc0), with
// the same one adaptation as internal/herd/paint: the workers arrive as a
// plain [Worker] list read from the herdr executor's attempt records rather
// than tk's spawn manifests, and the once-semantics state lives beside the
// executor's own state for the run instead of under .tick/logs/herd. The
// decision core — [Decide], [State], the send/persist order, the rate-limit
// retraction — is the shape ticks shipped, proven in production.
package notify

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// Kind names the two things this package can say.
type Kind string

const (
	// KindBlocked is "a worker wants you". Sound: request.
	KindBlocked Kind = "blocked"
	// KindWaveComplete is "nothing is running any more". Sound: done.
	KindWaveComplete Kind = "wave_complete"
)

// SettledStatuses are the agent statuses that count as a worker having
// stopped. `blocked` is in the set deliberately — see the package doc.
var SettledStatuses = []client.AgentStatus{
	client.StatusIdle,
	client.StatusDone,
	client.StatusBlocked,
}

// settled reports whether a live agent status stops the wave.
func settled(status client.AgentStatus) bool {
	for _, s := range SettledStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// Worker is one of the run's attempts joined to its live agent state. It is
// the only input the decision reads, which is what makes the decision testable
// without a socket.
type Worker struct {
	// Tick, Epic and Role come from the attempt record.
	Tick  string `json:"tick"`
	Epic  string `json:"epic,omitempty"`
	Role  string `json:"role,omitempty"`
	Agent string `json:"agent,omitempty"`
	// PaneID is the attempt's recorded pane; it is how the record is joined
	// to `agent.list`.
	PaneID string `json:"pane_id,omitempty"`
	// Live is whether herdr still has an agent on that pane. An attempt
	// whose agent is gone is finished business and holds nothing open.
	Live bool `json:"live"`
	// Status is the live agent status, empty when not live.
	Status client.AgentStatus `json:"status,omitempty"`
}

// Settled reports whether this worker has stopped. A worker herdr no longer
// knows about is not settled — it is absent, and absent workers are excluded
// from the wave rather than counted as finished.
func (w Worker) Settled() bool { return w.Live && settled(w.Status) }

// Notification is one thing to say, and the evidence for saying it.
type Notification struct {
	Kind  Kind                     `json:"kind"`
	Tick  string                   `json:"tick,omitempty"`
	Epic  string                   `json:"epic,omitempty"`
	Title string                   `json:"title"`
	Body  string                   `json:"body,omitempty"`
	Sound client.NotificationSound `json:"sound"`

	// Sent records that herdr accepted the call. False under DryRun.
	Sent bool `json:"sent"`
	// Shown and Reason are herdr's own answer: it may decline to show a
	// notification it accepted (notifications disabled, no foreground
	// client, rate limited). Declining is not an error, but it is not
	// delivery either, so both are reported.
	Shown  bool                          `json:"shown,omitempty"`
	Reason client.NotificationShowReason `json:"reason,omitempty"`
}

// Suppressed is a notification NOT sent, and why. It is reported rather than
// dropped: "this invocation ran and deliberately said nothing" is the
// observable that proves the once-semantics, and without it a silent run and
// a broken run look identical.
type Suppressed struct {
	Kind   Kind   `json:"kind"`
	Tick   string `json:"tick,omitempty"`
	Reason string `json:"reason"`
}

// Plan is the pure decision: what to say, what was held back, and the state to
// persist if the plan is carried out.
type Plan struct {
	Notifications []Notification `json:"notifications"`
	Suppressed    []Suppressed   `json:"suppressed,omitempty"`
	Next          State          `json:"-"`
	// PriorWaveCounted is the previous state's wave roster, pruned to the
	// current workers. It is what [Plan.Retract] restores when herdr drops
	// the wave chime transiently.
	PriorWaveCounted []string `json:"-"`

	// InFlight is how many workers herdr still has an agent for.
	InFlight int `json:"in_flight"`
	// SettledCount is how many of those have stopped.
	SettledCount int `json:"settled"`
	// WaveComplete is whether every in-flight worker has stopped. It is
	// true even when the completion was already announced — the fact and
	// the decision to speak are separate.
	WaveComplete bool `json:"wave_complete"`
}

// Decide is the whole notification policy, as a pure function of the previous
// state and the current workers.
//
// Everything interesting about this package is here, and nothing here talks to
// a socket, a clock or a filesystem: the once-semantics are exactly "what does
// the previous state say", so they can be driven to any edge in a unit test.
func Decide(prev State, runID string, workers []Worker) Plan {
	plan := Plan{Notifications: []Notification{}}

	prevBlocked := setOf(prev.BlockedNotified)
	prevCounted := setOf(prev.WaveCounted)

	// Ticks that still have a worker, used to keep the persisted sets from
	// growing without bound as runs progress.
	roster := make(map[string]bool, len(workers))
	for _, w := range workers {
		roster[w.Tick] = true
	}

	sorted := append([]Worker(nil), workers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Tick < sorted[j].Tick })

	// --- 1. blocked -----------------------------------------------------
	//
	// Edge-triggered: notify a blocked worker once, and re-arm it the
	// moment it is no longer blocked. Rebuilding the set from the workers
	// that are blocked RIGHT NOW does both at once — a tick that
	// recovered, or whose agent went away, simply is not carried forward.
	nextBlocked := make([]string, 0, len(prev.BlockedNotified))
	for _, w := range sorted {
		if !w.Live || w.Status != client.StatusBlocked {
			continue
		}
		nextBlocked = append(nextBlocked, w.Tick)
		if prevBlocked[w.Tick] {
			plan.Suppressed = append(plan.Suppressed, Suppressed{
				Kind:   KindBlocked,
				Tick:   w.Tick,
				Reason: "already notified for this blocked episode",
			})
			continue
		}
		plan.Notifications = append(plan.Notifications, blockedNotification(w))
	}

	// --- 2. wave completion ---------------------------------------------
	for _, w := range sorted {
		if !w.Live {
			continue
		}
		plan.InFlight++
		if w.Settled() {
			plan.SettledCount++
		}
	}
	plan.WaveComplete = plan.InFlight > 0 && plan.SettledCount == plan.InFlight

	nextCounted := make([]string, 0, len(prev.WaveCounted)+plan.InFlight)
	for tick := range prevCounted {
		// Prune ticks whose worker is gone: a torn-down attempt can
		// never re-arm anything, and keeping it would grow the file.
		if roster[tick] {
			nextCounted = append(nextCounted, tick)
		}
	}
	plan.PriorWaveCounted = normalize(nextCounted)

	switch {
	case plan.InFlight == 0:
		if len(workers) > 0 {
			plan.Suppressed = append(plan.Suppressed, Suppressed{
				Kind:   KindWaveComplete,
				Reason: "no recorded worker is live — there is no wave in flight",
			})
		}
	case !plan.WaveComplete:
		plan.Suppressed = append(plan.Suppressed, Suppressed{
			Kind: KindWaveComplete,
			Reason: fmt.Sprintf("%d of %d workers still running",
				plan.InFlight-plan.SettledCount, plan.InFlight),
		})
	default:
		// A completion is news only if it includes a worker that was not
		// part of the last completion this notifier announced. That is
		// what makes a NEW attempt re-arm the chime while a repeated
		// invocation — same roster, same states — stays silent.
		fresh := make([]string, 0, plan.InFlight)
		for _, w := range sorted {
			if w.Live && !prevCounted[w.Tick] {
				fresh = append(fresh, w.Tick)
			}
		}
		if len(fresh) == 0 {
			plan.Suppressed = append(plan.Suppressed, Suppressed{
				Kind:   KindWaveComplete,
				Reason: "this wave's completion was already notified",
			})
		} else {
			plan.Notifications = append(plan.Notifications,
				waveNotification(runID, sorted, plan.InFlight))
		}
		for _, w := range sorted {
			if w.Live {
				nextCounted = append(nextCounted, w.Tick)
			}
		}
	}

	plan.Next = State{
		Version:         StateVersion,
		BlockedNotified: normalize(nextBlocked),
		WaveCounted:     normalize(nextCounted),
	}
	return plan
}

// Retract undoes one notification's effect on the state to persist, for a
// chime herdr accepted but dropped TRANSIENTLY.
//
// Only rate limiting is retracted. `disabled` and `no_foreground_client` are
// standing conditions: retrying them would re-send on every single event for
// the rest of the run, which is a log flood rather than a delivery.
func (p *Plan) Retract(n Notification) {
	switch n.Kind {
	case KindBlocked:
		out := p.Next.BlockedNotified[:0:0]
		for _, t := range p.Next.BlockedNotified {
			if t != n.Tick {
				out = append(out, t)
			}
		}
		p.Next.BlockedNotified = out
	case KindWaveComplete:
		p.Next.WaveCounted = p.PriorWaveCounted
	}
}

// blockedNotification renders the "a worker wants you" chime. The title names
// the TICK, not the pane or the agent handle: a tick id is the thing the
// operator can look up, and it is what the painted badge already says.
func blockedNotification(w Worker) Notification {
	body := make([]string, 0, 3)
	if w.Epic != "" {
		body = append(body, "epic "+w.Epic)
	}
	if w.Role != "" {
		body = append(body, w.Role)
	}
	if w.Agent != "" {
		body = append(body, w.Agent)
	}
	return Notification{
		Kind:  KindBlocked,
		Tick:  w.Tick,
		Epic:  w.Epic,
		Title: "tick " + w.Tick + " blocked",
		Body:  strings.Join(body, " · "),
		Sound: client.SoundRequest,
	}
}

// waveNotification renders the "nothing is running" chime.
func waveNotification(runID string, workers []Worker, n int) Notification {
	noun := "workers"
	if n == 1 {
		noun = "worker"
	}
	ticks := make([]string, 0, n)
	blocked := 0
	for _, w := range workers {
		if !w.Live {
			continue
		}
		ticks = append(ticks, w.Tick)
		if w.Status == client.StatusBlocked {
			blocked++
		}
	}
	body := strings.Join(ticks, ", ")
	if blocked > 0 {
		// A wave that "completed" with blocked workers in it is not the
		// same news as one that finished clean, and the operator must
		// not have to open the dashboard to find that out.
		body += fmt.Sprintf(" (%d blocked)", blocked)
	}
	if runID != "" {
		body = "run " + runID + ": " + body
	}
	return Notification{
		Kind:  KindWaveComplete,
		Title: fmt.Sprintf("wave complete: %d %s settled", n, noun),
		Body:  body,
		Sound: client.SoundDone,
	}
}

// setOf turns a tick list into a membership set.
func setOf(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, v := range in {
		out[v] = true
	}
	return out
}

// herd is the slice of the herdr client this package needs.
type herd interface {
	AgentList(ctx context.Context) ([]client.AgentInfo, error)
	NotificationShow(ctx context.Context, params client.NotificationShowParams) (*client.NotificationShown, error)
}

// Options is one notify run, scoped to a single run.
type Options struct {
	// RunID names the run whose wave is being judged. It is what the wave
	// chime says, and it selects the state directory when StatePath is
	// empty.
	RunID string
	// Workers are the run's attempts, as recorded by the executor.
	Workers []Worker
	// StateRoot is the executor's state root — host-local, outside any
	// repository — when StatePath is empty. The once-semantics state
	// lives beside the run's attempt records, so a run's notifier state
	// dies with the run's own state and never touches a repository.
	StateRoot string
	// StatePath overrides where the once-semantics state is kept, for
	// tests and for a state root laid out elsewhere.
	StatePath string
	// Now supplies the clock; nil means time.Now.
	Now func() time.Time
	// DryRun decides and reports without calling herdr and WITHOUT
	// persisting state, so a preview never eats the real notification.
	DryRun bool
}

// Result is the outcome of one notify run.
type Result struct {
	RunID         string         `json:"run_id,omitempty"`
	Workers       []Worker       `json:"workers"`
	Notifications []Notification `json:"notifications"`
	Suppressed    []Suppressed   `json:"suppressed,omitempty"`
	InFlight      int            `json:"in_flight"`
	Settled       int            `json:"settled"`
	WaveComplete  bool           `json:"wave_complete"`
	StatePath     string         `json:"state_path"`
	DryRun        bool           `json:"dry_run"`
}

// Run joins this run's attempts to the live agent list, decides, sends, and
// persists.
//
// The order matters: state is written AFTER the notifications are sent, and
// only for the notifications that herdr accepted. A crash between the two
// therefore repeats a chime rather than swallowing one — the failure this
// package is allowed to have.
func Run(ctx context.Context, h herd, opts Options) (Result, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	statePath := opts.StatePath
	if statePath == "" {
		if strings.TrimSpace(opts.RunID) == "" || strings.TrimSpace(opts.StateRoot) == "" {
			return Result{}, fmt.Errorf("herd/notify: no state path, and no run id over a state root to derive one from")
		}
		statePath = StatePath(opts.StateRoot, opts.RunID)
	}

	res := Result{
		RunID:         opts.RunID,
		Workers:       []Worker{},
		Notifications: []Notification{},
		StatePath:     statePath,
		DryRun:        opts.DryRun,
	}
	if len(opts.Workers) == 0 {
		return res, nil
	}

	agents, err := h.AgentList(ctx)
	if err != nil {
		return res, fmt.Errorf("herd/notify: listing herdr agents: %w", err)
	}
	workers := joinWorkers(opts.Workers, agents)
	res.Workers = workers

	// A dry run must not take the lock or write anything — it is a preview,
	// and previewing must never consume the real notification.
	if opts.DryRun {
		plan := Decide(LoadState(statePath), opts.RunID, workers)
		fillResult(&res, plan)
		return res, nil
	}

	unlock, err := Lock(statePath, now)
	if err != nil {
		return res, err
	}
	defer unlock()

	plan := Decide(LoadState(statePath), opts.RunID, workers)

	// Send first. A notification herdr REFUSED (a transport or protocol
	// failure) must not be recorded as said, or the retry never happens; a
	// notification herdr accepted but declined to display for a STANDING
	// reason (disabled, no foreground client) IS recorded, because
	// re-sending it would be declined identically and only spam the log.
	// A TRANSIENT refusal — rate limiting — is retracted so the next hook
	// invocation says it again; see [Plan.Retract].
	var sendErr error
	for i := range plan.Notifications {
		n := &plan.Notifications[i]
		body := n.Body
		// Position is deliberately left nil. notification.show has NO
		// pane or workspace target — it is an overlay herdr renders on
		// whichever client is in the foreground — so this call can never
		// open, split or steal anything in the operator's focused
		// workspace. Pinning a corner would only override their own
		// placement preference.
		params := client.NotificationShowParams{
			Title: n.Title,
			Sound: n.Sound,
		}
		if body != "" {
			params.Body = client.Ptr(body)
		}
		shown, err := h.NotificationShow(ctx, params)
		if err != nil {
			sendErr = fmt.Errorf("herd/notify: showing %q: %w", n.Title, err)
			break
		}
		n.Sent = true
		if shown != nil {
			n.Shown = shown.Shown
			n.Reason = shown.Reason
			if !shown.Shown && shown.Reason == client.ReasonRateLimited {
				plan.Retract(*n)
			}
		}
	}

	fillResult(&res, plan)

	if sendErr != nil {
		// Deliberately do NOT persist: nothing here was reliably said, so
		// the next invocation must be free to say it again.
		return res, sendErr
	}
	if err := SaveState(statePath, plan.Next, now()); err != nil {
		return res, err
	}
	return res, nil
}

// fillResult copies the plan's reportable fields onto the result.
func fillResult(res *Result, plan Plan) {
	res.Notifications = plan.Notifications
	res.Suppressed = plan.Suppressed
	res.InFlight = plan.InFlight
	res.Settled = plan.SettledCount
	res.WaveComplete = plan.WaveComplete
}

// joinWorkers matches each recorded worker to the live agent list.
//
// The pane id is the join key: it is what the attempt record captured and
// what `agent.list` reports, and unlike the agent NAME it cannot be reused by
// a later attempt under a different tick. The agent name is a fallback for a
// record written before a pane id was captured.
func joinWorkers(workers []Worker, agents []client.AgentInfo) []Worker {
	byPane := make(map[string]client.AgentInfo, len(agents))
	byName := make(map[string]client.AgentInfo, len(agents))
	for _, a := range agents {
		byPane[a.PaneID] = a
		if a.Name != nil && *a.Name != "" {
			byName[*a.Name] = a
		}
		if a.Agent != nil && *a.Agent != "" {
			if _, taken := byName[*a.Agent]; !taken {
				byName[*a.Agent] = a
			}
		}
	}

	out := make([]Worker, 0, len(workers))
	for _, w := range workers {
		joined := w
		a, ok := byPane[w.PaneID]
		if !ok && w.PaneID == "" && w.Agent != "" {
			a, ok = byName[w.Agent]
		}
		if ok {
			joined.Live = true
			joined.Status = a.AgentStatus
		}
		out = append(out, joined)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tick < out[j].Tick })
	return out
}
