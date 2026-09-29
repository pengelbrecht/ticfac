package cli

// The run-scoped cloud commands (logs, trace, supervisor, stop) take the epic
// id a run was started for, the way status/events/watch do (runid.go): `ticfac
// run hn6 --cloud` hands the operator an epic id, never the factory's run id.
// And a tick id — what an operator reading a worker stream has to hand — is
// answered with the command that works, never with "No AI Gateway calls are
// stamped with run r5i" while that tick's worker had made 112 of them.

import (
	"net/http"
	"strings"
	"testing"
)

const (
	hn6LiveRunID = "run_aaf6c289d1e57942cea5fef6c1a508a0"
	hn6OldRunID  = "run_0ld6c289d1e57942cea5fef6c1a508a0"
)

// The factory of the hn6 incident: epic hn6 has a live run whose worker
// streams include tick r5i, and an older finished run LISTED FIRST, so the
// live one is chosen for being live, not for its place in the index.
func hn6Factory(t *testing.T) *[]cloudFactoryRequest {
	t.Helper()
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == "/api/runs":
			return http.StatusOK, map[string]any{"runs": []any{
				map[string]any{"run_id": hn6OldRunID, "state": "failed", "epic": "hn6", "project": "acme/project"},
				map[string]any{"run_id": hn6LiveRunID, "state": "running", "epic": "hn6", "project": "acme/project"},
			}}
		case request.Method == http.MethodGet && request.Path == "/api/runs/"+hn6LiveRunID+"/logs":
			return http.StatusOK, map[string]any{
				"run_id": hn6LiveRunID, "state": "running", "tick_id": request.Query.Get("tick"),
				"text": "ticks-worker: r5i at work\n", "bytes": 26, "total_bytes": 26,
				"streams": []map[string]any{{"tick_id": "r5i", "bytes": 26, "segments": 1}},
			}
		case request.Method == http.MethodPost && request.Path == "/api/runs/"+hn6LiveRunID+"/stop":
			return http.StatusOK, map[string]any{"run": map[string]any{"run_id": hn6LiveRunID, "state": "stopping"}}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	if endpoint != "https://factory.test" {
		t.Fatalf("unexpected fake factory endpoint %s", endpoint)
	}
	return requests
}

// A gateway holding hn6's live run: two calls stamped with tick r5i and one
// with tick k2p, newest first as the live API answers.
func hn6Gateway(t *testing.T) *[]traceGatewayRequest {
	t.Helper()
	row := func(id, created, tick string) map[string]any {
		r := traceRow(id, created, 40000, 0, 120, 0.2)
		r["metadata"] = map[string]string{"run_id": hn6LiveRunID, "tick_id": tick}
		return r
	}
	return newTraceGateway(t, func(request traceGatewayRequest) (int, any) {
		switch {
		case strings.HasSuffix(request.Path, "/request"):
			return http.StatusOK, traceRequestBody(1)
		case strings.HasSuffix(request.Path, "/logs"):
			return http.StatusOK, map[string]any{"success": true, "result": []any{
				row("callC", "2026-09-29T10:10:00Z", "r5i"),
				row("callB", "2026-09-29T10:05:00Z", "k2p"),
				row("callA", "2026-09-29T10:00:00Z", "r5i"),
			}}
		}
		return http.StatusNotFound, map[string]any{"success": false}
	})
}

// short: in-process fake factory and gateway; one temp git checkout.
func TestCloudTraceTakesAnEpicIDAndNarrowsToOneTick(t *testing.T) {
	setupCloudRepo(t, false)
	configureTraceGateway(t)
	factory := hn6Factory(t)
	gateway := hn6Gateway(t)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "trace", "hn6", "--tick", "r5i"})
	if code != exitSuccess {
		t.Fatalf("cloud trace hn6 --tick r5i: exit %d\n%s\n%s", code, stderr.String(), out.String())
	}
	if len(*gateway) == 0 || !strings.Contains((*gateway)[0].Query.Get("filters"), hn6LiveRunID) {
		t.Fatalf("the gateway was not asked for the epic's LIVE run %s: %#v", hn6LiveRunID, *gateway)
	}
	if !strings.Contains(stderr.String(), "hn6 is an epic id") || !strings.Contains(stderr.String(), hn6LiveRunID) {
		t.Errorf("the resolution from epic to run is not reported on stderr:\n%s", stderr.String())
	}
	output := out.String()
	if !strings.Contains(output, "tick r5i — 2 model calls") {
		t.Errorf("the trace was not narrowed to tick r5i's two calls:\n%s", output)
	}
	if index := (*factory)[0].Query.Get("project"); index != "acme/project" {
		t.Errorf("the run index was not narrowed to this checkout's project: %q", index)
	}
}

// short: in-process fake factory and gateway; one temp git checkout.
func TestCloudTraceTickWithNoCallsNamesTheTicksThatHaveThem(t *testing.T) {
	setupCloudRepo(t, false)
	configureTraceGateway(t)
	hn6Factory(t)
	hn6Gateway(t)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "trace", hn6LiveRunID, "--tick", "zzz"})
	if code != exitSuccess {
		t.Fatalf("cloud trace --tick zzz: exit %d\n%s", code, stderr.String())
	}
	output := out.String()
	if !strings.Contains(output, "none is stamped with tick zzz") || !strings.Contains(output, "k2p (1), r5i (2)") {
		t.Errorf("a tick with no calls is not told apart from a quiet run:\n%s", output)
	}
}

// THE incident: `ticfac cloud trace r5i` said the gateway had no calls for
// "run r5i". A tick of a live run is refused with the command that answers.
//
// short: in-process fake factory and gateway; one temp git checkout.
func TestCloudTraceOfATickIDPointsAtItsEpic(t *testing.T) {
	setupCloudRepo(t, false)
	configureTraceGateway(t)
	hn6Factory(t)
	gateway := hn6Gateway(t)

	code, out, stderr := runCloudArgs(t, []string{"cloud", "trace", "r5i"})
	if code == exitSuccess {
		t.Fatalf("cloud trace r5i succeeded on a tick id:\n%s", out.String())
	}
	if strings.Contains(out.String(), "No AI Gateway calls") {
		t.Fatalf("a tick id was answered with a confident negative:\n%s", out.String())
	}
	want := "r5i is a tick of epic hn6"
	try := "try: ticfac cloud trace hn6 --tick r5i"
	if !strings.Contains(stderr.String(), want) || !strings.Contains(stderr.String(), try) {
		t.Fatalf("the refusal does not name the epic and the working command:\n%s", stderr.String())
	}
	if len(*gateway) != 0 {
		t.Errorf("the gateway was asked about a tick id as if it were a run: %#v", *gateway)
	}
}

// short: in-process fake factory; one temp git checkout.
func TestCloudSupervisorOfATickIDPointsAtItsEpicWithoutTick(t *testing.T) {
	setupCloudRepo(t, false)
	hn6Factory(t)
	configureCloudFactory(t, "https://factory.test")

	code, _, stderr := runCloudArgs(t, []string{"cloud", "supervisor", "r5i"})
	if code == exitSuccess {
		t.Fatal("cloud supervisor r5i succeeded on a tick id")
	}
	if !strings.Contains(stderr.String(), "try: ticfac cloud supervisor hn6") || strings.Contains(stderr.String(), "--tick") {
		t.Fatalf("supervisor's hint is not its own command (it takes no --tick):\n%s", stderr.String())
	}
}

// short: in-process fake factory; one temp git checkout.
func TestCloudLogsTakesTheEpicRunIDSpelling(t *testing.T) {
	setupCloudRepo(t, false)
	requests := hn6Factory(t)
	configureCloudFactory(t, "https://factory.test")

	code, out, stderr := runCloudArgs(t, []string{"cloud", "logs", "epic-hn6", "--tick", "r5i"})
	if code != exitSuccess {
		t.Fatalf("cloud logs epic-hn6 --tick r5i: exit %d\n%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "r5i at work") {
		t.Errorf("the tick's own stream was not printed:\n%s", out.String())
	}
	last := (*requests)[len(*requests)-1]
	if last.Path != "/api/runs/"+hn6LiveRunID+"/logs" || last.Query.Get("tick") != "r5i" {
		t.Errorf("logs read %s?%s, want the live run's r5i stream", last.Path, last.Query.Encode())
	}
}

// short: in-process fake factory; one temp git checkout.
func TestCloudStopTakesAnEpicID(t *testing.T) {
	setupCloudRepo(t, false)
	requests := hn6Factory(t)
	configureCloudFactory(t, "https://factory.test")

	code, out, stderr := runCloudArgs(t, []string{"cloud", "stop", "hn6"})
	if code != exitSuccess {
		t.Fatalf("cloud stop hn6: exit %d\n%s", code, stderr.String())
	}
	last := (*requests)[len(*requests)-1]
	if last.Method != http.MethodPost || last.Path != "/api/runs/"+hn6LiveRunID+"/stop" {
		t.Errorf("stop hit %s %s, want the epic's live run", last.Method, last.Path)
	}
	if !strings.Contains(out.String(), hn6LiveRunID) {
		t.Errorf("the stop does not name the run it stopped:\n%s", out.String())
	}
}

// An id that names no run, no epic and no live run's tick is said to be that —
// with where the runs can be listed — never passed to the factory as a run.
//
// short: in-process fake factory; one temp git checkout.
func TestCloudLogsOfAnUnknownIDSaysSo(t *testing.T) {
	setupCloudRepo(t, false)
	requests := hn6Factory(t)
	configureCloudFactory(t, "https://factory.test")

	code, _, stderr := runCloudArgs(t, []string{"cloud", "logs", "q9q"})
	if code == exitSuccess {
		t.Fatal("cloud logs q9q succeeded")
	}
	if !strings.Contains(stderr.String(), "no cloud run answers to q9q") ||
		!strings.Contains(stderr.String(), "ticfac cloud status") {
		t.Fatalf("an unknown id is not said to be unknown:\n%s", stderr.String())
	}
	for _, request := range *requests {
		if request.Path == "/api/runs/q9q/logs" {
			t.Fatalf("the unknown id was read as a run: %#v", *requests)
		}
	}
}
