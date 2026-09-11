package herdr

import (
	"encoding/json"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The handle: the one open object in the contract, and the only place herdr
// addressing lives. What has to hold for the seam to work:
//
//   - a handle naming another executor is refused, so one tick's job can
//     never be addressed by another executor;
//   - the MINIMAL handle the reconciler constructs on adoption and teardown —
//     {"state": …} and nothing else — resolves to full addressing through the
//     attempt record, because that is the shape a restarted controller holds;
//   - an agent name is a legal herdr name and unique per attempt.

func TestLocalRefusesAHandleFromAnotherExecutor(t *testing.T) {
	h := &subprocess.JobHandle{Executor: subprocess.ExecutorName, Handle: map[string]any{"state": "/tmp/x"}}
	if _, err := local(h); err == nil {
		t.Fatal("a handle naming the local-subprocess executor decoded as this executor's: " +
			"one executor addressing another's job is the 'collected under another tick's name' failure")
	}
}

func TestLocalRefusesAHandleWithNoState(t *testing.T) {
	if _, err := local(&subprocess.JobHandle{Executor: ExecutorName}); err == nil {
		t.Fatal("a handle carrying no state directory decoded: nothing can be re-addressed through it")
	}
}

func TestTheMinimalHandleResolvesThroughTheAttemptRecord(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	full, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// The reconciler's adoption and teardown shape: the state directory and
	// NOTHING else — reconstructed from durable facts, not from this
	// executor's private naming.
	minimal := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         handle.JobID,
		Attempt:       handle.Attempt,
		Executor:      ExecutorName,
		Handle:        map[string]any{"state": full.State},
	}
	min, err := local(minimal)
	if err != nil {
		t.Fatal(err)
	}
	resolved, record, err := min.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.WorkspaceID != full.WorkspaceID || resolved.AgentName != full.AgentName ||
		resolved.PaneID != full.PaneID || resolved.Worktree != full.Worktree {
		t.Errorf("the minimal handle resolved to %+v, want the record's own addressing %+v", resolved, full)
	}
	if record.JobID != handle.JobID {
		t.Errorf("resolved to job %s, want %s", record.JobID, handle.JobID)
	}
}

func TestTheHandleRoundTripsThroughJSON(t *testing.T) {
	// The handle is a durable record: it is written to the run's attempt
	// marker on origin and read back by a fresh clone. A handle that does
	// not survive its own JSON round trip is one nobody can re-address.
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(handle)
	if err != nil {
		t.Fatal(err)
	}
	var back subprocess.JobHandle
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	a, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	b, err := local(&back)
	if err != nil {
		t.Fatal(err)
	}
	if *a != *b {
		t.Errorf("the handle did not survive its JSON round trip: %+v != %+v", a, b)
	}
}

func TestAgentNameIsALegalHerdrName(t *testing.T) {
	for _, check := range []struct {
		tick    string
		attempt int
	}{
		{"3gv", 1}, {"2xu", 2}, {"AVeryLongTickIdentifierIndeed", 12},
	} {
		name := agentName(check.tick, check.attempt)
		if len(name) > 32 {
			t.Errorf("agentName(%q, %d) = %q: herdr names are at most 32 characters", check.tick, check.attempt, name)
		}
		for _, r := range name {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				t.Errorf("agentName(%q, %d) = %q: %q is not a legal herdr name character", check.tick, check.attempt, name, string(r))
			}
		}
		if name[0] < 'a' || name[0] > 'z' {
			t.Errorf("agentName(%q, %d) = %q: a herdr name starts with a letter", check.tick, check.attempt, name)
		}
	}
	if agentName("3gv", 1) == agentName("3gv", 2) {
		t.Error("two attempts of one tick got the same agent name: a second attempt whose first is still live would collide")
	}
}
