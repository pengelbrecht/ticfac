package cli

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/jev"
)

// The wiring run-epic adds (tick x0k): the classifier it hands the reconciler
// is built from the credential source the process found, and what the run SAYS
// about classification is that source's own note — so a run that classifies
// nothing says why, names the credential that would change it, and names
// [tier_policy.start] as the fallback every dispatch takes. This is the pure
// half of the wiring; the end-to-end runs through the real client are in
// internal/reconcile's classify_credential_test.go, and the note's wording is
// pinned in internal/jev's credential tests.
//
// short: pure construction over a resolved source, no repository is built
func TestRunEpicBuildsItsClassifierFromTheCredentialSource(t *testing.T) {
	t.Setenv(jev.GatewayBaseEnv, "")
	t.Setenv(jev.GatewayTokenEnv, "")
	t.Setenv(jev.OperatorKeyEnv, "")
	t.Setenv(jev.OperatorBaseEnv, "")

	// No credential: no classifier — the run classifies nothing — and the
	// note carries the whole degradation, not just "none".
	classifier, note := classifierForRun()
	if classifier != nil {
		t.Fatalf("an empty environment built a classifier: %v", classifier)
	}
	for _, want := range []string{
		"no classifier credential", jev.OperatorKeyEnv, "[tier_policy.start]",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the degradation note does not say %q: %q", want, note)
		}
	}

	// The operator's key, locally: a real classifier on it.
	t.Setenv(jev.OperatorKeyEnv, "operator-key")
	classifier, note = classifierForRun()
	if classifier == nil {
		t.Fatalf("the operator's key built no classifier: %s", note)
	}
	if !strings.Contains(note, "the operator's own credential") {
		t.Errorf("the note does not name the operator's key as the source: %q", note)
	}

	// The cloud source outranks the operator key, because those names mean
	// the process lives inside a run's model path.
	t.Setenv(jev.GatewayBaseEnv, "https://factory.example.com/api/gateway")
	t.Setenv(jev.GatewayTokenEnv, "tkr_run-scoped")
	classifier, note = classifierForRun()
	if classifier == nil {
		t.Fatalf("the sandbox's gateway route built no classifier: %s", note)
	}
	if !strings.Contains(note, "gateway route") || !strings.Contains(note, "run's gateway token") {
		t.Errorf("the note does not say the classifier rides the run's gateway route: %q", note)
	}
}
