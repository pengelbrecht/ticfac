package reconcile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The gate failure a run absorbs instead of stopping for a person
// (ticfac tick wj6, epic gvc).
//
// l6t deleted the wave path while the lifecycle contract still pointed at
// the deleted symbols; mn7 deleted the TypeScript reconciler while a test
// harness still re-exported the deleted class. Both merges landed, both
// gates failed, and both times the run stopped for a person whose whole
// job was to read the gate's own output and make a small mechanical fix.
// These tests drive the same shape through the reconciler's OWN machinery:
// a real deletion, of a file the gate's own check references, whose merge
// lands and fails a real gate — and the run's answer, the plan-repair job,
// whose merge is merged and gated as usual.
//
// short: each test is one full fixture run; skipped under -short with the
// rest of the end-to-end suite.

// repairGate declares the world the acceptance drives: a tier policy whose
// ceiling is frontier, and the review cell's frontier overlay — the routing
// the repair job resolves through, exactly as .tick/runners.toml declares it
// for real runs. The check is a REAL harness: a script in the tree that
// reads a file a tick is about to delete.
const repairGate = `version = 2

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
tree = { command = "sh check.sh", description = "the harness's references resolve" }
`

// seedStaleReference puts the wj6 shape at the base: a committed harness
// (check.sh) that reads a committed file (stale-ref.txt) no tick declares —
// the dependent that lives OUTSIDE the files the deletion tick touched. The
// tick that deletes stale-ref.txt leaves the harness's reference stale, and
// that is a gate failure the gate's own output names.
func seedStaleReference(t *testing.T, f *fixture) {
	t.Helper()
	write(t, filepath.Join(f.Repo.Dir, "stale-ref.txt"), "the file the harness still references\n")
	write(t, filepath.Join(f.Repo.Dir, "check.sh"), "cat stale-ref.txt >/dev/null\n")
	mustRun(t, f.Repo.Dir, "git", "add", "-A")
	mustRun(t, f.Repo.Dir, "git", "commit", "--quiet", "-m", "the harness and the file it references at base")
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// repairSpecOf is the JobSpec and Dispatch the run dispatched its repair job
// with, and the tick whose gate failed.
func repairSpecOf(t *testing.T, f *fixture) (string, *subprocess.JobSpec, Dispatch) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for tick, spec := range f.specs {
		if spec != nil && spec.Role == RoleRepairGate {
			return tick, spec, f.dispatches[tick]
		}
	}
	return "", nil, Dispatch{}
}

// repairDispatchCount is how many repair jobs the run started — the number
// "one repair per failed gate" is.
func repairDispatchCount(t *testing.T, f *fixture) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, spec := range f.specs {
		if spec != nil && spec.Role == RoleRepairGate {
			n++
		}
	}
	return n
}

// gateEvidenceOf reads one gate evidence record off the run branch.
func gateEvidenceOf(t *testing.T, f *fixture, key string) *runstate.Evidence {
	t.Helper()
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	record, ok, err := store.Evidence(key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("no gate evidence record under %s", key)
	}
	return record
}

// THE ACCEPTANCE DRIVER. A run whose integrated gate fails after an attempt
// merged dispatches one repair job with the gate output and the tick's
// record in its prompt, merges and re-gates its result, and continues
// without a person when the repair passes — a real gate failure, a stale
// reference a deletion left, driven through the repair to a CLOSED tick.
func TestAGateFailureAfterAMergeIsDrivenThroughARepairJobToAClosedTick(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "gate_break_repair", gate: repairGate})
	seedStaleReference(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "gate_break_repair", gate: repairGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a gate failure the repair job fixed is not a stop",
			result.State, result.Reason)
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s: the gate failure it met was repaired, not left for a person", tick, current.Status)
		}
	}
	if got := r.Stages("a1"); !contains(got, StageGateFailed) {
		t.Errorf("a1's stages %v record no gate failure: the fixture did not produce one", got)
	}
	if got := r.Stages("a1"); !contains(got, StageClosed) {
		t.Errorf("a1's stages %v record no close: the repair's gate never closed it", got)
	}

	// The gate failure is REAL: the recorded evidence of the failing check
	// carries the check's own output, naming the deleted file — the output a
	// person used to read by hand, and the thing the repair job is handed.
	evidence := gateEvidenceOf(t, f, "gate-a1-1-tree")
	if evidence.Result != "fail" {
		t.Errorf("the recorded evidence of the failing check says %q, not \"fail\"", evidence.Result)
	}
	if evidence.Output.Inline == nil || !strings.Contains(evidence.Output.Inline.Stderr, "stale-ref.txt") {
		t.Errorf("the failing check's output does not name the deleted file: %+v", evidence.Output.Inline)
	}

	// The job: dispatched as plan-repair, at the policy's ceiling tier, with
	// the failing check's EVIDENCE RECORD and the TICK's record in its
	// inputs — the gate's own output and the tick's description are what its
	// prompt carries — and its role prompt, which tells the worker where the
	// evidence lives and where the tick's diff is.
	tick, spec, dispatch := repairSpecOf(t, f)
	if spec == nil {
		t.Fatal("no repair job was dispatched for a real gate failure")
	}
	if tick != "a1" {
		t.Errorf("the repair job was dispatched for %s, not the tick whose merge failed the gate", tick)
	}
	if dispatch.Role != RoleRepairGate {
		t.Errorf("the job was dispatched as role %s", dispatch.Role)
	}
	if dispatch.Tier != "frontier" {
		t.Errorf("the repair job was routed at tier %q, not the policy's ceiling \"frontier\"", dispatch.Tier)
	}
	if dispatch.Profile == nil || dispatch.Profile.Model != "opus" {
		t.Errorf("the repair job did not run on the ceiling's model: %+v", dispatch.Profile)
	}
	inputs := map[string]string{}
	for _, in := range spec.Inputs {
		inputs[in.Kind] = in.ID
	}
	if inputs["tick"] != "a1" {
		t.Errorf("the repair job's inputs name tick %q, not the tick whose gate failed", inputs["tick"])
	}
	if inputs["evidence"] != "gate-a1-1-tree" {
		t.Errorf("the repair job's inputs name no evidence record of the failing check: %v", inputs)
	}
	if dispatch.Profile == nil || !strings.Contains(dispatch.Profile.Prompt, "evidence/<id>.json") {
		t.Errorf("the repair job's role prompt does not point the worker at the failing check's evidence record")
	}
	if dispatch.Profile == nil || !strings.Contains(dispatch.Profile.Prompt, "git log") {
		t.Errorf("the repair job's role prompt does not tell the worker where the tick's diff is")
	}
	if dispatch.WriteRef != "refs/heads/ticfac/run-r-fixture/tick-a1/repair-1" {
		t.Errorf("the repair job writes %q, not its own ref under the run's namespace", dispatch.WriteRef)
	}
	if n := repairDispatchCount(t, f); n != 1 {
		t.Errorf("the run dispatched %d repair jobs; one repair per failed gate is all it gets", n)
	}

	// The merge: the repair's fix is on the integration branch as an ordinary
	// merge, the deletion stands, and the gate passed over the repaired
	// tree — which is what closed the tick.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
	epic := runGitQuiet(clone.Dir, "rev-parse", "--verify", "--quiet", "origin/epic/qeu")
	if epic == "" {
		t.Fatal("the run left no integration branch")
	}
	if got := strings.TrimSpace(readGitBlob(t, clone.Dir, "origin/epic/qeu", "check.sh")); got != "cat README.md >/dev/null" {
		t.Errorf("the harness on the integration branch is %q, not the repair's fix", got)
	}
	if mustRunAllowingFailure(clone.Dir, "git", "cat-file", "blob", "origin/epic/qeu:stale-ref.txt") {
		t.Error("the deleted file is back on the integration branch: the repair undid the tick instead of fixing the tree")
	}
	if got := readGitBlob(t, clone.Dir, "origin/epic/qeu", "work-a1.txt"); !strings.Contains(got, "a1") {
		t.Errorf("work-a1.txt did not survive the repair's merge: %q", got)
	}
	merges := runGitQuiet(clone.Dir, "log", "--merges", "--format=%s", "origin/epic/qeu")
	if !strings.Contains(merges, "tick-a1/repair-1") {
		t.Errorf("the integration branch carries no merge of the repair job's branch:\n%s", merges)
	}

	// The decision: the repair is a durable record on the run branch, so a
	// cold re-derivation reads it instead of paying for a second repair.
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range decisions {
		if d.Role == RoleRepairGate {
			found = true
			if id, _ := d.Request["tick_id"].(string); id != "a1" {
				t.Errorf("the recorded repair names tick %s", id)
			}
			if status, _ := d.Response["status"].(string); status != "merged" {
				t.Errorf("the recorded repair says %q", status)
			}
			if failures, _ := d.Response["gate_failures"].(string); !strings.Contains(failures, "tree") {
				t.Errorf("the recorded repair does not name the gate failure it was dispatched over: %q", failures)
			}
		}
	}
	if !found {
		t.Error("the repair was never recorded as a decision on the run branch")
	}
}

// A repair job that fails is the gate stop it replaced — and the stop names
// BOTH: the gate failure the repair was dispatched over, and the repair's
// own failure. The tick is NOT closed behind a repair that did not repair.
func TestAFailedRepairJobIsAStopNamingTheGateAndTheRepair(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "gate_break_unresolvable", gate: repairGate})
	seedStaleReference(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "gate_break_unresolvable", gate: repairGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: a repair job that asks for a person is a stop", result.State)
	}
	if r.failure == nil || r.failure.Message == "" {
		t.Fatal("the failed repair stopped the run with no refusal to read")
	}
	for _, want := range []string{"tree (fail)", "repair job failed", "BLOCKED"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, r.failure.Message)
		}
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("a1 was closed behind a repair job that never repaired anything")
	}
	// The failure is durable: a later incarnation that meets the same gate
	// failure stops naming the recorded repair instead of paying again.
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range decisions {
		if d.Role == RoleRepairGate {
			found = true
			if status, _ := d.Response["status"].(string); status != "failed" {
				t.Errorf("the recorded repair says %q, not \"failed\"", status)
			}
		}
	}
	if !found {
		t.Error("the failed repair was never recorded as a decision on the run branch")
	}
}

// A repair whose gate ALSO fails is the stop, as today — naming BOTH gate
// failures, the one the repair was dispatched over and the one the repaired
// tree produced. Its work is merged and gated as usual on the way in, which
// is exactly why the second failure can be named.
func TestARepairWhoseGateAlsoFailsNamesBothFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "gate_break_wrong_repair", gate: repairGate})
	seedStaleReference(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "gate_break_wrong_repair", gate: repairGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: a repair whose gate also fails is the stop", result.State)
	}
	if r.failure == nil {
		t.Fatal("the failed repair stopped the run with no refusal to read")
	}
	for _, want := range []string{"twice", "tree (fail)", "One repair per tick"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, r.failure.Message)
		}
	}
	// The repair's work WAS merged and gated as usual — that is what makes
	// the second failure the repaired tree's own.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
	if got := readGitBlob(t, clone.Dir, "origin/epic/qeu", "repair-note.txt"); !strings.Contains(got, "did not fix the gate") {
		t.Errorf("the repair's merge is not on the integration branch: %q", got)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("a1 was closed behind two failing gates")
	}
	if n := repairDispatchCount(t, f); n != 1 {
		t.Errorf("the run dispatched %d repair jobs over two gate failures; one is the bound", n)
	}
}

// ONE REPAIR PER TICK, durably: a gate failure for a tick whose repair
// decision is already recorded stops naming the recorded repair and dispatches
// nothing — the same shape as a second conflict on the same tick (2p6).
func TestAGateFailureOverAnAlreadyRepairedTickDispatchesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "gate_break_repair", gate: repairGate})
	seedStaleReference(t, f)
	// A repair decision for a1, recorded on the run branch the way a previous
	// incarnation's repair would have left it — merged, over a gate failure
	// of the same check this fixture produces.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	st, err := runstate.Open(runstate.Options{Repo: f.Repo.Dir, Remote: "origin", Branch: "epic/qeu", RunID: "r-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutDecision(runstate.Decision{
		Decision: 1, Role: RoleRepairGate,
		Request:   map[string]any{"tick_id": "a1", "epic_id": "qeu", "role": RoleRepairGate, "repair_branch": "ticfac/run-r-fixture/tick-a1/repair-1"},
		Response:  map[string]any{"status": "merged", "gate_failures": "tree (fail)"},
		Validated: true, RequestedAt: "2026-09-24T06:00:00Z", AnsweredAt: "2026-09-24T06:00:00Z",
		Provenance: ProvenanceForTest("a1"),
	}); err != nil {
		t.Fatal(err)
	}

	opts := f.options(f.Repo, fixtureOptions{mode: "gate_break_repair", gate: repairGate})
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: a gate failure over an already-repaired tick is a stop", result.State)
	}
	if r.failure == nil {
		t.Fatal("the gate failure stopped the run with no refusal to read")
	}
	for _, want := range []string{"twice", "tree (fail)", "tick-a1/repair-1"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, r.failure.Message)
		}
	}
	if n := repairDispatchCount(t, f); n != 0 {
		t.Errorf("a second repair job was dispatched over the recorded one (%d)", n)
	}
}
