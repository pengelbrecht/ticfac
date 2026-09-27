package reconcile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The repair a restart finishes (epic-2jn, 2026-09-27). 9sz's merge landed,
// the integrated gate failed, and the repair job ran and settled — and then
// the run was restarted before the repair's merge was pushed. The resumed run
// found the settled repair and finished it from its branch, and in doing so
// deleted the ATTEMPT's branch on origin instead of the repair's (the
// finish-from-branch path worked with the attempt's marker). The gate over
// the repaired tree then passed, and the freshness check read the absent
// attempt branch as an attempt head of "" and refused the passing gate as
// stale; the pass after that found the attempt's head only in the local
// checkout and held the tick as rejected work nobody merged — while that very
// head was on the integration branch. A person closed the tick by hand.
//
// Each test is a full fixture run, skipped under -short (shorttest.EndToEnd).

// repairCollected cuts the run the moment the REPAIR job's collect is
// recorded: the job has settled and pushed its branch, and nothing of its
// merge has happened yet — the window the epic-2jn restart landed in.
func repairCollected() func(Event) bool {
	done := false
	return func(e Event) bool {
		if done || e.Tick != "a1" || e.Stage != StageCollected || !strings.Contains(e.Detail, "the repair job") {
			return false
		}
		done = true
		return true
	}
}

func TestARestartMidRepairFinishesTheRepairAndClosesTheTick(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	for _, freshClone := range []bool{false, true} {
		name := "same checkout"
		if freshClone {
			name = "fresh clone"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, fixtureOptions{mode: "gate_break_repair", gate: repairGate})
			seedStaleReference(t, f)

			_, _, err := f.run(f.Repo, fixtureOptions{mode: "gate_break_repair", gate: repairGate,
				stopAfter: repairCollected()})
			killedAfter(t, err, "a1", StageCollected)

			repo := f.Repo
			if freshClone {
				repo = cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
			}
			r, result, err := f.run(repo, fixtureOptions{mode: "gate_break_repair", gate: repairGate})
			if err != nil {
				t.Fatalf("the restart did not finish: %v", err)
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the restart ended %s (%s): a repair that settled before the restart is finished, "+
					"gated and closed, not a stop", result.State, result.Reason)
			}
			stages := r.Stages("a1")
			for _, bad := range []string{StageStale, StageRejected} {
				if contains(stages, bad) {
					t.Errorf("a1's stages %v include %s: the repaired, passing gate was not honoured", stages, bad)
				}
			}
			if !contains(stages, StageClosed) {
				t.Errorf("a1's stages %v record no close", stages)
			}
			current, err := f.Tracker.Show(context.Background(), "a1")
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != "closed" {
				t.Errorf("a1 is %s: the repair's passing gate did not close it", current.Status)
			}
			if n := repairDispatchCount(t, f); n != 1 {
				t.Errorf("%d repair jobs were dispatched; the settled one is finished, not paid for twice", n)
			}

			// The repair's merge names the REPAIR's branch, and the recorded
			// decision does too: the attempt's branch is not what the repair
			// wrote.
			clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
			merges := runGitQuiet(clone.Dir, "log", "--merges", "--format=%s", "origin/epic/qeu")
			if !strings.Contains(merges, "tick-a1/repair-1") {
				t.Errorf("the integration branch carries no merge of the repair job's branch:\n%s", merges)
			}
			decision, ok, err := r.repairDecisionOf("a1")
			if err != nil || !ok {
				t.Fatalf("no repair decision recorded: %v", err)
			}
			if branch, _ := decision.Request["repair_branch"].(string); branch != "ticfac/run-r-fixture/tick-a1/repair-1" {
				t.Errorf("the recorded repair names branch %q, not the repair's own", branch)
			}
		})
	}
}

// The hold's half of epic-2jn. A tick rejected after its merge landed — here
// a gate failure whose repair asked for a person — whose attempt branch is
// then GONE from origin, while its head is on the integration branch. A
// person fixes the tree on the epic branch and resumes. The attempt is
// integrated: it is finished from the integration branch (gated and closed),
// never held as "work nobody merged" because origin lacks its branch — in the
// checkout that still has the local branch, and in a fresh clone that has
// only the head the run recorded.
func TestARejectedAttemptWhoseBranchIsGoneButWhoseHeadIsIntegratedIsFinished(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	for _, freshClone := range []bool{false, true} {
		name := "same checkout"
		if freshClone {
			name = "fresh clone"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, fixtureOptions{mode: "gate_break_unresolvable", gate: repairGate})
			seedStaleReference(t, f)
			_, result, err := f.run(f.Repo, fixtureOptions{mode: "gate_break_unresolvable", gate: repairGate})
			if err != nil {
				t.Fatalf("the first run did not finish: %v", err)
			}
			if result.State != runstate.StateFailed {
				t.Fatalf("the first run ended %s: the fixture's repair asks for a person", result.State)
			}

			// The attempt's branch is gone from origin; its head is on the
			// integration branch.
			attemptBranch := "ticfac/run-r-fixture/tick-a1/attempt-1"
			head := runGitQuiet(f.Repo.Dir, "rev-parse", "--verify", "--quiet", attemptBranch)
			if head == "" {
				t.Fatalf("the checkout has no local %s; this fixture proves nothing", attemptBranch)
			}
			if !containsCommit(t, f, head, "origin/epic/qeu") {
				t.Fatal("the attempt's head is not on the integration branch; this fixture proves nothing")
			}
			mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", ":refs/heads/"+attemptBranch)

			// A person fixes the tree on the integration branch.
			fixer := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "fixer"))
			mustRun(t, fixer.Dir, "git", "checkout", "--quiet", "-B", "fix", "origin/epic/qeu")
			write(t, filepath.Join(fixer.Dir, "check.sh"), "cat README.md >/dev/null\n")
			mustRun(t, fixer.Dir, "git", "commit", "--quiet", "-am", "fix the harness by hand")
			mustRun(t, fixer.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")

			repo := f.Repo
			if freshClone {
				repo = cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
			}
			r, result, err := f.run(repo, fixtureOptions{mode: "gate_break_unresolvable", gate: repairGate})
			if err != nil {
				t.Fatalf("the resume did not finish: %v", err)
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the resume ended %s (%s): an attempt whose head is integrated is finished from the "+
					"integration branch, not held; a1's stages %v", result.State, result.Reason, r.Stages("a1"))
			}
			stages := r.Stages("a1")
			if contains(stages, StageStale) || contains(stages, StageDispatched) {
				t.Errorf("a1's stages %v: the integrated attempt was refused as stale or dispatched over", stages)
			}
			if !contains(stages, StageGatePassed) || !contains(stages, StageClosed) {
				t.Errorf("a1's stages %v: the integrated attempt was not gated and closed", stages)
			}
		})
	}
}

// The freshness check's target, with the attempt's branch absent on origin.
// The target's attempt head is the RECORDED one when the integration branch
// carries it — nothing moved, so a passing gate is publishable — and stays
// "" (stale) when it does not; a branch present at another head is still a
// move.
func TestTheFreshnessTargetOfAnAbsentAttemptBranchIsTheRecordedContainedHead(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run did not complete: %v %+v", err, result)
	}
	attempt, ok, err := r.store.Attempt(1)
	if err != nil || !ok {
		t.Fatalf("no attempt 1 on the run branch: %v", err)
	}
	marker := handleFromMap(attempt.JobHandle)
	branch := branchOf(marker.WriteRef)
	if head, _ := r.git.remoteHead(branch); head != "" {
		mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", ":refs/heads/"+branch)
	}
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin")
	epicHead := runGitQuiet(f.Repo.Dir, "rev-parse", "origin/epic/qeu")
	recorded := runGitQuiet(f.Repo.Dir, "rev-parse", "origin/epic/qeu~1")

	target, err := r.currentTarget(marker, merge{AttemptHead: recorded, GateSHA: epicHead})
	if err != nil {
		t.Fatal(err)
	}
	if target["attempt_head"] != recorded {
		t.Errorf("an absent attempt branch whose recorded head %s the integration branch carries reads as %q",
			short(recorded), target["attempt_head"])
	}
	gated := Fingerprint{"attempt_head": recorded}
	if moved := gated.Mismatch(Fingerprint{"attempt_head": target["attempt_head"]}); len(moved) != 0 {
		t.Errorf("a passing gate over the recorded head is refused as stale: %v", moved)
	}

	// A recorded head the integration branch does NOT carry is not vouched
	// for by the absence: the target stays empty and the gate is stale.
	write(t, filepath.Join(f.Repo.Dir, "elsewhere.txt"), "not integrated\n")
	mustRun(t, f.Repo.Dir, "git", "checkout", "--quiet", "-B", "elsewhere", epicHead)
	mustRun(t, f.Repo.Dir, "git", "add", "elsewhere.txt")
	mustRun(t, f.Repo.Dir, "git", "commit", "--quiet", "-m", "a head nothing integrated")
	stray := runGitQuiet(f.Repo.Dir, "rev-parse", "HEAD")
	target, err = r.currentTarget(marker, merge{AttemptHead: stray, GateSHA: epicHead})
	if err != nil {
		t.Fatal(err)
	}
	if target["attempt_head"] != "" {
		t.Errorf("an absent branch vouched for a head the integration branch does not carry: %q", target["attempt_head"])
	}

	// A branch PRESENT at another head is a move, whatever was recorded.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", stray+":refs/heads/"+branch)
	target, err = r.currentTarget(marker, merge{AttemptHead: recorded, GateSHA: epicHead})
	if err != nil {
		t.Fatal(err)
	}
	if target["attempt_head"] != stray {
		t.Errorf("a branch moved to %s reads as %q", short(stray), target["attempt_head"])
	}
}
