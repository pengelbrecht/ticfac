package cloudflaresandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// collect: read the branch, parse the report, diff the attempt against its
// recorded base — and ask the door NOTHING.
//
// This file contains no client call, by construction, for the same reason
// herdr's collect contains no herdr call: the facts it reads are the landing
// branch, the report the container's own entrypoint committed to it, and the
// attempt's own record — the durable layer — and a collect that could be
// swayed by a substrate answer is a collect an upgrade can corrupt. Where
// collect LIVES was the open decision this package refused it for (tick xev
// absorbing the finding against 8ty); the decision is now recorded where the
// executor's doc says the contract lives, cloudflare/src/sandbox-dispatch.ts:
// collect is the GO side's, from git, because the orchestrator holds the
// clone and the remote is the durable storage — the same facts
// worker-collect.ts reads through GitHub's API, read through git instead.
//
// The branch it reads is the LANDING branch the door's handle names
// (record.Branch — `tick/<epic>/attempt-<n>/<tick>` as the container derives
// it from its boot's two slots), which is per-attempt by design, and the
// report is at `RESULT-<tick>.md` at the branch's ROOT, because that is the
// pinned image's own contract: the container commits the report itself, in
// its own commit, with an explicit pathspec (image/worker.sh — "in this
// substrate it has to be committed: the container is destroyed and collect
// reads the file off the pushed branch"). Nothing here depends on the
// container's terminal output, which no route can reach at all.
//
// The put-on-write-ref step the WORKFLOW-side executor performs at its own
// collect (tick us2) is NOT repeated here, deliberately: the reconciler's
// integrate already makes the collected head durable on the attempt's write
// ref (durableAttemptHead), so the marker, the collect and the settle all
// name one ref without this executor inventing a second push beside the one
// that exists.

// Collect returns the protocol record.
func (e *Executor) Collect(h *subprocess.JobHandle) (*subprocess.JobResult, error) {
	collected, err := e.CollectDetail(h)
	if err != nil {
		return nil, err
	}
	return collected.Result, nil
}

// resultFile is the report's path on the branch, mirroring worker-collect.ts
// resultFile and worker-boot.ts workerResultFile: the one path the pinned
// image guarantees the report reaches the durable layer at.
func resultFile(tickID string) string {
	return "RESULT-" + tickID + ".md"
}

// fileResult is where the collected protocol record lands beside the attempt
// record, the same name the other two executors use, so a later leg that asks
// whether the attempt's facts are durable reads one vocabulary.
const fileResult = "result.json"

// CollectDetail reads terminal facts from the durable layer.
func (e *Executor) CollectDetail(h *subprocess.JobHandle) (*subprocess.Collection, error) {
	payload, err := local(h)
	if err != nil {
		return nil, err
	}
	if payload.State == "" {
		return nil, fmt.Errorf("collect %s: the handle carries no state directory, so there is no attempt record "+
			"to read the dispatch's facts from", h.JobID)
	}
	record, err := newStore(payload.State).readAttempt()
	if err != nil {
		return nil, fmt.Errorf("collect %s attempt %d: no attempt record at %s: %w",
			h.JobID, h.Attempt, payload.State, err)
	}
	if e.opts.Repo == "" {
		return nil, fmt.Errorf("collect %s attempt %d: no repository is configured, so the durable layer cannot "+
			"be read: the orchestrator's checkout is what a sandbox attempt's work is collected from", h.JobID, h.Attempt)
	}
	st := e.storeAt(payload.State)

	// The landing branch, fetched from the remote into the orchestrator's own
	// checkout so every later read names a commit that is definitely here —
	// integrate resolves the collected head in THIS clone. An unreachable
	// remote is an error, never an empty branch: reading an outage as "no
	// work" is the same guess an unreachable door is refused as (tick avx's
	// rule), in the one other place this substrate crosses a network.
	head, err := e.attemptHead(record)
	if err != nil {
		return nil, err
	}
	commits, err := commitsBeyond(e.opts.Repo, record.BaseSHA, head)
	if err != nil {
		return nil, fmt.Errorf("count the commits beyond %s: %w", shortSHA(record.BaseSHA), err)
	}
	changed, err := changedPaths(e.opts.Repo, record.BaseSHA, head)
	if err != nil {
		return nil, fmt.Errorf("diff %s against %s: %w", record.Branch, shortSHA(record.BaseSHA), err)
	}

	// A10's tracker-record boundary, read from the same shared helpers the
	// other two collects read, so three executors cannot disagree about one
	// diff. Unconditional, like herdr's: the container's own boundary guard
	// (image/worker.sh) is PREVENTION, and the collect is the measurement —
	// and a PREVENTED attempt is the one case the diff alone cannot see, so
	// the marker the container's guard prepends to the report body
	// (BOUNDARY VIOLATION ATTEMPTED — worker-collect.ts's BOUNDARY_REPORT_MARKER,
	// pinned in the worker-boot contract) is surfaced below rather than
	// swallowed by a clean branch.
	violations := subprocess.BoundaryViolations(changed)
	artifactViolations := subprocess.ArtifactPrefixViolations(changed, record.Spec.ArtifactPrefix)

	report, raw, hasReport := e.readReport(record, head)

	// The one lie this substrate's collect could tell about its own branch
	// (tick dyo, finding 73ba193d): the container's entrypoint commits the
	// report itself, in its own commit, at the branch root — so a worker
	// that did NOTHING still leaves one commit beyond the base, and a
	// collect that counts commits alone reads it as ready-to-merge. The
	// work the attempt delivered is the diff MINUS the report, and when that
	// is empty the attempt is the no-commits shape in this substrate's own
	// terms: the same distinct verdict the subprocess executor mints for a
	// worker that committed nothing, with its own sentence, because "the
	// branch is empty" and "the only commit is the entrypoint's report" are
	// two facts an operator acts on the same way but reads differently.
	reportOnly := reportIsOnlyChange(changed, resultFile(record.TickID))

	verdict, outcome, class, reason := classify(record.Spec.Role, commits, reportOnly, hasReport, report,
		violations, artifactViolations)

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
		Artifacts: e.artifacts(st, hasReport, raw),
		Evidence:  []subprocess.EvidenceRef{},
	}
	if hasReport && report.Status != "" {
		result.RoleResult = &subprocess.RoleResult{
			SchemaVersion: subprocess.SchemaVersionRoleResult,
			SchemaID:      record.Spec.OutputSchema,
			Role:          record.Spec.Role,
			Status:        report.Status,
			// The payload and summary are minted through the one shared
			// implementation (subprocess/rolepayload.go, tick b50): three
			// executors cannot disagree about what a review's answer says.
			Summary: subprocess.RoleSummary(record.Spec.Role, report, verdict),
			Result: subprocess.RoleResultPayload(record.Spec.Role, report, verdict, commits, record.Branch,
				append(append([]string{}, violations...), artifactViolations...)),
			Findings: report.Findings,
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
		FindingsFolded:     report.FindingsFolded,
		Message: collectMessage(reason, class, record, head,
			append(append([]string{}, violations...), artifactViolations...)),
	}

	// A PREVENTED boundary attempt is invisible in the diff — the container's
	// guard refused the write and swept it, so the branch reads clean — and
	// the marker its guard prepends to the report is the only durable trace.
	// Surfaced on a ready-to-merge collect because the feed line is where a
	// person reading the run will find it (worker-collect.ts reports the same
	// fact as boundary_attempted for the same reason).
	if collected.Verdict == subprocess.VerdictReadyToMerge && strings.Contains(raw, boundaryReportMarker) {
		collected.Message = "the container caught the worker attempting a tracker-record write and refused it: the " +
			"branch is clean because the violation was prevented, and the report says so"
	}

	// Persisted, and read back before anything acts on it (Appendix A #7):
	// the same discipline the other two collects hold, over the same file
	// name, so the state directory answers "are this attempt's facts
	// durable?" the way every executor's does.
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

// attemptHead is the commit the attempt's landing branch carries on the
// remote, fetched into the orchestrator's own checkout. Empty is the honest
// answer for a container whose push never landed — the branch is not there,
// which is a fact about the work, stated by the remote the durable layer is.
func (e *Executor) attemptHead(record *attemptRecord) (string, error) {
	if record.Branch == "" {
		return "", fmt.Errorf("the attempt record at %s carries no landing branch: the door's handle names one, "+
			"and a collect without it has no durable layer to read", record.State)
	}
	head, err := remoteHead(e.opts.Repo, e.remoteName(), record.Branch)
	if err != nil {
		return "", fmt.Errorf("read %s on %s: %w", record.Branch, e.remoteName(), err)
	}
	if head == "" {
		return "", nil
	}
	fetched, err := fetchBranch(e.opts.Repo, e.remoteName(), record.Branch)
	if err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", record.Branch, e.remoteName(), err)
	}
	if fetched != head {
		return "", fmt.Errorf("%s was fetched as %s after reading %s: the remote moved under the collect, which is "+
			"not a branch to rule on — collect again", record.Branch, shortSHA(fetched), shortSHA(head))
	}
	return head, nil
}

// remoteName is the remote the durable layer lives on. Defaults to origin,
// which is what a run's clone carries; the host may name another.
func (e *Executor) remoteName() string {
	if e.opts.Remote != "" {
		return e.opts.Remote
	}
	return "origin"
}

// readReport reads the attempt's report: the archive a previous collect of
// THIS attempt left beside its record first (a resumed run re-collecting an
// attempt whose container is long gone), then the branch the container
// pushed — the report the container's own entrypoint committed at
// RESULT-<tick>.md, which is the one place this substrate guarantees it.
// The raw body comes back with the parsed report, because the archive below
// writes what was read, not a re-serialisation of a subset of it.
func (e *Executor) readReport(record *attemptRecord, head string) (subprocess.Report, string, bool) {
	if raw, err := os.ReadFile(filepath.Join(record.State, subprocess.FileReportArchive)); err == nil {
		report := subprocess.ParseReport(string(raw))
		report.Path = reportPathOn(record, head)
		return report, string(raw), true
	}
	if head == "" {
		return subprocess.Report{}, "", false
	}
	if body, ok := showFile(e.opts.Repo, head, resultFile(record.TickID)); ok {
		report := subprocess.ParseReport(body)
		report.Path = reportPathOn(record, head)
		return report, body, true
	}
	return subprocess.Report{}, "", false
}

// reportPathOn is where the report was read from, stated as the durable layer
// states it: the branch and the path, never a host path. The subprocess
// executor's equivalent carries its run-relative worktree path; this
// substrate has no worktree, and the branch is the only place the report
// exists.
func reportPathOn(record *attemptRecord, head string) string {
	if head != "" {
		return record.Branch + ":" + resultFile(record.TickID)
	}
	return subprocess.FileReportArchive
}

// artifacts are the report archive, kept in the attempt's state directory so
// it survives the container the way it survives a herdr worktree's removal
// (tick 35h): the analysis an attempt produced is what a redispatch's prompt
// reads (tick nvn), and on this substrate the branch is durable but a person
// reading the attempt should not have to fetch a ref to see what it said.
func (e *Executor) artifacts(st *store, hasReport bool, raw string) []subprocess.ArtifactRef {
	out := []subprocess.ArtifactRef{}
	if !hasReport || raw == "" {
		return out
	}
	archive := st.path(subprocess.FileReportArchive)
	if err := st.writeFile(archive, []byte(raw), 0o644); err != nil {
		// An archive that cannot be written is an operational problem, not a
		// verdict: the report is still on the branch, the reference says so,
		// and the failure is observed rather than swallowed.
		e.observe(st, "the report could not be archived beside the attempt record: "+err.Error())
		return out
	}
	return []subprocess.ArtifactRef{{
		Kind:          "report",
		URI:           "file://" + archive,
		ContentDigest: digestOf([]byte(raw)),
		Bytes:         len(raw),
	}}
}

// observe appends one bounded diagnostic line to the attempt's state
// directory, the same file name the other executors' stores carry. It is
// exhaust, never a verdict: an append that fails is dropped, because a
// diagnostic is not worth failing a collect that has its facts in hand.
func (e *Executor) observe(st *store, detail string) {
	line, err := json.Marshal(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited, Detail: detail})
	if err != nil {
		return
	}
	f, err := os.OpenFile(st.path(fileObservations), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// fileObservations is the attempt's diagnostic log, the same name the other
// executors' stores use.
const fileObservations = "observations.jsonl"

func digestOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// reportIsOnlyChange says whether every path the attempt changed beyond its
// base is the report the container's own entrypoint commits — the shape a
// worker that did no work leaves on this substrate, where the subprocess
// executor's empty branch is impossible by construction. Nothing changed is
// NOT this shape: that is the push that never landed or the honest empty
// branch, and it keeps its own verdict and message.
func reportIsOnlyChange(changed []string, reportPath string) bool {
	if len(changed) == 0 {
		return false
	}
	for _, path := range changed {
		if path != reportPath {
			return false
		}
	}
	return true
}

// classify is the verdict, in the order the checks run: the first FAILING
// check wins, and the order is the collect vocabulary's own — shared with the
// local executor and herdr's copy of it, because the same tick with the same
// facts must collect the same verdict on every substrate, and a re-ordered
// copy of the checks is how two executors come to disagree about one attempt
// with nothing failing.
//
// What this substrate has NO checks for is deliberate and decided (tick xev,
// recorded beside the door's contract): no cancelled case, because cancel is
// refused — the credential a container holds is the run's own token (D17), so
// there is no per-attempt dispatch to revoke; and no wall-clock marker,
// because the bound is enforced by the container's own supervisor
// (TICKS_WORKER_TIMEOUT) and the run's overrun is recorded from the INSPECT
// that observed the settle, not minted here out of a marker this side never
// writes.
func classify(role string, commits int, reportOnly bool, hasReport bool, report subprocess.Report,
	violations, artifactViolations []string) (verdict, outcome, class, reason string) {
	switch {
	case !hasReport || report.Status == "":
		if !hasReport {
			return subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonSettledNoReport
		}
		// A report the container left that carries no STATUS line: the worker
		// finished and left an answer nobody can read — its own shape, not
		// the same sentence as a report that was never written.
		return subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonReportNoStatus
	case commits == 0 && subprocess.NoCommitsIsFailure(role):
		return subprocess.VerdictNoCommits, subprocess.OutcomeFailed, subprocess.FailureRunnerError, subprocess.VerdictNoCommits
	case reportOnly && subprocess.NoCommitsIsFailure(role):
		// The report-only branch (tick dyo): the closed vocabulary's own word
		// for a worker that delivered nothing is `no-commits`, never a fifth
		// word — and the ROLE's recorded rule (NoCommitsIsFailure) is part of
		// the verdict exactly as it is for the empty branch: a review whose
		// only commit is its report delivered its whole deliverable.
		return subprocess.VerdictNoCommits, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonReportOnly
	case len(violations) > 0:
		return subprocess.VerdictBoundaryViolation, subprocess.OutcomeFailed, subprocess.FailureRunnerError, subprocess.VerdictBoundaryViolation
	case len(artifactViolations) > 0:
		return subprocess.VerdictBoundaryViolation, subprocess.OutcomeFailed, subprocess.FailureRunnerError, reasonArtifactCommitted
	default:
		return subprocess.VerdictReadyToMerge, subprocess.OutcomeSucceeded, "", subprocess.VerdictReadyToMerge
	}
}

// boundaryReportMarker is the line image/worker.sh prepends to the report
// when its boundary guard caught the agent writing tracker state. The same
// literal worker-collect.ts pins as BOUNDARY_REPORT_MARKER, mirrored rather
// than imported: the pinned image's own entrypoint writes it, this side's
// tests pin the spelling, and a worker that never crossed the boundary
// writes no line at all.
const boundaryReportMarker = "BOUNDARY VIOLATION ATTEMPTED"

// The message keys. A reason is not a verdict — the collect vocabulary is
// closed and this executor does not add a fifth word to it — it is what the
// MESSAGE is keyed on, because a container that pushed nothing and one that
// reported without a status line both leave facts worth telling apart
// (Appendix A #9).
const (
	reasonSettledNoReport   = "settled-no-report"
	reasonReportNoStatus    = "report-no-status"
	reasonArtifactCommitted = "artifact-committed"

	// reasonReportOnly is the report-only branch of `no-commits` (tick dyo):
	// the branch is not empty on this substrate — the entrypoint committed
	// the report — so the sentence says the one commit that is there rather
	// than one that is not.
	reasonReportOnly = "report-only"
)

// collectMessage keeps two failures from sharing one sentence.
func collectMessage(reason, class string, record *attemptRecord, head string, violations []string) string {
	switch reason {
	case subprocess.VerdictReadyToMerge:
		return ""
	case subprocess.VerdictNoCommits:
		return fmt.Sprintf("the attempt branch carries no commit beyond the base it was cut from (%s)",
			shortSHA(record.BaseSHA))
	case reasonReportOnly:
		return fmt.Sprintf("the only commit %s carries beyond its base (%s) is the container's own report at %s: "+
			"the worker committed no work, and a report is not a deliverable",
			record.Branch, shortSHA(record.BaseSHA), resultFile(record.TickID))
	case subprocess.VerdictBoundaryViolation:
		// Shared with the other two executors (tick 54n): the refusal names
		// the role and the permitted destinations, rendered from the
		// boundary's own single exemption list.
		return subprocess.BoundaryRefusal(record.Spec.Role, record.Spec.ArtifactPrefix, violations)
	case reasonArtifactCommitted:
		return fmt.Sprintf("the %s attempt committed its own report or artifact into the branch, "+
			"under the prefix this executor owns: %v. The report belongs under %s, not on the branch",
			record.Spec.Role, violations, record.Spec.ArtifactPrefix)
	case reasonSettledNoReport:
		if head == "" {
			return fmt.Sprintf("the container's branch %s carries no report and no commit beyond the base it was cut from: "+
				"the push never landed, and this is neither running nor done", record.Branch)
		}
		return fmt.Sprintf("the container pushed %s with no report at %s committed to it: settled is not finished, "+
			"and this is neither running nor done", record.Branch, resultFile(record.TickID))
	case reasonReportNoStatus:
		return fmt.Sprintf("the report at %s on %s carries no STATUS line: the worker finished and left an answer "+
			"nobody can read", resultFile(record.TickID), record.Branch)
	}
	return "the attempt failed"
}

// headOrNil is the collected head as a stated fact rather than an inferred
// absence: no commit produced reads as null, not as a sha nobody checked.
func headOrNil(head string, commits int) *string {
	if head == "" || commits == 0 {
		return nil
	}
	value := head
	return &value
}
