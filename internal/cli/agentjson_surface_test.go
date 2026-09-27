package cli

// The --json surfaces this tick ADDED, each checked on the property the
// acceptance names: ONE document on stdout that parses, names its versioned
// schema, agrees with the exit code — and nothing the document needs lands
// on stderr. The fixtures are the ones each command's own tests use, so
// these are the same worlds the prose surfaces are proven in.
import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// jsonAnswer runs one invocation with --json and returns its stdout parsed
// as one document, its stderr, and its exit code. Failing the parse is the
// test: stdout is the document's alone, and anything else there is the
// failure the test exists to catch.
func jsonAnswer(t *testing.T, args []string) (doc map[string]any, stderrOut string, code int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = Run(args, &stdout, &stderr)
	if code == 0 {
		if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
			t.Fatalf("stdout with --json is not one parseable document (exit %d):\n%s\n--stderr--\n%s",
				code, stdout.String(), stderr.String())
		}
	} else if strings.TrimSpace(stdout.String()) != "" {
		// A refused command owes prose on stderr and its code; stdout
		// staying empty is the half an agent's parse depends on. A failure
		// DOCUMENT (state: failed) is allowed — it still has to parse.
		if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
			t.Fatalf("stdout with --json is neither empty nor one document (exit %d):\n%s",
				code, stdout.String())
		}
	}
	return doc, stderr.String(), code
}

// mustSchema reads the document's schema field, refusing silence.
func mustSchema(t *testing.T, doc map[string]any, want string) {
	t.Helper()
	if got := doc["schema"]; got != want {
		t.Fatalf("the document's schema is %v, want %q", got, want)
	}
}

// doctor --json: the checks as fields, the state word agreeing with the
// exit code, each missing thing still carrying its fix. The seams are
// package state, so this test is serial.
func TestDoctorJSONAnswersTheChecks(t *testing.T) {
	repo := doctorFixture(t, false)

	saveDoctorSeams(t, "tk", false)

	doc, stderr, code := jsonAnswer(t, []string{"doctor", "--json", "--repo", repo})
	if code != exitGeneric {
		t.Fatalf("a doctor with a missing line exited %d, want %d", code, exitGeneric)
	}
	mustSchema(t, doc, "ticfac.doctor.v1")
	if doc["state"] != agentStateFailed {
		t.Errorf("the state word is %v, want %q — the exit code and it must agree", doc["state"], agentStateFailed)
	}
	checks, ok := doc["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("the document carries no checks:\n%v\n--stderr--\n%s", doc, stderr)
	}
	for _, c := range checks {
		check := c.(map[string]any)
		if check["name"] == "tk" {
			if check["ok"] != false || check["fix"] == "" || check["fix"] == nil {
				t.Errorf("the missing tk check does not carry its problem and fix: %v", check)
			}
		}
	}

	// The all-ok mirror: state done, exit 0.
	saveDoctorSeams(t, "", false)
	doc, _, code = jsonAnswer(t, []string{"doctor", "--json", "--repo", repo})
	if code != exitSuccess {
		t.Fatalf("an all-ok doctor exited %d, want 0: %v", code, doc)
	}
	if doc["state"] != agentStateDone {
		t.Errorf("the all-ok state word is %v, want done", doc["state"])
	}
}

// init --json: the answers and the files, one document, nothing else on
// stdout.
func TestInitJSONAnswersWhatItWrote(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	doc, stderr, code := jsonAnswer(t, []string{"init", "--json", "--yes", "--substrate", "local",
		"--runner", "claude", "--model", "sonnet", "--gate", "go test ./...", "--repo", repo})
	if code != exitSuccess {
		t.Fatalf("init --json exited %d: %s", code, stderr)
	}
	mustSchema(t, doc, "ticfac.init.v1")
	if doc["substrate"] != "local" || doc["runner"] != "claude" || doc["model"] != "sonnet" {
		t.Errorf("the resolved answers are wrong: %v", doc)
	}
	// The close-out answer is in the document too (tick 6vp): the guess —
	// here no origin, so none — so an agent reads what the repository was
	// left holding on.
	if doc["closeout"] != "none" {
		t.Errorf("the document's close-out answer is %v, want none (no origin to guess pr from)", doc["closeout"])
	}
	files, ok := doc["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("the document does not name the files it wrote:\n%v", doc)
	}
	for _, f := range files {
		file := f.(map[string]any)
		if _, err := os.Stat(filepath.Join(repo, file["name"].(string))); err != nil {
			t.Errorf("the document names %v, which was not written: %v", file["name"], err)
		}
	}
	gate, ok := doc["gate"].([]any)
	if !ok || len(gate) != 1 || gate[0].(map[string]any)["command"] != "go test ./..." {
		t.Errorf("the document does not carry the gate: %v", doc["gate"])
	}
}

// findings --json: the drafts as one document, the same shape triage lists
// and decides by. Serial, not parallel: newFindingsRepo pins the test's git
// config environment (GIT_CONFIG_GLOBAL/GIT_SYSTEM at os.DevNull) so the
// fixture is the same on every host, and a test that does that cannot be
// parallel.
func TestFindingsJSONListsTheDrafts(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding(triageKey("d34db33f"), ""))

	doc, stderr, code := jsonAnswer(t, []string{"findings", "--json", "--repo", repo, "qeu"})
	if code != exitSuccess {
		t.Fatalf("findings --json exited %d: %s", code, stderr)
	}
	mustSchema(t, doc, "ticfac.findings.v1")
	if doc["untriaged"] != float64(1) {
		t.Errorf("the document says %v untriaged, want 1", doc["untriaged"])
	}
	findings, ok := doc["findings"].([]any)
	if !ok || len(findings) != 1 || findings[0].(map[string]any)["key"] != triageKey("d34db33f") {
		t.Fatalf("the document does not carry the draft:\n%v", doc)
	}
}

// events --json: the standing feed as one document, each event inside it the
// same versioned JSON object the plain path prints as JSONL; --json with
// --follow is the refusal that keeps a stream from being called a document.
func TestEventsJSONIsOneDocumentAndRefusesAFollow(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "a1", &attempt, "dispatched", "attempt 1 started"))

	doc, stderr, code := jsonAnswer(t, []string{"events", "--json", "--repo", repo, "r-1"})
	if code != exitSuccess {
		t.Fatalf("events --json exited %d: %s", code, stderr)
	}
	mustSchema(t, doc, "ticfac.events.v1")
	events, ok := doc["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("the document does not carry the feed:\n%v", doc)
	}
	event := events[0].(map[string]any)
	if event["stage"] != "dispatched" || event["tick_id"] != "a1" {
		t.Errorf("the event inside the document lost its fields: %v", event)
	}

	_, stderr, code = jsonAnswer(t, []string{"events", "--json", "--follow", "--repo", repo, "r-1"})
	if code != exitUsage {
		t.Errorf("--json --follow exited %d, want %d: %s", code, exitUsage, stderr)
	}
}

// run-epic --json's result document: the state word agrees with the exit
// code, and a refusal carries its REASON CLASS — the stable vocabulary an
// agent branches on, never the prose message.
func TestRunEpicJSONResultAgreesWithTheExitCode(t *testing.T) {
	t.Parallel()

	completed := &reconcile.Result{State: runstate.StateCompleted, RunID: "epic-qeu", EpicID: "qeu"}
	doc, _, code := runEpicResultJSONForTest(t, completed)
	if code != exitSuccess || doc["state"] != agentStateDone {
		t.Errorf("a completed run's document is %v with exit %d, want done/0", doc["state"], code)
	}

	held := &reconcile.Result{
		State: runstate.StateFailed,
		RunID: "epic-qeu", EpicID: "qeu",
		Reason:  "the close-out holds: a finding is untriaged",
		Failure: &reconcile.Refusal{Reason: reconcile.RefusedFindingUntriaged, Message: "a finding is untriaged"},
	}
	doc, _, code = runEpicResultJSONForTest(t, held)
	// The held class (tick 4mv): a run that stopped holding something only a
	// person can move is the same verdict run-epic exits as the watch over
	// the same run — 3, with the state word the code agrees with.
	if code != ExitHeld || doc["state"] != agentStateHeld {
		t.Errorf("a refused run's document is %v with exit %d, want %q/%d — the same verdict the watch over this run ends by",
			doc["state"], code, agentStateHeld, ExitHeld)
	}
	failure, ok := doc["failure"].(map[string]any)
	if !ok || failure["reason"] != reconcile.RefusedFindingUntriaged {
		t.Fatalf("the refusal's reason class is not in the document:\n%v", doc)
	}
}

// runEpicResultJSONForTest emits one result's document into a buffer and
// reads it back — the seam the exit code's agreement is checked through,
// without running a reconciler to get a Result.
func runEpicResultJSONForTest(t *testing.T, result *reconcile.Result) (doc map[string]any, stderr string, code int) {
	t.Helper()
	var stdout bytes.Buffer
	emitRunEpicResultJSON(result, &stdout)
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the result document does not parse: %v\n%s", err, stdout.String())
	}
	return doc, "", resultExitCode(result)
}

// watch --json: the ended run answers once, as the model document, with the
// state word the exit code agrees with. The stream path is the one --json
// takes even on a terminal, so this is the pipe's contract too.
func TestWatchJSONAnswersOnceAtTheEnd(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-done", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-done", "a1", &attempt, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-done", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-done", "", nil, "run_finished", "completed"))

	doc, stderr, code := jsonAnswer(t, []string{"watch", "--json", "--repo", repo, "r-done"})
	if code != exitSuccess {
		t.Fatalf("a watch of an ended run exited %d, want 0: %s", code, stderr)
	}
	mustSchema(t, doc, "ticfac.watch.v1")
	if doc["state"] != agentStateDone {
		t.Errorf("the state word is %v, want %q", doc["state"], agentStateDone)
	}
	if _, carries := doc["model"]; !carries {
		t.Errorf("the document does not carry the status model the watch gathered:\n%v", doc)
	}
	if strings.Contains(stderr, "ticfac watch") {
		t.Logf("watch stderr: %s", stderr) // informational; the doc is the answer
	}
}

// watch --json, the failed end (tick bot): the same watch that answers
// done for a completed run answers FAILED for one whose own terminal line
// says so — the state word and the exit code are the pair an agent branches
// on, and a failed run that answered done/0 was indistinguishable from a
// finished epic.
func TestWatchJSONAnswersFailedWhenTheRunEndedFailed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-fail", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 40, 0, 0, time.UTC), "r-fail", "a1", nil,
		reconcile.StageGateFailed, "the integrated gate refused the work"))
	writeFeedEvent(t, repo, "r-fail", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-fail", "", nil, "run_finished",
		"failed: a1 did not pass"))

	doc, stderr, code := jsonAnswer(t, []string{"watch", "--json", "--repo", repo, "r-fail"})
	if code != exitGeneric {
		t.Fatalf("a watch of a failed run exited %d, want %d: %s", code, exitGeneric, stderr)
	}
	mustSchema(t, doc, "ticfac.watch.v1")
	if doc["state"] != agentStateFailed {
		t.Errorf("the state word is %v, want %q — a failed run is not done", doc["state"], agentStateFailed)
	}
}

// watch --json, the cancelled end (tick rix): the same watch that answers
// done for a completed run answers CANCELLED for one stopped deliberately —
// its own state word, with its own exit code, so an agent branching on
// either half of the pair never reads a stopped run as a finished epic.
func TestWatchJSONAnswersCancelledWhenTheRunEndedCancelled(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-stop", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 40, 0, 0, time.UTC), "r-stop", "a1", nil, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-stop", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "r-stop", "", nil, "run_finished",
		"cancelled: the operator stopped the run"))

	doc, stderr, code := jsonAnswer(t, []string{"watch", "--json", "--repo", repo, "r-stop"})
	if code != exitCancelled {
		t.Fatalf("a watch of a cancelled run exited %d, want %d: %s", code, exitCancelled, stderr)
	}
	mustSchema(t, doc, "ticfac.watch.v1")
	if doc["state"] != agentStateCancelled {
		t.Errorf("the state word is %v, want %q — a cancelled run is not done", doc["state"], agentStateCancelled)
	}
}

// run-epic --json, the cancelled result: the resume path replays an
// already-terminal checkpoint as a Result, so a cancelled checkpoint's
// replay must answer the same word the watch answers — cancelled, with the
// cancelled code — never failed/1, which would name a fix for a run nobody
// needs to fix.
func TestRunEpicJSONCancelledResultAnswersCancelled(t *testing.T) {
	t.Parallel()

	cancelled := &reconcile.Result{State: runstate.StateCancelled, RunID: "epic-qeu", EpicID: "qeu",
		Reason: "the operator stopped the run"}
	doc, _, code := runEpicResultJSONForTest(t, cancelled)
	if code != exitCancelled || doc["state"] != agentStateCancelled {
		t.Errorf("a cancelled run's document is %v with exit %d, want cancelled/%d", doc["state"], code, exitCancelled)
	}
}

// watch --json, the deliberate stop (tick vqc): a person's Ctrl-C of a
// foreground run-epic answers the cancelled state word with its own exit
// code — never failed/1, which names a fix nobody needs to make, and never
// done/0, which reads a stopped run as a finished epic.
func TestWatchJSONAnswersCancelledWhenAPersonStoppedTheRun(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-cc", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 5, 0, 0, time.UTC), "r-cc", "a1", nil, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-cc", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 6, 2, 0, time.UTC), "r-cc", "", nil, reconcile.StageRunDied,
		"cancelled: stopped by a signal (interrupt) before the run finished"))

	doc, stderr, code := jsonAnswer(t, []string{"watch", "--json", "--repo", repo, "r-cc"})
	if code != exitCancelled {
		t.Fatalf("a watch of a person-stopped run exited %d, want %d: %s", code, exitCancelled, stderr)
	}
	mustSchema(t, doc, "ticfac.watch.v1")
	if doc["state"] != agentStateCancelled {
		t.Errorf("the state word is %v, want %q — a deliberate stop is neither done nor failed", doc["state"], agentStateCancelled)
	}
}
