package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// A claude-sub job whose subscription ran out of quota MID-JOB (the cloud
// executor reads it from the factory's proxy, job-protocol 2.3.0) used to be
// collected as a failed attempt: the redispatch walk earned the tier ladder a
// rung, sonnet climbed to opus, and with every subscription benched the next
// try ran at the ceiling. It is infrastructure: redispatched at the same tier
// (where the factory steps the job down to Workers AI), no rung spent — and
// the feed says the job stopped MID-JOB, never that it "never reached its
// harness".

// quotaRanOutFor makes a1's first `tries` tries collect the way a claude-sub
// worker whose subscription ran out under it collects.
func quotaRanOutFor(tries int) func(string, int, *subprocess.Collection) {
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
			Service: "the claude subscription's quota", ExitCode: sandboximage.ExitWorkerAgent, MidJob: true,
			Fix: "wait for the reset"}
		collected.Message = "the claude subscription ran out of quota during the job"
	}
}

func TestAClaudeSubJobWhoseQuotaRanOutIsDispatchedAgainAtTheSameTierAndSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	r, err := New(f.landingOptions(fixtureOptions{}, quotaRanOutFor(1)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(t.Context())
	if err != nil {
		t.Fatalf("the run did not finish: %v\n%s", err, journalText(r))
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)\n%s", result.State, result.Failure, journalText(r))
	}
	if got := len(tickAttempts(t, r, "a1")); got != 2 {
		t.Fatalf("a1 was dispatched %d times, want 2\n%s", got, journalText(r))
	}
	for try := 1; try <= 2; try++ {
		if got := markerTierOfTry(t, r, "a1", try); got != "balanced" {
			t.Errorf("a1 try %d ran at tier %q, want balanced: a spent subscription earns no rung", try, got)
		}
	}
	detail, ok := journalLine(r, "a1", StageInfrastructureRedispatched)
	if !ok {
		t.Fatalf("no %s line\n%s", StageInfrastructureRedispatched, journalText(r))
	}
	if !strings.Contains(detail, "stopped mid-job") || !strings.Contains(detail, "the claude subscription's quota") ||
		!strings.Contains(detail, "same tier") {
		t.Errorf("the redispatch line does not say the subscription ran out mid-job: %s", detail)
	}
	if strings.Contains(journalText(r), "never reached its harness") {
		t.Errorf("the feed says a job whose harness ran never reached it:\n%s", journalText(r))
	}
}

// stepDownExecutor is an executor that says a started job was stepped down
// from the claude-sub rung.
type stepDownExecutor struct {
	Executor
	detail string
}

func (e stepDownExecutor) ClaudeSubStepDown(*subprocess.JobHandle) (string, bool) {
	return e.detail, e.detail != ""
}

// A job the factory stepped down from the claude-sub rung leaves a feed line
// saying why and until when, so `ticfac watch` explains a run expected on
// sonnet that is on Workers AI.
func TestAClaudeSubStepDownLeavesAFeedLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.landingOptions(fixtureOptions{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	r.noteClaudeSubStepDown(stepDownExecutor{detail: "claude-sub is exhausted until 2026-10-07T13:00:00Z; " +
		"job runs on Workers AI (pi-durable on glm) instead"}, "a1", &subprocess.JobHandle{})
	detail, ok := journalLine(r, "a1", StageClaudeSubSteppedDown)
	if !ok || !strings.Contains(detail, "exhausted until 2026-10-07T13:00:00Z") {
		t.Fatalf("no step-down line naming the reset (%q)\n%s", detail, journalText(r))
	}
	r.noteClaudeSubStepDown(stepDownExecutor{}, "a2", &subprocess.JobHandle{})
	if _, ok := journalLine(r, "a2", StageClaudeSubSteppedDown); ok {
		t.Error("a job that was not stepped down left a step-down line")
	}
}
