package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
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

// gateAndClose runs the integrated gate over the merge, records its evidence,
// checks that the evidence is still about what is being published, and closes
// the tick.
func (r *Reconciler) gateAndClose(ctx context.Context, entry planEntry, marker attemptHandle,
	collected *subprocess.Collection, merged merge) error {

	tick := marker.TickID
	if _, err := r.checkpoint(runstate.StateGating,
		fmt.Sprintf("running the integrated gate for %s on %s", tick, short(merged.GateSHA))); err != nil {
		return err
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
		return fmt.Errorf("the gate for %s cannot say which profile dispatched it: %w", tick, err)
	}
	fingerprint := Fingerprint{
		"source_sha":              merged.GateSHA,
		"attempt_head":            merged.AttemptHead,
		"integration_ref":         refFor(r.branch),
		"context_manifest_digest": r.gateDigest,
		"profile_digest":          dispatchProfile.Digest,
	}

	passed := true
	var failures []string
	keys := make([]string, len(r.gate))
	for i, command := range r.gate {
		key, err := r.gateEvidenceKey(tick, marker.Attempt, command.Name, merged.GateSHA)
		if err != nil {
			return err
		}
		keys[i] = key

		// Appendix A #13: a record that cannot say what it evaluated is not
		// evidence. All four fingerprint fields or none of it.
		if outcome := r.RecordEvidence(key, fingerprint); outcome != "recorded" {
			return r.refuse(RefusedStale, tick,
				"the gate's evidence for %s cannot say what it evaluated (%s): %v", tick, outcome, fingerprint)
		}

		record, err := r.runGateCommand(ctx, command, key, marker, merged, fingerprint)
		if err != nil {
			return err
		}
		if record.Result != "pass" {
			passed = false
			failures = append(failures, fmt.Sprintf("%s (%s)", command.Name, record.Result))
		}
	}

	if !passed {
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

	return r.closeTick(ctx, entry, marker, collected, merged)
}

// runGateCommand runs one declared check and records its evidence.
//
// The evidence record is created-if-absent, so a restarted run that already
// paid for this check re-reads its verdict instead of running it again — and a
// record that already exists is never overwritten, because a record that can be
// overwritten is not evidence.
func (r *Reconciler) runGateCommand(ctx context.Context, command GateCommand, key string,
	marker attemptHandle, merged merge, fingerprint Fingerprint) (*runstate.Evidence, error) {

	if _, err := r.store.Fetch(); err != nil {
		return nil, err
	}
	if existing, ok, err := r.store.Evidence(key); err != nil {
		return nil, err
	} else if ok {
		r.record(marker.TickID, StageWaiting, "the %s gate already ran for this attempt: %s", command.Name, existing.Result)
		return existing, nil
	}

	dir, remove, err := r.git.tempWorktree("ticfac-gate-", merged.GateSHA)
	if err != nil {
		return nil, fmt.Errorf("prepare the gate worktree at %s: %w", short(merged.GateSHA), err)
	}
	defer remove()

	started := r.now().UTC().Format(time.RFC3339)
	stdout, stderr, code, runErr := runShell(ctx, dir, command.Command, r.opts.GateTimeout)
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

	// The command ran in `dir`, a throwaway os.MkdirTemp worktree OUTSIDE the
	// repository (git.go's tempWorktree) — a host-local path a failing
	// command's own output (a stack trace, a shell error line naming its
	// $PWD) can carry verbatim. Redact it, the reconciler's own repository
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
			Executor:       runstate.Ptr(subprocess.ExecutorName),
			WorkspaceID:    nil,
			Backend:        nil,
			Role:           runstate.Ptr(marker.Role),
			ProfileDigest:  runstate.Ptr(fingerprint["profile_digest"]),
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
// So a record is keyed to the commit it ran on, whatever it says. When the
// previous record for this check was made at a DIFFERENT commit — a person
// fixed the check or the tree and pushed it, which moves the integration
// branch — this run asks for a record of its own and the check runs again.
// When the commit is the same, the key is the same and the recorded verdict
// stands: re-running a check on a commit nothing changed about would spend the
// same minutes to reach the same answer, and evidence is never overwritten.
//
// A PASS is keyed the same way, and that is the point rather than an oversight.
// The gate is a list of commands and each one carries its own record, so a
// verdict that reused a pass from an earlier commit would close the tick under
// this commit's fingerprint on the strength of a check no run ever performed
// here: the tree that passed and the tree being closed are not the same tree.
// A pass says something about the commit it ran on and about no other.
//
// What "a different commit" means is the SOURCE tree, not the commit id. The
// run writes its own records to `.ticfac/` on the branch it gates, so the
// integration head moves whenever the run checkpoints — every resume, with no
// change to anything a gate command reads. Keying on the raw commit is wrong in
// both directions at once: too loose across a real change (the pass above) and
// too tight across the run's own bookkeeping, where it re-pays for the whole
// gate on every restart. sourceFingerprint is the key that is right in both:
// the gated commit's top-level tree with the run's own entry dropped.
func (r *Reconciler) gateEvidenceKey(tick string, attempt int, check, gateSHA string) (string, error) {
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
	if !ok || existing.Provenance.SourceSHA == gateSHA {
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
	gated, err := r.sourceFingerprint(gateSHA)
	if err != nil {
		return "", fmt.Errorf("fingerprint the source of %s for the gate's evidence key: %w", short(gateSHA), err)
	}
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
		return base + "-" + short(gated), nil
	}
	if gated == recorded {
		return base, nil
	}
	return base + "-" + short(gated), nil
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
// The reconciler reads the gate's output through a pipe (a strings.Builder is
// not a file, so os/exec makes one), and cmd.Wait does not return until that
// pipe is closed — which a grandchild holding the write end can put off
// indefinitely, timeout or no timeout. WaitDelay is the bound on that: past it
// the pipes are closed under whoever still holds them and Wait returns.
const gateWaitDelay = 5 * time.Second

// runShell runs one declared gate command. It is `sh -c` because that is what
// the configuration is: a command LINE, written by the repository's author, in
// the same shell the person who wrote it ran it in.
//
// The timeout is a real bound rather than a hope, in the two halves it takes
// (gate_unix.go): the shell runs as a process GROUP leader and the cancellation
// kills the GROUP, so a server or watcher the gate started dies with it; and
// WaitDelay bounds the wait on the output pipe, so a child that outlived the
// kill cannot hold the run open through it.
func runShell(ctx context.Context, dir, command string, timeout time.Duration) (stdout, stderr string, code int, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TICFAC_GATE=1", "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = gateProcessGroup()
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killGateGroup(cmd.Process.Pid)
	}
	cmd.WaitDelay = gateWaitDelay
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	stdout, stderr = out.String(), errOut.String()

	switch {
	case err == nil:
		return stdout, stderr, 0, nil
	case cmd.ProcessState != nil && cmd.ProcessState.Exited():
		return stdout, stderr, cmd.ProcessState.ExitCode(), err
	default:
		// The command could not be run, or was killed. Either way this is not
		// a verdict about the ref: `error`, with a negative code that says the
		// process never reported one.
		return stdout, stderr, -1, err
	}
}

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
