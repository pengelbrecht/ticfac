package reconcile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
