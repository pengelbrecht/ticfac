package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The settle's factory witness (reconcile.Options.FactoryAttempt, tick bd5):
// the operator's machine holds none of a cloud attempt's state, so the release
// the hold's own printed command asks for is answered from the factory's
// records of the run and its worker container — one read, GET /api/runs/<id>,
// mapped to the same three things the executor's own Inspect would have said
// on the host that ran the attempt.
//
// short: a fake factory over one HTTP read; no repository, no run
func TestTheFactorysRecordAnswersASettleFromAHostThatNeverRanTheAttempt(t *testing.T) {
	factory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runs/run_dead":
			_, _ = w.Write([]byte(`{"run":{"run_id":"run_dead","state":"failed"},` +
				`"phase":{"state":"failed","workflow":{"id":"run_dead","status":"complete"}},` +
				`"settled_attempts":[` +
				`{"tick_id":"bd5","attempt":4,"state":"completed","exit_code":0,"at":"2026-10-04T13:06:00Z"},` +
				`{"tick_id":"bd5","attempt":5,"state":"completed","exit_code":1,"at":"2026-10-04T13:20:00Z"},` +
				`{"tick_id":"bd5","attempt":6,"state":"failed","exit_code":null,"at":"2026-10-04T13:24:00Z"}]}`))
		case "/api/runs/run_live":
			_, _ = w.Write([]byte(`{"run":{"run_id":"run_live","state":"running"},` +
				`"phase":{"state":"running","workflow":{"id":"run_live","status":"running"}},` +
				`"settled_attempts":[]}`))
		case "/api/runs/run_gone":
			_, _ = w.Write([]byte(`{"run":{"run_id":"run_gone","state":"failed"},` +
				`"phase":{"state":"failed","workflow":{"id":"run_gone","status":"complete"}},` +
				`"settled_attempts":[]}`))
		case "/api/runs/run_mute":
			_, _ = w.Write([]byte(`{"run":{"run_id":"run_mute","state":"running"},` +
				`"phase":{"state":"running","workflow":null}}`))
		case "/api/runs/run_down":
			http.Error(w, `{"error":"boom"}`, http.StatusBadGateway)
		default:
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		}
	}))
	defer factory.Close()
	t.Setenv("TICKS_FACTORY_URL", factory.URL)
	t.Setenv("TICKS_FACTORY_TOKEN", "tkr_run-scoped-token")
	ctx := context.Background()

	// A settlement the factory holds for the worker is TERMINAL, in the
	// executor's own vocabulary: exit 0 is succeeded, anything else failed.
	if got := factoryAttemptAnswer(ctx, "run_dead", "bd5", 4); !got.Asked || !got.Terminal ||
		got.State != subprocess.StateSucceeded {
		t.Errorf("a worker that completed with exit 0 reads %+v, want terminal and succeeded", got)
	}
	if got := factoryAttemptAnswer(ctx, "run_dead", "bd5", 5); !got.Asked || !got.Terminal ||
		got.State != subprocess.StateFailed {
		t.Errorf("a worker that completed with exit 1 reads %+v, want terminal and failed", got)
	}
	if got := factoryAttemptAnswer(ctx, "run_dead", "bd5", 6); !got.Asked || !got.Terminal ||
		got.State != subprocess.StateFailed ||
		!strings.Contains(got.Evidence, "failed with no exit code") {
		t.Errorf("a worker the factory recorded failed reads %+v, want terminal, failed, and the exit "+
			"named honestly", got)
	}

	// No settlement, and the run is still alive: the attempt is the run's to
	// address — never a person's to release behind its back.
	if got := factoryAttemptAnswer(ctx, "run_live", "bd5", 7); !got.Asked || !got.Live {
		t.Errorf("an attempt of a live run reads %+v, want live: A6 is not an operator's to waive", got)
	}

	// No settlement, and nothing will ever answer for the run: that is `lost`,
	// the one state a person may release.
	if got := factoryAttemptAnswer(ctx, "run_gone", "bd5", 7); !got.Asked || !got.Lost ||
		got.Terminal {
		t.Errorf("an attempt the factory can no longer answer for reads %+v, want lost", got)
	}

	// A factory that cannot vouch for the run either way is NOT asked: the
	// release refuses rather than ruling on a guess.
	for _, run := range []string{"run_mute", "run_down", "run_unheard"} {
		if got := factoryAttemptAnswer(ctx, run, "bd5", 7); got.Asked || got.Evidence == "" {
			t.Errorf("an uncertain factory (%s) reads %+v, want not asked and the reason said", run, got)
		}
	}
}

// The settle command carries the witness: the operator's `ticfac settle …
// --run-id run_…` builds its reconciler with Options.FactoryAttempt wired, so
// the release a hold prints is answerable on a host that never ran the attempt.
// A source scan, because the wiring is a literal: the behaviour it produces is
// pinned reconcile-side, and what needs pinning HERE is that the command a
// hold names does not lose the factory on its way to the reconciler.
//
// short: a source scan of the command's construction; no repository, no run
func TestTheSettleCommandAsksTheFactoryForTheAttemptItReleases(t *testing.T) {
	source, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatalf("read the cli package's own source: %v", err)
	}
	const literal = "FactoryAttempt:    factoryAttemptAnswer,"
	if !strings.Contains(string(source), literal) {
		t.Errorf("the settle command's reconcile.Options does not wire FactoryAttempt (%q): a hold's printed "+
			"command could not reach the factory that ran the attempt (tick bd5)", literal)
	}
}
