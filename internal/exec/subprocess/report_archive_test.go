package subprocess

import (
	"os"
	"strings"
	"testing"
)

// TestTheReportSurvivesTheWorktree is tick 35h's acceptance, and Phase 3's A5.
//
// The report is excluded from the branch by design and lives in the worktree,
// and the worktree is removed at teardown. So an attempt that was rejected or
// superseded took its prose with it. In the ticks pwp run that was five
// close-out reports and a 183-line review: the structured role_result survived,
// the reasoning did not, and every retry started blind.
//
// Here the worktree is deleted outright after collect — the teardown that
// never gives the executor a chance to save anything — and the report must
// still be readable, still referenced, and the reference must not dangle.
func TestTheReportSurvivesTheWorktree(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-35h/tick-rrr/attempt-1", "rrr"))
	f.waitSettled(handle)

	first := f.collect(handle)
	if !first.HasReport {
		t.Fatal("the attempt settled without a report; nothing to archive")
	}
	ref := reportRef(t, first)
	local, _ := handle.Local()
	if strings.HasPrefix(strings.TrimPrefix(ref.URI, "file://"), local.Worktree) {
		t.Fatalf("the report artifact points INTO the worktree (%s): it dangles the moment teardown runs", ref.URI)
	}
	archived, err := os.ReadFile(strings.TrimPrefix(ref.URI, "file://"))
	if err != nil {
		t.Fatalf("the archived report the artifact names does not exist: %v", err)
	}
	if digestOf(archived) != ref.ContentDigest {
		t.Error("the archive's content does not match the digest the result recorded")
	}

	// Teardown, at its most abrupt.
	if err := os.RemoveAll(local.Worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.ResultPath); !os.IsNotExist(err) {
		t.Fatalf("the worktree copy should be gone: %v", err)
	}

	again := f.collect(handle)
	if !again.HasReport {
		t.Fatal("after teardown the attempt reads as having no report: its analysis was lost with the worktree")
	}
	if again.Report.Status != first.Report.Status {
		t.Errorf("the report read back after teardown says %q, the original said %q", again.Report.Status, first.Report.Status)
	}
	if got := reportRef(t, again); got.ContentDigest != ref.ContentDigest {
		t.Errorf("the report referenced after teardown is not the one the attempt wrote")
	}
}

func reportRef(t *testing.T, c *Collection) ArtifactRef {
	t.Helper()
	for _, a := range c.Result.Artifacts {
		if a.Kind == "report" {
			return a
		}
	}
	t.Fatalf("the result carries no report artifact: %+v", c.Result.Artifacts)
	return ArtifactRef{}
}
