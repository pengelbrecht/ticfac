package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The factory's GitHub token door, from the forge's side (epic dm6): a cloud
// run on the App rung boots with an installation token GitHub kills an hour
// later, so every REST call the close-out makes must carry the door's CURRENT
// token — asked with the run's own credential, held until five minutes before
// it expires, and never letting a door outage turn into a failed call while
// the boot token still works.

func fakeFactoryDoor(t *testing.T, now *time.Time) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tkr_placeholder_run" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_door_" + string(rune('0'+n)),
			"expires_at": now.Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestFactoryTokenSourceHoldsATokenUntilFiveMinutesBeforeItExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	door, calls := fakeFactoryDoor(t, &now)
	source := FactoryTokenSource(door.URL, "tkr_placeholder_run", nil, func() time.Time { return now })

	first, err := source(context.Background())
	if err != nil || first != "ghs_door_1" {
		t.Fatalf("first = %q, %v; want ghs_door_1", first, err)
	}
	now = now.Add(50 * time.Minute)
	if again, _ := source(context.Background()); again != "ghs_door_1" || calls.Load() != 1 {
		t.Fatalf("at 50m got %q after %d door calls; want the held token and no second call", again, calls.Load())
	}
	now = now.Add(6 * time.Minute) // 56m: inside the five-minute margin
	if fresh, _ := source(context.Background()); fresh != "ghs_door_2" {
		t.Fatalf("inside the margin got %q; want a fresh token", fresh)
	}
}

func TestTheForgeSpeaksWithTheDoorsTokenAndFallsBackToTheBootToken(t *testing.T) {
	var seen []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	t.Cleanup(api.Close)

	now := time.Now()
	door, _ := fakeFactoryDoor(t, &now)
	live := GitHub{Token: "ghs_boot", Repo: "example-org/example-repo", API: api.URL,
		Refresh: FactoryTokenSource(door.URL, "tkr_placeholder_run", nil, nil)}
	if _, err := live.Find(context.Background(), "epic/x", "main"); err != nil {
		t.Fatal(err)
	}
	down := GitHub{Token: "ghs_boot", Repo: "example-org/example-repo", API: api.URL,
		Refresh: FactoryTokenSource(door.URL, "tkr_wrong", nil, nil)}
	if _, err := down.Find(context.Background(), "epic/x", "main"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "Bearer ghs_door_1" || seen[1] != "Bearer ghs_boot" {
		t.Fatalf("GitHub saw %v; want the door's token, then the boot token when the door refused", seen)
	}
}

func TestFactoryTokenSourceFromEnvIsNilWithoutTheDoor(t *testing.T) {
	t.Setenv(FactoryTokenURLEnv, "")
	t.Setenv(FactoryRunTokenEnv, "tkr_placeholder_run")
	if FactoryTokenSourceFromEnv() != nil {
		t.Fatal("a process with no TICKS_GITHUB_TOKEN_URL got a refreshing token source")
	}
	t.Setenv(FactoryTokenURLEnv, "https://factory.example.invalid/api/github/token")
	if FactoryTokenSourceFromEnv() == nil {
		t.Fatal("a process handed the door got no refreshing token source")
	}
}
