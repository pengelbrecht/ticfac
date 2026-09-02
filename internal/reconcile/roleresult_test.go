package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The role-result envelope is VALIDATED before the reconciler acts on it. An
// unvalidated model response acted on is how a hallucinated verdict closes a
// tick, so this is the check that stands between a role job and a close.

func wellFormed() *subprocess.RoleResult {
	return &subprocess.RoleResult{
		SchemaVersion: subprocess.SchemaVersion,
		SchemaID:      "ticfac.job-result.review-epic.v1",
		Role:          "review-epic",
		Status:        subprocess.StatusDone,
		Summary:       "the epic does what it said it would",
		Result:        map[string]any{"findings": []any{}},
	}
}

func TestAWellFormedEnvelopeValidates(t *testing.T) {
	if err := ValidateRoleResult(wellFormed(), "ticfac.job-result.review-epic.v1", "review-epic"); err != nil {
		t.Fatalf("a well-formed envelope was refused: %v", err)
	}
}

// The refusal contracts/job-protocol.json's own negative example pins, word for
// word: a fifth spelling of "done" makes two runs disagree about what happened
// to one tick with nothing failing.
func TestAStatusOfItsOwnInventionIsRefused(t *testing.T) {
	result := wellFormed()
	result.Status = "COMPLETE"
	err := ValidateRoleResult(result, "ticfac.job-result.review-epic.v1", "review-epic")
	if err == nil {
		t.Fatal("a status outside the closed vocabulary was accepted")
	}
	if !strings.Contains(err.Error(), "$.status: COMPLETE is not one of the permitted values") {
		t.Errorf("the refusal is %q, not the one the contract's negative example pins", err)
	}
}

func TestAnEnvelopeMissingARequiredFieldIsRefused(t *testing.T) {
	result := wellFormed()
	result.Summary = ""
	result.Result = nil
	err := ValidateRoleResult(result, "ticfac.job-result.review-epic.v1", "review-epic")
	if err == nil {
		t.Fatal("an envelope with no result was accepted")
	}
	if !strings.Contains(err.Error(), "result") {
		t.Errorf("the refusal does not name the missing property: %v", err)
	}
}

// The envelope must answer the question that was ASKED: the schema_id is the
// contract the JobSpec named, and the role is the role that was dispatched.
// Validating against what came back rather than against what was asked for is
// how a review's answer gets read as a closeout's.
func TestAnEnvelopeThatAnswersADifferentQuestionIsRefused(t *testing.T) {
	other := wellFormed()
	other.SchemaID = "ticfac.job-result.implement-tick.v1"
	if err := ValidateRoleResult(other, "ticfac.job-result.review-epic.v1", "review-epic"); err == nil {
		t.Error("an envelope citing a contract nobody asked for was accepted")
	}

	wrongRole := wellFormed()
	wrongRole.Role = "closeout-epic"
	if err := ValidateRoleResult(wrongRole, "ticfac.job-result.review-epic.v1", "review-epic"); err == nil {
		t.Error("an envelope answering as a different role was accepted")
	}
}

// No envelope at all is not "nothing to check": a role job whose only
// deliverable is its answer, with no answer, has not produced one.
func TestNoEnvelopeIsRefused(t *testing.T) {
	err := ValidateRoleResult(nil, "ticfac.job-result.review-epic.v1", "review-epic")
	if err == nil {
		t.Fatal("a missing envelope was accepted")
	}
	if !strings.Contains(err.Error(), "no role-result") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}
