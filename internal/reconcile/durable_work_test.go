package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The durability of an attempt's work at the moment it is REJECTED (ticfac
// tick 55i): settle --release promises that "whatever this one committed
// stays on its own write ref", and that promise was only true if the ref had
// reached origin — which it had not, because the only push lived in
// integrate's durableAttemptHead, and an attempt refused at COLLECT never
// reaches integrate. Its commits existed as a local branch in one worktree,
// one `git worktree remove --force` away from gone — and the executor's own
// disposal does exactly that.
//
// The fixture drives the exact shape: origin refuses every write to an
// attempt's ref for the whole of the attempt's life (the herdr reality, and
// the local executor's when every supervisor push fails), and comes back at
// the moment the run takes custody of the report. What the collect does with
// the work BEFORE it records the rejection is then the whole question.

// holdAttemptPushes makes origin refuse every write to an attempt's write ref
// while the hold file exists — the shape of a remote that is unreachable for
// the whole of an attempt's life, so its commits exist only in this checkout
// until somebody makes them durable.
func holdAttemptPushes(t *testing.T, origin string) (release func()) {
	t.Helper()
	hold := filepath.Join(filepath.Dir(origin), "origin-holds")
	if err := os.WriteFile(hold, []byte("origin is unreachable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(origin, "hooks", "update")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"refs/heads/ticfac/*)\n" +
		"\tif [ -e " + hold + " ]; then\n" +
		"\t\techo 'the fixture refuses every write to this ref' >&2\n" +
		"\t\texit 1\n" +
		"\tfi\n" +
		"\t;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Remove(hold) }
}

// openingExecutor opens origin at the moment the run takes custody of the
// attempt's report: CollectDetail is the seam the collect reads through, and
// nothing before it in the run's flow can put the work anywhere — by then the
// attempt has settled and its supervisor's final push has already failed.
type openingExecutor struct {
	Executor
	release func()
}

func (e *openingExecutor) CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error) {
	e.release()
	return e.Executor.CollectDetail(handle)
}

// A rejected attempt holding commits has those commits durable BEFORE the
// rejection is recorded: after the run has refused the attempt, the commits
// are findable on origin — not only in a worktree the refusal is about to
// remove.
func TestARejectedAttemptsWorkIsDurableBeforeTheRejectionIsRecorded(t *testing.T) {
	t.Parallel()
	silent := fixtureOptions{mode: "silent"}
	f := newFixture(t, silent)
	release := holdAttemptPushes(t, f.Repo.Origin)
	f.wrap = func(inner Executor) Executor {
		return &openingExecutor{Executor: inner, release: release}
	}

	_, result, err := f.run(f.Repo, silent)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCollect {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedCollect)
	}

	marker := attemptMarker(t, f, "a1", 1)
	branch := branchOf(marker.WriteRef)
	local := branchHead(f.Repo.Dir, branch)
	if local == "" || local == marker.BaseSHA {
		t.Fatalf("the fixture left no commit on %s; there is nothing to make durable", branch)
	}

	// Where the commits can be found: on ORIGIN, at the head the local branch
	// holds. This is the assertion the acceptance criterion names — after a
	// rejection with work, a person reads the location off the remote, not
	// out of a worktree the refusal has already torn down.
	remote := remoteHeadOf(t, f, branch)
	if remote != local {
		t.Fatalf("the rejected attempt's work is at %s on origin, want the local head %s: "+
			"a rejected attempt holding commits must have them durable before the rejection is recorded",
			short(remote), short(local))
	}

	// The rejection IS recorded — durably, on origin — and the attempt's
	// worktree is gone; the branch is kept, and the commits on it are the
	// same ones origin now carries.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("read the run's checkpoint from origin: %v", err)
	}
	rejected := false
	for _, ts := range checkpoint.Ticks {
		if ts.TickID == "a1" && ts.State == "rejected" && ts.Attempt == 1 {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("the rejection is not recorded on origin; a rejection only this process remembers is one the next run re-collects")
	}
	assertOneWorktree(t, f.Repo.Dir)
	if now := branchHead(f.Repo.Dir, branch); now != local {
		t.Errorf("%s is at %q, not at the %s the work is on", branch, now, short(local))
	}

	// And the settlement's promise says where the commits are, because it
	// asked: durable, on the write ref, at the head a person can check out.
	settler, err := New(f.options(f.Repo, silent))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle the rejected attempt: %v", err)
	}
	if !settled.Recorded {
		t.Fatalf("the settlement recorded nothing: %+v", settled)
	}
	if settled.WorkSHA != local || settled.WorkRef != marker.WriteRef || !settled.WorkDurable {
		t.Errorf("the settlement says the work is %q at %q (durable %t); want %s at %s, durable: "+
			"a released attempt's commits must be findable where the release says they are",
			settled.WorkRef, short(settled.WorkSHA), settled.WorkDurable, marker.WriteRef, short(local))
	}
	if strings.Contains(settled.WorkIn, "/") {
		t.Errorf("the settlement names a local checkout %q for work that is on origin", settled.WorkIn)
	}
}
