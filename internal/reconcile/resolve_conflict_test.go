package reconcile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The merge conflict a run absorbs instead of stopping for a person
// (ticfac tick 2p6, epic gvc).
//
// cr4 and 7eq — two ticks of one wave — both rewrote one function, and the
// second to merge stopped epic-yoh for a person. These tests drive the same
// shape through the reconciler's OWN machinery: a real content conflict, in
// a real git repository, produced by two same-wave workers that really
// edited one file — and the run's answer, the resolve-conflict job, whose
// merge is integrated and gated like any other.
//
// short: each test is one full fixture run; skipped under -short with the
// rest of the end-to-end suite.

// resolveGate declares the world the acceptance drives: a wave two ticks
// wide, a tier policy whose ceiling is frontier, and the review cell's
// frontier overlay — the routing the resolve-conflict job resolves through,
// exactly as .tick/runners.toml declares it for real runs.
const resolveGate = `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.strong]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.frontier]
kind = "claude"
model = "sonnet"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.tiers.frontier]
kind = "claude"
model = "opus"

[tier_policy]
default = "strong"
ceiling = "frontier"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// conflictSync points the fake runner's two conflicting workers at one sync
// directory (testdata/fake-runner.sh): both same-wave ticks edit
// shared-work.txt, and neither settles before the other started.
func conflictSync(t *testing.T) {
	t.Helper()
	t.Setenv("CONFLICT_SYNC", t.TempDir())
	t.Setenv("CONFLICT_TICKS", "a1 a2")
}

// seedSharedFile puts the file both sides edit on the base commit, so the
// collision is a CONTENT conflict — both sides edit a file that exists — not
// an add/add one.
func seedSharedFile(t *testing.T, f *fixture) {
	t.Helper()
	write(t, filepath.Join(f.Repo.Dir, "shared-work.txt"), "the base of the shared file\n")
	mustRun(t, f.Repo.Dir, "git", "add", "-A")
	mustRun(t, f.Repo.Dir, "git", "commit", "--quiet", "-m", "the shared file at base")
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// resolveSpecOf is the JobSpec the run dispatched its resolve-conflict job
// with, and the tick whose conflict it is.
func resolveSpecOf(t *testing.T, f *fixture) (string, *subprocess.JobSpec, Dispatch) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for tick, spec := range f.specs {
		if spec != nil && spec.Role == RoleResolveConflict {
			return tick, spec, f.dispatches[tick]
		}
	}
	return "", nil, Dispatch{}
}

// THE ACCEPTANCE DRIVER. A run whose attempt conflicts with the epic branch
// dispatches a resolve-conflict job, integrates its merge, gates it and
// continues without a person.
//
// serial: this test states the process environment (CONFLICT_SYNC,
// CONFLICT_TICKS) for the fake runner's workers to synchronise through, and
// t.Setenv forbids a parallel test.
func TestAContentConflictIsDrivenThroughAResolveJobToIntegration(t *testing.T) {
	conflictSync(t)
	f := newFixture(t, fixtureOptions{mode: "conflict", gate: resolveGate})
	seedSharedFile(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "conflict", gate: resolveGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a conflict the resolve job answered is not a stop",
			result.State, result.Reason)
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s: the conflict it met was absorbed, not left for a person", tick, current.Status)
		}
	}

	// The job: dispatched as resolve-conflict, at the policy's ceiling tier,
	// with BOTH ticks' records in its inputs — the two descriptions are the
	// two intents, and the prompt that does not carry both is a guess.
	tick, spec, dispatch := resolveSpecOf(t, f)
	if spec == nil {
		t.Fatal("no resolve-conflict job was dispatched for a real content conflict")
	}
	if tick != "a2" {
		t.Errorf("the resolve-conflict job was dispatched for %s, not the tick whose merge conflicted", tick)
	}
	if dispatch.Role != RoleResolveConflict {
		t.Errorf("the job was dispatched as role %s", dispatch.Role)
	}
	if dispatch.Tier != "frontier" {
		t.Errorf("the resolve-conflict job was routed at tier %q, not the policy's ceiling \"frontier\"",
			dispatch.Tier)
	}
	if dispatch.Profile == nil || dispatch.Profile.Model != "opus" {
		t.Errorf("the resolve-conflict job did not run on the ceiling's model: %+v", dispatch.Profile)
	}
	ticks := map[string]bool{}
	for _, in := range spec.Inputs {
		if in.Kind == "tick" {
			ticks[in.ID] = true
		}
	}
	if !ticks["a1"] || !ticks["a2"] {
		t.Errorf("the resolve-conflict job's inputs do not name both conflicting ticks: %v", ticks)
	}

	// The merge: a real two-parent merge commit on the integration branch,
	// whose tree is the union the job resolved to — integrated and gated
	// like any other merge, with the tick closed behind it.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
	epic := runGitQuiet(clone.Dir, "rev-parse", "--verify", "--quiet", "origin/epic/qeu")
	if epic == "" {
		t.Fatal("the run left no integration branch")
	}
	shared := readGitBlob(t, clone.Dir, "origin/epic/qeu", "shared-work.txt")
	if shared != "resolved by the resolve-conflict job" {
		t.Errorf("the shared file on the integration branch is %q, not the job's resolution", shared)
	}
	merges := runGitQuiet(clone.Dir, "log", "--merges", "--format=%s", "origin/epic/qeu")
	if !strings.Contains(merges, "resolve-conflict job's resolution") {
		t.Errorf("the integration branch carries no merge commit minted from the job's resolution:\n%s", merges)
	}
	if !strings.Contains(merges, "Merge branch 'ticfac/run-r-fixture/tick-a1") {
		t.Errorf("the integration branch does not carry a1's own merge either:\n%s", merges)
	}
	// Both sides of the union are still there: the work files each tick
	// committed, beside the resolved shared one.
	for _, tick := range []string{"a1", "a2"} {
		if got := readGitBlob(t, clone.Dir, "origin/epic/qeu", "work-"+tick+".txt"); !strings.Contains(got, tick) {
			t.Errorf("work-%s.txt did not survive the resolved merge: %q", tick, got)
		}
	}

	// The decision: the resolve is a durable record on the run branch, so a
	// cold re-derivation reads it instead of guessing.
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range decisions {
		if d.Role == RoleResolveConflict {
			found = true
			if id, _ := d.Request["tick_id"].(string); id != "a2" {
				t.Errorf("the recorded resolve names tick %s", id)
			}
			if status, _ := d.Response["status"].(string); status != "merged" {
				t.Errorf("the recorded resolve says %q", status)
			}
		}
	}
	if !found {
		t.Error("the resolved merge was never recorded as a decision on the run branch")
	}
}

// A resolve job that fails is still a stop — and the stop names the FILES, as
// ky5 made every merge stop name them, plus where the half-resolved work is.
//
// serial: this test states the process environment (CONFLICT_SYNC,
// CONFLICT_TICKS) for the fake runner's workers to synchronise through, and
// t.Setenv forbids a parallel test.
func TestAFailedResolveJobIsAStopNamingTheFiles(t *testing.T) {
	conflictSync(t)
	f := newFixture(t, fixtureOptions{mode: "conflict_unresolvable", gate: resolveGate})
	seedSharedFile(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "conflict_unresolvable", gate: resolveGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: a resolve job that asks for a person is a stop", result.State)
	}
	if r.failure == nil || r.failure.Message == "" {
		t.Fatal("the failed resolve stopped the run with no refusal to read")
	}
	for _, want := range []string{"shared-work.txt", "resolve-conflict", "does not merge onto"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, r.failure.Message)
		}
	}
	// The conflicting tick is NOT closed behind a resolve that did not
	// resolve; its own work is kept, exactly as every refused attempt's is.
	current, err := f.Tracker.Show(context.Background(), "a2")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("a2 was closed behind a resolve-conflict job that never resolved anything")
	}
	// And the failure is a durable record, so a later incarnation that meets
	// the same conflict stops naming the recorded resolve instead of paying
	// for a second one.
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range decisions {
		if d.Role == RoleResolveConflict {
			found = true
			if status, _ := d.Response["status"].(string); status != "failed" {
				t.Errorf("the recorded resolve says %q, not \"failed\"", status)
			}
		}
	}
	if !found {
		t.Error("the failed resolve was never recorded as a decision on the run branch")
	}
}

// A SECOND conflict on the same tick is the stop, as today — the recorded
// resolve is the durable answer to "what already ran here", and the refusal
// names the files and where that resolve's work is.
//
// serial: this test states the process environment (CONFLICT_SYNC,
// CONFLICT_TICKS) for the fake runner's workers to synchronise through, and
// t.Setenv forbids a parallel test.
func TestASecondConflictOnTheSameTickIsTheStop(t *testing.T) {
	conflictSync(t)
	f := newFixture(t, fixtureOptions{mode: "conflict", gate: resolveGate})
	seedSharedFile(t, f)
	// A resolve-conflict decision for a2, recorded on the run branch the way
	// a previous incarnation's resolve would have left it: the integration
	// branch is created the way the run's own start creates it, and the
	// record lands through the same store a run writes through.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	st, err := runstate.Open(runstate.Options{Repo: f.Repo.Dir, Remote: "origin", Branch: "epic/qeu", RunID: "r-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutDecision(runstate.Decision{
		Decision: 1, Role: RoleResolveConflict,
		Request:   map[string]any{"tick_id": "a2", "epic_id": "qeu", "role": RoleResolveConflict, "resolve_branch": "ticfac/run-r-fixture/tick-a2/resolve-2"},
		Response:  map[string]any{"status": "failed", "conflict_files": []string{"shared-work.txt"}},
		Validated: true, RequestedAt: "2026-09-24T06:00:00Z", AnsweredAt: "2026-09-24T06:00:00Z",
		Provenance: ProvenanceForTest("a2"),
	}); err != nil {
		t.Fatal(err)
	}

	opts := f.options(f.Repo, fixtureOptions{mode: "conflict", gate: resolveGate})
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: a second conflict on the same tick is a stop", result.State)
	}
	if r.failure == nil {
		t.Fatal("the second conflict stopped the run with no refusal to read")
	}
	for _, want := range []string{"shared-work.txt", "resolve-conflict job already ran", "tick-a2/resolve-2"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, r.failure.Message)
		}
	}
	// And no second job was dispatched: the recorded resolve is the answer.
	f.mu.Lock()
	started := 0
	for _, spec := range f.specs {
		if spec != nil && spec.Role == RoleResolveConflict {
			started++
		}
	}
	f.mu.Unlock()
	if started != 0 {
		t.Errorf("a second resolve-conflict job was dispatched over the recorded one (%d)", started)
	}
}

// readGitBlob reads one file's content as a ref holds it.
func readGitBlob(t *testing.T, dir, ref, path string) string {
	t.Helper()
	return runGitQuiet(dir, "cat-file", "blob", ref+":"+path)
}
