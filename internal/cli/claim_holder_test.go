package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The factory's half of "is the run holding this claim still running?" (the
// hn6 cloud-run stall): a run the factory records failed, or whose Workflow
// instance has ended, is DEAD — its claim is taken over — while a run the
// factory vouches for is alive, and one it cannot vouch for either way is
// unknown, never read as dead on a guess.
func TestTheFactorysRecordDecidesWhetherACloudClaimHolderIsDead(t *testing.T) {
	cases := []struct {
		state, workflow, want, says string
	}{
		// hn6's own shape: the record failed, the Workflow complete.
		{"failed", "complete", reconcile.HolderDead, "record says failed"},
		{"completed", "", reconcile.HolderDead, "record says completed"},
		{"stopped", "terminated", reconcile.HolderDead, "record says stopped"},
		// A record frozen at a live state by a supervisor that never wrote its
		// last word: the instance says what the record cannot.
		{"running", "errored", reconcile.HolderDead, "Workflow instance is errored"},
		{"stopping", "complete", reconcile.HolderDead, "Workflow instance is complete"},
		{"running", "running", reconcile.HolderAlive, "Workflow instance is running"},
		{"starting", "queued", reconcile.HolderAlive, "Workflow instance is queued"},
		// No instance the factory could read: neither alive nor dead.
		{"running", "", reconcile.HolderUnknown, "could read no Workflow instance"},
		{"", "", reconcile.HolderUnknown, "could read no Workflow instance"},
	}
	for _, c := range cases {
		got := cloudHolderVerdict(c.state, c.workflow)
		if got.Verdict != c.want || !strings.Contains(got.Evidence, c.says) {
			t.Errorf("record %q, instance %q: got %+v, want %s saying %q", c.state, c.workflow, got, c.want, c.says)
		}
	}
}

// The composed answer, against a fake factory: the run's own credential
// reaches GET /api/runs/<id>, a failed record is dead, a run the factory has
// never heard of is unknown, and an unreachable factory is unknown too — the
// claim is then held, as a wait, never taken over.
func TestAClaimHolderIsAskedOfTheFactoryOnTheRunsOwnCredential(t *testing.T) {
	var auth []string
	factory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/runs/run_dead":
			_, _ = w.Write([]byte(`{"run":{"run_id":"run_dead","state":"failed"},` +
				`"phase":{"state":"failed","workflow":{"id":"run_dead","status":"complete"}}}`))
		case "/api/runs/run_live":
			_, _ = w.Write([]byte(`{"run":{"run_id":"run_live","state":"running"},` +
				`"phase":{"state":"running","workflow":{"id":"run_live","status":"running"}}}`))
		case "/api/runs/run_down":
			http.Error(w, `{"error":"boom"}`, http.StatusBadGateway)
		default:
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		}
	}))
	defer factory.Close()
	t.Setenv("TICKS_FACTORY_URL", factory.URL)
	t.Setenv("TICKS_FACTORY_TOKEN", "tkr_run-scoped-token")

	ask := claimHolderLiveness(t.TempDir())
	ctx := context.Background()

	if got := ask(ctx, "run_dead"); got.Verdict != reconcile.HolderDead ||
		!strings.Contains(got.Evidence, "record says failed") || !strings.Contains(got.Evidence, "complete") {
		t.Errorf("a failed record with a complete Workflow reads %+v, want dead with the factory's evidence", got)
	}
	if got := ask(ctx, "run_live"); got.Verdict != reconcile.HolderAlive {
		t.Errorf("a running run reads %+v, want alive", got)
	}
	if got := ask(ctx, "run_unheard"); got.Verdict != reconcile.HolderUnknown ||
		!strings.Contains(got.Evidence, "has no run run_unheard") {
		t.Errorf("a run the factory never heard of reads %+v, want unknown: a local run from another checkout "+
			"is not dead because the factory does not know it", got)
	}
	if got := ask(ctx, "run_down"); got.Verdict != reconcile.HolderUnknown {
		t.Errorf("an unreachable factory reads %+v, want unknown", got)
	}
	for _, header := range auth {
		if header != "Bearer tkr_run-scoped-token" {
			t.Errorf("the factory was asked with %q, want the run's own credential", header)
		}
	}
}
