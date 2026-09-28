package reconcile

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A carried attempt whose worker confirms the carried work and adds nothing
// delivers the carried work — on EVERY path that collects, the role job's
// included (epic-6in v7z, 2026-09-28).
//
// THE STALL. v7z's close-out (attempt 8) wrote its retro and was released
// with its work carried. Attempt 11 was cut from that retro, found it done,
// answered DONE_WITH_CONCERNS and committed nothing. The executor measures
// "commits beyond the base" from the base the attempt was cut from — which,
// for a carried attempt, IS the carried work — so it collected no-commits, and
// the role-job collect refused it: "for this role an empty branch is an
// undelivered deliverable". Every resume cut the next close-out from the same
// carried retro, and the loop had no end. The implement-tick collect already
// measured a carried attempt from the base the ORIGINAL attempt was cut from
// (tick isp); the role-job collect never did.

// The v7z stall end to end: the carried close-out adds nothing, is collected
// ready-to-merge, its carried retro is merged and gated, and the tick closes.
func TestACarriedCloseoutThatAddsNothingDeliversTheCarriedRetro(t *testing.T) {
	t.Parallel()
	const mode = "closeout_red_carried_confirms"
	var resumed atomic.Bool
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: mode, pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareCloseoutRule(t, f.Repo)
	pr.ci = func(string) forge.CIReport {
		if resumed.Load() || f.dispatch("co").TickID == "" {
			return forge.CIReport{State: forge.CIGreen}
		}
		return forge.CIReport{State: forge.CIRed, Failing: []string{"go"}}
	}

	first, stopped, err := f.run(f.Repo, fixtureOptions{mode: mode, pullRequests: pr})
	if err != nil {
		t.Fatalf("the first run did not finish: %v", err)
	}
	if stopped.Failure == nil || stopped.Failure.Reason != RefusedCloseoutOverRedCI {
		t.Fatalf("the first run ended %s (%+v), want the %s stop", stopped.State, stopped.Failure,
			RefusedCloseoutOverRedCI)
	}
	rejected := markerOfTry(t, first, "co", 1)
	retro := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet",
		refFor(branchOf(rejected.WriteRef))))
	if retro == "" || retro == rejected.BaseSHA {
		t.Fatal("the rejected close-out committed nothing; the scenario proves nothing")
	}

	resumed.Store(true)
	r, result, err := f.run(f.Repo, fixtureOptions{mode: mode, pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resume ended %s (%+v): a carried close-out that confirms the carried retro delivers it:\n%s",
			result.State, result.Failure, journalText(r))
	}
	next := markerOfTry(t, r, "co", 2)
	// A carried close-out is cut from the carried retro merged onto the
	// integration branch as it is now (carryOntoIntegration).
	if next.ResumedFrom == nil || next.ResumedFrom.SHA != retro ||
		!mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", retro, next.BaseSHA) {
		t.Fatalf("the second close-out was cut from %s (resumed from %+v), want a base carrying the retro %s",
			short(next.BaseSHA), next.ResumedFrom, short(retro))
	}
	if head := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet",
		refFor(branchOf(next.WriteRef)))); head != "" && head != next.BaseSHA {
		t.Fatalf("the carried close-out committed %s beyond the retro; the scenario proves nothing", short(head))
	}
	for _, e := range r.Journal() {
		if e.Tick == "co" && e.Stage == StageRejected && strings.Contains(e.Detail, "no-commits") {
			t.Errorf("the carried close-out was refused as no-commits: %s", e.Detail)
		}
	}
	if !containsCommit(t, f, retro, "refs/remotes/origin/epic/qeu") {
		t.Errorf("the carried retro %s never reached the integration branch", short(retro))
	}
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("the close-out is %s, want closed behind the carried retro", current.Status)
	}
}

// The same shape on an implement tick, carried by the run rather than a
// person: a1's first try commits and is stopped by the stuck watch, its work
// is carried, and the next try finds it done and adds nothing. The carried
// commits are the delivery: merged, gated, closed — never no-commits.
func TestACarriedImplementTryThatAddsNothingDeliversTheCarriedWork(t *testing.T) {
	t.Parallel()
	const mode = "stuck-first-then-nothing"
	f := newFixture(t, fixtureOptions{mode: mode, gate: tierGate})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: mode, stuckAfter: 1500 * time.Millisecond})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v):\n%s", result.State, result.Failure, journalText(r))
	}
	second := markerOfTry(t, r, "a1", 2)
	if second.ResumedFrom == nil || second.BaseSHA != second.ResumedFrom.SHA {
		t.Fatalf("a1's second try was not cut from the carried work: %+v", second)
	}
	for _, e := range r.Journal() {
		if e.Tick == "a1" && e.Stage == StageRejected && strings.Contains(e.Detail, "no-commits") {
			t.Errorf("the carried try was refused as no-commits: %s", e.Detail)
		}
	}
	if !containsCommit(t, f, second.ResumedFrom.SHA, "refs/remotes/origin/epic/qeu") {
		t.Errorf("the carried commits %s are not on the integration branch", short(second.ResumedFrom.SHA))
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s, want closed behind the carried work", current.Status)
	}
}
