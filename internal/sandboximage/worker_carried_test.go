//go:build !windows

package sandboximage

import (
	"path/filepath"
	"strings"
	"testing"
)

// A CARRIED attempt (epic hn6, run_3f034e68): a run that took over a dead
// run's claim dispatched 7uv and 378 at the dead run's attempt tips, so each
// worker's tree was seeded with work that was already complete. Each worker
// verified it, ran the named tests and the gate, reported DONE and — correctly
// — committed nothing. The container counted its work from the seed it was
// booted at, found none, and exited 10, "the branch and report reached origin
// with no work commits": the factory settled a finished tick as failed, and a
// run taking the claim over would redo it rather than collect it.
//
// The dispatch now says where the carried work was cut from
// (TICKS_WORK_BASE_SHA), and a worker that adds nothing to carried work
// delivers that work — the reconciler's own rule for the same case (tick isp,
// deliverCarriedWork), which the gate still decides.

// carriedSeed commits work on top of the fixture's base, the way a released
// attempt's branch carries it, and boots the worker AT that work with the
// original base as the work base. It returns the carried head.
func carriedSeed(t *testing.T, f *workerFixture, path, body string) string {
	t.Helper()
	git(t, f.source, "checkout", "-q", "-b", "carried")
	write(t, filepath.Join(f.source, path), body)
	git(t, f.source, "add", "-A")
	git(t, f.source, "commit", "-q", "-m", "tick "+f.tick+": the carried work")
	head := git(t, f.source, "rev-parse", "HEAD")
	git(t, f.source, "checkout", "-q", "main")
	f.env[EnvBaseSHA] = head
	f.env[EnvWorkBaseSHA] = f.baseSHA
	// The agent verifies the carried work and has nothing to add.
	delete(f.env, "TICKS_TEST_WORKER_COMMIT")
	return head
}

func TestWorkerDeliversCarriedWorkItAddedNothingTo(t *testing.T) {
	f := newWorkerFixture(t)
	carried := carriedSeed(t, f, "carried.txt", "finished by the released attempt\n")

	out, code := f.run()
	if code != 0 {
		t.Fatalf("a worker that found the carried work complete and added nothing exited %d, want 0 — "+
			"the carried work IS this attempt's delivery:\n%s", code, out)
	}
	branch := WorkerBranch(f.epic, f.tick)
	if _, ok := f.remoteFile(branch, "carried.txt"); !ok {
		t.Error("the carried work is not on the pushed branch")
	}
	if got := strings.TrimSpace(git(t, f.source, "rev-parse", "refs/heads/"+branch+"^")); got != carried {
		t.Errorf("the branch's report sits on %s, not on the carried head %s", got, carried)
	}
	report := f.reportOnOrigin(branch)
	mustContain(t, report, f.baseSHA, "the base the carried work is measured from")
	if idx := strings.LastIndex(report, "STATUS:"); idx == -1 || !strings.Contains(report[idx:], "DONE") {
		t.Errorf("the agent's status line was displaced:\n%s", report)
	}
}

// The trap exit 10 exists for stays shut: a carried seed that carries nothing
// but the released attempt's own report is not work, and neither is a work
// base equal to the seed.
func TestWorkerStillReportsAnEmptyBranchWhenTheCarriedSeedHoldsOnlyAReport(t *testing.T) {
	f := newWorkerFixture(t)
	carriedSeed(t, f, WorkerResultFile(f.tick), "# old\n\nSTATUS: BLOCKED\n")
	out, code := f.run()
	if code != ExitWorkerNoWork {
		t.Fatalf("a carried seed holding only a report gave exit %d, want %d:\n%s", code, ExitWorkerNoWork, out)
	}
}

func TestWorkerStillReportsAnEmptyBranchWhenTheWorkBaseIsTheSeed(t *testing.T) {
	f := newWorkerFixture(t)
	delete(f.env, "TICKS_TEST_WORKER_COMMIT")
	f.env[EnvWorkBaseSHA] = f.baseSHA
	out, code := f.run()
	if code != ExitWorkerNoWork {
		t.Fatalf("an uncarried worker that committed nothing gave exit %d, want %d:\n%s", code, ExitWorkerNoWork, out)
	}
}

// A work base the container cannot read is not evidence of work: the old rule
// stands and the collect, which reads the same fact from the orchestrator's
// own records, still decides.
func TestWorkerIgnoresAWorkBaseItCannotRead(t *testing.T) {
	f := newWorkerFixture(t)
	carriedSeed(t, f, "carried.txt", "work\n")
	f.env[EnvWorkBaseSHA] = strings.Repeat("ab", 20)
	out, code := f.run()
	if code != ExitWorkerNoWork {
		t.Fatalf("an unreadable work base gave exit %d, want %d:\n%s", code, ExitWorkerNoWork, out)
	}
	if strings.Contains(out, "delivers the carried work") {
		t.Errorf("the worker claimed carried work it could not read:\n%s", out)
	}
}
