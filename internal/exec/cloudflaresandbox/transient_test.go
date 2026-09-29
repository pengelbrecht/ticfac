package cloudflaresandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Epic hn6's second cloud run: attempt 3's pre-start status read timed out
// waiting on the door ("Client.Timeout exceeded while awaiting headers") and
// the run halted over "a stop this run has no classification for" — a person
// was asked to retype past a door that was merely slow. A door that does not
// answer says of itself that it is a TRANSIENT remote failure, as a type the
// reconciler's supervisor reads without importing this package.

// transientRemote is the interface the supervisor asks (reconcile's
// errorStopReason), restated so this test pins the contract between them.
type transientRemote interface{ TransientRemote() bool }

func isTransient(err error) bool {
	var t transientRemote
	return errors.As(err, &t) && t.TransientRemote()
}

// short: an httptest door and an in-memory client.
func TestADoorThatDoesNotAnswerIsATransientRemoteFailure(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)

	client, err := NewClient(slow.URL, "run-token", 150*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.attemptStatus(context.Background(), "keh", 3, "run-r1/tick-keh/attempt-3")
	if err == nil {
		t.Fatal("a door that never answered produced a status")
	}
	if !isTransient(err) {
		t.Errorf("a door timeout is not typed transient: %v", err)
	}
	if _, isDoor := AsDoorError(err); isDoor {
		t.Errorf("a timeout was typed as the door's own refusal: %v", err)
	}

	// A door that is gone entirely: the same class.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	client, err = NewClient(dead.URL, "run-token", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.attemptStatus(context.Background(), "keh", 3, ""); !isTransient(err) {
		t.Errorf("an unreachable door is not typed transient: %v", err)
	}
}

// short: an httptest door and an in-memory client.
func TestOnlyTheEdgesFailuresAreTransientNeverTheDoorsAnswers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		transient bool
	}{
		{"the edge's gateway page", http.StatusBadGateway, "<html>bad gateway</html>", true},
		{"the edge's timeout page", http.StatusGatewayTimeout, "<html>timeout</html>", true},
		{"the door naming its own configuration", http.StatusServiceUnavailable,
			`{"error":"sandbox_dispatch_not_wired","detail":"no binding"}`, false},
		{"the door refusing the run", http.StatusConflict,
			`{"error":"lease_lost","detail":"the lease expired"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			door := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer door.Close()
			client, err := NewClient(door.URL, "run-token", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.attemptStatus(context.Background(), "keh", 1, "")
			if err == nil {
				t.Fatal("a refusal produced a status")
			}
			if got := isTransient(err); got != tc.transient {
				t.Errorf("transient = %v, want %v: %v", got, tc.transient, err)
			}
		})
	}
}
