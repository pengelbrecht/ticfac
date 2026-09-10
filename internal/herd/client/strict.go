package client

// STRICT DECODE — the policy (tick bcv).
//
// Stock encoding/json decodes into a fixed struct by NAME MATCH: an added
// field is dropped, and a RENAMED field silently becomes a zero value. For
// most of this contract a silent zero is a degraded answer. For four
// AgentInfo fields it INVERTS a safety property of the orchestrator built
// on this client:
//
//   - agent_status renamed -> "" -> non-terminal -> the wave fan-in never
//     completes: a 30-second wait becomes a 30-minute hang.
//   - interactive_ready renamed -> false -> readiness never observed ->
//     every spawn fails as a startup race.
//   - name renamed -> nil -> every live worker classifies as dead ->
//     redispatch onto a tick that already has a running worker.
//   - agent_session renamed -> nil -> recovery silently downgrades from
//     native resume to redispatch.
//
// The mechanism chosen here is a VALIDATE STEP, not DisallowUnknownFields and
// not pointer fields, and the difference is deliberate:
//
//   - DisallowUnknownFields would also reject ADDED fields, breaking the
//     forward-compatibility policy in protocol.go (a newer herdr may only
//     add). Unknown keys stay tolerated; the tests pin that.
//   - Pointer fields would push presence into every consumer and make nil a
//     third state to mishandle at each use site. The struct shapes stay as
//     tick 579 lifted them; the strictness lives in one place, here.
//
// The rule the UnmarshalJSON methods below enforce:
//
//   - A field whose zero value inverts a safety property — the four AgentInfo
//     fields above, agent_status wherever any response type carries it, and
//     the pane_id a status-change event keys on — must be PRESENT on the
//     wire. Presence means the key exists; for fields whose "none" value is
//     real (name, agent_session) an explicit null is present and decodes to
//     nil. For fields with no meaningful "none" (a bool readiness, the
//     status enum, pane_id) a null is refused as loudly as an absent key.
//   - agent_status is additionally ENUM-VALIDATED wherever it decodes: a
//     status this client does not know is an explicit error, never a
//     silently non-terminal empty string. This alone is the difference
//     between a 30-minute hang and a clear refusal.
//
// Every error this file produces is typed, so a caller can distinguish "the
// wire shape changed" from a transport failure, and each is wrapped by the
// method's decode path with the API method's name ("herd/client: decoding
// agent.get result: ..."), so the failure names method AND field.

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// RequiredFieldError names a field a response object did not carry, or
// carried as an explicit JSON null where a null is not a usable value. It is
// the loud replacement for the silent zero value a renamed herdr field used
// to produce.
type RequiredFieldError struct {
	// Type is the Go type being decoded, e.g. "AgentInfo".
	Type string
	// Field is the wire (JSON) field name, e.g. "agent_status".
	Field string
	// Null reports whether the key was present but explicitly null.
	Null bool
}

// Error implements error.
func (e *RequiredFieldError) Error() string {
	if e.Null {
		return fmt.Sprintf("herd/client: %s field %q is null, not a usable value", e.Type, e.Field)
	}
	return fmt.Sprintf("herd/client: %s is missing required field %q", e.Type, e.Field)
}

// UnknownAgentStatusError means a response carried an agent_status this
// client does not know — a newer herdr's status, or a renamed one. Either
// way it must not decode as the empty string, which every "is this state X?"
// check answers false to.
type UnknownAgentStatusError struct {
	// Status is the offending wire value, verbatim ("null" for an explicit
	// null, the raw JSON for a non-string).
	Status string
}

// Error implements error.
func (e *UnknownAgentStatusError) Error() string {
	return fmt.Sprintf(
		"herd/client: unknown agent_status %q — herdr reports one of %q, %q, %q, %q, %q",
		e.Status, StatusIdle, StatusWorking, StatusBlocked, StatusDone, StatusUnknown,
	)
}

// wireSnippet renders a raw JSON value for an error message, truncated so a
// pathological payload cannot become a pathological log line.
func wireSnippet(data []byte) string {
	const max = 64
	if len(data) > max {
		return string(data[:max]) + "…"
	}
	return string(data)
}

// UnmarshalJSON validates the closed status set. Every decode of an
// agent_status field — AgentInfo, PaneInfo, WorkspaceInfo, TabInfo, the
// status-change event payload — goes through here, so an unknown status is
// loud on every surface at once.
//
// Note what this does NOT catch: an agent_status key that is absent entirely
// never reaches this method, which is why the types below also require the
// key's presence.
func (s *AgentStatus) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return &UnknownAgentStatusError{Status: "null"}
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return &UnknownAgentStatusError{Status: wireSnippet(data)}
	}
	switch AgentStatus(v) {
	case StatusIdle, StatusWorking, StatusBlocked, StatusDone, StatusUnknown:
		*s = AgentStatus(v)
		return nil
	}
	return &UnknownAgentStatusError{Status: v}
}

// wireProbe records the raw JSON of a response object's load-bearing keys so
// their PRESENCE can be checked — the thing stock encoding/json cannot do:
// it cannot distinguish an absent key from a zero value, and a renamed
// field arrives as exactly that absent key.
type wireProbe struct {
	typ   string
	known map[string]json.RawMessage
}

// probe decodes data into out (which must be an alias type without the
// strict methods, so there is no recursion) while recording the raw JSON of
// the named fields. The named fields decode into out normally — agent_status
// runs its enum check there — but presence has to come from the recorded
// raws, so each caller re-decodes the load-bearing fields from them and
// refuses the absent ones.
func probe(data []byte, typ string, fields []string, out any) (*wireProbe, error) {
	// One decode of the object's raw members, so a non-object payload is a
	// loud decode failure here rather than a confusing one downstream.
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	known := make(map[string]json.RawMessage, len(fields))
	for _, f := range fields {
		if raw, ok := members[f]; ok {
			known[f] = raw
		}
	}
	return &wireProbe{typ: typ, known: known}, nil
}

// require returns the raw JSON of one probed field, or a typed error when it
// is absent — or, unless allowNull, explicitly null.
func (p *wireProbe) require(field string, allowNull bool) (json.RawMessage, error) {
	raw, ok := p.known[field]
	if !ok {
		return nil, &RequiredFieldError{Type: p.typ, Field: field}
	}
	if !allowNull && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, &RequiredFieldError{Type: p.typ, Field: field, Null: true}
	}
	return raw, nil
}

// requireStatus is the shared tail of every agent_status carrier: presence
// of the key, then the enum-checked decode of its value.
func (p *wireProbe) requireStatus(out *AgentStatus) error {
	raw, err := p.require("agent_status", false)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// UnmarshalJSON enforces the four load-bearing presence rules: agent_status,
// interactive_ready, name and agent_session must all be carried. name and
// agent_session may be null — "no name" and "no session" are real states —
// but their KEYS must be there, because an absent key is indistinguishable
// from a rename, and the rename is the hazard.
func (a *AgentInfo) UnmarshalJSON(data []byte) error {
	type alias AgentInfo
	var out alias
	p, err := probe(data, "AgentInfo",
		[]string{"agent_status", "interactive_ready", "name", "agent_session"}, &out)
	if err != nil {
		return err
	}
	if err := p.requireStatus(&out.AgentStatus); err != nil {
		return err
	}
	raw, err := p.require("interactive_ready", false)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &out.InteractiveReady); err != nil {
		return err
	}
	raw, err = p.require("name", true)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &out.Name); err != nil {
		return err
	}
	raw, err = p.require("agent_session", true)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &out.AgentSession); err != nil {
		return err
	}
	*a = AgentInfo(out)
	return nil
}

// UnmarshalJSON requires pane_id and agent_status: the status-change event
// is what a wait fans in on, and a rename of either key used to leave a
// waiter matching on "" for ever.
func (p *PaneAgentStatusChanged) UnmarshalJSON(data []byte) error {
	type alias PaneAgentStatusChanged
	var out alias
	pd, err := probe(data, "PaneAgentStatusChanged", []string{"pane_id", "agent_status"}, &out)
	if err != nil {
		return err
	}
	raw, err := pd.require("pane_id", false)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &out.PaneID); err != nil {
		return err
	}
	if err := pd.requireStatus(&out.AgentStatus); err != nil {
		return err
	}
	*p = PaneAgentStatusChanged(out)
	return nil
}

// UnmarshalJSON requires agent_status on a pane.
func (p *PaneInfo) UnmarshalJSON(data []byte) error {
	type alias PaneInfo
	var out alias
	pd, err := probe(data, "PaneInfo", []string{"agent_status"}, &out)
	if err != nil {
		return err
	}
	if err := pd.requireStatus(&out.AgentStatus); err != nil {
		return err
	}
	*p = PaneInfo(out)
	return nil
}

// UnmarshalJSON requires agent_status on a workspace.
func (w *WorkspaceInfo) UnmarshalJSON(data []byte) error {
	type alias WorkspaceInfo
	var out alias
	pd, err := probe(data, "WorkspaceInfo", []string{"agent_status"}, &out)
	if err != nil {
		return err
	}
	if err := pd.requireStatus(&out.AgentStatus); err != nil {
		return err
	}
	*w = WorkspaceInfo(out)
	return nil
}

// UnmarshalJSON requires agent_status on a tab.
func (t *TabInfo) UnmarshalJSON(data []byte) error {
	type alias TabInfo
	var out alias
	pd, err := probe(data, "TabInfo", []string{"agent_status"}, &out)
	if err != nil {
		return err
	}
	if err := pd.requireStatus(&out.AgentStatus); err != nil {
		return err
	}
	*t = TabInfo(out)
	return nil
}
