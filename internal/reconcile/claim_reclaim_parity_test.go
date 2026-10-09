package reconcile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// Tick krh: the fake tracker's claim counting must be tk's, or the width
// tests that lean on it prove things production never has.
//
// tk's claim is `update --status in_progress` (there is no `tk claim` verb;
// the manifest's claim entry says so), and tk counts what is in flight from
// the tick's STATUS — a tick claimed twice is one tick in progress, once.
// Re-claiming an already in_progress tick is idempotent and keeps its
// started_at. The fake counted every Claim call, so a re-claim — the redispatch
// of a rejected try, the takeover of this run's own stragglers — opened a
// phantom claim against the width and could even be REFUSED by the width
// guard for an over-claim that exists nowhere but in the fake: a run holding
// one claim, re-claiming its own tick at a width of one, held on itself.

// TestAReclaimOfAnAlreadyClaimedTickOpensNoNewClaim is the fake's half of the
// parity: the same claim moves tk answers, made against the fake, with the
// width the old tk's exit-8 guard modelled declared on it — because the width
// is where a miscounted claim becomes a wrong verdict.
//
// short: a tracker state file in a temp directory; no repository, no process
func TestAReclaimOfAnAlreadyClaimedTickOpensNoNewClaim(t *testing.T) {
	t.Parallel()
	tracker := newTracker(t, t.TempDir())
	ctx := context.Background()
	const worker = "worker@example.com"

	// The width the old guard modelled: one claim. A re-claim that opened a
	// claim would be refused here — which is the whole defect: before krh the
	// fake refused this run its OWN tick.
	tracker.refuseClaimsBeyond(1)

	first, err := tracker.Claim(ctx, "a1", worker)
	if err != nil {
		t.Fatalf("the first claim of a1: %v", err)
	}
	if first.Status != "in_progress" || first.Owner != worker {
		t.Fatalf("claim returned status=%q owner=%q, want in_progress for %s", first.Status, first.Owner, worker)
	}
	if first.StartedAt == "" {
		t.Fatalf("the claim left a1 with no started_at: tk records when a tick started, and the re-claim's " +
			"idempotence is pinned ON that field")
	}

	// THE re-claim: same tick, same claimant — the shape of a redispatched try.
	// tk answers it; the fake must not refuse it against a width its own claim
	// already fills, and must not count it.
	second, err := tracker.Claim(ctx, "a1", worker)
	if err != nil {
		var width *tk.ErrDispatchWidth
		if errors.As(err, &width) {
			t.Fatalf("the fake refused this run its own re-claim of a1: %v. tk counts a tick in progress once, "+
				"however many times it is claimed, so this claim opens nothing the width could refuse", err)
		}
		t.Fatalf("the re-claim of a1: %v", err)
	}
	if second.StartedAt != first.StartedAt {
		t.Errorf("the re-claim moved a1's started_at from %q to %q: tk's claim is idempotent on a tick already "+
			"in progress and keeps the field", first.StartedAt, second.StartedAt)
	}
	if got := tracker.count("claim:a1"); got != 2 {
		t.Errorf("the tracker saw %d claim commands for a1, want 2: the re-claim is a real claim call, "+
			"counted once by the tracker", got)
	}
	if peak := tracker.peakClaims(); peak != 1 {
		t.Errorf("%d claims were open at once under a width of 1 after one claim and one re-claim of the same "+
			"tick: a re-claim is not a new claim, and the fake counted it as one", peak)
	}

	// The claim the width exists to stop is still stopped: a NEW tick is a new
	// claim, and the guard that models tk's exit 8 still refuses it. The fix
	// must not have lobotomised the width.
	if _, err := tracker.Claim(ctx, "a2", worker); err == nil {
		t.Errorf("a2 was claimed at a width of 1 already filled by a1: the re-claim parity must narrow the " +
			"guard's refusals to claims that open one, never widen what it admits")
	} else {
		var width *tk.ErrDispatchWidth
		if !errors.As(err, &width) {
			t.Errorf("a2's over-width claim failed with %v, want the typed width refusal the run holds on", err)
		}
	}

	// And the in-flight reading agrees: the graph's dispatch.in_flight_ids —
	// the count the window and the width are made of — holds a1 once, however
	// many claims were asked for it, exactly as tk's graph answers.
	graph, err := tracker.Graph(ctx, "qeu")
	if err != nil {
		t.Fatal(err)
	}
	if graph.Dispatch.InFlight != 1 || strings.Join(graph.Dispatch.InFlightIDs, ",") != "a1" {
		t.Errorf("the graph reports %d in flight (%v), want a1 alone: the re-claim must not have counted "+
			"a second anything", graph.Dispatch.InFlight, graph.Dispatch.InFlightIDs)
	}

	// The claim ends at the CLOSE, once: closing a1 frees the width for a2 —
	// the bookkeeping a re-claim must not have inflated, and a close must not
	// have to pay twice for.
	if _, err := tracker.Close(ctx, "a1"); err != nil {
		t.Fatalf("close a1: %v", err)
	}
	if _, err := tracker.Claim(ctx, "a2", worker); err != nil {
		t.Errorf("a2 was refused the width a1's close freed: %v. One claim was open, one close ended it, and "+
			"the re-claim in between must have cost nothing", err)
	}
	if peak := tracker.peakClaims(); peak != 1 {
		t.Errorf("%d claims were open at once across the whole sequence, want the width's 1", peak)
	}
}

// TestTheFakeTrackerCountsAReclaimTheWayRealTkDoes is the parity pin itself:
// the same claim moves, made against a REAL tk in a throwaway repository and
// against the fake, must answer the same — the fake's claim counting is only
// evidence while it agrees with the tracker it stands in for.
//
// It follows internal/tk's tk_real_test.go: the real binary is looked up on
// PATH and the test skips where there is none (no CI runner installs one), and
// it is an end-to-end test — it builds a repository and runs a real process —
// so it runs in the full suite and at close-out, not in the per-tick gate.
// The fake's half above keeps its place in the gate.
func TestTheFakeTrackerCountsAReclaimTheWayRealTkDoes(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	tkPath, err := exec.LookPath("tk")
	if err != nil {
		t.Skip("tk binary not on PATH; skipping the real-tk half of the re-claim parity")
	}

	// A throwaway repository, seeded the way internal/tk's real-tk test seeds
	// one: an epic and one open task, no harness, no run — this test is about
	// the tracker and nothing else.
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mustRun(t, root, "git", "init", "--quiet", "-b", "main", repo)
	configure(t, repo)
	// tk init reads the repository's remote (tk_real_test.go's fixture remote
	// too): a repo without one is not a project it will initialise.
	mustRun(t, repo, "git", "remote", "add", "origin", "https://example.com/example/parity.git")
	write(t, filepath.Join(repo, "README.md"), "re-claim parity fixture\n")
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "--quiet", "-m", "base")
	if out, err := harnessCommand(tkPath, "init").output(repo); err != nil {
		t.Fatalf("tk init: %v\n%s", err, out)
	}
	issues := filepath.Join(repo, ".tick", "issues")
	if err := os.MkdirAll(issues, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(issues, "e01.json"), `{
  "id": "e01", "title": "Parity epic", "status": "open", "priority": 1,
  "type": "epic", "owner": "fixture", "created_by": "fixture",
  "created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z"
}
`)
	write(t, filepath.Join(issues, "a01.json"), `{
  "id": "a01", "title": "Parity task", "status": "open", "priority": 1,
  "type": "task", "parent": "e01", "owner": "fixture", "created_by": "fixture",
  "created_at": "2026-09-02T00:00:00Z", "updated_at": "2026-09-02T00:00:00Z"
}
`)
	const worker = "worker@example.com"
	ctx := context.Background()

	// --- the real tk, the oracle -----------------------------------------
	client, err := tk.New(tk.Options{Binary: tkPath, Dir: repo})
	if err != nil {
		t.Fatalf("tk client: %v", err)
	}
	realFirst, err := client.Claim(ctx, "a01", worker)
	if err != nil {
		t.Fatalf("real tk refused the first claim of a01: %v", err)
	}
	if realFirst.Status != "in_progress" || realFirst.StartedAt == "" {
		t.Fatalf("real tk's first claim answered status=%q started_at=%q, want in_progress with a started_at",
			realFirst.Status, realFirst.StartedAt)
	}
	realAfterFirst, err := client.Graph(ctx, "e01")
	if err != nil {
		t.Fatalf("real tk graph: %v", err)
	}
	// A moment, so a started_at tk rewrote on the re-claim could not hide
	// behind the clock's granularity: the pin below must be able to fail.
	time.Sleep(20 * time.Millisecond)
	realSecond, err := client.Claim(ctx, "a01", worker)
	if err != nil {
		t.Fatalf("real tk refused the re-claim of a01 it had already claimed: %v", err)
	}
	if realSecond.StartedAt != realFirst.StartedAt {
		t.Errorf("real tk moved a01's started_at on the re-claim (%q -> %q), but its claim is idempotent on a "+
			"tick already in progress: the pinned contract is stale and so is this pin", realFirst.StartedAt,
			realSecond.StartedAt)
	}
	if realSecond.UpdatedAt == realFirst.UpdatedAt {
		t.Errorf("the re-claim did not touch a01's updated_at (%q): tk applied no update at all, so the "+
			"idempotence above proved nothing", realFirst.UpdatedAt)
	}
	realAfterReclaim, err := client.Graph(ctx, "e01")
	if err != nil {
		t.Fatalf("real tk graph after the re-claim: %v", err)
	}
	if realAfterReclaim.Dispatch.InFlight != realAfterFirst.Dispatch.InFlight ||
		strings.Join(realAfterReclaim.Dispatch.InFlightIDs, ",") != strings.Join(realAfterFirst.Dispatch.InFlightIDs, ",") {
		t.Errorf("real tk's in-flight count moved on the re-claim: %d (%v) before, %d (%v) after. The count is a "+
			"reading of the tick's status, and a re-claim opens no new claim",
			realAfterFirst.Dispatch.InFlight, realAfterFirst.Dispatch.InFlightIDs,
			realAfterReclaim.Dispatch.InFlight, realAfterReclaim.Dispatch.InFlightIDs)
	}

	// --- the fake, the parity --------------------------------------------
	fake := newTracker(t, t.TempDir())
	fakeFirst, err := fake.Claim(ctx, "a1", worker)
	if err != nil {
		t.Fatalf("the fake refused the first claim of a1: %v", err)
	}
	if fakeFirst.StartedAt == "" {
		t.Fatalf("the fake's first claim left a1 with no started_at, so it cannot agree with tk's record and " +
			"the re-claim pin below would pass vacuously")
	}
	time.Sleep(20 * time.Millisecond)
	fakeSecond, err := fake.Claim(ctx, "a1", worker)
	if err != nil {
		t.Fatalf("the fake refused the re-claim of a1 real tk granted: %v", err)
	}
	if fakeSecond.StartedAt != fakeFirst.StartedAt {
		t.Errorf("the fake moved a1's started_at on the re-claim (%q -> %q) where real tk kept a01's (%q -> "+
			"%q): the two must answer the same claim the same way", fakeFirst.StartedAt, fakeSecond.StartedAt,
			realFirst.StartedAt, realSecond.StartedAt)
	}
	fakeAfterReclaim, err := fake.Graph(ctx, "qeu")
	if err != nil {
		t.Fatal(err)
	}
	if fakeAfterReclaim.Dispatch.InFlight != realAfterReclaim.Dispatch.InFlight {
		t.Errorf("the fake counts %d in flight after one claim and one re-claim, where real tk counts %d after "+
			"the same moves: the fake's width arithmetic is not tk's", fakeAfterReclaim.Dispatch.InFlight,
			realAfterReclaim.Dispatch.InFlight)
	}
}
