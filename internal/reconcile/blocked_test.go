package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A worker that stops to ask is not a stopped run (tick tyd).
//
// Before this tick a worker that committed and answered BLOCKED or
// NEEDS_CONTEXT made its attempt attempt_needs_human and the run stopped for a
// person. The ladder these tests pin: one tier up with the question; at the
// ceiling, a decide-and-log question decided under the standing orders and
// logged; only an always-ask question holds, naming the question, while the
// ticks that do not wait behind it keep going.

// ladderGate declares a ladder whose default is strong and whose ceiling is
// frontier: the first attempt runs at strong, and one question earns the rung.
const ladderGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.strong]
model = "opus"

[roles.implement.tiers.frontier]
model = "opus-frontier"

# The review cell's ceiling overlay: the on-demand resolve-conflict and
# plan-repair jobs route at the ceiling through it, and a run refuses at
# start when they cannot.
[roles.review]
kind = "claude"
model = "opus"

[roles.review.tiers.strong]
model = "opus"

[roles.review.tiers.frontier]
model = "opus"

[tier_policy]
default = "strong"
ceiling = "frontier"

[tier_policy.rate_limit]
response = "backoff-and-retry"
max_attempts = 8
max_delay_ms = 90000

[tier_policy.concurrency]
strong = 2
frontier = 1

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// ceilingGate is the same ladder with nothing above the default: the first
// attempt is already at the ceiling.
var ceilingGate = strings.Replace(ladderGate, `ceiling = "frontier"`, `ceiling = "strong"`, 1)

// standingOrdersConfig is a .tick/config.md in this repository's own shape.
const standingOrdersConfig = "# Tick Run Configuration\n\n## Rules\n\n- Nothing here declares a close-out rule.\n\n" +
	"## Standing orders\n\n" +
	"Library choice within the stack, naming, internal API shape, file layout, test strategy: **decide and log**. " +
	"Spending money, credentials and their grade, touching a live external system, removing scope, roadmap " +
	"changes, force-pushes: **always ask**.\n"

func declareStandingOrders(t *testing.T, repo *testRepo) {
	t.Helper()
	write(t, filepath.Join(repo.Dir, ".tick", "config.md"), standingOrdersConfig)
	mustRun(t, repo.Dir, "git", "add", ".tick/config.md")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "declare the standing orders")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// askingRunner is the fake runner in its `ask` mode: a1 commits and asks
// question until its prompt answers it (see fake-runner.sh).
func askingRunner(t *testing.T, tick, question, until string) []string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "internal", "reconcile", "testdata", "fake-runner.sh")
	return []string{"/usr/bin/env", "FAKE_RUNNER_MODE=ask", "FAKE_RUNNER_QUESTION=" + question,
		"FAKE_RUNNER_ASK_UNTIL=" + until, "FAKE_RUNNER_ASK_TICK=" + tick, "/bin/sh", script, "{{prompt}}"}
}

// BLOCKED at strong: the tick is dispatched again at frontier, cut from the
// stopped attempt's commits, with the question and its report in the prompt —
// and the run finishes the epic instead of stopping.
func TestABlockedAnswerBelowTheCeilingIsRedispatchedOneTierUpWithTheQuestion(t *testing.T) {
	t.Parallel()
	const question = "should the retry helper live in gitbin or in runstate"
	f := newFixture(t, fixtureOptions{gate: ladderGate})
	f.Runner = askingRunner(t, "a1", question, "escalated")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a question below the ceiling must not stop it", result.State, result.Failure)
	}
	if got := markerTierOfTry(t, r, "a1", 1); got != "strong" {
		t.Errorf("a1's first try ran at %q, want strong", got)
	}
	if got := markerTierOfTry(t, r, "a1", 2); got != "frontier" {
		t.Errorf("a1's second try ran at %q, want frontier: the question earns one rung", got)
	}

	escalated, ok := journalLine(r, "a1", StageBlockedEscalated)
	if !ok {
		t.Fatalf("no %s event: the step is not on the feed", StageBlockedEscalated)
	}
	if !strings.Contains(escalated, question) {
		t.Errorf("the %s event does not name the question: %s", StageBlockedEscalated, escalated)
	}

	dispatch := f.dispatch("a1")
	if dispatch.Escalation == nil {
		t.Fatal("the re-dispatch carried no escalation: the question is not in its prompt")
	}
	if !strings.Contains(dispatch.Escalation.Question, question) || dispatch.Escalation.Status != subprocess.StatusBlocked {
		t.Errorf("the escalation is %+v, want the BLOCKED question", dispatch.Escalation)
	}
	if dispatch.Escalation.AtCeiling {
		t.Errorf("the escalation says the ladder is over; it was one tier up")
	}
	if dispatch.Escalation.Report == "" {
		t.Errorf("the escalation names no report path")
	} else if _, err := os.Stat(dispatch.Escalation.Report); err != nil {
		t.Errorf("the escalation's report %s is not there: %v", dispatch.Escalation.Report, err)
	}
	prompt := subprocess.EscalationSection(dispatch.Escalation)
	for _, want := range []string{"An earlier attempt stopped because", question, "decide and proceed"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt section does not say %q:\n%s", want, prompt)
		}
	}

	// The carried work: the second try starts from the first try's commits.
	first, second := markerOfTry(t, r, "a1", 1), markerOfTry(t, r, "a1", 2)
	if second.ResumedFrom == nil || second.ResumedFrom.Attempt != first.Attempt {
		t.Fatalf("the second try resumed from %+v, want attempt %d's work", second.ResumedFrom, first.Attempt)
	}
	// The close retires the stopped attempt's branch, so its head is read off
	// the carry the marker recorded — and it must be work, not the base.
	if second.BaseSHA != second.ResumedFrom.SHA || second.BaseSHA == first.BaseSHA {
		t.Errorf("the second try was cut from %s, want the stopped attempt's head %s (not its base %s)",
			short(second.BaseSHA), short(second.ResumedFrom.SHA), short(first.BaseSHA))
	}
	if !containsCommit(t, f, second.ResumedFrom.SHA, "refs/remotes/origin/epic/qeu") {
		t.Errorf("the stopped attempt's commits %s are not on the integration branch", short(second.ResumedFrom.SHA))
	}

	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s, want closed behind the escalated attempt", current.Status)
	}
}

// At the ceiling a decide-and-log question is dispatched again with the
// instruction to decide it under the standing orders and log it; the worker
// decides, logs, and the tick closes.
func TestADecideAndLogQuestionAtTheCeilingIsDecidedAndLogged(t *testing.T) {
	t.Parallel()
	const question = "which naming convention should the new helper follow"
	f := newFixture(t, fixtureOptions{gate: ceilingGate})
	declareStandingOrders(t, f.Repo)
	f.Runner = askingRunner(t, "a1", question, "decide")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a decide-and-log question must not stop it", result.State, result.Failure)
	}
	if got := markerTierOfTry(t, r, "a1", 2); got != "strong" {
		t.Errorf("a1's second try ran at %q, want strong: the ceiling does not move", got)
	}
	decided, ok := journalLine(r, "a1", StageBlockedDecide)
	if !ok {
		t.Fatalf("no %s event", StageBlockedDecide)
	}
	if !strings.Contains(decided, question) {
		t.Errorf("the %s event does not name the question: %s", StageBlockedDecide, decided)
	}
	dispatch := f.dispatch("a1")
	if dispatch.Escalation == nil || !dispatch.Escalation.AtCeiling {
		t.Fatalf("the re-dispatch is %+v, want the decide-under-standing-orders instruction", dispatch.Escalation)
	}
	prompt := subprocess.EscalationSection(dispatch.Escalation)
	for _, want := range []string{question, "STANDING ORDERS", subprocess.EscalationDecisionsHeading,
		"Spending money, credentials"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the decide prompt does not carry %q:\n%s", want, prompt)
		}
	}

	// Logged: the worker's report carries the decision, and the run's record
	// lists how the question was answered for the PR.
	second := markerOfTry(t, r, "a1", 2)
	reports := r.priorReports("a1", second.Attempt+1)
	logged := false
	for _, report := range reports {
		if report.Attempt != second.Attempt {
			continue
		}
		raw, err := os.ReadFile(report.Path)
		if err != nil {
			t.Fatal(err)
		}
		logged = strings.Contains(string(raw), subprocess.EscalationDecisionsHeading) && strings.Contains(string(raw), question)
	}
	if !logged {
		t.Errorf("the deciding attempt's report does not log the decision")
	}
	if section := r.blockedHoldsSection(); !strings.Contains(section, question) || !strings.Contains(section, "decided under the standing orders") {
		t.Errorf("the PR's account of the question does not say it was decided:\n%s", section)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s, want closed behind the deciding attempt", current.Status)
	}
}

// At the ceiling an always-ask question holds, naming the question and its
// class, and only what waits behind it waits: a2 still closes.
func TestAnAlwaysAskQuestionAtTheCeilingHoldsNamingIt(t *testing.T) {
	t.Parallel()
	const question = "the migration needs a production credential nobody gave me"
	f := newFixture(t, fixtureOptions{gate: ceilingGate})
	declareStandingOrders(t, f.Repo)
	f.Runner = askingRunner(t, "a1", question, "decide")
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{"b1": {"a1"}}
	f.Tracker.write(t, state)

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.Reason != RefusedNeedsHuman {
		t.Fatalf("the run ended %s (%+v), want held as %s", result.State, result.Failure, RefusedNeedsHuman)
	}
	if !strings.Contains(result.Failure.Message, question) || !strings.Contains(result.Failure.Message, "credentials") {
		t.Errorf("the hold does not name the question and its class: %s", result.Failure.Message)
	}
	held, ok := journalLine(r, "a1", StageBlockedHeld)
	if !ok || !strings.Contains(held, question) {
		t.Errorf("the %s event does not name the question: %q", StageBlockedHeld, held)
	}
	if _, ok := journalLine(r, "a1", StageBlockedDecide); ok {
		t.Errorf("an always-ask question was dispatched to be decided")
	}
	if got := len(tickAttempts(t, r, "a1")); got != 1 {
		t.Errorf("a1 has %d attempts, want 1: an always-ask question at the ceiling is not re-dispatched", got)
	}

	// Other ticks keep going: a2 does not wait behind a1 and closes; b1 does.
	a2, err := f.Tracker.Show(context.Background(), "a2")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Status != "closed" {
		t.Errorf("a2 is %s: a held question stopped a tick that does not wait behind it", a2.Status)
	}
	if _, ok := journalLine(r, "b1", StageWaitsBehindHeld); !ok {
		t.Errorf("b1 is not recorded as waiting behind the held question")
	}
	if contains(r.Stages("b1"), StageDispatched) {
		t.Errorf("b1 was dispatched behind a held blocker")
	}

	// The PR body lists the held question.
	if section := r.blockedHoldsSection(); !strings.Contains(section, "Questions held for you") || !strings.Contains(section, question) {
		t.Errorf("the PR body's section does not list the held question:\n%s", section)
	}
}

// A role job runs outside the ladder, so its question meets the standing
// orders at once: a decide-and-log question is dispatched again to be decided,
// and the review then answers.
func TestARoleJobsDecideAndLogQuestionIsDecidedNotHeld(t *testing.T) {
	t.Parallel()
	const question = "should the review read the tests before the diff"
	f := newFixture(t, fixtureOptions{})
	declareStandingOrders(t, f.Repo)
	f.Runner = askingRunner(t, "rv", question, "decide")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a role job's decide-and-log question must not stop it", result.State, result.Failure)
	}
	decided, ok := journalLine(r, "rv", StageBlockedDecide)
	if !ok || !strings.Contains(decided, question) {
		t.Errorf("the review's %s event does not name the question: %q", StageBlockedDecide, decided)
	}
	if dispatch := f.dispatch("rv"); dispatch.Escalation == nil || !dispatch.Escalation.AtCeiling {
		t.Errorf("the review's re-dispatch is %+v, want the decide-under-standing-orders instruction", dispatch.Escalation)
	}
	if got := len(tickAttempts(t, r, "rv")); got != 2 {
		t.Errorf("rv has %d attempts, want 2: the question, then the decision", got)
	}
}

// The standing orders as this repository writes them, read.
//
// short: one file read and string matching; nothing is dispatched.
func TestStandingOrdersSortQuestionsIntoTheirClasses(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	orders := readStandingOrders(filepath.Join(root, ".tick", "config.md"))
	if !orders.Declared {
		t.Fatalf("this repository's standing orders declare no always-ask class: %+v", orders)
	}
	for question, want := range map[string]string{
		"which naming convention should the helper use":           "",
		"should the test live beside the file or in testdata":     "",
		"the job needs a production credential":                   "credentials and their grade",
		"do I force-push the rebased branch":                      "force-pushes",
		"should I drop the requirement for the cloud path":        "removing scope",
		"this needs a paid API subscription":                      "Spending money",
		"deliver the fix in the next wave or this one":            "",
		"shall I deploy to the live external system to verify it": "touching a live external system",
	} {
		if got := orders.alwaysAskClass(question); got != want {
			t.Errorf("%q is in always-ask class %q, want %q", question, got, want)
		}
	}
	// A repository that declares nothing gets the ticks defaults.
	none := parseStandingOrders("# config\n\n## Rules\n\n- none\n")
	if none.Declared || none.alwaysAskClass("the job needs an api key") == "" {
		t.Errorf("with no standing orders a credential question must still be always-ask: %+v", none)
	}
}

func tickAttempts(t *testing.T, r *Reconciler, tick string) []runstate.Attempt {
	t.Helper()
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	var out []runstate.Attempt
	for _, a := range attempts {
		if a.TickID == tick {
			out = append(out, a)
		}
	}
	return out
}

func markerOfTry(t *testing.T, r *Reconciler, tick string, try int) attemptHandle {
	t.Helper()
	mine := tickAttempts(t, r, tick)
	if try < 1 || try > len(mine) {
		t.Fatalf("%s has %d attempts, want at least %d", tick, len(mine), try)
	}
	lowest := make([]runstate.Attempt, len(mine))
	copy(lowest, mine)
	for i := range lowest {
		for j := i + 1; j < len(lowest); j++ {
			if lowest[j].Attempt < lowest[i].Attempt {
				lowest[i], lowest[j] = lowest[j], lowest[i]
			}
		}
	}
	return handleFromMap(lowest[try-1].JobHandle)
}

// askingEmptyRunner is askingRunner whose asking worker commits nothing
// before it asks: the no-commits shape.
func askingEmptyRunner(t *testing.T, tick, question, until string) []string {
	t.Helper()
	argv := askingRunner(t, tick, question, until)
	return append([]string{argv[0], "FAKE_RUNNER_ASK_COMMIT=no"}, argv[1:]...)
}

// A worker that asks with NOTHING committed takes the same in-run ladder: a
// tier up with the question, not collect_failed and a stop.
func TestABlockedAnswerWithNoCommitsIsRedispatchedOneTierUpInRun(t *testing.T) {
	t.Parallel()
	const question = "which package owns the retry helper"
	f := newFixture(t, fixtureOptions{gate: ladderGate})
	f.Runner = askingEmptyRunner(t, "a1", question, "escalated")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a question with nothing committed must not stop it", result.State, result.Failure)
	}
	if got := markerTierOfTry(t, r, "a1", 2); got != "frontier" {
		t.Errorf("a1's second try ran at %q, want frontier", got)
	}
	if escalated, ok := journalLine(r, "a1", StageBlockedEscalated); !ok || !strings.Contains(escalated, question) {
		t.Errorf("the %s event does not name the question: %q", StageBlockedEscalated, escalated)
	}
	if dispatch := f.dispatch("a1"); dispatch.Escalation == nil || !strings.Contains(dispatch.Escalation.Question, question) {
		t.Errorf("the re-dispatch's escalation is %+v, want the question", dispatch.Escalation)
	}
	if second := markerOfTry(t, r, "a1", 2); second.ResumedFrom != nil {
		t.Errorf("the second try carried %+v; a question with nothing committed has nothing to carry", second.ResumedFrom)
	}
}

// At the ceiling, a no-commit decide-and-log question is decided in-run; an
// always-ask one holds naming it, and holds again on resume rather than being
// redispatched.
func TestANoCommitQuestionAtTheCeilingIsDecidedOrHeldByName(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	t.Run("decide", func(t *testing.T) {
		t.Parallel()
		const question = "which file layout should the new tests use"
		f := newFixture(t, fixtureOptions{gate: ceilingGate})
		declareStandingOrders(t, f.Repo)
		f.Runner = askingEmptyRunner(t, "a1", question, "decide")
		r, result, err := f.run(f.Repo, fixtureOptions{})
		if err != nil {
			t.Fatalf("the run did not finish: %v", err)
		}
		if result.State != runstate.StateCompleted {
			t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
		}
		if decided, ok := journalLine(r, "a1", StageBlockedDecide); !ok || !strings.Contains(decided, question) {
			t.Errorf("the %s event does not name the question: %q", StageBlockedDecide, decided)
		}
	})
	t.Run("hold", func(t *testing.T) {
		t.Parallel()
		const question = "this needs a paid API subscription"
		f := newFixture(t, fixtureOptions{gate: ceilingGate})
		declareStandingOrders(t, f.Repo)
		f.Runner = askingEmptyRunner(t, "a1", question, "decide")
		_, result, err := f.run(f.Repo, fixtureOptions{})
		if err != nil {
			t.Fatalf("the run did not finish: %v", err)
		}
		if result.Failure == nil || result.Failure.Reason != RefusedNeedsHuman || !strings.Contains(result.Failure.Message, question) {
			t.Fatalf("the run failed as %+v, want %s naming the question", result.Failure, RefusedNeedsHuman)
		}
		r, resumed, err := f.run(f.Repo, fixtureOptions{})
		if err != nil {
			t.Fatalf("the resumed run did not finish: %v", err)
		}
		if resumed.Failure == nil || resumed.Failure.Reason != RefusedNeedsHuman || !strings.Contains(resumed.Failure.Message, question) {
			t.Fatalf("the resume failed as %+v, want the question held again", resumed.Failure)
		}
		if contains(r.Stages("a1"), StageDispatched) {
			t.Errorf("the resume dispatched a held question again: %v", r.Stages("a1"))
		}
	})
}
