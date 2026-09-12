package herdr

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// THE FIRST-ROUND-TRIP GATE (tick x9x).
//
// One question: did this dispatch reach an agent that can do work? The gate
// observes the answer over the PROTOCOL — herdr's own prompt submission and
// herdr's own event-driven wait for `working` — and it claims ONLY what it
// saw. The failure mode this gate exists to prevent is the spawn error that
// asserts a cause nobody observed ("the agent started clean and cannot do
// work: stale model string, auth or quota"): a finding here names the
// observation — "the submission was refused", "no working was observed
// within Ns", "the read came back truncated" — and offers no causes at all.
// Auth, quota and a stale model string are hypotheses; a hypothesis is
// offered as such or not at all, and nothing in this file offers one.
//
// The gate NEVER fails a spawn. Whatever it finds, Start has already
// launched the agent and the dispatch is a recorded fact; a gate finding is
// a fact about the DISPATCH, and the closed failure vocabulary of the
// collect path is minted only from durable evidence — the report, the
// branch, this executor's own settlement records. A dispatch that never
// visibly reached the agent is exactly what a later wait or a person
// resolves; it is never "the agent cannot work", because a rendering or
// truncation change must not be able to fake that finding (x6j's line).
//
// The three outcomes the tick names are three DIFFERENT findings, recorded
// on the attempt, each stating only what was observed:
//
//   - not_delivered — the prompt was not observed to land: herdr refused
//     the submission outright, or (when delivery was uncertain) a complete
//     pane read shows no trace of the prompt.
//   - read_truncated — the last-resort pane read came back truncated: a
//     read that merely cut off the echo is indistinguishable from a prompt
//     that never arrived, and the two are never one classification.
//   - unexpected_answer — the prompt was observed to land, and the agent
//     was not observed to enter `working` within the budget: the agent's
//     answer was not the expected work.
//
// The pane read is the LAST resort, in the tick's exact sense: it runs only
// when the submission itself left delivery uncertain (herdr answered
// agent_prompt_stalled — no state change after submitting, which a worker
// that answers in under a second also produces). It honours the read's
// Truncated flag as its own finding, and it is structurally incapable of
// being the sole basis for a hard failure: the gate has no hard failure to
// hand out. It matches the prompt's echo — the report filename, the one
// string only the prompt carries — in herdr's unwrapped recent output,
// never any answer word an agent was trusted to type.

// The gate findings, a closed vocabulary. These are recorded on the attempt
// record and are findings about the dispatch, never verdicts about the work.
const (
	// GateConfirmed: the wait observed the agent enter `working` within the
	// confirmation budget.
	GateConfirmed = "confirmed"
	// GateNotDelivered: the prompt was not observed to land.
	GateNotDelivered = "not_delivered"
	// GateReadTruncated: the last-resort pane read came back truncated;
	// whether the prompt landed is indistinguishable from absent.
	GateReadTruncated = "read_truncated"
	// GateUnexpectedAnswer: the prompt was observed to land and no `working`
	// was observed within the budget — the agent's answer was not the
	// expected work.
	GateUnexpectedAnswer = "unexpected_answer"
	// GateUnconfirmed: the gate could observe neither delivery nor the
	// agent's answer.
	GateUnconfirmed = "unconfirmed"
)

// gateReadLines caps the last-resort pane read: wide enough to cover the
// delivered prompt plus a screen or two of what the agent has rendered since,
// and a window herdr will flag Truncated when it cuts anything off — which is
// precisely the flag the gate exists to honour.
const gateReadLines = 200

// gate delivers the worker prompt and confirms the dispatch.
//
// The confirmation is the spawn lesson that is NOT fire-and-forget: returning
// the instant the submission is accepted leaves the worker in the settled
// state the launch left it in, and a wait moments later resolves it as
// already settled — a wave that fans in before any work starts. So the gate
// waits once, over the protocol, for the agent to reach `working`. A
// confirmation that times out is reported through the observation log and
// DispatchConfirmed=false rather than failing the spawn: a trivial tick can
// finish before `working` is ever rendered, and an unconfirmed dispatch is a
// fact, not a verdict.
func (e *Executor) gate(ctx context.Context, st *store, record *attemptRecord) {
	prompt := renderWorkerPrompt(record, record.Spec)
	record.DispatchConfirmed = false
	record.DispatchGate = GateUnconfirmed

	// ---- delivery ---------------------------------------------------------
	_, err := e.client.AgentPrompt(ctx, client.AgentPromptParams{
		Target: record.AgentName,
		Text:   prompt,
	})
	switch {
	case err == nil:
		// herdr accepted the prompt. Delivery is the one thing observed
		// here; what the agent does with it is the wait's question.
	case client.IsCode(err, client.CodeAgentPromptStalled):
		// herdr saw no state change after submitting — which a worker that
		// answered in under a second also produces. Delivery stays
		// uncertain; the last-resort read below is what settles it, and
		// only if the wait cannot.
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "herdr answered agent_prompt_stalled — no state change after submitting, " +
				"which a worker that answers in under a second also produces; whether the prompt " +
				"landed is left for the gate to judge"})
	default:
		// herdr refused the submission: the prompt was not delivered. This
		// is herdr's own answer, an observation about the dispatch — never
		// a verdict about the agent, and never a diagnosis of why.
		record.DispatchGate = GateNotDelivered
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the prompt submission was refused: " + err.Error() +
				"; the dispatch is recorded not delivered — an observation about the dispatch, " +
				"never a verdict about the agent"})
		e.recordGate(st, record)
		return
	}

	// ---- the acknowledgement, over the protocol ---------------------------
	waitCtx, cancel := context.WithTimeout(ctx, e.opts.ConfirmTimeout)
	defer cancel()
	_, waitErr := e.client.AgentWait(waitCtx, client.AgentWaitParams{
		Target:  record.AgentName,
		Until:   []client.AgentStatus{client.StatusWorking},
		Timeout: e.opts.ConfirmTimeout,
	})
	if waitErr == nil {
		record.DispatchConfirmed = true
		record.DispatchGate = GateConfirmed
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the agent entered working: the dispatch is confirmed"})
		e.recordGate(st, record)
		return
	}

	// ---- no acknowledgement within the budget ------------------------------
	if err == nil {
		// Delivery was observed (herdr accepted the prompt); the wait is
		// the only open question. No pane read is needed to answer a
		// question already answered by the protocol.
		switch {
		case client.IsTimeout(waitErr):
			record.DispatchGate = GateUnexpectedAnswer
			_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
				Detail: "the prompt was accepted and no working was observed within " +
					e.opts.ConfirmTimeout.String() + ": the agent's answer was not the expected work. " +
					"The dispatch is recorded unconfirmed — a trivial tick can finish before working " +
					"is rendered. No cause is observed; none is asserted."})
		default:
			record.DispatchGate = GateUnconfirmed
			_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
				Detail: "the confirmation wait answered " + waitErr.Error() +
					"; the dispatch is recorded unconfirmed — a fact, not a failure"})
		}
		e.recordGate(st, record)
		return
	}

	// Delivery itself is uncertain (the stalled submission) and the wait
	// could not confirm the agent's answer either way. The LAST RESORT is
	// one pane read over the protocol: does the delivered prompt's echo —
	// the report filename, the one string only the prompt carries — appear
	// in the pane's recent output?
	read, readErr := e.client.PaneRead(ctx, client.PaneReadParams{
		PaneID: record.PaneID,
		Source: client.SourceRecentUnwrapped,
		Lines:  client.Ptr(uint32(gateReadLines)),
	})
	switch {
	case readErr != nil:
		record.DispatchGate = GateUnconfirmed
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the last-resort pane read answered " + readErr.Error() +
				"; whether the prompt landed stays unobserved. The dispatch is recorded " +
				"unconfirmed — a fact, not a failure"})
	case read.Truncated:
		// Truncated is honoured as its own finding: a read that merely cut
		// off the echo is indistinguishable from a prompt that never
		// arrived, and the two must never be one classification. A
		// truncated read is never a failed agent.
		record.DispatchGate = GateReadTruncated
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the last-resort pane read came back truncated: whether the prompt landed " +
				"is indistinguishable from a prompt that never arrived. Recorded as an observation " +
				"— a truncated read is never a failed agent"})
	case paneEcho(read.Text, record):
		record.DispatchGate = GateUnexpectedAnswer
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the prompt is in the pane's recent output (a complete read) and no working " +
				"was observed within " + e.opts.ConfirmTimeout.String() + ": the agent's answer was " +
				"not the expected work. The dispatch is recorded unconfirmed — a fact, not a failure"})
	default:
		record.DispatchGate = GateNotDelivered
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: "the pane's recent output (a complete read) shows no trace of the prompt; " +
				"the dispatch is recorded not delivered — an observation about the dispatch, " +
				"never a verdict about the agent"})
	}
	e.recordGate(st, record)
}

// recordGate persists the gate's finding. The launch is already durable; the
// finding is a recorded observation and its absence is a fact, not a failure.
// A write failure here is still reported: a record that did not land is a job
// nobody can find.
func (e *Executor) recordGate(st *store, record *attemptRecord) {
	if err := st.writeAttempt(record); err != nil {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: "the dispatch gate's finding could not be recorded: " + err.Error()})
	}
}

// paneEcho reports whether the delivered prompt is in the pane's recent
// output. The needle is the report FILENAME — the one string only the worker
// prompt carries: the agent cannot know the executor-owned path from anywhere
// else, so a false positive would still mean the prompt was delivered. Both
// sides are whitespace-collapsed, so a re-wrapped render of the prompt still
// matches; the read's own Truncated flag, not a missing needle, is what says
// the window cut something off.
func paneEcho(text string, record *attemptRecord) bool {
	needle := collapseWhitespace(filepath.Base(record.ResultRel))
	if needle == "" {
		return false
	}
	return strings.Contains(collapseWhitespace(text), needle)
}

// collapseWhitespace reduces every run of whitespace to one space, so text
// matched across a pane's re-wrapping compares by words, not by lines.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
