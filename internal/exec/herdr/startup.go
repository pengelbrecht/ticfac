package herdr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// A pane that never starts (tick o35).
//
// The launch can fail in a way no retry of agent.start fixes: herdr typed the
// agent's command, the agent came up, and it is sitting at a dialog of its
// own — claude's folder-trust "Quick safety check" in a worktree it has never
// seen, an external-imports question, an API-key confirmation. herdr reports
// such an agent live but blocked and never interactive_ready, the startup
// budget runs out, and before this file the run saw only "never reported
// interactive_ready", stopped, and on resume read the pane live and held
// for a person.
//
// Three things change here. The failure is DIAGNOSED: the pane is read and a
// recognised dialog is named, with the worktree it is in ("claude is waiting
// at its folder-trust dialog in <path>"). The pane is RE-DISPATCHED once: the
// dialog is dismissed (Escape cancels every claude dialog; ctrl+c twice
// exits claude from anywhere), trust is registered again, and the launch is
// repeated in the same pane. And when that also fails the error is a
// *PaneStartError, which the dispatching caller (internal/cli) answers by
// abandoning the never-prompted pane (AbandonUnstartedLaunch) and running the
// job on the headless local runner instead.
//
// A pane is only ever called stuck on herdr's positive answer, after the
// whole startup budget was spent: an agent IS in the pane, it is not
// working, it is not ready, and its launch is not pending. A launch whose
// outcome is unknown (a client-side timeout, an unanswered call) is never
// read as stuck — that is the case liveness must be asked, not assumed
// (TestALaunchPastTheCallBoundProducesNoVerdictAndNoTeardown).

// StartupTimeoutEnv overrides DefaultStartupTimeout for every herdr dispatch
// this process makes: a Go duration ("3m", "90s") or a whole number of
// seconds. It is an environment variable, like the executor's other host
// knobs (TICFAC_EXEC_STATE_DIR, HERDR_SOCKET_PATH), because the startup
// budget is a fact about this host — how fast its agents boot — and not
// about the repository whose runners.toml a run reads.
const StartupTimeoutEnv = "TICFAC_HERDR_STARTUP_TIMEOUT"

// StartupTimeoutFromEnv reads StartupTimeoutEnv: zero when it is unset, an
// error naming the variable when it is set to anything but a positive
// duration — a typo silently replaced by the default is a budget nobody set.
func StartupTimeoutFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(StartupTimeoutEnv))
	if raw == "" {
		return 0, nil
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0, fmt.Errorf("%s=%q: the startup budget must be positive", StartupTimeoutEnv, raw)
		}
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s=%q is not a positive duration (\"3m\", \"90s\", or whole seconds)",
			StartupTimeoutEnv, raw)
	}
	return d, nil
}

// The dialogs a stuck pane is recognised as.
const (
	DialogFolderTrust     = "folder-trust"
	DialogExternalImports = "external-imports"
	DialogAPIKey          = "api-key"
	DialogBypassWarning   = "bypass-permissions"
	DialogUnrecognised    = "unrecognised"
)

// dialogSignatures are the texts that identify each dialog on the pane, as
// Claude Code 2.1.296 renders them (read off a pty in a scratch HOME). Matched
// case-insensitively; the first match wins.
var dialogSignatures = []struct {
	dialog  string
	phrases []string
	name    string
}{
	{DialogFolderTrust, []string{"quick safety check", "yes, i trust this folder", "do you trust the files in this folder"},
		"its folder-trust dialog"},
	{DialogExternalImports, []string{"allow external claude.md file imports"},
		"its external CLAUDE.md imports dialog"},
	{DialogAPIKey, []string{"do you want to use this api key"},
		"its API-key confirmation"},
	{DialogBypassWarning, []string{"bypass permissions mode"},
		"its bypass-permissions warning"},
}

// classifyScreen names the dialog a pane's visible text shows, or "" when it
// shows none this build recognises.
func classifyScreen(text string) (dialog, name string) {
	lower := strings.ToLower(text)
	for _, sig := range dialogSignatures {
		for _, phrase := range sig.phrases {
			if strings.Contains(lower, phrase) {
				return sig.dialog, sig.name
			}
		}
	}
	return "", ""
}

// PaneStartError is a herdr pane whose agent never became ready: herdr
// answers an agent is there, not working and not ready, after the whole
// startup budget. It is the one start failure the dispatching caller may
// answer with the headless runner, because the attempt it belongs to never
// received a prompt — nothing ran.
type PaneStartError struct {
	Kind     string
	Agent    string
	PaneID   string
	Worktree string
	// Dialog is the recognised dialog (Dialog*), DialogUnrecognised for a
	// blocked agent showing none this build knows, or "" for one herdr
	// reports neither blocked nor ready.
	Dialog string
	// DialogName is the dialog in words ("its folder-trust dialog").
	DialogName string
	Status     client.AgentStatus
	// Screen is the last lines of the pane, for the operator.
	Screen string
	Budget time.Duration
	// Redispatched says the launch was already repeated once in the pane.
	Redispatched bool
	// Resumed says this is a later incarnation finding the earlier one's
	// launch stuck, not a launch this call made.
	Resumed bool
	Err     error
}

func (e *PaneStartError) Error() string {
	kind := e.Kind
	if kind == "" {
		kind = "the agent"
	}
	var what string
	switch {
	case e.DialogName != "":
		what = fmt.Sprintf("%s is waiting at %s in %s", kind, e.DialogName, e.Worktree)
	case e.Status == client.StatusBlocked:
		what = fmt.Sprintf("%s is blocked at a prompt in %s (herdr reports it blocked, never interactive_ready)",
			kind, e.Worktree)
	default:
		what = fmt.Sprintf("%s in %s never became ready (herdr reports it %s, never interactive_ready)",
			kind, e.Worktree, orUnknown(string(e.Status)))
	}
	msg := fmt.Sprintf("%s: agent %s in pane %s did not start within the startup budget of %s", what, e.Agent,
		e.PaneID, e.Budget)
	if e.Redispatched {
		msg += ", and again after it was re-dispatched once in the same pane"
	}
	if e.Resumed {
		msg += " (found so by a later incarnation; the launch was never confirmed and no prompt was sent)"
	}
	if e.Screen != "" && e.DialogName == "" {
		msg += fmt.Sprintf("; the pane shows: %q", e.Screen)
	}
	if e.Err != nil {
		msg += fmt.Sprintf(" (%v)", e.Err)
	}
	return msg
}

func (e *PaneStartError) Unwrap() error { return e.Err }

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// AsPaneStartError recovers a *PaneStartError from a start's error chain.
func AsPaneStartError(err error) (*PaneStartError, bool) {
	var pse *PaneStartError
	if errors.As(err, &pse) {
		return pse, true
	}
	return nil, false
}

// paneStartFailure turns a launch failure into a *PaneStartError when — and
// only when — the whole budget was spent (began is when the launch started)
// and herdr positively answers the pane's agent is stuck. Any other failure
// is returned unchanged.
func (e *Executor) paneStartFailure(ctx context.Context, record *attemptRecord, began time.Time, err error) error {
	if err == nil {
		return nil
	}
	if _, refused := subprocess.AsRefusal(err); refused {
		return err
	}
	if e.now().Sub(began) < e.opts.StartupTimeout {
		return err
	}
	if pse := e.stuckPane(ctx, record); pse != nil {
		pse.Err = err
		return pse
	}
	return err
}

// stuckPane asks herdr about the attempt's agent and, when herdr answers it
// is there, not working, not ready and not pending, reads the pane to name
// what it is waiting at. nil means "not stuck, or nobody can say".
func (e *Executor) stuckPane(ctx context.Context, record *attemptRecord) *PaneStartError {
	agent, err := e.client.AgentGet(ctx, record.AgentName)
	if err != nil && record.PaneID != "" {
		agent, err = e.client.AgentGet(ctx, record.PaneID)
	}
	if err != nil || agent == nil {
		return nil
	}
	if agent.InteractiveReady || agent.LaunchPending || agent.AgentStatus == client.StatusWorking {
		return nil
	}
	pse := &PaneStartError{
		Kind: record.Kind, Agent: record.AgentName, PaneID: record.PaneID, Worktree: record.Worktree,
		Status: agent.AgentStatus, Budget: e.opts.StartupTimeout,
	}
	lines := uint32(40)
	read, readErr := e.client.PaneRead(ctx, client.PaneReadParams{
		PaneID: record.PaneID, Source: client.SourceVisible, Format: client.FormatText, Lines: &lines,
	})
	if readErr == nil && read != nil {
		pse.Dialog, pse.DialogName = classifyScreen(read.Text)
		pse.Screen = lastLines(read.Text, 3)
	}
	if pse.Dialog == "" && agent.AgentStatus == client.StatusBlocked {
		pse.Dialog = DialogUnrecognised
	}
	return pse
}

// stuckUnconfirmedLaunch is the resume half: an attempt record whose launch
// was never confirmed, that nothing cancelled or settled and that wrote no
// report, whose agent herdr answers is stuck. nil for every other shape.
func (e *Executor) stuckUnconfirmedLaunch(st *store, record *attemptRecord) *PaneStartError {
	if record.LaunchConfirmed || record.PaneID == "" {
		return nil
	}
	if _, cancelled := st.cancelled(); cancelled || st.agentGone() || st.idleSettled() || st.stuckStopped() {
		return nil
	}
	if report, has := e.readReport(record); has && report.Status != "" {
		return nil
	}
	pse := e.stuckPane(context.Background(), record)
	if pse != nil {
		pse.Resumed = true
	}
	return pse
}

// lastLines is the last n non-blank lines of a pane's text, joined.
func lastLines(text string, n int) string {
	var kept []string
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			kept = append([]string{line}, kept...)
		}
	}
	return strings.Join(kept, " / ")
}

// clearStuckPane takes a stuck agent out of its pane so the launch can be
// repeated there: Escape (which cancels every claude dialog — at the trust
// dialog it is "No, exit"), then ctrl+c twice (claude's exit chord, and a
// harmless empty line to the shell if the agent has already gone). Every
// failure is an observation: the repeated launch is what finds out whether
// the pane is free.
func (e *Executor) clearStuckPane(ctx context.Context, st *store, record *attemptRecord) {
	for _, keys := range [][]string{{"esc"}, {"ctrl+c"}, {"ctrl+c"}} {
		if _, err := e.client.AgentSendKeys(ctx, client.AgentSendKeysParams{Target: record.PaneID, Keys: keys}); err != nil {
			_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
				Detail: fmt.Sprintf("clearing the stuck pane %s: send %v: %v", record.PaneID, keys, err)})
		}
		e.sleepUntil(clearKeyInterval)
	}
}

// clearKeyInterval spaces the keys that clear a stuck pane, so each lands on
// the screen the previous one left.
const clearKeyInterval = 300 * time.Millisecond

// registerTrust marks the attempt's worktree trusted in Claude Code's config
// before claude is launched in it (trust.go). It is for claude only, and only
// for the worktree THIS executor created for the attempt — which is the only
// worktree an attempt record names. A failure is an observation, never a
// failed launch: the launch still goes ahead, and a pane that then stops at
// the dialog is diagnosed precisely.
func (e *Executor) registerTrust(st *store, record *attemptRecord) {
	if record.Kind != "claude" || record.Worktree == "" || e.opts.ClaudeConfigPath == "" {
		return
	}
	if err := RegisterClaudeTrust(e.opts.ClaudeConfigPath, record.Worktree); err != nil {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("could not register Claude Code folder trust for %s (%v): claude may stop at its "+
				"folder-trust dialog", record.Worktree, err)})
	}
}

// RefusedLaunchConfirmed: AbandonUnstartedLaunch was asked to abandon an
// attempt whose launch WAS confirmed — it may have been prompted, so it is
// not this executor's to throw away.
const RefusedLaunchConfirmed = "launch_confirmed"

// AbandonUnstartedLaunch tears down the pane, the workspace, the worktree
// and the branch of THIS attempt (spec's job, the executor's attempt number)
// when its launch was never confirmed — so no prompt was ever submitted and
// nothing ran — and herdr answers its agent is not working. It is how the
// dispatching caller frees the attempt's branch to run the same job on
// another runner. The branch is deleted only when it carries no commit
// beyond the attempt's base; otherwise it is kept and the abandonment is
// refused, because something wrote there.
func (e *Executor) AbandonUnstartedLaunch(spec *subprocess.JobSpec, reason string) error {
	st := e.storeAt(e.stateDirFor(spec.JobID, e.opts.Attempt))
	record, err := st.readAttempt()
	if err != nil {
		return fmt.Errorf("abandon %s attempt %d: no attempt record: %w", spec.JobID, e.opts.Attempt, err)
	}
	if record.LaunchConfirmed {
		return refuse(RefusedLaunchConfirmed,
			"attempt %d of %s was launched and may have been prompted: it is not abandoned", record.Attempt, record.JobID)
	}
	ctx := context.Background()
	if agent, err := e.client.AgentGet(ctx, record.AgentName); err == nil && agent.AgentStatus == client.StatusWorking {
		return refuse(subprocess.RefusedLive,
			"the agent %s of attempt %d of %s is working: it is not abandoned", record.AgentName, record.Attempt, record.JobID)
	}
	head := headOf(record.Repo, record.Branch)
	if head != "" && head != record.BaseSHA {
		if n, err := commitsBeyond(record.Repo, record.BaseSHA, head); err != nil || n > 0 {
			return refuse(subprocess.RefusedBranchUnsafe,
				"branch %s of the never-prompted attempt %d of %s carries commits beyond its base: it is not abandoned",
				record.Branch, record.Attempt, record.JobID)
		}
	}
	if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
		Detail: fmt.Sprintf("abandoning the never-prompted launch in pane %s: %s", record.PaneID, reason)}); err != nil {
		return fmt.Errorf("record the abandonment: %w", err)
	}
	if record.PaneID != "" {
		if err := e.client.PaneClose(ctx, client.PaneCloseParams{PaneID: record.PaneID}); err != nil &&
			!client.IsCode(err, client.CodePaneNotFound) && !gone(err) {
			return fmt.Errorf("abandon %s: close pane %s: %w", record.JobID, record.PaneID, err)
		}
	}
	if err := e.Dispose(handleFor(record), subprocess.DisposeOptions{
		Reason: "the launch never started (" + reason + ")", KeepBranch: true,
	}); err != nil {
		return fmt.Errorf("abandon %s: %w", record.JobID, err)
	}
	if branchExists(record.Repo, record.Branch) {
		if err := branchDelete(record.Repo, record.Branch); err != nil {
			return fmt.Errorf("abandon %s: delete branch %s: %w", record.JobID, record.Branch, err)
		}
	}
	return nil
}
