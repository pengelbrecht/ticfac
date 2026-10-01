package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
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

// A boot that stops on a DETERMINISTIC environment fault — the inputs the
// factory sent, the image's tk, the repository's pre-flight or setup, a model
// route the gateway refused, a harness that cannot use the route — failed the
// same way before (missing-result, a rung spent: the ladder climbed to a tier
// that boots the same image on the same repository and stops the same way).
// It spends no rung and is not redispatched: the run stops at once on
// worker_boot_fault, naming the cause, the boot's reason and the fix.
func TestABootThatStopsOnAnEnvironmentFaultStopsTheRunWithoutSpendingARung(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	for _, tc := range []struct {
		code    int
		service string
	}{
		{sandboximage.ExitConfig, "the worker's boot inputs"},
		{sandboximage.ExitTkVersion, "the worker image's tk"},
		{sandboximage.ExitPreflight, "the repository's environment pre-flight"},
		{sandboximage.ExitSetup, "the repository's [sandbox] setup"},
		{sandboximage.ExitModel, "the model route"},
		{sandboximage.ExitHarness, "the harness's model wiring"},
	} {
		t.Run(tc.service, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, fixtureOptions{gate: tierGate})
			const fix = "fix the named thing, then run the epic again"
			const said = "the boot's own words about what stopped it"
			fault := func(tick string, try int, collected *subprocess.Collection) {
				gatewayDownFor(1000)(tick, try, collected)
				if collected.Infrastructure != nil {
					collected.Infrastructure = &subprocess.InfrastructureFailure{Service: tc.service,
						ExitCode: tc.code, Persistent: true, Fix: fix}
					collected.Message = said
				}
			}
			r, err := New(f.landingOptions(fixtureOptions{}, fault))
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.Supervise(t.Context())
			if err != nil {
				t.Fatalf("the supervised run did not finish: %v", err)
			}
			if result.Failure == nil || result.Failure.Reason != RefusedWorkerBootFault {
				t.Fatalf("the run ended %s (%+v), want it stopped over %s", result.State, result.Failure,
					RefusedWorkerBootFault)
			}
			for _, want := range []string{tc.service, said, fix} {
				if !strings.Contains(result.Failure.Message, want) {
					t.Errorf("the stop does not carry %q: %s", want, result.Failure.Message)
				}
			}
			if got := len(tickAttempts(t, r, "a1")); got != 1 {
				t.Errorf("a1 was dispatched %d times, want once: a retry boots the same environment", got)
			}
			if got := markerTierOfTry(t, r, "a1", 1); got != "balanced" {
				t.Errorf("a1 ran at tier %q, want balanced", got)
			}
		})
	}
}
