package herdr

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The interrupt is the harness's own (interrupt.go). epic-6in (2026-09-28):
// two pi workers ran past their wall clock, the executor re-sent ctrl+c at
// every poll — which pi reads as "clear the editor" — and 46x attempt 2 was
// only stopped by the pane close two minutes later. The pi kind itself is
// deleted (epic 43y, tick uxi); its verified interrupt stays recorded in
// interrupt.go's history for the day somebody round-trips a kind whose
// interrupt is not the generic chord.

// TestInterruptKeysPerKind pins the key sequence each verified harness is
// interrupted with, and the generic chord an unverified kind keeps.
//
// short: a pure table lookup; no harness, no process, no git.
func TestInterruptKeysPerKind(t *testing.T) {
	for kind, want := range map[string][]string{
		"claude": {"esc"},
		"codex":  {"esc"},
		"":       {"esc"}, // the executor's default kind is claude
		"Claude": {"esc"},
		"gemini": {"ctrl+c"},
		"amp":    {"ctrl+c"},
		// The deleted pi kind keeps the generic chord, not its verified
		// Escape: nothing verifies a key for a kind the executor can no
		// longer be given, and the verified entry must not survive as a
		// reason to add the kind back (epic 43y, tick uxi).
		"pi": {"ctrl+c"},
	} {
		if got := interruptKeys(kind); !reflect.DeepEqual(got, want) {
			t.Errorf("interruptKeys(%q) = %v, want %v", kind, got, want)
		}
	}
	// The table is not handed out by reference: a caller that appends to
	// the keys must not change what the next stop sends.
	keys := interruptKeys("claude")
	keys[0] = "ctrl+c"
	if got := interruptKeys("claude"); got[0] != "esc" {
		t.Errorf("mutating a returned sequence changed the table: claude now interrupts with %v", got)
	}
}

// TestCancelSendsEachKindsOwnInterrupt drives Cancel through the fake herdr
// for each kind and asserts the keys herdr was asked to send.
func TestCancelSendsEachKindsOwnInterrupt(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, tc := range []struct {
		kind string
		want string
	}{
		{"claude", "esc"},
		{"codex", "esc"},
		// The deleted pi kind gets the generic chord like every unverified
		// kind — its verified Escape went with the kind (epic 43y, uxi).
		{"pi", "ctrl+c"},
		{"gemini", "ctrl+c"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			h := newHarness(t, harnessOptions{kind: tc.kind})
			handle, err := h.start("t1")
			if err != nil {
				t.Fatal(err)
			}
			h.setStatus("working")
			if _, err := h.ex.Cancel(handle); err != nil {
				t.Fatal(err)
			}
			sent := h.server.SendKeysCalls()
			if len(sent) != 1 {
				t.Fatalf("agent.send_keys was called %d times, want one interrupt", len(sent))
			}
			if got := strings.Join(sent[0].Keys, " "); got != tc.want {
				t.Errorf("a %s agent was interrupted with %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestAnEscapeOnlyAgentStopsWithinOnePoll is epic-6in's 46x attempt 2,
// repaired, stated against a kind the executor still runs: a claude agent
// past its wall clock is sent Escape — its own interrupt — and, back at its
// prompt, is closed at the very next poll rather than after the two-minute
// grace (it will never exit or report on its own). The uncommitted work is
// snapshotted before the close, as for any close.
func TestAnEscapeOnlyAgentStopsWithinOnePoll(t *testing.T) {
	h := newHarness(t, harnessOptions{kind: "claude", spawnAgent: true, agentMode: "esc_only"})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, "uncommitted.txt"), []byte("an hour of work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ---- the bound fires: the interrupt is pi's Escape
	clock.advance(301 * time.Second)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Fatalf("state = %s at the first poll past the bound, want running: the interrupt was just delivered", status.State)
	}
	sent := h.server.SendKeysCalls()
	if len(sent) != 1 || strings.Join(sent[0].Keys, " ") != "esc" {
		t.Fatalf("send_keys calls = %+v, want exactly one, of esc: pi's app.interrupt", sent)
	}

	// The agent honours it: back at its prompt, still running.
	if !waitForOr(t, "the pi agent to return to its prompt", 5*time.Second, func() bool {
		return h.currentStatus() == "idle"
	}) {
		h.dumpAgent(t)
		t.Fatal("the esc_only agent never honoured the Escape it was sent")
	}
	if _, exited, _ := h.agentProcess(); exited {
		t.Fatal("the agent exited on the interrupt: the fixture must stay at its prompt, as pi does")
	}

	// ---- ONE poll later, well inside the grace: the close
	clock.advance(PollInterval)
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed || !status.Terminal {
		t.Fatalf("state = %s (terminal %t) one poll after an honoured interrupt, want failed and terminal: "+
			"an agent back at its prompt is not waited on for the grace", status.State, status.Terminal)
	}
	detail := lastDetail(status.Observations)
	if !strings.Contains(detail, wallWording) || !strings.Contains(detail, "honoured the interrupt") {
		t.Errorf("the settlement reads %q, want the wall-clock wording naming the honoured interrupt", detail)
	}
	if strings.Contains(detail, "unhonoured") {
		t.Errorf("the settlement reads %q: an honoured interrupt must not be recorded as unhonoured", detail)
	}
	if closes := h.paneCloses(); len(closes) != 1 {
		t.Fatalf("pane.close calls = %v, want exactly one", closes)
	}
	if n := len(h.server.SendKeysCalls()); n != 1 {
		t.Errorf("send_keys was called %d times, want the one interrupt: the second poll closes, it does not re-send", n)
	}
	snap, ok := h.ex.storeAt(local.State).wipSnapshot()
	if !ok {
		t.Fatal("the pane was closed with no wip snapshot taken first")
	}
	if got, ok := showFile(local.Worktree, snap.Commit, "uncommitted.txt"); !ok || got != "an hour of work" {
		t.Errorf("the snapshot's uncommitted.txt reads %q (present %t), want the work as it stood", got, ok)
	}
	if !waitForOr(t, "the closed pane's agent process to exit", 5*time.Second, func() bool {
		_, exited, _ := h.agentProcess()
		return exited
	}) {
		t.Fatal("the pane was closed but the agent process is still running")
	}
}

// TestAnUnverifiedKindKeepsTheGraceForAnAgentStillWorking is the control:
// the same pi-like agent under a kind nobody has verified is sent ctrl+c,
// which it does not honour, so it is still working one poll later and the
// grace — not the honoured-interrupt close — governs it.
func TestAnUnverifiedKindKeepsTheGraceForAnAgentStillWorking(t *testing.T) {
	h := newHarness(t, harnessOptions{kind: "gemini", spawnAgent: true, agentMode: "esc_only"})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(301 * time.Second)
	if _, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // the fixture's 50ms loop has read the keys
	clock.advance(PollInterval)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("state = %s, want running: a working agent inside the grace is not closed", status.State)
	}
	if closes := h.paneCloses(); len(closes) != 0 {
		t.Errorf("the pane was closed at %v inside the grace for an agent still working", closes)
	}
	for _, call := range h.server.SendKeysCalls() {
		if got := strings.Join(call.Keys, " "); got != "ctrl+c" {
			t.Errorf("an unverified kind was interrupted with %q, want herdr's generic ctrl+c", got)
		}
	}
}
