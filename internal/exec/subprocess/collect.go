package subprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// collect: read the branch, parse the report, diff the attempt against its
// recorded base, and say what happened in the vocabulary all three collect
// implementations share.
//
// Terminal output is never consulted for the verdict. The runner log is
// attached as a transcript artifact, which is what SPEC §10.1 means by
// diagnostic material: useful, and not a completion contract.

// Collection is the host's view of a collect: the protocol record, plus the
// facts the record has nowhere to put. A JobResult with no report cannot carry
// a role_result — its status vocabulary is closed and this executor will not
// invent a worker's answer — so the boundary attempts, which A10 requires to
// be REPORTED whatever else happened, travel here and in the observation log.
type Collection struct {
	Result             *JobResult
	Verdict            string
	Report             Report
	HasReport          bool
	BoundaryViolations []string
	Message            string
}

// Collect returns the protocol record.
func (e *Executor) Collect(h *JobHandle) (*JobResult, error) {
	collected, err := e.CollectDetail(h)
	if err != nil {
		return nil, err
	}
	return collected.Result, nil
}

// CollectDetail reads terminal facts from the durable layer.
func (e *Executor) CollectDetail(h *JobHandle) (*Collection, error) {
	local, err := h.Local()
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
	var violations []string
	if e.guarded("substrate_enforces_boundary") {
		violations = BoundaryViolations(changed)
	}
	for _, path := range violations {
		// A10's reporting half: a boundary that refuses silently tells nobody
		// the model tried.
		_ = st.observe(Observation{At: e.stamp(), Kind: ObsExited,
			Detail: "boundary violation: the attempt wrote " + path})
	}

	report, hasReport := e.readReport(record)
	_, cancelled := st.cancelled()

	verdict, outcome, class, reason := e.classify(st, commits, hasReport, report, violations, cancelled)

	result := &JobResult{
		SchemaVersion: SchemaVersion,
		JobID:         h.JobID,
		Attempt:       record.Attempt,
		Outcome:       outcome,
		FailureClass:  class,
		FinishedAt:    e.finishedAt(st),
		Source: ResultSource{
			BaseSHA:  record.BaseSHA,
			WriteRef: record.WriteRef,
			HeadSHA:  headOrNil(head, commits),
			Commits:  commits,
		},
		Artifacts: e.artifacts(st, record, hasReport, report),
		Evidence:  []EvidenceRef{},
	}
	if hasReport && report.Status != "" {
		result.RoleResult = &RoleResult{
			SchemaVersion: SchemaVersion,
			SchemaID:      record.Spec.OutputSchema,
			Role:          record.Spec.Role,
			Status:        report.Status,
			Summary:       summaryOf(report, verdict),
			Result: map[string]any{
				"verdict":             verdict,
				"commits":             commits,
				"branch":              record.Branch,
				"report_path":         report.Path,
				"boundary_violations": stringsOrEmpty(violations),
				"needs_human":         report.NeedsHuman(),
			},
		}
	}

	collected := &Collection{
		Result:             result,
		Verdict:            verdict,
		Report:             report,
		HasReport:          hasReport,
		BoundaryViolations: violations,
		Message:            e.message(reason, class, record, violations),
	}

	// Persisted, and read back before anything acts on it: disposal asks this
	// file whether the attempt's facts are durable yet.
	if err := st.writeJSON(fileResult, result); err != nil {
		return nil, fmt.Errorf("persist the collected result: %w", err)
	}
	if e.guarded("read_back_after_write") {
		var confirm JobResult
		if err := st.readJSON(fileResult, &confirm); err != nil {
			return nil, fmt.Errorf("the collected result did not land: %w", err)
		}
		if confirm.Outcome != result.Outcome || confirm.JobID != result.JobID {
			return nil, fmt.Errorf("the collected result read back as %s/%s, not %s/%s",
				confirm.JobID, confirm.Outcome, result.JobID, result.Outcome)
		}
	}
	return collected, nil
}

// classify is the verdict, in the order the checks run: the first FAILING
// check wins. The order is the collect vocabulary's own, and it is why a
// worker that reports DONE over a branch with no commits is `no-commits`
// rather than ready to merge.
// `reason` is what the MESSAGE is keyed on, and it is not always the verdict:
// a cancellation and a worker that never reported both leave no report, and
// telling a person the same sentence about both is Appendix A #9's failure.
func (e *Executor) classify(st *store, commits int, hasReport bool,
	report Report, violations []string, cancelled bool) (verdict, outcome, class, reason string) {

	switch {
	case cancelled:
		return VerdictMissingResult, OutcomeCancelled, "", reasonCancelled
	case commits == 0:
		return VerdictNoCommits, OutcomeFailed, FailureRunnerError, VerdictNoCommits
	case !hasReport || report.Status == "":
		if st.wallClockExceeded() {
			return VerdictMissingResult, OutcomeFailed, FailureWallClockExceeded, VerdictMissingResult
		}
		return VerdictMissingResult, OutcomeFailed, FailureRunnerError, VerdictMissingResult
	case len(violations) > 0:
		return VerdictBoundaryViolation, OutcomeFailed, FailureRunnerError, VerdictBoundaryViolation
	default:
		return VerdictReadyToMerge, OutcomeSucceeded, "", VerdictReadyToMerge
	}
}

// reasonCancelled is not a verdict — the collect vocabulary is closed and this
// executor does not add a fifth word to it. It is a message key.
const reasonCancelled = "cancelled"

// failureMessages keeps two failures from sharing one sentence. Appendix A #9
// is not about the class field, which has six values for many more failures —
// it is about the MESSAGE a person reads, and "the run broke" is the message
// that sends a diagnosis looking for the wrong thing.
var failureMessages = map[string]string{
	VerdictNoCommits:         "the attempt branch carries no commit beyond the base it was cut from",
	VerdictMissingResult:     "there is no report at the path this executor owns, so the attempt never said what it did",
	VerdictBoundaryViolation: "the attempt committed records under an authority that is not its own",
	reasonCancelled:          "the attempt was cancelled: its credential was revoked and then it was stopped",
	VerdictReadyToMerge:      "",
}

const collapsedMessage = "the attempt failed"

func (e *Executor) message(reason, class string, record *attemptRecord, violations []string) string {
	if !e.guarded("distinct_failure_classes") {
		// The negative control: every failure reads the same, which is how an
		// expired lease and a stolen one become one incident report.
		if reason == VerdictReadyToMerge {
			return ""
		}
		return collapsedMessage
	}
	base := failureMessages[reason]
	switch {
	case base == "":
		return ""
	case class == FailureWallClockExceeded:
		return fmt.Sprintf("%s: it was stopped at its wall clock of %d seconds", base, record.WallSeconds)
	case reason == VerdictBoundaryViolation:
		return fmt.Sprintf("%s: %v", base, violations)
	case reason == VerdictNoCommits:
		return fmt.Sprintf("%s (%s)", base, short(record.BaseSHA))
	default:
		return base
	}
}

// finishedAt is stable across collects: a terminal fact that moves every time
// somebody asks for it is not a terminal fact.
func (e *Executor) finishedAt(st *store) string {
	var previous JobResult
	if err := st.readJSON(fileResult, &previous); err == nil && previous.FinishedAt != "" {
		return previous.FinishedAt
	}
	if info, err := os.Stat(st.path(fileRunnerExit)); err == nil {
		return info.ModTime().UTC().Format(time.RFC3339)
	}
	return e.stamp()
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

func summaryOf(report Report, verdict string) string {
	if report.Detail != "" {
		return report.Detail
	}
	return fmt.Sprintf("%s (%s)", report.Status, verdict)
}

func stringsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// artifacts are the report and the transcript. The transcript is attached
// BECAUSE it is diagnostic material — naming it as an artifact is what keeps
// it from being mistaken for the completion contract.
func (e *Executor) artifacts(st *store, record *attemptRecord, hasReport bool, report Report) []ArtifactRef {
	out := []ArtifactRef{}
	if hasReport {
		if raw, err := os.ReadFile(record.ResultPath); err == nil {
			out = append(out, ArtifactRef{
				Kind:          "report",
				URI:           "file://" + record.ResultPath,
				ContentDigest: digestOf(raw),
				Bytes:         len(raw),
			})
		}
	}
	if raw, err := os.ReadFile(st.path(fileRunnerLog)); err == nil {
		out = append(out, ArtifactRef{
			Kind:          "transcript",
			URI:           "file://" + st.path(fileRunnerLog),
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
