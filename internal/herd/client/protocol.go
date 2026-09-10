package client

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is the protocol this package was written against and
// verified against (herdr 0.8.2) — the PIN of the supported range. A server
// newer than the pin is not a problem; see [MinProtocolVersion] for the
// hard floor and [ProtocolWarnVersion] for the warning line.
//
// The pin is the middle of three distinct numbers with three distinct jobs:
// MinProtocolVersion (19) is the oldest this package can speak, this pin (20)
// is the newest it has been verified against, and ProtocolWarnVersion (22)
// is the newest it has observed a live server speaking.
//
// Bumped 19 -> 20 for herdr 0.8.2.
//
// THE 19 -> 20 SHAPE DIFF, taken 2026-08-25 against a live 0.8.2 server rather
// than assumed. `session.snapshot` is identical at the top level — no key
// added, removed or renamed — and every difference below it is ADDITIVE:
// `panes[]` gained `agent`, `agent_session`, `foreground_cwd`; `agents[]`
// gained `agent_session`, `cwd`, `foreground_cwd`, `state_change_seq`. Nothing
// this client decodes disappeared or changed meaning. The captured `pong`
// matches the live one exactly.
//
// That is the evidence the forward-compatible policy below rests on: a newer
// server was observed to only ADD. It is one dated observation against one
// version, not a guarantee — which is why the floor is still a hard stop.
//
// The testdata/ fixtures remain deliberately 0.8.0-era CURATED scenarios (two
// workspaces, two panes, one agent) rather than live dumps: they exist to pin
// decoding of a known shape, and a live capture would churn them every time
// the operator opens a window. Their provenance headers say what they are.
const ProtocolVersion uint32 = 20

// MinProtocolVersion is the OLDEST protocol this package can speak: the floor
// of the supported range and a hard stop below it. A server under the floor
// fails closed with [ProtocolMismatchError]: its response shapes are older
// than anything this package has ever decoded, so a best-effort continue
// would be a guess. The floor moves only when support for an old protocol is
// dropped — it is not the current pin.
//
// It is 19, not 20, because this package demonstrably SPEAKS 19: every
// captured fixture is a 0.8.0-era protocol-19 scenario (the error codes and
// the report_metadata result shape were verified live against herdr 0.8.0 /
// protocol 19), and the 19 -> 20 shape diff recorded above was purely
// ADDITIVE — a protocol-19 server sends a strict subset of the shapes this
// client decodes.
const MinProtocolVersion uint32 = 19

// ProtocolWarnVersion is the NEWEST protocol this package has observed a
// live server speaking without any decoded shape surprising it. A server
// newer than the warn line is still ACCEPTED — there is no hard upper bound:
// a forward-compatible upgrade degrades to a warning via
// [Options.ProtocolWarning], never a refusal and never a stopped run. A
// genuinely incompatible protocol is expected to arrive as a bumped
// MINIMUM, not as a break announced under the same version.
//
// It is 22, not 20, because herdr 0.9.0 / protocol 22 was observed LIVE on
// this machine (the 2026-09-10 0.8.2 -> 0.9.0 upgrade; captured ping:
// version "0.9.0", protocol 22, capabilities live_handoff +
// detached_server_daemon plus three additive newer flags the stock decoder
// already tolerates). Protocol 22 is proven, not assumed; protocol 21,
// unobserved, falls inside the silent band between two observed-compatible
// neighbours.
const ProtocolWarnVersion uint32 = 22

// Method names, exactly as herdr spells them.
const (
	MethodPing              = "ping"
	MethodSessionSnapshot   = "session.snapshot"
	MethodWorktreeCreate    = "worktree.create"
	MethodWorktreeList      = "worktree.list"
	MethodWorktreeRemove    = "worktree.remove"
	MethodWorkspaceFocus    = "workspace.focus"
	MethodAgentStart        = "agent.start"
	MethodAgentPrompt       = "agent.prompt"
	MethodAgentWait         = "agent.wait"
	MethodAgentList         = "agent.list"
	MethodAgentGet          = "agent.get"
	MethodPaneRead          = "pane.read"
	MethodPaneWaitForOutput = "pane.wait_for_output"
	// MethodPaneReportMetadata and MethodWorkspaceReportMetadata are the
	// display-only metadata channels: a source reports title, state labels
	// and tokens for a pane, or tokens for a workspace. Nothing they write
	// is authoritative — herdr renders it and expires it.
	MethodPaneReportMetadata      = "pane.report_metadata"
	MethodWorkspaceReportMetadata = "workspace.report_metadata"
	MethodEventsSubscribe         = "events.subscribe"
	MethodEventsWait              = "events.wait"
	// MethodNotificationShow raises a desktop/toast notification on the
	// operator's foreground herdr client. Like the metadata channel it is
	// display-only — it changes nothing about a pane or an agent — but
	// unlike the metadata channel it is EPHEMERAL and INTERRUPTIVE: there
	// is no TTL and no idempotence, so every call the operator can hear is
	// a separate interruption. Callers must decide not to call it twice.
	MethodNotificationShow = "notification.show"
)

// Result discriminators, the `result.type` value each method answers with.
const (
	resultPong                = "pong"
	resultSessionSnapshot     = "session_snapshot"
	resultWorktreeCreated     = "worktree_created"
	resultWorktreeList        = "worktree_list"
	resultWorktreeRemoved     = "worktree_removed"
	resultWorkspaceInfo       = "workspace_info"
	resultAgentStarted        = "agent_started"
	resultAgentPrompted       = "agent_prompted"
	resultAgentInfo           = "agent_info"
	resultAgentList           = "agent_list"
	resultPaneRead            = "pane_read"
	resultOutputMatched       = "output_matched"
	resultSubscriptionStarted = "subscription_started"
	resultWaitMatched         = "wait_matched"
	resultNotificationShow    = "notification_show"
	// resultOK is the bare acknowledgement the report_metadata methods
	// answer with — verified live against herdr 0.8.0 / protocol 19; they
	// echo no pane or workspace object back.
	resultOK = "ok"
)

// Error codes observed from herdr 0.8.0 / protocol 19. The set is open-ended —
// treat an unrecognised code as an opaque server error rather than a bug.
const (
	// CodeTimeout is returned by the waiting methods (agent.wait,
	// events.wait, pane.wait_for_output) when their deadline elapses.
	CodeTimeout = "timeout"
	// CodeInvalidRequest means the envelope or params failed to parse.
	CodeInvalidRequest = "invalid_request"
	// CodeAgentNotFound means the agent target does not resolve.
	CodeAgentNotFound = "agent_not_found"
	// CodePaneNotFound means the pane id does not resolve.
	CodePaneNotFound = "pane_not_found"
	// CodeWorkspaceNotFound means the workspace id does not resolve.
	CodeWorkspaceNotFound = "workspace_not_found"
	// CodeAgentPaneBusy means agent.start's target pane is not sitting at an
	// available shell prompt. Observed live as a STARTUP RACE: the pane
	// worktree.create hands back is not a usable shell for the first few
	// hundred milliseconds of its life, so an agent.start issued immediately
	// after the create fails with this code and succeeds on a retry. It is
	// therefore transient, not a rejection — see internal/herd/spawn.
	CodeAgentPaneBusy = "agent_pane_busy"
	// CodeAgentPromptStalled means agent.prompt observed no state change
	// after submitting. It is herdr's own detection of the DROPPED FIRST
	// PROMPT documented in skills/ticks/references/herdr-kinds.md: the CLI
	// was still painting its startup UI and never received the text. The
	// documented recovery is to send it again, so this code is transient on
	// a first prompt — see internal/herd/spawn's gate.
	CodeAgentPromptStalled = "agent_prompt_stalled"
)

// Capability names, exactly as herdr spells them in the `capabilities` object
// of a ping reply. A capability is never assumed: the caller requires the
// one it needs, at the point of use, BY NAME ([Client.RequireCapability]) —
// a missing feature is refused there, never silently worked around.
const (
	// CapabilityLiveHandoff: the server supports herdr's live handoff
	// machinery (the `server.live_handoff` method, herdr's `update --handoff`
	// path).
	CapabilityLiveHandoff = "live_handoff"
	// CapabilityDetachedServerDaemon: the server can run detached, as a
	// daemon surviving the client that started it.
	CapabilityDetachedServerDaemon = "detached_server_daemon"
)

// request is the wire request envelope.
type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// response is the wire reply envelope. Exactly one of Result and Error is set.
// On a parse failure the server answers with an empty ID, so callers must not
// require the ID to match before reading Error.
type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *errorBody      `json:"error,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIError is a structured error response from the herdr server.
type APIError struct {
	// Method is the API method that produced the error.
	Method string
	// RequestID is the id the server echoed; empty for envelope parse errors.
	RequestID string
	// Code is the machine-readable error code, e.g. [CodeTimeout].
	Code string
	// Message is herdr's human-readable message.
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("herdr %s failed: %s: %s", e.Method, e.Code, e.Message)
}

// IsTimeout reports whether this error is herdr's wait timeout.
func (e *APIError) IsTimeout() bool { return e != nil && e.Code == CodeTimeout }

// AsAPIError extracts an *APIError from err, if there is one in its chain.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// IsTimeout reports whether err is a herdr wait timeout ([CodeTimeout]). It is
// the check the wait/collect commands want: a timed-out wait is a normal
// outcome, not a transport failure.
func IsTimeout(err error) bool {
	apiErr, ok := AsAPIError(err)
	return ok && apiErr.IsTimeout()
}

// IsCode reports whether err is an [APIError] carrying the given code.
func IsCode(err error, code string) bool {
	apiErr, ok := AsAPIError(err)
	return ok && apiErr.Code == code
}

// ProtocolMismatchError is returned by [New] when the server's protocol is
// below [MinProtocolVersion]. The client fails closed: no call is attempted
// against a server whose wire format it cannot know. The supported range is
// [MinProtocolVersion]..[ProtocolWarnVersion]; the error text names the
// minimum, which is what the server failed to meet.
type ProtocolMismatchError struct {
	// Endpoint is the socket path that was dialled.
	Endpoint string
	// Min is [MinProtocolVersion], the bottom of the supported range.
	Min uint32
	// Actual is the protocol the server reported.
	Actual uint32
	// ServerVersion is the herdr version string the server reported.
	ServerVersion string
}

// Error implements error.
func (e *ProtocolMismatchError) Error() string {
	return fmt.Sprintf(
		"herd/client: herdr protocol mismatch at %s: server reports protocol %d (herdr %s), this client requires at least protocol %d — refusing to continue",
		e.Endpoint, e.Actual, e.ServerVersion, e.Min,
	)
}

// CapabilityError is returned by [Client.RequireCapability] when the server
// did not advertise the named capability. The refusal names the capability
// BY NAME, plus the herdr version and endpoint, so it reads as "this herdr
// is too old for this operation", never as a mystery failure.
//
// Like [ProtocolMismatchError], it is OPERATIONAL: the operation cannot be
// attempted, so it is refused up front. It is never evidence that work
// already submitted was judged failed — verdicts live in durable evidence,
// never in the transport (the av8 contract).
type CapabilityError struct {
	// Capability is the required name, e.g. [CapabilityLiveHandoff].
	Capability string
	// Endpoint is the socket path that was dialled.
	Endpoint string
	// ServerVersion is the herdr version string the server reported.
	ServerVersion string
	// Protocol is the protocol the server reported.
	Protocol uint32
}

// Error implements error. It names the capability, the herdr version and the
// endpoint — everything needed to tell the user which herdr upgrade removes
// the refusal.
func (e *CapabilityError) Error() string {
	return fmt.Sprintf(
		"herd/client: herdr at %s (herdr %s, protocol %d) does not advertise capability %q — refusing to continue",
		e.Endpoint, e.ServerVersion, e.Protocol, e.Capability,
	)
}

// UnexpectedResultError means the server answered a method with a result
// discriminator this client does not expect — a symptom of protocol drift
// that slipped past the version check.
type UnexpectedResultError struct {
	// Method is the API method that was called.
	Method string
	// Want is the result.type this client expected.
	Want string
	// Got is the result.type the server sent.
	Got string
}

// Error implements error.
func (e *UnexpectedResultError) Error() string {
	return fmt.Sprintf("herd/client: %s returned result type %q, expected %q", e.Method, e.Got, e.Want)
}
