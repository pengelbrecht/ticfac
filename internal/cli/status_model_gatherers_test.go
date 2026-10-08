package cli

// The modelGatherers seam, pinned as ONE thing (the b13 × 93n resolution).
//
// Ticks 93n (the worker activity line) and b13 (the cost line's leased
// claude-sub subscription) grew the SAME per-frame source policy from the
// SAME base: each added a reader to modelGatherers, each wired that reader at
// the same call sites (the one-shot surfaces, the cloud gathering, the
// watch's own gather), and each gave the watch a cache for it. Their merge
// met seven conflict markers over three files, and the resolution is a union
// at every one: both readers, both caches, side by side.
//
// Neither tick's own tests could see the PAIR, because each was written
// against a tree that carried only its own half — 93n's wiring test pins the
// local RemoteActivity reader and b13's pins the cloud subscription, and no
// test anywhere asks one gathering for both. That is the one obligation the
// merged tree owes the seam and no parent could have paid: these pins hold
// it, so the next tick that grows a third reader onto this struct cannot
// quietly drop a first one at a call site, and a reader wired into the
// gathering but never reached is a bug a test names rather than a frame a
// person reads.
//
// The wave, not the text, was the defect: two ticks in one wave writing one
// policy struct is a partition the epic paid for in a resolve job. The pin
// is the durable half of that lesson — the seam now says what it owns, and
// whoever grows it next re-runs these.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// bothReadersFixture is the smallest world a CLOUD gathering can be asked for
// both readers in: a configured factory whose only routes are the run's own
// feed and the pool's operator snapshot, a client on it, and a bare
// repository to stand for the checkout. Every source the gathering reads is
// best-effort — the wiring is what is under test, never what any source
// answers.
func bothReadersFixture(t *testing.T, runID string, claudeSub func(cloudFactoryRequest) (int, any)) (*cloudClient, string, *[]cloudFactoryRequest) {
	t.Helper()
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{"run_id": runID, "epic": "cst", "state": "running"}}}
		case "/api/runs/" + runID:
			return 200, map[string]any{"run": map[string]any{"run_id": runID, "epic": "cst", "state": "running"}}
		case "/api/runs/" + runID + "/events":
			return 200, map[string]any{"run_id": runID, "state": "running", "text": "", "bytes": 0, "total_bytes": 0}
		case "/api/claude-sub":
			if claudeSub != nil {
				return claudeSub(request)
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	client, err := newCloudClient()
	if err != nil {
		t.Fatalf("the configured factory would not open a client: %v", err)
	}
	repo := t.TempDir()
	execTestCmd(t, repo, "git", "init", "--quiet", "-b", "main")
	// The tracker is faked at its own seam (the convention every status
	// wiring test holds): the graph's derivation is pinned in
	// internal/statusmodel, and a shell-out to tk is not what this tests.
	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph { return nil }
	return client, repo, requests
}

// TestCloudStatusModelGathersActivityAndTheLeasedSubscription is the union
// itself: ONE cloud gathering, asked for BOTH readers — 93n's activity
// dispatcher handed the cloud client and keyed by the tick and attempt, and
// b13's leased-subscription read — and both observable in the one Sources
// every surface renders. A resolution that took either side's line would
// fail here with the reader it dropped, and a future edit that unwires one
// of them at this call site fails here with the frame it cost.
func TestCloudStatusModelGathersActivityAndTheLeasedSubscription(t *testing.T) {
	captured := captureStatusSources(t)
	const runID = "run_6a4b8e0f2c1d5f3a"
	client, repo, _ := bothReadersFixture(t, runID, nil)

	// Both readers are stubs: the wiring under test is what the gathering
	// PASSES and WHO it calls, never what the factory would answer.
	calls := map[string]int{}
	handed := (*cloudClient)(nil)
	gather := modelGatherers{
		graph: func(context.Context, string, string) *tk.Graph { return nil },
		ci:    func(context.Context, string, string) (*statusmodel.CIInput, error) { return nil, nil },
		activity: func(_ context.Context, client *cloudClient, runID, tickID string, attempt int) *statusmodel.ActivityInput {
			calls[fmt.Sprintf("activity %s %s#%d", runID, tickID, attempt)]++
			handed = client
			return &statusmodel.ActivityInput{LastAction: "running go test ./internal/cli"}
		},
		claudeSub: func(_ context.Context, runID string) (*statusmodel.CostSubscription, error) {
			calls["claude-sub "+runID]++
			return &statusmodel.CostSubscription{Label: "MAX1", FiveHour: ptr(0.34), SevenDay: ptr(0.08)}, nil
		},
	}

	cloudStatusModel(context.Background(), client, repo, runID,
		cloudRunRecord{RunID: runID, Epic: "cst", State: "running"},
		cloudLiveness{Alive: true, State: "running", Source: "workflow-record"},
		io.Discard, gather, true)

	// 93n's half: the cloud gathering passes the fallback reader keyed by
	// the tick and attempt, and it reaches the GATHERER with the cloud
	// client — the factory's watch socket, never a local door.
	if captured.RemoteActivity == nil {
		t.Fatal("the cloud gathering with an activity gatherer wired passes no RemoteActivity reader: a cloud worker's activity is null in every real run")
	}
	if got := captured.RemoteActivity("cst", 2); got == nil || got.LastAction != "running go test ./internal/cli" {
		t.Errorf("the wired RemoteActivity answered %+v, want the gatherer's own window", got)
	}
	if calls["activity "+runID+" cst#2"] != 1 {
		t.Errorf("the reader reached the gatherer %d time(s), want the one call keyed by the run's own id and the tick and attempt: %v",
			calls["activity "+runID+" cst#2"], calls)
	}
	if handed == nil {
		t.Error("the activity gatherer was handed a nil client: a cloud run's workers are read through the factory's watch socket")
	}

	// b13's half: the SAME gathering asked the pool and carried the lease —
	// the label and both windows the reader reduced, not a re-read.
	if captured.ClaudeSub == nil {
		t.Fatal("the cloud gathering with a claude-sub gatherer wired passed no subscription: the leased line is silent in every real run")
	}
	if captured.ClaudeSub.Label != "MAX1" || captured.ClaudeSub.FiveHour == nil || *captured.ClaudeSub.FiveHour != 0.34 ||
		captured.ClaudeSub.SevenDay == nil || *captured.ClaudeSub.SevenDay != 0.08 {
		t.Errorf("the leased subscription reads %+v, want the gatherer's own MAX1 with both windows", captured.ClaudeSub)
	}
	if calls["claude-sub "+runID] != 1 {
		t.Errorf("the gathering asked the pool %d time(s), want the one read per model: %v", calls["claude-sub "+runID], calls)
	}
}

// TestWatchGatherModelCarriesBothReaders pins the watch's own gather — the
// line both ticks rewrote and the merge had to union: a cloud watch asks for
// the worker's live activity AND the run's leased subscription in the one
// model it builds, and the subscription is read under the operator's own
// token, the auth `ticfac factory status` carries. watchLive's per-frame
// caches hold the same pair (see the cache tests below); this is the
// one-document path the watch's --json emits.
func TestWatchGatherModelCarriesBothReaders(t *testing.T) {
	captured := captureStatusSources(t)
	const runID = "run_6a4b8e0f2c1d5f3a"
	client, repo, requests := bothReadersFixture(t, runID, func(cloudFactoryRequest) (int, any) {
		return 200, map[string]any{
			"labels": []string{"MAX1"},
			"subscriptions": []any{map[string]any{
				"label":         "MAX1",
				"active_leases": []string{runID + "-cst-1", "another_run-7zk-1"},
				"last_limits": map[string]string{
					"anthropic-ratelimit-unified-5h-utilization": "0.34",
					"anthropic-ratelimit-unified-7d-utilization": "0.08",
				},
			}},
		}
	})

	if _, err := watchGatherModel(context.Background(), &cloudFeedSource{client: client, runID: runID, warn: io.Discard},
		"cloud", repo, runID); err != nil {
		t.Fatalf("the watch's own gather could not build the model: %v", err)
	}

	if captured.RemoteActivity == nil {
		t.Error("the watch's cloud gather wires no activity reader: the dashboard's live line reads null for every cloud worker (tick 93n)")
	}
	if captured.ClaudeSub == nil {
		t.Fatal("the watch's cloud gather wires no claude-sub reader: the cost line's leased segment never renders in a watch (tick b13)")
	}
	if captured.ClaudeSub.Label != "MAX1" || captured.ClaudeSub.FiveHour == nil || *captured.ClaudeSub.FiveHour != 0.34 ||
		captured.ClaudeSub.SevenDay == nil || *captured.ClaudeSub.SevenDay != 0.08 {
		t.Errorf("the watch's leased subscription reads %+v, want the pool's own MAX1 with both windows", captured.ClaudeSub)
	}
	// Under the operator's own bearer — the same auth every factory command
	// carries — and only this run's leases claimed.
	asked, bearer := false, ""
	for _, request := range cloudFactoryRequests(requests) {
		if request.Path == "/api/claude-sub" {
			asked, bearer = true, request.Auth
		}
	}
	if !asked {
		t.Error("the watch's cloud gather never asked the pool: the leased subscription cannot be read anywhere else")
	} else if bearer != "Bearer tkf_test-token" {
		t.Errorf("the pool was asked with %q, want the operator's own bearer", bearer)
	}
}

// TestWatchClaudeSubCacheServesWithinTTLAndReturnsErrorsFresh is the
// claude-sub cache's own contract (tick b13), the twin of the activity
// cache's test in watch_test.go: a frame every two seconds must not ask the
// pool's Durable Object every two seconds, an error is a fact of THIS frame
// and is never frozen, and a successful answer — nil included, because "no
// lease right now" is an answer too — is served within the TTL so the
// leases a running run holds and releases are picked up within half a
// minute.
func TestWatchClaudeSubCacheServesWithinTTLAndReturnsErrorsFresh(t *testing.T) {
	calls := 0
	cache := &watchClaudeSubCache{ttl: time.Hour, read: func(context.Context, string) (*statusmodel.CostSubscription, error) {
		calls++
		switch calls {
		case 1:
			// A factory that should have answered and did not.
			return nil, errors.New("the factory could not be asked")
		case 2:
			return &statusmodel.CostSubscription{Label: "MAX1", FiveHour: ptr(0.34)}, nil
		}
		return nil, nil // "no lease right now" is an answer too
	}}

	if _, err := cache.Subscription(context.Background(), "run_1"); err == nil {
		t.Error("the failed read was not returned as an error: the model degrades per frame, and a frozen error would hide a factory that came back")
	}
	if calls != 1 {
		t.Fatalf("the pool was asked %d time(s), want the one failed read", calls)
	}
	first, err := cache.Subscription(context.Background(), "run_1")
	if err != nil || first == nil || first.Label != "MAX1" {
		t.Errorf("the read after a failure answered (%+v, %v), want the pool's fresh answer: an error must not be cached", first, err)
	}
	if calls != 2 {
		t.Fatalf("the pool was asked %d time(s), want a fresh read after the error", calls)
	}
	second, err := cache.Subscription(context.Background(), "run_1")
	if err != nil || second != first {
		t.Errorf("a read within the TTL answered (%+v, %v), want the cached answer: a frame every two seconds must not re-ask the pool", second, err)
	}
	if calls != 2 {
		t.Errorf("the pool was asked %d time(s) within the TTL, want the two reads above", calls)
	}

	// Past the TTL the pool is asked again: the leases a running run holds
	// and releases are picked up, never a lease that ended half an hour ago.
	cache.at = cache.at.Add(-2 * time.Hour)
	third, err := cache.Subscription(context.Background(), "run_1")
	if err != nil || third != nil {
		t.Errorf("a read past the TTL answered (%+v, %v), want the pool's fresh nil", third, err)
	}
	if calls != 3 {
		t.Errorf("a read past the TTL asked the pool %d time(s) in all, want the fresh read", calls)
	}
}
