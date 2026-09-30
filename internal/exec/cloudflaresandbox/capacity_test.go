package cloudflaresandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// hn6's cloud run (2026-09-30): the door's start for a third worker waited for
// a container slot until the client gave up, and the supervisor could only
// read the timeout as remote_transient. The door now answers a start it has
// no room for at once, 503 no_capacity, and this client types it so the
// reconciler waits instead of stopping.

// capacityAnswer is the interface the reconciler asks (reconcile's
// isNoCapacity), restated so this test pins the contract between them.
type capacityAnswer interface{ NoCapacity() bool }

// short: an httptest door and an in-memory client.
func TestTheDoorsNoCapacityAnswerIsTypedAsAWaitNotATransientRemote(t *testing.T) {
	door := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no_capacity","detail":"3 of 3 container slots are held"}`))
	}))
	defer door.Close()

	client, err := NewClient(door.URL, "run-token", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.startAttempt(context.Background(), &startRequest{TickID: "keh", Attempt: 3})
	if err == nil {
		t.Fatal("a door with no room produced a handle")
	}
	var answer capacityAnswer
	if !errors.As(err, &answer) || !answer.NoCapacity() {
		t.Fatalf("the door's no_capacity answer is not typed as one: %v", err)
	}
	if isTransient(err) {
		t.Errorf("no_capacity was typed as a transient remote, which spends the continuation cap: %v", err)
	}

	// Every other refusal of the door is not a capacity answer.
	other := &doorError{Status: http.StatusConflict, Class: "lease_lost"}
	if other.NoCapacity() {
		t.Error("lease_lost was read as no_capacity")
	}
}

// short: pure arithmetic over an environment value.
func TestWorkerSlotsLeaveTheOrchestratorItsOwnContainer(t *testing.T) {
	for _, tc := range []struct {
		ceiling string
		want    int
	}{
		{"3", 2}, // the production ceiling: the orchestrator plus two workers
		{" 5 ", 4},
		{"1", 1}, // never zero once a ceiling is stated: the door still bounds it
		{"", 0},  // not inside an orchestrator container: no bound stated
		{"three", 0},
		{"0", 0},
	} {
		if got := workerSlots(tc.ceiling, ""); got != tc.want {
			t.Errorf("workerSlots(%q) = %d, want %d", tc.ceiling, got, tc.want)
		}
	}
}

// short: pure arithmetic over an environment value.
func TestWorkerSlotsGiveALocalOrchestratorTheWholeCeiling(t *testing.T) {
	// `ticfac run --cloud-workers`: the orchestrator is the operator's
	// machine, so no container of the ceiling is its own.
	for _, tc := range []struct {
		ceiling string
		want    int
	}{
		{"3", 3},
		{"12", 12},
		{"", 0}, // no ceiling stated: the door's no_capacity bounds it
	} {
		if got := workerSlots(tc.ceiling, OrchestratorLocal); got != tc.want {
			t.Errorf("workerSlots(%q, local) = %d, want %d", tc.ceiling, got, tc.want)
		}
	}
}
