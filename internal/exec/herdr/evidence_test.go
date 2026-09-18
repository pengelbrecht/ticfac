package herdr

// THE VERDICT COMES FROM DURABLE EVIDENCE, NEVER FROM HERDR — tick 2xu's
// test of record, and the one a herdr upgrade can never lose to.
//
// Phase 1 proved the shape on ticks' own collect package: an error means the
// check could not be performed, never "the worker failed", and verdicts come
// only from git and the result file. This executor inherits it BY
// CONSTRUCTION — collect.go imports no client, dials no socket — and this
// file is the mechanical proof, in three parts:
//
//   - the test of record: a fixture in which EVERY herdr call returns an
//     error, driven through a completed attempt, still collects the correct
//     verdict from the report and the branch;
//   - the same fixture cannot flip a FAILING verdict either: an erroring
//     herdr is as powerless to manufacture success as to hide it;
//   - a herdr that is up and LYING — every answer success-shaped, every
//     story contradicting the branch — changes nothing either, because
//     collect never asks.
//
// The zero-calls assertion in each test is the guarantee itself: herdr
// answers "is the agent alive", never "did the work succeed", and a collect
// that consulted it even once would show up here as a counted call, whatever
// it then did with the answer. A rendering change, a renamed status word or a
// dropped connection can cost a dispatch; none of them can cost a result.
//
//   - the fourth part is the one tick 3kx added: the same all-errors
//     fixture, collected AFTER teardown removed the worktree, so the
//     doctrine also covers the post-teardown report read (tick 35h's
//     archive fallback) — the path a reviewer's mutation once rode
//     through every test green, because no fixture there counted calls.

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// everyHerdrMethod is every method the wire has, so "every herdr call
// returns an error" means all of them and not the subset collect is believed
// to use — the belief is the thing under test.
var everyHerdrMethod = []string{
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
	herdtest.MethodEventsSubscribe,
	herdtest.MethodEventsWait,
	herdtest.MethodPaneReportMetadata,
	herdtest.MethodWorkspaceReportMetadata,
	herdtest.MethodNotificationShow,
}

// errEveryCall is the code the all-errors fixture answers with. Not one of
// the codes a caller might treat specially: the fixture's point is that the
// call happened and failed, not which failure it was.
const errEveryCall = "substrate_unavailable"

// failEveryHerdrCall routes every method the wire has to an error envelope —
// herdr is UP, listening, and has nothing to say about anything.
func (h *harness) failEveryHerdrCall() {
	for _, method := range everyHerdrMethod {
		h.server.Route(method, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
			return herdtest.RespondErr(w, req.ID, errEveryCall, "the substrate has nothing to say")
		})
	}
}

// probeCall dials the fake server by hand and issues one call for method,
// returning the decoded reply envelope. It is how the fixture proves it is
// honest: the test asserts herdr really is erroring rather than assuming it,
// because a fixture that quietly kept answering would prove nothing.
func probeCall(t *testing.T, socketPath, method string) map[string]any {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("probe %s: dial: %v", method, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := json.Marshal(map[string]any{"id": "probe-" + method, "method": method, "params": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.Fatalf("probe %s: write: %v", method, err)
	}
	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("probe %s: read: %v", method, err)
	}
	var reply map[string]any
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("probe %s: the reply is not an envelope: %v (%s)", method, err, raw)
	}
	return reply
}

// assertEveryCallErrors is the fixture's honesty check: every method on the
// wire must answer an error envelope, and each probe lands on the server's
// own request record, so the count the verdict assertion compares against
// starts from a fixture that is known to be live and known to be erroring.
func assertEveryCallErrors(t *testing.T, h *harness) {
	t.Helper()
	for _, method := range everyHerdrMethod {
		reply := probeCall(t, h.server.Path(), method)
		errObj, ok := reply["error"].(map[string]any)
		if !ok {
			t.Errorf("probe %s was answered %v, want an error envelope: an all-errors fixture that "+
				"still answers something has not been built", method, reply)
			continue
		}
		if errObj["code"] != errEveryCall {
			t.Errorf("probe %s was answered error code %v, want %s", method, errObj["code"], errEveryCall)
		}
	}
}

// TestCollectAnswersFromDurableEvidenceWhenEveryHerdrCallErrors is THE TEST
// OF RECORD for tick 2xu. A completed attempt — real commits on the attempt
// branch, a real report at the executor-owned path, the executor's own
// inspect already answering terminal — and then a substrate in which every
// herdr call returns an error. Collect still reads the verdict from the
// report and the branch, and makes zero calls to do it: if any herdr
// response could change a verdict, this fixture is where it would have to
// show, and it has nowhere to show because nothing is asked.
func TestCollectAnswersFromDurableEvidenceWhenEveryHerdrCallErrors(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// The worker's half: one commit on the attempt branch, the report at the
	// path the executor owns.
	h.doWork(t, handle, "STATUS: DONE")

	// Driven through a COMPLETED attempt: the executor's own inspect
	// answers terminal from the report, before herdr is broken at all.
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateSucceeded || !status.Terminal {
		t.Fatalf("the attempt inspects as %s (terminal %t), want a completed attempt: the fixture "+
			"must drive one, not assume one", status.State, status.Terminal)
	}

	// herdr goes wrong for EVERY call: up, listening, erroring everything.
	h.failEveryHerdrCall()
	assertEveryCallErrors(t, h)

	// Collect. The verdict must come from the durable layer alone.
	callsBefore := len(h.server.Methods())
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect failed with herdr erroring every call: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s with every herdr call erroring, want ready-to-merge: the verdict comes "+
			"from the report and the branch, never from the substrate", collected.Verdict)
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome = %s, want succeeded", collected.Result.Outcome)
	}
	if collected.Result.RoleResult == nil || collected.Result.RoleResult.Status != "DONE" {
		t.Errorf("the report's status did not reach the role-result envelope: %+v", collected.Result.RoleResult)
	}
	if collected.Result.Source.Commits != 1 {
		t.Errorf("commits = %d, want the one the worker made beyond the recorded base", collected.Result.Source.Commits)
	}
	if collected.Message != "" {
		t.Errorf("a ready-to-merge attempt collected a message (%q): the message vocabulary says "+
			"ready-to-merge says nothing", collected.Message)
	}

	// THE GUARANTEE, mechanically: collect made zero herdr calls. Not "it
	// ignored the errors" — it never heard them. A refactor that lets one
	// herdr answer into a verdict shows up here as a counted call, whatever
	// it does with the answer.
	if got := len(h.server.Methods()); got != callsBefore {
		t.Errorf("collect made %d herdr call(s) (%v): the verdict is derivable from the report, the "+
			"branch and the settlement record, so no herdr answer — error or success — can reach it",
			got-callsBefore, h.server.Methods()[callsBefore:])
	}
}

// TestCollectAfterTeardownAnswersFromDurableEvidenceWhenEveryHerdrCallErrors
// is the all-errors fixture on the path tick 35h added to readReport: collect
// AFTER the worktree is removed, when the report read falls back to the
// archived copy beside the attempt record. Until tick 3kx the doctrine's
// fixture stopped at the worktree copy — collect before teardown — so the
// post-teardown read had no zero-calls proof at all: a mutation that made THAT
// read consult the substrate passed every test in this package, because the
// one test that drove the path (TestTheReportSurvivesTheWorktree) never
// counted calls. The doctrine has to hold on EVERY path a verdict is minted
// on, so the fixture drives the ordinary end-of-run order — collect, then the
// executor's own teardown — and collects again with every herdr call
// erroring: the verdict must come from the archive, the branch and the
// settlement record, and nothing may dial herdr to get it.
func TestCollectAfterTeardownAnswersFromDurableEvidenceWhenEveryHerdrCallErrors(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// The ordinary end-of-run order: collect first, while the worktree is
	// still there — the verdict is minted and persisted, and the report is
	// archived beside the attempt record — then the executor's own teardown
	// removes the worktree, keeping the branch (its commits are on no remote
	// yet).
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatalf("the first collect failed: %v", err)
	}
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{KeepBranch: true}); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Fatalf("the worktree still exists after teardown: %v", err)
	}
	// The fixture must be honest about which read the collect below makes:
	// the worktree copy is GONE, so the only report left is the archive.
	if _, err := os.Stat(local.ResultPath); !os.IsNotExist(err) {
		t.Fatalf("the worktree's report copy is still at %s: the collect below would read the worktree, not the archive", local.ResultPath)
	}
	if _, err := os.Stat(filepath.Join(local.State, fileReportArchive)); err != nil {
		t.Fatalf("the report was not archived beside the attempt record: %v", err)
	}

	// herdr goes wrong for EVERY call: up, listening, erroring everything.
	h.failEveryHerdrCall()
	assertEveryCallErrors(t, h)

	// Collect after teardown. The verdict must come from the durable layer
	// alone — the archive, the branch, the settlement record.
	callsBefore := len(h.server.Methods())
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect after teardown failed with herdr erroring every call: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s with every herdr call erroring after teardown, want ready-to-merge: the post-"+
			"teardown report read is archive work, and the verdict comes from the report and the branch, never "+
			"from the substrate", collected.Verdict)
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome = %s, want succeeded", collected.Result.Outcome)
	}
	if !collected.HasReport || collected.Report.Status != "DONE" {
		t.Errorf("the archived report did not reach the collection (has=%t status=%q): the worktree copy is "+
			"gone, so this is the archive read or nothing", collected.HasReport, collected.Report.Status)
	}
	if collected.Result.RoleResult == nil || collected.Result.RoleResult.Status != "DONE" {
		t.Errorf("the archived report's status did not reach the role-result envelope: %+v", collected.Result.RoleResult)
	}
	if collected.Result.Source.Commits != 1 {
		t.Errorf("commits = %d, want the one the worker made beyond the recorded base", collected.Result.Source.Commits)
	}

	// THE GUARANTEE, mechanically, on the post-teardown path too: collect
	// made zero herdr calls. This is the assertion the reviewer's mutation
	// — a substrate call on the archived-report branch of readReport — rode
	// past: no fixture counted calls there. It does now.
	if got := len(h.server.Methods()); got != callsBefore {
		t.Errorf("collect after teardown made %d herdr call(s) (%v): the archived report is this executor's "+
			"own durable copy, and no herdr answer — error or success — belongs anywhere in the read of it",
			got-callsBefore, h.server.Methods()[callsBefore:])
	}
}

// TestEveryHerdrCallErroringCannotFlipAFailedVerdictEither is the other
// direction of the same guarantee: the all-errors fixture must not be able
// to manufacture a success either. A worker that reported DONE over an empty
// branch is a FAILING attempt, and a herdr that is erroring has no more
// power to hide that than to overturn a real success — for the same reason:
// it is never asked.
func TestEveryHerdrCallErroringCannotFlipAFailedVerdictEither(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// A report but no commit: the worker SAID done and did nothing.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("STATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.failEveryHerdrCall()
	assertEveryCallErrors(t, h)

	callsBefore := len(h.server.Methods())
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect failed with herdr erroring every call: %v", err)
	}
	if collected.Verdict != subprocess.VerdictNoCommits {
		t.Errorf("verdict = %s with every herdr call erroring, want no-commits: an erroring substrate "+
			"is as powerless to flip a failing verdict as a succeeding one", collected.Verdict)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome = %s, want failed", collected.Result.Outcome)
	}
	if got := len(h.server.Methods()); got != callsBefore {
		t.Errorf("collect made %d herdr call(s) on a failing attempt too: the verdict is closed "+
			"vocabulary over durable facts, and herdr holds none of them", got-callsBefore)
	}
}

// TestHerdrAnswersCannotChangeAVerdict breaks herdr the other way: not
// erroring, but LYING. Every answer is success-shaped and every story
// contradicts the durable evidence — the agent is in state `error`, the pane
// reads as a worker that gave up. If a herdr response could reach a verdict,
// it would be here, with the substrate freely inventing failure; the verdict
// must stay what the report and the branch say it is, and collect must ask
// nothing. This is the shape a herdr UPGRADE most plausibly produces — a
// renamed status word, a new rendering — and the shape this tick exists to
// make survivable.
func TestHerdrAnswersCannotChangeAVerdict(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	// herdr is up and answers everything, and every answer tells a story of
	// failure: the agent is in state `error`, the pane's text reads as a
	// worker that gave up mid-tick.
	h.setStatus("error")
	h.server.SetPaneTexts("the worker hit a fatal error and gave up")

	callsBefore := len(h.server.Methods())
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect failed under a lying substrate: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s under a herdr that claims the worker failed, want ready-to-merge: no "+
			"herdr answer — least of all a success-shaped one — is a verdict", collected.Verdict)
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome = %s, want succeeded", collected.Result.Outcome)
	}
	if got := len(h.server.Methods()); got != callsBefore {
		t.Errorf("collect made %d herdr call(s) it could have believed: the lies never reached "+
			"the verdict because nothing dialed to hear them", got-callsBefore)
	}
}
