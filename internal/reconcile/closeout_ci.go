package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

// The close-out's CI read, and why it cannot simply ask about the PR's head.
//
// The run's durable state lives in .ticfac/ ON the integration branch, and the
// close-out gates on CI of the epic PR whose head IS that branch. So the act of
// gating rewrites the thing being gated: the run writes a checkpoint, the push
// moves the PR's head, and the close-out then asks the forge about a commit
// created seconds ago that no check has run on yet. It sees nothing, and either
// holds until its bound or refuses as "no CI has run at all".
//
// That is not a race that can be won by waiting. Every resume writes another
// checkpoint and moves the head again, so the next attempt asks about an even
// newer commit. Epic 9pd deadlocked exactly this way — heads cde6d962 ->
// 73fdbb59 -> ae0089e7, each refused, the last four commits on the branch all
// "update checkpoint.json", while CI sat green the whole time on whichever head
// had stood still long enough. An operator had to merge it by hand.
//
// The fix rests on what a run-state commit IS: a write under .ticfac/ and
// nothing else. It cannot change what the build compiles or what the tests run,
// so CI's verdict on an earlier commit is still a true statement about this
// tree's CODE — which is what the gate is actually about. So:
//
//   - the PR is RE-READ, because the head this run remembers is stale the
//     moment it checkpoints,
//   - CI is asked about the head first, the ordinary case,
//   - and when the head has no CI, the newest ancestor that DOES is found and
//     used, but only after proving that everything between it and the head
//     lives under .ticfac/.
//
// The proof is the point. If anything outside .ticfac/ changed since the last
// commit CI ran on, the verdict does not describe this tree and the answer is
// pending — a real wait for a real check, not a borrowed pass.
const (
	// ciWalkLimit bounds how far back a CI verdict is looked for. Tracker
	// pushes start no CI either (ticks 5ob/ciw), so the commits between the
	// last one CI ran on and the head are the run's record writes since its
	// last code landed: tens, not hundreds. Past the limit the answer is the
	// head's own, and dispatchSilentCI starts a run on it.
	ciWalkLimit = 60

	// runStatePrefix is the run's own durable state. landedAt sees past it
	// and nothing else: a tracker write the epic branch holds after its merge
	// is a write the base does not have yet.
	runStatePrefix = ".ticfac/"
)

// ciIgnoredPrefixes are the paths a commit may touch and still be one CI
// starts no run for (ci.yml's paths-ignore, kept equal to this list by
// TestTheWorkflowIgnoresExactlyTheCIIgnoredPaths) and one the gate can see
// past: the run's durable state, and the tracker's RECORDS - issues, the
// activity log, pending questions. A run pushes these to its epic branch
// several times a minute; when they started CI, epic/hn6 carried 591 CI runs,
// 86% of them cancelled by the next tracker push (docs/analysis/
// github-failures.md). None of them can change what CI says: no test reads
// the repository's own tracker records.
//
// NOT the rest of .tick/: runners.toml, config.md and their siblings are read
// by this repository's drift guards (the gate commands, the close-out rule),
// so a change to them can legitimately change CI's verdict and must start a
// run.
var ciIgnoredPrefixes = []string{runStatePrefix, ".tick/issues/", ".tick/activity/", ".tick/pending/"}

// ciIgnoredSubject is how a feed line names those paths.
const ciIgnoredSubject = "run state (.ticfac/) and tracker records (.tick/issues, activity, pending)"

// ciForTree answers what CI says about the code this PR would merge.
//
// It returns the report, the sha the report is about, and whether that sha is
// the PR's own head — callers say so in the feed, because "green on the head"
// and "green on the last commit that changed code" are different sentences and
// an operator is owed the true one.
func (r *Reconciler) ciForTree(ctx context.Context, pr *forge.PullRequest) (forge.CIReport, string, bool, error) {
	// Re-read the PR. The head this run holds was read before its own
	// checkpoint push, so it is stale by construction — which is the first
	// half of the defect this function exists for.
	if fresh, err := r.opts.PullRequests.Find(ctx, pr.HeadRef, pr.BaseRef); err == nil && fresh != nil && fresh.HeadSHA != "" {
		*pr = *fresh
	}

	report, sha, err := r.ciForCode(ctx, *pr, pr.HeadSHA, true)
	return report, sha, sha == pr.HeadSHA, err
}

// ciForCode answers what CI says about the CODE at one commit of the PR's
// branch: the commit's own verdict when CI ran on it, else the verdict of the
// newest ancestor CI ran on whose tree differs from it only under the paths
// CI ignores (ciIgnoredPrefixes) — and the sha that verdict is about.
//
// Three things make the answer truthful rather than merely available
// (epic-6in, 2026-09-28):
//
//   - A commit whose every check was SKIPPED has no verdict (forge answers
//     none for it): 6in's checkpoint head carried five skipped pull_request
//     checks, read green, and admitted a close-out over a red go job one
//     commit back. The walk below treats it as the absence it is.
//   - The walk ends where the code does. A commit outside the confined chain
//     describes other code, so no verdict from it or beyond is taken, and
//     none is looked for.
//   - When the chain's only verdicts were CANCELLED — a later push superseded
//     the run, and the push that superseded it changed only ignored paths, so
//     nothing replaced it — waiting cannot produce a verdict. With `restart`
//     the cancelled runs are restarted ONCE (forge.CIRestarter) and the answer
//     is pending: a real wait for a run that now exists.
func (r *Reconciler) ciForCode(ctx context.Context, pr forge.PullRequest, at string, restart bool) (forge.CIReport, string, error) {
	probe := pr
	probe.HeadSHA = at
	own, err := r.opts.PullRequests.CI(ctx, probe)
	if err != nil {
		return forge.CIReport{}, at, err
	}
	if own.State != forge.CINone {
		return own, at, nil
	}

	// Nothing ran on this commit. Look back along the chain of commits whose
	// code IS this commit's code for one CI did run on.
	running := false
	var cancelled []int64
	for _, sha := range r.sameCodeAncestors(pr.HeadRef, at) {
		probe.HeadSHA = sha
		report, err := r.opts.PullRequests.CI(ctx, probe)
		if err != nil {
			return forge.CIReport{}, at, err
		}
		switch report.State {
		case forge.CIGreen, forge.CIRed:
			// A CONCLUSIVE verdict about this very code: sameCodeAncestors
			// proved every change since it lives under ciIgnoredPrefixes.
			return report, sha, nil
		case forge.CIPending:
			// PENDING IS NOT A VERDICT, it is the absence of one, and taking
			// it would end the walk with the very answer the walk exists to
			// get past (tick tk3): a cancelled ancestor reads pending forever,
			// and a green one may sit one commit further on.
			if len(report.CancelledRuns) > 0 {
				cancelled = append(cancelled, report.CancelledRuns...)
			} else {
				running = true
			}
		}
	}
	if running {
		// A run is executing on this code: that is the wait, and nothing
		// needs restarting for it.
		return forge.CIReport{State: forge.CIPending}, at, nil
	}
	if len(cancelled) > 0 {
		if restart {
			if restarter, ok := r.opts.PullRequests.(forge.CIRestarter); ok {
				restarted, err := restarter.RestartCancelledOnce(ctx, cancelled)
				if err != nil {
					r.record("", StageCIRestarted, "the cancelled CI run(s) %v on the code of %s could not be "+
						"restarted: %v", cancelled, short(at), err)
				}
				if len(restarted) > 0 {
					r.record("", StageCIRestarted, "CI on the code of %s has no verdict: its run(s) were cancelled by "+
						"a later push that changed only %s, so nothing would ever run them again — workflow run(s) "+
						"%v are restarted ONCE, and the wait is for them", short(at), ciIgnoredSubject, restarted)
				}
			}
		}
		return forge.CIReport{State: forge.CIPending, CancelledRuns: cancelled}, at, nil
	}
	if restart && r.dispatchSilentCI(ctx, pr, at) {
		return forge.CIReport{State: forge.CIPending}, at, nil
	}
	return own, at, nil
}

// ciDispatchAfter is how long the code a PR would merge may carry NO CI run
// at all before the run starts the workflow itself: long enough for a push's
// own run to appear (tick ox0's "not yet" window, which is seconds), short
// enough that a close-out is not spent waiting for a run nobody will start.
const ciDispatchAfter = 3 * time.Minute

// dispatchSilentCI starts the CI workflow on the PR's branch, ONCE per commit
// per incarnation, when neither the commit nor any commit carrying its code
// has had a CI run for ciDispatchAfter (or half the run's CI bound, whichever
// is shorter). A push whose run was cancelled while still queued leaves no
// check run behind, and when the pushes after it changed only ignored paths
// nothing will ever run CI on that code: waiting — the whole bound, then a
// resume, then the bound again — waits for nothing. It answers whether it
// dispatched, so the caller says pending: a real wait for a run that exists.
func (r *Reconciler) dispatchSilentCI(ctx context.Context, pr forge.PullRequest, at string) bool {
	dispatcher, ok := r.opts.PullRequests.(forge.CIDispatcher)
	if !ok || pr.HeadRef == "" {
		return false
	}
	if r.ciSilentSince == nil {
		r.ciSilentSince = map[string]time.Time{}
		r.ciDispatched = map[string]bool{}
	}
	if r.ciDispatched[at] {
		return true
	}
	now := r.now()
	since, seen := r.ciSilentSince[at]
	if !seen {
		r.ciSilentSince[at] = now
		return false
	}
	after := ciDispatchAfter
	if bound := r.opts.GateTimeout / 2; bound > 0 && bound < after {
		after = bound
	}
	if now.Sub(since) < after {
		return false
	}
	workflow := r.closeoutRule.CIWorkflow
	if workflow == "" {
		workflow = DefaultCIWorkflow
	}
	if err := dispatcher.DispatchWorkflow(ctx, workflow, pr.HeadRef); err != nil {
		r.record("", StageCIRestarted, "the code of %s has had no CI run for %s and %s could not be started on %s: %v",
			short(at), now.Sub(since).Round(time.Second), workflow, pr.HeadRef, err)
		return false
	}
	r.ciDispatched[at] = true
	r.record("", StageCIRestarted, "the code of %s has had no CI run for %s — no run to wait for and none coming — so "+
		"%s is started on %s, once: an automatic intervention, and the wait is for that run",
		short(at), now.Sub(since).Round(time.Second), workflow, pr.HeadRef)
	return true
}

// sameCodeAncestors lists, newest first, the ancestors of `at` whose tree
// differs from it only under ciIgnoredPrefixes — the commits a CI verdict can be
// borrowed from for `at`'s code. The list ends at the first ancestor outside
// that chain: it and everything past it describe other code.
//
// A branch this checkout cannot fetch, or a history it cannot read, answers
// nothing: the caller falls back to the commit's own answer, and a proof that
// could not be read is not a proof.
func (r *Reconciler) sameCodeAncestors(branch, at string) []string {
	if err := r.git.fetch(branch); err != nil {
		return nil
	}
	out, err := r.git.run("", "rev-list", "--max-count="+fmt.Sprint(ciWalkLimit), at)
	if err != nil {
		return nil
	}
	var chain []string
	for i, line := range strings.Split(out, "\n") {
		sha := strings.TrimSpace(line)
		if sha == "" || i == 0 {
			continue // i == 0 is `at` itself, already asked
		}
		if !r.onlyCIIgnored(sha, at) {
			break
		}
		chain = append(chain, sha)
	}
	return chain
}

// onlyRunState reports whether every path that changed between two commits
// lives under .ticfac/.
//
// A diff that cannot be read answers false, deliberately: this is the proof
// that lets an older verdict stand for this tree, and an unreadable proof is
// not a proof.
func (r *Reconciler) onlyRunState(from, to string) bool {
	return r.onlyUnder(from, to, []string{runStatePrefix})
}

// onlyCIIgnored reports whether every path that changed between two commits
// lives under ciIgnoredPrefixes: the commits CI starts no run for, so CI's
// verdict on `from` is its verdict on `to`'s code. Unreadable answers false,
// as onlyRunState does.
func (r *Reconciler) onlyCIIgnored(from, to string) bool {
	return r.onlyUnder(from, to, ciIgnoredPrefixes)
}

func (r *Reconciler) onlyUnder(from, to string, prefixes []string) bool {
	out, err := r.git.run("", "diff", "--name-only", from, to)
	if err != nil {
		return false
	}
	for _, path := range strings.Split(out, "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		under := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				under = true
				break
			}
		}
		if !under {
			return false
		}
	}
	return true
}

// ciSubject is how the feed and a refusal name what a CI verdict is about.
func ciSubject(sha string, isHead bool, pr *forge.PullRequest) string {
	if isHead {
		return fmt.Sprintf("the epic PR #%d's head %s", pr.Number, short(sha))
	}
	return fmt.Sprintf("%s, the newest commit of the epic PR #%d that CI ran on (every commit since changes only %s, so the verdict is about this tree's code)",
		short(sha), pr.Number, ciIgnoredSubject)
}

// errClosedBehindRepair is the close-out close gate's answer when CI was red
// at the close and the repair job answered it: the repair's merge was gated
// and the close-out closed behind that gate, so the caller's own close is
// over (gate.go's closeAfterGate treats it as done, not as a failure).
var errClosedBehindRepair = errors.New("the close-out closed behind the repair of its red CI")

// ciRepairOwner is the attempt a repair of red CI is dispatched under at the
// close-out's admission, where the close-out itself has no attempt yet: the
// latest attempt this run dispatched for a tick that is now CLOSED — the work
// the red verdict is about, in the order it integrated. Nil when the run
// closed nothing (then there is nothing of this run's to repair, and the
// admission refuses as it always did).
//
// A closed tick is the right owner because the repair is gated and "closed"
// behind it exactly like any merge onto the tick's tree (gateAndClose), and
// closing a closed tick is a no-op; and it keeps the close-out's own repair
// allowance for the close-out's own writes.
func (r *Reconciler) ciRepairOwner(ctx context.Context) (*landingCloseout, error) {
	if r.store == nil {
		return nil, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return nil, err
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return nil, err
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Attempt > attempts[j].Attempt })
	asked := map[string]bool{}
	for _, attempt := range attempts {
		marker := handleFromMap(attempt.JobHandle)
		if marker.TickID == "" {
			marker.TickID = attempt.TickID
		}
		if marker.Attempt == 0 {
			marker.Attempt = attempt.Attempt
		}
		if marker.TickID == "" || marker.Attempt < 1 || asked[marker.TickID] || marker.Role == "closeout-epic" {
			continue
		}
		asked[marker.TickID] = true
		current, err := r.tracker.Show(ctx, marker.TickID)
		if err != nil || current.Status != "closed" {
			continue
		}
		if marker.Role == "" {
			marker.Role = "implement-tick"
		}
		marker.Repo = r.opts.Repo
		marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
		return &landingCloseout{
			entry:  planEntry{TickID: marker.TickID, Role: marker.Role, Title: r.titles[marker.TickID]},
			marker: marker,
		}, nil
	}
	return nil, nil
}

// closeoutDispatchedOverRedCI reports whether the code a close-out attempt
// was cut from had a RED CI verdict — read truthfully (ciForCode: executed
// checks only, the commit that changed code), not from the attempt's answer.
//
// It is how a close-out's BLOCKED is told apart from a close-out's BLOCKED
// about red CI without parsing prose (epic-6in): the latter is about the tree,
// the repair job's to fix, and never a person's. The admission now refuses to
// dispatch a close-out over red CI at all, so this answers true only for an
// attempt a false green let through — 6in's v7z attempt 8 is one.
func (r *Reconciler) closeoutDispatchedOverRedCI(ctx context.Context, marker attemptHandle) ([]string, bool) {
	if !r.closeoutRule.Declared || r.opts.PullRequests == nil || marker.BaseSHA == "" {
		return nil, false
	}
	pr := forge.PullRequest{HeadRef: r.branch, BaseRef: r.prBase()}
	if found, err := r.opts.PullRequests.Find(ctx, r.branch, pr.BaseRef); err == nil && found != nil {
		pr = *found
	}
	report, _, err := r.ciForCode(ctx, pr, marker.BaseSHA, false)
	if err != nil || report.State != forge.CIRed {
		return nil, false
	}
	return report.Failing, true
}
