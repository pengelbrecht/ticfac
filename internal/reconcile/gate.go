package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
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

	fingerprint := Fingerprint{
		"source_sha":              merged.AttemptHead,
		"integration_ref":         refFor(r.branch),
		"context_manifest_digest": r.gateDigest,
		"profile_digest":          digestOf("profile", r.profile),
	}

	passed := true
	var failures []string
	for _, command := range r.gate {
		key := evidenceKey(tick, marker.Attempt, command.Name)

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
				"gate is a close nothing stands behind", short(merged.GateSHA), tick, strings.Join(failures, ", "))
	}
	r.record(tick, StageGatePassed, "the integrated gate passed on %s (%s)", short(merged.GateSHA), r.gate)

	// Appendix A #13's other half: the target may have moved between the check
	// and the publication. The record is still true about what it evaluated and
	// is no longer true about what is being published.
	target, err := r.currentTarget(marker)
	if err != nil {
		return err
	}
	for _, command := range r.gate {
		key := evidenceKey(tick, marker.Attempt, command.Name)
		if outcome := r.PublishEvidence(key, target); outcome != "published" {
			r.record(tick, StageStale, "the gate's evidence is no longer about what would be published (%s)", outcome)
			return r.refuse(RefusedStale, tick,
				"the gate's evidence for %s evaluated %s and the current target is %s: publishing it would state a "+
					"verdict about something else", tick, fingerprint["source_sha"], target["source_sha"])
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

	tickID, attempt := marker.TickID, marker.Attempt
	record := runstate.Evidence{
		Key: key,
		Provenance: runstate.Provenance{
			RunID:                 r.runID,
			TickID:                &tickID,
			Attempt:               &attempt,
			SourceRef:             marker.WriteRef,
			SourceSHA:             fingerprint["source_sha"],
			IntegrationRef:        runstate.Ptr(fingerprint["integration_ref"]),
			Phase:                 runstate.PhaseIntegrated,
			Executor:              runstate.Ptr(subprocess.ExecutorName),
			WorkspaceID:           nil,
			Backend:               nil,
			Role:                  runstate.Ptr(marker.Role),
			ProfileDigest:         runstate.Ptr(fingerprint["profile_digest"]),
			Model:                 nil,
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
			Redacted:  false,
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
func (r *Reconciler) currentTarget(marker attemptHandle) (Fingerprint, error) {
	head, err := r.git.remoteHead(branchOf(marker.WriteRef))
	if err != nil {
		return nil, err
	}
	gate, err := ReadGateCommands(r.opts.GateConfig)
	if err != nil {
		return nil, err
	}
	return Fingerprint{
		"source_sha":              head,
		"integration_ref":         refFor(r.branch),
		"context_manifest_digest": gate.Digest(),
		"profile_digest":          digestOf("profile", r.profile),
	}, nil
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

	current, err := r.opts.Tracker.Show(ctx, tick)
	if err != nil {
		return fmt.Errorf("read tick %s before closing it: %w", tick, err)
	}
	if current.Status != "closed" {
		note := fmt.Sprintf("ticfac run %s: attempt %d merged into %s as %s; the integrated gate (%s) passed.",
			r.runID, marker.Attempt, r.branch, short(merged.GateSHA), r.gate)
		if _, err := r.opts.Tracker.Note(ctx, tick, note); err != nil {
			return fmt.Errorf("note the gate evidence on %s: %w", tick, err)
		}
		if _, err := r.opts.Tracker.Close(ctx, tick); err != nil {
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

// runShell runs one declared gate command. It is `sh -c` because that is what
// the configuration is: a command LINE, written by the repository's author, in
// the same shell the person who wrote it ran it in.
func runShell(ctx context.Context, dir, command string, timeout time.Duration) (stdout, stderr string, code int, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TICFAC_GATE=1", "GIT_TERMINAL_PROMPT=0")
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
