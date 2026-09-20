package herdr

// p6b: the artifact boundary, enforced by this executor in two layers,
// neither of which rests on the worker's prompt, its cooperation, or the
// kind of agent running.
//
//   - PREVENT. Start writes a git exclude for the job's artifact prefix into
//     the worktree BEFORE the agent is launched, so an ordinary `git add -A`
//     cannot stage the report — the exact incident this tick exists for: two
//     of four wave-2 workers committed their RESULT file onto the attempt
//     branch because nothing but the prompt stood in the way.
//   - DETECT, because prevention is bypassable. Collect re-checks the
//     boundary against the branch diff: a worker that force-adds past the
//     exclude is refused and REPORTED (an observation, and the violation
//     carried in the collected record), never silently dropped.
//
// The fixtures are the fake agent's `report_then_addall` and `force_report`
// modes — the same two shapes the local subprocess executor's fake runner
// carries — driven here by a real agent process in a real herdr worktree.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// artifactPrefix reads the prefix the ATTEMPT RECORD's spec carried — the
// substrate's own grant of artifact space, which is where the exclude comes
// from, never the prompt.
func (h *harness) artifactPrefix(t *testing.T, handle *subprocess.JobHandle) string {
	t.Helper()
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	record, err := newStore(local.State).readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	return record.Spec.ArtifactPrefix
}

// excludeFile resolves the exclude file the way the executor resolves it:
// from the attempt worktree while it exists.
func (h *harness) excludeFile(t *testing.T, handle *subprocess.JobHandle) string {
	t.Helper()
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(mustRun(t, local.Worktree,
		"git", "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
}

func excludeLine(prefix string) string {
	return "/" + strings.Trim(strings.TrimSpace(prefix), "/")
}

// Layer one, written where the criterion says: the exclude is in the
// worktree's git before the agent is ever launched. The harness here spawns
// no agent at all, and the kind is a non-default one — the boundary cannot
// be a claude-only or prompt-following assumption, and this test says so by
// construction.
func TestStartExcludesTheArtifactPrefixBeforeTheAgentRuns(t *testing.T) {
	h := newHarness(t, harnessOptions{kind: "pi"})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	prefix := h.artifactPrefix(t, handle)
	if prefix == "" {
		t.Fatal("the spec carried no artifact prefix: nothing to exclude")
	}

	// No agent was started, and the line is already there.
	raw, err := os.ReadFile(h.excludeFile(t, handle))
	if err != nil {
		t.Fatalf("the exclude file does not exist: %v", err)
	}
	if !strings.Contains(string(raw), excludeLine(prefix)+"\n") {
		t.Fatalf("the exclude file does not carry the line %q:\n%s", excludeLine(prefix), raw)
	}
	// The operational proof, the way git itself answers it: the report
	// path is ignored in this worktree. check-ignore exits non-zero when a
	// path is NOT ignored, so mustRun's success is the assertion.
	mustRun(t, local.Worktree, "git", "check-ignore", "-q", local.ResultRel)
}

// The prevention fixture: a real agent process writes its report FIRST and
// then runs an ordinary `git add -A` over the worktree — the shape two of
// four wave-2 workers produced. The exclude Start wrote must keep the report
// off the branch whatever the prompt said, and collect must still read it
// from the worktree exactly as it would any other report.
func TestAnOrdinaryAddAllCannotStageTheReport(t *testing.T) {
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "report_then_addall", kind: "codex"})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForOr(t, "the agent to finish its turn", 30*time.Second, h.agentDone) {
		h.dumpAgent(t)
		t.Fatal("the fake agent never settled")
	}

	changed := mustRun(t, h.repo.Dir, "git", "diff", "--name-only", "--no-renames",
		local.BaseSHA, "refs/heads/"+local.Branch)
	if strings.Contains(changed, local.ResultRel) {
		t.Fatalf("the report %s rode into the branch despite the ordinary git add -A:\n%s",
			local.ResultRel, changed)
	}
	// The add-all's own work is still there — only the report was excluded.
	if !strings.Contains(changed, "addall.txt") {
		t.Fatalf("the add-all's own commit did not land:\n%s", changed)
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Fatalf("verdict %s (%s), want ready-to-merge: an excluded report is not a violation",
			collected.Verdict, collected.Message)
	}
	if len(collected.ArtifactViolations) != 0 {
		t.Fatalf("no path should have reached the branch, got violations %v", collected.ArtifactViolations)
	}
	if !collected.HasReport || collected.Report.Status != subprocess.StatusDone {
		t.Fatalf("collect did not read the report from the worktree: hasReport=%t report=%+v",
			collected.HasReport, collected.Report)
	}
}

// The detection fixture: a worker that bypasses the exclude outright
// (`git add -f`) must be caught at collect off the branch diff — refused
// with the boundary-violation verdict, and REPORTED: an observation, and the
// violation carried in the collected record rather than dropped.
func TestAForceAddedReportIsCaughtAtCollect(t *testing.T) {
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "force_report"})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForOr(t, "the agent to finish its turn", 30*time.Second, h.agentDone) {
		h.dumpAgent(t)
		t.Fatal("the fake agent never settled")
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictBoundaryViolation {
		t.Fatalf("verdict %s (%s), want %s: a force-added report is the shape the backstop exists for",
			collected.Verdict, collected.Message, subprocess.VerdictBoundaryViolation)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("a force-committed artifact collected as %s", collected.Result.Outcome)
	}
	if len(collected.ArtifactViolations) != 1 || collected.ArtifactViolations[0] != local.ResultRel {
		t.Fatalf("artifact violations = %v, want exactly the forced report %s",
			collected.ArtifactViolations, local.ResultRel)
	}
	// Kept separate from the tracker-record boundary: the two are different
	// problems even though both refuse the same way.
	if len(collected.BoundaryViolations) != 0 {
		t.Errorf("the artifact violation was also counted as a tracker write: %v", collected.BoundaryViolations)
	}
	// REPORTED, not silently refused: the attempt is in the observation log…
	observations, _ := h.ex.storeAt(local.State).observationsFrom("")
	reported := false
	for _, obs := range observations {
		if strings.Contains(obs.Detail, "committed its own report or artifact at "+local.ResultRel) {
			reported = true
		}
	}
	if !reported {
		t.Errorf("no observation reports the bypassed boundary: %+v", observations)
	}
	// …and carried in the collected record, whatever else the attempt did.
	if collected.Result.RoleResult == nil {
		t.Fatal("the worker reported, so the result must carry its report")
	}
	if got := collected.Result.RoleResult.Result["verdict"]; got != subprocess.VerdictBoundaryViolation {
		t.Errorf("the role result reports verdict %v", got)
	}
	carried, _ := collected.Result.RoleResult.Result["boundary_violations"].([]string)
	if len(carried) != 1 || carried[0] != local.ResultRel {
		t.Errorf("the collected record's boundary_violations = %#v, want the forced report carried",
			collected.Result.RoleResult.Result["boundary_violations"])
	}
	// The two boundary problems get DIFFERENT sentences (Appendix A #9):
	// this one is the artifact, not a tracker record.
	if collected.Message == "" || strings.Contains(collected.Message, "authority that is not its own") {
		t.Errorf("the message was %q: a force-added report needs its own sentence", collected.Message)
	}
}

// The flake that made TestAForceAddedReportIsCaughtAtCollect fail only in
// a whole-suite run (tick wmw), made deterministic. The fake agent's
// force_report shape writes its report BEFORE it commits it, and an
// inspect answers TERMINAL as soon as the report exists — that is the
// completion contract (inspect.go: durable evidence first), not a claim
// that the agent's turn is over. A test that synchronizes on
// Inspect().Terminal can therefore collect inside the window and read a
// branch the forced report has not reached yet: ready-to-merge,
// artifactViolations empty — the exact intermittent failure. The _slow
// fixture holds that window open for two seconds, so this regression test
// fails against the Terminal-based wait every time, not one run in
// twenty, and keeps passing only for a wait that means "the agent's whole
// turn is over".
func TestASlowForceAddedReportIsStillCaughtAtCollect(t *testing.T) {
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "force_report_slow"})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForOr(t, "the agent to finish its turn", 30*time.Second, h.agentDone) {
		h.dumpAgent(t)
		t.Fatal("the fake agent never settled")
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictBoundaryViolation {
		t.Fatalf("verdict %s (%s), want %s: the wait returned while the agent still had its forced-add commit to land, "+
			"and the backstop read a branch the report had not reached yet",
			collected.Verdict, collected.Message, subprocess.VerdictBoundaryViolation)
	}
	if len(collected.ArtifactViolations) != 1 || collected.ArtifactViolations[0] != local.ResultRel {
		t.Fatalf("artifact violations = %v, want exactly the forced report %s",
			collected.ArtifactViolations, local.ResultRel)
	}
}

// The exclude line is not a permanent edit to a file this executor does not
// own: disposal removes it while the worktree it was resolved against still
// exists, so nothing is orphaned in info/exclude once the worktree is gone.
func TestDisposeRemovesTheExcludeLineWhileTheWorktreeExists(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	prefix := h.artifactPrefix(t, handle)
	line := excludeLine(prefix)
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	// The line is there before disposal, resolved from the worktree.
	if raw, err := os.ReadFile(h.excludeFile(t, handle)); err != nil || !strings.Contains(string(raw), line+"\n") {
		t.Fatalf("the exclude line %q was not in place before disposal: %v\n%s", line, err, raw)
	}

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Fatalf("the worktree still exists after disposal: %v", err)
	}
	// After the worktree is gone the exclude file still exists — it is the
	// repository's, not the attempt's — and it no longer carries the line.
	after := strings.TrimSpace(mustRun(t, h.repo.Dir,
		"git", "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
	raw, err := os.ReadFile(after)
	if err != nil {
		t.Fatalf("the exclude file did not survive the attempt: %v", err)
	}
	if strings.Contains(string(raw), line+"\n") {
		t.Fatalf("the exclude line %q was orphaned in %s after disposal:\n%s", line, after, raw)
	}
}

// Tick 54n's other half, for this executor: the boundary refusal names the
// role and the permitted destinations, and the two executors render the SAME
// sentence for the same tracker-record write — the collect vocabulary is
// shared by contract, and two collects that disagree about the same tick are
// exactly the drift the contract's closed vocabulary exists to make
// impossible. This test reads the message composer directly because the
// refusal is the thing under test, not the agent's conduct.
// short: the refusal text for a record built in memory
func TestTheBoundaryRefusalMatchesTheLocalExecutors(t *testing.T) {
	t.Parallel()

	record := &attemptRecord{Spec: &subprocess.JobSpec{
		Role:           "closeout-epic",
		ArtifactPrefix: "runs/r/t1/",
	}}
	violations := []string{".tick/issues/t1.json"}

	msg := collectMessage(subprocess.VerdictBoundaryViolation, subprocess.FailureRunnerError, record, violations)
	if want := subprocess.BoundaryRefusal(record.Spec.Role, record.Spec.ArtifactPrefix, violations); msg != want {
		t.Errorf("the herdr refusal and the local executor's disagree about the same write:\n"+
			"  herdr:    %q\n  subprocess: %q", msg, want)
	}
	if !strings.Contains(msg, record.Spec.Role) {
		t.Errorf("the refusal %q does not name the role %s", msg, record.Spec.Role)
	}
	for _, path := range subprocess.ExemptFromBoundary() {
		if !strings.Contains(msg, path) {
			t.Errorf("the refusal %q never names %s as a permitted destination (tick 54n)", msg, path)
		}
	}
}
