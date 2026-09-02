package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The three role profiles, at the two places they have to be visible: what was
// DISPATCHED under them, and what the durable record says they were.

func TestEachRoleIsDispatchedUnderItsOwnProfileAndRecordedWithIt(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	profiles, err := profile.ResolveAll(profile.Options{RunnersConfig: f.Repo.Dir + "/.tick/runners.toml"})
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{"a1": "implement-tick", "a2": "implement-tick", "b1": "implement-tick",
		"rv": "review-epic", "co": "closeout-epic"}

	store := openStore(t, f, r)
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 5 {
		t.Fatalf("%d attempt records for five dispatches", len(attempts))
	}
	seen := map[string]bool{}
	for _, attempt := range attempts {
		role := roles[attempt.TickID]
		want := profiles[role]
		provenance := attempt.Provenance

		// The three claims a Phase 1 attempt record has to be able to make
		// about its profile: which role, which model it routed to, and the
		// digest of the profile itself.
		if provenance.Role == nil || *provenance.Role != role {
			t.Errorf("attempt for %s names role %v, want %s", attempt.TickID, provenance.Role, role)
		}
		if provenance.Model == nil || *provenance.Model != want.Model {
			t.Errorf("attempt for %s names model %v, want %s", attempt.TickID, provenance.Model, want.Model)
		}
		if provenance.ProfileDigest == nil || *provenance.ProfileDigest != want.Digest {
			t.Errorf("attempt for %s digests profile %v, want %s", attempt.TickID, provenance.ProfileDigest, want.Digest)
		}
		// The phase is the role's, not everybody's: the same command at a
		// different phase answers a different question.
		if got, want := provenance.Phase, phaseFor(role); got != want {
			t.Errorf("attempt for %s is phase %s, want %s", attempt.TickID, got, want)
		}
		seen[role] = true
	}
	for role := range roles {
		if !seen[roles[role]] {
			t.Errorf("no attempt was recorded under %s", roles[role])
		}
	}

	// The review's profile is not the implementer's, and the record can tell
	// them apart: a run whose evidence digested one profile for every role
	// could not say what any of it was made under.
	if profiles["review-epic"].Digest == profiles["implement-tick"].Digest {
		t.Error("two roles resolved the same profile digest")
	}
}

// The routing an operator already keeps in `.tick/runners.toml` for `tk herd`
// is the routing a ticfac run honours: one place, not two.
func TestTheTargetRepositoriesRolesTableRoutesTheRun(t *testing.T) {
	const routed = passingGate + `
[roles.implement]
kind = "codex"
model = "gpt-5.6-luna"

[roles.review]
kind = "pi"
model = "pi-large"
`
	f := newFixture(t, fixtureOptions{gate: routed})
	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	for tick, want := range map[string][2]string{
		"a1": {"codex", "gpt-5.6-luna"},
		"rv": {"pi", "pi-large"},
	} {
		dispatch := f.dispatch(tick)
		if dispatch.Profile == nil {
			t.Fatalf("%s was dispatched under no profile", tick)
		}
		if dispatch.Profile.Runner != want[0] || dispatch.Profile.Model != want[1] {
			t.Errorf("%s was dispatched as %s/%s, want %s/%s",
				tick, dispatch.Profile.Runner, dispatch.Profile.Model, want[0], want[1])
		}
		if !strings.Contains(dispatch.Profile.Routed, "roles.") {
			t.Errorf("%s does not record where its routing came from: %q", tick, dispatch.Profile.Routed)
		}
	}

	// A role the file does not route keeps the profile this repository ships,
	// and says so.
	if closeout := f.dispatch("co"); closeout.Profile == nil || closeout.Profile.Routed != "" {
		t.Errorf("the closeout claims routing from %q", closeout.Profile.Routed)
	}
}

// A profile this build cannot honour is refused at CONSTRUCTION. Three ticks
// into an epic, with a tick already claimed, is not when a run should discover
// that nothing can launch its runner.
func TestAProfileThisBuildCannotHonourIsRefusedAtConstruction(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "implement-tick", `"executor": "herdr", "runner": "claude", "model": "sonnet"`)
	writeProfile(t, dir, "review-epic", `"executor": "local-subprocess", "runner": "claude", "model": "opus"`)
	writeProfile(t, dir, "closeout-epic", `"executor": "local-subprocess", "runner": "claude", "model": "sonnet"`)

	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.ProfileDir = dir
	if _, err := New(opts); err == nil {
		t.Fatal("a profile naming an executor this phase does not have was accepted")
	} else if !strings.Contains(err.Error(), "herdr") {
		t.Errorf("the refusal does not name what it refused: %v", err)
	}

	writeProfile(t, dir, "implement-tick", `"executor": "local-subprocess", "runner": "gemini", "model": "sonnet"`)
	if _, err := New(opts); err == nil {
		t.Fatal("a profile naming a runner this host cannot launch was accepted")
	} else if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("the refusal does not name the runner: %v", err)
	}
}

func writeProfile(t *testing.T, dir, role, fields string) {
	t.Helper()
	write(t, dir+"/"+role+".json", `{"schema_version": 1, "role": "`+role+`", "version": "1.0.0", `+
		fields+`, "prompt": "`+role+`.md"}`)
	write(t, dir+"/"+role+".md", "the "+role+" prompt")
}

func openStore(t *testing.T, f *fixture, r *Reconciler) *runstate.Store {
	t.Helper()
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.IntegrationBranch(), RunID: r.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	return store
}

// ------------------------------------------------------------ role jobs ---

// SPEC §6.3: the review runs at the epic boundary, READ-ONLY, against what the
// controller integrated — not against the state the run started from.
func TestTheReviewRunsReadOnlyAtTheControllerState(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	spec := f.spec("rv")
	if spec == nil {
		t.Fatal("the review was never dispatched")
	}
	if grade := spec.Credentials.Source.Grade(); grade != "read-only" {
		t.Errorf("the review was issued source access at grade %q", grade)
	}
	if prefix := spec.Credentials.Source.WriteRefPrefix(); prefix != "" {
		t.Errorf("a read-only grade bounded a write namespace %q; there is no write to bound", prefix)
	}
	if spec.OutputSchema != "ticfac.job-result.review-epic.v1" {
		t.Errorf("the review was asked for %s", spec.OutputSchema)
	}

	// The state it was dispatched at is the integration branch as origin had
	// it — which is not the base the run was cut from, because three ticks
	// merged onto it first.
	if spec.Source.BaseSHA == f.Repo.Base {
		t.Errorf("the review ran at %s, the state the run started from", short(spec.Source.BaseSHA))
	}
	head := remoteHeadOf(t, f, r.IntegrationBranch())
	if !containsCommit(t, f, spec.Source.BaseSHA, head) {
		t.Errorf("the review ran at %s, which %s does not contain", short(spec.Source.BaseSHA), r.IntegrationBranch())
	}

	// And an implementation tick is NOT dispatched there: it branches from the
	// run's base like any other.
	if implement := f.spec("a1"); implement == nil || implement.Source.BaseSHA != f.Repo.Base {
		t.Errorf("a1 was dispatched at %s, not at the run's base", short(implement.Source.BaseSHA))
	}
	if implement := f.spec("a1"); implement.Credentials.Source.Grade() != "write" {
		t.Errorf("an implementation tick was issued grade %q", implement.Credentials.Source.Grade())
	}
}

// The validated answer LANDS, as the run state's decision record: a decision is
// a thing a model was paid for once, so a restart re-reads it instead of
// re-asking.
func TestARoleJobsValidatedAnswerIsRecordedAsADecision(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, _, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}

	decisions, err := openStore(t, f, r).Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("%d decisions for a review and a closeout: %+v", len(decisions), decisions)
	}
	byRole := map[string]runstate.Decision{}
	for _, decision := range decisions {
		byRole[decision.Role] = decision
	}
	for _, role := range []string{"review-epic", "closeout-epic"} {
		decision, ok := byRole[role]
		if !ok {
			t.Fatalf("no decision for %s", role)
		}
		if !decision.Validated {
			t.Errorf("the %s decision is unvalidated: an unvalidated response landing as a decision is how a "+
				"hallucinated verdict becomes authority", role)
		}
		if decision.Request["output_schema"] != "ticfac.job-result."+role+".v1" {
			t.Errorf("the %s decision does not record what was asked for: %v", role, decision.Request)
		}
		if decision.Response["role"] != role || decision.Response["schema_version"] != float64(subprocess.SchemaVersion) {
			t.Errorf("the %s decision's response is not the envelope: %v", role, decision.Response)
		}
		if decision.Provenance.ProfileDigest == nil || *decision.Provenance.ProfileDigest == "" {
			t.Errorf("the %s decision does not say which profile answered it", role)
		}
	}
	if byRole["review-epic"].Request["source_grade"] != "read-only" {
		t.Errorf("the review's decision records grade %v", byRole["review-epic"].Request["source_grade"])
	}
}

// FAIL CLOSED. A role-result envelope the reconciler cannot validate is not
// acted on: nothing is noted, nothing is closed, and the process tick is left
// open for whoever looks next.
func TestAMalformedRoleResultLeavesTheTickOpen(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.wrap = corruptRoleResult(func(result *subprocess.RoleResult) {
		// A status of the model's own invention: the verdict-inversion bug the
		// closed vocabulary exists to stop.
		result.Status = "COMPLETE"
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedRoleResult {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedRoleResult)
	}
	if !strings.Contains(result.Failure.Message, "COMPLETE") {
		t.Errorf("the refusal does not say what was wrong with the answer: %s", result.Failure.Message)
	}

	current, err := f.Tracker.Show(context.Background(), "rv")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("the tick was closed behind an answer nobody could validate")
	}
	if got := f.Tracker.count("close:rv"); got != 0 {
		t.Errorf("the tracker was asked to close rv %d times", got)
	}
	if got := f.Tracker.count("note:rv"); got != 0 {
		t.Errorf("an unvalidated answer was noted on rv %d times", got)
	}
	// And the closeout that came after it never ran: one epic at concurrency
	// one stops rather than closing out over an unanswered review.
	if f.spec("co") != nil {
		t.Error("the closeout was dispatched after the review's answer was refused")
	}
}

// An envelope that VALIDATES and asks for a person is a different failure, and
// it reads differently: a role job's answer is its verdict, and this one says a
// person is needed rather than that the answer was unreadable.
func TestARoleAnswerThatAsksForAPersonLeavesTheTickOpen(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.wrap = corruptRoleResult(func(result *subprocess.RoleResult) {
		result.Status = subprocess.StatusBlocked
		result.Summary = "the epic's second wave was never integrated"
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedRoleAnswer {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedRoleAnswer)
	}
	if !strings.Contains(result.Failure.Message, "the epic's second wave was never integrated") {
		t.Errorf("the refusal does not carry what the answer said: %s", result.Failure.Message)
	}
	current, err := f.Tracker.Show(context.Background(), "rv")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("a tick whose role job asked for a person was closed anyway")
	}
}

// corruptRoleResult decorates the executor so that the envelope the reconciler
// sees is not the one the executor built. It is how a malformed answer is
// produced without touching the executor package — and it stands in for the
// thing that actually produces one: a model.
func corruptRoleResult(apply func(*subprocess.RoleResult)) func(Executor) Executor {
	return func(inner Executor) Executor { return &corrupting{Executor: inner, apply: apply} }
}

type corrupting struct {
	Executor
	apply func(*subprocess.RoleResult)
}

func (c *corrupting) CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error) {
	collected, err := c.Executor.CollectDetail(handle)
	if err != nil || collected.Result == nil || collected.Result.RoleResult == nil {
		return collected, err
	}
	if collected.Result.RoleResult.Role == "review-epic" {
		c.apply(collected.Result.RoleResult)
	}
	return collected, nil
}

// remoteHeadOf and containsCommit read git rather than the reconciler's memory:
// the question is what ORIGIN has, which is the only authority a restarted run
// would read.
func remoteHeadOf(t *testing.T, f *fixture, branch string) string {
	t.Helper()
	out := mustRun(t, f.Repo.Dir, "git", "ls-remote", "origin", refFor(branch))
	sha, _, ok := strings.Cut(strings.TrimSpace(out), "\t")
	if !ok {
		t.Fatalf("origin has no %s", branch)
	}
	return sha
}

func containsCommit(t *testing.T, f *fixture, commit, container string) bool {
	t.Helper()
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin")
	cmd := mustRunAllowingFailure(f.Repo.Dir, "git", "merge-base", "--is-ancestor", commit, container)
	return cmd
}
