package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// Epic hn6, run_37b36bfe (2026-10-01): 0rx's worker container probed the model
// gateway while the factory's Worker was being redeployed, got no answer, and
// died in its boot. The collect read "missing-result … carries no report", the
// run rejected the try, and the redispatch walk counted it as a failed attempt:
// 0rx went up the tier ladder to the ceiling over an infrastructure blip that
// says nothing about the tick.

// gatewayDownFor makes a1's first `tries` tries collect the way a sandbox
// worker that died in its boot on the gateway collects: no work on any branch
// the run reads, missing-result, and the typed infrastructure fact beside it.
func gatewayDownFor(tries int) func(string, int, *subprocess.Collection) {
	return func(tick string, try int, collected *subprocess.Collection) {
		if tick != "a1" || try > tries {
			return
		}
		collected.Verdict = subprocess.VerdictMissingResult
		collected.HasReport = false
		collected.Report = subprocess.Report{}
		collected.Result.Outcome = subprocess.OutcomeFailed
		collected.Result.FailureClass = subprocess.FailureInfrastructure
		collected.Result.RoleResult = nil
		collected.Result.Source.HeadSHA = nil
		collected.Result.Source.Commits = 0
		collected.Infrastructure = &subprocess.InfrastructureFailure{
			Service: "the model gateway", ExitCode: sandboximage.ExitGatewayUnavailable}
		collected.Message = "the container never reached its harness: the model gateway did not answer"
	}
}

// Jobs that died in their boot on the gateway are dispatched again in-run at
// the SAME tier: no rung of the ladder is spent on them, and the tick finishes
// once the gateway answers.
func TestAJobThatNeverReachedItsHarnessIsDispatchedAgainAtTheSameTier(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	r, err := New(f.landingOptions(fixtureOptions{}, gatewayDownFor(2)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(t.Context())
	if err != nil {
		t.Fatalf("the run did not finish: %v\n%s", err, journalText(r))
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a gateway that came back is not a reason to stop\n%s",
			result.State, result.Failure, journalText(r))
	}
	if got := len(tickAttempts(t, r, "a1")); got != 3 {
		t.Fatalf("a1 was dispatched %d times, want 3 (two died in their boot, the third ran)\n%s", got, journalText(r))
	}
	for try := 1; try <= 3; try++ {
		if got := markerTierOfTry(t, r, "a1", try); got != "balanced" {
			t.Errorf("a1 try %d ran at tier %q, want balanced: a job that never reached its harness earns no rung",
				try, got)
		}
	}
	detail, ok := journalLine(r, "a1", StageInfrastructureRedispatched)
	if !ok {
		t.Fatalf("no %s line: the run did not say why it dispatched a1 again\n%s",
			StageInfrastructureRedispatched, journalText(r))
	}
	if !strings.Contains(detail, "the model gateway") || !strings.Contains(detail, "same tier") {
		t.Errorf("the redispatch line does not name the service and the tier rule: %s", detail)
	}
}

// A gateway that never answers does not loop: after the bound, the run stops
// with a refusal that names the gateway, and still no rung was spent.
func TestAGatewayThatStaysDownStopsTheRunNamingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	r, err := New(f.landingOptions(fixtureOptions{}, gatewayDownFor(1000)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Supervise(t.Context())
	if err != nil {
		t.Fatalf("the supervised run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil {
		t.Fatalf("the run ended %s (%+v), want it stopped on the gateway", result.State, result.Failure)
	}
	if result.Failure.Reason != RefusedInfrastructure {
		t.Fatalf("the run stopped over %s (%s), want %s", result.Failure.Reason, result.Failure.Message,
			RefusedInfrastructure)
	}
	if !strings.Contains(result.Failure.Message, "the model gateway") {
		t.Errorf("the stop does not name the gateway: %s", result.Failure.Message)
	}
	attempts := tickAttempts(t, r, "a1")
	if len(attempts) != maxInfrastructureRedispatches+1 {
		t.Errorf("a1 was dispatched %d times, want %d: the bound is what keeps a dead gateway from looping",
			len(attempts), maxInfrastructureRedispatches+1)
	}
	for try := 1; try <= len(attempts); try++ {
		if got := markerTierOfTry(t, r, "a1", try); got != "balanced" {
			t.Errorf("a1 try %d ran at tier %q, want balanced", try, got)
		}
	}
}

// A worker whose tk is not the one its image pins dies in its boot the same
// way, and no retry or tier fixes it: every container of the run boots the same
// image. It spends no rung and is not redispatched — the run stops at once,
// naming the image.
func TestAWorkerImageWithTheWrongTkStopsTheRunWithoutSpendingARung(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	wrongTk := func(tick string, try int, collected *subprocess.Collection) {
		gatewayDownFor(1000)(tick, try, collected)
		if collected.Infrastructure != nil {
			collected.Infrastructure = &subprocess.InfrastructureFailure{Service: "the worker image's tk",
				ExitCode: sandboximage.ExitTkVersion, Persistent: true}
		}
	}
	r, err := New(f.landingOptions(fixtureOptions{}, wrongTk))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Supervise(t.Context())
	if err != nil {
		t.Fatalf("the supervised run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedInfrastructure {
		t.Fatalf("the run ended %s (%+v), want it stopped over %s", result.State, result.Failure, RefusedInfrastructure)
	}
	if !strings.Contains(result.Failure.Message, "tk") {
		t.Errorf("the stop does not name the image's tk: %s", result.Failure.Message)
	}
	if got := len(tickAttempts(t, r, "a1")); got != 1 {
		t.Errorf("a1 was dispatched %d times, want once: a retry boots the same image", got)
	}
}
