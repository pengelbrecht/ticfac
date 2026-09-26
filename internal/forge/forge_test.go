package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The GitHub surface, against an httptest server that speaks the three
// requests the seam makes. These tests exist so the reconciler's tests can
// take the SEAM on faith and prove the behaviour above it: a surface whose
// own answers nobody had checked is a seam that fails only in production.

// newGitHub builds the surface against a test server, and records every
// request it saw: the ORDER the reconciler asks its questions in is part of
// what the close-out admission tests assert.
func newGitHub(t *testing.T, handler http.HandlerFunc) (GitHub, *[]string) {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return GitHub{Token: "test-token", API: server.URL, Repo: "example/example", Client: server.Client()}, &seen
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatal(err)
	}
}

func TestParseRepo(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{
		"git@github.com:example/example.git",
		"https://github.com/example/example.git",
		"https://github.com/example/example",
		"http://local_proxy@127.0.0.1:8080/git/example/example",
		"ssh://git@github.com/example/example.git",
	} {
		repo, err := ParseRepo(remote)
		if err != nil {
			t.Errorf("ParseRepo(%q): %v", remote, err)
			continue
		}
		if repo != "example/example" {
			t.Errorf("ParseRepo(%q) = %q, want example/example", remote, repo)
		}
	}
	for _, remote := range []string{"", "git@github.com:", "https://github.com/", "not-a-remote"} {
		if _, err := ParseRepo(remote); err == nil {
			t.Errorf("ParseRepo(%q) was accepted", remote)
		}
	}
}

// Find answers the ONE open PR for a head branch, and nil when there is none.
func TestFindAnswersTheOpenPR(t *testing.T) {
	t.Parallel()
	g, seen := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/example/example/pulls"):
			if got := r.URL.Query().Get("head"); got != "example:epic/9pd" {
				t.Errorf("head query = %q, want the owner-qualified branch", got)
			}
			writeJSON(t, w, http.StatusOK, []map[string]any{{
				"number": 7, "html_url": "https://example/example/pull/7",
				"head": map[string]any{"ref": "epic/9pd", "sha": "abc123"},
				"base": map[string]any{"ref": "main"},
			}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "nope"})
		}
	})
	pr, err := g.Find(context.Background(), "epic/9pd", "main")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 7 || pr.HeadSHA != "abc123" || pr.BaseRef != "main" {
		t.Fatalf("Find = %+v", pr)
	}
	if len(*seen) == 0 || !strings.HasPrefix((*seen)[0], "GET /repos/example/example/pulls") {
		t.Errorf("the calls seen were %v", *seen)
	}
}

func TestFindAnswersNilWhenNoPROpen(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})
	pr, err := g.Find(context.Background(), "epic/9pd", "main")
	if err != nil {
		t.Fatal(err)
	}
	if pr != nil {
		t.Fatalf("Find = %+v, want nil", pr)
	}
}

// Open posts the PR the run's own configuration demands, and nothing else.
func TestOpenCreatesThePR(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/example/example/pulls" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "nope"})
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("Authorization = %q", auth)
		}
		var asked map[string]string
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			t.Fatal(err)
		}
		if asked["head"] != "epic/9pd" || asked["base"] != "main" || asked["title"] == "" {
			t.Errorf("the PR asked for is %+v", asked)
		}
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"number": 8, "html_url": "https://example/example/pull/8",
			"head": map[string]any{"ref": "epic/9pd", "sha": "def456"},
			"base": map[string]any{"ref": "main"},
		})
	})
	pr, err := g.Open(context.Background(), "epic/9pd", "main", "title", "body")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 8 || pr.HeadSHA != "def456" {
		t.Fatalf("Open = %+v", pr)
	}
}

// An API refusal is an error carrying the status and the API's own message:
// the typed refusal that forwards it names its cause.
func TestAnAPIRefusalCarriesItsCause(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnprocessableEntity,
			map[string]string{"message": "A pull request already exists for this branch"})
	})
	_, err := g.Open(context.Background(), "epic/9pd", "main", "title", "body")
	if err == nil {
		t.Fatal("the duplicate-PR refusal was accepted")
	}
	for _, want := range []string{"422", "already exists"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

// UpdateBody rewrites the PR's body in place — the write half of the
// close-out rule (tick 4sb), and the one thing this seam could not do until
// now: put the run's record where the person merging reads it. The verb is
// EDIT, deliberately: a body written twice leaves the PR in the state one
// write left it in, which is what makes a resumed close-out safe — see the
// interface.
func TestUpdateBodyRewritesThePRBody(t *testing.T) {
	t.Parallel()
	g, seen := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/example/example/pulls/7" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "nope"})
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("Authorization = %q", auth)
		}
		var asked map[string]string
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			t.Fatal(err)
		}
		if asked["body"] != "the composed body" {
			t.Errorf("the body sent was %q", asked["body"])
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"number": 7})
	})
	pr := PullRequest{Number: 7, HeadRef: "epic/9pd", HeadSHA: "abc123", BaseRef: "main"}
	if err := g.UpdateBody(context.Background(), pr, "the composed body"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) == 0 || (*seen)[0] != "PATCH /repos/example/example/pulls/7" {
		t.Errorf("the calls seen were %v", *seen)
	}
}

// A rewrite the forge refuses is an error carrying the status and the API's
// own message, the same way every other answer is: the typed close-out refusal
// that forwards it names its cause rather than "could not write".
func TestUpdateBodyRefusalCarriesItsCause(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
	})
	err := g.UpdateBody(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"}, "body")
	if err == nil {
		t.Fatal("the forbidden rewrite was accepted")
	}
	for _, want := range []string{"403", "not accessible"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

// A PR naming no number addresses nothing, and the surface says so locally
// rather than asking the API about "/pulls/0".
func TestUpdateBodyRefusesAPRNamingNoNumber(t *testing.T) {
	t.Parallel()
	g := GitHub{Token: "t", Repo: "example/example"}
	err := g.UpdateBody(context.Background(), PullRequest{HeadSHA: "abc123"}, "body")
	if err == nil || !strings.Contains(err.Error(), "no number") {
		t.Fatalf("the no-number refusal = %v", err)
	}
}

// CI classifies the check runs the forge recorded for the head: the four
// states each send the next repair somewhere different, and the names of the
// failing jobs are the whole point of the report.
func TestCI(t *testing.T) {
	t.Parallel()
	checks := func(runs ...map[string]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
		}
	}
	cases := []struct {
		name    string
		runs    []map[string]string
		want    CIState
		failing []string
	}{
		{"all green", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "success"},
		}, CIGreen, nil},
		{"one failed", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "success"},
			{"name": "pi-runner", "status": "completed", "conclusion": "failure"},
		}, CIRed, []string{"pi-runner"}},
		{"timed out is a failure", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "timed_out"},
		}, CIRed, []string{"go"}},
		{"one still running", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "success"},
			{"name": "pi-runner", "status": "in_progress"},
		}, CIPending, nil},
		{"queued", []map[string]string{
			{"name": "go", "status": "queued"},
		}, CIPending, nil},
		{"neutral neither passes nor fails", []map[string]string{
			{"name": "lint", "status": "completed", "conclusion": "neutral"},
		}, CIGreen, nil},
		{"cancelled is not green", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "cancelled"},
		}, CIPending, nil},
	}
	for _, tc := range cases {
		g, _ := newGitHub(t, checks(tc.runs...))
		report, err := g.CI(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if report.State != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, report.State, tc.want)
		}
		if strings.Join(report.Failing, ",") != strings.Join(tc.failing, ",") {
			t.Errorf("%s: failing = %v, want %v", tc.name, report.Failing, tc.failing)
		}
	}
}

// No check runs at all is `none`, not `pending`: a workflow that never
// triggered on the PR is unsatisfiable by waiting, which is the failure mode
// the close-out rule exists to surface.
func TestCIAnswersNoneWhenNothingRan(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"total_count": 0, "check_runs": []map[string]string{}})
	})
	report, err := g.CI(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if report.State != CINone {
		t.Fatalf("state = %q, want none", report.State)
	}
}

// A missing token is refused by the surface itself, naming the one
// environment variable that fixes it.
func TestNoTokenIsRefusedNamingTheFix(t *testing.T) {
	t.Parallel()
	g := GitHub{Repo: "example/example"}
	if _, err := g.Find(context.Background(), "epic/9pd", "main"); err == nil ||
		!strings.Contains(err.Error(), TokenEnv) {
		t.Fatalf("the no-token refusal does not name the fix: %v", err)
	}
}

// The LATEST run of each check decides. An epic head carries a push run and a
// pull_request run of every job, and a re-run adds another: an old failure
// must not veto a later green run of the same job (wne's close-out held red on
// 'go failed' while go had passed on the same head, 2026-09-23).
func TestCIReadsOnlyTheLatestRunOfEachCheck(t *testing.T) {
	t.Parallel()
	failed := map[string]string{"name": "go", "status": "completed", "conclusion": "failure",
		"started_at": "2026-09-23T10:00:00Z", "details_url": "https://github.com/example/example/actions/runs/111/job/9"}
	passed := map[string]string{"name": "go", "status": "completed", "conclusion": "success",
		"started_at": "2026-09-23T10:05:00Z", "details_url": "https://github.com/example/example/actions/runs/222/job/8"}
	for _, tc := range []struct {
		name     string
		runs     []map[string]string
		want     CIState
		wantRuns []int64
	}{
		{"a later green run supersedes an earlier failure", []map[string]string{failed, passed}, CIGreen, nil},
		{"order in the answer does not matter", []map[string]string{passed, failed}, CIGreen, nil},
		{"a later failure is red and names its workflow run", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "success", "started_at": "2026-09-23T09:00:00Z"},
			failed,
		}, CIRed, []int64{111}},
		// CI skips an epic branch's pull_request run (the push run carries the
		// checks); a later skip must not shadow the red run that executed.
		{"a later skipped run does not hide a failure", []map[string]string{
			failed,
			{"name": "go", "status": "completed", "conclusion": "skipped", "started_at": "2026-09-23T11:00:00Z"},
		}, CIRed, []int64{111}},
		{"a skipped run does not stand in for a pending one", []map[string]string{
			{"name": "go", "status": "completed", "conclusion": "skipped", "started_at": "2026-09-23T11:00:00Z"},
			{"name": "go", "status": "in_progress", "conclusion": "", "started_at": "2026-09-23T10:30:00Z"},
		}, CIPending, nil},
	} {
		runs := tc.runs
		g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
		})
		report, err := g.CI(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if report.State != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, report.State, tc.want)
		}
		if fmt.Sprint(report.FailingRuns) != fmt.Sprint(tc.wantRuns) && !(len(report.FailingRuns) == 0 && len(tc.wantRuns) == 0) {
			t.Errorf("%s: failing runs = %v, want %v", tc.name, report.FailingRuns, tc.wantRuns)
		}
	}
}

// Once, by GitHub's own count: a run on its first attempt is re-run, a run
// already re-run is left alone - so a restarted reconciler cannot retry twice.
func TestRerunFailedOnceSkipsARunAlreadyRetried(t *testing.T) {
	t.Parallel()
	g, seen := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions/runs/1"):
			writeJSON(t, w, http.StatusOK, map[string]any{"id": 1, "run_attempt": 1})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions/runs/2"):
			writeJSON(t, w, http.StatusOK, map[string]any{"id": 2, "run_attempt": 2})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rerun-failed-jobs"):
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	rerun, err := g.RerunFailedOnce(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rerun) != "[1]" {
		t.Errorf("re-ran %v, want only [1]", rerun)
	}
	for _, call := range *seen {
		if call == "POST /repos/example/example/actions/runs/2/rerun-failed-jobs" {
			t.Error("re-ran a workflow run already on its second attempt")
		}
	}
}
