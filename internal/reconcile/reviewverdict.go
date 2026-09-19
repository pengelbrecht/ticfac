package reconcile

import (
	"fmt"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The review's verdict (tick b50): the review-epic role contract's one typed
// judgement field, and the rule that says what a NOT READY answer does.
//
// THE FIELD. ticfac.job-result.review-epic.v1 — the role contract JobSpec
// output_schema names — now requires `review_verdict`, the review's own
// closed vocabulary (READY, NOT READY, subprocess.ReviewVerdicts): the
// review's judgement of the epic AS INTEGRATED, parsed off its typed
// REVIEW-VERDICT report line, minted by both executors through the one shared
// payload (subprocess/rolepayload.go) and validated HERE, where the envelope
// is validated, before anything is decided on it. Until this tick the
// judgement had nowhere to live: the envelope carries only the collect
// vocabulary's merge verdict, which for a review is a constant about the
// branch — so a review answering NOT READY was recorded (epic-ncv decision 1)
// as `result.verdict: "ready-to-merge"` beside a summary saying the opposite,
// and only the untriaged-findings hold stopped that record from releasing the
// close-out. A word that read as approval nobody gave was the whole defect.
//
// THE RULE, decided and stated: a review answering NOT READY does NOT hold
// the close-out — it is CARRIED TO THE EPIC PR. The three reasons:
//
//   - The merge is a person's, by the repository's own close-out rule: a
//     review's NOT READY is a judgement about ACCEPTING the work, and the PR
//     is where that judgement already lives. The same shape the operator
//     chose for findings (tick aqm): carried to the one page a person
//     already reads, one decision point at the end.
//   - A hold has no release path. A decision is create-if-absent, so a
//     resumed run replays the recorded NOT READY forever; findings have a
//     triage verb, a verdict has none — holding the close-out on it would
//     strand every honest review behind a refusal only a code change could
//     lift. Carrying puts the decision where the verb already exists: merge
//     the PR, or do not.
//   - The carry has teeth: the close-out composes the typed verdict into the
//     PR body (closeoutPRBody) at the admission AND at the close gate, and
//     a body the forge cannot carry is a typed refusal (tick 4sb) — the run
//     cannot hand over behind a PR that hides its own review's judgement.
//
// What still holds the run is unchanged: an answer that is BLOCKED or
// NEEDS_CONTEXT (needsHuman) holds the review tick for a person, untriaged
// findings hold at the close-out, and CI holds the close-out. A NOT READY
// that is stated as a verdict is an answer, not an escalation.

// validateReviewVerdict holds a review's validated envelope to its role
// contract's one required field: the review's own judgement, in its own
// vocabulary. A review whose report never stated a REVIEW-VERDICT line, or
// stated one outside the closed vocabulary, is refused — the tick is not
// closed behind a judgement nobody can read, and prose is not read for one,
// because prose is where NOT READY went to be recorded as its opposite.
func validateReviewVerdict(answer *subprocess.RoleResult) error {
	if answer.Result == nil {
		answer.Result = map[string]any{}
	}
	verdict, _ := answer.Result["review_verdict"].(string)
	if verdict == "" {
		return fmt.Errorf("the answer carries no review_verdict: the report stated no REVIEW-VERDICT line, and the " +
			"review's judgement is the one deliverable that cannot be prose. State it as its own typed line, " +
			"REVIEW-VERDICT: READY or REVIEW-VERDICT: NOT READY — <what would make it ready>")
	}
	for _, known := range subprocess.ReviewVerdicts {
		if verdict == known {
			return nil
		}
	}
	return fmt.Errorf("the answer's review_verdict %q is outside the closed vocabulary %v: a verdict outside it is a " +
		"word two runs can disagree about, which is how a status line came to be the only thing a verdict could ride on",
		verdict, subprocess.ReviewVerdicts)
}

// reviewVerdictOf reads the typed verdict off a RECORDED decision's response:
// the envelope as it landed in the run state, payload and all. Empty when the
// decision predates the field or is not a review's — a caller that composes
// the record for a person states that absence rather than inventing a verdict.
func reviewVerdictOf(response map[string]any) string {
	result, _ := response["result"].(map[string]any)
	if result == nil {
		return ""
	}
	verdict, _ := result["review_verdict"].(string)
	return verdict
}

// reviewVerdictParagraph is the epic PR body's statement of the final
// review's verdict: the typed verdict first — a person merging reads a
// verdict, not prose — then the review's own summary, then, for a NOT READY,
// the stated rule that says what the verdict does (carried to this PR, never
// spelled as approval, the close-out not held on it).
//
// A decision recorded before the typed field existed states what it has —
// the review's status and summary, the way 4sb composed it — rather than
// silence, because a PR that says nothing about the review reads as "no
// review found anything", which is a verdict nobody gave.
func reviewVerdictParagraph(decision runstate.Decision) string {
	status, _ := decision.Response["status"].(string)
	summary, _ := decision.Response["summary"].(string)
	verdict := reviewVerdictOf(decision.Response)
	if verdict == "" {
		return fmt.Sprintf("The final review (decision %d) answered %s: %s\n",
			decision.Decision, status, summary)
	}
	if verdict == subprocess.ReviewVerdictNotReady {
		return fmt.Sprintf("The final review (decision %d) judged the epic NOT READY: %s\n"+
			"The close-out is not held on this verdict; it is carried here, because the merge is a person's "+
			"judgement and this PR is where that judgement is made. The durable record states the same verdict.\n",
			decision.Decision, summary)
	}
	return fmt.Sprintf("The final review (decision %d) judged the epic READY: %s\n", decision.Decision, summary)
}
