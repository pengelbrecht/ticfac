package factory

// The claude-sub pool's operator read (tick b13): /api/claude-sub fetched
// under the operator's own token, and the two derivations the dashboard's
// cost line makes from the answer — which subscriptions this run's jobs
// lease, and what the proxy last saw of the account's shared windows. The
// fixture bodies copy the identity strings the real route serves
// (cloudflare/test/claude-sub-routes.test.ts: labels MAX1/MAX2, leases keyed
// by sandbox name) and the utilization spellings the spike measured on
// staging (docs/spikes/jvj-claude-sub-cloud.md).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchClaudeSubReadsThePoolSnapshot(t *testing.T) {
	t.Parallel()
	// The real answer's shape, labels in the pool's own sorted order and a
	// lease keyed by a sandbox name — never a token value anywhere.
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
					"active_leases": ["run_abc12346-46x-1", "run_abc12346-7zk-1"],
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
		t.Errorf("MAX1's leases are %v, want the two sandbox names", view.ActiveLeases)
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

func TestLeasedLabelsMatchTheRunOwnJobs(t *testing.T) {
	t.Parallel()
	snapshot := &ClaudeSubSnapshot{
		Labels: []string{"MAX1", "MAX2"},
		Subscriptions: []ClaudeSubView{
			{Label: "MAX1", ActiveLeases: []string{"run_abc1-46x-1", "other_run-7zk-1"}},
			{Label: "MAX2", ActiveLeases: []string{"run_abc12-46x-1"}},
			{Label: "MAX3", ActiveLeases: nil},
		},
	}
	got := snapshot.LeasedLabels("run_abc1")
	if len(got) != 1 || got[0] != "MAX1" {
		t.Errorf("run_abc1's leases are %v, want MAX1 alone (the separator keeps run_abc12's lease from matching)", got)
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
	snapshot.Subscriptions[2].ActiveLeases = []string{"run_abc1-9zz-2"}
	got = snapshot.LeasedLabels("run_abc1")
	if len(got) != 2 || got[0] != "MAX1" || got[1] != "MAX3" {
		t.Errorf("two leased labels read %v, want MAX1 then MAX3", got)
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
