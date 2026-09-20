package subprocess

import "fmt"

// The role-result payload and summary, shared by both executors (tick b50).
//
// The role-result envelope's `result` map is open on purpose — its shape is
// the role contract named by JobSpec.output_schema, and the reconciler
// validates it separately — and a payload minted in two places is a payload
// that can say different things about the same answer on the two hosts a run
// actually uses. Both collect paths build it through this one file, so the
// review's answer cannot drift between them.
//
// What this file fixes is the epic-ncv decision 1 defect: a review that
// answered NOT READY was recorded with `result.verdict: "ready-to-merge"` —
// the collect vocabulary's word about the branch — beside a summary saying
// the opposite. A reader of the decision record could take the word as an
// approval that was never given, because it was the only verdict-shaped word
// in the record. The review-epic payload therefore carries the review's OWN
// judgement as `review_verdict` and does not carry the collect verdict at
// all: for a review the collect verdict is a constant (an empty branch is
// what a correct attempt looks like), the branch facts are already in the
// envelope's Source, and a verdict-shaped word that read as approval was the
// whole defect.

// RoleResultPayload is the open `result` map of the role-result envelope,
// minted identically by every executor that collects a role job.
//
// For every role the payload states the collect facts — commits, branch,
// report path, boundary violations, needs-human, the findings problem — and
// for every role EXCEPT the review it states the collect verdict under
// `verdict`: for an implement or closeout job the merge verdict about the
// branch is a fact the run acts on. For the review-epic role the payload
// states `review_verdict` instead — the review's own closed vocabulary, and
// the one field its contract exists to carry. It is present even when empty:
// a review whose report never stated its judgement is one the reconciler
// refuses, and the absence is a stated fact, never a missing key.
func RoleResultPayload(role string, report Report, verdict string, commits int, branch string, violations []string) map[string]any {
	payload := map[string]any{
		"commits":             commits,
		"branch":              branch,
		"report_path":         report.Path,
		"boundary_violations": stringsOrEmpty(violations),
		"needs_human":         report.NeedsHuman(),
		// The findings channel's one open-payload problem (tick 7vn): a block
		// that would not parse is stated as a problem, never as an empty
		// list, whichever executor collected it.
		"findings_problem": report.FindingsProblem,
	}
	if role == "review-epic" {
		payload["review_verdict"] = report.ReviewVerdict
		return payload
	}
	payload["verdict"] = verdict
	return payload
}

// RoleSummary is the envelope's Summary for a role job: the role's own answer
// as its report stated it. The report's final status line DETAIL is the
// worker's own words when it gave any; without one, the summary is the status
// with a parenthetical — and WHICH verdict the parenthetical carries is the
// one difference the roles make, because a summary is read as the answer.
//
// For every role except the review the parenthetical is the collect verdict,
// the way it always was. For the review it is the review's OWN verdict: the
// review's summary is read as the review's judgement of the epic, and
// spelling the collect verdict there put the word "ready-to-merge" in a
// record whose summary said NOT READY — epic-ncv decision 1, the exact
// sentence this function exists to never write again.
func RoleSummary(role string, report Report, verdict string) string {
	if report.Detail != "" {
		return report.Detail
	}
	if role == "review-epic" && report.ReviewVerdict != "" {
		return fmt.Sprintf("%s (%s)", report.Status, report.ReviewVerdict)
	}
	return fmt.Sprintf("%s (%s)", report.Status, verdict)
}
