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

// A rejected attempt that carries work is disposed by the run (epic-6in 823):
// an operational rejection carries the work into the next try, a rejection on
// the merits releases it fresh with the branch kept — and neither needs a
// person, where both used to halt a resumed run on
// rejected_attempt_carries_work.

// The 823 stall itself: a worker commits, goes quiet, and is stopped by the
// stuck watch; it collects as missing-result WITH commits. The next try starts
// from those commits one tier up, and the epic completes with nobody typing
// `ticfac settle --carry-work`.
func TestAStuckStoppedAttemptWithCommitsIsCarriedIntoTheNextTryWithNoPerson(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "stuck-first", gate: tierGate})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "stuck-first", stuckAfter: 1500 * time.Millisecond})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a stuck worker's committed work must go forward without a person",
			result.State, result.Failure)
	}
	if _, ok := journalLine(r, "a1", StageStuckStopped); !ok {
		t.Fatalf("a1's first try was not stopped by the stuck watch; the scenario proves nothing:\n%s", journalText(r))
	}
	carried, ok := journalLine(r, "a1", StageRejectedWorkCarried)
	if !ok {
		t.Fatalf("no %s line: where the work went is not on the feed:\n%s", StageRejectedWorkCarried, journalText(r))
	}
	if !strings.Contains(carried, "missing-result") {
		t.Errorf("the %s line does not name the rejection it answered: %s", StageRejectedWorkCarried, carried)
	}
	for _, e := range r.Journal() {
		if strings.Contains(e.Detail, RefusedRejectedWork) || e.Stage == StageRunHeld {
			t.Errorf("the run held or named the hold: %s %s", e.Stage, e.Detail)
		}
	}

	first, second := markerOfTry(t, r, "a1", 1), markerOfTry(t, r, "a1", 2)
	if second.ResumedFrom == nil || second.ResumedFrom.Attempt != first.Attempt {
		t.Fatalf("the second try resumed from %+v, want attempt %d's work", second.ResumedFrom, first.Attempt)
	}
	if second.BaseSHA != second.ResumedFrom.SHA || second.BaseSHA == first.BaseSHA {
		t.Errorf("the second try was cut from %s, want the stuck attempt's head %s (not its base %s)",
			short(second.BaseSHA), short(second.ResumedFrom.SHA), short(first.BaseSHA))
	}
	if !strings.HasPrefix(second.ResumedFrom.ReleasedBy, runReleaser) {
		t.Errorf("the carry is attributed to %q, want the run", second.ResumedFrom.ReleasedBy)
	}
	if got := markerTierOfTry(t, r, "a1", 2); got != "strong" {
		t.Errorf("a1's second try ran at %q, want strong: the failed try earns its rung", got)
	}

	// The durable record is the one `ticfac settle --carry-work` writes,
	// attributed to the run and the reason, as fields.
	released, err := r.settlements()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := released[attemptKey("a1", first.Attempt)]
	if !ok || !s.byRun || !s.carry || s.reason != "missing-result" {
		t.Errorf("the release record of a1's first try is %+v (present %v), want a run release carrying its work "+
			"for missing-result", s, ok)
	}
	if !containsCommit(t, f, second.ResumedFrom.SHA, "refs/remotes/origin/epic/qeu") {
		t.Errorf("the carried commits %s are not on the integration branch", short(second.ResumedFrom.SHA))
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s, want closed behind the carried attempt", current.Status)
	}
}

// A rejection ON THE MERITS: the first try wrote under the tracker's authority.
// Its commits are not carried — the next try is cut fresh from the
// integration branch — and its branch is kept on origin and named on the feed.
func TestABoundaryViolationWithCommitsIsRedispatchedFreshWithItsBranchKept(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "boundary-first", gate: tierGate})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "boundary-first"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a boundary violation is redispatched fresh, not held",
			result.State, result.Failure)
	}
	first, second := markerOfTry(t, r, "a1", 1), markerOfTry(t, r, "a1", 2)
	branch := branchOf(first.WriteRef)
	released, ok := journalLine(r, "a1", StageRejectedWorkReleased)
	if !ok {
		t.Fatalf("no %s line:\n%s", StageRejectedWorkReleased, journalText(r))
	}
	if !strings.Contains(released, branch) || !strings.Contains(released, ".tick/") {
		t.Errorf("the %s line does not name the kept branch and the violation: %s", StageRejectedWorkReleased, released)
	}
	if second.ResumedFrom != nil {
		t.Errorf("the second try carried %+v: work rejected on the merits must not be carried", second.ResumedFrom)
	}
	head := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet", refFor(branch)))
	if head == "" || head == first.BaseSHA {
		t.Fatalf("the violating attempt's branch %s is not kept on origin", branch)
	}
	if head == second.BaseSHA || containsCommit(t, f, head, "refs/remotes/origin/epic/qeu") {
		t.Errorf("the violating work %s reached the next try or the integration branch", short(head))
	}
	if got := runGitQuiet(f.Repo.Origin, "ls-tree", "-r", "--name-only", "refs/heads/epic/qeu"); strings.Contains(got, "forged-a1") {
		t.Errorf("the forged tracker record reached the integration branch")
	}
	settled, err := r.settlements()
	if err != nil {
		t.Fatal(err)
	}
	if s := settled[attemptKey("a1", first.Attempt)]; !s.byRun || s.carry {
		t.Errorf("the release record of a1's first try is %+v, want a run release WITHOUT carry", s)
	}
}

// The bound: an attempt that keeps getting stuck climbs to the ceiling, gets
// one more try there, and then the run stops on the backstop hold, saying the
// bound is spent. It never loops.
func TestRejectedWorkDisposalIsBoundedByTheLadder(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "silent", gate: tierGate})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "silent"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State == runstate.StateCompleted {
		t.Fatal("a worker that never reports completed the run")
	}
	// balanced → strong (escalate), strong again (the one try at the
	// ceiling), and the third rejection is not disposed.
	if n := a1AttemptCount(t, f, "a1"); n != 3 {
		t.Errorf("a1 was dispatched %d times, want 3 (escalate, one at the ceiling, stop)", n)
	}
	_, resumed, err := f.run(f.Repo, fixtureOptions{mode: "silent"})
	if err != nil {
		t.Fatalf("the resume did not finish: %v", err)
	}
	if resumed.Failure == nil || resumed.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the resume ended %+v, want the %s backstop", resumed.Failure, RefusedRejectedWork)
	}
	if !strings.Contains(resumed.Failure.Message, "bound is spent") {
		t.Errorf("the backstop does not say the bound is spent: %s", resumed.Failure.Message)
	}
	// The release command the backstop names is addressed by the run whose
	// store carries the attempt (tick qxj): this run's records live under
	// r-fixture, not under the epic spelling a settle without --run-id opens.
	if !strings.Contains(resumed.Failure.Message,
		"ticfac settle qeu a1 5 --run-id r-fixture --release") {
		t.Errorf("the backstop's release command does not name the run its attempt is recorded under: %s",
			resumed.Failure.Message)
	}
	_ = r
}

// Epic-6in v7z, the stall on a build that included the disposal: a close-out
// rejected with its retro committed by a path that writes NO disposition — a
// close-out that answered BLOCKED over red CI, as v7z's did before the
// collect disposed of anything — resumed into the backstop hold once CI read
// green again, and a person typed `ticfac settle --release … --carry-work`.
// The resume now decides it from the rejection it recorded: a close-out
// rejected over red CI is operational, its commits are carried, and a new
// close-out is dispatched from them, with nobody.
func TestARejectedCloseoutWithWorkAndNoDispositionIsCarriedOnResumeWithNoPerson(t *testing.T) {
	t.Parallel()
	const mode = "closeout_red_uncarried"
	var resumed atomic.Bool
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: mode, pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareCloseoutRule(t, f.Repo)
	pr.ci = func(string) forge.CIReport {
		if resumed.Load() || f.dispatch("co").TickID == "" {
			// The false green the close-out was admitted on; and, on resume,
			// CI re-run green — the red-CI supersede has nothing to read.
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
	head := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet",
		refFor(branchOf(rejected.WriteRef))))
	if head == "" || head == rejected.BaseSHA {
		t.Fatal("the rejected close-out committed nothing; the scenario proves nothing")
	}
	// The premise: the rejection carries work and no decision says what
	// becomes of it.
	if released, err := first.settlements(); err != nil {
		t.Fatal(err)
	} else if s, ok := released[attemptKey("co", rejected.Attempt)]; ok {
		t.Fatalf("the rejection already has a disposition %+v; the scenario proves nothing", s)
	}

	resumed.Store(true)
	r, result, err := f.run(f.Repo, fixtureOptions{mode: mode, pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resume ended %s (%+v): a rejected close-out's work is carried by the run, not held for a "+
			"person", result.State, result.Failure)
	}
	for _, e := range r.Journal() {
		if strings.Contains(e.Detail, RefusedRejectedWork) || e.Stage == StageRunHeld {
			t.Errorf("the run held or named the hold: %s %s", e.Stage, e.Detail)
		}
	}
	next := markerOfTry(t, r, "co", 2)
	if next.ResumedFrom == nil || next.ResumedFrom.Attempt != rejected.Attempt || next.ResumedFrom.SHA != head {
		t.Fatalf("the new close-out resumed from %+v, want attempt %d's work at %s", next.ResumedFrom,
			rejected.Attempt, short(head))
	}
	// A close-out is cut from the carried commits merged onto the integration
	// branch as it is now, so its base carries the rejected head.
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", head, next.BaseSHA) {
		t.Errorf("the new close-out was cut from %s, which does not carry the rejected close-out's head %s",
			short(next.BaseSHA), short(head))
	}
	if !strings.HasPrefix(next.ResumedFrom.ReleasedBy, runReleaser) {
		t.Errorf("the carry is attributed to %q, want the run", next.ResumedFrom.ReleasedBy)
	}
	released, err := r.settlements()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := released[attemptKey("co", rejected.Attempt)]
	if !ok || !s.byRun || !s.carry || !strings.Contains(s.reason, "red CI") ||
		!strings.Contains(s.reason, rejectedOnResume) {
		t.Errorf("the release record of the rejected close-out is %+v (present %v), want a run release carrying "+
			"its work, naming the recorded rejection", s, ok)
	}
	if line, ok := journalLine(r, "co", StageRejectedWorkCarried); !ok || !strings.Contains(line, "red CI") {
		t.Errorf("the feed does not say the work was carried and why: %q", line)
	}
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("the close-out is %s, want closed behind the carried try", current.Status)
	}
}

// The classifier over RECORDED reasons, on the reasons the run really writes —
// v7z's own among them.
//
// short: string matching over a table; no repository, no run
func TestARecordedRejectionIsClassifiedAsTheCollectWouldHave(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		reason       string
		carry, known bool
	}{
		// v7z attempt 8, from epic-6in's checkpoint history.
		{`the closeout-epic job for v7z answered BLOCKED: epic/6in's integrated code fails CI deterministically ` +
			`(TestARunUnderANewRunIDIsOfferedThePreviousRunsReports, regressed by dz1 5e493a2f), and the "CI green" ` +
			`that admitted this close-out was a false green from all-skipped PR checks. The tick stays open, because ` +
			`a role job's answer IS its verdict and this one asks for a person`, true, true},
		{"the closeout-epic job for co answered BLOCKED, and it was dispatched over code whose CI is red (go failed " +
			"on the code of 1234567)", true, true},
		{"a1 try 1 (run dispatch #1) is rejected (missing-result): no report", true, true},
		{"a1 try 1 (run dispatch #1) is rejected (boundary-violation): wrote .tick/x", false, true},
		{"a1 try 1 (run dispatch #1) is rejected (ready-to-merge): the collected base is not the dispatched base",
			false, true},
		{"the review-epic job for rv wrote under an authority that is not its own (.tick/x): no", false, true},
		{"a1 try 1 (run dispatch #1) is rejected (no-commits): nothing", false, false},
		{"a1 could not be merged onto epic/qeu: add/add in work-a1.txt", false, false},
	} {
		_, carry, known := classifyRecordedRejection(c.reason)
		if carry != c.carry || known != c.known {
			t.Errorf("%q classified carry=%v known=%v, want carry=%v known=%v", c.reason, carry, known, c.carry,
				c.known)
		}
	}
}
