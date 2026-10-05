package cli

// The epic a run id belongs to, when the id itself names no epic (tick mwt).
//
// THE PROBLEM. A local run's id is its epic's name only for the local
// spelling epic-<epic-id>. Every other spelling — today's cloud runs
// resumed locally, whose orchestrator keeps the factory's run_<hex> —
// carries the epic in exactly one durable place: the run's own checkpoint,
// at `.ticfac/runs/<run-id>/checkpoint.json`, committed to the epic's
// integration branch (epic/<epic-id>). A surface that knows only the run id
// (the machine's registry names one, nothing more) cannot fetch that branch
// — its name is the very thing missing — so the records were never found
// and the row's model carried epic_id "". Everything the epic should have
// carried was lost with it: the superseded marking (a row with an empty
// epic never enters markOverviewHistory's `latest`), and the operand of
// every clearing command the row printed.
//
// THE ANSWER. The epic branch is found by LOOKING for the run's checkpoint
// instead of guessing its branch: every epic branch origin holds is fetched
// once per repository per process into this process's private ref namespace
// (the store's own convention — never FETCH_HEAD, never a shared ref), and
// the run's checkpoint is read from whichever branch carries it. The
// checkpoint names both ids, and the run's own id is checked against the
// one asked for: a directory a branch carries for another run is that run's
// record, not this one's answer.
//
// THE BOUNDS. The scan is memoized per run id for the life of the process:
// a run's epic is a fact about the run and does not move, and the watch
// re-gathers its model every frame — a scan per frame would be a fetch per
// frame. The fetch is best effort: a checkout without a remote, or one
// whose remote cannot be asked, falls back to the refs the checkout itself
// holds — local epic branches and origin's remote-tracking ones — and a
// run nothing resolves stays as unattributed as before, its rows now
// naming no clearing command rather than one with a hole in it.

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// runScanSeq distinguishes one process's scans from any other's, the same
// way the store's fetch ids do: the private ref namespace a scan fetches
// into is per process, so two processes — or a test's children — never race
// on one ref.
var runScanSeq atomic.Uint64

// epicBranchRefsMemo is the once-per-repository list of refs that may carry
// run checkpoints: the branches this process fetched from origin beside the
// ones the checkout itself holds. Keyed by repo; the value's Once makes one
// fetch serve every run the process answers for.
var epicBranchRefsMemo sync.Map // repo → *epicBranchRefs

// epicBranchRefs is the memoized half of the scan: one fetch, one listing.
type epicBranchRefs struct {
	once sync.Once
	refs []string
}

// epicBranchRefsOf is the refs that may hold run checkpoints, fetched and
// listed once per repository for the life of the process.
func epicBranchRefsOf(repo string) []string {
	entry, _ := epicBranchRefsMemo.LoadOrStore(repo, &epicBranchRefs{})
	refs := entry.(*epicBranchRefs)
	refs.once.Do(func() { refs.refs = scanEpicBranchRefs(repo) })
	return refs.refs
}

// scanEpicBranchRefs lists the epic branches a run's records may sit on: the
// ones origin holds, fetched into this process's private namespace, beside
// the ones the checkout itself holds. The fetch never touches FETCH_HEAD or
// a shared ref (the store's own rules, and the Phase 3 review's lesson
// beside them), and its failure costs only the remote half: a checkout with
// no remote still answers from its local refs, and one with a silent remote
// answers with what it last fetched.
func scanEpicBranchRefs(repo string) []string {
	ctx := context.Background()
	id := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(runScanSeq.Add(1), 10)
	namespace := "refs/ticfac/peek/runscan/" + id
	_, _ = trackerGitOut(ctx, repo, "fetch", "--quiet", "--no-write-fetch-head", "--refmap=",
		"origin", "+refs/heads/epic/*:"+namespace+"/epic/*")
	refs := []string{}
	for _, pattern := range []string{
		namespace + "/epic/",
		"refs/heads/epic/",
		"refs/remotes/origin/epic/",
	} {
		out, err := trackerGitOut(ctx, repo, "for-each-ref", "--format=%(refname)", pattern)
		if err != nil {
			continue // a pattern that lists nothing is an empty half, not a failure
		}
		refs = append(refs, strings.Fields(out)...)
	}
	return refs
}

// runEpicMemo is the per-run answer, keyed by repo and run id: a run's epic
// does not move, and the watch re-gathers a model every frame.
var runEpicMemo sync.Map // repo \x00 runID → *runEpicAnswer

// runEpicAnswer is one run's memoized resolution, computed by whichever
// caller asks first while the rest wait on the same Once.
type runEpicAnswer struct {
	once sync.Once
	epic string
}

// epicOfUnhintedRun is the epic a run id belongs to when the id names no
// epic — "" when nothing this repository can read says: the honest
// unattributed answer the rows already knew, never a guess from the id's
// own shape. Callers pass the repo the run is read IN (the overview: the
// repo its registration names; status and the watch: the checkout being
// asked), so the answer is a fact about what that checkout can read, the
// same boundary every other record read here holds.
func epicOfUnhintedRun(repo, runID string) string {
	if repo == "" || runID == "" {
		return ""
	}
	entry, _ := runEpicMemo.LoadOrStore(repo+"\x00"+runID, &runEpicAnswer{})
	answer := entry.(*runEpicAnswer)
	answer.once.Do(func() { answer.epic = scanEpicOfRun(repo, runID) })
	return answer.epic
}

// scanEpicOfRun reads the run's own checkpoint from whichever epic branch
// the repository holds it on — the one durable record that names both the
// run and its epic — and answers the epic the checkpoint states. A branch
// that carries nothing of the run, or a checkpoint that names another run,
// is not this run's answer; a run no branch carries answers "".
func scanEpicOfRun(repo, runID string) string {
	ctx := context.Background()
	path := runstate.CheckpointPath(runID)
	for _, ref := range epicBranchRefsOf(repo) {
		raw, err := trackerGitOut(ctx, repo, "cat-file", "blob", ref+":"+path)
		if err != nil {
			continue // this branch carries no checkpoint for the run
		}
		var checkpoint runstate.Checkpoint
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&checkpoint); err != nil {
			continue // an unreadable checkpoint is not this read's answer
		}
		if checkpoint.RunID == runID && checkpoint.EpicID != "" {
			return checkpoint.EpicID
		}
	}
	return ""
}

// epicHintOfRunID is the cheap first guess at a run's epic id — from the
// id's own shape when it names one, else resolved from the run's checkpoint
// on the epic branches the repo holds (epicOfUnhintedRun, tick mwt), and ""
// when neither answers. It is the hint the overview's cheap row starts
// from, so the history rules that read the cheap row can see the epic
// before any full gather.
func epicHintOfRunID(repo, runID string) string {
	if rest, ok := strings.CutPrefix(runID, "epic-"); ok && rest != "" {
		return rest
	}
	return epicOfUnhintedRun(repo, runID)
}
