package herdr

// THE x6j FIXTURES: a substrate failure is an OPERATIONAL error and never a
// verdict about the work.
//
// ticks learned this line the hard way (collect/collect.go:108-109, the
// specification this package holds): "an error means the check could not be
// performed ... never 'the worker failed', which is a verdict." Verdicts come
// only from durable evidence — git, the report, and this executor's own
// settlement records — and from herdr's POSITIVE answers, recorded durably at
// the moment they were observed.
//
// The tests here take the two shapes a substrate failure arrives in and pin
// that neither can decide anything about the work:
//
//   - SILENCE: every herdr call fails (the settling fixture below), a
//     protocol refusal at construction, an unreadable attempt record across
//     an upgrade.
//   - DRIFT: herdr renames a code this executor tolerates (agent_pane_busy,
//     agent_prompt_stalled). A rename must never flip a tolerated condition
//     into a verdict — every unrecognized code lands on the operational
//     side of the classification in classify.go, and no content gate reads
//     the pane back.
//
// What "decides nothing" means mechanically, asserted below for every
// operation: the failure rejects no tick (no error carries a verdict — a
// settled/failed/collect refusal — about work nobody could look at), tears
// no attempt down (no workspace is removed, no worktree deleted), and
// deletes no branch and no state.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// failEveryRoute makes EVERY herdr method answer an error — a substrate that
// cannot be asked anything. invalid_request is deliberately used and NOT
// agent_not_found: not-found is herdr's POSITIVE answer that nobody is there
// (a verdict-bearing fact, per classify.go), and a fixture about operational
// silence must not smuggle a positive answer in through the error code.
func (h *harness) failEveryRoute() {
	fail := func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "the substrate answers nothing")
	}
	for _, method := range []string{
		herdtest.MethodPing,
		herdtest.MethodSessionSnapshot,
		herdtest.MethodWorktreeCreate,
		herdtest.MethodWorktreeList,
		herdtest.MethodWorktreeRemove,
		herdtest.MethodWorkspaceFocus,
		herdtest.MethodAgentStart,
		herdtest.MethodAgentPrompt,
		herdtest.MethodAgentSendKeys,
		herdtest.MethodAgentWait,
		herdtest.MethodAgentList,
		herdtest.MethodAgentGet,
		herdtest.MethodPaneRead,
		"pane.wait_for_output",
		herdtest.MethodPaneReportMetadata,
		herdtest.MethodWorkspaceReportMetadata,
		herdtest.MethodEventsSubscribe,
		herdtest.MethodEventsWait,
		herdtest.MethodNotificationShow,
	} {
		h.server.Route(method, fail)
	}
}

// assertNotAVerdict pins that err rejects no tick: it is either a plain
// operational error (not a Refusal at all) or a refusal that HOLDS —
// liveness_unknown or live, the two reasons the reconciler maps to
// attempt_unaddressed and a person — never a verdict about the work
// (settled/failed/collect-shaped reasons).
func assertNotAVerdict(t *testing.T, where string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected the operational failure, got success", where)
	}
	refusal, ok := subprocess.AsRefusal(err)
	if !ok {
		return // a plain operational error: no verdict is carried at all
	}
	switch refusal.Reason {
	case subprocess.RefusedUnknown, subprocess.RefusedLive:
		return // the hold: nobody can say, so a person decides
	}
	t.Errorf("%s: the failure was carried as refusal %q (%s): an operational "+
		"failure must never speak in verdicts about the work", where, refusal.Reason, refusal.Message)
}

// assertNothingTornDown pins that the attempt is exactly where it was: the
// worktree on disk, the branch in git, the state directory with its attempt
// record, and nothing routed to worktree.remove.
func (h *harness) assertNothingTornDown(t *testing.T, where string, local *herdrHandle) {
	t.Helper()
	if _, err := os.Stat(local.Worktree); err != nil {
		t.Errorf("%s: the worktree is gone (%v): an operational failure tore the attempt down", where, err)
	}
	if head := headOf(h.repo.Dir, local.Branch); head == "" {
		t.Errorf("%s: the branch %s was deleted: an operational failure must never delete a branch", where, local.Branch)
	}
	if _, err := os.Stat(filepath.Join(local.State, fileAttempt)); err != nil {
		t.Errorf("%s: the attempt record is gone (%v): the diagnostic state a substrate failure leaves must stay", where, err)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("%s: worktree.remove accepted %v: an operational failure tore a workspace down", where, removed)
	}
}

// TestEveryHerdrCallFailingDecidesNothing is the fixture that settles the
// tick: EVERY herdr call fails, and no operation in the five — start, adopt,
// inspect, cancel, dispose, collect — turns that into a verdict, a teardown
// or a deletion.
func TestEveryHerdrCallFailingDecidesNothing(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// The substrate goes silent for EVERY method, mid-run.
	h.failEveryRoute()

	// ---- inspect: nobody can be asked, so nobody is declared dead --------
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("inspect under a silent substrate returned an error: %v", err)
	}
	if status.State != subprocess.StateLost {
		t.Errorf("state = %s, want lost: a substrate that cannot be asked says nothing about the job", status.State)
	}
	if status.Terminal {
		t.Error("lost is terminal: an attempt nobody can address is held for a person, never classified dead")
	}

	// ---- adopt: the same identity is refused, held for a person ---------
	_, err = h.start("t1")
	assertNotAVerdict(t, "a re-Start of an attempt nobody can address", err)
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the re-Start was %v, want the liveness-unknown hold", err)
	}

	// ---- dispose: the unanswered liveness question refuses the removal ---
	disposeErr := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "the run stopped mid-flight"})
	assertNotAVerdict(t, "dispose against a silent substrate", disposeErr)
	if refusal, ok := subprocess.AsRefusal(disposeErr); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the dispose was %v, want the liveness-unknown refusal", disposeErr)
	}
	h.assertNothingTornDown(t, "after the refused dispose", local)

	// ---- collect: no report, no settlement — held, never collected ------
	// The worker did work but never reported, and no leg ever settled the
	// attempt: every herdr call that could have failed. Collect refuses
	// rather than guessing — a missing result here would be a verdict minted
	// out of the substrate's silence.
	doCommitOnly := func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(local.Worktree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun(t, local.Worktree, "git", "add", "hello.txt")
		mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "work, no report")
	}
	doCommitOnly(t)
	_, collectErr := h.ex.CollectDetail(handle)
	assertNotAVerdict(t, "a collect with no report and no settlement", collectErr)
	if refusal, ok := subprocess.AsRefusal(collectErr); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the collect was %v, want the liveness-unknown hold", collectErr)
	}
	if _, err := os.Stat(filepath.Join(local.State, fileResult)); err == nil {
		t.Error("the refused collect persisted a result anyway: a held attempt must not carry a collected verdict")
	}

	// ---- collect again: durable evidence answers what silence cannot ----
	// The worker's report IS durable evidence. With every herdr call still
	// failing, collect reads the report and the branch and answers
	// ready-to-merge — the seam tick 2xu asserts from its side, present
	// here because this fixture owns the every-call-fails substrate.
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("## Report\n\nDid the thing.\n\nSTATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("a collect with a report failed under a silent substrate: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s with every herdr call failing, want ready-to-merge from the durable evidence alone",
			collected.Verdict)
	}
	// And inspect agrees from the report, without asking herdr anything.
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateSucceeded {
		t.Errorf("state = %s, want succeeded from the report: durable evidence outranks a silent substrate", status.State)
	}

	// ---- cancel: the revocation is durable, the interrupt is the error ---
	cancelErr := func() error {
		_, err := h.ex.Cancel(handle)
		return err
	}()
	if cancelErr == nil {
		t.Fatal("a cancel that could not interrupt through a silent substrate reported success: the agent may still be spending")
	}
	assertNotAVerdict(t, "cancel against a silent substrate", cancelErr)
	if _, err := os.Stat(filepath.Join(local.State, fileCancel)); err != nil {
		t.Errorf("the revocation was not recorded: substrate silence must not leave a re-issuable dispatch (%v)", err)
	}

	// ---- a fresh dispatch into the same silence fails operationally ------
	creates := h.server.CountMethod(herdtest.MethodWorktreeCreate)
	_, err = h.start("t2")
	assertNotAVerdict(t, "a fresh dispatch into a silent substrate", err)
	if h.server.CountMethod(herdtest.MethodWorktreeCreate) != creates+1 {
		t.Errorf("the fresh dispatch did not reach worktree.create: the failure is not the one this test pins")
	}
	t2Branch := "ticfac/run-harness/tick-t2/attempt-1"
	if branchExists(h.repo.Dir, t2Branch) {
		t.Errorf("branch %s exists after a failed dispatch: nothing the substrate failed to reach may leave work behind",
			t2Branch)
	}

	// ---- and the earlier attempt is exactly where it was ---------------
	h.assertNothingTornDown(t, "after every operation under total substrate failure", local)
}

// TestAProtocolRefusalIsOperational pins that the client's fail-closed
// protocol refusal — a herdr too old for this client to speak, the refusal
// the version work raised — is an operational failure at the executor: the
// constructor refuses, nothing is created, nothing is deleted, and no
// verdict is carried.
func TestAProtocolRefusalIsOperational(t *testing.T) {
	repo := newRepo(t, "repo")
	s := herdtest.New(t, herdtest.Config{
		Strict: true, Version: "0.7.0", Protocol: int(client.MinProtocolVersion - 1),
	})
	root := t.TempDir()

	_, err := New(Options{
		Repo:       repo.Dir,
		StateDir:   filepath.Join(root, "state"),
		SocketPath: s.Path(),
		Remote:     "origin",
	})
	if err == nil {
		t.Fatal("a below-floor protocol was accepted: the client fails closed by contract")
	}
	var mismatch *client.ProtocolMismatchError
	if !errors.As(err, &mismatch) {
		t.Errorf("the refusal was %v, want the typed protocol mismatch so a caller reports the upgrade it needs", err)
	}
	assertNotAVerdict(t, "executor construction under a protocol mismatch", err)
	// Nothing was created and nothing deleted: the state root is empty.
	entries, readErr := os.ReadDir(filepath.Join(root, "state"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("the refused constructor left %d entries in the state root: it created nothing", len(entries))
	}
	if branchExists(repo.Dir, "main") != true {
		t.Error("the sanity check lost the base branch")
	}
}

// TestTheExecutorDemandsNoCapability pins the other refusal the version work
// raised — a server that does not advertise a capability — to the
// operational side, from below: the executor's five operations place NO
// capability demand on the server, so a capability refusal can never arise
// from them, and a herdr advertising nothing at all runs a whole attempt.
func TestTheExecutorDemandsNoCapability(t *testing.T) {
	// A non-default version makes the fake's ping answer WITHOUT a
	// capabilities block: a server that advertises nothing.
	h := newHarness(t, harnessOptions{serverVersion: "0.9.0", serverProtocol: int(client.ProtocolWarnVersion)})
	if h.ex.client.ServerInfo().Capabilities != nil {
		t.Fatal("the harness's server advertised capabilities: the fixture is not capability-free")
	}
	if h.ex.client.ServerInfo().Capabilities.Has(client.CapabilityLiveHandoff) ||
		h.ex.client.ServerInfo().Capabilities.Has(client.CapabilityDetachedServerDaemon) {
		t.Fatal("the fixture must prove the operations against a server with NO capabilities")
	}

	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a start against a capability-free server failed: %v", err)
	}
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("state = %s, want running: inspect demands no capability", status.State)
	}
	h.doWork(t, handle, "STATUS: DONE")
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("a collect against a capability-free server failed: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s, want ready-to-merge: no leg of an attempt needs a capability the server lacks", collected.Verdict)
	}
	mustRun(t, h.repo.Dir, "git", "push", "--quiet", "origin", collected.Result.Source.WriteRef)
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "merged and closed"}); err != nil {
		t.Fatalf("a dispose against a capability-free server failed: %v", err)
	}
}

// TestARenamedPaneBusyCodeIsAnOperationalFailure is the rename hazard ticks
// hit live: herdr renamed agent_pane_busy and a tolerated startup race
// became a hard failure. Here the rename lands on the operational side —
// the classification in classify.go puts every code this executor does not
// POSITIVELY know on the failure side of a call that cleans up nothing,
// tears nothing down, and never speaks in verdicts.
func TestARenamedPaneBusyCodeIsAnOperationalFailure(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// The renamed code: herdr says what agent_pane_busy used to say, under
	// a name this build has never heard.
	h.server.Route(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_pane_unready", "the pane is not an interactive shell yet")
	})
	_, err := h.start("t1")
	assertNotAVerdict(t, "a launch answered by a code this build has never heard", err)
	if _, ok := subprocess.AsRefusal(err); ok {
		t.Fatalf("a renamed startup race arrived as a refusal: %v", err)
	}

	// The spawn lesson, held: nothing is cleaned up on a substrate
	// failure. The worktree, the branch and the record of the failed
	// launch stay as diagnostic state for a person to read. The handle
	// never existed (Start failed), so assert on the durable state the
	// failed launch left behind.
	spec := h.spec("run-harness/tick-t1/attempt-1", "t1")
	dir := h.ex.stateDirFor(spec.JobID, h.ex.opts.Attempt)
	st := h.ex.storeAt(dir)
	record, readErr := st.readAttempt()
	if readErr != nil {
		t.Fatalf("the failed launch left no diagnostic record: %v", readErr)
	}
	if record.LaunchConfirmed {
		t.Error("the record claims a confirmed launch: the failure must be recorded, not rounded away")
	}
	if _, err := os.Stat(record.Worktree); err != nil {
		t.Errorf("the worktree herdr created was cleaned up on the substrate failure: %v", err)
	}
	if head := headOf(h.repo.Dir, record.Branch); head == "" {
		t.Errorf("branch %s was deleted by a substrate failure: nothing that failed to launch may be erased", record.Branch)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("worktree.remove accepted %v on the failed launch's behalf", removed)
	}

	// And the failed attempt is never redispatched as THIS attempt: a
	// retry is a new attempt number, refused here as settled.
	_, err = h.start("t1")
	if err == nil {
		t.Fatal("the never-launched attempt started again: a retry is a new attempt number, not this one")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedSettled {
		t.Errorf("the re-Start was %v, want the settled refusal: this attempt is spent, not re-run", err)
	}
}

// TestAnyPromptFailureIsAnObservation is the other rename hazard, and the
// gate-content one beside it: herdr renamed agent_prompt_stalled, or the
// pane's rendering changed, or the text never landed — and none of it may
// turn into "the agent cannot work". There is no content gate reading the
// pane back in this executor; every prompt failure is an observation the
// wait judges, and Start succeeds.
func TestAnyPromptFailureIsAnObservation(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// The renamed stall code, and a confirmation wait that fails with a
	// code this build has never heard either.
	h.server.Route(herdtest.MethodAgentPrompt, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_prompt_never_landed", "herdr saw no state change")
	})
	h.server.Route(herdtest.MethodAgentWait, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "wait_broken", "the wait could not run")
	})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a prompt failure failed the start: %v — a rendering or truncation change must never "+
			"turn into 'the agent cannot work'", err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	_, record, err := local.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if !record.LaunchConfirmed {
		t.Error("the launch was confirmed and the record must say so: only the dispatch is unconfirmed")
	}
	if record.DispatchConfirmed {
		t.Error("the record claims a confirmed dispatch: the wait never observed working")
	}
	// The failure is in the observation stream, where a person reads it.
	st := h.ex.storeAt(local.State)
	observations, _ := st.observationsFrom("")
	joined := ""
	for _, obs := range observations {
		joined += obs.Detail + "\n"
	}
	if !strings.Contains(joined, "agent_prompt_never_landed") {
		t.Errorf("the prompt failure is missing from the observation stream:\n%s", joined)
	}
	// And the attempt is live and addressable: inspect answers from herdr,
	// which this fixture left working.
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Terminal {
		t.Errorf("state = %s: an unconfirmed dispatch is a fact, not a verdict about the work", status.State)
	}
}

// TestAnUnreadableAttemptRecordIsHeld is resume across an upgrade: a later
// leg of the run — a ticfac built after a schema change, or a record half
// rewritten by a crash — finds an attempt record it cannot address. It is
// HELD for a person, never redispatched: a fresh worktree must not be
// created behind a record nobody could read.
func TestAnUnreadableAttemptRecordIsHeld(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	// The upgrade: the record this build would write is not the record on
	// disk — a schema version from another release, written raw so the
	// production read path is the thing that refuses it.
	if err := os.WriteFile(filepath.Join(local.State, fileAttempt),
		[]byte(`{"schema_version":99,"job_id":"run-harness/tick-t1/attempt-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	creates := h.server.CountMethod(herdtest.MethodWorktreeCreate)
	_, err = h.start("t1")
	assertNotAVerdict(t, "a re-Start over an unreadable attempt record", err)
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the re-Start was %v, want the liveness-unknown hold: a record this leg cannot address is "+
			"held for a person", err)
	}
	if h.server.CountMethod(herdtest.MethodWorktreeCreate) != creates {
		t.Error("an unreadable attempt record was redispatched behind herdr's back: no worktree may be " +
			"created for an attempt nobody could read")
	}
	h.assertNothingTornDown(t, "after the held re-Start", local)
}
