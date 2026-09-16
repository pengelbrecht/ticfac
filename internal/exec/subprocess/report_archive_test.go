package subprocess

import (
	"os"
	"path/filepath"
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

// TestDisposeKeepsTheReportWhenNothingCollected is the Phase 3 review's
// finding 4, and the other half of tick 35h's guarantee.
//
// collect archives the report, but dispose is reached without a collect:
// settle.go cancels and disposes a RELEASED attempt. So a report a person
// released — the case where someone most needs to read what the worker
// concluded — was deleted with the worktree, unread. herdr's dispose archived
// it; the local executor's did not.
func TestDisposeKeepsTheReportWhenNothingCollected(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-35h/tick-sss/attempt-1", "sss"))
	f.waitSettled(handle)

	local, _ := handle.Local()
	wanted, err := os.ReadFile(local.ResultPath)
	if err != nil {
		t.Fatalf("the attempt wrote no report to lose: %v", err)
	}

	// No collect: cancel then dispose, exactly what settle.go does for a
	// released attempt (the credential must die before the teardown).
	if _, err := f.Executor.Cancel(handle); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := f.Executor.Dispose(handle, DisposeOptions{Reason: "released by a person", KeepBranch: true}); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Fatalf("the worktree should be gone: %v", err)
	}

	archived, err := os.ReadFile(filepath.Join(local.State, fileReportArchive))
	if err != nil {
		t.Fatalf("the released attempt's report did not survive disposal: %v", err)
	}
	if string(archived) != string(wanted) {
		t.Error("the archived report is not what the attempt wrote")
	}
}
