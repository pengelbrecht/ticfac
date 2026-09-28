package herdr

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The report pushback, herdr's half (tick 4m6; the local executor's is
// subprocess/pushback.go).
//
// A report whose report check fails (subprocess/lint.go) is not yet the
// answer the run collects. While the agent is working it is left alone — it
// was told to run the check itself and may be fixing the report now. Once its
// turn has ended (idle for IdleGrace, the nudge's grace) with the report still
// failing, the checker's errors are typed into its OWN pane through
// agent.prompt — #81's nudge path — at most subprocess.MaxLintPushbacks times.
// After that, or when herdr cannot say the agent is there, the report settles
// the attempt as it always did, and collect decides: FATAL problems left are
// missing-result (retried), repairable ones are accepted as read.

const fileLintPushbacks = "lint-pushbacks"

func (s *store) lintPushbacks() int {
	raw, err := os.ReadFile(s.path(fileLintPushbacks))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

func (s *store) markLintPushbacks(n int) error {
	return s.writeFile(s.path(fileLintPushbacks), []byte(strconv.Itoa(n)+"\n"), 0o644)
}

// lintCommand is the report check this attempt's prompt names.
func lintCommand(record *attemptRecord, role string) string {
	return subprocess.LintCommand(subprocess.LintBinary(), record.ResultPath, role, record.TickID, record.Worktree)
}

// roleOf is the role an attempt was dispatched as, empty when its record
// carries no spec.
func roleOf(record *attemptRecord) string {
	if record.Spec == nil {
		return ""
	}
	return record.Spec.Role
}

// pushBackReport is observe's answer for an attempt whose report carries a
// STATUS line. handled=false means the report settles the attempt now.
func (e *Executor) pushBackReport(st *store, record *attemptRecord) (state, detail string, handled bool) {
	if e.pastWall(record) {
		return "", "", false
	}
	raw, err := os.ReadFile(record.ResultPath)
	if err != nil {
		return "", "", false
	}
	lint := subprocess.LintReport(string(raw), subprocess.LoadLintContext(record.Worktree, roleOf(record), record.TickID))
	if lint.Clean() {
		return "", "", false
	}
	sent := st.lintPushbacks()
	if sent >= subprocess.MaxLintPushbacks {
		return "", "", false
	}
	agent, err := e.client.AgentGet(context.Background(), record.AgentName)
	if err != nil {
		// Gone, or herdr cannot be asked: nobody is there to push back to,
		// and the report is durable evidence — it settles as it always did.
		return "", "", false
	}
	if !turnEnded(agent.AgentStatus) {
		st.clearIdleSince()
		return subprocess.StateRunning, fmt.Sprintf(
			"the report at %s fails the report check (%d error(s)) and the agent %s is still working (herdr "+
				"reports %s): it may be fixing it", record.ResultPath, len(lint.Errors), record.AgentName, agent.AgentStatus), true
	}
	now := e.now()
	since, ok := st.idleSince()
	if !ok {
		_ = st.markIdleSince(now)
		since = now
	}
	if idle := now.Sub(since); idle < e.opts.IdleGrace {
		return subprocess.StateRunning, fmt.Sprintf(
			"the report at %s fails the report check (%d error(s)) and the agent %s has ended its turn; it is "+
				"pushed back if it stays so for %s", record.ResultPath, len(lint.Errors), record.AgentName, e.opts.IdleGrace), true
	}

	n := sent + 1
	_, err = e.client.AgentPrompt(context.Background(), client.AgentPromptParams{
		Target: record.AgentName,
		Text:   subprocess.LintPushbackPrompt(record.ResultPath, lintCommand(record, roleOf(record)), lint.Text()),
	})
	if err != nil && !client.IsCode(err, client.CodeAgentPromptStalled) {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the report at %s fails the report check, and the pushback could not be delivered "+
				"through herdr (%v); it is re-attempted at the next poll", record.ResultPath, err)})
		return subprocess.StateRunning, "the report fails the report check, and the pushback is re-attempted", true
	}
	_ = st.markLintPushbacks(n)
	st.clearIdleSince()
	_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsStarted,
		Detail: subprocess.LintPushbackDetail(n, fmt.Sprintf("the %s agent %s ended its turn", record.Kind, record.AgentName),
			record.ResultPath, "typed into its own pane "+agent.PaneID, len(lint.Errors))})
	return subprocess.StateRunning, fmt.Sprintf("the agent %s was pushed back (%d of %d) to fix its report",
		record.AgentName, n, subprocess.MaxLintPushbacks), true
}
