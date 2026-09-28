package reconcile

import (
	"context"
	"sort"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A rejected close-out's commits go forward (epic-6in v7z, the follow-up):
// its retro is still valid whatever stopped it, so the fresh close-out starts
// from them — and, being a close-out, over the integration branch as it is
// NOW, so a repaired tree is under it too.

// A close-out answered BLOCKED over red CI: the resume repairs the CI and the
// fresh close-out is cut from the rejected one's commits MERGED onto the
// repaired tree — never fresh (which threw the retro away) and never from the
// stale commits alone (which would put it back over the red code).
func TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "closeout_red", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareCloseoutRule(t, f.Repo)
	pr.ci = func(sha string) forge.CIReport {
		if f.dispatch("co").TickID == "" {
			return forge.CIReport{State: forge.CIGreen}
		}
		if !mustRunAllowingFailure(pr.origin, "git", "cat-file", "-e", sha+":ci-fix.txt") {
			return forge.CIReport{State: forge.CIRed, Failing: []string{"go"}}
		}
		return forge.CIReport{State: forge.CIGreen}
	}

	r, result, err := f.supervise(f.Repo, fixtureOptions{mode: "closeout_red", pullRequests: pr})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	all, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	var co []attemptHandle
	for _, record := range all {
		if record.TickID == "co" {
			co = append(co, handleFromMap(record.JobHandle))
		}
	}
	sort.Slice(co, func(i, j int) bool { return co[i].Attempt < co[j].Attempt })
	if len(co) < 2 {
		t.Fatalf("the close-out was dispatched %d time(s), want a rejected try and a fresh one", len(co))
	}
	first, next := co[0], co[1]
	if next.ResumedFrom == nil || next.ResumedFrom.Attempt != first.Attempt {
		t.Fatalf("the fresh close-out resumed from %+v, want the rejected close-out's work (attempt %d)",
			next.ResumedFrom, first.Attempt)
	}
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", next.ResumedFrom.SHA,
		next.BaseSHA) {
		t.Errorf("the fresh close-out was cut from %s, which does not carry the rejected close-out's commits %s",
			short(next.BaseSHA), short(next.ResumedFrom.SHA))
	}
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "cat-file", "-e", next.BaseSHA+":ci-fix.txt") {
		t.Errorf("the fresh close-out was cut from %s, a tree without the repair: it is back over red CI",
			short(next.BaseSHA))
	}
	// r is the first incarnation; the release was recorded by a later one.
	if _, err := r.store.Fetch(); err != nil {
		t.Fatal(err)
	}
	released, err := r.settlements()
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := released[attemptKey("co", first.Attempt)]; !ok || !s.byRun || !s.carry {
		t.Errorf("the release of the rejected close-out is %+v (present %v), want a run release carrying its work",
			s, ok)
	}
	for _, e := range feedStages(t, f.Repo.Dir, "r-fixture") {
		if e.Stage == StageSupervisionHalted || e.Stage == StageRunHeld {
			t.Errorf("the run stopped for a person: %s %s", e.Stage, e.Detail)
		}
	}
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("the close-out is %s, want closed", current.Status)
	}
}

// A close-out that stopped to ask, was told to decide under the standing
// orders, and asked again used to hold for a person (role_answer_needs_human)
// whatever it asked. Its question is in no always-ask class, so the run
// decides it: the supervisor continues, the resume carries the close-out's
// work into one more try (the bound's one try at the ceiling), and the epic
// completes with nobody.
func TestASupervisedCloseoutThatAsksAgainIsCarriedWithoutAPerson(t *testing.T) {
	t.Parallel()
	const mode = "closeout_asks_twice"
	f := newFixture(t, fixtureOptions{mode: mode})
	_, result, err := f.supervise(f.Repo, fixtureOptions{mode: mode})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), halt %q: a close-out's question in no always-ask class is the run's "+
			"to decide", result.State, result.Failure, result.Halt)
	}
	if len(result.Resumes) == 0 || result.Resumes[0].Reason != RefusedRoleAnswer {
		t.Errorf("the resumes were %+v, want the %s hold continued by the supervisor", result.Resumes,
			RefusedRoleAnswer)
	}
	for _, e := range feedStages(t, f.Repo.Dir, "r-fixture") {
		if e.Stage == StageSupervisionHalted {
			t.Errorf("the supervisor halted: %s", e.Detail)
		}
	}
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("the close-out is %s, want closed", current.Status)
	}
}

// The one close-out question that still holds for a person: one in an
// always-ask class of the standing orders (tick tyd). The supervisor does not
// continue across it, and nothing is dispatched again.
func TestASupervisedCloseoutWithAnAlwaysAskQuestionStillHolds(t *testing.T) {
	t.Parallel()
	const question = "the retro needs a production credential nobody gave me"
	f := newFixture(t, fixtureOptions{})
	declareStandingOrders(t, f.Repo)
	f.Runner = askingRunner(t, "co", question, "never")
	r, result, err := f.supervise(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedRoleAnswer || result.Halt == "" {
		t.Fatalf("the run ended %s (%+v), halt %q: an always-ask question is a person's", result.State,
			result.Failure, result.Halt)
	}
	if len(result.Resumes) != 0 {
		t.Errorf("the supervisor continued across an always-ask question: %+v", result.Resumes)
	}
	if got := len(tickAttempts(t, r, "co")); got != 1 {
		t.Errorf("co has %d attempts, want 1", got)
	}
}
