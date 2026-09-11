package herdtest

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/herd/wirevocab"
)

// The fake's half of the wire vocabulary contract: the routing switch is the
// single source of truth for what the fake serves, and this test walks it
// against internal/herd/wirevocab/herd-vocabulary.json. The fake must speak
// the pinned words — never a private dialect that happens to satisfy the
// client tests.
//
// The method constants below (18) mirror the client's 19, minus
// pane.wait_for_output, which the fake deliberately does not serve: the
// client routes it, tests that need it stub it. worktree.list and
// events.wait have constants but no built-in handler — the routing switch
// answers them with invalid_request unless a test routes them itself, which
// is the documented wrinkle this test pins rather than papers over.

// fakeMethodConstants is every method name the fake can spell. It is checked
// against the contract's client_used so the two never drift apart.
func fakeMethodConstants() []string {
	return []string{
		MethodPing, MethodSessionSnapshot,
		MethodWorktreeCreate, MethodWorktreeList, MethodWorktreeRemove,
		MethodWorkspaceFocus,
		MethodAgentStart, MethodAgentPrompt, MethodAgentSendKeys, MethodAgentWait,
		MethodAgentList, MethodAgentGet,
		MethodPaneRead,
		MethodEventsSubscribe, MethodEventsWait,
		MethodPaneReportMetadata, MethodWorkspaceReportMetadata,
		MethodNotificationShow,
	}
}

func TestFakeMethodsAreClientVocabulary(t *testing.T) {
	v := wirevocab.MustLoad()
	for _, m := range fakeMethodConstants() {
		if !v.Methods.Has(m) {
			t.Errorf("fake method %q is not in the contract's client_used methods", m)
		}
	}
	// The one method the client speaks but the fake does not spell at all.
	if v.Methods.Has("pane.wait_for_output") {
		for _, m := range fakeMethodConstants() {
			if m == "pane.wait_for_output" {
				t.Fatal("the fake grew a pane.wait_for_output constant — fold it into the dial sweep below")
			}
		}
	}
}

func TestFakeRoutingMatchesContract(t *testing.T) {
	v := wirevocab.MustLoad()
	s := New(t, Config{})

	// Static pass: the routing switch itself, which is the fake's source of
	// truth. Every method must resolve to a built-in handler except the two
	// documented wrinkles — worktree.list and events.wait have constants but
	// no built-in — and ping, which handlerFor serves ahead of the switch.
	for _, m := range fakeMethodConstants() {
		s.mu.Lock()
		_, ok := s.builtin(m)
		s.mu.Unlock()
		want := m != MethodPing && m != MethodWorktreeList && m != MethodEventsWait
		if ok != want {
			t.Errorf("routing: builtin(%q) exists = %v, want %v", m, ok, want)
		}
	}

	// Dynamic pass: dial every method name over the socket with empty
	// params and demand the reply speak the contract — a result whose
	// discriminator (or error code) is a pinned word.
	for _, m := range fakeMethodConstants() {
		reply := dialOne(t, s, m)

		if m == MethodWorktreeList || m == MethodEventsWait {
			// The documented wrinkle: unrouted constants answer
			// invalid_request, exactly as an unknown method would.
			if reply.Error == nil {
				t.Errorf("%s: expected an error reply, got result %v", m, reply.Result)
			} else if reply.Error.Code != CodeInvalidRequest || reply.Error.Message != "unexpected method "+m {
				t.Errorf("%s: error = %+v, want unexpected-method invalid_request", m, reply.Error)
			}
			continue
		}

		if reply.Error != nil {
			if !v.ErrorCodes.Has(reply.Error.Code) {
				t.Errorf("%s: fake replies with error code %q, which the contract does not pin", m, reply.Error.Code)
			}
			continue
		}
		var result struct {
			Type string `json:"type"`
		}
		raw, err := json.Marshal(reply.Result)
		if err != nil {
			t.Fatalf("%s: marshal result: %v", m, err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Errorf("%s: result is not a typed object: %v", m, err)
			continue
		}
		if !v.ResultDiscriminators.Has(result.Type) {
			t.Errorf("%s: fake replies with result discriminator %q, which the contract does not pin", m, result.Type)
		}
	}
}

// rawReply is the fake's reply envelope, read raw off the socket.
type rawReply struct {
	ID     string         `json:"id"`
	Result any            `json:"result"`
	Error  *rawReplyError `json:"error"`
}

type rawReplyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// dialOne opens a connection, sends one request with empty params — the
// fake serves one request per connection — and reads the first reply line.
func dialOne(t *testing.T, s *Server, method string) rawReply {
	t.Helper()
	conn, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatalf("dial %s: %v", s.Path(), err)
	}
	defer func() { _ = conn.Close() }()

	req, err := json.Marshal(map[string]any{"id": "vocab", "method": method, "params": map[string]any{}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatalf("%s: write: %v", method, err)
	}

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("%s: read reply: %v", method, err)
	}
	var reply rawReply
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		t.Fatalf("%s: reply is not JSON: %v\n%s", method, err, line)
	}
	return reply
}
