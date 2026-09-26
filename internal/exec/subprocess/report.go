package subprocess

import (
	"fmt"
	"regexp"
	"strings"
)

// The worker's report and the boundary, read the way contracts/collect-vocabulary.json
// and contracts/lifecycle-invariants.json say to read them.
//
// The status-line pattern and the decoration trim set are the bundle's, copied
// here BY VALUE and pinned to the fixture by report_test.go — an executor
// cannot read the bundle off disk at run time, and a re-spelled verdict is the
// bug that makes a cloud run and a local run disagree about what happened to
// the same tick with nothing failing.

// The four verdicts, in the order the checks run: the first failing check wins.
const (
	VerdictReadyToMerge      = "ready-to-merge"
	VerdictNoCommits         = "no-commits"
	VerdictMissingResult     = "missing-result"
	VerdictBoundaryViolation = "boundary-violation"
)

// The four statuses a worker's report may end with. Independent of the
// verdict: a worker can commit, write a report and still report BLOCKED.
const (
	StatusDone             = "DONE"
	StatusDoneWithConcerns = "DONE_WITH_CONCERNS"
	StatusNeedsContext     = "NEEDS_CONTEXT"
	StatusBlocked          = "BLOCKED"
)

// statusLinePattern is contracts/collect-vocabulary.json's `status_line_pattern`,
// byte for byte. DONE_WITH_CONCERNS precedes DONE in the alternation and the
// `\b` guard follows the capture: either defence alone stops `STATUS:
// DONE_WITH_CONCERNS` being read as its opposite, and the fixture pins the
// text so a re-ordering fails a build before it can fail a run.
const statusLinePattern = "^STATUS:[ \\t]*(DONE_WITH_CONCERNS|DONE|NEEDS_CONTEXT|BLOCKED)\\b[ \\t]*(?:[-–—:][ \\t]*)?(.*)$"

// decorationCutset is the fixture's `decoration.trimmed`: the markdown a
// report line may be wrapped in. Workers write prose, and a status line inside
// a bullet or bolded is still a status line.
const decorationCutset = " \t>-*#`"

var statusLine = regexp.MustCompile(statusLinePattern)

// ----------------------------------------------- the review's own verdict ---

// The review-epic job's own judgement of the epic AS INTEGRATED (tick b50):
// a closed vocabulary of two words, deliberately NOT the collect vocabulary's.
// READY and ready-to-merge answer different questions — the review's verdict
// is about the EPIC, the collect verdict is about the ATTEMPT'S branch — and
// one word shared between them is exactly how a review that said NOT READY
// came to be recorded as ready-to-merge (epic-ncv decision 1). The line rides
// in the report the review already writes, the way the findings block does,
// and is asked for by the review-epic profile's prompt — the role contract
// JobSpec.output_schema names.
//
// This vocabulary is ticfac's own rather than contracts/collect-vocabulary's,
// on purpose. The bundle's words are shared by three implementations of one
// reader and pinned there so they cannot drift; the review's verdict has one
// reader (this parser) and one consumer (the reconciler that validates it),
// and the bundle keeps `result` open so the role contract can own its shape.
const (
	ReviewVerdictReady    = "READY"
	ReviewVerdictNotReady = "NOT READY"
)

// ReviewVerdicts is the closed review-verdict vocabulary, NOT READY first for
// the same order-discipline the status alternation keeps: NOT READY must
// precede READY so the alternation cannot read a longer word as a shorter
// one, whatever else weakens.
var ReviewVerdicts = []string{ReviewVerdictNotReady, ReviewVerdictReady}

// reviewVerdictLinePattern mirrors the status line's two defences: NOT READY
// precedes READY in the alternation, and the \b guard stops a suffixed word
// (READINESS) parsing as a bare one. It is pinned here as source, the way the
// bundle pins its own pattern: a re-ordered alternation is a verdict
// inversion waiting for a weakened guard to let it through.
const reviewVerdictLinePattern = "^REVIEW-VERDICT:[ \\t]*(NOT READY|READY)\\b[ \\t]*(?:[-\u2013\u2014:][ \\t]*)?(.*)$"

var reviewVerdictLine = regexp.MustCompile(reviewVerdictLinePattern)

// Report is a parsed RESULT-<tick>.md: the FINAL status line, and the typed
// findings block if the report carries one (findings.go).
type Report struct {
	Path   string
	Status string
	Detail string
	Line   string

	// Findings is the typed findings list the report's ```findings block
	// carried, and FindingsProblem is why it could not be read when it could
	// not. A block that does not parse is NOT dropped silently: dropping
	// findings is the exact failure the channel exists to remove, so the
	// problem is carried to the reconciler, which refuses the close behind it.
	Findings        []Finding
	FindingsProblem string

	// FindingsFolded names every finding key the block carried that the
	// finding record does not know (tick ryv): such a key is folded into the
	// finding's BODY as a labelled line rather than refused, and what was
	// folded rides here so the attempt's records can note it — the 3h0
	// worker's finished tick was once rejected over an extra "title_note",
	// and the fold is the repair: kept, visibly, never thrown away.
	FindingsFolded []string

	// ReviewVerdict is the review's own judgement parsed off its typed
	// REVIEW-VERDICT line — READY or NOT READY, the closed vocabulary above.
	// Empty when the report carries none, whatever its prose says: prose is
	// where NOT READY went to be recorded as its opposite, so the absence is
	// stated as an absence and the reconciler refuses the answer rather than
	// guessing a verdict a report never gave. ReviewVerdictDetail is what the
	// review said would make the epic ready, and ReviewVerdictLine is the
	// line as written, kept for the same reason Line is.
	ReviewVerdict       string
	ReviewVerdictDetail string
	ReviewVerdictLine   string
}

// NeedsHuman is the escalation set: two statuses that reach a person
// regardless of the verdict.
func (r Report) NeedsHuman() bool {
	return r.Status == StatusBlocked || r.Status == StatusNeedsContext
}

// ParseReport reads the FINAL status line of a report body, the FINAL
// review-verdict line, and the FINAL findings block. Everything is empty when
// the report carries no recognisable status — which is the `missing-result`
// verdict, so a body that stops matching here is a verdict change too.
func ParseReport(body string) Report {
	var out Report
	findings, problem, folded := ParseFindings(body)
	out.Findings, out.FindingsProblem, out.FindingsFolded = findings, problem, folded
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.Trim(strings.TrimRight(raw, "\r"), decorationCutset)
		if m := statusLine.FindStringSubmatch(trimmed); m != nil {
			// Keep scanning: the contract is the *final* status line, because a
			// report may quote the template's four options above its own answer.
			out.Status = m[1]
			out.Detail = strings.TrimSpace(strings.Trim(m[2], decorationCutset))
			out.Line = trimmed
		}
		if m := reviewVerdictLine.FindStringSubmatch(trimmed); m != nil {
			// The same final-line contract, and for the same reason: a review
			// may weigh both sides in prose before it says which it is.
			out.ReviewVerdict = m[1]
			out.ReviewVerdictDetail = strings.TrimSpace(strings.Trim(m[2], decorationCutset))
			out.ReviewVerdictLine = trimmed
		}
	}
	return out
}

// ------------------------------------------------------------- the boundary ---

// protectedPrefixes is A10's boundary: the two authorities a job may not
// rewrite. It is contracts/lifecycle-invariants.json's
// `harness.protected_prefixes`, and report_test.go asserts this copy still
// equals the fixture's — the fixture defines the boundary, this is the reader.
var protectedPrefixes = []string{".tick/", ".ticfac/"}

// exemptFromBoundary are the tracker files a worker is EXPECTED to read and
// may legitimately amend: the run configuration, the runner table and the
// learnings a retro compacts. The records — .tick/issues and .tick/activity —
// are the tracker's authority and are never a worker's to write.
var exemptFromBoundary = []string{
	".tick/config.md",
	".tick/runners.toml",
	".tick/learnings.md",
}

// ExemptFromBoundary is the list OutsideBoundary lets through, exported so a
// prompt can state the boundary it will actually be judged against.
//
// The two were allowed to drift, and it cost a whole close-out. The prompt said
// "do not write under .tick/" flatly while this list exempted .tick/learnings.md,
// and the closeout role — whose own instructions tell it to compact what was
// learned into that very file — believed the prohibition and reported it could
// not do its job. Five attempts, none able to commit anything. A worker obeys
// what it is TOLD the boundary is, so the telling has to come from here.
func ExemptFromBoundary() []string {
	return append([]string{}, exemptFromBoundary...)
}

// BoundaryViolations returns, in order, the paths in a diff that a job was not
// allowed to write. A10's reporting half matters as much as its refusal half:
// a boundary that silently refuses tells nobody the model tried.
func BoundaryViolations(paths []string) []string {
	var out []string
	for _, path := range paths {
		if OutsideBoundary(path) {
			out = append(out, path)
		}
	}
	return out
}

// OutsideBoundary says whether one path is a write the substrate refuses.
func OutsideBoundary(path string) bool {
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	if path == "" {
		return false
	}
	for _, exempt := range exemptFromBoundary {
		if path == exempt {
			return false
		}
	}
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// learningsPath is the exempt file a retro or a learning belongs in: the one
// destination the closeout role's own instructions point its output at. It is
// looked up from the exemption list rather than hard-coded a second time, so
// a refusal can never offer a destination the boundary would also refuse —
// the drift this tick exists to close, moved from the prompt into the refusal
// if it were ever hand-written here. Empty when the boundary exempts no
// learnings file, in which case the refusal simply omits the clause.
func learningsPath() string {
	for _, path := range exemptFromBoundary {
		if strings.HasSuffix(path, "/learnings.md") {
			return path
		}
	}
	return ""
}

// BoundaryRefusal is the sentence a tracker-record boundary violation is
// refused in, shared by both collect implementations so they cannot disagree
// about the same tick. It names the ROLE the attempt ran under and the
// destinations the boundary permits, because a refusal that says only "no"
// leaves a worker inventing a workaround — which is what the flat prohibition
// cost the closeout role: five attempts, each refused without being told that
// its learnings had a permitted destination all along (tick 54n).
//
// The destinations are rendered from ExemptFromBoundary(), the same single
// source the prompt renders its permission line from, and the report's
// destination is the artifact prefix the executor owns — so the refusal says
// where the output should have gone in the same words the prompt does.
func BoundaryRefusal(role, artifactPrefix string, violations []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the %s attempt committed records under an authority that is not its own: %v. "+
		"The boundary permits any role to amend %s",
		role, violations, strings.Join(ExemptFromBoundary(), ", "))
	if learnings := learningsPath(); learnings != "" {
		fmt.Fprintf(&b, "; a retro or a learning belongs in %s", learnings)
	}
	fmt.Fprintf(&b, ", and the report belongs under %s", artifactPrefix)
	return b.String()
}

// ArtifactPrefixViolations is the backstop behind Start's git exclude: it
// returns, in order, the paths in a diff that fall under a JOB'S OWN
// artifact_prefix. The executor owns that path (JobSpec.artifact_prefix) and
// reads the report from the worktree at collect — it must never ride into
// the repository, and Start already keeps a compliant runner's `git add -A`
// from staging it. Finding one here means something bypassed that exclude
// (a runner that force-added it, most likely), and it is reported the same
// way a tracker-record write is: as a boundary this executor did not ask for
// and does not merge.
func ArtifactPrefixViolations(paths []string, prefix string) []string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return nil
	}
	var out []string
	for _, path := range paths {
		clean := strings.TrimPrefix(strings.TrimSpace(path), "./")
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			out = append(out, clean)
		}
	}
	return out
}
