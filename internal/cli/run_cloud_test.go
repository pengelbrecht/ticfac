package cli

// `ticfac run <epic> --cloud` (tick ejw): the acceptance criteria, as tests
// against a fake factory — "--cloud runs, attaches, resumes and triages
// exactly as a local run from the operator's point of view".
//
// The seams answer the attach for the decision tests (start, attach, resume),
// exactly as 9sz's tests answer `runAttach`: everything above the seam is
// the production code the criterion is about. The one test that runs the
// production attach proves the whole cloud path through the REAL watch —
// the exit-3 hold, the words that name `ticfac triage`, and the one triage
// command that clears the hold — because that round trip is the parity the
// acceptance names: the same view, the same exit codes, the same triage.
//
// The fake factory is the same harness the cloud command tests use
// (newCloudFactory + configureCloudFactory + stubCloudTk + setupCloudRepo),
// so what these tests serve is shaped by the same contract readers the
// expert commands are tested against — never a second opinion about the
// factory's wire shape.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// cloudRunIDOf builds a whole cloud run id from a hex head: `run_` plus 32
// hex characters, the shape newRunID mints in cloudflare/src/runs.ts and
// feedSource/looksLikeCloudRunID key on — so the ids these tests use are
// indistinguishable from the factory's own.
func cloudRunIDOf(head string) string {
	padded := head + strings.Repeat("0", 32)
	return "run_" + padded[:32]
}

// saveRunCloudAttach snapshots the cloud attach seam and restores it on
// cleanup, the same discipline saveRunSeams holds for the local seams: a
// test that answers the attach with a controlled value cannot leak it.
func saveRunCloudAttach(t *testing.T) {
	t.Helper()
	saved := runCloudAttach
	t.Cleanup(func() { runCloudAttach = saved })
}

// cloudAttachRecorder records the run ids `run --cloud` attached to.
type cloudAttachRecorder struct{ runIDs []string }

// recordCloudAttach replaces the attach seam with one that records and
// succeeds, printing a line the tests can read.
func recordCloudAttach(t *testing.T) *cloudAttachRecorder {
	t.Helper()
	saveRunCloudAttach(t)
	rec := &cloudAttachRecorder{}
	runCloudAttach = func(ctx context.Context, repo, epicID, runID string, stdout, stderr io.Writer) int {
		rec.runIDs = append(rec.runIDs, runID)
		fmt.Fprintf(stdout, "(attach seam) attached to cloud run %s\n", runID)
		return 0
	}
	return rec
}

// runRunCloud drives `ticfac run ... --cloud` the way an operator does.
func runRunCloud(t *testing.T, repo, epicArg string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"run", "--cloud", "--repo", repo, epicArg}, &stdout, &stderr)
	return code, &stdout, &stderr
}

// cloudIndexRequest is the run-index read every `run --cloud` decision starts
// with, so each test's handler can state what the factory knows without
// restating the path shape.
const cloudIndexPath = "/api/runs"

// TestRunCloudSubmitsAndAttaches: no cloud run for the epic, so the one
// command STARTS it — the same pushed boundary and the same POST the expert
// `cloud run` makes — and attaches the live view to the run the factory
// started.
func TestRunCloudSubmitsAndAttaches(t *testing.T) {
	stubCloudTk(t)
	repo, _, baseSHA := setupCloudRepo(t, true)
	started := cloudRunIDOf("aa11")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{
				"run": map[string]any{"run_id": started, "state": "starting"},
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d starting a cloud run: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "no cloud run for epic epic1") ||
		!strings.Contains(stdout.String(), "starting one") {
		t.Fatalf("stdout does not say the epic is being started:\n%s", stdout.String())
	}
	if len(rec.runIDs) != 1 || rec.runIDs[0] != started {
		t.Fatalf("attached to %v, want the run the factory started (%s)", rec.runIDs, started)
	}
	if !strings.Contains(stdout.String(), "attaching") {
		t.Fatalf("stdout does not say it is attaching:\n%s", stdout.String())
	}

	var post *cloudFactoryRequest
	for i := range *requests {
		request := (*requests)[i]
		if request.Method == http.MethodPost {
			if post != nil {
				t.Fatalf("the factory received %d submissions, want one", len(*requests))
			}
			post = &(*requests)[i]
		}
	}
	if post == nil {
		t.Fatal("the epic was never submitted to the factory")
	}
	for field, want := range map[string]any{
		"project":      "acme/project",
		"epic":         "epic1",
		"base_sha":     baseSHA,
		"requested_by": "operator@example.com",
		"queue":        false,
	} {
		if got := post.Body[field]; got != want {
			t.Errorf("the submission's %s is %#v, want %#v", field, got, want)
		}
	}
}

// TestRunCloudAttachesALiveRunWithoutResubmitting: a second invocation
// against a live cloud run attaches — the run's own record claims life, so
// the epic is never submitted twice — and the epic id is accepted spelled
// as the run id prefix too, the same rule the local command holds.
func TestRunCloudAttachesALiveRunWithoutResubmitting(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	live := cloudRunIDOf("bb22")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": live, "epic": "epic1", "state": "running", "started_at": "2026-09-27T10:00:00Z",
			}}}
		case request.Method == http.MethodPost:
			t.Error("a live cloud run was submitted to again")
			return 409, map[string]any{"error": "lease_held"}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	// The run-id spelling of the epic id: `run epic-epic1` is `run epic1`.
	code, stdout, stderr := runRunCloud(t, repo, "epic-epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d attaching to a live cloud run: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "cloud run "+live+" is alive") ||
		!strings.Contains(stdout.String(), "attaching") {
		t.Fatalf("stdout does not say the live cloud run is being attached to:\n%s", stdout.String())
	}
	if len(rec.runIDs) != 1 || rec.runIDs[0] != live {
		t.Fatalf("attached to %v, want the live run %s", rec.runIDs, live)
	}
	if len(*requests) != 1 {
		t.Fatalf("the factory was asked %d times, want the one index read", len(*requests))
	}
}

// TestRunCloudResumesAFinishedRunWithANewSubmission: a finished cloud run is
// resumed exactly the way a stopped local one is — run the command again —
// and for a cloud run the resume is a new submission, the one the cloud
// orchestrator resumes from the integration branch.
func TestRunCloudResumesAFinishedRunWithANewSubmission(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	finished := cloudRunIDOf("cc33")
	resumed := cloudRunIDOf("dd44")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": finished, "epic": "epic1", "state": "completed", "started_at": "2026-09-26T10:00:00Z",
			}}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{
				"run": map[string]any{"run_id": resumed, "state": "starting"},
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d resuming a finished cloud run: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "cloud run "+finished+" is not running") ||
		!strings.Contains(stdout.String(), "resuming the epic as a new submission") {
		t.Fatalf("stdout does not say the epic is being resumed:\n%s", stdout.String())
	}
	if len(rec.runIDs) != 1 || rec.runIDs[0] != resumed {
		t.Fatalf("attached to %v, want the resumed run %s", rec.runIDs, resumed)
	}
	var submissions int
	for _, request := range *requests {
		if request.Method == http.MethodPost {
			submissions++
			if request.Body["epic"] != "epic1" {
				t.Errorf("the resubmission's epic is %#v", request.Body["epic"])
			}
		}
	}
	if submissions != 1 {
		t.Fatalf("%d submissions, want the one the resume is", submissions)
	}
}

// TestRunCloudReportsAQueuedSubmission: a submission the factory parks
// behind the project lease is reported as what it is — work that has not
// started yet — never attached to (a run with no feed would read as a
// failure it is not) and never failed.
func TestRunCloudReportsAQueuedSubmission(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	queued, holder := cloudRunIDOf("ee55"), cloudRunIDOf("ff66")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{
				"queued": map[string]any{"run_id": queued, "epic": "epic1"},
				"holder": map[string]any{"run_id": holder, "epic": "other"},
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	saveRunCloudAttach(t)
	attached := false
	runCloudAttach = func(ctx context.Context, repo, epicID, runID string, stdout, stderr io.Writer) int {
		attached = true
		return 0
	}

	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != exitSuccess {
		t.Fatalf("exit %d for a queued submission: %s\n%s", code, stderr.String(), stdout.String())
	}
	if attached {
		t.Error("a queued submission was attached to; there is no run to attach to yet")
	}
	if !strings.Contains(stdout.String(), "cloud run "+queued+" queued") ||
		!strings.Contains(stdout.String(), holder) ||
		!strings.Contains(stdout.String(), "`ticfac run epic1 --cloud` attaches then") {
		t.Fatalf("stdout does not say where the submission parks and how to come back:\n%s", stdout.String())
	}
}

// TestRunCloudEndsHoldingForTriageAndTriageSettlesIt: the round trip the
// acceptance's triage verb is. A cloud run whose close-out waits on an
// untriaged finding ends — through the REAL watch, on the run the command
// itself started — holding something for a person, with exit 3 (the same
// code a local hold exits by) and words that name the one command that
// clears it; and that command settles the finding exactly as it settles a
// local run's, because the findings channel is the integration branch
// either host runs on.
func TestRunCloudEndsHoldingForTriageAndTriageSettlesIt(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	finished, resumed := cloudRunIDOf("ab12"), cloudRunIDOf("cd34")

	// The integration branch the cloud run works on, and the finding its
	// worker drafted there — the same branch shape a local run's records
	// take, which is the point: triage reads one channel for both hosts.
	execTestCmd(t, repo, "git", "push", "origin", "main:refs/heads/epic/epic1")
	finding := testDraftFinding(triageKey("d34db33f"), "")
	finding.DiscoveredFrom = "run-epic-epic1/tick-a1/attempt-1"
	finding.Provenance.RunID = "epic-epic1"
	store, err := runstate.Open(runstate.Options{
		Repo: repo, Remote: "origin", Branch: "epic/epic1", RunID: "epic-epic1",
	})
	if err != nil {
		t.Fatalf("open the run's state store: %v", err)
	}
	if _, err := store.PutFinding(finding); err != nil {
		t.Fatalf("seed the cloud run's drafted finding: %v", err)
	}

	// The feed the factory serves the resumed run: it ends holding for a
	// person, with the close-out hold's own words — the ones that name
	// `ticfac triage` — and the run's own last word after them.
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	feed := feedLine(t, runfeed.NewEvent(at, resumed, "epic1", nil, reconcile.StageRunHeld,
		"the close-out does not hand over while a finding is untriaged: "+
			"Triage with `ticfac triage epic1`: every untriaged finding of the run settles there, "+
			"addressed by a short key prefix"))
	feed += feedLine(t, runfeed.NewEvent(at.Add(time.Minute), resumed, "", nil, reconcile.StageRunFinished,
		"holding: the close-out waits on untriaged findings"))

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": finished, "epic": "epic1", "state": "completed", "started_at": "2026-09-26T10:00:00Z",
			}}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{
				"run": map[string]any{"run_id": resumed, "state": "starting"},
			}
		case request.Path == "/api/runs/"+resumed:
			return 200, map[string]any{"run": map[string]any{
				"run_id": resumed, "epic": "epic1", "state": "completed",
			}}
		case request.Path == "/api/runs/"+resumed+"/events":
			return 200, map[string]any{
				"run_id": resumed, "state": "completed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	// The production attach: no seam, the real watch over the fake
	// factory's feed — the same exit code a local hold ends by.
	code, stdout, stderr := runRunCloud(t, repo, "epic1")
	if code != ExitHeld {
		t.Fatalf("exit %d, want the held-for-person code %d a local run ends by:\n%s\n%s",
			code, ExitHeld, stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "HOLDING") || !strings.Contains(stderr.String(), "ticfac triage epic1") {
		t.Fatalf("the hold does not name the triage command:\n%s", stderr.String())
	}

	// The one command that moves the hold on, settled the everyday way — a
	// short key prefix, no 64-hex key — on the branch the cloud run owns.
	var triageOut, triageErr bytes.Buffer
	triageCode := Run([]string{"triage", "--repo", repo, "epic1", "d34=discard"}, &triageOut, &triageErr)
	if triageCode != exitSuccess {
		t.Fatalf("triage exit %d settling the cloud run's finding: %s", triageCode, triageErr.String())
	}
	settled, err := runstate.Open(runstate.Options{
		Repo: repo, Remote: "origin", Branch: "epic/epic1", RunID: "epic-epic1",
	})
	if err != nil {
		t.Fatalf("reopen the run's state store: %v", err)
	}
	if _, err := settled.Fetch(); err != nil {
		t.Fatalf("read the settled records off the branch: %v", err)
	}
	findings, err := settled.Findings()
	if err != nil || len(findings) != 1 {
		t.Fatalf("the branch carries %d findings (err %v), want the one settled", len(findings), err)
	}
	if findings[0].Status != runstate.FindingDiscarded {
		t.Errorf("the finding's status is %q, want discarded", findings[0].Status)
	}
}

// TestRunCloudDetachKeepsTheRunGoing: an interrupted attach to a live cloud
// run is a DETACH, not a failure — the same contract the local attach holds
// — and the line that says so names the one command that comes back. The
// run's life is asked of the factory on a context that does not carry the
// cancellation, because the operator leaving is not the run ending.
func TestRunCloudDetachKeepsTheRunGoing(t *testing.T) {
	live, finished := cloudRunIDOf("0a0a"), cloudRunIDOf("1b1b")
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/" + live:
			return 200, map[string]any{"run": map[string]any{"run_id": live, "epic": "epic1", "state": "running"}}
		case "/api/runs/" + live + "/events":
			// The run has not written its first event yet — the operator who
			// detaches here is one who left right after starting it.
			return 200, map[string]any{"run_id": live, "state": "running"}
		case "/api/runs/" + finished:
			return 200, map[string]any{"run": map[string]any{"run_id": finished, "epic": "epic1", "state": "completed"}}
		case "/api/runs/" + finished + "/events":
			return 200, map[string]any{"run_id": finished, "state": "completed"}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the operator left; the run did not

	var stdout, stderr bytes.Buffer
	code := runCloudAttach(ctx, t.TempDir(), "epic1", live, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit %d for a detached live run, want 0: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "detached from cloud run "+live) ||
		!strings.Contains(stdout.String(), "`ticfac run epic1 --cloud` attaches again") {
		t.Fatalf("the detach line does not say how to come back:\n%s", stdout.String())
	}

	// A run the factory says has finished is not a detach: the interruption
	// keeps the code the watch ended by.
	stdout.Reset()
	stderr.Reset()
	code = runCloudAttach(ctx, t.TempDir(), "epic1", finished, &stdout, &stderr)
	if code == exitSuccess {
		t.Fatal("an interrupted watch on a finished run read as a clean detach")
	}
	if strings.Contains(stdout.String(), "detached from cloud run") {
		t.Fatalf("a finished run's interruption was rewritten as a detach:\n%s", stdout.String())
	}
}

// TestRunCloudRefusesTheLocalOnlyFlags: --no-herdr, --profiles and --wall
// drive a LOCAL run's jobs, and a flag that silently did nothing would be a
// lie the everyday command must not tell.
func TestRunCloudRefusesTheLocalOnlyFlags(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--cloud", "--no-herdr", "epic1"},
		{"run", "--cloud", "--profiles", "herdr", "epic1"},
		{"run", "--cloud", "--wall", "3600", "epic1"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(args, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("exit %d for %v, want the usage refusal %d", code, args, exitUsage)
		}
		if !strings.Contains(stderr.String(), "do not apply to a --cloud run") {
			t.Errorf("stderr for %v does not say why: %s", args, stderr.String())
		}
	}
}

// TestRunCloudNamesTheMissingFactory: no factory configured is a refusal
// that says how to configure one — the same words the expert commands use,
// because it is the same missing fact.
func TestRunCloudNamesTheMissingFactory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"run", "--cloud", "--repo", t.TempDir(), "epic1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit %d, want a refusal (%d): %s", code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "factory") {
		t.Fatalf("the refusal does not name the factory: %s", stderr.String())
	}
}
