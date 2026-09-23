package runstate

import (
	"os"
	"testing"
)

// The pinned classification decision record, read back through the decoder
// this store reads a run with (tick kl9).
//
// cloudflare/test/fixtures/classify-tick-decision.json is pinned byte for
// byte from the live exchange by internal/reconcile
// (TestTheClassificationRecordIsPinnedAsItIsWritten), and BOTH run-state
// stores test against it: the TypeScript store parses it through
// validateDecision — the read that used to throw, because that side had
// closed the decision record's role vocabulary with $defs.role alone while
// this side's DecisionRoles had gained the classification exchange — and
// THIS test proves the other direction: the pin is a record the Go reader
// accepts, so the fixture cannot drift into something one store refuses
// while the other passes.
//
// It is this package's test rather than the pin writer's because the pin's
// side of the comparison needs a whole run; the READ is cheap, so it stays in
// the short suite where the per-tick gate pays for it.
const classificationPin = "../../cloudflare/test/fixtures/classify-tick-decision.json"

// short: reads and validates the pinned fixture, no repository is built
func TestThePinnedClassificationRecordDecodesAsThisStoreReadsIt(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(classificationPin)
	if err != nil {
		t.Fatalf("the pin is missing — regenerate it with `go test ./internal/reconcile -run TestTheClassificationRecordIsPinnedAsItIsWritten -update-pin`: %v", err)
	}
	var decision Decision
	if err := decodeRecord(raw, &decision); err != nil {
		t.Fatalf("the pinned record does not decode as a decision: %v", err)
	}
	if decision.Role != RoleClassifyTick {
		t.Fatalf("the pinned record is a %q exchange, not the classification's", decision.Role)
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("the pinned record does not validate: %v", err)
	}
	if decision.Provenance.Role != nil {
		t.Fatalf("the pinned record's provenance claims a role-job produced it: %q", *decision.Provenance.Role)
	}
	if decision.Provenance.Model == nil || *decision.Provenance.Model == "" {
		t.Fatal("the pinned record's provenance names no model identity: the model field is how a classification says which model answered")
	}
	if !contains(DecisionRoles(), RoleClassifyTick) {
		t.Error("DecisionRoles no longer carries the classification exchange")
	}
}
