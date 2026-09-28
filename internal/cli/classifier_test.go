package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/jev"
)

// The wiring run-epic adds (tick x0k, rewired to Workers AI by tick tum): the
// classifier it hands the reconciler is built from the credential source the
// process found — the sandbox's gateway route, else the Cloudflare credential
// ~/.ticfacrc already holds — and what the run SAYS about classification is
// that source's own note. This is the pure half of the wiring; the end-to-end
// runs through the real client are in internal/reconcile's
// classify_credential_test.go, and the note's wording is pinned in
// internal/jev's credential tests.

// isolateClassifierCredential points HOME at an empty directory and clears
// every environment source, so the host's own ~/.ticfacrc and shell never
// answer for the test. It returns the ~/.ticfacrc path to write.
func isolateClassifierCredential(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{jev.GatewayBaseEnv, jev.GatewayTokenEnv,
		jev.OperatorTokenEnv, jev.OperatorAccountEnv, jev.OperatorBaseEnv} {
		t.Setenv(name, "")
	}
	return filepath.Join(home, ".ticfacrc")
}

// short: pure construction over a resolved source and a temp ~/.ticfacrc, no repository is built
func TestRunEpicBuildsItsClassifierFromTheCredentialSource(t *testing.T) {
	rc := isolateClassifierCredential(t)

	// No credential: no classifier — the run classifies nothing — and the
	// note carries the whole degradation, not just "none".
	classifier, note := classifierForRun()
	if classifier != nil {
		t.Fatalf("an empty environment built a classifier: %v", classifier)
	}
	for _, want := range []string{
		"no classifier credential", "factory_cloudflare_api_token", "[tier_policy.start]",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the degradation note does not say %q: %q", want, note)
		}
	}

	// The Cloudflare credential ~/.ticfacrc already holds — the API token
	// and the account in the gateway URL — is the local classifier: nothing
	// new for the operator to obtain. Placeholders only; this repo is public.
	if err := os.WriteFile(rc, []byte(
		"factory_gateway_url=https://gateway.ai.cloudflare.com/v1/account-placeholder/gateway-placeholder\n"+
			"factory_cloudflare_api_token=cf-token-placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	classifier, note = classifierForRun()
	if classifier == nil {
		t.Fatalf("the stored Cloudflare credential built no classifier: %s", note)
	}
	if !strings.Contains(note, "Workers AI") || !strings.Contains(note, "factory_cloudflare_api_token") {
		t.Errorf("the note does not name Workers AI and the stored token as the source: %q", note)
	}
	if strings.Contains(note, "account-placeholder") || strings.Contains(note, "cf-token-placeholder") {
		t.Errorf("the note prints a credential value: %q", note)
	}

	// The cloud source outranks the stored credential, because those names
	// mean the process lives inside a run's model path.
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

// Doctor's classifier probe says WHY it is missing when no credential
// resolves — the same note a run prints — without dialling anything.
//
// short: resolves against a temp HOME with no ~/.ticfacrc; no network
func TestDoctorsClassifierProbeNamesTheMissingCredential(t *testing.T) {
	isolateClassifierCredential(t)
	_, err := doctorClassifier(context.Background())
	if err == nil {
		t.Fatal("the classifier probe answered ok with no credential anywhere")
	}
	for _, want := range []string{"no classifier credential", "[tier_policy.start]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the probe's problem does not say %q: %v", want, err)
		}
	}
}

// The LIVE check, opt-in: with TICFAC_LIVE_JEV=1, doctor's real probe asks Jev
// on Workers AI one tiny question (a couple of hundred input tokens) on the
// credential THIS machine resolves — the operator's ~/.ticfacrc — and must get
// an answer. It is the proof that a local run-epic would log a classification
// rather than "no classifier credential".
//
//	TICFAC_LIVE_JEV=1 go test ./internal/cli -run TestLiveJevAnswers -v
//
// short: skips unless TICFAC_LIVE_JEV is set; one tiny request when it is
func TestLiveJevAnswers(t *testing.T) {
	if os.Getenv("TICFAC_LIVE_JEV") == "" {
		t.Skip("set TICFAC_LIVE_JEV=1 to ask the real Jev on Workers AI one tiny question")
	}
	classifier, note := classifierForRun()
	t.Logf("run-epic would say: %s", note)
	detail, err := doctorClassifier(context.Background())
	if err != nil {
		t.Fatalf("Jev did not answer: %v", err)
	}
	t.Logf("doctor would say: %s", detail)

	// And the work-type question itself, exactly as run-epic asks it, for
	// one tiny tick: a classification, not "no classifier credential".
	result, err := classifier.Classify(context.Background(), []jev.Tick{{
		ID: "live1", Title: "Delete hello.txt", Description: "Remove hello.txt from the repository root.",
	}})
	if err != nil || result.Unavailable != "" {
		t.Fatalf("the work-type classification did not answer: %v %s", err, result.Unavailable)
	}
	one, ok := result.Classifications["live1"]
	if !ok {
		t.Fatalf("the tick was not classified: %+v", result)
	}
	t.Logf("classified live1 as %s at confidence %.2f (%s, %d input tokens)",
		one.Choice, one.Confidence, result.Model, result.Usage.InputTokens)
}
