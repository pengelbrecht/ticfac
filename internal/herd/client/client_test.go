package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNewPerformsHandshake(t *testing.T) {
	c, srv := newTestClient(t, nil)

	if got := c.ServerInfo().Protocol; got != ProtocolVersion {
		t.Errorf("protocol = %d, want %d", got, ProtocolVersion)
	}
	if got := c.ServerInfo().Version; got != "0.8.2" {
		t.Errorf("version = %q, want 0.8.2", got)
	}
	if c.ServerInfo().Capabilities == nil || !c.ServerInfo().Capabilities.LiveHandoff {
		t.Errorf("capabilities not decoded: %+v", c.ServerInfo().Capabilities)
	}
	if c.SocketPath() != srv.Path() {
		t.Errorf("SocketPath = %q, want %q", c.SocketPath(), srv.Path())
	}

	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Method != MethodPing {
		t.Fatalf("expected one ping request, got %+v", reqs)
	}
	if reqs[0].ID == "" {
		t.Error("request carried no id")
	}
	if string(reqs[0].Params) != "{}" {
		t.Errorf("ping params = %s, want {}", reqs[0].Params)
	}
}

func TestNewFailsClosedBelowMinProtocol(t *testing.T) {
	// 18 is below the floor (19); 0 is a server that reported no protocol at
	// all. Both fail closed. (19 itself is IN range now: every fixture is
	// 0.8.0-era protocol 19, so the floor dropped below the pin — see
	// TestNewAppliesSupportedProtocolRange for the floor being accepted.)
	for _, protocol := range []int{0, 18} {
		srv := newFakeServer(t, func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, pongResultProtocol(protocol))
		})

		c, err := New(t.Context(), Options{SocketPath: srv.Path()})
		if err == nil {
			t.Fatalf("protocol %d: New succeeded, want mismatch error", protocol)
		}
		if c != nil {
			t.Errorf("protocol %d: New returned a usable client alongside the error", protocol)
		}

		var mismatch *ProtocolMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("protocol %d: error is %T (%v), want *ProtocolMismatchError", protocol, err, err)
		}
		if mismatch.Actual != uint32(protocol) || mismatch.Min != MinProtocolVersion {
			t.Errorf("mismatch = %+v", mismatch)
		}
		if !strings.Contains(mismatch.Error(), srv.Path()) {
			t.Errorf("error message omits the endpoint: %s", mismatch.Error())
		}
		if !strings.Contains(mismatch.Error(), "refusing to continue") {
			t.Errorf("error message is not fail-closed: %s", mismatch.Error())
		}
	}
}

// TestNewAcceptsForwardCompatibleProtocol pins the range policy: a server
// NEWER than the pin is assumed forward-compatible and must succeed (degrading
// to a warning), while a server BELOW the minimum still hard-stops above.
// TestSupportedProtocolConstantsAreDistinct pins the three protocol
// constants to three DISTINCT values with three distinct jobs. A single
// constant cannot say all of: the oldest server this client can speak to
// (19 — every fixture is protocol 19, and the 19 -> 20 shape diff was
// purely additive), the newest this package was verified against
// (20 — the herdr 0.8.2 pin), and the newest observed live without a
// warning (22 — the captured herdr 0.9.0 ping).
func TestSupportedProtocolConstantsAreDistinct(t *testing.T) {
	if MinProtocolVersion != 19 || ProtocolVersion != 20 || ProtocolWarnVersion != 22 {
		t.Fatalf("protocol constants = floor %d, pin %d, warn %d; want 19 / 20 / 22",
			MinProtocolVersion, ProtocolVersion, ProtocolWarnVersion)
	}
	if !(MinProtocolVersion < ProtocolVersion && ProtocolVersion < ProtocolWarnVersion) {
		t.Fatal("protocol constants are not three strictly distinct values")
	}
}

// TestNewAppliesSupportedProtocolRange pins the handshake's range policy
// across the whole supported band: the floor (19) is accepted SILENTLY — a
// protocol-19 server sends a strict subset of the shapes this client
// decodes; the pin (20), the unseen gap (21) and the observed live upgrade
// (22, herdr 0.9.0) are all silent; anything above the warn line (22)
// proceeds WITH a warning — a forward-compatible upgrade degrades to a
// warning, never a refusal. There is no hard upper bound.
func TestNewAppliesSupportedProtocolRange(t *testing.T) {
	tests := []struct {
		protocol int
		wantWarn bool
	}{
		{19, false}, // floor: 0.8.0-era shapes, all fixtures decode
		{20, false}, // pin: herdr 0.8.2
		{21, false}, // newer than the pin, still inside the silent band
		{22, false}, // newest observed: herdr 0.9.0, captured live
		{23, true},  // newer than anything observed: warn, but proceed
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("protocol_%d", tc.protocol), func(t *testing.T) {
			srv := newFakeServer(t, func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
				return respond(w, req.ID, pongResultProtocol(tc.protocol))
			})

			var warning strings.Builder
			c, err := New(t.Context(), Options{SocketPath: srv.Path(), ProtocolWarning: &warning})
			if err != nil {
				t.Fatalf("protocol %d: New returned %v, want success", tc.protocol, err)
			}
			if c == nil {
				t.Fatalf("protocol %d: New returned no client", tc.protocol)
			}
			if got := c.ServerInfo().Protocol; got != uint32(tc.protocol) {
				t.Errorf("ServerInfo().Protocol = %d, want %d", got, tc.protocol)
			}
			if tc.wantWarn {
				if warning.Len() == 0 {
					t.Errorf("protocol %d: no warning emitted, want a forward-compatibility warning", tc.protocol)
				}
				if !strings.Contains(warning.String(), "newer than protocol 22") {
					t.Errorf("warning = %q, want it to name the warn line 22", warning.String())
				}
			} else if warning.Len() != 0 {
				t.Errorf("protocol %d: warning = %q, want silence inside the supported band", tc.protocol, warning.String())
			}
		})
	}
}

// snapshotResult builds a session_snapshot result advertising the given
// protocol and version over an otherwise empty session. Workspaces, tabs,
// panes and agents are all optional in the snapshot shape — herdr omits keys
// it has no data for — so this exercises exactly the mid-run re-report path:
// the client reads the protocol field it already decodes.
func snapshotResult(protocol uint32, version string) string {
	body, err := json.Marshal(map[string]any{
		"type": "session_snapshot",
		"snapshot": map[string]any{
			"version":    version,
			"protocol":   protocol,
			"workspaces": []any{},
			"tabs":       []any{},
			"panes":      []any{},
			"agents":     []any{},
			"layouts":    []any{},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// TestSessionSnapshotRechecksProtocolBelowFloor scripts herdr DOWNGRADING
// mid-run: the handshake accepted a protocol-20 server, a later snapshot
// reports protocol 18. The re-check must fail the call closed (shapes older
// than the floor are undecodable) and stay OPERATIONAL — the client is not
// torn down, so the next snapshot against a server back in range succeeds.
// A refused call, never a dead client.
func TestSessionSnapshotRechecksProtocolBelowFloor(t *testing.T) {
	calls := 0
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodSessionSnapshot: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			calls++
			if calls == 1 {
				return respond(w, req.ID, snapshotResult(18, "0.7.0"))
			}
			return respond(w, req.ID, snapshotResult(20, "0.8.2"))
		},
	})
	if got := c.ServerInfo().Protocol; got != ProtocolVersion {
		t.Fatalf("handshake protocol = %d, want %d", got, ProtocolVersion)
	}

	_, err := c.SessionSnapshot(t.Context())
	var mismatch *ProtocolMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("SessionSnapshot on protocol 18: err = %v, want *ProtocolMismatchError", err)
	}
	if mismatch.Actual != 18 || mismatch.Min != MinProtocolVersion {
		t.Errorf("mismatch = %+v, want Actual 18 and Min %d", *mismatch, MinProtocolVersion)
	}
	if !strings.Contains(mismatch.Error(), srv.Path()) || !strings.Contains(mismatch.Error(), "refusing to continue") {
		t.Errorf("mismatch message = %q, want endpoint + fail-closed wording", mismatch.Error())
	}
	// ServerInfo reports what the server speaks NOW, even when that
	// observation is what the refusal was made from.
	if got := c.ServerInfo().Protocol; got != 18 {
		t.Errorf("ServerInfo().Protocol = %d after the mid-run downgrade, want 18", got)
	}

	// The refusal is operational: the next call dials fresh, re-observes a
	// protocol back in range, and succeeds.
	snap, err := c.SessionSnapshot(t.Context())
	if err != nil {
		t.Fatalf("SessionSnapshot after a mid-run refusal: %v, want success", err)
	}
	if snap.Protocol != 20 {
		t.Errorf("snapshot protocol = %d, want 20", snap.Protocol)
	}
	if got := c.ServerInfo().Protocol; got != 20 {
		t.Errorf("ServerInfo().Protocol = %d after recovery, want 20", got)
	}
}

// TestSessionSnapshotRechecksProtocolAboveWarn: a mid-run upgrade PAST the
// warn line (22) still succeeds — a forward-compatible upgrade degrades to
// a warning, never a refusal — and the warning names what was observed. The
// tick requires this proceed: nothing about a newer protocol stops a run.
func TestSessionSnapshotRechecksProtocolAboveWarn(t *testing.T) {
	var warning strings.Builder
	c, _ := newTestClientOpts(t, map[string]fakeHandler{
		MethodSessionSnapshot: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, snapshotResult(23, "0.9.1"))
		},
	}, Options{ProtocolWarning: &warning})

	snap, err := c.SessionSnapshot(t.Context())
	if err != nil {
		t.Fatalf("SessionSnapshot on protocol 23: %v, want success with a warning", err)
	}
	if snap.Protocol != 23 {
		t.Errorf("snapshot protocol = %d, want 23", snap.Protocol)
	}
	if !strings.Contains(warning.String(), "protocol 23") || !strings.Contains(warning.String(), "newer than protocol 22") {
		t.Errorf("warning = %q, want it to name the observed protocol 23 and the warn line 22", warning.String())
	}
	// ServerInfo must refresh: provenance records what the server speaks
	// NOW, not what it spoke at connect.
	if got := c.ServerInfo(); got.Protocol != 23 || got.Version != "0.9.1" {
		t.Errorf("ServerInfo = %s/%d, want 0.9.1/23", got.Version, got.Protocol)
	}
}

// TestSessionSnapshotNoticesObservedUpgradeSilently: the REAL upgrade this
// machine took (herdr 0.8.2 -> 0.9.0, protocol 20 -> 22, live 2026-09-10)
// must stay silent — 22 is observed, not surprising — while ServerInfo still
// notices the change. A client built against 0.8.2 keeps working against
// 0.9.0 with zero ceremony.
func TestSessionSnapshotNoticesObservedUpgradeSilently(t *testing.T) {
	var warning strings.Builder
	c, _ := newTestClientOpts(t, map[string]fakeHandler{
		MethodSessionSnapshot: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, snapshotResult(22, "0.9.0"))
		},
	}, Options{ProtocolWarning: &warning})

	snap, err := c.SessionSnapshot(t.Context())
	if err != nil {
		t.Fatalf("SessionSnapshot on protocol 22: %v, want success", err)
	}
	if snap.Protocol != 22 {
		t.Errorf("snapshot protocol = %d, want 22", snap.Protocol)
	}
	if warning.Len() != 0 {
		t.Errorf("warning = %q, want silence: herdr 0.9.0 / protocol 22 is the live-observed upgrade", warning.String())
	}
	if got := c.ServerInfo(); got.Protocol != 22 || got.Version != "0.9.0" {
		t.Errorf("ServerInfo = %s/%d, want 0.9.0/22", got.Version, got.Protocol)
	}
	// Capabilities are a handshake field: a snapshot re-report refreshes
	// the protocol, but never silently drops what the ping advertised.
	if err := c.RequireCapability(CapabilityLiveHandoff); err != nil {
		t.Errorf("RequireCapability(live_handoff) after a silent upgrade to 0.9.0 = %v, want nil", err)
	}
}

// TestRequireCapabilityRefusedByName: a server advertising only
// detached_server_daemon must refuse live_handoff BY NAME — naming the
// capability, the herdr version and the endpoint — while the capability it
// DOES have passes. An unknown name is refused too: the known set is what
// the server may advertise, anything else is not a capability.
func TestRequireCapabilityRefusedByName(t *testing.T) {
	// Only detached_server_daemon; live_handoff explicitly absent.
	c, srv := newTestClientOpts(t, map[string]fakeHandler{
		MethodPing: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"pong","version":"0.9.0","protocol":22,"capabilities":{"live_handoff":false,"detached_server_daemon":true}}`)
		},
	}, Options{})

	if err := c.RequireCapability(CapabilityDetachedServerDaemon); err != nil {
		t.Errorf("RequireCapability(detached_server_daemon) = %v, want nil — the server advertises it", err)
	}

	err := c.RequireCapability(CapabilityLiveHandoff)
	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("RequireCapability(live_handoff) = %v, want *CapabilityError", err)
	}
	if capErr.Capability != CapabilityLiveHandoff {
		t.Errorf("refused capability = %q, want %q", capErr.Capability, CapabilityLiveHandoff)
	}
	if capErr.ServerVersion != "0.9.0" || capErr.Protocol != 22 {
		t.Errorf("refusal = %+v, want it to cite herdr 0.9.0 / protocol 22", *capErr)
	}
	msg := capErr.Error()
	for _, want := range []string{"live_handoff", "0.9.0", srv.Path()} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not name %q — a missing feature must be refused BY NAME", msg, want)
		}
	}

	if err := c.RequireCapability("not_a_capability"); err == nil {
		t.Error("RequireCapability on an unknown name succeeded, want refusal")
	}
}

// TestRequireCapabilityAcceptsAdvertised: the captured herdr 0.8.2 pong
// advertises both capabilities this package knows.
func TestRequireCapabilityAcceptsAdvertised(t *testing.T) {
	c, _ := newTestClient(t, nil)
	for _, name := range []string{CapabilityLiveHandoff, CapabilityDetachedServerDaemon} {
		if err := c.RequireCapability(name); err != nil {
			t.Errorf("RequireCapability(%s) = %v, want nil — the 0.8.2 pong advertises it", name, err)
		}
	}
}

// TestServerCapabilitiesHasNilAndUnknown: absence fails closed. A nil
// capabilities block advertises nothing; an unknown name is never a
// capability.
func TestServerCapabilitiesHasNilAndUnknown(t *testing.T) {
	var nilCaps *ServerCapabilities
	if nilCaps.Has(CapabilityLiveHandoff) {
		t.Error("nil ServerCapabilities claims live_handoff — absence must fail closed")
	}
	full := &ServerCapabilities{LiveHandoff: true, DetachedServerDaemon: true}
	if !full.Has(CapabilityLiveHandoff) || !full.Has(CapabilityDetachedServerDaemon) {
		t.Error("advertised capabilities not found")
	}
	if full.Has("not_a_capability") {
		t.Error("unknown capability name answered true, want false")
	}
}

// TestDialTellsNotRunningFromWrongVersion: "herdr is not running" and "herdr
// is the wrong version" must be distinguishable at exit — the ticks CLI's
// herd_shared.go used to flatten both into ExitGeneric. Both not-running
// shapes — no socket file, leftover socket file with nothing listening —
// classify as NotRunningError; a protocol refusal does not.
//
// Paths use a short os.MkdirTemp("", ...) prefix rather than t.TempDir():
// darwin caps unix socket paths around 104 bytes.
func TestDialTellsNotRunningFromWrongVersion(t *testing.T) {
	dir, err := os.MkdirTemp("", "hd-not-running")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Herdr was never started: no socket file at the endpoint.
	absent := filepath.Join(dir, "absent.sock")
	if _, err := NewUnixTransport(absent).Dial(t.Context()); !IsNotRunning(err) {
		t.Fatalf("dial on an absent socket: err = %v, want IsNotRunning", err)
	}
	_, err = New(t.Context(), Options{SocketPath: absent})
	if !IsNotRunning(err) {
		t.Fatalf("New on an absent socket: err = %v, want IsNotRunning", err)
	}
	var nre *NotRunningError
	if !errors.As(err, &nre) || nre.Endpoint != absent {
		t.Fatalf("New on an absent socket: err = %v, want *NotRunningError naming %s", err, absent)
	}
	if !strings.Contains(nre.Error(), "not running") {
		t.Errorf("error = %q, want it to say herdr is not running", nre.Error())
	}

	// Herdr exited and left its socket file behind: connection refused is
	// ALSO not running, and the underlying cause stays inspectable.
	ghost := filepath.Join(dir, "ghost.sock")
	ln, err := net.Listen("unix", ghost)
	if err != nil {
		t.Fatal(err)
	}
	// Go's unix listener unlinks the socket on Close; keep the file to
	// simulate herdr exiting without cleanup.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // the file remains; nothing accepts
	_, err = NewUnixTransport(ghost).Dial(t.Context())
	if !IsNotRunning(err) {
		t.Fatalf("dial on a leftover socket: err = %v, want IsNotRunning", err)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("err = %v, want it to wrap ECONNREFUSED so callers can still inspect the cause", err)
	}

	// But a server that IS running the wrong protocol is NOT a not-running
	// failure: it is a protocol refusal.
	srv := newFakeServer(t, func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
		return respond(w, req.ID, pongResultProtocol(18))
	})
	_, err = New(t.Context(), Options{SocketPath: srv.Path()})
	if IsNotRunning(err) {
		t.Fatalf("protocol refusal misclassified as not running: %v", err)
	}
	var mismatch *ProtocolMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("wrong-version err = %v, want *ProtocolMismatchError", err)
	}
}

func TestCallMapsErrorResponse(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentGet: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respondErr(w, req.ID, CodeAgentNotFound, "agent target nope not found")
		},
	})

	_, err := c.AgentGet(t.Context(), "nope")
	if err == nil {
		t.Fatal("AgentGet succeeded, want error")
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error is %T (%v), want *APIError", err, err)
	}
	if apiErr.Code != CodeAgentNotFound {
		t.Errorf("code = %q", apiErr.Code)
	}
	if apiErr.Method != MethodAgentGet {
		t.Errorf("method = %q", apiErr.Method)
	}
	if apiErr.Message != "agent target nope not found" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if !IsCode(err, CodeAgentNotFound) {
		t.Error("IsCode did not recognise the code")
	}
	if IsTimeout(err) {
		t.Error("IsTimeout matched a non-timeout error")
	}
	if !strings.Contains(err.Error(), MethodAgentGet) {
		t.Errorf("error text omits the method: %v", err)
	}
}

func TestCallMapsEmptyIDErrorResponse(t *testing.T) {
	// herdr answers envelope parse failures with an empty id, so an error
	// body must be honoured regardless of the id it carries.
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respondErr(w, "", CodeInvalidRequest, "invalid request: missing field `params`")
		},
	})

	_, err := c.AgentList(t.Context())
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error is %T (%v), want *APIError", err, err)
	}
	if apiErr.Code != CodeInvalidRequest || apiErr.RequestID != "" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestTimeoutErrorIsRecognised(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentWait: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respondErr(w, req.ID, CodeTimeout, "timed out waiting for agent status")
		},
	})

	_, err := c.AgentWait(t.Context(), AgentWaitParams{Target: "w3S:p1", Timeout: time.Second})
	if !IsTimeout(err) {
		t.Fatalf("IsTimeout(%v) = false, want true", err)
	}
}

func TestCallRejectsMismatchedResponseID(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, "some-other-id", `{"type":"agent_list","agents":[]}`)
		},
	})

	_, err := c.AgentList(t.Context())
	if err == nil || !strings.Contains(err.Error(), "does not match request id") {
		t.Fatalf("err = %v, want an id-mismatch error", err)
	}
}

func TestCallRejectsUnexpectedResultType(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"ok"}`)
		},
	})

	_, err := c.AgentList(t.Context())
	var unexpected *UnexpectedResultError
	if !errors.As(err, &unexpected) {
		t.Fatalf("error is %T (%v), want *UnexpectedResultError", err, err)
	}
	if unexpected.Got != "ok" || unexpected.Want != resultAgentList {
		t.Errorf("unexpected = %+v", unexpected)
	}
}

func TestEnvelopeCarriesUniqueIDsPerCall(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"agent_list","agents":[]}`)
		},
	})

	for range 3 {
		if _, err := c.AgentList(t.Context()); err != nil {
			t.Fatalf("AgentList: %v", err)
		}
	}

	seen := map[string]bool{}
	for _, req := range srv.Requests() {
		if seen[req.ID] {
			t.Fatalf("duplicate request id %q", req.ID)
		}
		seen[req.ID] = true
	}
	if len(seen) != 4 { // ping + three lists
		t.Errorf("saw %d requests, want 4", len(seen))
	}
}

func TestAgentListDecodesCapturedFixture(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodAgentList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, loadFixture(t, "agent_list.json"))
		},
	})

	agents, err := c.AgentList(t.Context())
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].PaneID != "w3S:p1" || agents[0].AgentStatus != StatusWorking {
		t.Errorf("agent[0] = %+v", agents[0])
	}
	if agents[0].AgentSession == nil || agents[0].AgentSession.Value != "eb93fb33-7f94-4a1d-b05f-2f4c5152b9af" {
		t.Errorf("agent session not decoded: %+v", agents[0].AgentSession)
	}
	if agents[1].Agent == nil || *agents[1].Agent != "codex" {
		t.Errorf("agent[1].Agent = %v", agents[1].Agent)
	}
}

func TestAgentGetDecodesCapturedFixture(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodAgentGet: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, loadFixture(t, "agent_info.json"))
		},
	})

	agent, err := c.AgentGet(t.Context(), "w3S:p1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.AgentStatus != StatusIdle {
		t.Errorf("status = %q", agent.AgentStatus)
	}
	assertParams(t, srv, MethodAgentGet, map[string]any{"target": "w3S:p1"})
}

func TestAgentWaitDecodesAgentInfo(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodAgentWait: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, loadFixture(t, "agent_info.json"))
		},
	})

	agent, err := c.AgentWait(t.Context(), AgentWaitParams{
		Target:  "w3S:p1",
		Until:   TerminalStatuses,
		Timeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("AgentWait: %v", err)
	}
	if agent.PaneID != "w3S:p1" {
		t.Errorf("pane = %q", agent.PaneID)
	}
	assertParams(t, srv, MethodAgentWait, map[string]any{
		"target":     "w3S:p1",
		"until":      []any{"idle", "done"},
		"timeout_ms": float64(90000),
	})
}

func TestWorktreeMethodsDecodeCapturedFixtures(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodWorktreeList: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, loadFixture(t, "worktree_list.json"))
		},
		MethodWorktreeCreate: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"worktree_created","workspace":{"workspace_id":"w9","number":9,"label":"tick-xw7","focused":false,"pane_count":1,"tab_count":1,"active_tab_id":"w9:t1","agent_status":"unknown"},"tab":{"tab_id":"w9:t1","workspace_id":"w9","number":1,"label":"main","focused":false,"pane_count":1,"agent_status":"unknown"},"root_pane":{"pane_id":"w9:p1","terminal_id":"term_z","workspace_id":"w9","tab_id":"w9:t1","focused":false,"agent_status":"unknown","revision":0},"worktree":{"path":"/repo/.worktrees/tick-xw7","branch":"tick/xw7","is_bare":false,"is_detached":false,"is_prunable":false,"is_linked_worktree":true,"label":"ticks"}}`)
		},
		MethodWorktreeRemove: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"worktree_removed","workspace_id":"w9","path":"/repo/.worktrees/tick-xw7","forced":true}`)
		},
	})

	listing, err := c.WorktreeList(t.Context(), WorktreeListParams{Cwd: Ptr("/repo")})
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if listing.Source.RepoName != "ticks" || len(listing.Worktrees) != 2 {
		t.Fatalf("listing = %+v", listing)
	}
	if !listing.Worktrees[1].IsLinkedWorktree || listing.Worktrees[1].Branch == nil {
		t.Errorf("linked worktree = %+v", listing.Worktrees[1])
	}
	assertParams(t, srv, MethodWorktreeList, map[string]any{"cwd": "/repo"})

	created, err := c.WorktreeCreate(t.Context(), WorktreeCreateParams{
		Branch: Ptr("tick/xw7"),
		Base:   Ptr("epic/gyz"),
		Label:  Ptr("tick-xw7"),
	})
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	if created.RootPane.PaneID != "w9:p1" || created.Workspace.WorkspaceID != "w9" {
		t.Errorf("created = %+v", created)
	}
	// Optional fields must be omitted rather than sent as null, and focus
	// must default to false so a spawn never steals the user's focus.
	assertParams(t, srv, MethodWorktreeCreate, map[string]any{
		"branch": "tick/xw7",
		"base":   "epic/gyz",
		"label":  "tick-xw7",
		"focus":  false,
	})

	removed, err := c.WorktreeRemove(t.Context(), WorktreeRemoveParams{WorkspaceID: "w9", Force: true})
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if !removed.Forced || removed.WorkspaceID != "w9" {
		t.Errorf("removed = %+v", removed)
	}
}

func TestAgentStartSendsCompiledArgv(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodAgentStart: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"agent_started","agent":{"terminal_id":"term_z","agent":"claude","agent_status":"working","workspace_id":"w9","tab_id":"w9:t1","pane_id":"w9:p1","focused":false,"revision":1},"argv":["claude","--permission-mode","bypassPermissions","--model","opus"]}`)
		},
	})

	started, err := c.AgentStart(t.Context(), AgentStartParams{
		Name:           "tick-xw7",
		Kind:           "claude",
		PaneID:         "w9:p1",
		Args:           []string{"--permission-mode", "bypassPermissions", "--model", "opus"},
		StartupTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("AgentStart: %v", err)
	}
	if len(started.Argv) != 5 {
		t.Errorf("argv = %v", started.Argv)
	}
	assertParams(t, srv, MethodAgentStart, map[string]any{
		"name":       "tick-xw7",
		"kind":       "claude",
		"pane_id":    "w9:p1",
		"args":       []any{"--permission-mode", "bypassPermissions", "--model", "opus"},
		"timeout_ms": float64(30000),
	})
}

func TestAgentPromptSendsInlineWait(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodAgentPrompt: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"agent_prompted","agent":{"terminal_id":"term_z","agent_status":"idle","workspace_id":"w9","tab_id":"w9:t1","pane_id":"w9:p1","focused":false,"revision":2}}`)
		},
	})

	agent, err := c.AgentPrompt(t.Context(), AgentPromptParams{
		Target: "w9:p1",
		Text:   "go",
		Wait:   &AgentWaitOptions{Until: TerminalStatuses, Timeout: 2 * time.Minute},
	})
	if err != nil {
		t.Fatalf("AgentPrompt: %v", err)
	}
	if agent.AgentStatus != StatusIdle {
		t.Errorf("status = %q", agent.AgentStatus)
	}
	assertParams(t, srv, MethodAgentPrompt, map[string]any{
		"target": "w9:p1",
		"text":   "go",
		"wait": map[string]any{
			"until":      []any{"idle", "done"},
			"timeout_ms": float64(120000),
		},
	})
}

func TestPaneReadAndWaitForOutput(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodPaneRead: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, loadFixture(t, "pane_read.json"))
		},
		MethodPaneWaitForOutput: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"output_matched","pane_id":"w3S:p1","revision":12,"matched_line":"STATUS: DONE","read":{"pane_id":"w3S:p1","workspace_id":"w3S","tab_id":"w3S:t1","source":"recent_unwrapped","format":"text","text":"STATUS: DONE\n","revision":12,"truncated":false}}`)
		},
	})

	read, err := c.PaneRead(t.Context(), PaneReadParams{
		PaneID:    "w3S:p1",
		Source:    SourceVisible,
		Lines:     Ptr(uint32(50)),
		StripANSI: Ptr(true),
	})
	if err != nil {
		t.Fatalf("PaneRead: %v", err)
	}
	if !strings.Contains(read.Text, "STATUS: DONE") || !read.Truncated {
		t.Errorf("read = %+v", read)
	}
	assertParams(t, srv, MethodPaneRead, map[string]any{
		"pane_id":    "w3S:p1",
		"source":     "visible",
		"lines":      float64(50),
		"strip_ansi": true,
	})

	matched, err := c.PaneWaitForOutput(t.Context(), PaneWaitForOutputParams{
		PaneID:  "w3S:p1",
		Source:  SourceRecentUnwrapped,
		Match:   Substring("STATUS: DONE"),
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("PaneWaitForOutput: %v", err)
	}
	if matched.MatchedLine == nil || *matched.MatchedLine != "STATUS: DONE" {
		t.Errorf("matched = %+v", matched)
	}
	assertParams(t, srv, MethodPaneWaitForOutput, map[string]any{
		"pane_id":    "w3S:p1",
		"source":     "recent_unwrapped",
		"match":      map[string]any{"type": "substring", "value": "STATUS: DONE"},
		"timeout_ms": float64(300000),
	})
}

func TestSessionSnapshotDecodesCapturedFixture(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		MethodSessionSnapshot: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, loadFixture(t, "session_snapshot.json"))
		},
	})

	snap, err := c.SessionSnapshot(t.Context())
	if err != nil {
		t.Fatalf("SessionSnapshot: %v", err)
	}
	if snap.Protocol != ProtocolVersion {
		t.Errorf("snapshot protocol = %d", snap.Protocol)
	}
	if len(snap.Workspaces) != 2 || len(snap.Panes) != 2 || len(snap.Agents) != 1 {
		t.Fatalf("snapshot = %d workspaces, %d panes, %d agents", len(snap.Workspaces), len(snap.Panes), len(snap.Agents))
	}
	wt := snap.Workspaces[1].Worktree
	if wt == nil || wt.CheckoutPath != "/repo/.worktrees/tick-xw7" || !wt.IsLinkedWorktree {
		t.Errorf("workspace worktree = %+v", wt)
	}
	if len(snap.Layouts) != 1 {
		t.Errorf("layouts kept raw: got %d", len(snap.Layouts))
	}
}

func TestCallEscapeHatch(t *testing.T) {
	c, _ := newTestClient(t, map[string]fakeHandler{
		"pane.list": func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"pane_list","panes":[]}`)
		},
	})

	var out struct {
		Type string `json:"type"`
	}
	if err := c.Call(t.Context(), "pane.list", struct{}{}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Type != "pane_list" {
		t.Errorf("type = %q", out.Type)
	}
}

// TestCallTimeoutClampsGenerousCallerDeadline pins that CallTimeout is a real
// bound, not a fallback. A substrate probe with a long caller deadline must
// still fail on the client's own budget when the socket accepts but never
// answers — otherwise availability detection hangs instead of degrading.
func TestCallTimeoutClampsGenerousCallerDeadline(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)

	srv := newFakeServer(t, func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
		if req.Method == MethodPing && req.ID == "tk-1" {
			return respond(w, req.ID, pongResult) // let New's handshake through
		}
		<-blocked // accept, never reply
		return nil
	})

	c, err := New(t.Context(), Options{SocketPath: srv.Path(), CallTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err = c.AgentList(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("AgentList against a never-replying server succeeded")
	}
	if elapsed > time.Second {
		t.Errorf("call took %s; the caller's 5s deadline overrode the 100ms CallTimeout", elapsed)
	}
	if ctx.Err() != nil {
		t.Errorf("the caller's context was consumed: %v", ctx.Err())
	}
}

func TestCallerDeadlineWinsWhenShorter(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)

	srv := newFakeServer(t, func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
		if req.Method == MethodPing && req.ID == "tk-1" {
			return respond(w, req.ID, pongResult)
		}
		<-blocked
		return nil
	})

	c, err := New(t.Context(), Options{SocketPath: srv.Path(), CallTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.AgentList(ctx); err == nil {
		t.Fatal("AgentList succeeded, want a deadline error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("call took %s; the shorter caller deadline was ignored", elapsed)
	}
}

func TestEventBufferDefaults(t *testing.T) {
	tests := []struct {
		name string
		opt  int
		want int
	}{
		{name: "zero means the default", opt: 0, want: DefaultEventBuffer},
		{name: "negative means unbuffered", opt: -1, want: 0},
		{name: "explicit size is honoured", opt: 8, want: 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClientOpts(t, nil, Options{EventBuffer: tc.opt})
			if c.eventBuffer != tc.want {
				t.Errorf("eventBuffer = %d, want %d", c.eventBuffer, tc.want)
			}
		})
	}
}

func TestAgentStartRejectsOutOfRangeStartupTimeout(t *testing.T) {
	c, srv := newTestClient(t, nil)

	for _, timeout := range []time.Duration{time.Second, MinAgentStartupTimeout, MaxAgentStartupTimeout + time.Second} {
		_, err := c.AgentStart(t.Context(), AgentStartParams{
			Name: "w", Kind: "claude", PaneID: "w9:p1", StartupTimeout: timeout,
		})
		if err == nil {
			t.Errorf("StartupTimeout %s was accepted, want a range error", timeout)
		} else if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("StartupTimeout %s: err = %v", timeout, err)
		}
	}
	for _, req := range srv.Requests() {
		if req.Method == MethodAgentStart {
			t.Fatal("an out-of-range startup timeout reached the server")
		}
	}
}

func TestEventsWaitValidatesAgentStatusMatch(t *testing.T) {
	c, srv := newTestClient(t, nil)

	// The schema requires agent_status on this match; there is no wildcard.
	_, err := c.EventsWait(t.Context(), EventsWaitParams{
		Match: EventMatch{Event: EventPaneAgentStatusChanged, PaneID: "w3S:p1"},
	})
	if err == nil || !strings.Contains(err.Error(), "needs an AgentStatus") {
		t.Errorf("err = %v, want a missing-AgentStatus error", err)
	}

	_, err = c.EventsWait(t.Context(), EventsWaitParams{
		Match: EventMatch{Event: EventPaneAgentStatusChanged, AgentStatus: StatusIdle},
	})
	if err == nil || !strings.Contains(err.Error(), "needs a PaneID") {
		t.Errorf("err = %v, want a missing-PaneID error", err)
	}

	for _, req := range srv.Requests() {
		if req.Method == MethodEventsWait {
			t.Fatal("an invalid match reached the server")
		}
	}
}

func TestNotificationShowSendsSchemaParamsAndDecodesTheReason(t *testing.T) {
	c, srv := newTestClient(t, map[string]fakeHandler{
		MethodNotificationShow: func(t *testing.T, req fakeRequest, w *fakeConnWriter) error {
			return respond(w, req.ID, `{"type":"notification_show","shown":false,"reason":"no_foreground_client"}`)
		},
	})

	shown, err := c.NotificationShow(t.Context(), NotificationShowParams{
		Title: "tick 6pn blocked",
		Body:  Ptr("epic zz0 implement"),
		Sound: SoundRequest,
	})
	if err != nil {
		t.Fatalf("NotificationShow: %v", err)
	}
	// herdr declining to display is a NORMAL result, not an error: a caller
	// that treated it as one would retry for ever against a session with no
	// UI attached.
	if shown.Shown {
		t.Error("shown = true, want false")
	}
	if shown.Reason != ReasonNoForegroundClient {
		t.Errorf("reason = %q, want %q", shown.Reason, ReasonNoForegroundClient)
	}
	assertParams(t, srv, MethodNotificationShow, map[string]any{
		"title": "tick 6pn blocked",
		"body":  "epic zz0 implement",
		"sound": "request",
	})
}

func TestNotificationShowRejectsBadParamsBeforeDialling(t *testing.T) {
	c, srv := newTestClient(t, nil)

	if _, err := c.NotificationShow(t.Context(), NotificationShowParams{Sound: SoundDone}); err == nil {
		t.Error("an empty title was accepted; herdr's schema requires one")
	}
	// `sound` is a closed enum in the schema, so a typo must fail here
	// rather than come back as an opaque invalid_request.
	if _, err := c.NotificationShow(t.Context(), NotificationShowParams{
		Title: "hi", Sound: NotificationSound("chime"),
	}); err == nil {
		t.Error("an unknown sound was accepted")
	}
	for _, req := range srv.Requests() {
		if req.Method == MethodNotificationShow {
			t.Fatal("an invalid notification reached the server")
		}
	}
}

// assertParams checks the JSON params the client sent for a method.
func assertParams(t *testing.T, srv *fakeServer, method string, want map[string]any) {
	t.Helper()
	for _, req := range srv.Requests() {
		if req.Method != method {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(req.Params, &got); err != nil {
			t.Fatalf("decoding %s params %s: %v", method, req.Params, err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%s params = %s, want %s", method, gotJSON, wantJSON)
		}
		return
	}
	t.Fatalf("no %s request recorded", method)
}
