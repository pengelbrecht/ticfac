package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The close-out admission precondition (tick 0iz), end to end through the
// real dispatch path: a target repo declaring the PR + CI rule in its own
// .tick/config.md has its close-out held by the RUN — the PR opened if
// absent, CI waited on until green, a red CI refused typed and NAMED — never
// by a close-out worker reading the config and reasoning correctly.
//
// The forge is the one thing faked here, for the same reason the agent is:
// these tests are about the run's own behaviour, and the GitHub surface's
// answers are proven against an httptest server in internal/forge.

// theCloseoutRule is the rule verbatim from this repository's own
// .tick/config.md — the same line the production run that routed this tick
// had to rediscover by hand. The fixture declaring it is therefore not an
// invented spelling: it is the one that exists.
const theCloseoutRule = "- Epic integration goes through a PR + CI gate: the orchestrator pushes the " +
	"epic branch and opens a PR; the epic close-out may not complete until the CI workflow " +
	"(.github/workflows/ci.yml) is green on that PR. No direct merges of epic branches to the " +
	"default branch."

// declareCloseoutRule commits a .tick/config.md declaring the rule to the
// fixture repo and pushes it, so that a fresh clone — the restarted run —
// reads the same declaration from the same authority the rule lives on.
func declareCloseoutRule(t *testing.T, repo *testRepo) {
	t.Helper()
	write(t, filepath.Join(repo.Dir, ".tick", "config.md"), strings.Join([]string{
		"# Tick Run Configuration",
		"",
		"## Rules",
		"",
		theCloseoutRule,
		"",
		"## Standing orders",
		"",
		"- nothing here mentions a PR",
		"",
	}, "\n"))
	mustRun(t, repo.Dir, "git", "add", ".tick/config.md")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "declare the PR + CI close-out rule")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// fakeForge is the code-hosting surface a test controls: what Find answers,
// whether Open succeeds, what CI says — in sequence, because the SEQUENCE
// is part of what the admission is — and whether the body can be rewritten
// at all (tick 4sb). Every call is recorded, so "the PR was opened before
// the close-out was dispatched" is a list, not an impression, and every
// body written is kept, so "a resumed close-out carried the findings once"
// is a count over strings, not an impression either.
type fakeForge struct {
	mu      sync.Mutex
	exists  bool  // Find's answer: does the PR already exist?
	openErr error // Open's failure, when a test wants "not admitted while no PR exists"
	ci      []forge.CIReport
	calls   []string
	pr      *forge.PullRequest

	// bodies is every body the run ever put on the PR, through Open or
	// UpdateBody, in order: the resumed-close-out tests read the SEQUENCE,
	// because "rewritten, not appended to" is a fact about two writes.
	bodies []string
	// updateErr is UpdateBody's failure, when a test wants the typed
	// refusal the body's absence produces.
	updateErr error

	// bySHA answers per commit rather than per call, which is what the
	// close-out's real problem needs: CI exists on one commit and not on the
	// newer one the run's own checkpoint just created. When it is nil the
	// queue above answers, exactly as before.
	bySHA map[string]forge.CIReport
}

var _ forge.PullRequests = (*fakeForge)(nil)

func (f *fakeForge) Find(_ context.Context, headRef, baseRef string) (*forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "find")
	if f.exists && f.pr != nil {
		return f.pr, nil
	}
	return nil, nil
}

func (f *fakeForge) Open(_ context.Context, headRef, baseRef, title, body string) (*forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "open")
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.pr = &forge.PullRequest{
		Number: 7, URL: "https://example.example/pull/7",
		HeadRef: headRef, HeadSHA: "fake-head-sha", BaseRef: baseRef,
	}
	f.exists = true
	f.bodies = append(f.bodies, body)
	return f.pr, nil
}

func (f *fakeForge) UpdateBody(_ context.Context, pr forge.PullRequest, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "update_body")
	if f.updateErr != nil {
		return f.updateErr
	}
	f.bodies = append(f.bodies, body)
	return nil
}

// body is the body the PR carries now: the last one written, which is the
// whole point of owning the body — the last write is the state, not one more
// entry in a pile.
func (f *fakeForge) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return f.bodies[len(f.bodies)-1]
}

// allBodies is every body the PR was ever given, in order: the rewrite
// tests read the SEQUENCE, because "rewritten, not appended to" is a fact
// about two writes.
func (f *fakeForge) allBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.bodies...)
}

func (f *fakeForge) CI(_ context.Context, pr forge.PullRequest) (forge.CIReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "ci")
	if f.bySHA != nil {
		report, ok := f.bySHA[pr.HeadSHA]
		if !ok {
			return forge.CIReport{State: forge.CINone}, nil
		}
		return report, nil
	}
	if len(f.ci) == 0 {
		return forge.CIReport{State: forge.CIGreen}, nil
	}
	report := f.ci[0]
	if len(f.ci) > 1 {
		f.ci = f.ci[1:]
	}
	return report, nil
}

func (f *fakeForge) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

func (f *fakeForge) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

// openPR is the PR a test that wants one pre-existing declares.
func openPR() *forge.PullRequest {
	return &forge.PullRequest{
		Number: 7, URL: "https://example.example/pull/7",
		HeadRef: "epic/qeu", HeadSHA: "fake-head-sha", BaseRef: "main",
	}
}

// ------------------------------------------------------------- the rule ---

func TestTheCloseoutRuleReader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		document string
		declared bool
		workflow string
	}{
		{
			name:     "the verbatim rule declares it, with its workflow",
			document: "## Rules\n\n" + theCloseoutRule + "\n",
			declared: true, workflow: ".github/workflows/ci.yml",
		},
		{
			name: "the rule without a named workflow defaults to ci.yml",
			document: "## Rules\n\n- Epic integration goes through a PR + CI gate; the epic " +
				"close-out may not complete until CI is green on that PR.\n",
			declared: true, workflow: DefaultCIWorkflow,
		},
		{
			name: "the same sentence outside the Rules section declares nothing",
			document: "## Standing orders\n\n- Epic integration goes through a PR + CI gate: " +
				"a prose note is not a rule a run enforces.\n",
		},
		{
			name:     "no rules at all",
			document: "# Tick Run Configuration\n\n## Rules\n\n- package management is pnpm only\n",
		},
	}
	for _, tc := range cases {
		rule, err := parseCloseoutRule(tc.document)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if rule.Declared != tc.declared {
			t.Errorf("%s: declared = %v, want %v", tc.name, rule.Declared, tc.declared)
		}
		if tc.declared && rule.CIWorkflow != tc.workflow {
			t.Errorf("%s: workflow = %q, want %q", tc.name, rule.CIWorkflow, tc.workflow)
		}
	}

	// A repo with no config.md at all declares nothing, and that is not an
	// error: most target repositories carry none.
	rule, err := ReadCloseoutRule(filepath.Join(t.TempDir(), "config.md"))
	if err != nil {
		t.Fatalf("a missing config.md is an error: %v", err)
	}
	if rule.Declared {
		t.Error("a missing config.md declared a rule")
	}
}

// This repository declares the rule its own run now enforces — the same
// dogfooding the runners.toml reader has: a mistake in the repo's own
// declaration breaks the run that routes the very workers building ticfac.
func TestThisRepoDeclaresTheRuleItNowEnforces(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rule, err := ReadCloseoutRule(filepath.Join(root, ".tick", "config.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !rule.Declared {
		t.Fatal("this repository's own .tick/config.md does not declare the PR + CI close-out rule")
	}
	if rule.CIWorkflow != ".github/workflows/ci.yml" {
		t.Errorf("the declared workflow is %q, want .github/workflows/ci.yml", rule.CIWorkflow)
	}
}

// ------------------------------------------------------------- admission ---

// A repo that declares the rule and a build with no surface behind it is
// refused at construction, before anything is claimed: the failure the tick
// routed in was two close-out attempts discovering it at the end of a run.
func TestARuleWithNoForgeRefusesConstruction(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	declareCloseoutRule(t, f.Repo)
	_, _, err := f.run(f.Repo, fixtureOptions{})
	if err == nil {
		t.Fatal("a repo declaring the close-out rule was accepted with no code-hosting surface")
	}
	for _, want := range []string{"PR + CI close-out rule", forge.TokenEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the construction refusal does not say %q: %v", want, err)
		}
	}
}

// No rule, no forge needed: the surface is never asked anything, and the
// close-out is admitted exactly as it was before the rule existed.
func TestNoRuleMeansNoForgeAndNoQuestions(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	_, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s", result.State)
	}
	if calls := forge.callLog(); len(calls) > 0 {
		t.Errorf("a repo that declares no rule was asked about PRs: %v", calls)
	}
}

// While no PR exists and none can be opened, the close-out is NOT admitted:
// nothing is claimed, nothing dispatched, the tick stays open, and the
// refusal names the precondition rather than reading as a generic failure.
func TestCloseoutIsNotAdmittedWhileNoPRExists(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{openErr: fmt.Errorf("GitHub answered 403: permission denied")}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	_, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCloseoutPR {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedCloseoutPR)
	}
	if !strings.Contains(result.Failure.Message, "no PR exists") {
		t.Errorf("the refusal does not name the unmet half: %q", result.Failure.Message)
	}
	// The close-out was never admitted: no dispatch for it, and the tracker
	// still holds the tick open. Everything before it completed, which is
	// what makes the next run cheap — re-run the epic and the admission is
	// re-derived from the PR, not rediscovered.
	if d := f.dispatch("co"); d.TickID != "" {
		t.Fatal("the close-out was dispatched while no PR existed")
	}
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatal("the close-out tick was closed behind an unmet precondition")
	}
	for _, tick := range []string{"a1", "a2", "b1", "rv"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s: the precondition is a close-out gate, not a work gate", tick, current.Status)
		}
	}
}

// The run opens the epic PR itself — the orchestrator's half of the rule —
// before the close-out is dispatched, and records the PR in the checkpoint
// on origin, so a resumed run and a person both read where it lives.
func TestTheRunOpensTheEpicPR(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	// The PR was opened, exactly once, into the default branch, and BEFORE
	// the close-out was dispatched — the find/open/ci order the admission
	// keeps, and the dispatch of co the admission precedes.
	if got := forge.count("open"); got != 1 {
		t.Errorf("the run opened the PR %d times, want 1", got)
	}
	log := forge.callLog()
	for _, want := range []string{"find", "open", "ci"} {
		if !contains(log, want) {
			t.Errorf("the forge saw %v, which does not include %q", log, want)
		}
	}
	if strings.Join(log[:2], ",") != "find,open" {
		t.Errorf("the forge saw %v: the PR was not looked for before it was opened", log)
	}
	if forge.pr == nil || forge.pr.BaseRef != "main" || forge.pr.HeadRef != r.IntegrationBranch() {
		t.Errorf("the opened PR is %+v", forge.pr)
	}
	if !contains(r.Stages("co"), StagePROpened) {
		t.Errorf("stages %v do not record the PR the run opened", r.Stages("co"))
	}
	// The PR fact is in the run's own durable record: a committed checkpoint
	// on origin names the PR, so a person — and a resumed run cut before the
	// close-out — reads where it lives instead of asking again.
	audit := mustRun(t, f.Repo.Dir, "git", "log", "-p", "origin/"+r.IntegrationBranch(),
		"--", ".ticfac/runs/r-fixture/checkpoint.json")
	if !strings.Contains(audit, "the epic PR #7") {
		t.Error("no committed checkpoint names the epic PR")
	}
}

// A red CI refuses the close-out typed, NAMING THE FAILING JOB — the exact
// answer the tick's acceptance demands, and the difference between a repair
// and a mystery. The failing job is in the refusal, in the feed, and in the
// durable checkpoint the run leaves.
func TestRedCIRefusesTheCloseoutNamingTheFailingJob(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{exists: true, pr: openPR(),
		ci: []forge.CIReport{{State: forge.CIRed, Failing: []string{"go", "pi-runner"}}}}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCloseoutCI {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedCloseoutCI)
	}
	// The failing job is NAMED, not summarized as "CI failed": the two jobs
	// the fake reports are the repair's address.
	for _, job := range []string{"go", "pi-runner"} {
		if !strings.Contains(result.Failure.Message, job) {
			t.Errorf("the refusal does not name the failing job %q: %q", job, result.Failure.Message)
		}
	}
	if !strings.Contains(result.Failure.Message, "#7") {
		t.Errorf("the refusal does not name the PR it is about: %q", result.Failure.Message)
	}
	// The feed line says which half is unmet, and no close-out happened.
	var held string
	for _, e := range r.Journal() {
		if e.Tick == "co" && e.Stage == StageCloseoutHeld {
			held = e.Detail
		}
	}
	if !strings.Contains(held, "red") || !strings.Contains(held, "pi-runner") {
		t.Errorf("the feed line does not name the failing job: %q", held)
	}
	if d := f.dispatch("co"); d.TickID != "" {
		t.Fatal("the close-out was dispatched behind a red CI")
	}
	// The durable checkpoint carries the same name: a person reading the
	// run's record reads the repair's address, not "the run broke".
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint on origin: %v", err)
	}
	if !strings.Contains(checkpoint.Reason, "pi-runner") {
		t.Errorf("the durable checkpoint does not name the failing job: %q", checkpoint.Reason)
	}
}

// A pending CI is a HOLD, not a failure: the run waits on the PR's CI the
// way it waits on a job, and admits the close-out the moment it turns
// green. The wait is what "holds close-out until CI is green" means.
func TestCloseoutIsHeldUntilCITurnsGreen(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{exists: true, pr: openPR(),
		ci: []forge.CIReport{{State: forge.CIPending}, {State: forge.CIPending},
			{State: forge.CIGreen}}}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if got := forge.count("ci"); got < 3 {
		t.Errorf("CI was asked %d times; the run did not wait on it", got)
	}
	stages := r.Stages("co")
	if !contains(stages, StageCloseoutHeld) {
		t.Errorf("stages %v do not record the hold", stages)
	}
	if !contains(stages, StageCloseoutAdmitted) {
		t.Errorf("stages %v do not record the admission", stages)
	}
	if contains(stages, StageRejected) {
		t.Errorf("stages %v reject a close-out whose CI turned green", stages)
	}
	// And the close-out really happened: the tick closed behind its answer.
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("co is %s, want closed", current.Status)
	}
}

// No CI on the PR at all is not pending: it is unsatisfiable by waiting —
// the failure mode the rule exists to surface, because a workflow that does
// not trigger on pull_request runs nothing on any PR.
func TestNoCIOnThePRRefusesUnsatisfiable(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{exists: true, pr: openPR(), ci: []forge.CIReport{{State: forge.CINone}}}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	_, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCloseoutCIAbsent {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedCloseoutCIAbsent)
	}
	for _, want := range []string{"no CI has run", ".github/workflows/ci.yml", "pull_request"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the refusal does not say %q: %q", want, result.Failure.Message)
		}
	}
}

// A CI that never concludes is a wait the run BOUNDS: the refusal names the
// clock, and re-running the epic re-derives the admission from the PR.
// ciWaitBound is what the CI-wait tests set GateTimeout to.
//
// One knob bounds two different things: how long the close-out waits for a
// pending CI answer, AND how long any single gate COMMAND may run. The CI wait
// wants a small number so the test is quick; the gate command wants a number
// comfortably above shell noise, because the fixture's gate is
// `test -f README.md && ls work-*.txt` and killing that produces
// "tree (error)" instead of the CI refusal the test is about.
//
// At 200ms those two wants collided. The command takes about five milliseconds
// on an idle machine and sailed past 200ms under the integrated gate's load,
// so this test failed claiming tick a2's gate errored — refusing tick sqx for
// the machine's speed rather than anything in the tree. Two seconds is still
// fast and is far outside anything a two-command shell script does.
const ciWaitBound = 2 * time.Second

func TestAPendingCIThatNeverConcludesIsBounded(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{exists: true, pr: openPR(),
		ci: []forge.CIReport{{State: forge.CIPending}}}
	f := newFixture(t, fixtureOptions{pullRequests: forge, gateTimeout: ciWaitBound})
	declareCloseoutRule(t, f.Repo)
	_, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge, gateTimeout: ciWaitBound})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCloseoutCIPending {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedCloseoutCIPending)
	}
	if !strings.Contains(result.Failure.Message, "pending") {
		t.Errorf("the refusal does not name the state it stopped in: %q", result.Failure.Message)
	}
}

// A resumed run does not re-open the PR or re-ask anything it can read: cut
// after the PR is opened, the next incarnation finds the PR the first one
// opened — the forge is the PR's authority the way origin is the run's — and
// proceeds to admit the close-out.
func TestAResumedRunFindsThePROpenedAndDoesNotOpenAnother(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{pullRequests: forge, stopAfter: stopAt("co", StagePROpened)})
	declareCloseoutRule(t, f.Repo)
	_, _, err := f.run(f.Repo, fixtureOptions{pullRequests: forge, stopAfter: stopAt("co", StagePROpened)})
	killedAfter(t, err, "co", StagePROpened)

	// The restarted run reads everything from origin, on a fresh clone.
	repo := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restart"))
	_, result, err := f.run(repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restarted run ended %s: %s", result.State, result.Reason)
	}
	if got := forge.count("open"); got != 1 {
		t.Errorf("the PR was opened %d times across two incarnations, want 1", got)
	}
	if got := forge.count("find"); got < 2 {
		t.Errorf("the PR was looked for %d times across two incarnations, want at least 2", got)
	}
}

// ------------------------------------------------- the close-out's close ---

// The close-out's OWN commits are the one head the admission's green CI is
// not evidence about (tick sqx): the close-out writes — a retro, learnings
// — and those integrate onto the epic branch, which IS the PR's head, so CI
// runs again on a tree nobody gated. The pwp run is why the gap is not
// theoretical: its close-out committed records a public-repo guard then
// failed on, the admission stayed green, and the close stood behind
// evidence about a head that no longer existed.
//
// A close-out whose own commits turn the PR's CI red is NOT closed: the
// refusal names the failing job, the merge already on the integration
// branch stays there for the repair to fix, and the tick stays open.
func TestCloseoutOwnCommitsThatFailCIHoldTheClose(t *testing.T) {
	t.Parallel()
	// The CI sequence is the tick's whole point: green on the head as it
	// stood when the close-out STARTED, red on the head its own commits
	// made. The same PR, two different heads, two different answers.
	forge := &fakeForge{ci: []forge.CIReport{
		{State: forge.CIGreen}, // the admission: the head before the close-out
		{State: forge.CIRed, Failing: []string{"public-repo-guard", "go"}}, // the head after it
	}}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCloseoutCIOnClose {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedCloseoutCIOnClose)
	}
	// The failing job is NAMED, and so is the PR the red CI is on: the
	// repair's address, not "the run broke".
	for _, job := range []string{"public-repo-guard", "go"} {
		if !strings.Contains(result.Failure.Message, job) {
			t.Errorf("the refusal does not name the failing job %q: %q", job, result.Failure.Message)
		}
	}
	if !strings.Contains(result.Failure.Message, "#7") {
		t.Errorf("the refusal does not name the PR it is about: %q", result.Failure.Message)
	}
	// The feed line names the failing job too, at the close-out's scope.
	var held string
	for _, e := range r.Journal() {
		if e.Tick == "co" && e.Stage == StageCloseoutHeld {
			held = e.Detail
		}
	}
	if !strings.Contains(held, "red") || !strings.Contains(held, "public-repo-guard") {
		t.Errorf("the feed line does not name the failing job: %q", held)
	}
	// The close-out RAN — it was dispatched, and its commits are merged onto
	// the integration branch. What is held is the CLOSE, not the merge: the
	// work stays where CI can see it, so the repair is the tree the failing
	// job names and not a redone close-out.
	if d := f.dispatch("co"); d.TickID == "" {
		t.Fatal("the close-out was never dispatched")
	}
	mustRun(t, f.Repo.Dir, "git", "cat-file", "-e", "origin/"+r.IntegrationBranch()+":work-co.txt")
	// And the tick is not closed behind its own red CI.
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatal("the close-out tick was closed behind a CI its own commits turned red")
	}
	// Everything before the close-out closed as usual: the close gate is a
	// gate on the close-out's close, not a work gate on the epic.
	for _, tick := range []string{"a1", "a2", "b1", "rv"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s: the close-out's close gate is not a work gate", tick, current.Status)
		}
	}
}

// The close's other half: green on the head that includes the close-out's
// own commits CLOSES the tick, through the same wait the admission keeps —
// a pending CI on that head is a hold, and the close proceeds the moment it
// turns green.
func TestCloseoutClosesBehindGreenCIOnItsOwnCommits(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{ci: []forge.CIReport{
		{State: forge.CIGreen},   // the admission
		{State: forge.CIPending}, // the close-out's own commits just pushed
		{State: forge.CIGreen},   // ... and CI concludes on them
	}}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	stages := r.Stages("co")
	if !contains(stages, StageCloseoutCloseGated) {
		t.Errorf("stages %v do not record the close gate's green CI", stages)
	}
	if !contains(stages, StageCloseoutHeld) {
		t.Errorf("stages %v do not record the hold while CI on the close-out's own head was pending", stages)
	}
	if contains(stages, StageRejected) {
		t.Errorf("stages %v reject a close-out whose own commits' CI turned green", stages)
	}
	if got := forge.count("ci"); got != 3 {
		t.Errorf("CI was asked %d times, want 3: admission, pending, green", got)
	}
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("co is %s, want closed behind green CI on its own commits", current.Status)
	}
}
