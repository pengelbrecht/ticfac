package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Publishing a job's start commit before it is dispatched to an executor that
// cannot see this orchestrator's repository (epic hn6, run_09ebaf29).
//
// WHAT WAS WRONG. Several jobs start from a commit the run made in its OWN
// clone and never pushed: the base fold's resolve job starts from the
// conflicted fold, a tick merge's resolve job from the conflicted merge, a
// repair from the merge whose gate failed. A local executor works in a
// worktree of this repository, so it never mattered. The cloudflare-sandbox
// executor's container clones ORIGIN at the start commit — and hn6's fold of
// main dispatched three resolve jobs onto a merge origin had never seen. Each
// container died on "reference is not a tree … is it pushed?", each empty
// landing branch read as missing-result, and the run halted for a person
// without the conflict ever being looked at.
//
// THE RULE. Whether an executor needs this is the EXECUTOR's to say
// (RemoteCheckout), and the publish happens in the one place every start goes
// through (startWithRoom) — never per job kind, so a job kind added later
// cannot forget it. The commit is pushed under a run-owned ref outside
// refs/heads, so no branch listing, no attempt-ref reader and no sweep of the
// run's branches mistakes it for work; GitHub serves a SHA reachable from any
// ref to a fetch by SHA, which is how the container fetches its start. The
// refs retire with their tick (sweep.go's pruneStartRefs).

// RemoteCheckout is an executor whose jobs run off a checkout of ORIGIN rather
// than of the orchestrator's own repository, and therefore cannot start from a
// commit only this clone holds.
type RemoteCheckout interface {
	ChecksOutFromOrigin() bool
}

// startRefPrefix is the namespace one run's published start commits live under.
func startRefPrefix(runID string) string {
	return "refs/ticfac/start/run-" + runID + "/"
}

// startRef is the run-owned ref one start commit is published under: per tick
// (a base fold's job names no tick, and is the run's own), per commit.
func startRef(runID, tick, sha string) string {
	scope := "run"
	if tick != "" {
		scope = "tick-" + tick
	}
	return startRefPrefix(runID) + scope + "/" + sha
}

// checksOutFromOrigin asks the executor whether its jobs start from origin.
func checksOutFromOrigin(executor Executor) bool {
	remote, ok := executor.(RemoteCheckout)
	return ok && remote.ChecksOutFromOrigin()
}

// publishStart makes the start commit of `spec` one origin serves, when the
// executor it is about to be dispatched to checks out from origin. A publish
// that fails is the start's failure: a job dispatched anyway is a container
// that dies on its checkout, reported as a job that answered nothing.
func (r *Reconciler) publishStart(executor Executor, tick string, spec *subprocess.JobSpec) error {
	if spec == nil || spec.Source.BaseSHA == "" || !checksOutFromOrigin(executor) {
		return nil
	}
	return r.publishCommit(tick, spec.Source.BaseSHA)
}

// publishCommit pushes one commit to origin under its run-owned start ref. A
// ref already there at the commit is an up-to-date push, nothing more.
func (r *Reconciler) publishCommit(tick, sha string) error {
	if r.git == nil || r.git.remote == "" {
		return nil
	}
	ref := startRef(r.runID, tick, sha)
	if _, err := r.git.run("", "push", "--quiet", r.git.remote, sha+":"+ref); err != nil {
		return fmt.Errorf("publish the start commit %s to %s as %s before dispatching a job to an executor "+
			"that checks out from %s: %w", short(sha), r.git.remote, ref, r.git.remote, err)
	}
	return nil
}

// startNotOnOrigin is the orchestrator's answer to a job whose executor could
// not check out its start commit because origin does not serve it
// (subprocess.Collection.StartNotOnOrigin). The collect's verdict stays the
// missing-result it is — the job never answered — but it is never
// redispatched AS IS: the commit is published here, before the retry, and a
// publish that fails is the error the collect returns, so the run stops on
// the cause rather than paying for the same doomed container again.
func (r *Reconciler) startNotOnOrigin(tick string, collected *subprocess.Collection) error {
	if collected == nil || collected.StartNotOnOrigin == "" {
		return nil
	}
	sha := collected.StartNotOnOrigin
	if err := r.publishCommit(tick, sha); err != nil {
		return fmt.Errorf("the job could not check out its start commit %s because origin does not serve it, "+
			"and publishing it failed: %w", short(sha), err)
	}
	r.record(tick, StageStartPublished,
		"the job could not check out its start commit %s: origin did not serve it. It is published now as %s, so "+
			"the next job is not a repeat of this one", short(sha), startRef(r.runID, tick, sha))
	return nil
}

// collectDetail is every job's collect: the executor's, and then the
// orchestrator's answer to a job that could not start from its commit.
func (r *Reconciler) collectDetail(executor Executor, handle *subprocess.JobHandle, tick string) (*subprocess.Collection, error) {
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, err
	}
	if err := r.startNotOnOrigin(tick, collected); err != nil {
		return nil, err
	}
	return collected, nil
}

// pruneStartRefs retires the published start commits of every tick in scope
// on origin (a base fold's, scoped "run", with the run-level sweep). A start
// commit is only ever needed by the job dispatched on it; a closed tick has
// no job left to dispatch. A deletion that fails is recorded and left for
// the next sweep.
func (r *Reconciler) pruneStartRefs(sweepable func(string) bool, say func(string, ...any)) {
	if r.git == nil || r.git.remote == "" || sweepable == nil {
		return
	}
	prefix := startRefPrefix(r.runID)
	out, err := r.git.run("", "ls-remote", r.git.remote, prefix+"*")
	if err != nil {
		say("not swept: the run's start refs on %s could not be listed (%s: %s); the next sweep retries",
			r.git.remote, remoteFailure(err), firstLine(err.Error()))
		return
	}
	var gone []string
	for _, line := range strings.Split(out, "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		scope, _, _ := strings.Cut(strings.TrimPrefix(ref, prefix), "/")
		tick := strings.TrimPrefix(scope, "tick-")
		if scope == "run" {
			tick = ""
		}
		if sweepable(tick) {
			gone = append(gone, ref)
		}
	}
	if len(gone) == 0 {
		return
	}
	sort.Strings(gone)
	args := []string{"push", "--quiet", r.git.remote}
	for _, ref := range gone {
		args = append(args, ":"+ref)
	}
	if _, err := r.git.run("", args...); err != nil {
		say("not swept: the start refs %s on %s could not be deleted (%s: %s); the next sweep retries",
			strings.Join(gone, ", "), r.git.remote, remoteFailure(err), firstLine(err.Error()))
		return
	}
	say("swept: deleted the start refs %s on %s — nothing will be dispatched on them again",
		strings.Join(gone, ", "), r.git.remote)
}
