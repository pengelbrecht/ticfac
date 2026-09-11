package client

// STRICT DECODE TESTS (tick bcv).
//
// One per acceptance clause: each of the four load-bearing AgentInfo renames
// must fail LOUDLY (a typed error naming method and field, never a zero
// value); added unknown fields must still be tolerated; an unknown
// agent_status must be an explicit error; and a result payload that is not
// decodable JSON must be a decode error, not a swallowed one.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fullAgent is a complete agent object in the pinned herdr 0.8.2 shape: every
// load-bearing key present, name and session populated. Each strictness
// case below varies exactly one key of it.
const fullAgent = `{"terminal_id":"term_z","agent":"claude","agent_status":"idle","workspace_id":"w9","tab_id":"w9:t1","pane_id":"w9:p1","focused":false,"revision":1,"name":"tick-bcv","interactive_ready":true,"agent_session":{"source":"herdr:claude","agent":"claude","kind":"id","value":"sess-1"}}`

// agentInfoResult wraps one agent object as agent.get's reply.
func agentInfoResult(agent string) string {
	return `{"type":"agent_info","agent":` + agent + `}`
}

// TestAgentInfoFieldRenamesAreLoud is the heart of the tick: each of the four
// load-bearing fields renamed must produce a typed error naming the method
// and the field — never a decoded zero value, which is how a silent rename
// used to invert a safety property:
//
//	agent_status      -> ""      -> the wave fan-in never completes
//	interactive_ready -> false   -> readiness never observed, spawns race
//	name              -> nil     -> every live worker classifies as dead
//	agent_session     -> nil     -> recovery silently downgrades to redispatch
func TestAgentInfoFieldRenamesAreLoud(t *testing.T) {
	cases := []struct {
		name  string
		from  string // the key as it should be
		to    string // the key as a renamed version would spell it
		field string // the field the error must name
	}{
		{"agent_status renamed", `"agent_status"`, `"agent_state"`, "agent_status"},
		{"interactive_ready renamed", `"interactive_ready"`, `"ready"`, "interactive_ready"},
		{"name renamed", `"name"`, `"agent_name"`, "name"},
		{"agent_session renamed", `"agent_session"`, `"session"`, "agent_session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			renamed := strings.Replace(fullAgent, tc.from, tc.to, 1)
			if renamed == fullAgent {
				t.Fatalf("fixture bug: %s not found in fullAgent", tc.from)
			}
			c, _ := newTestClient(t, map[string]fakeHandler{
				MethodAgentGet: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
					return respond(w, req.ID, agentInfoResult(renamed))
				},
			})

			agent, err := c.AgentGet(t.Context(), "w9:p1")
			if err == nil {
				t.Fatal("decode succeeded, want a loud error — a renamed field must not degrade to a zero value")
			}
			if agent != nil {
				t.Errorf("a zero-valued AgentInfo came back alongside the error: %+v", agent)
			}

			var missing *RequiredFieldError
			if !errors.As(err, &missing) {
				t.Fatalf("error is %T (%v), want *RequiredFieldError", err, err)
			}
			if missing.Field != tc.field {
				t.Errorf("RequiredFieldError.Field = %q, want %q", missing.Field, tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error does not name the field: %v", err)
			}
			if !strings.Contains(err.Error(), MethodAgentGet) {
				t.Errorf("error does not name the method: %v", err)
			}
		})
	}
}

// TestAgentListFieldRenameNamesItsMethod proves the strictness is wired into
// every surface, not just agent.get: the same renamed field through
// agent.list must name agent.list.
func TestAgentListFieldRenameNamesItsMethod(t *testing.T) {
	renamed := strings.Replace(fullAgent, `"agent_status"`, `"agent_state"`, 1)
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"agent_list","agents":[`+renamed+`]}`)
		},
	})

	agents, err := c.AgentList(t.Context())
	if err == nil {
		t.Fatal("AgentList decoded a renamed agent_status, want a loud error")
	}
	if agents != nil {
		t.Errorf("agents came back alongside the error: %+v", agents)
	}
	var missing *RequiredFieldError
	if !errors.As(err, &missing) || missing.Field != "agent_status" {
		t.Fatalf("err = %v, want a RequiredFieldError naming agent_status", err)
	}
	if !strings.Contains(err.Error(), MethodAgentList) {
		t.Errorf("error does not name the method: %v", err)
	}
}

// TestUnknownAgentStatusIsLoud covers the tick's second clause: an
// agent_status this client does not know — a newer herdr's status, or a
// renamed one — must be an explicit error, not a silently non-terminal empty
// string. "This alone is the difference between a 30-minute hang and a clear
// refusal."
func TestUnknownAgentStatusIsLoud(t *testing.T) {
	renamed := strings.Replace(fullAgent, `"agent_status":"idle"`, `"agent_status":"finished"`, 1)
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentGet: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, agentInfoResult(renamed))
		},
	})

	agent, err := c.AgentGet(t.Context(), "w9:p1")
	if err == nil {
		t.Fatalf("unknown agent_status decoded to %q, want a loud error", agent.AgentStatus)
	}
	var unknown *UnknownAgentStatusError
	if !errors.As(err, &unknown) {
		t.Fatalf("error is %T (%v), want *UnknownAgentStatusError", err, err)
	}
	if unknown.Status != "finished" {
		t.Errorf("UnknownAgentStatusError.Status = %q, want \"finished\"", unknown.Status)
	}
	if !strings.Contains(err.Error(), "finished") {
		t.Errorf("error does not name the offending status: %v", err)
	}
}

// TestSnapshotStatusPresenceAndEnumAreLoud proves agent_status strictness on
// the other three carriers — PaneInfo, WorkspaceInfo, TabInfo — through the
// method that reports them, session.snapshot. A renamed status on a pane
// used to decode as "" there too.
func TestSnapshotStatusPresenceAndEnumAreLoud(t *testing.T) {
	const pane = `{"pane_id":"w9:p1","workspace_id":"w9","tab_id":"w9:t1","focused":false,"agent_status":"idle","revision":1}`
	const workspace = `{"workspace_id":"w9","number":1,"label":"x","focused":false,"pane_count":1,"tab_count":1,"active_tab_id":"w9:t1","agent_status":"idle"}`
	const tab = `{"tab_id":"w9:t1","workspace_id":"w9","number":1,"label":"main","focused":false,"pane_count":1,"agent_status":"idle"}`

	cases := []struct {
		name    string
		broken  string // the snapshot member with agent_status renamed/unknown
		unknown bool   // true: renamed to an unknown value; false: key renamed
	}{
		{"pane with renamed agent_status", strings.Replace(pane, `"agent_status"`, `"state"`, 1), false},
		{"pane with unknown agent_status", strings.Replace(pane, `"agent_status":"idle"`, `"agent_status":"finished"`, 1), true},
		{"workspace with renamed agent_status", strings.Replace(workspace, `"agent_status"`, `"state"`, 1), false},
		{"tab with renamed agent_status", strings.Replace(tab, `"agent_status"`, `"state"`, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var snapshot string
			switch {
			case strings.Contains(tc.broken, `"pane_id"`):
				snapshot = `{"type":"session_snapshot","snapshot":{"version":"0.8.2","protocol":20,"workspaces":[],"tabs":[],"panes":[` + tc.broken + `],"agents":[],"layouts":[]}}`
			case strings.Contains(tc.broken, `"workspace_id"`) && strings.Contains(tc.broken, `"pane_count"`):
				snapshot = `{"type":"session_snapshot","snapshot":{"version":"0.8.2","protocol":20,"workspaces":[` + tc.broken + `],"tabs":[],"panes":[],"agents":[],"layouts":[]}}`
			default:
				snapshot = `{"type":"session_snapshot","snapshot":{"version":"0.8.2","protocol":20,"workspaces":[],"tabs":[` + tc.broken + `],"panes":[],"agents":[],"layouts":[]}}`
			}
			c, _ := newTestClient(t, map[string]fakeHandler{
				MethodSessionSnapshot: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
					return respond(w, req.ID, snapshot)
				},
			})

			snap, err := c.SessionSnapshot(t.Context())
			if err == nil {
				t.Fatalf("SessionSnapshot decoded %s, want a loud error", tc.name)
			}
			if snap != nil {
				t.Errorf("a snapshot came back alongside the error: %+v", snap)
			}
			if tc.unknown {
				var unknown *UnknownAgentStatusError
				if !errors.As(err, &unknown) || unknown.Status != "finished" {
					t.Fatalf("err = %v, want an UnknownAgentStatusError naming \"finished\"", err)
				}
			} else {
				var missing *RequiredFieldError
				if !errors.As(err, &missing) || missing.Field != "agent_status" {
					t.Fatalf("err = %v, want a RequiredFieldError naming agent_status", err)
				}
			}
			if !strings.Contains(err.Error(), MethodSessionSnapshot) {
				t.Errorf("error does not name the method: %v", err)
			}
		})
	}
}

// TestSnapshotAgentRenameIsLoud runs one of the four load-bearing renames
// through session.snapshot's agents[] — the surface reconcile reads — to
// prove the AgentInfo presence rules apply wherever the type decodes.
func TestSnapshotAgentRenameIsLoud(t *testing.T) {
	renamed := strings.Replace(fullAgent, `"agent_session"`, `"session"`, 1)
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodSessionSnapshot: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"session_snapshot","snapshot":{"version":"0.8.2","protocol":20,"workspaces":[],"tabs":[],"panes":[],"agents":[`+renamed+`],"layouts":[]}}`)
		},
	})

	snap, err := c.SessionSnapshot(t.Context())
	if err == nil {
		t.Fatal("SessionSnapshot decoded an agent with a renamed agent_session, want a loud error")
	}
	if snap != nil {
		t.Errorf("a snapshot came back alongside the error: %+v", snap)
	}
	var missing *RequiredFieldError
	if !errors.As(err, &missing) || missing.Field != "agent_session" {
		t.Fatalf("err = %v, want a RequiredFieldError naming agent_session", err)
	}
	if !strings.Contains(err.Error(), MethodSessionSnapshot) {
		t.Errorf("error does not name the method: %v", err)
	}
}

// TestEventPayloadStrictnessIsLoud covers the event the wave fan-in waits
// on: a status-change payload with a renamed or unknown agent_status, or a
// renamed pane_id, must fail loudly rather than leave a waiter matching on
// "" for ever.
func TestEventPayloadStrictnessIsLoud(t *testing.T) {
	const good = `{"type":"pane_agent_status_changed","pane_id":"w9:p1","workspace_id":"w9","agent_status":"idle","agent":"claude"}`

	cases := []struct {
		name  string
		event string
		check func(t *testing.T, err error)
	}{
		{
			name:  "agent_status renamed",
			event: strings.Replace(good, `"agent_status"`, `"state"`, 1),
			check: func(t *testing.T, err error) {
				var missing *RequiredFieldError
				if !errors.As(err, &missing) || missing.Field != "agent_status" {
					t.Fatalf("err = %v, want a RequiredFieldError naming agent_status", err)
				}
			},
		},
		{
			name:  "agent_status unknown",
			event: strings.Replace(good, `"agent_status":"idle"`, `"agent_status":"finished"`, 1),
			check: func(t *testing.T, err error) {
				var unknown *UnknownAgentStatusError
				if !errors.As(err, &unknown) || unknown.Status != "finished" {
					t.Fatalf("err = %v, want an UnknownAgentStatusError naming \"finished\"", err)
				}
			},
		},
		{
			name:  "pane_id renamed",
			event: strings.Replace(good, `"pane_id"`, `"pane"`, 1),
			check: func(t *testing.T, err error) {
				var missing *RequiredFieldError
				if !errors.As(err, &missing) || missing.Field != "pane_id" {
					t.Fatalf("err = %v, want a RequiredFieldError naming pane_id", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := Event{Kind: EventPaneAgentStatusChanged, Data: []byte(tc.event)}
			if !ev.IsAgentStatusChanged() {
				t.Fatal("fixture bug: event not recognised as a status change")
			}
			changed, err := ev.AgentStatusChanged()
			if err == nil {
				t.Fatalf("payload decoded to %+v, want a loud error", changed)
			}
			tc.check(t, err)
		})
	}
}

// TestAddedUnknownFieldsAreTolerated pins the other half of the policy:
// strictness about MISSING required fields must not become strictness about
// ADDED ones. A newer herdr that only adds is the forward-compatible case
// protocol.go documents, and it must keep decoding.
func TestAddedUnknownFieldsAreTolerated(t *testing.T) {
	added := strings.Replace(fullAgent, `"revision":1`,
		`"revision":1,"future_agent_field":{"nested":[1,2]}`, 1)
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentGet: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			// An added field at the RESULT level too, not just in the agent.
			return respond(w, req.ID, `{"type":"agent_info","future_result_field":true,"agent":`+added+`}`)
		},
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"agent_list","future_field":"x","agents":[`+added+`]}`)
		},
	})

	agent, err := c.AgentGet(t.Context(), "w9:p1")
	if err != nil {
		t.Fatalf("an added unknown field was rejected: %v", err)
	}
	if agent.AgentStatus != StatusIdle || agent.InteractiveReady != true ||
		agent.Name == nil || *agent.Name != "tick-bcv" ||
		agent.AgentSession == nil || agent.AgentSession.Value != "sess-1" {
		t.Errorf("known fields were disturbed by the unknown one: %+v", agent)
	}

	agents, err := c.AgentList(t.Context())
	if err != nil {
		t.Fatalf("agent.list with an added field failed: %v", err)
	}
	if len(agents) != 1 || agents[0].PaneID != "w9:p1" {
		t.Errorf("agents = %+v", agents)
	}
}

// TestNullOptionalAgentFieldsDecode pins that name and agent_session may be
// null — "no name" and "no session" are real states — so the strictness is
// about the KEY being carried, not the value being non-null.
func TestNullOptionalAgentFieldsDecode(t *testing.T) {
	nulled := strings.Replace(
		strings.Replace(fullAgent, `"name":"tick-bcv"`, `"name":null`, 1),
		`"agent_session":{"source":"herdr:claude","agent":"claude","kind":"id","value":"sess-1"}`,
		`"agent_session":null`, 1)
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentGet: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, agentInfoResult(nulled))
		},
	})

	agent, err := c.AgentGet(t.Context(), "w9:p1")
	if err != nil {
		t.Fatalf("null name/agent_session must decode: %v", err)
	}
	if agent.Name != nil {
		t.Errorf("Name = %v, want nil", agent.Name)
	}
	if agent.AgentSession != nil {
		t.Errorf("AgentSession = %+v, want nil", agent.AgentSession)
	}
	if agent.AgentStatus != StatusIdle || !agent.InteractiveReady {
		t.Errorf("other fields disturbed: %+v", agent)
	}
}

// TestNullRequiredValuesAreLoud pins the null half of each presence rule: a
// bool readiness, the status enum and pane_id have no meaningful "none", so
// an explicit null is refused as loudly as an absent key.
func TestNullRequiredValuesAreLoud(t *testing.T) {
	t.Run("agent_status null", func(t *testing.T) {
		agent := strings.Replace(fullAgent, `"agent_status":"idle"`, `"agent_status":null`, 1)
		var out AgentInfo
		err := jsonUnmarshalAgent(t, agent, &out)
		var unknown *UnknownAgentStatusError
		if !errors.As(err, &unknown) {
			t.Fatalf("err = %T (%v), want *UnknownAgentStatusError", err, err)
		}
	})
	t.Run("interactive_ready null", func(t *testing.T) {
		agent := strings.Replace(fullAgent, `"interactive_ready":true`, `"interactive_ready":null`, 1)
		var out AgentInfo
		err := jsonUnmarshalAgent(t, agent, &out)
		var missing *RequiredFieldError
		if !errors.As(err, &missing) || !missing.Null || missing.Field != "interactive_ready" {
			t.Fatalf("err = %v, want a null RequiredFieldError naming interactive_ready", err)
		}
	})
	t.Run("pane_id null in an event payload", func(t *testing.T) {
		payload := strings.Replace(
			`{"type":"pane_agent_status_changed","pane_id":"w9:p1","agent_status":"idle"}`,
			`"pane_id":"w9:p1"`, `"pane_id":null`, 1)
		var out PaneAgentStatusChanged
		if err := json.Unmarshal([]byte(payload), &out); err == nil {
			t.Fatal("a null pane_id decoded, want a loud error")
		} else {
			var missing *RequiredFieldError
			if !errors.As(err, &missing) || !missing.Null || missing.Field != "pane_id" {
				t.Fatalf("err = %v, want a null RequiredFieldError naming pane_id", err)
			}
		}
	})
}

// jsonUnmarshalAgent decodes one agent object directly, for the cases where
// the property under test is the type's own, not the method wrapper's.
func jsonUnmarshalAgent(t *testing.T, agent string, out *AgentInfo) error {
	t.Helper()
	if err := json.Unmarshal([]byte(agent), out); err != nil {
		return err
	}
	t.Error("decode succeeded, want a loud error")
	return nil
}

// TestMalformedResultIsALoudDecodeError pins that the resultType unmarshal
// error is no longer swallowed. Before tick bcv a result that was not a JSON
// object decoded as an empty discriminator and came back as an
// UnexpectedResultError with Got "" — protocol drift where the real problem
// was malformed JSON.
func TestMalformedResultIsALoudDecodeError(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			// A valid envelope whose result is a JSON string, not an object.
			return respond(w, req.ID, `"agent_list"`)
		},
	})

	_, err := c.AgentList(t.Context())
	if err == nil {
		t.Fatal("a non-object result decoded, want a loud error")
	}
	if !strings.Contains(err.Error(), MethodAgentList) {
		t.Errorf("error does not name the method: %v", err)
	}
	if !strings.Contains(err.Error(), "not a decodable JSON object") {
		t.Errorf("error does not say what was wrong with the result: %v", err)
	}
	var unexpected *UnexpectedResultError
	if errors.As(err, &unexpected) {
		t.Errorf("malformed JSON misfiled as protocol drift: %+v", unexpected)
	}
}

// TestAgentWaitRenameIsLoud covers the last agent-carrying surface, the
// blocking wait — the call the wave fan-in actually makes.
func TestAgentWaitRenameIsLoud(t *testing.T) {
	renamed := strings.Replace(fullAgent, `"interactive_ready"`, `"ready"`, 1)
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentWait: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, agentInfoResult(renamed))
		},
	})

	agent, err := c.AgentWait(t.Context(), AgentWaitParams{
		Target:  "w9:p1",
		Until:   TerminalStatuses,
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatalf("AgentWait decoded a renamed interactive_ready to %+v, want a loud error", agent)
	}
	if agent != nil {
		t.Errorf("an AgentInfo came back alongside the error: %+v", agent)
	}
	var missing *RequiredFieldError
	if !errors.As(err, &missing) || missing.Field != "interactive_ready" {
		t.Fatalf("err = %v, want a RequiredFieldError naming interactive_ready", err)
	}
	if !strings.Contains(err.Error(), MethodAgentWait) {
		t.Errorf("error does not name the method: %v", err)
	}
}
