package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The resolve a restart must wait for (epic-2jn, 2026-09-27, 4mv attempt 33).
// 4mv's attempt conflicted on integration and the run dispatched a
// resolve-conflict job, whose worktree is cut at the committed, conflicted
// merge — markers present. The operator SIGTERMed the orchestrator mid-resolve
// to restart it on a new build: the flush snapshotted the job's uncommitted
// work onto its wip ref and pushed the job's branch — still at the conflicted
// starting commit, because the worker, which outlives the orchestrator, had
// not committed yet. The resumed run met the same conflict, found the resolve
// branch on origin and read its head as the job's FINISHED result: "committed
// README.md still carrying its conflict markers", merge_failed, halted — while
// the job it judged was still running.
//
// A branch on origin says only that something pushed it: the supervisor's
// timer pushes a live job's head, and so does a SIGTERM flush. Whether the job
// settled is the executor's answer, never the branch's.

// resolveResumeExecutor stands in for the operating system around the run: it
// kills the orchestrator mid-await (a panic RunProtected turns into the
// simulated kill, exactly as stopAfter's is), and it releases the held worker
// the moment a later incarnation addresses the resolve job again.
type resolveResumeExecutor struct {
	Executor
	die     *atomic.Bool
	release func()
}

func isResolveJob(jobID string) bool { return strings.Contains(jobID, "/resolve-") }

func (e *resolveResumeExecutor) Inspect(h *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	if h != nil && isResolveJob(h.JobID) && e.die.Load() {
		panic(stopped{At: Event{Tick: "a2", Stage: "killed_mid_resolve"}})
	}
	return e.Executor.Inspect(h, cursor)
}

func (e *resolveResumeExecutor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	handle, err := e.Executor.Start(spec)
	if isResolveJob(spec.JobID) && !e.die.Load() && e.release != nil {
		e.release()
	}
	return handle, err
}

// TestARestartMidResolveWaitsForTheResolveJobInsteadOfJudgingItsStartingCommit
// drives the epic-2jn sequence: a live resolve job, a SIGTERM flush, the
// orchestrator's death mid-await, and a resume.
//
// serial: this test states the process environment (CONFLICT_SYNC,
// CONFLICT_TICKS) for the fake runner's workers to synchronise through, and
// t.Setenv forbids a parallel test.
func TestARestartMidResolveWaitsForTheResolveJobInsteadOfJudgingItsStartingCommit(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, diskLost := range []bool{false, true} {
		name := "the worker outlives the orchestrator"
		if diskLost {
			name = "the worker and its state are gone"
		}
		t.Run(name, func(t *testing.T) {
			conflictSync(t)
			// The implement side that conflicts outlives its own report by a
			// moment, so the run collects and releases it while it is still
			// alive (hol): the interleaving CI met by chance, forced.
			t.Setenv("CONFLICT_LINGER", "3")
			sync := os.Getenv("CONFLICT_SYNC")
			opts := fixtureOptions{mode: "conflict_resolve_hold", gate: resolveGate}
			f := newFixture(t, opts)
			goFile := filepath.Join(f.Root, "evac-go")
			f.Runner = append([]string{f.Runner[0], "EVAC_GO=" + goFile}, f.Runner[1:]...)
			seedSharedFile(t, f)

			var die atomic.Bool
			var releasing atomic.Bool
			f.wrap = func(inner Executor) Executor {
				return &resolveResumeExecutor{Executor: inner, die: &die, release: func() {
					if releasing.Load() {
						touch(t, goFile)
					}
				}}
			}

			// Incarnation one: the conflict, and the resolve job holding its
			// resolution uncommitted.
			r, err := New(f.options(f.Repo, opts))
			if err != nil {
				t.Fatal(err)
			}
			runDone := make(chan error, 1)
			go func() {
				_, err := r.RunProtected(context.Background())
				runDone <- err
			}()
			waitUntil(t, 90*time.Second, "the resolve job's uncommitted resolution",
				func() bool { _, err := os.Stat(filepath.Join(sync, "resolve.holding")); return err == nil })
			states, _ := filepath.Glob(filepath.Join(f.StateRoot, "r-fixture", "a2", "*", "resolve"))
			if len(states) != 1 {
				t.Fatalf("want one resolve state directory for a2, found %v", states)
			}
			resolveRoot := states[0]
			state, found := findAttemptState(resolveRoot)
			if !found {
				t.Fatalf("no attempt record under %s", resolveRoot)
			}
			attempt := filepath.Base(filepath.Dir(resolveRoot))

			// The SIGTERM: the flush, then the orchestrator's death — and
			// nothing else. The worker is a separate process, never signalled.
			account := strings.Join(r.Evacuate("terminated", 30*time.Second), "\n")
			die.Store(true)
			select {
			case err := <-runDone:
				if _, ok := err.(*killedAt); !ok {
					t.Fatalf("the first incarnation ended with %v, not the simulated kill", err)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("the first incarnation did not die mid-await")
			}
			if subprocess.AttemptSettled(state) {
				t.Fatal("the resolve worker settled before it was released: the fixture did not hold it")
			}

			// The incident's state on origin: the resolve branch at the
			// conflicted starting commit, and the uncommitted resolution on
			// the wip ref — never on the branch.
			branch := "refs/heads/ticfac/run-r-fixture/tick-a2/resolve-" + attempt
			head := originRefSHA(t, f.Repo.Origin, branch)
			if head == "" {
				t.Fatalf("origin holds no %s after the flush\n%s", branch, account)
			}
			if blob := mustRun(t, f.Repo.Origin, "git", "show", head+":shared-work.txt"); !conflictMarkersIn(blob) {
				t.Fatalf("the resolve branch on origin is not at its conflicted starting commit: %q", blob)
			}
			wipRef := "refs/ticfac/wip/run-r-fixture/tick-a2/resolve-" + attempt
			if originRefSHA(t, f.Repo.Origin, wipRef) == "" {
				t.Errorf("origin holds no %s: the flush did not preserve the resolve's uncommitted work\n%s",
					wipRef, account)
			}

			repo := f.Repo
			if diskLost {
				// The worker dies with its container, and its state and the
				// checkout with it: only what it pushed survives.
				repo = cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
				if alive, _ := subprocess.KillLiveProcesses(state, 10*time.Second); alive {
					t.Fatal("the resolve worker could not be stopped")
				}
				if err := os.RemoveAll(resolveRoot); err != nil {
					t.Fatal(err)
				}
			}

			// Incarnation two: the same conflict, and the resolve job it must
			// adopt (or, with the disk gone, continue from what it pushed) and
			// WAIT for — the worker is released only once the job is
			// addressed again.
			releasing.Store(true)
			die.Store(false)
			resumed, result, err := f.run(repo, opts)
			if err != nil {
				t.Fatalf("the resumed run did not finish: %v", err)
			}
			for _, event := range resumed.Journal() {
				if event.Tick == "a2" && event.Stage == StageRejected {
					t.Errorf("a2 was rejected on resume: %s", event.Detail)
				}
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the resumed run ended %s (%s): an in-flight resolve job is waited on, not judged "+
					"by its starting commit", result.State, result.Reason)
			}
			current, err := f.Tracker.Show(context.Background(), "a2")
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != "closed" {
				t.Errorf("a2 is %s, want closed", current.Status)
			}
			clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
			if shared := readGitBlob(t, clone.Dir, "origin/epic/qeu", "shared-work.txt"); shared != "resolved by the resolve-conflict job" {
				t.Errorf("the integrated shared file is %q, not the resolve job's resolution", shared)
			}

			// The resume says what it did: adopted the live job, or continued
			// the gone one from what it pushed.
			wantNote := "adopted by identity and waited on"
			if diskLost {
				wantNote = "started again from the head it pushed"
			}
			noted := false
			for _, event := range resumed.Journal() {
				if event.Tick == "a2" && event.Stage == StageAdopted && strings.Contains(event.Detail, wantNote) {
					noted = true
				}
			}
			if !noted {
				t.Errorf("the resume recorded no %q for the resolve job: %v", wantNote, resumed.Stages("a2"))
			}

			// Collected exactly once, by the incarnation that waited for it.
			collects := 0
			for _, event := range resumed.Journal() {
				if event.Stage == StageCollected && strings.Contains(event.Detail, "the resolve-conflict job") {
					collects++
				}
			}
			if collects != 1 {
				t.Errorf("the resolve job was collected %d times on resume, want exactly once", collects)
			}
			raw, _ := os.ReadFile(filepath.Join(sync, "resolve.starts"))
			starts := strings.Count(string(raw), "\n")
			want := 1
			if diskLost {
				want = 2
			}
			if starts != want {
				t.Errorf("the resolve worker was started %d times, want %d: a live job is adopted, never "+
					"started again", starts, want)
			}
		})
	}
}
