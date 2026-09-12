package herdr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// collect: read the branch, parse the report, diff the attempt against its
// recorded base — and ask herdr NOTHING.
//
// This file contains no client call, by construction, because that is the
// seam tick 2xu exists to assert: herdr answers "is the agent alive", never
// "did the work succeed", and a collect that could be swayed by a substrate
// answer is a collect an upgrade can corrupt. The facts it reads are the
// report file, the branch, and this executor's own settlement record — the
// `agent-gone` marker the inspect that observed the departure wrote. An
// attempt that settled in a way nothing recorded (a herdr that was down for
// every poll) is `lost` at inspect and is refused here rather than guessed
// about.
//
// The artifact boundary is enforced here the same two ways the local
// executor enforces it (tick p6b): Start excludes the job's artifact prefix
// from git in the worktree before the agent ever launches, and the backstop
// below re-checks that boundary against the branch diff because prevention
// is bypassable — a `git add -f` past the exclude collects as a boundary
// violation, refused and REPORTED, never silently dropped. Neither layer
// reads the prompt, and neither knows the agent's kind. The tracker-record
// boundary (A10) IS "reading the branch", so it is here too, shared with the
// local executor through the same exported helper.

// Collect returns the protocol record.
func (e *Executor) Collect(h *subprocess.JobHandle) (*subprocess.JobResult, error) {
	collected, err := e.CollectDetail(h)
	if err != nil {
		return nil, err
	}
	return collected.Result, nil
}

// CollectDetail reads terminal facts from the durable layer.
func (e *Executor) CollectDetail(h *subprocess.JobHandle) (*subprocess.Collection, error) {
	local, err := local(h)
	if err != nil {
		return nil, err
	}
	st := e.storeAt(local.State)
	record, err := st.readAttempt()
	if err != nil {
		return nil, fmt.Errorf("collect %s attempt %d: no attempt record at %s: %w",
			h.JobID, h.Attempt, local.State, err)
	}

	head := headOf(record.Repo, record.Branch)
	commits, err := commitsBeyond(record.Repo, record.BaseSHA, head)
	if err != nil {
		return nil, fmt.Errorf("count the commits beyond %s: %w", short(record.BaseSHA), err)
	}
	changed, err := changedPaths(record.Repo, record.BaseSHA, head)
	if err != nil {
		return nil, fmt.Errorf("diff %s against %s: %w", record.Branch, short(record.BaseSHA), err)
	}
	violations := subprocess.BoundaryViolations(changed)
	for _, path := range violations {
		// A10's reporting half: a boundary that refuses silently tells
		// nobody the model tried.
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: "boundary violation: the attempt wrote " + path})
	}

	// The backstop behind Start's git exclude: a path under the job's OWN
	// artifact_prefix that made it into the branch diff means something
	// bypassed the exclude (a `git add -f`, most likely). Reported the same
	// way a tracker write is — as an observation — and refused with the
	// same closed-vocabulary verdict, though it is kept a separate KIND of
	// violation: the report is this executor's own artifact, not the
	// tracker's authority.
	artifactViolations := subprocess.ArtifactPrefixViolations(changed, record.Spec.ArtifactPrefix)
	for _, path := range artifactViolations {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: "boundary violation: the attempt committed its own report or artifact at " + path})
	}
	allViolations := append(append([]string{}, violations...), artifactViolations...)

	report, hasReport := e.readReport(record)
	_, cancelled := st.cancelled()
	agentGone := st.agentGone()
	wallExceeded := st.wallExceeded()

	// x6j's hold, at the one place a verdict could have been minted out of
	// the substrate's silence. No report, no cancellation, no settlement
	// any leg recorded — the attempt was never answered, positively or
	// otherwise, and nobody can say whether it is still running. That is
	// not "the worker failed", which is a verdict; it is a question only a
	// person can settle, so the collect refuses with the liveness-unknown
	// hold rather than answering. The reconciler stops without rejecting
	// the tick, nothing is torn down after a refusal, and the next run's
	// adopt holds the attempt for a person. (A launch that was never
	// confirmed is the executor's OWN settlement record and passes; the
	// report and the cancellation record are durable evidence and pass —
	// and so does the wall-clock stop marker (gwc), which is this
	// executor's own durable record that IT settled the attempt, never
	// herdr's silence.)
	if !hasReport && !cancelled && !agentGone && !wallExceeded && record.LaunchConfirmed {
		return nil, refuse(subprocess.RefusedUnknown,
			"attempt %d of %s has no report at %s and no settlement this executor recorded: nobody can say "+
				"whether it is still running, which is not the same as nothing running — it is held for a person, "+
				"never collected into a verdict",
			record.Attempt, record.JobID, record.ResultPath)
	}

	verdict, outcome, class, reason := classify(commits, hasReport, report, violations, artifactViolations, cancelled, wallExceeded)

	result := &subprocess.JobResult{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         h.JobID,
		Attempt:       record.Attempt,
		Outcome:       outcome,
		FailureClass:  class,
		FinishedAt:    e.stamp(),
		Source: subprocess.ResultSource{
			BaseSHA:  record.BaseSHA,
			WriteRef: record.WriteRef,
			HeadSHA:  headOrNil(head, commits),
			Commits:  commits,
		},
		Artifacts: e.artifacts(record, hasReport, report),
		Evidence:  []subprocess.EvidenceRef{},
	}
	if hasReport && report.Status != "" {
		result.RoleResult = &subprocess.RoleResult{
			SchemaVersion: subprocess.SchemaVersion,
			SchemaID:      record.Spec.OutputSchema,
			Role:          record.Spec.Role,
			Status:        report.Status,
			Summary:       summaryOf(report, verdict),
			Result: map[string]any{
				"verdict":             verdict,
				"commits":             commits,
				"branch":              record.Branch,
				"report_path":         report.Path,
				"boundary_violations": violationsOrEmpty(allViolations),
				"needs_human":         report.NeedsHuman(),
				// The findings channel (tick 7vn), shared with the local executor
				// through the same helper and the same vocabulary: a report block
				// means the same thing whichever executor collected it.
				"findings":         subprocess.FindingsAsAny(report.Findings),
				"findings_problem": report.FindingsProblem,
			},
		}
	}

	collected := &subprocess.Collection{
		Result:             result,
		Verdict:            verdict,
		Report:             report,
		HasReport:          hasReport,
		BoundaryViolations: violations,
		ArtifactViolations: artifactViolations,
		Findings:           report.Findings,
		FindingsProblem:    report.FindingsProblem,
		Message:            collectMessage(reason, class, record, allViolations),
	}

	// Persisted, and read back before anything acts on it: disposal asks
	// this file whether the attempt's facts are durable yet.
	if err := st.writeJSON(fileResult, result); err != nil {
		return nil, fmt.Errorf("persist the collected result: %w", err)
	}
	var confirm subprocess.JobResult
	if err := st.readJSON(fileResult, &confirm); err != nil {
		return nil, fmt.Errorf("the collected result did not land: %w", err)
	}
	if confirm.Outcome != result.Outcome || confirm.JobID != result.JobID {
		return nil, fmt.Errorf("the collected result read back as %s/%s, not %s/%s",
			confirm.JobID, confirm.Outcome, result.JobID, result.Outcome)
	}
	return collected, nil
}

// classify is the verdict, in the order the checks run: the first FAILING
// check wins. The order is the collect vocabulary's own, shared with the
// local executor — including the artifact-prefix backstop, which sits where
// the local executor puts it, after the tracker-record boundary.
//
// The wall-clock stop carries the failure class Phase 1 already
// distinguishes — wall_clock_exceeded, the same word the local executor's
// collect answers — but it does not by itself decide anything: the checks
// ahead of it (cancelled, no-commits) outrank it, and a worker that reported
// before the stop caught it never reaches a failing check at all, because
// the report and the branch are read the usual way.
//
// It is reached only for attempts that carry settlement evidence — the
// liveness-unknown hold in CollectDetail returned first — so every branch
// below is a verdict from durable evidence, never a guess.
func classify(commits int, hasReport bool, report subprocess.Report,
	violations, artifactViolations []string, cancelled, wallExceeded bool) (verdict, outcome, class, reason string) {

	// The order is the LOCAL executor's own, because it is the collect
	// vocabulary's: the same tick with the same facts must collect the same
	// verdict on either executor, and a re-ordered copy of the checks here
	// is how the two come to disagree about one attempt with nothing
	// failing.
	switch {
	case cancelled:
		return subprocess.VerdictMissingResult, subprocess.OutcomeCancelled, "", reasonCancelled
	case commits == 0:
		return subprocess.VerdictNoCommits, subprocess.OutcomeFailed, subprocess.FailureRunnerError, subprocess.VerdictNoCommits
	case !hasReport || report.Status == "":
		// No report, or one nobody can read. Which of the two it is, the
		// SETTLEMENT evidence has already answered — the hold in
		// CollectDetail returned for the attempt nothing settled, so every
		// shape left here carries durable evidence. A worker this executor
		// stopped at its wall clock is its own failure class:
		// stopped-at-the-bound and merely settled are different verdicts,
		// and the durable wall marker is what keeps them apart on a
		// herdr-free collect — the same first-failing-check order the local
		// executor's collect answers.
		if wallExceeded {
			return subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureWallClockExceeded, reasonWallClockStopped
		}
		if !hasReport {
			// No report, on an attempt the durable layer settled: the
			// agent-gone marker — herdr's POSITIVE answer, recorded when it
			// was observed — or the executor's own record of a launch that
			// never confirmed.
			return subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonSettledNoReport
		}
		// A report the worker left that carries no STATUS line: the worker
		// finished and left an answer nobody can read — its own shape, not
		// the same sentence as a report that was never written.
		return subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonReportNoStatus
	case len(violations) > 0:
		return subprocess.VerdictBoundaryViolation, subprocess.OutcomeFailed, subprocess.FailureRunnerError, subprocess.VerdictBoundaryViolation
	case len(artifactViolations) > 0:
		// The RESULT artifact riding into the branch is the exact incident
		// this executor's own exclude exists to stop; finding one here means
		// an agent bypassed it. It reads as the same closed-vocabulary
		// verdict a tracker write does — collect does not invent a fifth
		// word — and it is merge-blocking for the same reason: an attempt
		// that put its own report artifact into the branch is not one to
		// merge as-is, whatever else it did.
		return subprocess.VerdictBoundaryViolation, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonArtifactCommitted
	default:
		return subprocess.VerdictReadyToMerge, subprocess.OutcomeSucceeded, "", subprocess.VerdictReadyToMerge
	}
}

// The message keys. A reason is not a verdict — the collect vocabulary is
// closed and this executor does not add a fifth word to it — it is what the
// MESSAGE is keyed on, because a cancellation and a worker that never
// reported both leave no report, and telling a person the same sentence
// about both is Appendix A #9's failure (the local executor spells this out
// at the same place).
const (
	reasonCancelled         = "cancelled"
	reasonSettledNoReport   = "settled-no-report"
	reasonReportNoStatus    = "report-no-status"
	reasonArtifactCommitted = "artifact-committed"
	// reasonWallClockStopped is the stop at the bound: a distinct reason so
	// the message keyed on it can say what happened without inheriting the
	// settled or unsettled sentence.
	reasonWallClockStopped = "stopped-at-wall-clock"
)

// collectMessage keeps two failures from sharing one sentence.
func collectMessage(reason, class string, record *attemptRecord, violations []string) string {
	switch reason {
	case subprocess.VerdictReadyToMerge:
		return ""
	case subprocess.VerdictNoCommits:
		return fmt.Sprintf("the attempt branch carries no commit beyond the base it was cut from (%s)", short(record.BaseSHA))
	case subprocess.VerdictBoundaryViolation:
		return fmt.Sprintf("the attempt committed records under an authority that is not its own: %v", violations)
	case reasonCancelled:
		return "the attempt was cancelled: its dispatch was revoked and then the agent was interrupted"
	case reasonSettledNoReport:
		return fmt.Sprintf("the agent settled with no report at %s: settled is not finished, and this is neither running nor done",
			record.ResultPath)
	case reasonReportNoStatus:
		return fmt.Sprintf("the report at %s carries no STATUS line: the worker finished and left an answer nobody can read",
			record.ResultPath)
	case reasonArtifactCommitted:
		// Not the tracker sentence: a committed report artifact is this
		// executor's own boundary the agent bypassed, and telling a person
		// "records under an authority that is not its own" sends the
		// diagnosis looking at the wrong thing (Appendix A #9).
		return fmt.Sprintf("the attempt committed its own report or artifact into the branch, under the prefix this executor owns: %v", violations)
	}
	if class == subprocess.FailureWallClockExceeded {
		return fmt.Sprintf("it was stopped at its wall clock of %d seconds", record.WallSeconds)
	}
	return "the attempt failed"
}

func headOrNil(head string, commits int) *string {
	if head == "" || commits == 0 {
		// Stated as a fact rather than inferred from a missing key: the job
		// produced no commit.
		return nil
	}
	value := head
	return &value
}

func summaryOf(report subprocess.Report, verdict string) string {
	if report.Detail != "" {
		return report.Detail
	}
	return fmt.Sprintf("%s (%s)", report.Status, verdict)
}

// violationsOrEmpty states the empty boundary as [] rather than nil, the way
// the local executor does: a record field that is absent and one that was
// counted and found empty must not read differently downstream.
func violationsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// artifacts are the report and the rendered worker prompt — the record of
// what the agent was actually asked to do, which survives the worktree.
func (e *Executor) artifacts(record *attemptRecord, hasReport bool, report subprocess.Report) []subprocess.ArtifactRef {
	out := []subprocess.ArtifactRef{}
	if hasReport {
		if raw, err := os.ReadFile(record.ResultPath); err == nil {
			out = append(out, subprocess.ArtifactRef{
				Kind:          "report",
				URI:           "file://" + record.ResultPath,
				ContentDigest: digestOf(raw),
				Bytes:         len(raw),
			})
		}
	}
	if raw, err := os.ReadFile(record.State + "/" + filePrompt); err == nil {
		out = append(out, subprocess.ArtifactRef{
			Kind:          "prompt",
			URI:           "file://" + record.State + "/" + filePrompt,
			ContentDigest: digestOf(raw),
			Bytes:         len(raw),
		})
	}
	return out
}

func digestOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
