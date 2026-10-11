package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

type fallbackExecutor struct {
	Executor
	detail string
}

func (e fallbackExecutor) StartFallback(*subprocess.JobHandle) (string, bool) {
	return e.detail, e.detail != ""
}

// A job its executor started on a fallback substrate — a herdr pane that
// could not start, run on the headless runner instead (tick o35) — leaves a
// feed line saying so, and one that did not fall back leaves none.
func TestAStartFallbackLeavesAFeedLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.landingOptions(fixtureOptions{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	r.noteStartFallback(fallbackExecutor{detail: "the herdr pane could not start (claude is waiting at its " +
		"folder-trust dialog in /wt); the job runs on the headless claude runner instead"}, "a1", &subprocess.JobHandle{})
	detail, ok := journalLine(r, "a1", StageStartFellBack)
	if !ok || !strings.Contains(detail, "folder-trust dialog in /wt") {
		t.Fatalf("no fallback line naming why (%q)\n%s", detail, journalText(r))
	}
	r.noteStartFallback(fallbackExecutor{}, "a2", &subprocess.JobHandle{})
	if _, ok := journalLine(r, "a2", StageStartFellBack); ok {
		t.Error("a job that did not fall back left a fallback line")
	}
}
