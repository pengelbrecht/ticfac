package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The hn6 cloud-run stall: a run that DIED holding a claim blocked the next
// run of its epic forever. Its checkpoint on the integration branch still read
// "dispatching" — a run that dies never writes that it did — so the next run,
// under a new run id, read its claim as a live party's and held on
// foreign_claim; the supervisor resumed it, it held again over an unchanged
// tree, and it halted for a person. Meanwhile the dead run's attempt of the
// tick sat on origin with its committed work, which a dispatch would have
// redone from scratch.
//
// The fix asks the holder's HOST — the factory, the process table — when the
// holder's records do not read finished (claim.go), and takes a dead holder's
// claim over carrying its work (takeover.go). A live holder still blocks
// (dz1, tick 823), and a holder nobody can vouch for either way still holds,
// but as a wait on an answer rather than on a person.

// deadHolderFixture drives a1 to an attempt with committed work under the
// fixture's default run id, and kills that run the way a container dies: no
// terminal checkpoint, the claim left standing. It returns the held work's
// head and the run id that holds the claim. The gate names the epic's width:
// wideGate leaves room beside the standing claim, so a hold is about the
// claim itself and never about the width.
func deadHolderFixture(t *testing.T, gate string) (*fixture, string, string) {
	t.Helper()
	f := newFixture(t, fixtureOptions{mode: "hang", gate: gate})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", gate: gate, stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	_, _, head := waitHeldWork(t, f, "a1")
	f.stopEverything()

	holder := "r-fixture"
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", holder)
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("the killed run left no checkpoint: %v", err)
	}
	if checkpoint.State.Terminal() {
		t.Fatalf("the killed run's checkpoint reads %s: a run that DIED never writes that it did, and this "+
			"fixture proves nothing when it does", checkpoint.State)
	}
	return f, head, holder
}

// hostSays is a fake holder host answering one verdict for one run, counting
// the questions it was asked.
type hostSays struct {
	mu      sync.Mutex
	run     string
	verdict string
	asked   []string
}

func (h *hostSays) ask(_ context.Context, runID string) HolderState {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, runID)
	if runID != h.run {
		return HolderState{Verdict: HolderUnknown, Evidence: "the fake host knows no run " + runID}
	}
	switch h.verdict {
	case HolderDead:
		return HolderState{Verdict: HolderDead,
			Evidence: "the factory's record says failed and its Workflow instance is complete"}
	case HolderAlive:
		return HolderState{Verdict: HolderAlive, Evidence: "the Workflow instance is running"}
	}
	return HolderState{Verdict: HolderUnknown, Evidence: "the factory could not be reached"}
}

// A new run under a new run id takes over the claim a DEAD run left and
// continues that run's work: the dispatch is cut from the dead run's committed
// head, the marker says whose claim it took, on what evidence, and whose work
// it carries, and the tick closes behind the gate — with no person.
func TestANewRunTakesOverADeadRunsClaimAndCarriesItsWork(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, head, holder := deadHolderFixture(t, "")
	if containsCommit(t, f, head, "origin/epic/qeu") {
		t.Fatal("the dead run's work is already on the integration branch; this fixture proves nothing")
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	f.Runner = fakeRunnerArgv(t, "report")
	next := fixtureOptions{runID: "r-next", claimHolder: host.ask}
	r, result, err := f.run(f.Repo, next)
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed: the dead run's claim held it; a1's "+
			"stages %v", result.State, result.Failure, r.Stages("a1"))
	}
	if len(host.asked) == 0 || host.asked[0] != holder {
		t.Errorf("the holder's host was asked about %v, want %s: its checkpoint does not read finished, so "+
			"only its host can say it ended", host.asked, holder)
	}

	// The takeover is on the feed, naming the holder and the evidence.
	line, ok := journalLine(r, "a1", StageClaimTakenOver)
	if !ok {
		t.Fatalf("no %s line: a takeover nobody can see is a claim taken on a guess\n%s",
			StageClaimTakenOver, journalText(r))
	}
	for _, want := range []string{holder, "Workflow instance is complete", short(head)} {
		if !strings.Contains(line, want) {
			t.Errorf("the %s line does not name %q: %s", StageClaimTakenOver, want, line)
		}
	}

	// The dispatch continued the dead run's work rather than redoing it.
	spec := f.spec("a1")
	if spec == nil {
		t.Fatal("a1 was never dispatched by the new run")
	}
	if spec.Source.BaseSHA != head {
		t.Fatalf("the new run's attempt of a1 was cut from %s, want the dead run's work at %s",
			spec.Source.BaseSHA, head)
	}
	if !containsCommit(t, f, head, "origin/epic/qeu") {
		t.Error("the dead run's work never reached the integration branch behind the gate")
	}

	// And the marker on origin carries the decision as fields.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-next")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	var marker *attemptHandle
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			m := handleFromMap(attempt.JobHandle)
			marker = &m
			break
		}
	}
	if marker == nil {
		t.Fatal("the new run left no marker for a1")
	}
	if marker.TakenOver == nil || marker.TakenOver.RunID != holder ||
		!strings.Contains(marker.TakenOver.Evidence, "record says failed") {
		t.Errorf("the marker's taken_over is %+v, want run %s and the host's evidence", marker.TakenOver, holder)
	}
	if marker.ResumedFrom == nil || marker.ResumedFrom.RunID != holder || marker.ResumedFrom.SHA != head ||
		marker.ResumedFrom.Attempt != 1 {
		t.Errorf("the marker's resumed_from is %+v, want run %s's attempt 1 at %s", marker.ResumedFrom, holder, head)
	}
}

// hn6's ltg: the dead run's worker had FINISHED — it settled succeeded with its
// report and its commits — and the run died before it collected it. The run
// that takes the claim over must not dispatch a fresh worker "starting from"
// that work: it rules on the work as it stands, through the ordinary collect
// (report, boundary, gate), with no worker at all, and closes the tick.
func TestATakeoverCollectsADeadRunsFinishedAttemptWithoutAWorker(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f := newFixture(t, fixtureOptions{mode: "report"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "report", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	holder := "r-fixture"

	// The dead run's worker runs to its end without the run: wait for its own
	// record to say it settled, the fact the takeover reads.
	dead := f.dispatch("a1")
	executor, _, err := f.newExecutor(dead)
	if err != nil {
		t.Fatal(err)
	}
	state, found := "", false
	waitUntil(t, 30*time.Second, "the dead run's worker to settle", func() bool {
		if state == "" {
			state, found = findAttemptState(dead.StateDir)
			if !found {
				return false
			}
		}
		status, err := executor.Inspect(&subprocess.JobHandle{SchemaVersion: subprocess.SchemaVersion,
			JobID: dead.JobID, Attempt: dead.Attempt, Handle: map[string]any{"state": state}}, "")
		return err == nil && status.Terminal
	})
	_, _, head := waitHeldWork(t, f, "a1")
	if containsCommit(t, f, head, "origin/epic/qeu") {
		t.Fatal("the dead run's work is already on the integration branch; this fixture proves nothing")
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "report", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed; a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	if n := f.startCount(attemptJobID("r-next", "a1", 1)); n != 0 {
		t.Fatalf("the new run started %d worker(s) for a1 over work the dead run's worker had already finished; "+
			"a1's stages %v", n, r.Stages("a1"))
	}
	if !containsCommit(t, f, head, "origin/epic/qeu") {
		t.Error("the dead run's finished work never reached the integration branch behind the gate")
	}
	line, ok := journalLine(r, "a1", StageSettledWorkCollected)
	if !ok {
		t.Fatalf("no %s line: a collect of another run's work nobody can see\n%s",
			StageSettledWorkCollected, journalText(r))
	}
	for _, want := range []string{holder, "reads succeeded", short(head)} {
		if !strings.Contains(line, want) {
			t.Errorf("the %s line does not name %q: %s", StageSettledWorkCollected, want, line)
		}
	}

	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-next")
	record, ok, err := store.Attempt(1)
	if err != nil || !ok {
		t.Fatalf("the new run left no marker for its attempt of a1: %v", err)
	}
	marker := handleFromMap(record.JobHandle)
	if marker.CollectedFrom == nil || marker.CollectedFrom.RunID != holder || marker.CollectedFrom.Attempt != 1 ||
		marker.CollectedFrom.SHA != head {
		t.Errorf("the marker's collected_from is %+v, want run %s's attempt 1 at %s", marker.CollectedFrom, holder, head)
	}
	if marker.ResumedFrom != nil || marker.BaseSHA != dead.BaseSHA {
		t.Errorf("the collecting attempt is cut at %s resuming %+v, want the dead attempt's own base %s and no "+
			"carry: it rules on that attempt's work, measured from where that work began",
			marker.BaseSHA, marker.ResumedFrom, dead.BaseSHA)
	}
	if marker.TakenOver == nil || marker.TakenOver.RunID != holder {
		t.Errorf("the marker's taken_over is %+v, want run %s", marker.TakenOver, holder)
	}
}

// crossRunExecutor stands in for the cloudflare-sandbox executor's two
// cross-run facts: its door will not answer for another run's job (so the
// dead run's own record cannot be read through it), and it adopts another
// run's settled attempt from the evidence it is handed. The adoption points
// at the state the dead run's worker left, which is where this fixture's
// work can be collected from.
type crossRunExecutor struct {
	Executor
	foreignRun string
	state      string
	mu         sync.Mutex
	adopted    []string
}

func (e *crossRunExecutor) Inspect(h *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	if strings.HasPrefix(h.JobID, "run-"+e.foreignRun+"/") {
		return nil, fmt.Errorf("the door answers only for the credential's own run, not %s", h.JobID)
	}
	return e.Executor.Inspect(h, cursor)
}

func (e *crossRunExecutor) AdoptSettledElsewhere(spec *subprocess.JobSpec, runID, jobID string, attempt int,
	branch, evidence string) (*subprocess.JobHandle, error) {
	e.mu.Lock()
	e.adopted = append(e.adopted, fmt.Sprintf("%s %s %d %s: %s", runID, jobID, attempt, branch, evidence))
	e.mu.Unlock()
	return &subprocess.JobHandle{SchemaVersion: subprocess.SchemaVersion, JobID: spec.JobID, Attempt: attempt,
		Handle: map[string]any{"state": e.state}}, nil
}

// The hn6 shape through the cloud seams: the dead run's worker ran in the
// factory, so nothing on this host can say how it settled — the executor's
// door refuses another run's job — and the evidence is the factory's record
// (Options.SettledAttempt). A clean finish there is adopted and collected with
// no worker; the executor is handed the branch the work is on and the
// factory's own words.
func TestATakeoverCollectsACloudWorkersFinishedAttemptOnTheFactorysRecord(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f := newFixture(t, fixtureOptions{mode: "report"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "report", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	holder := "r-fixture"

	dead := f.dispatch("a1")
	executor, _, err := f.newExecutor(dead)
	if err != nil {
		t.Fatal(err)
	}
	state := ""
	waitUntil(t, 30*time.Second, "the dead run's worker to settle", func() bool {
		found := false
		if state, found = findAttemptState(dead.StateDir); !found {
			return false
		}
		status, err := executor.Inspect(&subprocess.JobHandle{SchemaVersion: subprocess.SchemaVersion,
			JobID: dead.JobID, Attempt: dead.Attempt, Handle: map[string]any{"state": state}}, "")
		return err == nil && status.Terminal
	})
	_, _, head := waitHeldWork(t, f, "a1")

	cloud := &crossRunExecutor{foreignRun: holder, state: state}
	f.wrap = func(inner Executor) Executor {
		cloud.Executor = inner
		return cloud
	}
	var asked []string
	factory := func(_ context.Context, runID, tickID string, attempt int) SettledState {
		asked = append(asked, fmt.Sprintf("%s/%s/%d", runID, tickID, attempt))
		return SettledState{Known: true, Succeeded: true,
			Evidence: "the factory recorded its worker container completed with exit 0"}
	}
	host := &hostSays{run: holder, verdict: HolderDead}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "report", claimHolder: host.ask,
		settledAttempt: factory})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed; a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	if n := f.startCount(attemptJobID("r-next", "a1", 1)); n != 0 {
		t.Fatalf("the new run started %d worker(s) over a cloud worker's finished work", n)
	}
	if len(asked) == 0 || asked[0] != holder+"/a1/1" {
		t.Errorf("the factory was asked about %v, want %s/a1/1", asked, holder)
	}
	cloud.mu.Lock()
	adopted := append([]string{}, cloud.adopted...)
	cloud.mu.Unlock()
	if len(adopted) == 0 || !strings.Contains(adopted[0], "completed with exit 0") ||
		!strings.Contains(adopted[0], "run-"+holder+"/tick-a1/attempt-1") {
		t.Errorf("the executor adopted %v, want the dead run's job on the factory's evidence", adopted)
	}
	if line, ok := journalLine(r, "a1", StageSettledWorkCollected); !ok ||
		!strings.Contains(line, "completed with exit 0") {
		t.Errorf("the %s line does not carry the factory's evidence: %q", StageSettledWorkCollected, line)
	}
	if !containsCommit(t, f, head, "origin/epic/qeu") {
		t.Error("the cloud worker's finished work never reached the integration branch behind the gate")
	}
}

// The dead run's finished work is RULED ON, not waved through: its worker
// settled succeeded, but it wrote under the tracker's authority. The collect
// rejects it on the merits exactly as it would any attempt's, and only then
// is a worker dispatched — fresh, without the rejected work — and the tick
// closes on that worker's clean try.
func TestATakeoverDispatchesAWorkerOnlyWhenTheDeadRunsFinishedWorkIsRejected(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f := newFixture(t, fixtureOptions{mode: "boundary-first"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "boundary-first", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)

	dead := f.dispatch("a1")
	executor, _, err := f.newExecutor(dead)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "the dead run's worker to settle", func() bool {
		state, found := findAttemptState(dead.StateDir)
		if !found {
			return false
		}
		status, err := executor.Inspect(&subprocess.JobHandle{SchemaVersion: subprocess.SchemaVersion,
			JobID: dead.JobID, Attempt: dead.Attempt, Handle: map[string]any{"state": state}}, "")
		return err == nil && status.Terminal
	})

	host := &hostSays{run: "r-fixture", verdict: HolderDead}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "boundary-first", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed; a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	if _, ok := journalLine(r, "a1", StageSettledWorkCollected); !ok {
		t.Fatalf("the dead run's finished work was not collected before a worker was dispatched; a1's stages %v",
			r.Stages("a1"))
	}
	if n := f.startCount(attemptJobID("r-next", "a1", 1)); n != 0 {
		t.Errorf("the collecting attempt started %d worker(s)", n)
	}
	if n := f.startCount(attemptJobID("r-next", "a1", 2)); n != 1 {
		t.Errorf("the rejection of the dead run's work dispatched %d worker(s), want exactly one; a1's stages %v",
			n, r.Stages("a1"))
	}
	if line, ok := journalLine(r, "a1", StageRejected); !ok || !strings.Contains(line, "boundary") {
		t.Errorf("the dead run's work was not rejected on its boundary violation: %q", line)
	}
}

// A LIVE holder still blocks (dz1, tick 823): its host vouches it is running,
// so the new run holds on foreign_claim and never claims or dispatches the
// tick — however much room the width has.
func TestALiveHolderStillBlocksTheTickItClaims(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, _, holder := deadHolderFixture(t, wideGate)

	host := &hostSays{run: holder, verdict: HolderAlive}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "report", gate: wideGate, claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run should have HELD, not returned an operational error: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedForeignClaim || result.Failure.TickID != "a1" {
		t.Fatalf("the new run ended %s with failure %+v, want %s on a1: a live holder's claim is not this "+
			"run's to work under", result.State, result.Failure, RefusedForeignClaim)
	}
	if !strings.Contains(result.Failure.Message, holder) {
		t.Errorf("the hold does not name the holder: %s", result.Failure.Message)
	}
	for _, event := range r.Journal() {
		if event.Tick == "a1" && (event.Stage == StageDispatched || event.Stage == StageClaimTakenOver) {
			t.Errorf("a1 was %s over a LIVE holder's claim", event.Stage)
		}
	}
}

// A holder nobody can vouch for either way — its records do not read
// finished and its host cannot be asked — is never taken over on a guess:
// the run holds. But the hold waits on an answer, not on a person, so the
// supervisor continues it without one, and an unchanged tree is not a spin.
func TestAHolderWhoseStateCannotBeReadHoldsAsATransientWait(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, _, holder := deadHolderFixture(t, wideGate)

	host := &hostSays{run: holder, verdict: HolderUnknown}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "report", gate: wideGate, claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run should have HELD, not returned an operational error: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedClaimHolderUnknown || result.Failure.TickID != "a1" {
		t.Fatalf("the new run ended %s with failure %+v, want %s on a1", result.State, result.Failure,
			RefusedClaimHolderUnknown)
	}
	if !strings.Contains(result.Failure.Message, "could not be reached") {
		t.Errorf("the hold does not carry the host's answer: %s", result.Failure.Message)
	}
	if holdsForAPerson(RefusedClaimHolderUnknown) {
		t.Error("an unreadable holder is held for a person: nobody has anything to decide")
	}
	if !resumesWithoutAPerson(RefusedClaimHolderUnknown) || !waitsOnTheWorld(RefusedClaimHolderUnknown) {
		t.Error("an unreadable holder's hold is not resumed without a person, or is read as a spin over an " +
			"unchanged tree: the answer comes from the holder's host, never from the branch")
	}
	for _, event := range r.Journal() {
		if event.Tick == "a1" && (event.Stage == StageDispatched || event.Stage == StageClaimTakenOver) {
			t.Errorf("a1 was %s over a holder nobody could vouch for", event.Stage)
		}
	}
}

// The hn6 shape exactly: the dead run's attempt was a cloudflare-sandbox
// one, so its work is not on the attempt's write ref at all — it is on the
// branch its worker container pushed, and because another run's attempt 1
// already held the landing name, on the per-run fallback
// `tick/<epic>/attempt-1/<tick>-<run id>` (image/worker.sh
// adopt_worker_branch, PR #129). The takeover finds it there, and never
// takes the shared landing name's work, which is another run's.
func TestATakeoverCarriesASandboxAttemptsWorkFromItsPerRunLandingBranch(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, head, holder := deadHolderFixture(t, "")
	dir := f.Repo.Dir

	// The work leaves the write ref for the per-run landing branch, and an
	// older run's work — not cut from this attempt's base — holds the shared
	// landing name.
	writeRef := "refs/heads/ticfac/run-" + holder + "/tick-a1/attempt-1"
	landing := "tick/qeu/attempt-1/a1"
	mustRun(t, dir, "git", "push", "--quiet", "origin", head+":refs/heads/"+landing+"-"+holder)
	mustRun(t, dir, "git", "push", "--quiet", "origin", f.Repo.Base+":refs/heads/"+landing)
	mustRunAllowingFailure(dir, "git", "push", "--quiet", "origin", "--delete", writeRef)
	mustRun(t, dir, "git", "update-ref", "-d", writeRef)

	// The dead run's marker names the sandbox executor, as a cloud run's does.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "marker-edit"))
	mustRun(t, clone.Dir, "git", "checkout", "--quiet", "epic/qeu")
	path := filepath.Join(clone.Dir, ".ticfac", "runs", holder, "attempts", "1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["job_handle"].(map[string]any)["executor"] = cloudflareSandboxExecutor
	edited, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(edited)+"\n")
	mustRun(t, clone.Dir, "git", "commit", "--quiet", "-am", "the dead run's attempt was a sandbox one")
	mustRun(t, clone.Dir, "git", "push", "--quiet", "origin", "epic/qeu")

	host := &hostSays{run: holder, verdict: HolderDead}
	f.Runner = fakeRunnerArgv(t, "report")
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed; a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	spec := f.spec("a1")
	if spec == nil || spec.Source.BaseSHA != head {
		t.Fatalf("the new run's attempt of a1 was not cut from the dead run's per-run landing branch at %s: %+v",
			head, spec)
	}
	if line, ok := journalLine(r, "a1", StageClaimTakenOver); !ok || !strings.Contains(line, landing+"-"+holder) {
		t.Errorf("the takeover does not name the branch the work was on (%s-%s): %q", landing, holder, line)
	}
}

// A dead run never reaches its close-out, so the findings it left UNTRIAGED
// would sit under a run nothing will ever finish. The run that takes over its
// claim adopts them: they become this run's drafts — their discovery kept —
// and this run's close-out decides them by the existing rules (here: routed to
// another repository, so a local backlog tick naming the target), while a
// finding a person already decided stays where it was decided.
func TestATakeoverAdoptsTheDeadRunsUntriagedFindingsForTheCloseOut(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, _, holder := deadHolderFixture(t, "")
	routedEpic(t, f)

	// The dead run's drafts: one left for triage, one a person discarded.
	dead := openRunStore(t, f.Repo.Dir, "epic/qeu", holder)
	attempts, err := dead.Attempts()
	if err != nil || len(attempts) == 0 {
		t.Fatalf("read the dead run's attempts: %v %d", err, len(attempts))
	}
	draft := func(title string) runstate.Finding {
		reported := subprocess.Finding{Kind: "upstream-tick", Title: title,
			Body: "The upstream half, reported verbatim.", Severity: "low", Target: routedTarget}
		return runstate.Finding{
			Key: findingKey(reported), Source: findingSource, DiscoveredFrom: "run-" + holder + "/tick-a1/attempt-1",
			Kind: reported.Kind, Title: reported.Title, Body: reported.Body, Severity: reported.Severity,
			Target: reported.Target, DoneItem: "A1", TickID: attempts[0].TickID, Attempt: attempts[0].Attempt,
			Status: runstate.FindingProposed, ProposedAt: "2026-09-29T09:00:00Z", Provenance: attempts[0].Provenance,
		}
	}
	untriaged, decided := draft("An upstream finding the dead run left untriaged"), draft("One a person already discarded")
	for _, one := range []runstate.Finding{untriaged, decided} {
		if _, err := dead.PutFinding(one); err != nil {
			t.Fatalf("draft the dead run's finding: %v", err)
		}
	}
	if _, _, err := dead.TriageFinding(decided.Key, runstate.Triage{Status: runstate.FindingDiscarded, By: "a person"}); err != nil {
		t.Fatalf("discard the decided finding: %v", err)
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	f.Runner = fakeRunnerArgv(t, "report")
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the new run ended %s (%+v): the adopted finding must be decided by its close-out, not held on",
			result.State, result.Failure)
	}
	line, ok := journalLine(r, untriaged.TickID, StageFindingAdopted)
	if !ok || !strings.Contains(line, holder) || !strings.Contains(line, untriaged.Key) {
		t.Errorf("no %s line naming the dead run and the finding: %q\n%s", StageFindingAdopted, line, journalText(r))
	}

	// Decided by this run's close-out as its own: routed, a local backlog tick.
	finding, record := routedFinding(t, f, r)
	if finding.Key != untriaged.Key || finding.DiscoveredFrom != untriaged.DiscoveredFrom {
		t.Errorf("the adopted finding is %s discovered by %s, want %s discovered by %s — the discovery is kept",
			finding.Key, finding.DiscoveredFrom, untriaged.Key, untriaged.DiscoveredFrom)
	}
	assertRuleDecided(t, finding, record)
	assertLocalTrackingTick(t, f, finding, record, "no [findings.route")

	// The decided one stays the dead run's: its decision is already made.
	next := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-next")
	if _, ok, err := next.Finding(decided.Key); err != nil || ok {
		t.Errorf("a finding a person already discarded was adopted (%v, %v): its decision stands where it was made", ok, err)
	}
}

// integratedHolderFixture drives a1 under the fixture's default run id until
// its work is MERGED into the integration branch, and kills that run there —
// before the integrated gate and the close, its claim left standing. It
// returns the merged head of a1's work and the run id holding the claim.
func integratedHolderFixture(t *testing.T) (*fixture, string, string) {
	t.Helper()
	f := newFixture(t, fixtureOptions{mode: "report"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "report", stopAfter: stopAt("a1", StageIntegrated)})
	killedAfter(t, err, "a1", StageIntegrated)
	f.stopEverything()
	holder := "r-fixture"
	head := branchHead(f.Repo.Dir, "refs/heads/ticfac/run-"+holder+"/tick-a1/attempt-1")
	if head == "" || !containsCommit(t, f, head, "origin/epic/qeu") {
		t.Fatalf("a1's work (%q) is not on the integration branch: this fixture proves nothing", head)
	}
	if f.Tracker.count("close:a1") != 0 {
		t.Fatal("a1 closed before the kill: this fixture proves nothing")
	}
	return f, head, holder
}

// assertFinishedFromIntegration is the shape a taken-over tick whose work is
// already merged must take: no worker, no collect, the tick closed behind the
// gate, and the marker naming the merged delivery it was finished from.
func assertFinishedFromIntegration(t *testing.T, f *fixture, r *Reconciler, result *Result, holder, delivered string) {
	t.Helper()
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed; a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	for n := 1; n <= 4; n++ {
		if starts := f.startCount(attemptJobID("r-next", "a1", n)); starts != 0 {
			t.Fatalf("the new run started %d worker(s) for a1 (run dispatch #%d) over work already merged into "+
				"the integration branch; a1's stages %v", starts, n, r.Stages("a1"))
		}
	}
	for _, event := range r.Journal() {
		if event.Tick != "a1" {
			continue
		}
		switch event.Stage {
		case StageSettledWorkCollected, StageRejected, StageDispatched:
			t.Errorf("a1 was %s although its work is already merged: %s", event.Stage, event.Detail)
		case StageCollected:
			if !strings.Contains(event.Detail, "not collected a second time") {
				t.Errorf("a1 was collected again although its work is already merged: %s", event.Detail)
			}
		}
	}
	line, ok := journalLine(r, "a1", StageClaimTakenOver)
	if !ok || !strings.Contains(line, "ALREADY on") || !strings.Contains(line, short(delivered)) {
		t.Errorf("the takeover does not say the work is already merged at %s: %q", short(delivered), line)
	}
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Errorf("a1 closed %d times, want once", got)
	}
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-next")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.TickID != "a1" {
			continue
		}
		marker := handleFromMap(attempt.JobHandle)
		if marker.CollectedFrom == nil || marker.CollectedFrom.RunID != holder || marker.CollectedFrom.SHA != delivered {
			t.Errorf("the marker's collected_from is %+v, want run %s's merged delivery %s", marker.CollectedFrom,
				holder, delivered)
		}
		if marker.TakenOver == nil || marker.TakenOver.RunID != holder {
			t.Errorf("the marker's taken_over is %+v, want run %s", marker.TakenOver, holder)
		}
		return
	}
	t.Error("the new run left no marker for a1: the finish is not reconstructible")
}

// hn6 run_ee8e's 378, the plain shape: the dead run had MERGED its attempt's
// work into the integration branch and died before the integrated gate and the
// close. The run that takes the claim over must not start a fresh worker on a
// tick whose work is merged, nor collect it again: it goes straight to the
// integrated gate and closes the tick.
func TestATakeoverFinishesADeadRunsMergedAttemptFromTheIntegrationBranch(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, head, holder := integratedHolderFixture(t)

	host := &hostSays{run: holder, verdict: HolderDead}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "report", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	assertFinishedFromIntegration(t, f, r, result, holder, head)
}

// hn6 run_ee8e's 378, exactly: the dead run's attempt was a CARRIED cloud
// attempt whose worker added nothing but its own report, so the dead run's
// collect delivered the carried head (tick isp) and merged THAT — while the
// container's landing branch still carries one more commit, the report. The
// takeover read that branch as unmerged work, re-collected it measured from
// the carried head, ruled it no-commits ("the only commit … is the
// container's own report") and redispatched the tick a tier up. Work whose
// only commits beyond the integration branch are the attempt's own report is
// merged work: the tick is finished from the integration branch.
func TestATakeoverFinishesACarriedCloudAttemptWhoseOnlyUnmergedCommitIsItsReport(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, head, holder := integratedHolderFixture(t)
	dir := f.Repo.Dir

	// The dead run's attempt becomes the hn6 shape: a cloudflare-sandbox
	// attempt CARRIED from an earlier one, cut at the carried head — which is
	// the work, already merged — with its write ref at that base and its
	// container's landing branch one report commit above it.
	writeRef := "refs/heads/ticfac/run-" + holder + "/tick-a1/attempt-1"
	landing := "tick/qeu/attempt-1/a1"
	scratch := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "landing"))
	mustRun(t, scratch.Dir, "git", "fetch", "--quiet", "origin", writeRef)
	mustRun(t, scratch.Dir, "git", "checkout", "--quiet", "--detach", head)
	write(t, filepath.Join(scratch.Dir, "RESULT-a1.md"), "# a1\n\nThe container's own report.\n\nSTATUS: DONE\n")
	mustRun(t, scratch.Dir, "git", "add", "RESULT-a1.md")
	mustRun(t, scratch.Dir, "git", "commit", "--quiet", "-m", "tick a1: worker report")
	reportHead := strings.TrimSpace(mustRun(t, scratch.Dir, "git", "rev-parse", "HEAD"))
	mustRun(t, scratch.Dir, "git", "push", "--quiet", "origin", reportHead+":refs/heads/"+landing)
	mustRun(t, dir, "git", "push", "--quiet", "--force", "origin", head+":"+writeRef)

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "marker-edit"))
	mustRun(t, clone.Dir, "git", "checkout", "--quiet", "epic/qeu")
	path := filepath.Join(clone.Dir, ".ticfac", "runs", holder, "attempts", "1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	handle := record["job_handle"].(map[string]any)
	handle["executor"] = cloudflareSandboxExecutor
	handle["base_sha"] = head
	handle["resumed_from"] = map[string]any{"tick_id": "a1", "attempt": 1, "write_ref": writeRef, "sha": head,
		"released_by": "ticfac (took over the claim of run r-earlier)", "run_id": "r-earlier"}
	edited, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(edited)+"\n")
	mustRun(t, clone.Dir, "git", "commit", "--quiet", "-am", "the dead run's attempt was a carried sandbox one")
	mustRun(t, clone.Dir, "git", "push", "--quiet", "origin", "epic/qeu")
	if containsCommit(t, f, reportHead, "origin/epic/qeu") {
		t.Fatal("the report commit is on the integration branch: this fixture proves nothing")
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	factory := func(context.Context, string, string, int) SettledState {
		return SettledState{Known: true, Succeeded: true,
			Evidence: "the factory recorded its worker container completed with exit 0"}
	}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "report", claimHolder: host.ask,
		settledAttempt: factory})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	assertFinishedFromIntegration(t, f, r, result, holder, head)
}

// hn6's oro/log and yjq/qrl: findings are content-hashed, and the key is the
// finding's identity across the runs of one epic, not only within one. A run
// that absorbed a finding into a tick and then DIED left that decision on the
// integration branch; the run that takes its claim over collects the same
// report again and meets the same finding. It must LINK to the standing
// decision — the tick the dead run created, open or closed — and never draft,
// decide or absorb it a second time as a duplicate tick.
func TestATakeoverLinksAFindingTheDeadRunAlreadyAbsorbedRatherThanAbsorbingItAgain(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	// An item nothing can run yet: the predicted fallback absorbs the finding.
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", stopAfter: stopAt("a1", StageAbsorbed)})
	killedAfter(t, err, "a1", StageAbsorbed)
	f.stopEverything()
	holder := "r-fixture"

	dead := openRunStore(t, f.Repo.Dir, "epic/qeu", holder)
	decided, err := dead.Absorptions()
	if err != nil || len(decided) != 1 {
		t.Fatalf("the dead run left %d absorption decision(s) (%v), want the one this fixture proves on", len(decided), err)
	}
	first := decided[0]
	original, ok, err := dead.Finding(first.Key)
	if err != nil || !ok || original.Status != runstate.FindingPromoted || original.PromotedAs != first.TickID {
		t.Fatalf("the dead run's draft is %+v (%v): want it promoted as %s", original, err, first.TickID)
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "finding_local", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the new run ended %s (failure %+v) with a1 not closed; a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}

	next := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-next")
	again, err := next.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("the new run absorbed the dead run's finding again as %+v: a finding whose key already produced "+
			"tick %s is linked, never re-absorbed", again, first.TickID)
	}
	if _, ok, err := next.Finding(first.Key); err != nil || ok {
		t.Errorf("the new run drafted the finding a second time (%v, %v): the dead run's decision stands", ok, err)
	}
	for _, event := range r.Journal() {
		if event.Stage == StageAbsorbed || event.Stage == StageFindingFiled {
			t.Errorf("the new run %s the finding the dead run already absorbed: %s", event.Stage, event.Detail)
		}
	}
	line, ok := journalLine(r, "a1", StageFindingDuplicate)
	if !ok {
		t.Fatalf("no %s line on a1: the link to the standing decision nobody can see\n%s",
			StageFindingDuplicate, journalText(r))
	}
	for _, want := range []string{first.Key, holder, first.TickID} {
		if !strings.Contains(line, want) {
			t.Errorf("the %s line does not name %q: %s", StageFindingDuplicate, want, line)
		}
	}
	if n := f.Tracker.count("create:" + first.TickID); n != 1 {
		t.Errorf("the absorbed tick was created %d times, want once", n)
	}
}

// hn6's run_be66ff09: the dead run's orphaned claim stands on a tick that is
// NOT at the head of the new run's plan, and the width is full of it. The new
// run must take the orphan over first — taking it over claims nothing the
// width has not already counted — rather than hold on claim_width for a slot
// only the takeover itself would ever free. That run held, the supervisor
// resumed it over an unchanged tree and halted: a stopped run's claim cost the
// epic its whole run.
func TestADeadRunsClaimOffTheHeadOfThePlanIsTakenOverBeforeTheWidthIsCounted(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	// Width one (no declared width): a1's orphaned claim fills it.
	f, _, holder := deadHolderFixture(t, "")

	// The tracker now layers a2 ahead of a1, so the plan's head is a tick
	// nobody claims and the orphan sits behind it.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Waves = [][]string{{"a2"}, {"a1"}, {"b1"}, {"rv", "co"}}
	state.Order = []string{"a2", "a1", "b1", "rv", "co"}
	if err := f.Tracker.save(state); err != nil {
		t.Fatal(err)
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	f.Runner = fakeRunnerArgv(t, "report")
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") || !contains(result.Closed, "a2") {
		t.Fatalf("the new run ended %s (failure %+v) closing %v: the dead run's claim on a1 filled the width "+
			"and the run held on it instead of taking it over\n%s", result.State, result.Failure, result.Closed,
			journalText(r))
	}
	if _, ok := journalLine(r, "a1", StageClaimTakenOver); !ok {
		t.Errorf("no %s line for a1: the orphan was not taken over\n%s", StageClaimTakenOver, journalText(r))
	}
}
