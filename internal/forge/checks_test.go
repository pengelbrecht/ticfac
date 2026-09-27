package forge

import (
	"context"
	"net/http"
	"testing"
)

// Checks is the per-check half of the CI seam (ticfac tick 6dh): the status
// model carries the latest run of EVERY check beside the classification,
// and both must read the same reduction — a classification that said "red"
// while the per-check facts showed green (or the reverse) would be two
// surfaces disagreeing about one head.
func TestChecksAnswersTheLatestRunOfEveryCheck(t *testing.T) {
	t.Parallel()
	runs := []map[string]string{
		{"name": "go", "status": "completed", "conclusion": "success",
			"started_at": "2026-09-23T10:05:00Z", "details_url": "https://github.com/example/example/actions/runs/222/job/8"},
		{"name": "go", "status": "completed", "conclusion": "failure",
			"started_at": "2026-09-23T10:00:00Z", "details_url": "https://github.com/example/example/actions/runs/111/job/9"},
		{"name": "ts", "status": "in_progress", "conclusion": "", "started_at": "2026-09-23T10:06:00Z"},
	}
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
	})
	checks, err := g.Checks(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 {
		t.Fatalf("the head carries 2 checks and the answer has %d", len(checks))
	}
	byName := map[string]CheckRun{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	goCheck := byName["go"]
	if goCheck.Conclusion != "success" || goCheck.StartedAt != "2026-09-23T10:05:00Z" {
		t.Errorf("the go check reads %+v, want the LATEST run of it", goCheck)
	}
	if byName["ts"].Status != "in_progress" {
		t.Errorf("the ts check reads %+v, want the run that has not concluded", byName["ts"])
	}

	// The classification of the same answer must agree with the per-check
	// facts: a green pass, a red failure, a pending run in flight.
	report, err := g.CI(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if report.State != CIPending {
		t.Errorf("a head with a check in flight classifies %q, want pending", report.State)
	}
}

func TestChecksAnswersEmptyWhenNothingRan(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"total_count": 0, "check_runs": []any{}})
	})
	checks, err := g.Checks(context.Background(), PullRequest{Number: 7, HeadSHA: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 0 {
		t.Errorf("a head with no check run answered %d checks, want none", len(checks))
	}
}
