package herdr

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The nudge, herdr's half (the local executor's is subprocess/nudge.go).
//
// epic-2jn vqc (2026-09-27): a claude worker committed, started the gate as a
// background task and ended its turn "to wait for the completion
// notification". In print mode that ended the process; in a herdr pane it
// ends nothing — the agent sits idle, its turn over, with no report, and
// nobody will ever deliver the notification it is waiting for. The attempt
// read `running` until the wall clock stopped it and then collected as
// missing-result: a whole wall clock spent on a worker that was one sentence
// from finishing.
//
// So an agent herdr answers is idle (`idle` or `done`: its turn has ended)
// with no report, for longer than IdleGrace, is re-prompted in its OWN pane
// through agent.prompt — the same session, with its whole history — at most
// subprocess.MaxNudges times. After the last one, an agent idle again with no
// report is settled as failed: missing-result, the same verdict the wall clock
// would have reached, without spending the rest of the wall clock to reach it.
// Each nudge is a `started` observation (subprocess.IsNudge), which the
// reconciler puts on the feed.
//
// The grace is what keeps a turn boundary from reading as the end of the work:
// the status reaches idle before a freshly launched agent is ready, and a
// nudge's own turn needs a moment to register as working. The idle-since
// stamp is cleared whenever the agent is seen doing anything else, so only an
// UNBROKEN idle stretch counts.

// DefaultIdleGrace is how long an agent sits with its turn ended and no report
// before it is nudged. Long enough that a turn boundary or a slow status
// update is never mistaken for the end of the work; short against any wall
// clock a real job carries.
const DefaultIdleGrace = 90 * time.Second

// The nudge's durable facts, beside the attempt's other markers: when the
// current idle stretch began, how many nudges were sent, and that the nudges
// were spent and the attempt settled.
const (
	fileIdleSince   = "idle-since"
	fileNudges      = "nudges"
	fileIdleSettled = "idle-settled"
)

// turnEnded is whether herdr's status says the agent's turn is over.
func turnEnded(status client.AgentStatus) bool {
	return status == client.StatusIdle || status == client.StatusDone
}

func (s *store) idleSince() (time.Time, bool) {
	raw, err := os.ReadFile(s.path(fileIdleSince))
	if err != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	return at, err == nil
}

func (s *store) markIdleSince(at time.Time) error {
	return s.writeFile(s.path(fileIdleSince), []byte(at.UTC().Format(time.RFC3339Nano)+"\n"), 0o644)
}

func (s *store) clearIdleSince() { _ = os.Remove(s.path(fileIdleSince)) }

func (s *store) nudges() int {
	raw, err := os.ReadFile(s.path(fileNudges))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

func (s *store) markNudges(n int) error {
	return s.writeFile(s.path(fileNudges), []byte(strconv.Itoa(n)+"\n"), 0o644)
}

// idleSettled is the settlement the spent nudges recorded: this executor's
// own durable record, like agent-gone and the wall marker, that a herdr-free
// collect reads as settled rather than holding the attempt for a person.
func (s *store) idleSettled() bool { return s.exists(fileIdleSettled) }

func (s *store) markIdleSettled(at string) error {
	if s.exists(fileIdleSettled) {
		return nil
	}
	return s.writeFile(s.path(fileIdleSettled), []byte(at+"\n"), 0o644)
}

// idleSettledDetail is the sentence a settled-idle attempt reads as.
func idleSettledDetail(record *attemptRecord) string {
	return fmt.Sprintf("the agent %s ended its turn with no report at %s after %d nudges: settled as failed "+
		"with the agent left idle in its pane, never read as running until the wall clock",
		record.AgentName, record.ResultPath, subprocess.MaxNudges)
}

// nudgeIdle is observe's answer for a live agent inside its wall clock with
// no report. It returns handled=false for an agent that is doing anything but
// sitting idle, and the state and sentence otherwise.
func (e *Executor) nudgeIdle(st *store, record *attemptRecord, agent *client.AgentInfo) (state, detail string, handled bool) {
	if !turnEnded(agent.AgentStatus) {
		st.clearIdleSince()
		return "", "", false
	}
	now := e.now()
	since, ok := st.idleSince()
	if !ok {
		_ = st.markIdleSince(now)
		since = now
	}
	idle := now.Sub(since)
	if idle < e.opts.IdleGrace {
		return subprocess.StateRunning, fmt.Sprintf(
			"the agent %s is live in pane %s and its turn has ended (herdr reports %s) with no report yet; "+
				"it is re-prompted if it stays so for %s", record.AgentName, agent.PaneID, agent.AgentStatus,
			e.opts.IdleGrace), true
	}

	sent := st.nudges()
	if sent >= subprocess.MaxNudges {
		_ = st.markIdleSettled(e.stamp())
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: idleSettledDetail(record)})
		return subprocess.StateFailed, idleSettledDetail(record), true
	}

	n := sent + 1
	_, err := e.client.AgentPrompt(context.Background(), client.AgentPromptParams{
		Target: record.AgentName,
		Text:   subprocess.NudgePrompt(record.Branch, record.ResultPath),
	})
	if err != nil && !client.IsCode(err, client.CodeAgentPromptStalled) {
		// Not delivered: nothing is counted, and the next poll tries again.
		// A stall is herdr seeing no state change within its own window,
		// which an agent that answers fast produces too: the prompt was
		// submitted, so it counts.
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the agent %s ended its turn with no report, and the nudge could not be "+
				"delivered through herdr (%v); it is re-attempted at the next poll", record.AgentName, err)})
		return subprocess.StateRunning, fmt.Sprintf(
			"the agent %s ended its turn with no report, and the nudge is re-attempted", record.AgentName), true
	}
	_ = st.markNudges(n)
	st.clearIdleSince()
	what := fmt.Sprintf("the %s agent %s ended its turn %s ago", record.Kind, record.AgentName, idle.Round(time.Second))
	how := fmt.Sprintf("re-prompted in its own pane %s", agent.PaneID)
	_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsStarted,
		Detail: subprocess.NudgeDetail(n, what, record.ResultPath, how)})
	return subprocess.StateRunning, fmt.Sprintf("the agent %s was nudged (%d of %d) and is working again",
		record.AgentName, n, subprocess.MaxNudges), true
}
