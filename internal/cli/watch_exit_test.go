package cli

// The one ending, one verdict half of tick 4mv: a run that ended holding
// something only a person can move exits the held class (3) on EVERY path —
// the terminal's live view, the pipe's plain stream, and the one --json
// document — with the wait kind named, because an agent or a person who
// branches on the code must not get a different answer for the same ending
// depending on whether stdout was a terminal. The case the disagreement was
// found on: a COMPLETED run whose close-out left the epic PR open — the
// merge that is a person's by design — which the live view ended by 3 while
// the pipe and the document ended by 0.
import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// fakeTheForge swaps the forge seam for one test: the CI answer the model
// reads, carried as data, the same discipline the tracker seam follows.
func fakeTheForge(t *testing.T, ci *statusmodel.CIInput) {
	t.Helper()
	real := statusCI
	t.Cleanup(func() { statusCI = real })
	statusCI = func(context.Context, string, string) (*statusmodel.CIInput, error) {
		return ci, nil
	}
}

// completedRunWithAnOpenPR seeds one ended run: every tick closed behind the
// gate, the close-out opened the epic PR, and the run said so and stopped.
func completedRunWithAnOpenPR(t *testing.T, repo string) {
	t.Helper()
	attempt := 1
	writeFeedEvent(t, repo, "epic-qeu", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "epic-qeu", "a1", &attempt, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "epic-qeu", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "epic-qeu", "", nil, "run_finished",
		"completed: every tick closed behind the gate; the close-out opened the epic PR"))
}

func TestWatchOfACompletedRunWithAnOpenPRExitsHeldOnEveryPath(t *testing.T) {
	fakeTheForge(t, &statusmodel.CIInput{
		State: "green",
		PR: &statusmodel.PR{
			Number: 12, URL: "https://github.com/example/ticfac/pull/12",
			HeadRef: "epic/qeu", HeadSHA: "9f2ab", BaseRef: "main",
		},
		Checks: []statusmodel.CheckState{{Name: "go", Status: "completed", Conclusion: "success"}},
	})

	// The pipe: the same ending the live view answers 3 for, and the line
	// that says WHAT the run holds — the PR, the one thing a person moves.
	repo := t.TempDir()
	completedRunWithAnOpenPR(t, repo)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"watch", "--repo", repo, "epic-qeu"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("a piped watch of a completed run with an open PR exited %d, want the held code %d — the terminal answers 3 for the same ending; stderr %q",
			code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended holding something for a person") ||
		!strings.Contains(stderr.String(), "pull/12") {
		t.Errorf("the pipe's end does not name what the run holds for a person:\n%s", stderr.String())
	}

	// The document: one answer, state held, the wait kind a person can read
	// without parsing prose — and the code agrees with it.
	repo = t.TempDir()
	completedRunWithAnOpenPR(t, repo)
	doc, _, code := jsonAnswer(t, []string{"watch", "--json", "--repo", repo, "epic-qeu"})
	if code != ExitHeld {
		t.Fatalf("watch --json of a completed run with an open PR exited %d, want %d", code, ExitHeld)
	}
	mustSchema(t, doc, "ticfac.watch.v1")
	if doc["state"] != agentStateHeld {
		t.Errorf("the document's state word is %v, want %q — the exit code and it must agree", doc["state"], agentStateHeld)
	}
	held, ok := doc["held"].(map[string]any)
	if !ok || held["kind"] != "merge" {
		t.Fatalf("the document does not carry the merge hold:\n%v", doc)
	}

	// The terminal: the live view's own end, the one that was already 3 —
	// pinned here so the three paths are held to one answer by one test.
	repo = t.TempDir()
	completedRunWithAnOpenPR(t, repo)
	fakeTerminal(t)
	stdout, stderr = bytes.Buffer{}, bytes.Buffer{}
	code = Run([]string{"watch", "--repo", repo, "epic-qeu"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("a terminal watch of a completed run with an open PR exited %d, want %d", code, ExitHeld)
	}
	if !strings.Contains(stderr.String(), "ended holding something for a person") ||
		!strings.Contains(stderr.String(), "pull/12") {
		t.Errorf("the live view's last word does not name what the run holds:\n%s", stderr.String())
	}
}
