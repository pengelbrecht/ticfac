package reconcile

import (
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Epic hn6, run_09ebaf29 (2026-09-30): the run folded main into epic/hn6,
// the fold conflicted, and the resolve-conflict job was dispatched to a cloud
// worker on the CONFLICTED MERGE — a commit the run made in its own clone and
// never pushed. The container cloned origin, could not check the commit out
// ("reference is not a tree … is it pushed?"), and died; the run read the
// empty landing branch as missing-result, redispatched the same job twice
// more, and halted for a person. Nothing about the conflict was ever tried.

// originCheckout stands in for an executor whose job runs off a checkout of
// ORIGIN (the cloudflare-sandbox executor): it answers ChecksOutFromOrigin,
// and every start records whether origin could serve the start commit at that
// moment — which is exactly what the container's clone would find.
type originCheckout struct {
	Executor
	origin  string
	mu      *sync.Mutex
	starts  *[]string
	missing *[]string
}

func (e *originCheckout) ChecksOutFromOrigin() bool { return true }

func (e *originCheckout) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	base := spec.Source.BaseSHA
	served := strings.TrimSpace(runGitQuiet(e.origin, "for-each-ref", "--contains", base, "--format=%(refname)")) != ""
	e.mu.Lock()
	*e.starts = append(*e.starts, spec.JobID)
	if !served {
		*e.missing = append(*e.missing, spec.JobID+" at "+short(base))
	}
	e.mu.Unlock()
	return e.Executor.Start(spec)
}

// Every job dispatched to an executor that checks out from origin starts from
// a commit origin serves — the base fold's resolve job (the hn6 case) and
// every tick attempt after it — because the run publishes the start commit
// under a run-owned ref before it dispatches, in the one place every start
// goes through.
func TestEveryJobForAnOriginCheckoutStartsFromACommitOriginServes(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{mode: "conflict"}
	f := newFixture(t, opts)
	baseFoldConflict(t, f)

	var mu sync.Mutex
	var starts, missing []string
	f.wrap = func(inner Executor) Executor {
		return &originCheckout{Executor: inner, origin: f.Repo.Origin, mu: &mu, starts: &starts, missing: &missing}
	}

	_, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s)", result.State, result.Reason)
	}
	if resolveStarts(f) == 0 {
		t.Fatal("no resolve-conflict job was dispatched for the fold: this fixture cannot show the hn6 case")
	}
	if len(missing) != 0 {
		t.Errorf("jobs were dispatched to an origin checkout on commits origin does not serve (%d of %d starts): %v — "+
			"each is a container that dies on its checkout", len(missing), len(starts), missing)
	}
	// And they retire with the run: a completed run leaves no start ref behind.
	if refs := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "for-each-ref", "--format=%(refname)",
		"refs/ticfac/start/")); refs != "" {
		t.Errorf("the completed run left its published start commits on origin:\n%s", refs)
	}
}

// A job whose executor says it could not check out its start commit because
// origin does not serve it (subprocess.Collection.StartNotOnOrigin) is never
// redispatched as is: the orchestrator publishes the commit, says so, and only
// then lets the collect's missing-result go on to its retry.
func TestAStartNotOnOriginCollectPublishesTheCommitBeforeAnyRetry(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{}
	f := newFixture(t, opts)
	r, _, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}

	writeUnder(t, f.Repo.Dir, "local-only.txt", "a commit only the orchestrator's clone holds\n")
	mustRun(t, f.Repo.Dir, "git", "add", "-A")
	mustRun(t, f.Repo.Dir, "git", "commit", "--quiet", "-m", "the conflicted merge nobody pushed")
	sha := strings.TrimSpace(mustRun(t, f.Repo.Dir, "git", "rev-parse", "HEAD"))
	if mustRunAllowingFailure(f.Repo.Origin, "git", "cat-file", "-e", sha+"^{commit}") {
		t.Fatal("origin already has the commit: this test cannot show the publish")
	}

	collected := &subprocess.Collection{Verdict: subprocess.VerdictMissingResult, StartNotOnOrigin: sha}
	if err := r.startNotOnOrigin("a1", collected); err != nil {
		t.Fatalf("startNotOnOrigin: %v", err)
	}
	served := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "for-each-ref", "--contains", sha, "--format=%(refname)"))
	if served != startRef(r.runID, "a1", sha) {
		t.Errorf("origin serves %s from %q, want the run-owned start ref %s", short(sha), served,
			startRef(r.runID, "a1", sha))
	}
	said := false
	for _, event := range r.Journal() {
		said = said || (event.Stage == StageStartPublished && strings.Contains(event.Detail, short(sha)))
	}
	if !said {
		t.Errorf("the feed never says the start commit %s was published (%s)", short(sha), StageStartPublished)
	}
}

// An executor that works in the orchestrator's own repository needs nothing
// published: the run pushes no start refs for it.
func TestALocalExecutorPublishesNoStartRefs(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{mode: "conflict"}
	f := newFixture(t, opts)
	baseFoldConflict(t, f)

	_, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s)", result.State, result.Reason)
	}
	if refs := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "for-each-ref", "--format=%(refname)",
		"refs/ticfac/start/")); refs != "" {
		t.Errorf("a local executor's run published start refs to origin:\n%s", refs)
	}
}
