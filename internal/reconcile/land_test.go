package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The READY epic PR, kept ready, and the opt-in merge (land.go; operator
// decisions 2026-09-28).
//
// Epic 2jn, after its run completed: main moved (other PRs merged), the epic
// PR's CI went red on a semantic conflict, and a person folded main into
// epic/2jn by hand with tk's merge drivers, resolved the conflicts, re-ran the
// gate, pushed, waited for CI — then merged by hand, meeting more conflicts
// from a PR that landed meanwhile. These tests drive every one of those steps
// through the run's own machinery, against a real repository whose base moves
// while the run is at the close-out or after it completed.

// landingRule is the opt-in, verbatim from this repository's own config: the
// run merges its own PR once it is ready.
const landingRule = "- Epic integration goes through a PR + CI gate: the orchestrator pushes the epic branch and " +
	"opens a PR; the epic close-out may not complete until CI is green on that PR. No direct merges of epic " +
	"branches to the default branch other than the run's own: **the run merges its own PR** once it is ready."

// declareRule commits a .tick/config.md with one Rules line and pushes it.
func declareRule(t *testing.T, repo *testRepo, line string) {
	t.Helper()
	write(t, filepath.Join(repo.Dir, ".tick", "config.md"),
		"# Tick Run Configuration\n\n## Rules\n\n"+line+"\n\n## Standing orders\n\n- nothing here mentions a PR\n")
	mustRun(t, repo.Dir, "git", "add", ".tick/config.md")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "declare the close-out rule")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// landingForge is a code-hosting surface that behaves like GitHub where
// these tests need it to: the PR's head IS the epic branch's head on origin,
// and a PR whose head the base contains reads as merged (Find answers none).
// CI answers per commit through `ci` (green when nil), and `onCI` is called
// on every CI read with its 1-based count — where a test moves the base, the
// way another PR merging would, at a chosen moment of the run.
type landingForge struct {
	mu     sync.Mutex
	origin string
	opened bool
	bodies []string
	reads  int
	ci     func(sha string) forge.CIReport
	onCI   func(n int)
}

var _ forge.PullRequests = (*landingForge)(nil)

func (f *landingForge) pr() *forge.PullRequest {
	head := strings.TrimSpace(runGitQuiet(f.origin, "rev-parse", refFor("epic/qeu")))
	body := ""
	if len(f.bodies) > 0 {
		body = f.bodies[len(f.bodies)-1]
	}
	return &forge.PullRequest{Number: 7, URL: "https://example.example/pull/7", HeadRef: "epic/qeu",
		HeadSHA: head, BaseRef: "main", Body: body}
}

func (f *landingForge) Find(_ context.Context, headRef, _ string) (*forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.opened {
		return nil, nil
	}
	pr := f.pr()
	if mustRunAllowingFailure(f.origin, "git", "merge-base", "--is-ancestor", pr.HeadSHA, refFor("main")) {
		return nil, nil // merged: GitHub closes a PR whose head its base contains
	}
	return pr, nil
}

func (f *landingForge) Open(_ context.Context, _, _, _, body string) (*forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = true
	f.bodies = append(f.bodies, body)
	return f.pr(), nil
}

func (f *landingForge) UpdateBody(_ context.Context, _ forge.PullRequest, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	return nil
}

func (f *landingForge) CI(_ context.Context, pr forge.PullRequest) (forge.CIReport, error) {
	f.mu.Lock()
	f.reads++
	n, hook, answer := f.reads, f.onCI, f.ci
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if answer != nil {
		return answer(pr.HeadSHA), nil
	}
	return forge.CIReport{State: forge.CIGreen}, nil
}

func (f *landingForge) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return f.bodies[len(f.bodies)-1]
}

// mergeMain lands a change on main the way another PR would: through a clone
// of its own, never the reconciler's checkout.
func mergeMain(t *testing.T, f *fixture, path, content, message string) string {
	t.Helper()
	dir := filepath.Join(f.Root, "another-pr")
	if _, err := os.Stat(dir); err != nil {
		cloneRepo(t, f.Repo.Origin, dir)
	}
	mustRun(t, dir, "git", "fetch", "--quiet", "origin", "main")
	mustRun(t, dir, "git", "checkout", "--quiet", "-B", "main", "FETCH_HEAD")
	writeUnder(t, dir, path, content)
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", message)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "main")
	return strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
}

// onOrigin reports whether origin's branch carries a commit.
func onOrigin(f *fixture, commit, branch string) bool {
	return mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", commit, refFor(branch))
}

func originHead(t *testing.T, f *fixture, branch string) string {
	t.Helper()
	return strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor(branch)))
}

// --------------------------------------------------------------- the rule ---

// short: parses config text in memory; no repository, no run
func TestWhoMergesTheReadyPRIsTheRepositorysOwnRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                string
		rule                string
		lands, personMerges bool
	}{
		{"the gate alone: a person merges, by default", theCloseoutRule, false, false},
		{"this repository's old rule: the merge itself is a person's",
			theCloseoutRule + " **The merge itself is a person's**, and deliberately so.", false, true},
		{"the ticks repository's rule", theCloseoutRule + " **A run never merges its own PR** — not the local " +
			"orchestrator, not a cloud run.", false, true},
		{"the opt-in", landingRule, true, false},
		{"the opt-in beside a person's merge: the person wins",
			landingRule + " The merge itself is a person's.", false, true},
	}
	for _, tc := range cases {
		rule, err := parseCloseoutRule("## Rules\n\n" + tc.rule + "\n")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !rule.Declared {
			t.Fatalf("%s: the PR + CI gate is not declared", tc.name)
		}
		if rule.Lands() != tc.lands || rule.PersonMerges != tc.personMerges {
			t.Errorf("%s: lands=%v personMerges=%v, want lands=%v personMerges=%v", tc.name, rule.Lands(),
				rule.PersonMerges, tc.lands, tc.personMerges)
		}
	}
	// An opt-in with no PR + CI gate merges nothing: there is no PR.
	if rule, _ := parseCloseoutRule("## Rules\n\n- the run merges its own PR\n"); rule.Lands() {
		t.Error("a repository with no PR + CI gate opted in to merging a PR it never opens")
	}
}

// short: pure text classification; no repository, no run
func TestALiveRunRemedyIsRecognisedAndOrdinaryWorkIsNot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		title, body string
		live        bool
	}{
		{"Demonstrate 2jn's A1 and A5 live: ticfac init + ticfac run on claude in herdr, and one epic via run " +
			"--cloud, on the merged binary", "", true},
		{"Demonstrate the resume path on a real substrate", "Run one epic end to end in herdr.", true},
		{"Live run of the cloud factory needed", "Run the next epic via ticfac run --cloud.", true},
		{"ticfac run --cloud crashes when the factory is down", "Stack trace attached.", false},
		{"Demonstrate that the parser handles CRLF", "Add a test.", false},
		{"The live view flickers", "ticfac run redraws twice.", false},
	} {
		got := needsLiveRun(runstate.Finding{Title: tc.title, Body: tc.body})
		if got != tc.live {
			t.Errorf("needsLiveRun(%q) = %v, want %v", tc.title, got, tc.live)
		}
	}
}

// ------------------------------------------------ the ready PR, by default ---

// THE 2jn SHAPE, WITHOUT THE PERSON. main moves while the run is at its
// close-out — another PR merges — and the run does not end at a PR that is
// stale against it: it folds main in, gates the fold and waits for CI green
// on it, and the PR is ready. The merge stays a person's: main does not carry
// the epic, and the body tells the reviewer where to look first.
func TestABaseThatMovesBeforeTheRunEndsIsFoldedInAndThePRIsReady(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareCloseoutRule(t, f.Repo)
	var moved string
	pr.onCI = func(n int) {
		if n == 2 { // the close-out's close gate: another PR lands on main
			moved = mergeMain(t, f, "other-pr.txt", "another PR's work\n", "another PR merges")
		}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a base that moved is the run's to fold in", result.State, result.Failure)
	}
	if moved == "" {
		t.Fatal("the base never moved: the test did not reach the close-out's close gate")
	}
	if !onOrigin(f, moved, "epic/qeu") {
		t.Errorf("epic/qeu does not carry main's new commit %s: the PR was left stale", short(moved))
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged a PR the repository leaves to a person")
	}
	stages := r.Stages("co")
	for _, want := range []string{StageLanding, StageGatePassed, StageLandCIGreen, StagePRReady} {
		if !contains(stages, want) {
			t.Errorf("the close-out's stages %v do not record %s", stages, want)
		}
	}
	if contains(stages, StageLanded) {
		t.Errorf("the stages %v record a merge the repository leaves to a person", stages)
	}
	body := pr.body()
	for _, want := range []string{"## Where to look first", "## What this epic did", "## Definition of done",
		"## The review's verdict", "## Readiness", "The merge is yours", "`a1`"} {
		if !strings.Contains(body, want) {
			t.Errorf("the PR body does not carry %q for its reviewer:\n%s", want, body)
		}
	}
	if !strings.Contains(result.Reason, "is ready") {
		t.Errorf("the run's terminal reason does not say the PR is ready: %q", result.Reason)
	}
}

// A COMPLETED RUN RE-ENTERED keeps its PR ready: main moves after the run
// completed, and running the epic again folds it in, gates the fold and waits
// for CI — no person merges main into the epic branch by hand.
func TestReRunningACompletedEpicReadiesItsPRAgainstTheMovedBase(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareCloseoutRule(t, f.Repo)
	if _, result, err := f.run(f.Repo, fixtureOptions{pullRequests: pr}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the first run did not complete: %v %+v", err, result)
	}
	moved := mergeMain(t, f, "later-pr.txt", "a PR that merged after the epic's run\n", "a later PR merges")
	started := totalStarts(f)

	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: pr})
	if err != nil {
		t.Fatalf("the re-entry: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the re-entered run ended %s (%+v)", result.State, result.Failure)
	}
	if !onOrigin(f, moved, "epic/qeu") {
		t.Errorf("epic/qeu does not carry the later commit %s after the re-entry", short(moved))
	}
	if !contains(r.Stages("co"), StagePRReady) || !contains(r.Stages("co"), StageGatePassed) {
		t.Errorf("the re-entry did not gate the fold and ready the PR: %v", r.Stages("co"))
	}
	if now := totalStarts(f); now != started {
		t.Errorf("the re-entry started %d job(s): a clean fold needs no worker, and nothing the completed run "+
			"finished is redone", now-started)
	}
}

// totalStarts is every job start the fixture's executors were asked for.
func totalStarts(f *fixture) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, count := range f.starts {
		n += count
	}
	return n
}

// A RED CI ON THE FOLDED HEAD is a gate failure the repair job answers, not a
// hold: main moved, the fold's CI goes red, the plan-repair job commits the
// fix the failing job named, its merge is gated as usual, CI turns green, and
// the PR is ready — with the repair listed first for its reviewer.
func TestARedCIAfterTheFoldIsRepairedByTheRepairJob(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "land_repair", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareCloseoutRule(t, f.Repo)
	var moved string
	pr.onCI = func(n int) {
		if n == 2 {
			moved = mergeMain(t, f, "semantic.txt", "a change that breaks the epic's tree\n", "a PR that breaks the epic")
		}
	}
	pr.ci = func(sha string) forge.CIReport {
		// Red on any tree that carries main's breaking change and not the
		// repair's fix: the semantic conflict 2jn's CI caught.
		if moved != "" && mustRunAllowingFailure(pr.origin, "git", "merge-base", "--is-ancestor", moved, sha) &&
			!mustRunAllowingFailure(pr.origin, "git", "cat-file", "-e", sha+":ci-fix.txt") {
			return forge.CIReport{State: forge.CIRed, Failing: []string{"go"}}
		}
		return forge.CIReport{State: forge.CIGreen}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "land_repair", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): red CI after the fold is the repair job's, not a stop",
			result.State, result.Failure)
	}
	if got := showOnOrigin(t, f, "epic/qeu", "ci-fix.txt"); !strings.Contains(got, "the fix the red CI job named") {
		t.Errorf("the repair's fix is not on the epic branch: %q", got)
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	repaired := false
	for _, d := range decisions {
		if d.Role == RoleRepairGate && d.Response["status"] == "merged" {
			repaired = true
		}
	}
	if !repaired {
		t.Error("no merged repair decision is recorded for the red CI")
	}
	evidence := false
	for _, key := range store.EvidenceKeys() {
		if strings.HasPrefix(key, "land-ci-co-") {
			evidence = true
		}
	}
	if !evidence {
		t.Errorf("the red CI was not recorded as the repair's evidence: %v", store.EvidenceKeys())
	}
	if spec := f.spec("co"); spec == nil || spec.Role != RoleRepairGate {
		t.Errorf("the last job started for the close-out is %+v, want the repair job", spec)
	}
	if !contains(r.Stages("co"), StagePRReady) {
		t.Errorf("the PR was not readied after the repair: %v", r.Stages("co"))
	}
	if body := pr.body(); !strings.Contains(body, "REPAIRED") {
		t.Errorf("the reviewer is not told first that the run repaired the tree itself:\n%s", body)
	}
}

// ------------------------------------------------------- the opt-in merge ---

// THE OPT-IN, AND THE BASE THAT KEEPS MOVING. The repository says the run
// merges its own PR. main moves at the close-out, and again while the run is
// waiting on the fold's CI: the first pass sees the base moved and goes round,
// the second folds again, and the epic lands as a merge commit on main — the
// base's head and the epic's head its parents — with the PR merged by it and
// CI on main verified.
func TestAnOptInRepoMergesTheReadyPRAfterTheBaseMovesTwice(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)
	var first, second string
	pr.onCI = func(n int) {
		switch n {
		case 2:
			first = mergeMain(t, f, "first-pr.txt", "one PR\n", "a first PR merges")
		case 3:
			second = mergeMain(t, f, "second-pr.txt", "another PR\n", "a second PR merges meanwhile")
		}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
	}
	if first == "" || second == "" {
		t.Fatal("the base did not move twice: the test did not reach the readying's CI")
	}
	stages := r.Stages("co")
	for _, want := range []string{StageLandBaseMoved, StageLanded, StageLandVerified} {
		if !contains(stages, want) {
			t.Errorf("the stages %v do not record %s", stages, want)
		}
	}
	main := originHead(t, f, "main")
	parents := strings.Fields(mustRun(t, f.Repo.Origin, "git", "rev-list", "--parents", "-n", "1", main))
	if len(parents) != 3 {
		t.Fatalf("main's head %s is not a merge commit (parents %v): the epic must land as a merge, never a "+
			"squash or a fast-forward", short(main), parents)
	}
	if parents[1] != second {
		t.Errorf("the merge's first parent is %s, want main's head %s", short(parents[1]), short(second))
	}
	for _, commit := range []string{first, second} {
		if !onOrigin(f, commit, "epic/qeu") {
			t.Errorf("the epic branch never folded in %s", short(commit))
		}
	}
	if got := showOnOrigin(t, f, "main", "work-a1.txt"); got == "" {
		t.Error("main does not carry the epic's work after the merge")
	}
	// GitHub marks the PR merged at the push, because main then contains
	// its head; since then the epic branch carries only the run's own
	// closing records under .ticfac/.
	merged := parents[2]
	if !onOrigin(f, merged, "epic/qeu") {
		t.Errorf("the merge's second parent %s is not the epic branch's commit", short(merged))
	}
	if changed := mustRun(t, f.Repo.Origin, "git", "diff", "--name-only", merged, refFor("epic/qeu")); !allUnder(changed, ".ticfac/") {
		t.Errorf("the epic branch moved past the merged head with more than run state:\n%s", changed)
	}
	if !strings.Contains(result.Reason, "merged into main") {
		t.Errorf("the terminal reason does not say the epic is merged: %q", result.Reason)
	}
}

// A CONFLICTING FOLD at the merge is the resolve job's: main edits the same
// file the epic did after the run reached its close-out, the fold conflicts,
// the resolve-conflict job makes the union, and the epic still lands.
func TestAConflictingBaseFoldBeforeTheMergeIsResolvedAndTheEpicLands(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "land_repair", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)
	commitOnBase(t, f.Repo, "deps.txt", "require example.com/a v1.0.0\n", "the manifest both sides start from")
	forkIntegrationBranch(t, f.Repo, "epic/qeu")
	commitOnIntegrationBranch(t, f, "epic/qeu", "deps.txt",
		"require example.com/a v1.0.0\nrequire example.com/epic v0.3.0\n", "the epic adds a requirement")
	pr.onCI = func(n int) {
		if n == 2 {
			mergeMain(t, f, "deps.txt", "require example.com/a v1.2.0\n", "main bumps a requirement")
		}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "land_repair", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a conflicting fold is the resolve job's", result.State, result.Failure)
	}
	if len(baseFoldDecisions(t, r)) != 1 {
		t.Errorf("the conflicting fold was not resolved by exactly one resolve job: %d", len(baseFoldDecisions(t, r)))
	}
	if got := strings.TrimSpace(showOnOrigin(t, f, "main", "deps.txt")); got != "resolved by the resolve-conflict job" {
		t.Errorf("main's manifest is %q, not the resolution the epic landed with", got)
	}
	if !contains(r.Stages("co"), StageLanded) {
		t.Errorf("the epic did not land: %v", r.Stages("co"))
	}
}

// --------------------------------------------------- the live-run finding ---

// A FINDING WHOSE REMEDY IS A LIVE RUN never becomes a worker's tick (epic
// 2jn's tm5): it claims done item A1, which the fallback would absorb into the
// epic — and then dispatch a worker at running a whole epic. The run's rule
// files it as a backlog tick OUTSIDE the epic, labelled for the next epic run,
// and no worker is ever started on it.
func TestALiveRunFindingIsBackloggedForTheNextEpicRunAndNeverDispatched(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "finding_live_run"})
	setEpicAcceptance(t, f, "[A1] ticfac init then ticfac run starts an epic in herdr.")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_live_run"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a live-run finding must never hold or stall the epic",
			result.State, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating || record.Basis != runstate.AbsorptionRule || record.Placement != runstate.AbsorptionNextRun {
		t.Fatalf("the decision is %+v, want the rule's non-gating backlog", record)
	}
	if !strings.Contains(record.Reason, "live run") || !strings.Contains(record.Reason, "A1") {
		t.Errorf("the decision does not say why a claim on A1 is not the epic's: %q", record.Reason)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	tick, ok := state.Ticks[record.TickID]
	if !ok {
		t.Fatalf("the backlog tick %s does not exist", record.TickID)
	}
	if tick.Parent != "" || tick.Status != "open" {
		t.Errorf("the live-run tick is parent %q, status %s: it must be an open backlog tick outside the epic",
			tick.Parent, tick.Status)
	}
	if !hasLabel(tick, liveRunLabel) || !strings.HasPrefix(tick.Title, "Next epic run: ") {
		t.Errorf("the live-run tick is not flagged for the next epic run: title %q labels %v", tick.Title, tick.Labels)
	}
	if d := f.dispatch(record.TickID); d.TickID != "" {
		t.Errorf("a worker was dispatched at the live-run tick %s: %+v", record.TickID, d)
	}
	if children := epicChildren(t, f); children[record.TickID] != "" {
		t.Errorf("the live-run tick %s is a child of the epic", record.TickID)
	}
}

// allUnder reports whether every path of a `git diff --name-only` lives
// under a prefix.
func allUnder(names, prefix string) bool {
	for _, name := range strings.Split(strings.TrimSpace(names), "\n") {
		if name != "" && !strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func hasLabel(tick tk.Tick, label string) bool {
	for _, l := range tick.Labels {
		if l == label {
			return true
		}
	}
	return false
}
