package herdr

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// names: an agent name herdr says is taken is a question with three answers,
// never an unclassified stop.
//
// herdr refuses agent.start over a name another pane already holds
// (agent_name_taken). The run stopped on exactly that in epic-6in: the repair
// job for 4i8 attempt 3 was given the attempt's own name, the implement
// attempt's pane was still there, and the run halted on a gate failure it had
// a repair for. Job-scoped names (jobAgentName) are the fix for the cause;
// this file is what a taken name means when it happens anyway:
//
//   - the holder is THIS attempt's own pane: an earlier agent.start of this
//     very launch landed (a reply lost to a timeout, a restart). It is
//     adopted — launching a second agent over it is how a job pays twice.
//   - the holder is a SETTLED job of this run — its branch is in the run's
//     own write namespace, and herdr says its agent is neither working nor
//     blocked on a person: a pane a teardown missed. Its pane is closed
//     (herdr opens a shell in its place; the workspace, the worktree and the
//     branch all stay, for the teardown that owns them) and the launch is
//     retried once.
//   - anything else — a working or blocked agent, a pane outside this run, a
//     holder herdr will not describe — is refused with RefusedAgentNameTaken,
//     naming the holder, so the stop says what holds the name and why it was
//     not touched.

// RefusedAgentNameTaken: the herdr agent name this job launches under is held
// by an agent this executor may not close — live, blocked on a person, or not
// a job of this run.
const RefusedAgentNameTaken = "agent_name_taken"

// resolveNameTaken answers one agent_name_taken on record's launch. A non-nil
// AgentStarted is the adopted launch; nil with no error means the stale holder
// is gone and the launch may be retried.
func (e *Executor) resolveNameTaken(ctx context.Context, st *store, record *attemptRecord,
	taken error) (*client.AgentStarted, error) {

	holder, err := e.client.AgentGet(ctx, record.AgentName)
	switch {
	case err != nil && gone(err):
		// Freed between the refusal and the question: retry.
		return nil, nil
	case err != nil:
		return nil, refuse(RefusedAgentNameTaken,
			"herdr agent.start for %s answered that the name is taken (%v), and herdr could not be asked who "+
				"holds it (%v): nothing is closed on an answer nobody gave", record.AgentName, taken, err)
	}

	if holder.PaneID == record.PaneID {
		// Our own launch landed behind the refusal: adopt it.
		if obsErr := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("herdr answered that the name %s is taken, by this attempt's own pane %s: "+
				"an earlier launch of it landed, and it is adopted rather than launched twice",
				record.AgentName, record.PaneID)}); obsErr != nil {
			return nil, fmt.Errorf("record the adopted launch: %w", obsErr)
		}
		if holder.InteractiveReady {
			return &client.AgentStarted{Agent: *holder}, nil
		}
		return e.waitInteractiveReady(ctx, st, record)
	}

	branch, known, err := e.holderBranch(ctx, holder.WorkspaceID)
	if err != nil {
		return nil, refuse(RefusedAgentNameTaken,
			"the herdr agent name %s is held by the agent in pane %s (workspace %s), and herdr could not be asked "+
				"what that workspace is (%v): it is not closed on a guess", record.AgentName, holder.PaneID,
			holder.WorkspaceID, err)
	}
	namespace := runNamespace(record)
	holderDesc := fmt.Sprintf("the agent in pane %s (workspace %s, %s)", holder.PaneID, holder.WorkspaceID,
		describeBranch(branch, known))
	switch {
	case namespace == "" || !known || !strings.HasPrefix(branch, namespace):
		return nil, refuse(RefusedAgentNameTaken,
			"the herdr agent name %s is held by %s, which is not a job of this run (its write namespace is %s): "+
				"it is not this executor's to close. Close or rename that agent, and dispatch again",
			record.AgentName, holderDesc, orNone(namespace))
	case branch == record.Branch:
		return nil, refuse(RefusedAgentNameTaken,
			"the herdr agent name %s is held by %s, which holds this job's own branch in another workspace: "+
				"two workspaces claim one job, and neither is closed on a guess", record.AgentName, holderDesc)
	case holder.AgentStatus == client.StatusWorking || holder.AgentStatus == client.StatusBlocked:
		return nil, refuse(RefusedAgentNameTaken,
			"the herdr agent name %s is held by %s, a job of this run whose agent is %s: it may be mid-turn or "+
				"waiting on a person, so it is not closed", record.AgentName, holderDesc, holder.AgentStatus)
	}

	// A settled job of this run whose pane a teardown missed. The pane goes;
	// the workspace, its worktree and the branch stay for the teardown that
	// owns them.
	if err := e.client.PaneClose(ctx, client.PaneCloseParams{PaneID: holder.PaneID}); err != nil &&
		!client.IsCode(err, client.CodePaneNotFound) {
		return nil, refuse(RefusedAgentNameTaken,
			"the herdr agent name %s is held by %s, a settled job of this run, and its pane could not be closed "+
				"(%v)", record.AgentName, holderDesc, err)
	}
	if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
		Detail: fmt.Sprintf("the herdr agent name %s was held by %s, a settled job of this run (agent %s): its pane "+
			"was closed and the launch is retried once", record.AgentName, holderDesc, holder.AgentStatus)}); err != nil {
		return nil, fmt.Errorf("record the stale holder's close: %w", err)
	}
	// herdr opens a shell where the pane was; the NAME is free only once
	// herdr answers the agent is gone.
	deadline := e.now().Add(e.opts.StartupTimeout)
	for {
		_, err := e.client.AgentGet(ctx, record.AgentName)
		if err != nil && gone(err) {
			return nil, nil
		}
		if !e.now().Before(deadline) {
			return nil, refuse(RefusedAgentNameTaken,
				"the herdr agent name %s was held by %s; its pane was closed, but herdr still answers the name "+
					"within the startup budget of %s", record.AgentName, holderDesc, e.opts.StartupTimeout)
		}
		e.sleepUntil(readinessPollInterval)
	}
}

// holderBranch is the branch herdr says the workspace's worktree holds, and
// whether herdr named one at all.
func (e *Executor) holderBranch(ctx context.Context, workspaceID string) (string, bool, error) {
	if workspaceID == "" {
		return "", false, nil
	}
	sv, err := e.survey(ctx)
	if err != nil {
		return "", false, err
	}
	for _, wt := range sv.listing.Worktrees {
		if wt.OpenWorkspaceID != nil && *wt.OpenWorkspaceID == workspaceID && wt.Branch != nil {
			return strings.TrimPrefix(*wt.Branch, "refs/heads/"), true, nil
		}
	}
	return "", false, nil
}

// runNamespace is the branch prefix this job's grant may write — per run, so
// a branch under it is a job of THIS run — or "" when the grant names none.
func runNamespace(record *attemptRecord) string {
	if record.Spec == nil {
		return ""
	}
	return strings.TrimPrefix(record.Spec.Credentials.Source.WriteRefPrefix(), "refs/heads/")
}

func describeBranch(branch string, known bool) string {
	if !known {
		return "on no branch herdr would name"
	}
	return "on branch " + branch
}

func orNone(s string) string {
	if s == "" {
		return "not stated by the grant"
	}
	return s
}

// relaunchRefusedLaunch relaunches an attempt whose agent.start herdr refused
// over a name ANOTHER workspace's agent holds. Such an attempt never started:
// herdr launched nothing in its pane and no prompt was ever submitted (the
// prompt follows a confirmed launch), so launching it now — in its own
// workspace, under its job's own name — is the start A6 permits, not a
// redispatch over live work. The evidence is herdr's, next to the act: the
// recorded name answers from a DIFFERENT workspace, and this attempt's own
// workspace is still where the record says, by its worktree or branch.
//
// It is how an attempt the pre-fix naming stranded resumes (epic-6in's
// repair-3 record carries its implement attempt's name): any other shape
// returns relaunched=false and Start decides as it always did.
func (e *Executor) relaunchRefusedLaunch(st *store, record *attemptRecord, spec *subprocess.JobSpec) (
	*subprocess.JobHandle, bool, error) {

	if record.LaunchConfirmed || record.WorkspaceID == "" || record.PaneID == "" {
		return nil, false, nil
	}
	if _, cancelled := st.cancelled(); cancelled || st.agentGone() || st.idleSettled() {
		return nil, false, nil
	}
	if report, has := e.readReport(record); has && report.Status != "" {
		return nil, false, nil
	}
	ctx := context.Background()
	holder, err := e.client.AgentGet(ctx, record.AgentName)
	if err != nil || holder.WorkspaceID == "" || holder.WorkspaceID == record.WorkspaceID ||
		holder.PaneID == record.PaneID {
		return nil, false, nil
	}
	sv, err := e.survey(ctx)
	if err != nil {
		return nil, false, nil
	}
	if att := sv.attribute(record, record.WorkspaceID); att.id != record.WorkspaceID {
		return nil, false, nil
	}
	if err := e.enforceableWall(spec); err != nil {
		return nil, true, err
	}

	previous := record.AgentName
	record.AgentName = jobAgentName(record.Spec, record.Attempt)
	if err := st.writeAttempt(record); err != nil {
		return nil, true, err
	}
	if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
		Detail: fmt.Sprintf("the launch under %s was refused by herdr: the name is held by the agent in pane %s "+
			"(workspace %s), not by this attempt's pane %s. Nothing ran here and no prompt was sent, so the "+
			"attempt is launched in its own workspace %s under its job's own name %s",
			previous, holder.PaneID, holder.WorkspaceID, record.PaneID, record.WorkspaceID, record.AgentName)}); err != nil {
		return nil, true, fmt.Errorf("record the relaunch: %w", err)
	}
	if _, err := e.startAgent(ctx, st, record); err != nil {
		return nil, true, err
	}
	record.LaunchConfirmed = true
	if err := st.writeAttempt(record); err != nil {
		return nil, true, err
	}
	if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsStarted,
		Detail: fmt.Sprintf("agent %s (%s) started in pane %s, workspace %s, worktree %s",
			record.AgentName, record.Kind, record.PaneID, record.WorkspaceID, record.Worktree)}); err != nil {
		return nil, true, fmt.Errorf("record the launch: %w", err)
	}
	e.gate(ctx, st, record)
	return handleFor(record), true, nil
}
