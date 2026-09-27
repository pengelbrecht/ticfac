package forge

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"hegel.dev/go/hegel"
)

// Properties of CI aggregation (tick 89g), the seam whose "a later skipped run
// hides a red one" false green was found by hand on 2026-09-26:
//   - order in the API's answer never changes the verdict;
//   - adding a skipped run never changes the verdict;
//   - if the latest EXECUTED run of any check failed, the verdict is not green.

type pbtRun struct {
	name, status, conclusion string
	minute                   int
}

func genRuns(tc hegel.TestCase) []pbtRun {
	return hegel.Draw(tc, hegel.Lists(hegel.Composite(func(tc hegel.TestCase) pbtRun {
		r := pbtRun{
			name:   hegel.Draw(tc, hegel.SampledFrom([]string{"go", "typescript"})),
			status: hegel.Draw(tc, hegel.SampledFrom([]string{"completed", "completed", "completed", "in_progress"})),
			minute: hegel.Draw(tc, hegel.Integers(0, 30)),
		}
		if r.status == "completed" {
			r.conclusion = hegel.Draw(tc, hegel.SampledFrom([]string{"success", "failure", "skipped", "cancelled", "neutral"}))
		}
		return r
	})).MinSize(1).MaxSize(6))
}

func ciOf(t *testing.T, runs []pbtRun) CIState {
	t.Helper()
	answer := make([]map[string]string, 0, len(runs))
	for i, r := range runs {
		answer = append(answer, map[string]string{
			"name": r.name, "status": r.status, "conclusion": r.conclusion,
			"started_at":  fmt.Sprintf("2026-09-27T10:%02d:00Z", r.minute),
			"details_url": fmt.Sprintf("https://github.com/o/r/actions/runs/%d/job/1", 100+i),
		})
	}
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"total_count": len(answer), "check_runs": answer})
	})
	report, err := g.CI(context.Background(), PullRequest{Number: 1, HeadSHA: "abc"})
	if err != nil {
		t.Fatalf("CI: %v", err)
	}
	return report.State
}

func TestPBTCIVerdictIgnoresOrderAndSkips(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		runs := genRuns(ht)
		// Distinct start times per check, so "latest" is well defined.
		seen := map[string]bool{}
		for _, r := range runs {
			key := fmt.Sprintf("%s@%d", r.name, r.minute)
			ht.Assume(!seen[key])
			seen[key] = true
		}
		want := ciOf(t, runs)

		reversed := make([]pbtRun, len(runs))
		for i, r := range runs {
			reversed[len(runs)-1-i] = r
		}
		if got := ciOf(t, reversed); got != want {
			ht.Fatalf("order changed the verdict: %q vs %q for %+v", want, got, runs)
		}

		skip := pbtRun{name: hegel.Draw(ht, hegel.SampledFrom([]string{"go", "typescript"})), status: "completed",
			conclusion: "skipped", minute: hegel.Draw(ht, hegel.Integers(31, 60))}
		if got := ciOf(t, append(append([]pbtRun{}, runs...), skip)); got != want {
			ht.Fatalf("adding a skipped run changed the verdict %q -> %q for %+v", want, got, runs)
		}

		// The latest executed run of each check.
		latest := map[string]pbtRun{}
		for _, r := range runs {
			if r.conclusion == "skipped" {
				continue
			}
			if prev, ok := latest[r.name]; !ok || r.minute > prev.minute {
				latest[r.name] = r
			}
		}
		for _, r := range latest {
			if r.conclusion == "failure" && want == CIGreen {
				ht.Fatalf("check %s's latest executed run failed, yet the verdict is green: %+v", r.name, runs)
			}
		}
	}, hegel.WithTestCases(300))
}
