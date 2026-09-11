package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// dispose: tear the workspace down — after collect, with the liveness
// question answered NEXT TO the removal, and never over work nobody kept.

func TestDisposeTearsTheWorkspaceDown(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// The run made the work durable on the remote — what a merge means here.
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree still exists after disposal: %v", err)
	}
	if _, err := os.Stat(local.State + "/" + fileResult); err != nil {
		t.Error("disposal must not take the attempt's state with it: the collected result outlives the worktree")
	}
	// The archived report survived the worktree it was removed from.
	if _, err := os.Stat(local.State + "/" + fileReportArchive); err != nil {
		t.Errorf("the worker's report was not archived before the worktree went: %v", err)
	}
	removed := h.removals()
	if len(removed) != 1 || removed[0] != local.WorkspaceID {
		t.Fatalf("worktree.remove saw %v, want the one workspace %s", removed, local.WorkspaceID)
	}
}

func TestDisposeRefusesBeforeTheFactsArePersisted(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{})
	if err == nil {
		t.Fatal("an uncollected attempt was disposed: disposal before its facts are persisted is how a run " +
			"loses the only record of what it did")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedNotPersisted {
		t.Errorf("the refusal was %v, want the not-persisted one", err)
	}
	// An explicit reason is the escape hatch, and it does not need the
	// collect to have happened. The branch is kept: a reason permits an
	// uncollected disposal, and never says the commits are disposable.
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "the run was released by the operator", KeepBranch: true}); err != nil {
		t.Errorf("a disposal with an explicit reason was refused: %v", err)
	}
}

func TestDisposeRefusesAWorkingAgent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup"})
	if err == nil {
		t.Fatal("a WORKING agent was torn down: it may be mid-turn, about to commit")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedLive {
		t.Errorf("the refusal was %v, want the live one", err)
	}
	if len(h.server.Removed()) != 0 {
		t.Error("the refusal must have torn down nothing")
	}
}

func TestDisposeRefusesOnAnUnansweredLivenessQuestion(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	// herdr goes quiet between the collect and the removal: silence is not
	// evidence that killing an agent is safe.
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "invalid_request", "herdr has nothing to say")
	})

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup"})
	if err == nil {
		t.Fatal("an unanswered liveness question let the teardown through")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the refusal was %v, want the liveness-unknown one", err)
	}
	if len(h.server.Removed()) != 0 {
		t.Error("the refusal must have torn down nothing")
	}
}

func TestDisposeToleratesAnAlreadyGoneWorkspace(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// A workspace herdr no longer has is the state the step exists to
	// reach, not a failure to reach it.
	mustRun(t, h.repo.Dir, "git", "worktree", "remove", "--force", local.Worktree)
	delete(h.workspaces, local.WorkspaceID)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup", KeepBranch: true}); err != nil {
		t.Errorf("an already-gone workspace failed the disposal: %v", err)
	}
}

func TestDisposeRefusesABranchNobodyKept(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}

	// The commits exist only on this branch. Deleting it would discard the
	// only copy — and a Reason does not lift that.
	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the run was released"})
	if err == nil {
		t.Fatal("a branch holding commits no remote has was deleted")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedBranchUnsafe {
		t.Errorf("the refusal was %v, want the branch-unsafe one — it is what the reconciler retries on KeepBranch", err)
	}
	// The reconciler's retry keeps the branch: the worktree and the
	// workspace go, the commits stay.
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the run was released", KeepBranch: true}); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if head := headOf(h.repo.Dir, local.Branch); head == "" {
		t.Error("KeepBranch disposed the branch too: the refusal a Reason does not lift was lifted anyway")
	}
}

func TestDisposeArchivesTheOwnReportBeforeRemoving(t *testing.T) {
	// The never-Force rule, and its one narrow exception: the attempt's own
	// untracked report is the only dirt a collected worktree should carry,
	// and it moves beside the attempt record rather than widening to Force.
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileReportArchive); err != nil {
		t.Fatalf("the report was not archived: %v", err)
	}
	// The removal went through WITHOUT force — the harness's remove refuses
	// a dirty worktree, so a passing disposal proves the archive happened.
	if len(h.removals()) != 1 {
		t.Errorf("worktree.remove saw %v, want the one workspace", h.removals())
	}
}

// ------------------------------------------------- identity, not bookkeeping ---

// The 5hz hazard, driven exactly as it was observed: herdr restarts (an
// upgrade, a protocol bump), the workspace ids it hands out move, and the id
// the attempt recorded is now the name of NOTHING — or worse, of somebody
// else's workspace. The seven canonify orphans were made by a teardown that
// trusted the id, believed `workspace_not_found`, and removed the manifest.

func TestDisposeReclaimsByIdentityWhenTheRecordedWorkspaceIdIsStale(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	// The restart: the same worktree at the same path, the same branch —
	// under a workspace id nothing ever recorded.
	renumbered := h.restartHerdr("wR")
	real := renumbered[local.WorkspaceID]

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed"}); err != nil {
		t.Fatalf("a disposal whose recorded workspace id went stale failed: %v", err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Errorf("the workspace survived its own disposal: the worktree still exists: %v", err)
	}
	if removed := h.removals(); len(removed) != 1 || removed[0] != real {
		t.Errorf("worktree.remove saw %v, want the real id %s: the teardown must reclaim "+
			"by identity, not by the stale id", removed, real)
	}
	// The reclaim is RECORDED, durably, in the provenance that carries the
	// herdr facts: the fresh id beside the stale one, so the next reader
	// recognises the drift rather than guessing at it.
	st := h.ex.storeAt(local.State)
	record, err := st.readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkspaceID != real {
		t.Errorf("the attempt record still names %q, want the reclaimed id %q", record.WorkspaceID, real)
	}
	if record.StaleWorkspaceID != local.WorkspaceID {
		t.Errorf("the stale id %q was not recorded beside the reclaimed one (stale_workspace_id = %q)",
			local.WorkspaceID, record.StaleWorkspaceID)
	}
	if note, ok := observationMentioning(h, local.State, "was stale"); !ok {
		t.Errorf("no observation records the stale-id reclaim: %v", note)
	}
}

func TestDisposeReclaimsByBranchWhenTheSnapshotCannotAttribute(t *testing.T) {
	// The snapshot without worktree blocks is the shape that forces the
	// SECOND evidence: the branch. "A workspace whose worktree path names
	// the tick is evidence; so is a branch."
	h := newHarness(t, harnessOptions{snapshotOmitsWorktrees: true})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	renumbered := h.restartHerdr("wR")
	real := renumbered[local.WorkspaceID]

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "merged and closed"}); err != nil {
		t.Fatalf("the branch evidence did not carry the attribution: %v", err)
	}
	if removed := h.removals(); len(removed) != 1 || removed[0] != real {
		t.Errorf("worktree.remove saw %v, want the real id %s", removed, real)
	}
}

func TestDisposeRefusesToRemoveAWorkspaceIdNothingTiesToThisAttempt(t *testing.T) {
	// The dangerous half of `workspace_not_found` read as success, inverted:
	// herdr STILL has a workspace under the recorded id, but nothing — not
	// the worktree, not the branch — says it is this attempt's. The id may
	// be stale onto a foreign workspace, and removing it would take
	// somebody else's work with it. This is the refusal that keeps "this
	// id is unknown" and "this workspace is gone" distinguishable.
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// The recorded id now names a foreign workspace, and the attempt's own
	// is nowhere in the session at all.
	h.mu.Lock()
	delete(h.workspaces, local.WorkspaceID)
	h.workspaces[local.WorkspaceID] = harnessWorkspace{
		path:   filepath.Join(h.repo.Root, "a-worktree-nobody-here-owns"),
		branch: "ticfac/run-elsewhere/tick-zz9/attempt-1",
		label:  "zz9",
	}
	h.mu.Unlock()

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup", KeepBranch: true})
	if err == nil {
		t.Fatal("a workspace nothing ties to this attempt was removed: the recorded id may " +
			"name somebody else's workspace")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != RefusedWorkspaceUnattributed {
		t.Errorf("the refusal was %v, want the workspace-unattributed one", err)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("the refusal must have torn down nothing; worktree.remove saw %v", removed)
	}
}

func TestDisposeNeverTouchesTheForeignWorkspaceTheStaleIdCollidesWith(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	// The stale id collides: herdr re-used the recorded id for ANOTHER
	// workspace while this attempt's lives on under a new one.
	h.foreignWorkspaceUnder(local.WorkspaceID, "w9")

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed"}); err != nil {
		t.Fatalf("the collision was not reclaimed by identity: %v", err)
	}
	if removed := h.removals(); len(removed) != 1 || removed[0] != "w9" {
		t.Errorf("worktree.remove saw %v, want exactly this attempt's real workspace w9", removed)
	}
	h.mu.Lock()
	_, foreignStillThere := h.workspaces[local.WorkspaceID]
	h.mu.Unlock()
	if !foreignStillThere {
		t.Errorf("the teardown took the foreign workspace the stale id %s had collided with", local.WorkspaceID)
	}
	if note, ok := observationMentioning(h, local.State, "names another workspace"); !ok {
		t.Errorf("no observation records that the stale id now names another workspace: %v", note)
	}
}

func TestDisposeRefusesWhenHerdrCannotBeAskedWhatExists(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	// herdr goes quiet on the one question disposal now has to ask: what
	// exists. Silence is not evidence that the recorded id is still the
	// right id — it is the same fail-closed rule the liveness question is
	// held to, applied to the identity question.
	h.server.Route(herdtest.MethodSessionSnapshot, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "herdr has nothing to say")
	})

	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "cleanup", KeepBranch: true})
	if err == nil {
		t.Fatal("an unanswerable identity question let the teardown through on the recorded id alone")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the refusal was %v, want the unknown one — the id may be stale and nobody can ask", err)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("the refusal must have torn down nothing; worktree.remove saw %v", removed)
	}
}

func TestAnInterruptedTeardownIsResumable(t *testing.T) {
	// The kill lands between the steps of a disposal — the archive is done,
	// herdr never accepted the removal — and the NEXT run must find the
	// workspace from durable state and finish the job, not shrug because
	// some intermediate pointer is gone.
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	// The kill, before herdr accepted the removal.
	h.mu.Lock()
	h.failRemoveOnce = "the caller died before herdr accepted the removal"
	h.mu.Unlock()
	first := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"})
	if first == nil {
		t.Fatal("the interrupted teardown reported success: herdr never accepted the removal")
	}
	if _, err := os.Stat(local.Worktree); err != nil {
		t.Fatalf("the worktree went away although herdr never removed it: %v", err)
	}
	// The durable pointer outlives the interrupted teardown: the attempt
	// record is what the next run re-finds the workspace from.
	if _, err := os.Stat(local.State + "/" + fileAttempt); err != nil {
		t.Fatalf("the attempt record did not outlive the interrupted teardown: %v", err)
	}

	// The next run: same handle, same durable state, nothing manual.
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"}); err != nil {
		t.Fatalf("the next run could not resume the interrupted teardown: %v", err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Errorf("the workspace survived the completed teardown: %v", err)
	}
	if removed := h.removals(); len(removed) != 1 || removed[0] != local.WorkspaceID {
		t.Errorf("worktree.remove saw %v, want the one workspace %s", removed, local.WorkspaceID)
	}
	if _, err := os.Stat(local.State + "/" + fileAttempt); err != nil {
		t.Errorf("a completed teardown removed its own pointer: %v", err)
	}
}

func TestDisposeTreatsALiveAgentOnMergedWorkAsAnOrdinaryCase(t *testing.T) {
	// "Still live on a merged branch" was reported as a note to step around
	// on this epic's own wave 1 — all three of ours produced it. A settled,
	// live agent on merged work is the NORMAL end-of-run state, and the
	// teardown handles it in a defined order (revoke, stop with the
	// workspace removal), recorded as an ordinary observation.
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)
	h.setStatus("idle") // live, settled, on merged work

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed"}); err != nil {
		t.Fatalf("a live, settled agent on merged work was not an ordinary disposal case: %v", err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree still exists after disposal: %v", err)
	}
	if note, ok := observationMentioning(h, local.State, "worktree.remove tears it down with the workspace"); !ok {
		t.Errorf("the teardown did not record the settled agent's fate as part of the ordinary order: %v", note)
	}
}

func TestTheAttemptRecordOutlivesTheTeardownAndCarriesTheProvenance(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", local.Branch)

	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"}); err != nil {
		t.Fatal(err)
	}
	// Disposal is idempotent and removes NO record of its own: the attempt
	// record is the durable pointer the next run re-finds the workspace
	// from, and PurgeState is the separate, explicit step that retires it.
	st := h.ex.storeAt(local.State)
	record, err := st.readAttempt()
	if err != nil {
		t.Fatalf("the attempt record did not outlive the teardown: %v", err)
	}
	if record.WorkspaceID == "" {
		t.Error("the record that outlives the teardown names no workspace: the pointer died with the thing it points at")
	}
	// The substrate provenance, d9m's half and this tick's: the workspace id
	// sits beside the herdr protocol and server version, so a stale id can
	// be recognised as stale rather than guessed at.
	if record.ServerVersion == "" || record.Protocol == 0 {
		t.Errorf("the provenance lost the herdr facts: server %q, protocol %d",
			record.ServerVersion, record.Protocol)
	}
}

// observationMentioning finds the first observation whose detail contains the
// given text, for asserting what the run's own journal recorded.
func observationMentioning(h *harness, state, text string) ([]subprocess.Observation, bool) {
	st := h.ex.storeAt(state)
	observations, _ := st.observationsFrom("")
	for _, obs := range observations {
		if strings.Contains(obs.Detail, text) {
			return observations, true
		}
	}
	return observations, false
}
