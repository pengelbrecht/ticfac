package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The integrated gate, its evidence, and the close.
//
// This is the only place in ticfac's reconciler that runs a shell, and the only
// thing it will run is `[testing.commands]` from the target repository's
// `.tick/runners.toml`. Nothing else authorises a command line here — which is
// why the reader in toml.go refuses every other table rather than parsing it.
//
// The order is the whole point:
//
//	merge  ->  gate  ->  evidence  ->  freshness  ->  close  ->  clean up
//
// A close before the gate is a close nothing stands behind. A clean-up before
// the close throws away the only copy of what was closed. And publication is
// checked for freshness against the CURRENT target, because evidence stays true
// about what it evaluated and stops being true about what is being published.

// maxInlineOutput bounds what a gate's output puts in the record. Evidence is
// read by people and by machines; a run whose records are megabytes of test
// output is a run nobody reads.
const maxInlineOutput = 16 << 10

// gateProgress is the integrated gate, PART WAY THROUGH.
//
// The gate is a list of declared commands run one after another, and until tick
// 9pz that list was a `for` loop inside one blocking call. It is now the state a
// caller carries between steps, so the run loop can advance it one command — or
// one poll of one command — per round and go on admitting and polling in
// between. What it is not is concurrency: there is still exactly one gate
// running at a time, in one goroutine, over the integration branch as it stands.
type gateProgress struct {
	fingerprint Fingerprint
	profile     *profile.Profile
	keys        []string
	index       int
	passed      bool
	failures    []string

	// running is the command that has been started and not yet answered for.
	// Nil between commands, and nil once every command has been answered.
	running *gateCommand
}

// gateAndClose runs the integrated gate over the merge, records its evidence,
// checks that the evidence is still about what is being published, and closes
// the tick.
//
// This is the BLOCKING driver: it drives beginGate/stepGate to completion and
// then closes. It is what a role job uses, because a role job runs alone and has
// nothing to admit or poll while its gate runs. The window drives the same three
// calls a step at a time instead (finish.go).
func (r *Reconciler) gateAndClose(ctx context.Context, entry planEntry, marker attemptHandle,
	collected *subprocess.Collection, merged merge) error {

	g, err := r.beginGate(marker, merged)
	if err != nil {
		return err
	}
	for {
		done, err := r.stepGate(ctx, marker, merged, g)
		if err != nil {
			return err
		}
		if done {
			break
		}
		// The WALL clock, not the run's own sleep. What this waits on is a
		// local `sh` this host is running, not a substrate whose cadence an
		// executor states (tick u9l) and not anything a fake clock in a test
		// or a replay is describing — and the gate's own bound is measured the
		// same way, for the same reason.
		time.Sleep(gatePollInterval)
	}
	return r.closeAfterGate(ctx, entry, marker, collected, merged, g)
}

// beginGate is everything the gate decides before it runs a single command: the
// checkpoint, what the evidence will say it evaluated, and the structural check
// that costs nothing.
func (r *Reconciler) beginGate(marker attemptHandle, merged merge) (*gateProgress, error) {
	tick := marker.TickID
	if _, err := r.checkpoint(runstate.StateGating,
		fmt.Sprintf("running the integrated gate for %s on %s", tick, short(merged.GateSHA))); err != nil {
		return nil, err
	}

	// What this evidence will say it evaluated (Appendix A #13).
	//
	// `source_sha` is the commit the gate ACTUALLY RAN ON — the merge of the
	// attempt into the integration branch, which is what a `phase: integrated`
	// record's source is and how contracts/job-protocol.json's own golden
	// integrated-gate example spells it. Recording the attempt's head there
	// instead was the hole this fingerprint had: the gate ran on a commit no
	// record named, so nothing downstream could tell which integration the
	// verdict was about, and freshness could only ever notice the ATTEMPT
	// branch moving.
	//
	// `attempt_head` is the other half — the head the collect and the boundary
	// check read — kept as a field of its own so that a branch moved under a
	// passed gate is still caught. Neither is derivable from the other: the
	// merge commit's second parent is the attempt head only while the merge is
	// the one this run made.
	//
	// The profile digest is the digest of the profile THIS tick's role was
	// dispatched under, not the run's set: a check run under a different
	// profile evaluated something else, and that is what the fingerprint is
	// for. Since tick 5eq that means the role AT THE TIER the dispatch
	// derived — the marker's own record of it — and not the role's base
	// profile, which a tiered dispatch never used.
	dispatchProfile, err := r.profileOfMarker(marker)
	if err != nil {
		return nil, fmt.Errorf("the gate for %s cannot say which profile dispatched it: %w", tick, err)
	}
	fingerprint := Fingerprint{
		"source_sha":              merged.GateSHA,
		"attempt_head":            merged.AttemptHead,
		"integration_ref":         refFor(r.branch),
		"context_manifest_digest": r.gateDigest,
		"profile_digest":          dispatchProfile.Digest,
	}

	// The gate's first check is structural, and it comes first because it is
	// nearly free and because it is about something the declared commands
	// cannot say: the commands prove the tree, and this proves that the repo's
	// own CI configuration still DESCRIBES that tree. A workflow naming a
	// package the tree deleted keeps the epic's PR red on every push while
	// every command here stays green — the blindness tick cwa closes.
	stale, err := r.staleWorkflowPatterns(merged.GateSHA)
	if err != nil {
		return nil, err
	}
	if len(stale) > 0 {
		r.setTick(tick, "rejected")
		r.record(tick, StageGateFailed, "the repo's own CI configuration is not about this tree: %s",
			strings.Join(stale, "; "))
		return nil, r.refuse(RefusedGate, tick,
			"the integrated gate on %s did not pass for %s: the repo's own CI configuration is not about this "+
				"tree — %s. The tick is NOT closed, and the repair is the workflow or the tree, not the check: "+
				"fix the workflow to name what the tree carries, or restore the package it names, push it to %s, "+
				"and run the epic again under this run id: the gate runs again because this record is keyed by the "+
				"commit it ran on and the fixed tree is a different commit",
			short(merged.GateSHA), tick, strings.Join(stale, "; "), r.branch)
	}

	return &gateProgress{
		fingerprint: fingerprint,
		profile:     dispatchProfile,
		keys:        make([]string, len(r.gate)),
		passed:      true,
	}, nil
}

// stepGate takes the gate one step: start the next declared command, or ask the
// running one whether it has answered. It reports true once every command has.
//
// One step is deliberately small. The caller gets the turn back between every
// command AND on every poll of a running one, which is what lets the run loop
// keep admitting and polling through an eight-minute gate (tick 9pz). The
// commands still run ONE AT A TIME and in declared order: the gate is a verdict
// about a tree, and two commands racing on the same tree would be two verdicts
// about different moments of it.
func (r *Reconciler) stepGate(ctx context.Context, marker attemptHandle, merged merge, g *gateProgress) (bool, error) {
	tick := marker.TickID
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if g.running != nil {
		if !g.running.shell.settled() {
			// The gate's own bound is the ONE bound on a gate, and this is
			// where a second one was deliberately not added (tick 9pz). The
			// shell already carries GateTimeout and kills its process group at
			// it; a bound in the loop could only fire earlier, which refuses a
			// gate that is merely slow — the false refusals cy2 is about, on a
			// host where a 3m26s check has been measured taking 37 minutes
			// under someone else's load — or later, which is decoration. What
			// a second bound would have been a clumsy proxy for is the
			// INFORMATION, and that is what the heartbeat carries instead:
			// elapsed, output, and how much of the real bound is left.
			r.announceGate(g.running, marker, merged)
			return false, nil
		}
		record, err := r.finishGateCommand(g.running, marker, merged, g.fingerprint)
		g.running = nil
		if err != nil {
			return false, err
		}
		if record.Result != "pass" {
			g.passed = false
			g.failures = append(g.failures, fmt.Sprintf("%s (%s)", record.Check.ID, record.Result))
		}
		g.index++
		return g.index >= len(r.gate), nil
	}

	if g.index >= len(r.gate) {
		return true, nil
	}

	command := r.gate[g.index]
	key, err := r.gateEvidenceKey(tick, marker.Attempt, command.Name, merged.GateSHA, g.profile.Digest)
	if err != nil {
		return false, err
	}
	g.keys[g.index] = key

	// Appendix A #13: a record that cannot say what it evaluated is not
	// evidence. All four fingerprint fields or none of it.
	if outcome := r.RecordEvidence(key, g.fingerprint); outcome != "recorded" {
		return false, r.refuse(RefusedStale, tick,
			"the gate's evidence for %s cannot say what it evaluated (%s): %v", tick, outcome, g.fingerprint)
	}

	started, existing, err := r.startGateCommand(command, key, marker, merged)
	if err != nil {
		return false, err
	}
	if existing != nil {
		// Already paid for by an earlier incarnation: no command runs and the
		// recorded verdict stands.
		if existing.Result != "pass" {
			g.passed = false
			g.failures = append(g.failures, fmt.Sprintf("%s (%s)", command.Name, existing.Result))
		}
		g.index++
		return g.index >= len(r.gate), nil
	}
	g.running = started
	return false, nil
}

// closeAfterGate is the verdict on a finished gate and everything behind it:
// the freshness check, the close-out's own CI gate, and the close.
func (r *Reconciler) closeAfterGate(ctx context.Context, entry planEntry, marker attemptHandle,
	collected *subprocess.Collection, merged merge, g *gateProgress) error {

	tick := marker.TickID
	fingerprint, keys, failures := g.fingerprint, g.keys, g.failures

	if !g.passed {
		r.setTick(tick, "rejected")
		r.record(tick, StageGateFailed, "the integrated gate did not pass: %s", strings.Join(failures, ", "))
		return r.refuse(RefusedGate, tick,
			"the integrated gate on %s did not pass for %s: %s. The tick is NOT closed: a close behind a failing "+
				"gate is a close nothing stands behind. The merge is already on %s, so the repair is to fix the "+
				"check or the tree, push it to %s, and run the epic again under this run id: the gate runs again "+
				"because this record is keyed by the commit it ran on and the fixed tree is a different commit",
			short(merged.GateSHA), tick, strings.Join(failures, ", "), r.branch, r.branch)
	}
	r.record(tick, StageGatePassed, "the integrated gate passed on %s (%s)", short(merged.GateSHA), r.gate)

	// Appendix A #13's other half: the target may have moved between the check
	// and the publication. The record is still true about what it evaluated and
	// is no longer true about what is being published.
	target, err := r.currentTarget(marker, merged)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if outcome := r.PublishEvidence(key, target); outcome != "published" {
			moved := fingerprint.Mismatch(target)
			r.record(tick, StageStale, "the gate's evidence is no longer about what would be published (%s): %s",
				outcome, strings.Join(moved, "; "))
			return r.refuse(RefusedStale, tick,
				"the gate's evidence for %s is no longer about what would be published (%s): publishing it would "+
					"state a verdict about something else", tick, strings.Join(moved, "; "))
		}
	}

	// The close-out's own commits are the one thing the admission's green CI
	// is not evidence about (tick sqx): they are already merged onto the
	// integration branch — the PR's head — and CI runs again on that head.
	// The close-out's CLOSE is gated on green CI for IT, re-derived from the
	// PR rather than trusted from the admission, for the same reason CI
	// state is re-derived there: CI's answer changes with every push, and
	// this push was the close-out's own. Other roles integrate onto the same
	// branch and move the same head — but the rule a repository declares is
	// a gate on the epic's CLOSE-OUT, the phase whose own writes are its
	// deliverable, and their closes stand on the integrated gate above.
	if entry.Role == "closeout-epic" {
		if err := r.gateCloseoutClose(ctx, marker, merged); err != nil {
			return err
		}
	}

	return r.closeTick(ctx, entry, marker, collected, merged)
}

// gateCommand is one declared check that has been STARTED: its throwaway
// worktree, the shell running in it, and what the record will say about when it
// began.
type gateCommand struct {
	command GateCommand
	key     string
	dir     string
	remove  func()
	shell   *gateShell
	started string

	// beat is when this check last said so, in the same wall clock the shell
	// records its own start in, and stalled records that it has already been
	// warned about — once per check, for the reason the attempt's own stall
	// warning is written once: a warning repeated every minute is a warning
	// nobody reads.
	//
	// What is NOT here any more is when the check STARTED (tick m4n). That
	// belongs to the shell: this wrapper can be rebuilt around a live shell,
	// and a clock that restarts with the wrapper reports the wrapper's age
	// rather than the gate's.
	beat    time.Time
	stalled bool
}

// startGateCommand begins one declared check, or reports the record that
// already answers it.
//
// The evidence record is created-if-absent, so a restarted run that already
// paid for this check re-reads its verdict instead of running it again — and a
// record that already exists is never overwritten, because a record that can be
// overwritten is not evidence. A non-nil record means nothing was started.
func (r *Reconciler) startGateCommand(command GateCommand, key string,
	marker attemptHandle, merged merge) (*gateCommand, *runstate.Evidence, error) {

	if _, err := r.store.Fetch(); err != nil {
		return nil, nil, err
	}
	if existing, ok, err := r.store.Evidence(key); err != nil {
		return nil, nil, err
	} else if ok {
		r.record(marker.TickID, StageWaiting, "the %s gate already ran for this attempt: %s", command.Name, existing.Result)
		return nil, existing, nil
	}

	// gateWorktree, not tempWorktree: a gate that ran in a fresh directory
	// every time could never hit Go's test cache, which keys on the absolute
	// paths a test opened. The slot is reset to this commit on the way in and
	// held under a lock for the life of the check (gatedir.go, tick 6wh).
	dir, lock, remove, err := r.git.gateWorktree("ticfac-gate-", merged.GateSHA)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare the gate worktree at %s: %w", short(merged.GateSHA), err)
	}
	// The slot's lock goes to the shell as well as staying here: a gate that
	// outlives the reconciler that started it must keep holding the directory
	// it is running in (gatedir.go).
	shell, err := startShell(dir, command.Command, r.opts.GateTimeout, r.now(), lock)
	if err != nil {
		remove()
		return nil, nil, err
	}
	now := r.now()
	r.record(marker.TickID, StageGateStarted,
		"the %s gate is running on %s, bounded at %s: %s",
		command.Name, short(merged.GateSHA), r.opts.GateTimeout, command.Description)
	return &gateCommand{
		command: command, key: key, dir: dir, remove: remove, shell: shell,
		started: now.UTC().Format(time.RFC3339),
		beat:    now.Round(0),
	}, nil, nil
}

// announceGate is what a running gate says about itself, and it is the answer
// to the second half of tick 9pz: a run whose window has nothing live left to
// poll must still not look dead.
//
// It is driven from stepGate, which the run loop calls every round whether or
// not any worker is alive — that is the whole point. Before this, the two
// things a run emitted (feed lines and liveness probes) were both driven off
// polling LIVE attempts, so "the last tick of a wave is gating" produced
// exactly the same feed as a process that had died.
//
// Everything it writes is an observation, never a verdict. It does not stop,
// refuse or hold anything: the gate's own timeout is the only bound, and see
// the note in stepGate for why a second one would be worse than none.
func (r *Reconciler) announceGate(g *gateCommand, marker attemptHandle, merged merge) {
	if r.gateHeartbeat < 0 {
		return
	}
	// Wall, with the monotonic reading stripped, because this is the clock the
	// feed stamps its own lines with and an operator subtracts one from the
	// other (tick m4n).
	now := r.now().Round(0)
	if now.Sub(g.beat) < r.gateHeartbeat {
		return
	}
	g.beat = now

	elapsed := g.shell.age(now).Round(time.Second)
	left := g.shell.remaining().Round(time.Second)
	bytes, at := g.shell.written()
	produced := fmt.Sprintf("%d bytes of output", bytes)
	idle := elapsed
	if bytes > 0 && !at.IsZero() {
		idle = now.Sub(at).Round(time.Second)
		produced = fmt.Sprintf("%d bytes of output, last %s ago", bytes, idle)
	}
	r.record(marker.TickID, StageGateRunning,
		"the %s gate has been running for %s on %s and has written %s; %s of its bound is left",
		g.command.Name, elapsed, short(merged.GateSHA), produced, left)

	// dh1's rule, pointed at a check instead of a worker: alive was never the
	// question, and what a person needs to know is whether it is getting
	// anywhere. Said once, and only about a gate that has BOTH outlived the
	// threshold and produced nothing in that time — a long check that is
	// printing as it goes is working, however long it takes.
	if g.stalled || r.opts.StallWarnAfter <= 0 || elapsed < r.opts.StallWarnAfter || idle < r.opts.StallWarnAfter {
		return
	}
	g.stalled = true
	r.record(marker.TickID, StageGateStalled,
		"the %s gate has been running for %s on %s and has produced nothing for %s — longer than this run's stall "+
			"threshold of %s. It is NOT refused and nothing about it is decided: a check that prints only at the end "+
			"legitimately looks like this, and the gate's own bound of %s is what spends it. It is a reason to look "+
			"at this host — at load, at the command, at what it is waiting on",
		g.command.Name, elapsed, short(merged.GateSHA), idle, r.opts.StallWarnAfter, r.opts.GateTimeout)
}

// finishGateCommand collects one started check's answer and records its
// evidence. It is only ever called once the shell has settled — or once the
// bound it was given has passed, which is the same call with a killed shell at
// the end of it.
func (r *Reconciler) finishGateCommand(g *gateCommand, marker attemptHandle, merged merge,
	fingerprint Fingerprint) (*runstate.Evidence, error) {

	defer g.remove()
	command, key, dir, started := g.command, g.key, g.dir, g.started
	stdout, stderr, code, runErr := g.shell.wait()
	finished := r.now().UTC().Format(time.RFC3339)

	// `error` is not `fail`: a gate whose command could not run has produced no
	// evidence about the ref at all, and telling a person it failed would send
	// the next repair at the wrong problem.
	result := "pass"
	switch {
	case runErr != nil && code < 0:
		result = "error"
		stderr = strings.TrimSpace(stderr + "\n" + runErr.Error())
	case code != 0:
		result = "fail"
	}

	// The command ran in `dir`, a worktree OUTSIDE the repository (gatedir.go's
	// gateWorktree, or git.go's tempWorktree when every slot is held) — a
	// host-local path a failing command's own output (a stack trace, a shell
	// error line naming its $PWD) can carry verbatim. Redact it, the
	// reconciler's own repository
	// checkout, and the operator's home directory before anything here is
	// recorded: this evidence is pushed to origin, which may be a public repo.
	redactPaths := []string{dir, r.opts.Repo}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		redactPaths = append(redactPaths, home)
	}
	var redacted bool
	stdout, changedOut := redactHostPaths(stdout, redactPaths)
	stderr, changedErr := redactHostPaths(stderr, redactPaths)
	redacted = changedOut || changedErr

	tickID, attempt := marker.TickID, marker.Attempt
	record := runstate.Evidence{
		Key: key,
		Provenance: runstate.Provenance{
			RunID:   r.runID,
			TickID:  &tickID,
			Attempt: &attempt,
			// The ref and the commit the gate ran on: the integration branch,
			// and the merge this run made on it. A `phase: integrated` record
			// whose source is the attempt's own branch says the check ran
			// somewhere it did not.
			SourceRef:      fingerprint["integration_ref"],
			SourceSHA:      fingerprint["source_sha"],
			IntegrationRef: runstate.Ptr(fingerprint["integration_ref"]),
			Phase:          runstate.PhaseIntegrated,
			// The executor the attempt this gate is over was DISPATCHED through
			// (the marker's own, the same value the dispatch's provenance
			// states) — a gate naming an executor the run did not use would be
			// evidence that lies about the attempt it evaluated.
			Executor:      gateExecutorPtr(marker),
			WorkspaceID:   nil,
			Backend:       nil,
			Role:          runstate.Ptr(marker.Role),
			ProfileDigest: runstate.Ptr(fingerprint["profile_digest"]),
			// The tier is the one the attempt was DERIVED under (the marker's own
			// tier, the same value the dispatch's provenance states) — the rung
			// that routed the model this gate is over, not the role's base one.
			// Since bundle 4.0.0 it is a field of the closed provenance object, so
			// an over-tiered attempt is auditable from the gate's evidence alone.
			Tier: gateTierPtr(marker),
			// The substrate is the one the attempt was DISPATCHED under (the
			// marker's own, the same values the dispatch's provenance states) —
			// so the gate's evidence names the substrate it evaluated the work
			// of, and a run that spans a substrate upgrade reads coherently from
			// the gate alone too (bundle 5.1.0, tick to1).
			SubstrateProtocol:      gateSubstrateProtocolPtr(marker),
			SubstrateServerVersion: gateSubstrateServerVersionPtr(marker),
			// The model is the one the tier-resolved profile routes to — the
			// model that DISPATCHED the attempt this gate is over, not the
			// role's base one a tiered dispatch never used.
			Model:                 runstate.Ptr(r.gateModel(marker)),
			ContextManifestDigest: runstate.Ptr(fingerprint["context_manifest_digest"]),
		},
		Check:      runstate.Check{ID: command.Name, Kind: "command", Command: []string{"sh", "-c", command.Command}},
		StartedAt:  started,
		FinishedAt: finished,
		ExitCode:   &code,
		Output: runstate.Output{Inline: &runstate.InlineOutput{
			Mode:      "inline",
			Stdout:    bound(stdout),
			Stderr:    bound(stderr),
			Truncated: len(stdout) > maxInlineOutput || len(stderr) > maxInlineOutput,
			Redacted:  redacted,
			MaxBytes:  maxInlineOutput,
		}},
		Result:         result,
		Acceptance:     "required",
		ContentDigest:  contentDigest(stdout, stderr, fmt.Sprint(code)),
		PersistenceURI: "git:" + refFor(r.branch) + ":" + runstate.EvidencePath(r.runID, key),
	}
	outcome, err := r.store.PutEvidence(record)
	if err != nil {
		return nil, fmt.Errorf("record the gate's evidence for %s: %w", marker.TickID, err)
	}
	if !outcome.EffectPermitted() {
		// Somebody else recorded it between the read and the write. Theirs is
		// the record; evidence is never overwritten.
		if _, err := r.store.Fetch(); err != nil {
			return nil, err
		}
		if existing, ok, err := r.store.Evidence(key); err == nil && ok {
			return existing, nil
		}
	}
	return &record, nil
}

// currentTarget is the fingerprint of what a publication would be about, read
// fresh. It is deliberately re-read from origin rather than remembered: a
// freshness check against a value the checker itself carried forward would
// always be fresh.
//
// The integration half is a CONTAINMENT question rather than an equality one,
// and it has to be: the integration branch moves under this run constantly —
// every checkpoint and every tracker record this reconciler pushes lands on it
// — so "origin's head is still exactly the commit the gate ran on" would be
// false a second after the merge and would refuse every close. What must still
// hold is that the branch about to be published STILL CARRIES the commit that
// was gated. A branch that was reset, force-pushed or rebuilt from elsewhere
// no longer does, and that is the moving target A13 is about: the target's
// `source_sha` is then origin's head, which is not what the record states.
func (r *Reconciler) currentTarget(marker attemptHandle, merged merge) (Fingerprint, error) {
	head, err := r.git.remoteHead(branchOf(marker.WriteRef))
	if err != nil {
		return nil, err
	}
	epicHead, err := r.git.remoteHead(r.branch)
	if err != nil {
		return nil, err
	}
	gated := merged.GateSHA
	if err := r.git.fetch(r.branch); err != nil {
		return nil, err
	}
	if !r.git.contains(gated, epicHead) {
		gated = epicHead
	}
	gate, err := ReadGateCommands(r.opts.GateConfig)
	if err != nil {
		return nil, err
	}
	dispatchProfile, err := r.profileOfMarker(marker)
	if err != nil {
		return nil, fmt.Errorf("the gate for %s cannot say which profile dispatched it: %w", marker.TickID, err)
	}
	return Fingerprint{
		"source_sha":              gated,
		"attempt_head":            head,
		"integration_ref":         refFor(r.branch),
		"context_manifest_digest": gate.Digest(),
		"profile_digest":          dispatchProfile.Digest,
	}, nil
}

// gateModel is the model the evidence record's provenance names: the one
// the dispatch's own profile routed to. On a resolution failure it is the
// role's base model, said in the run's own record — the evidence has already
// established its fingerprint against the resolved profile, and a second
// refusal inside a record build would lose the gate's actual result, which is
// the one thing that cannot be re-derived.
func (r *Reconciler) gateModel(marker attemptHandle) string {
	if p, err := r.profileOfMarker(marker); err == nil {
		return p.Model
	}
	return r.profileFor(marker.Role).Model
}

// gateTierPtr is the gate evidence's tier: the tier the marker says the
// attempt was dispatched under, or nil when the marker names none (an
// untiered dispatch, or a marker older than the tier policy). It is a
// separate helper for the same reason gateModel is: a failure here must
// degrade to a stated null, never lose the gate's record.
func gateTierPtr(marker attemptHandle) *string {
	if marker.Tier == "" {
		return nil
	}
	tier := marker.Tier
	return &tier
}

// gateExecutorPtr is the gate evidence's executor: the one the marker says the
// attempt was dispatched through, or nil when the marker names none (a marker
// older than the executor-naming profiles). It is a separate helper for the
// same reason gateModel is: a failure here must degrade to a stated null, never
// lose the gate's record.
func gateExecutorPtr(marker attemptHandle) *string {
	if marker.Executor == "" {
		return nil
	}
	executor := marker.Executor
	return &executor
}

// gateSubstrateProtocolPtr and gateSubstrateServerVersionPtr are the gate
// evidence's substrate: the protocol and server version the marker says the
// attempt was dispatched under, nil when it names none (a local process —
// which states no protocol — or a marker older than the substrate fields).
// Separate helpers for the same reason gateTierPtr is.
func gateSubstrateProtocolPtr(marker attemptHandle) *int {
	if marker.SubstrateProtocol == 0 {
		return nil
	}
	protocol := marker.SubstrateProtocol
	return &protocol
}

func gateSubstrateServerVersionPtr(marker attemptHandle) *string {
	if marker.SubstrateServerVersion == "" {
		return nil
	}
	serverVersion := marker.SubstrateServerVersion
	return &serverVersion
}

// closeTick closes the tick durably, through the tracker, and only after the
// gate. The tracker is re-read first: it is the authority on whether a tick is
// closed, so reading it is the compare-and-swap that proves the close has not
// already happened.
func (r *Reconciler) closeTick(ctx context.Context, entry planEntry, marker attemptHandle,
	collected *subprocess.Collection, merged merge) error {

	tick := marker.TickID
	if _, err := r.checkpoint(runstate.StatePublishing,
		fmt.Sprintf("closing %s behind the integrated gate on %s", tick, short(merged.GateSHA))); err != nil {
		return err
	}

	current, err := r.tracker.Show(ctx, tick)
	if err != nil {
		return fmt.Errorf("read tick %s before closing it: %w", tick, err)
	}
	if current.Status != "closed" {
		// The close's other gate (tick 7vn): a tick whose findings have not
		// been triaged does not close. The gate has already passed and the
		// merge is already on the integration branch, so the repair is not
		// the tree and not the worker — it is the person the drafted finding is waiting for, and
		// the refusal names exactly where they act.
		if refusal, err := r.gateOnFindings(tick); err != nil {
			return err
		} else if refusal != nil {
			r.setTick(tick, "rejected")
			r.record(tick, StageRejected, "%s: %s", refusal.Reason, firstLine(refusal.Message))
			if _, err := r.checkpoint(runstate.StateRunning,
				fmt.Sprintf("%s is refused its close: a drafted finding is untriaged", tick)); err != nil {
				return err
			}
			return refusal
		}
		note := fmt.Sprintf("ticfac run %s: attempt %d merged into %s as %s; the integrated gate (%s) passed.",
			r.runID, marker.Attempt, r.branch, short(merged.GateSHA), r.gate)
		if _, err := r.tracker.Note(ctx, tick, note); err != nil {
			return fmt.Errorf("note the gate evidence on %s: %w", tick, err)
		}
		if _, err := r.tracker.Close(ctx, tick); err != nil {
			return fmt.Errorf("close %s: %w", tick, err)
		}
	}

	r.setTick(tick, "closed")
	r.record(tick, StageClosed, "closed behind the integrated gate on %s", short(merged.GateSHA))
	if _, err := r.checkpoint(runstate.StateRunning, fmt.Sprintf("%s is closed", tick)); err != nil {
		return err
	}
	_, _ = collected, entry
	return nil
}

// evidenceKey names the gate's evidence for one check of one attempt. It
// carries the attempt rather than the merge commit so that a restarted run
// asks for the SAME record it already paid for, and a retry — which is a new
// attempt number — asks for a new one.
func evidenceKey(tick string, attempt int, check string) string {
	return fmt.Sprintf("gate-%s-%d-%s", tick, attempt, check)
}

// gateEvidenceKey is the key this run will record one check under, and it is
// where a FAILED gate gets its way out.
//
// The plain key is per (tick, attempt, check) and the record under it is
// created if absent and never overwritten, which is right for a gate that
// passed — a restarted run re-reads what it already paid for — and was a dead
// end for a gate that failed. The attempt's merge is already on the integration
// branch, so a resume finds it "already contained", re-reads the recorded
// `fail` without running anything, and refuses again for as long as the run is
// restarted; a new run id does not help, because the merge is still there and a
// new attempt cut from the integration head finds the work already done.
//
// So a record is keyed to what it is evidence ABOUT, whatever it says. When
// the previous record for this check was made over something else — a person
// fixed the check or the tree and pushed it, which moves the integration
// branch — this run asks for a record of its own and the check runs again.
// When nothing about the record's subject changed, the key is the same and
// the recorded verdict stands: re-running a check on a commit nothing changed
// about would spend the same minutes to reach the same answer, and evidence
// is never overwritten.
//
// What a record is evidence about is the SOURCE tree, not the commit id. The
// run writes its own records to `.ticfac/` on the branch it gates, so the
// integration head moves whenever the run checkpoints — every resume, with no
// change to anything a gate command reads. Keying on the raw commit is wrong in
// both directions at once: too loose across a real change (a pass from another
// tree would close this tick) and too tight across the run's own bookkeeping,
// where it re-pays for the whole gate on every restart. sourceFingerprint is
// the key that is right in both: the gated commit's top-level tree with the
// run's own entry dropped.
//
// A PASS is keyed the same way, and that is the point rather than an oversight.
// The gate is a list of commands and each one carries its own record, so a
// verdict that reused a pass from an earlier tree would close the tick under
// this tree's fingerprint on the strength of a check no run ever performed
// here: the tree that passed and the tree being closed are not the same tree.
// A pass says something about the tree it ran on and about no other.
//
// And it is the source tree TOGETHER WITH THE DECLARED GATE (tick 0dc). A
// record stands for the gate that ran it, so the reuse judgement asks the
// recorded context_manifest_digest before it re-reads a record as evidence —
// without that comparison the digest was decorative on exactly one path. A
// resumed run that adopts an already-merged attempt re-reads the old record
// without running anything, and the freshness check it publishes through
// compares the fingerprint the run WOULD have used against a target built the
// same way, so a record the fresh path had just refused as stale — the gate
// command changed mid-run — closed the tick five minutes later. A record from
// a different declared gate now gets a key of its own, so the check re-runs
// under the gate declared now and the close stands on evidence that is about
// this gate. The rekey is still a function of the thing it identifies — the
// source's fingerprint and the declared gate's — never of the history that
// produced it, so it does not chain either.
//
// And it is the source tree TOGETHER WITH THE PROFILE THE ATTEMPT IS
// DISPATCHED UNDER, as the run resolves it NOW (tick oe0). A gate command is
// about the CHECK and the profile is about the WORKER, and that is the one
// argument for leaving the profile out — but it loses here, twice over. The
// record's own provenance NAMES the profile (profile_digest is one of Appendix
// A #13's four), so a published record that says a verdict was produced under a
// profile the current configuration no longer describes is exactly the claim
// freshness exists to refuse; and the freshness index registers the fingerprint
// the run WOULD use — the profile resolved now — while the record it re-reads
// names the profile that dispatched the attempt, so on the reuse path the
// publication check compares the new profile against itself and cannot see the
// difference. The reuse path must ask the same question the fresh path is held
// to: a record whose profile_digest is not the currently resolved profile is a
// record from a different worker configuration, and it gets a key of its own
// the same way a record from a different declared gate does.
func (r *Reconciler) gateEvidenceKey(tick string, attempt int, check, gateSHA, profileDigest string) (string, error) {
	base := evidenceKey(tick, attempt, check)
	if gateSHA == "" {
		return base, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return "", err
	}
	existing, ok, err := r.store.Evidence(base)
	if err != nil {
		return "", err
	}
	if !ok {
		return base, nil
	}

	gated, err := r.sourceFingerprint(gateSHA)
	if err != nil {
		return "", fmt.Errorf("fingerprint the source of %s for the gate's evidence key: %w", short(gateSHA), err)
	}
	// The suffix names the three things a record is evidence about — the
	// declared gate's digest, the profile the run resolves now, and the
	// source's fingerprint, in that order, so the key still ENDS with the tree
	// it ran on the way the rekey's own test pins — and two resumes of the
	// same subject under the same gate and profile name the same key, so the
	// record minted for it stands rather than a third key being minted and
	// the whole gate paid for again.
	rekeyed := base + "-" + digestKeySuffix(r.gateDigest) + "-" + digestKeySuffix(profileDigest) + "-" + short(gated)

	// The digest is part of the reuse judgement or it is decorative (tick 0dc).
	// A record whose context_manifest_digest is not the currently declared
	// gate's was produced by a different check, whatever tree it ran on, and a
	// verdict from a different check is not a verdict about this gate.
	recordedDigest := ""
	if existing.Provenance.ContextManifestDigest != nil {
		recordedDigest = *existing.Provenance.ContextManifestDigest
	}
	if recordedDigest != r.gateDigest {
		was := digestKeySuffix(recordedDigest)
		if was == "" {
			was = "none"
		}
		r.record(tick, StageStale,
			"the recorded %s gate ran under a different declared gate (%s was gated, %s is declared now), so the "+
				"check runs again under the gate declared now", check, was, digestKeySuffix(r.gateDigest))
		return rekeyed, nil
	}

	// The profile digest is part of the same reuse judgement (tick oe0). A
	// record whose profile_digest is not the currently resolved profile was
	// produced under a worker configuration the run no longer describes, and
	// a verdict from a different profile is a verdict the record's own
	// provenance contradicts: it names the profile that ran it, and publishing
	// it under this run would state a verdict produced under something else.
	recordedProfile := ""
	if existing.Provenance.ProfileDigest != nil {
		recordedProfile = *existing.Provenance.ProfileDigest
	}
	if recordedProfile != profileDigest {
		was := digestKeySuffix(recordedProfile)
		if was == "" {
			was = "none"
		}
		r.record(tick, StageStale,
			"the recorded %s gate ran under a different profile (%s dispatched it, %s is resolved now), so the "+
				"check runs again under the profile resolved now", check, was, digestKeySuffix(profileDigest))
		return rekeyed, nil
	}

	if existing.Provenance.SourceSHA == gateSHA {
		return base, nil
	}

	// The suffix is the SOURCE's fingerprint and never the commit's, so that
	// the key is a function of the tree alone. Keying it by commit would make
	// the rekey a chain one link long: the comparison here is always against
	// the record at the PLAIN key, so a second resume compares the same old
	// record against a second new commit, mints a second suffix, and pays for
	// the whole gate again — the outcome the fingerprint exists to prevent. Two
	// resumes of the same tree now name the same key, and the record already
	// under it stands.
	recorded, err := r.sourceFingerprint(existing.Provenance.SourceSHA)
	if err != nil {
		// Nobody can say whether the two commits carry the same source. The
		// safe direction is to run the check again under a key of its own: a
		// gate paid for twice costs minutes, and a gate reused across a tree
		// nobody compared closes a tick on evidence about another tree. The
		// key is still the tree's, so this does not chain either.
		r.record(tick, StageStale,
			"the source of the recorded %s could not be read, so %s is gated under a key of its own: %v",
			short(existing.Provenance.SourceSHA), short(gateSHA), err)
		return rekeyed, nil
	}
	if gated == recorded {
		return base, nil
	}
	return rekeyed, nil
}

// digestKeySuffix is the digest's spelling inside an evidence KEY. A key is a
// filename (runstate.EvidencePath) and a citation, so the sha256 label and its
// colon stay in the messages for people and the key carries the digest's own
// hex, spelled as short spells the source fingerprint beside it.
func digestKeySuffix(digest string) string {
	digits := strings.TrimPrefix(digest, "sha256:")
	if len(digits) > 12 {
		digits = digits[:12]
	}
	return digits
}

func (r *Reconciler) sourceFingerprint(commit string) (string, error) {
	out, err := r.git.run("", "ls-tree", commit)
	if err != nil {
		return "", fmt.Errorf("read the tree of %s: %w", short(commit), err)
	}
	entries := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "<mode> <type> <sha>\t<name>"
		_, name, ok := strings.Cut(line, "\t")
		if !ok || name == runstate.Root {
			continue
		}
		entries = append(entries, line)
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// gateWaitDelay is how long the gate's output may keep the wait alive after
// the gate's own processes are gone.
//
// The reconciler USED to read the gate's output through a pipe (a
// strings.Builder is not a file, so os/exec makes one), and cmd.Wait does not
// return until that pipe is closed — which a grandchild holding the write end
// can put off indefinitely, timeout or no timeout. WaitDelay was the bound on
// that: past it the pipes are closed under whoever still holds them and Wait
// returns.
//
// Since tick 9pz the output goes to FILES, which os/exec hands the child
// directly, so there is no pipe for a grandchild to hold and Wait returns when
// the killed group is reaped. The delay is kept because it costs nothing and
// because it is still the bound on the one case files do not cover: an os/exec
// wait that has been asked to give up.
const gateWaitDelay = 5 * time.Second

// gateShell is one declared gate command, RUNNING.
//
// It is STARTED and then POLLED rather than waited for (tick 9pz). The wait was
// the run's longest blind spot: measured across the three epic feeds this
// repository carries, a settled tick blocked the whole window for a median of
// 7m19s from its settlement to the next admission, and 89% of that was this one
// call. Nothing else about the gate changes — same `sh -c`, same process group,
// same bound — only who is holding still while it runs.
//
// The finish is a state machine the run loop advances one step per round
// (finish.go), so the step that owns a running gate has to be able to ask "is it
// done yet" and get an answer without blocking. Two decisions follow from that,
// and both are about not needing a goroutine of our own:
//
//   - The output goes to FILES, not to a strings.Builder. os/exec makes a pipe
//     for anything that is not an *os.File and copies it on a goroutine of its
//     own, and Wait then does not return until that pipe closes — which a
//     grandchild holding the write end can put off indefinitely. Files have no
//     such wait, which is also why gateWaitDelay stops being the thing that
//     rescues the timeout path.
//   - The shell writes a SENTINEL carrying its exit status as its last act, so
//     "has it finished" is one os.Stat rather than a blocking Wait. The inner
//     command line is handed over in the environment rather than concatenated
//     into the wrapper, so what runs is byte for byte the line the repository
//     declared.
type gateShell struct {
	cmd      *exec.Cmd
	scratch  string
	outPath  string
	errPath  string
	donePath string

	// The gate's clock lives HERE, on the shell, and not on the gateCommand
	// wrapper around it (tick m4n). Two separate reasons, and they want
	// different clocks:
	//
	//   - startedAt is WALL time with the monotonic reading stripped
	//     (Round(0)), because it is what the heartbeat REPORTS. An operator
	//     reads "running for 5m2s" beside a feed line stamped 07:14:44 and
	//     subtracts; those two numbers have to come from the same clock. They
	//     did not: `began` came off time.Now() carrying a monotonic reading,
	//     and on a host whose monotonic clock does not advance while it is
	//     suspended — every darwin laptop — time.Sub silently measured AWAKE
	//     time while the feed's own stamps measured wall. On epic dha the
	//     go gate reported 1m0s, 2m1s, 3m1s, 4m2s, 5m2s across beats that
	//     were 17, 16, 29 and 18 WALL minutes apart. The giveaway was in the
	//     same sentence: "last 16m9s ago" was right, because an mtime carries
	//     no monotonic reading and that subtraction fell back to wall. One
	//     line, two clocks, and only the alarming half was true.
	//
	//   - deadline keeps its monotonic reading, deliberately, because it
	//     bounds WORK rather than describing it. A gate that got five minutes
	//     of a suspended laptop's attention has spent five minutes of its
	//     bound, and killing it because the lid was shut for an hour would
	//     manufacture exactly the false refusal cy2 is about.
	//
	// Putting both on the shell also answers m4n's other half: the wrapper
	// can be rebuilt around a live shell without the clock restarting, so a
	// re-derived finish reports the gate's real age rather than its own.
	startedAt time.Time
	deadline  time.Time
}

// gateSentinelScript is the wrapper the gate's shell runs: the declared command
// line, exactly as declared, and then the sentinel that says it is over.
const gateSentinelScript = `sh -c "$TICFAC_GATE_COMMAND"; __ticfac_gate_status=$?; ` +
	`printf %s "$__ticfac_gate_status" > "$TICFAC_GATE_DONE"; exit $__ticfac_gate_status`

// errGateKilled is what a gate that never reported an exit status returns: the
// bound fired, or the run was cancelled under it. `error` is not `fail` — a
// command that was killed has produced no verdict about the ref at all.
var errGateKilled = errors.New("the gate command was killed before it reported an exit status")

// startShell starts one declared gate command and returns without waiting for
// it.
//
// The bound is measured on the host's own clock rather than on the
// reconciler's `now`: it bounds a process this host is running, not anything
// the run reasons about, and a run whose clock a test or a replay holds still
// must not thereby hold a real `sh` open forever. `now` is the run's clock and
// is what the heartbeat reports from — see the field comments above for why
// those are two different clocks on purpose.
// hold, when it is not nil, is a lock on the directory the command runs in. It
// is passed to the shell so that the kernel keeps holding it for as long as
// anything this gate started is alive — including a shell that outlived the
// reconciler. Nothing reads fd 3; being open is the whole job. See gatedir.go.
func startShell(dir, command string, timeout time.Duration, now time.Time, hold *os.File) (*gateShell, error) {
	scratch, err := os.MkdirTemp("", "ticfac-gate-io-")
	if err != nil {
		return nil, fmt.Errorf("prepare the gate's output files: %w", err)
	}
	s := &gateShell{
		scratch:   scratch,
		outPath:   filepath.Join(scratch, "stdout"),
		errPath:   filepath.Join(scratch, "stderr"),
		donePath:  filepath.Join(scratch, "exit"),
		startedAt: now.Round(0),
		deadline:  time.Now().Add(timeout),
	}
	out, err := os.Create(s.outPath)
	if err != nil {
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("prepare the gate's output files: %w", err)
	}
	defer out.Close()
	errOut, err := os.Create(s.errPath)
	if err != nil {
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("prepare the gate's output files: %w", err)
	}
	defer errOut.Close()

	cmd := exec.Command("sh", "-c", gateSentinelScript)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TICFAC_GATE=1", "GIT_TERMINAL_PROMPT=0",
		"TICFAC_GATE_COMMAND="+command, "TICFAC_GATE_DONE="+s.donePath)
	cmd.SysProcAttr = gateProcessGroup()
	cmd.Stdout, cmd.Stderr = out, errOut
	if hold != nil {
		cmd.ExtraFiles = []*os.File{hold}
	}
	cmd.WaitDelay = gateWaitDelay
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(scratch)
		return nil, err
	}
	s.cmd = cmd
	return s, nil
}

// settled answers whether there is nothing left to wait for: the shell wrote
// its sentinel, or the bound this gate was given has passed.
func (s *gateShell) settled() bool {
	if _, err := os.Stat(s.donePath); err == nil {
		return true
	}
	return !time.Now().Before(s.deadline)
}

// wait collects the gate's answer. A shell that has not written its sentinel is
// KILLED first — by group, so a server or watcher the command started dies with
// it (gate_unix.go) — because wait is only ever reached when the caller is done
// waiting: the bound fired, or the run was cancelled under it.
func (s *gateShell) wait() (stdout, stderr string, code int, err error) {
	defer func() { _ = os.RemoveAll(s.scratch) }()

	_, sentinel := os.Stat(s.donePath)
	if sentinel != nil && s.cmd.Process != nil {
		_ = killGateGroup(s.cmd.Process.Pid)
	}
	waitErr := s.cmd.Wait()
	stdout, stderr = readGateOutput(s.outPath), readGateOutput(s.errPath)

	switch {
	case sentinel != nil:
		// The command could not be run, or was killed. Either way this is not
		// a verdict about the ref: `error`, with a negative code that says the
		// process never reported one.
		if waitErr != nil {
			return stdout, stderr, -1, fmt.Errorf("%w: %v", errGateKilled, waitErr)
		}
		return stdout, stderr, -1, errGateKilled
	case waitErr == nil:
		return stdout, stderr, 0, nil
	case s.cmd.ProcessState != nil && s.cmd.ProcessState.Exited():
		return stdout, stderr, s.cmd.ProcessState.ExitCode(), waitErr
	default:
		return stdout, stderr, -1, waitErr
	}
}

// age is how long this shell has been running, in the same wall clock the feed
// stamps its lines with, and remaining is how much of its bound is left in the
// monotonic clock that bound is actually enforced in. The two are equal on a
// host that never suspends and diverge on one that does; reporting each from
// the clock that governs it is the whole of tick m4n.
func (s *gateShell) age(now time.Time) time.Duration { return now.Round(0).Sub(s.startedAt) }

func (s *gateShell) remaining() time.Duration { return time.Until(s.deadline) }

// written is how much output the command has produced so far and when it last
// produced any: the gate's equivalent of the branch tip and worktree mtime that
// dh1 reads off a worker (liveness.go).
//
// It is available for free because the output goes to FILES — one stat, no
// read, nothing consumed that the record needs later — and it is the only
// evidence a run has about whether a gate is getting anywhere. A command that
// has written nothing for twenty minutes may be perfectly healthy (a single
// long Go package prints at the end, not during), which is exactly why this
// feeds a line that says LOOK and never a bound that refuses.
func (s *gateShell) written() (bytes int64, at time.Time) {
	for _, path := range []string{s.outPath, s.errPath} {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		bytes += info.Size()
		if mod := info.ModTime(); mod.After(at) {
			at = mod
		}
	}
	return bytes, at
}

func readGateOutput(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// runShell runs one declared gate command to completion. It is the blocking
// driver over the start-and-poll pair above, for the callers that have nothing
// else to do while a gate runs — and it is what the process-group kill is
// proved through.
func runShell(ctx context.Context, dir, command string, timeout time.Duration) (stdout, stderr string, code int, err error) {
	s, err := startShell(dir, command, timeout, time.Now(), nil)
	if err != nil {
		return "", "", -1, err
	}
	for {
		if ctx.Err() != nil || s.settled() {
			return s.wait()
		}
		time.Sleep(gatePollInterval)
	}
}

// gatePollInterval is how often a blocking driver asks a running gate whether
// it is done. The run loop asks at its OWN cadence — this is only for the
// callers that have nothing else to do.
const gatePollInterval = 10 * time.Millisecond

// redactHostPaths strips every occurrence of any of paths from text, and
// reports whether it changed anything. A path is matched both as given and
// through its resolved form, so a symlinked host temp directory (macOS's
// /tmp -> /private/tmp, for one) is caught either way a command happened to
// print it.
func redactHostPaths(text string, paths []string) (redacted string, changed bool) {
	redacted = text
	for _, path := range paths {
		if path == "" {
			continue
		}
		candidates := []string{path}
		if real, err := filepath.EvalSymlinks(path); err == nil && real != path {
			candidates = append(candidates, real)
		}
		for _, candidate := range candidates {
			if strings.Contains(redacted, candidate) {
				redacted = strings.ReplaceAll(redacted, candidate, "<redacted-host-path>")
				changed = true
			}
		}
	}
	return redacted, changed
}

func bound(text string) string {
	if len(text) <= maxInlineOutput {
		return text
	}
	return text[:maxInlineOutput]
}

func contentDigest(parts ...string) string {
	sum := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(sum, "%d:%s\n", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}
