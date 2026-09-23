package jev

import (
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// THE CREDENTIAL SOURCES (tick x0k, epic wne), resolved the way run-epic
// resolves them: the classifier is wired, and what decides whether a run
// classifies anything is which credential the process can reach. Two sources,
// one per environment a run lives in, chosen BY PRECEDENCE rather than by
// flag:
//
//   - the GATEWAY ROUTE, in the cloud: a sandbox is booted with
//     AI_GATEWAY_BASE_URL pointing at the factory's own /api/gateway prefix
//     and AI_GATEWAY_TOKEN holding the run-scoped token, and the classifier
//     rides that route like every other model call — the factory Worker
//     exchanges the run token for the deployment's classifier key, and a
//     revoked token stops classification with the rest of the run's traffic.
//   - the OPERATOR'S OWN KEY, locally: $TICFAC_JEV_API_KEY, an optional
//     $TICFAC_JEV_API_BASE beside it for a non-default API root.
//
// NEITHER is a credential grade the repository pins: a source that resolves is
// a source the operator chose to configure, and one that does not is the
// documented degradation — the run says so and every dispatch starts at
// [tier_policy.start], never a stop, never a silent classification gap.
//
// short: pure function over a lookup, no dialling, no repository

func lookupOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// A sandboxed run — the process the factory booted with the gateway route and
// the run token in its environment — classifies through the gateway route and
// presents the run token, exactly as it presents every other model call.
func TestTheGatewayRouteIsTheCloudCredential(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"AI_GATEWAY_BASE_URL": "https://factory.example.com/api/gateway/ ",
		"AI_GATEWAY_TOKEN":    " tkr_run-scoped ",
	}))
	if !source.Configured {
		t.Fatalf("a run holding the gateway route and the run token resolved no classifier: %s", source.Note)
	}
	if want := "https://factory.example.com/api/gateway/" + GatewayRouteSlug; source.Config.APIBase != want {
		t.Errorf("the classifier base is %q, want the gateway route %q", source.Config.APIBase, want)
	}
	if source.Config.APIKey != "tkr_run-scoped" {
		t.Errorf("the classifier credential is %q, want the run token the sandbox holds", source.Config.APIKey)
	}
	if !strings.Contains(source.Note, "gateway route") || !strings.Contains(source.Note, "run") {
		t.Errorf("the note does not say the classifier rides the run's gateway route: %q", source.Note)
	}
}

// The gateway source wins over the operator key when both are present, on
// purpose: AI_GATEWAY_BASE_URL/AI_GATEWAY_TOKEN in the environment is the
// sandbox contract — those names mean "this process lives inside a run's model
// path" — and a process inside that path reaches for the operator's key never.
func TestTheGatewayRouteOutranksTheOperatorsKey(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"AI_GATEWAY_BASE_URL": "https://factory.example.com/api/gateway",
		"AI_GATEWAY_TOKEN":    "tkr_run-scoped",
		"TICFAC_JEV_API_KEY":  "operator-key",
	}))
	if source.Config.APIKey != "tkr_run-scoped" {
		t.Errorf("a sandboxed run was handed the operator's key: %q", source.Config.APIKey)
	}
}

// A half-set gateway — a base with no token, a token with no base — is a broken
// sandbox boot, not a classifier: the run degrades to the start policy and
// SAYS which two variables disagree, because the alternative (guessing a
// source) is a run that classifies against something the operator never chose.
func TestAHalfSetGatewayIsNoCredentialAndSaysWhichVariables(t *testing.T) {
	t.Parallel()
	for name, values := range map[string]map[string]string{
		"base without token": {"AI_GATEWAY_BASE_URL": "https://factory.example.com/api/gateway"},
		"token without base": {"AI_GATEWAY_TOKEN": "tkr_run-scoped"},
	} {
		source := ResolveCredential(lookupOf(values))
		if source.Configured {
			t.Errorf("%s resolved a classifier: %+v", name, source.Config)
		}
		for _, want := range []string{"AI_GATEWAY_BASE_URL", "AI_GATEWAY_TOKEN", "[tier_policy.start]"} {
			if !strings.Contains(source.Note, want) {
				t.Errorf("%s: the note does not name %s: %q", name, want, source.Note)
			}
		}
	}
}

// A local run — no gateway route in the environment — classifies on the
// operator's own key, with no base override and therefore the default API root
// that [New] applies.
func TestTheOperatorsKeyIsTheLocalCredential(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"TICFAC_JEV_API_KEY": " operator-key ",
	}))
	if !source.Configured {
		t.Fatalf("an operator key resolved no classifier: %s", source.Note)
	}
	if source.Config.APIKey != "operator-key" {
		t.Errorf("the classifier credential is %q, want the operator's key", source.Config.APIKey)
	}
	if source.Config.APIBase != "" {
		t.Errorf("an unset base override resolved to %q, want the default New applies", source.Config.APIBase)
	}
	if !strings.Contains(source.Note, "TICFAC_JEV_API_KEY") {
		t.Errorf("the note does not name where the operator's key came from: %q", source.Note)
	}
}

// The optional base override, so an operator (and a test) can point the
// classifier at a different API root without changing the credential.
func TestAnOperatorBaseOverrideMovesTheAPIRoot(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"TICFAC_JEV_API_KEY":  "operator-key",
		"TICFAC_JEV_API_BASE": "https://classifier.example.com/ ",
	}))
	if !source.Configured || source.Config.APIBase != "https://classifier.example.com" {
		t.Errorf("the base override resolved to %+v", source.Config)
	}
	if !strings.Contains(source.Note, "TICFAC_JEV_API_BASE") {
		t.Errorf("the note does not say the API root was overridden: %q", source.Note)
	}
}

// No source at all is the DOCUMENTED DEGRADATION, and the note has to carry
// it: the run classifies nothing, every dispatch starts at [tier_policy.start],
// and the operator is told both which credential would have configured a
// classifier and what the run does without one.
func TestNoCredentialDegradesAndSaysSo(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{}))
	if source.Configured {
		t.Fatalf("an empty environment resolved a classifier: %+v", source.Config)
	}
	for _, want := range []string{
		"TICFAC_JEV_API_KEY", "AI_GATEWAY_BASE_URL", "[tier_policy.start]",
	} {
		if !strings.Contains(source.Note, want) {
			t.Errorf("the degradation note does not name %s: %q", want, source.Note)
		}
	}
}

// The cloud route is a contract written on both sides in two languages, and
// neither imports the other: the Go client targets
// <AI_GATEWAY_BASE_URL>/jev and the TypeScript gateway must serve that slug
// with the TYPESAFE_API_KEY secret and route it to the same API root the local
// source defaults to. A slug or root that drifts silently is a cloud run whose
// every classification degrades to "the classifier answered 404 the gateway
// route", which reads as an outage of Jev rather than a typo — the exact shape
// a parity test reading both sides exists to catch.
//
// short: reads the pinned-in-repo TypeScript source, builds nothing
func TestTheGatewayRouteAndTheFactoryAgreeAboutTheSlugAndTheRoot(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(root + "/cloudflare/src/gateway.ts")
	if err != nil {
		t.Fatalf("reading the factory's gateway route: %v", err)
	}
	for _, want := range []string{
		// The slug the Go client targets must be a route the Worker serves.
		`jev: { secret: "TYPESAFE_API_KEY", scheme: "bearer"`,
		// And the route must send vendor-direct to the same API root the
		// local source defaults to, or a cloud run would ask a different Jev.
		`"` + DefaultAPIBase + `"`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("cloudflare/src/gateway.ts no longer contains %q — the classifier route the Go client targets has drifted", want)
		}
	}
}
