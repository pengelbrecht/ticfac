package factory

// The claude-sub pool's operator read (tick b13): /api/claude-sub fetched
// under the operator's own token, and the two derivations the dashboard's
// cost line makes from the answer — which subscriptions this run's jobs
// lease, and what the proxy last saw of the account's shared windows. The
// fixture bodies copy the identity strings the real route serves
// (cloudflare/test/claude-sub-routes.test.ts: labels MAX1/MAX2) and the lease
// and utilization spellings the factory's own suite asserts
// (cloudflare/test/sandbox-dispatch.test.ts and run-workflow.test.ts: a
// worker dispatch's lease is its job id, the review boot's is its sandbox
// name; docs/spikes/jvj-claude-sub-cloud.md measured the windows).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestFetchClaudeSubReadsThePoolSnapshot(t *testing.T) {
	t.Parallel()
	// The real answer's shape: labels in the pool's own sorted order, and
	// leases under the two spellings the factory keys a run's own jobs by —
	// the door's job id for a worker dispatch, the sandbox name for the
	// review boot — and never a token value anywhere.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/claude-sub" {
			t.Errorf("the read hit %s, want /api/claude-sub", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer op-token" {
			t.Errorf("the read authenticated with %q, want the operator's bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"labels": ["MAX1", "MAX2"],
			"subscriptions": [
				{
					"label": "MAX1",
					"active_leases": ["run-run_abc12346/tick-46x/attempt-1", "run_abc12346-1"],
					"benched": null,
					"requests": 272,
					"limited": 0,
					"last_status": 200,
					"last_limits": {
						"anthropic-ratelimit-unified-5h-utilization": "0.34",
						"anthropic-ratelimit-unified-7d-utilization": "0.08"
					}
				},
				{
					"label": "MAX2",
					"active_leases": [],
					"benched": {"until": 1760000000000, "reason": "quota", "detail": "the window is spent"},
					"requests": 0,
					"limited": 0,
					"last_status": null,
					"last_limits": {}
				}
			]
		}`))
	}))
	defer srv.Close()

	snapshot, err := FetchClaudeSub(context.Background(), srv.Client(), srv.URL, "op-token")
	if err != nil {
		t.Fatalf("FetchClaudeSub: %v", err)
	}
	if len(snapshot.Labels) != 2 || snapshot.Labels[0] != "MAX1" || snapshot.Labels[1] != "MAX2" {
		t.Errorf("the labels are %v, want MAX1 and MAX2", snapshot.Labels)
	}
	if len(snapshot.Subscriptions) != 2 {
		t.Fatalf("the snapshot carries %d subscriptions, want 2", len(snapshot.Subscriptions))
	}
	view := snapshot.ViewOf("MAX1")
	if view == nil {
		t.Fatal("the snapshot carries no MAX1 view")
	}
	if len(view.ActiveLeases) != 2 {
		t.Errorf("MAX1's leases are %v, want the worker job's id and the review boot's sandbox name", view.ActiveLeases)
	}
	fiveHour, sevenDay := view.WindowUtilization()
	if fiveHour == nil || *fiveHour != 0.34 || sevenDay == nil || *sevenDay != 0.08 {
		t.Errorf("the windows read (%v, %v), want 0.34 and 0.08", fiveHour, sevenDay)
	}
}

func TestFetchClaudeSubRefusalsAreTheOptionalStateOrAFailure(t *testing.T) {
	t.Parallel()
	t.Run("a deployment that binds no pool", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error": "no_claude_sub_pool", "detail": "this deployment binds no CLAUDE_SUB_POOL"}`))
		}))
		defer srv.Close()
		if _, err := FetchClaudeSub(context.Background(), srv.Client(), srv.URL, "op-token"); !errors.Is(err, ErrNoClaudeSubPool) {
			t.Errorf("a pool-less factory answers %v, want ErrNoClaudeSubPool", err)
		}
	})
	t.Run("a factory that predates the route", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error": "not_found"}`))
		}))
		defer srv.Close()
		if _, err := FetchClaudeSub(context.Background(), srv.Client(), srv.URL, "op-token"); !errors.Is(err, ErrNoClaudeSubPool) {
			t.Errorf("a route-less factory answers %v, want ErrNoClaudeSubPool", err)
		}
	})
	t.Run("a rejected token is a failure, never silence", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": "unauthorized"}`))
		}))
		defer srv.Close()
		_, err := FetchClaudeSub(context.Background(), srv.Client(), srv.URL, "op-token")
		if err == nil || errors.Is(err, ErrNoClaudeSubPool) {
			t.Errorf("a rejected token answers %v, want a real error", err)
		}
	})
	t.Run("no URL or no token configured", func(t *testing.T) {
		if _, err := FetchClaudeSub(context.Background(), nil, "", "op-token"); !errors.Is(err, ErrNoClaudeSubPool) {
			t.Errorf("no URL answers %v, want ErrNoClaudeSubPool", err)
		}
		if _, err := FetchClaudeSub(context.Background(), nil, "https://factory.example.com", ""); !errors.Is(err, ErrNoClaudeSubPool) {
			t.Errorf("no token answers %v, want ErrNoClaudeSubPool", err)
		}
	})
}

// TestLeasedLabelsMatchTheRunOwnJobs: the pool keys a lease by the job id
// of the job that took it, and a run's own jobs arrive under two spellings —
// the door's worker job ids (`run-<run>/tick-<tick>/…`, with the role jobs'
// variants and the base fold's under the same `run-<run>/` prefix the
// attempt protocol bounds job_id by) and the review boot's sandbox name
// (`<run>-<boot>`, the one job that leases under its container's name). The
// fixtures spell both exactly as the factory's own suite asserts them:
// cloudflare/test/sandbox-dispatch.test.ts pins a worker dispatch's
// active_leases to attemptJobID(…), run-workflow.test.ts pins the review
// boot's to sandboxName(…). b13's first delivery hand-typed `<run>-<tick>-
// <attempt>` — a shape no real lease carries — so its matcher read only a
// review boot's lease and MISSED every worker lease of a real claude-sub
// run: green on the fake, silent on the run.
func TestLeasedLabelsMatchTheRunOwnJobs(t *testing.T) {
	t.Parallel()
	snapshot := &ClaudeSubSnapshot{
		Labels: []string{"MAX1", "MAX2"},
		Subscriptions: []ClaudeSubView{
			{Label: "MAX1", ActiveLeases: []string{
				"run-run_abc1/tick-46x/attempt-1",   // this run's worker dispatch
				"run-run_abc1/tick-46x/repair-1-r2", // its gate repair's role job
				"run-run_abc1/base-fold-2",          // its base fold's job
				"run_abc1-1",                        // its review boot's sandbox name
				"run-run_abc12/tick-46x/attempt-1",  // another run's worker dispatch
				"other_run-7zk-1",                   // another run's review boot
				"scratch-review",                    // the staging door, the operator's own name
			}},
			{Label: "MAX2", ActiveLeases: []string{"run_abc12-1"}},
			{Label: "MAX3", ActiveLeases: nil},
		},
	}
	got := snapshot.LeasedLabels("run_abc1")
	if len(got) != 1 || got[0] != "MAX1" {
		t.Errorf("run_abc1's leases are %v, want MAX1 alone (its four own jobs; the separators keep run_abc12's, another run's boot and the scratch name out)", got)
	}
	// The other run reads its own two: its worker dispatch (MAX1) and its
	// review boot (MAX2).
	got = snapshot.LeasedLabels("run_abc12")
	if len(got) != 2 || got[0] != "MAX1" || got[1] != "MAX2" {
		t.Errorf("run_abc12's leases are %v, want MAX1 then MAX2", got)
	}
	if got := snapshot.LeasedLabels("nobody"); got != nil {
		t.Errorf("a run with no leases reads %v, want none", got)
	}
	var empty *ClaudeSubSnapshot
	if got := empty.LeasedLabels("run_abc1"); got != nil {
		t.Errorf("a nil snapshot reads %v, want none", got)
	}
	if got := snapshot.LeasedLabels(""); got != nil {
		t.Errorf("an empty run id reads %v, want none", got)
	}
	// Multiple labels leased: every one, in the snapshot's order.
	snapshot.Subscriptions[2].ActiveLeases = []string{"run-run_abc1/tick-9zz/attempt-2"}
	got = snapshot.LeasedLabels("run_abc1")
	if len(got) != 2 || got[0] != "MAX1" || got[1] != "MAX3" {
		t.Errorf("two leased labels read %v, want MAX1 then MAX3", got)
	}
}

// TestLeasedLabelsMatchOnlyTheSpellingsTheFactoryMints: the matcher's rule
// — "the two spellings the factory keys a run's own jobs by" — is the whole
// of it. The factory mints no lease under the BARE run id: the door's job ids
// are `run-<run>/…` (attemptJobID, and a spec's own job_id is bounded under
// the same prefix), the review boot's is `<run>-<boot>` (sandboxName), and
// the staging door leases under the sandbox name its caller picked, which
// cannot even spell a run id (its route binds `[a-z0-9-]`, and a run id
// carries an underscore). A lease that reads as the bare run id is a shape
// nobody writes, and a matcher that claims it is the same forgiving fixture
// this suite exists to refuse — it reads another job's lease as this run's
// on a spelling the factory cannot produce.
func TestLeasedLabelsMatchOnlyTheSpellingsTheFactoryMints(t *testing.T) {
	t.Parallel()
	const runID = "run_abc1"
	snapshot := &ClaudeSubSnapshot{Subscriptions: []ClaudeSubView{{
		Label:        "MAX1",
		ActiveLeases: []string{runID},
	}}}
	if got := snapshot.LeasedLabels(runID); got != nil {
		t.Errorf("a lease spelled the bare run id %q read as this run's: %v — the factory mints no lease under it (the door's job ids are run-%s/…, the review boot's is %s-<boot>), so it is another job's",
			runID, got, runID, runID)
	}
}

// TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints: the matcher's two
// spellings are the factory's own, and neither language imports the other —
// the same seam the subscription rung guards
// (internal/profile/claude_sub_parity_test.go) — so this guard reads both
// spellings out of the factory's source and matches ids the factory itself
// would mint, for this run and for another. A change on either side that
// the matcher does not follow fails HERE, on the table — not on a real
// claude-sub run whose watch stays silent while its jobs hold leases.
func TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints(t *testing.T) {
	t.Parallel()
	// The door's job id, minted by attemptJobID (cloudflare/src/
	// sandbox-executor.ts): the id the pool keys a worker dispatch's lease by.
	executorSrc, err := os.ReadFile(payloadPath(t, "cloudflare", "src", "sandbox-executor.ts"))
	if err != nil {
		t.Fatalf("reading the factory's executor: %v", err)
	}
	jobID := regexp.MustCompile("export function attemptJobID[^`]*`([^`]+)`").FindSubmatch(executorSrc)
	if jobID == nil {
		t.Fatal("cloudflare/src/sandbox-executor.ts no longer spells attemptJobID's template — " +
			"the pool's worker-lease spelling moved, and this guard must move with it")
	}
	// The sandbox name, minted by sandboxName (cloudflare/src/sandbox.ts):
	// the name the review boot keys its lease by (run-workflow.ts).
	sandboxSrc, err := os.ReadFile(payloadPath(t, "cloudflare", "src", "sandbox.ts"))
	if err != nil {
		t.Fatalf("reading the factory's sandbox: %v", err)
	}
	sandboxName := regexp.MustCompile("export function sandboxName[^`]*`([^`]+)`").FindSubmatch(sandboxSrc)
	if sandboxName == nil {
		t.Fatal("cloudflare/src/sandbox.ts no longer spells sandboxName's template — " +
			"the review boot's lease spelling moved, and this guard must move with it")
	}

	const runID = "run_6a4b8e0f2c1d5f3a"
	const tickID = "46x"
	const otherRun = runID + "f" // one hex digit longer: the near miss
	spell := func(template, run string) string {
		id := strings.ReplaceAll(template, "${runID}", run)
		id = strings.ReplaceAll(id, "${tickID}", tickID)
		return strings.ReplaceAll(id, "${attempt}", "1")
	}

	// THIS run's worker job and review boot, spelled by the factory's own
	// templates, are this run's leases — EACH on its own, so one spelling
	// drifting alone is the failure it is, not a wash beside the other.
	for _, lease := range []struct {
		name  string
		id    string
		label string
	}{
		{"the door's worker job id", spell(string(jobID[1]), runID), "MAX1"},
		{"the review boot's sandbox name", spell(string(sandboxName[1]), runID), "MAX1"},
	} {
		own := &ClaudeSubSnapshot{Subscriptions: []ClaudeSubView{{Label: lease.label, ActiveLeases: []string{lease.id}}}}
		if got := own.LeasedLabels(runID); len(got) != 1 || got[0] != lease.label {
			t.Errorf("the factory's own %s %q did not read as %s's lease", lease.name, lease.id, runID)
		}
	}
	// The SAME spellings for another run are not this run's.
	foreign := &ClaudeSubSnapshot{Subscriptions: []ClaudeSubView{{Label: "MAX1", ActiveLeases: []string{
		spell(string(jobID[1]), otherRun), spell(string(sandboxName[1]), otherRun)}}}}
	if got := foreign.LeasedLabels(runID); got != nil {
		t.Errorf("another run's leases read as %s's: %v (want none — the separators are the whole match)", runID, got)
	}
}

func TestWindowUtilizationReadsTheHeadersTheProxySaw(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		limits             map[string]string
		fiveHour, sevenDay *float64
	}{
		{"the spike's decimal fractions", map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "0.12",
			"anthropic-ratelimit-unified-7d-utilization": "0.06",
		}, ptrFraction(0.12), ptrFraction(0.06)},
		{"a window the proxy has not answered for", map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "0.4",
		}, ptrFraction(0.4), nil},
		{"no headers at all — no measurement, not zero", map[string]string{}, nil, nil},
		{"a percent spelling", map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "40%",
		}, ptrFraction(0.4), nil},
		{"a value outside a fraction's range is refused", map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "1.5",
		}, nil, nil},
		{"an unparsable value is refused", map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": "soon",
		}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := &ClaudeSubView{Label: "MAX1", LastLimits: tc.limits}
			fiveHour, sevenDay := view.WindowUtilization()
			if !sameFraction(fiveHour, tc.fiveHour) || !sameFraction(sevenDay, tc.sevenDay) {
				t.Errorf("the windows read (%v, %v), want (%v, %v)", fiveHour, sevenDay, tc.fiveHour, tc.sevenDay)
			}
		})
	}
}

func ptrFraction(v float64) *float64 { return &v }

func sameFraction(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
