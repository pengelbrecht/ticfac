package subprocess

import (
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

// Report is a parsed RESULT-<tick>.md.
type Report struct {
	Path   string
	Status string
	Detail string
	Line   string
}

// NeedsHuman is the escalation set: two statuses that reach a person
// regardless of the verdict.
func (r Report) NeedsHuman() bool {
	return r.Status == StatusBlocked || r.Status == StatusNeedsContext
}

// ParseReport reads the FINAL status line of a report body. Everything is
// empty when the report carries no recognisable status — which is the
// `missing-result` verdict, so a body that stops matching here is a verdict
// change too.
func ParseReport(body string) Report {
	var out Report
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.Trim(strings.TrimRight(raw, "\r"), decorationCutset)
		m := statusLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		// Keep scanning: the contract is the *final* status line, because a
		// report may quote the template's four options above its own answer.
		out.Status = m[1]
		out.Detail = strings.TrimSpace(strings.Trim(m[2], decorationCutset))
		out.Line = trimmed
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
