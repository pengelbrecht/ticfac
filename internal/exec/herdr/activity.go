package herdr

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The stuck watch, herdr's half (tick wv2; the policy and the signals are
// subprocess/activity.go, shared with the local supervisor).
//
// Each poll of a live agent that is NOT at its prompt (an agent whose turn
// ended is nudge.go's) takes one look, at most every CheckEvery(StuckAfter):
//
//   - its harness's transcript for the worktree it runs in;
//   - the CPU of the tool processes under it: pane.process_info names the
//     pane's shell pid, the agent is the shell's child, and everything under
//     the agent is its tools — walked by pid from the shell, never by name;
//   - when its worktree last changed and its branch last moved.
//
// Quiet on all of them for StuckAfter: one nudge, typed into its own pane
// through agent.prompt with the evidence. Still quiet StuckAfter after that:
// the harness's own interrupt, the worktree snapshotted, the pane closed,
// and the attempt settled failed as stuck — the retry and the tier ladder
// take it from there.
//
// The same look also serves the commit-WIP nudge: a large uncommitted change
// on a branch that has not moved for subprocess.WipNudgeEvery is asked to be
// committed, at most once per that interval.

const (
	fileActivity     = "activity.json"
	fileStuckStopped = "stuck-stopped"
)

func (s *store) activity() subprocess.ActivityState {
	var a subprocess.ActivityState
	_ = s.readJSON(fileActivity, &a)
	return a
}

func (s *store) saveActivity(a subprocess.ActivityState) { _ = s.writeJSON(fileActivity, a) }

// stuckStopped is this executor's durable record that it stopped the attempt
// as stuck: what a herdr-free inspect and collect read the settlement as.
func (s *store) stuckStopped() bool { return s.exists(fileStuckStopped) }

func (s *store) markStuckStopped(detail string) error {
	return s.writeFile(s.path(fileStuckStopped), []byte(detail+"\n"), 0o644)
}

// stuckStopDetail is the sentence the stop was recorded with.
func (s *store) stuckStopDetail() string {
	raw, err := os.ReadFile(s.path(fileStuckStopped))
	if err != nil {
		return "stopped as stuck"
	}
	return strings.TrimSpace(string(raw))
}

// watchActivity is observe's look at a live agent that is not at its prompt.
// handled=false means "nothing to do": the caller answers running as before.
func (e *Executor) watchActivity(st *store, record *attemptRecord, agent *client.AgentInfo) (state, detail string, handled bool) {
	after := e.opts.StuckAfter
	if after < 0 || turnEnded(agent.AgentStatus) {
		return "", "", false
	}
	now := e.now()
	s := st.activity()
	if s.FirstSeenAt.IsZero() {
		// The baseline is the attempt's issue: nothing it did can be older,
		// and a worker that was quiet since before this look was quiet.
		s.FirstSeenAt = now
		if issued, err := time.Parse(time.RFC3339, record.IssuedAt); err == nil && issued.Before(now) {
			s.FirstSeenAt = issued
		}
	}
	if !s.CheckedAt.IsZero() && now.Sub(s.CheckedAt) < subprocess.CheckEvery(after) {
		st.saveActivity(s)
		return "", "", false
	}
	s.CheckedAt = now

	a := subprocess.Activity{FirstSeenAt: s.FirstSeenAt, Status: string(agent.AgentStatus)}
	if ev, ok := subprocess.LastTranscriptEvent(record.Kind, record.Worktree); ok {
		a.Transcript, a.HasTranscript = ev, true
	}
	pane := agent.PaneID
	if pane == "" {
		pane = record.PaneID
	}
	if info, err := e.client.PaneProcessInfo(context.Background(), pane); err == nil && info.ShellPID != nil {
		if procs, err := e.opts.Procs(); err == nil {
			// depth 0 the shell, 1 the agent, 2 and below its tools.
			cpu, n := subprocess.TreeCPU(procs, *info.ShellPID, 2)
			s.ObserveCPU(cpu, now, after)
			a.ToolCPU, a.ToolProcs, a.CPUMeasured, a.CPUAt = cpu, n, true, s.CPUMarkAt
		}
	}
	a.BranchAt, a.WorktreeAt = subprocess.MeasureWork(record.Repo, record.Branch, record.Worktree, now)

	if due, evidence := subprocess.WipNudgeDue(&s, a.BranchAt, now, func() (int, int, error) {
		return subprocess.Uncommitted(record.Worktree)
	}); due {
		if e.prompt(record, subprocess.WipPrompt(record.Branch, evidence)) {
			s.WipNudgedAt = now
			_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
				Detail: subprocess.WipNudgeDetail(evidence)})
		}
	}

	switch subprocess.DecideStuck(&s, a, now, after) {
	case subprocess.StuckNudge:
		evidence := a.Evidence(now)
		if !e.prompt(record, subprocess.StuckPrompt(evidence, after)) {
			st.saveActivity(s)
			return subprocess.StateRunning, fmt.Sprintf("the agent %s appears stuck and the nudge could not be "+
				"delivered through herdr; it is re-attempted at the next look — %s", record.AgentName, evidence), true
		}
		s.StuckNudgedAt = now
		st.saveActivity(s)
		how := fmt.Sprintf("the %s agent %s was nudged in its own pane %s", record.Kind, record.AgentName, pane)
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: subprocess.StuckNudgeDetail(how, evidence)})
		return subprocess.StateRunning, fmt.Sprintf("the agent %s appears stuck and was nudged: %s",
			record.AgentName, evidence), true
	case subprocess.StuckStop:
		evidence := a.Evidence(now)
		st.saveActivity(s)
		return e.stopStuck(st, record, pane, evidence)
	}
	st.saveActivity(s)
	return "", "", false
}

// prompt types text into the agent's own pane. A stall (herdr saw no state
// change within its window) still submitted the prompt, so it counts.
func (e *Executor) prompt(record *attemptRecord, text string) bool {
	_, err := e.client.AgentPrompt(context.Background(), client.AgentPromptParams{Target: record.AgentName, Text: text})
	return err == nil || client.IsCode(err, client.CodeAgentPromptStalled)
}

// stopStuck is the stop: the harness's own interrupt, the worktree
// snapshotted, the pane closed. A snapshot that fails holds the close, as at
// the wall clock: a stop that destroyed the work would be worse than the
// stall.
func (e *Executor) stopStuck(st *store, record *attemptRecord, pane, evidence string) (string, string, bool) {
	_, _ = e.client.AgentSendKeys(context.Background(), client.AgentSendKeysParams{
		Target: record.AgentName, Keys: interruptKeys(record.Kind),
	})
	if err := e.snapshotUncommitted(st, record); err != nil {
		return subprocess.StateRunning, fmt.Sprintf("the agent %s is stuck and is to be stopped, but its worktree "+
			"could not be snapshotted first (%v): the close is held and re-attempted — %s", record.AgentName, err, evidence), true
	}
	err := e.client.PaneClose(context.Background(), client.PaneCloseParams{PaneID: pane})
	if err != nil && !gone(err) {
		return subprocess.StateRunning, fmt.Sprintf("the agent %s is stuck and its pane %s could not be closed "+
			"through herdr (%v): the close is re-attempted — %s", record.AgentName, pane, err, evidence), true
	}
	how := fmt.Sprintf("the %s agent %s was interrupted and its pane %s closed", record.Kind, record.AgentName, pane)
	detail := subprocess.StuckStopDetail(how, evidence)
	if err := st.markStuckStopped(detail); err != nil {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: "the stuck stop could not be recorded: " + err.Error()})
	}
	if _, gerr := e.client.AgentGet(context.Background(), record.AgentName); gone(gerr) {
		_ = st.markAgentGone(e.stamp())
	}
	_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited, Detail: detail})
	return subprocess.StateFailed, detail + fmt.Sprintf("; no report at %s", record.ResultPath), true
}
