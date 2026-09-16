package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// fakeHerd answers the agent list and records notifications.
type fakeHerd struct {
	agents  []client.AgentInfo
	shown   []client.NotificationShowParams
	answers []client.NotificationShown
	listErr error
	showErr error
}

func (f *fakeHerd) AgentList(context.Context) ([]client.AgentInfo, error) {
	return f.agents, f.listErr
}

func (f *fakeHerd) NotificationShow(_ context.Context, params client.NotificationShowParams) (*client.NotificationShown, error) {
	if f.showErr != nil {
		return nil, f.showErr
	}
	f.shown = append(f.shown, params)
	var answer client.NotificationShown
	if len(f.answers) > 0 {
		answer = f.answers[0]
		f.answers = f.answers[1:]
	} else {
		answer = client.NotificationShown{Shown: true, Reason: client.ReasonShown}
	}
	return &answer, nil
}

func agent(name, pane string, status client.AgentStatus) client.AgentInfo {
	n := name
	return client.AgentInfo{PaneID: pane, AgentStatus: status, Agent: &n}
}

func TestDecideBlockedChimesOnceAndRearms(t *testing.T) {
	blocked := Worker{Tick: "w1", Epic: "e1", Role: "implement-tick", PaneID: "p1", Live: true, Status: client.StatusBlocked}

	// First sight: the blocked chime fires (the wave chime fires too — a
	// lone blocked worker IS a settled wave; the next test covers that).
	plan := Decide(State{}, "r1", []Worker{blocked})
	var blockedChime *Notification
	for i := range plan.Notifications {
		if plan.Notifications[i].Kind == KindBlocked {
			blockedChime = &plan.Notifications[i]
		}
	}
	if blockedChime == nil {
		t.Fatalf("first decision: %+v", plan.Notifications)
	}
	if blockedChime.Sound != client.SoundRequest {
		t.Fatalf("blocked sound: %q", blockedChime.Sound)
	}
	if blockedChime.Title != "tick w1 blocked" {
		t.Fatalf("blocked title: %q", blockedChime.Title)
	}

	// Same state again, having recorded it: the blocked chime is
	// suppressed (the wave chime is suppressed too — same roster).
	plan = Decide(plan.Next, "r1", []Worker{blocked})
	for _, n := range plan.Notifications {
		if n.Kind == KindBlocked {
			t.Fatalf("a blocked episode must chime once: %+v", plan.Notifications)
		}
	}
	held := false
	for _, s := range plan.Suppressed {
		if s.Kind == KindBlocked {
			held = true
		}
	}
	if !held {
		t.Fatalf("the hold-back must be reported: %+v", plan.Suppressed)
	}

	// Worker recovers, then blocks again: two episodes, two chimes. The
	// recovering decision is what clears the memo, so its state is the one
	// the re-blocking decision must read.
	recovered := blocked
	recovered.Status = client.StatusWorking
	recoveredPlan := Decide(plan.Next, "r1", []Worker{recovered})
	if len(recoveredPlan.Next.BlockedNotified) != 0 {
		t.Fatalf("recovering must re-arm the blocked chime: %+v", recoveredPlan.Next)
	}
	plan = Decide(recoveredPlan.Next, "r1", []Worker{blocked})
	chimed := false
	for _, n := range plan.Notifications {
		if n.Kind == KindBlocked {
			chimed = true
		}
	}
	if !chimed {
		t.Fatalf("a NEW blocked episode must chime: %+v", plan.Notifications)
	}
}

func TestDecideWaveCompleteChimesOnceAndRearmsOnFreshWorker(t *testing.T) {
	settled := Worker{Tick: "w1", PaneID: "p1", Live: true, Status: client.StatusIdle}
	running := Worker{Tick: "w2", PaneID: "p2", Live: true, Status: client.StatusWorking}

	// Mixed wave: nothing.
	plan := Decide(State{}, "r1", []Worker{settled, running})
	if plan.WaveComplete || len(plan.Notifications) != 0 {
		t.Fatalf("mixed wave: %+v", plan)
	}

	// All settled: chime done.
	running.Status = client.StatusDone
	plan = Decide(plan.Next, "r1", []Worker{settled, running})
	if !plan.WaveComplete || len(plan.Notifications) != 1 || plan.Notifications[0].Kind != KindWaveComplete {
		t.Fatalf("settled wave: %+v", plan)
	}
	if plan.Notifications[0].Sound != client.SoundDone {
		t.Fatalf("wave sound: %q", plan.Notifications[0].Sound)
	}
	if plan.Notifications[0].Title != "wave complete: 2 workers settled" {
		t.Fatalf("wave title: %q", plan.Notifications[0].Title)
	}

	// Same roster seen again: suppressed.
	plan2 := Decide(plan.Next, "r1", []Worker{settled, running})
	if len(plan2.Notifications) != 0 {
		t.Fatalf("an already-counted completion must not re-chime: %+v", plan2.Notifications)
	}

	// A fresh worker joins and settles: news again.
	fresh := Worker{Tick: "w3", PaneID: "p3", Live: true, Status: client.StatusDone}
	plan3 := Decide(plan.Next, "r1", []Worker{settled, running, fresh})
	if len(plan3.Notifications) != 1 || plan3.Notifications[0].Kind != KindWaveComplete {
		t.Fatalf("a fresh worker re-arms the wave chime: %+v", plan3.Notifications)
	}
	if !reflect.DeepEqual(plan3.Notifications[0].Body, "run r1: w1, w2, w3") {
		t.Fatalf("wave body: %q", plan3.Notifications[0].Body)
	}
}

func TestDecideBlockedCountsAsSettledAndIsSaid(t *testing.T) {
	// A wave whose last worker is BLOCKED is complete AND wants you: both
	// chimes fire in one invocation, because they answer different
	// questions.
	blocked := Worker{Tick: "w9", PaneID: "p9", Live: true, Status: client.StatusBlocked}
	plan := Decide(State{}, "r1", []Worker{blocked})
	if !plan.WaveComplete {
		t.Fatal("a blocked worker is settled, on purpose")
	}
	kinds := map[Kind]bool{}
	for _, n := range plan.Notifications {
		kinds[n.Kind] = true
	}
	if !kinds[KindBlocked] || !kinds[KindWaveComplete] {
		t.Fatalf("both chimes must fire: %+v", plan.Notifications)
	}
	if got := plan.Notifications[len(plan.Notifications)-1].Body; !reflect.DeepEqual(got, "run r1: w9 (1 blocked)") {
		t.Fatalf("the wave body must name the blocked worker: %q", got)
	}
}

func TestDecideAbsentWorkersAreNotSettled(t *testing.T) {
	gone := Worker{Tick: "w1", PaneID: "p1", Live: false}
	plan := Decide(State{}, "r1", []Worker{gone})
	if plan.WaveComplete || plan.InFlight != 0 {
		t.Fatalf("absent workers hold nothing open: %+v", plan)
	}
	if len(plan.Notifications) != 0 {
		t.Fatalf("nothing to say: %+v", plan.Notifications)
	}
}

func TestRetractRateLimitOnly(t *testing.T) {
	plan := Decide(State{}, "r1", []Worker{
		{Tick: "w1", Live: true, Status: client.StatusBlocked},
	})
	var blockedChime *Notification
	for i := range plan.Notifications {
		if plan.Notifications[i].Kind == KindBlocked {
			blockedChime = &plan.Notifications[i]
		}
	}
	if blockedChime == nil {
		t.Fatalf("setup: %+v", plan.Notifications)
	}
	if len(plan.Next.BlockedNotified) != 1 {
		t.Fatalf("setup memo: %+v", plan.Next)
	}
	plan.Retract(*blockedChime)
	if len(plan.Next.BlockedNotified) != 0 {
		t.Fatalf("retracting a rate-limited blocked chime must drop it from the memo: %+v", plan.Next)
	}

	settlePlan := Decide(State{}, "r1", []Worker{{Tick: "w1", Live: true, Status: client.StatusIdle}})
	if len(settlePlan.Notifications) != 1 || settlePlan.Notifications[0].Kind != KindWaveComplete {
		t.Fatalf("setup: %+v", settlePlan.Notifications)
	}
	if len(settlePlan.Next.WaveCounted) != 1 {
		t.Fatalf("setup roster: %+v", settlePlan.Next)
	}
	settlePlan.Retract(settlePlan.Notifications[0])
	if len(settlePlan.Next.WaveCounted) != 0 {
		t.Fatalf("retracting a rate-limited wave chime must restore the prior roster: %+v", settlePlan.Next)
	}
}

func TestRunSendsPersistsAndRetracts(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "r1", StateFile)
	h := &fakeHerd{
		agents:  []client.AgentInfo{agent("w1-agent", "p1", client.StatusBlocked)},
		answers: []client.NotificationShown{{Shown: false, Reason: client.ReasonRateLimited}},
	}
	res, err := Run(context.Background(), h, Options{
		RunID:     "r1",
		Workers:   []Worker{{Tick: "w1", Epic: "e1", Role: "implement-tick", Agent: "w1-agent", PaneID: "p1"}},
		StatePath: statePath,
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	// Both chimes fire for a lone blocked worker (it IS a settled wave);
	// the first — blocked — is rate limited, the second — the wave — is
	// shown. Only the blocked one must be retracted from the memo.
	if len(res.Notifications) != 2 {
		t.Fatalf("notifications: %+v", res.Notifications)
	}
	if res.Notifications[0].Kind != KindBlocked || res.Notifications[0].Sent != true || res.Notifications[0].Shown {
		t.Fatalf("blocked chime: %+v", res.Notifications[0])
	}
	if res.Notifications[1].Kind != KindWaveComplete || !res.Notifications[1].Shown {
		t.Fatalf("wave chime: %+v", res.Notifications[1])
	}
	if s := LoadState(statePath); len(s.BlockedNotified) != 0 {
		t.Fatalf("a rate-limited chime was consumed: %+v", s)
	}
	res, err = Run(context.Background(), h, Options{
		RunID:     "r1",
		Workers:   []Worker{{Tick: "w1", Epic: "e1", Agent: "w1-agent", PaneID: "p1"}},
		StatePath: statePath,
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(res.Notifications) != 1 {
		t.Fatalf("the retried invocation must chime: %+v", res.Notifications)
	}
	if s := LoadState(statePath); len(s.BlockedNotified) != 1 || s.BlockedNotified[0] != "w1" {
		t.Fatalf("the delivered chime must be memoized: %+v", s)
	}
}

func TestRunDryRunConsumesNothing(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "r1", StateFile)
	h := &fakeHerd{
		agents: []client.AgentInfo{agent("w2-agent", "p2", client.StatusBlocked)},
	}
	for i := 0; i < 2; i++ {
		res, err := Run(context.Background(), h, Options{
			RunID:     "r1",
			Workers:   []Worker{{Tick: "w2", Agent: "w2-agent", PaneID: "p2"}},
			StatePath: statePath,
			DryRun:    true,
		})
		if err != nil {
			t.Fatalf("notify: %v", err)
		}
		if len(res.Notifications) != 2 {
			t.Fatalf("a dry run must still DECIDE both chimes (invocation %d): %+v", i, res.Notifications)
		}
		for _, n := range res.Notifications {
			if n.Sent {
				t.Fatal("a dry run must not send")
			}
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("a dry run must not write state: %v", err)
	}
}

func TestRunRefusalDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "r1", StateFile)
	h := &fakeHerd{
		agents:  []client.AgentInfo{agent("w3-agent", "p3", client.StatusBlocked)},
		showErr: os.ErrPermission,
	}
	if _, err := Run(context.Background(), h, Options{
		RunID:     "r1",
		Workers:   []Worker{{Tick: "w3", Agent: "w3-agent", PaneID: "p3"}},
		StatePath: statePath,
	}); err == nil {
		t.Fatal("a herdr refusal must fail the run")
	}
	if s := LoadState(statePath); len(s.BlockedNotified) != 0 {
		t.Fatalf("an undelivered chime was memoized: %+v", s)
	}
}

func TestRunJoinsByPaneFirst(t *testing.T) {
	// A pane id is the join key; the agent name is only a fallback for a
	// record with no pane id.
	h := &fakeHerd{
		agents: []client.AgentInfo{
			agent("w4-agent", "p4", client.StatusWorking),
			agent("w5-agent", "p5", client.StatusDone),
		},
	}
	res, err := Run(context.Background(), h, Options{
		RunID:     "r1",
		Workers:   []Worker{{Tick: "w4", Agent: "w5-agent", PaneID: "p4"}},
		StatePath: filepath.Join(t.TempDir(), "r1", StateFile),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !res.Workers[0].Live || res.Workers[0].Status != client.StatusWorking {
		t.Fatalf("join: %+v", res.Workers[0])
	}

	res, err = Run(context.Background(), h, Options{
		RunID:     "r1",
		Workers:   []Worker{{Tick: "w4", Agent: "w5-agent"}},
		StatePath: filepath.Join(t.TempDir(), "r1", StateFile),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !res.Workers[0].Live || res.Workers[0].Status != client.StatusDone {
		t.Fatalf("name fallback join: %+v", res.Workers[0])
	}
}

func TestStateRoundTripAndDiscard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFile)
	s := State{BlockedNotified: []string{"b", "a", "a"}, WaveCounted: []string{"c"}}
	if err := SaveState(path, s, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadState(path)
	if !reflect.DeepEqual(got.BlockedNotified, []string{"a", "b"}) || !reflect.DeepEqual(got.WaveCounted, []string{"c"}) {
		t.Fatalf("round trip: %+v", got)
	}

	// A malformed or wrong-version memo is empty, never an error.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(path); got.Version != StateVersion || len(got.BlockedNotified) != 0 {
		t.Fatalf("malformed memo: %+v", got)
	}
	wrong, _ := json.Marshal(State{Version: StateVersion + 1, BlockedNotified: []string{"x"}})
	if err := os.WriteFile(path, wrong, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(path); len(got.BlockedNotified) != 0 {
		t.Fatalf("wrong-version memo: %+v", got)
	}
}
