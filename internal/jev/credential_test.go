package jev

import (
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// THE CREDENTIAL SOURCES (tick x0k, rewired to Workers AI by tick tum),
// resolved the way run-epic resolves them. Two sources, one per environment a
// run lives in, chosen BY PRECEDENCE rather than by flag:
//
//   - the GATEWAY ROUTE, in the cloud: a sandbox is booted with
//     AI_GATEWAY_BASE_URL pointing at the factory's own /api/gateway prefix
//     and AI_GATEWAY_TOKEN holding the run-scoped token, and the classifier
//     rides that route like every other model call — the factory Worker runs
//     typesafe/jev on Workers AI with the deployment's own Cloudflare
//     credential, and a revoked token stops classification with the rest of
//     the run's traffic.
//   - the OPERATOR'S CLOUDFLARE CREDENTIAL, locally: the API token and the
//     account ~/.ticfacrc already holds (handed in as a [Stored]), each
//     overridable from the environment.
//
// A source that does not resolve is the documented degradation — the run says
// so and every dispatch starts at [tier_policy.start], never a stop.
//
// short: pure function over a lookup, no dialling, no repository

func lookupOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// Placeholders only: this repository is public, so no test carries a real
// account id or token.
var storedCredential = Stored{APIToken: "cf-token-placeholder", AccountID: "account-placeholder"}

// A sandboxed run classifies through the gateway route and presents the run
// token, exactly as it presents every other model call — the route names its
// own account, so the client names none.
func TestTheGatewayRouteIsTheCloudCredential(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"AI_GATEWAY_BASE_URL": "https://factory.example.com/api/gateway/ ",
		"AI_GATEWAY_TOKEN":    " tkr_run-scoped ",
	}), Stored{})
	if !source.Configured {
		t.Fatalf("a run holding the gateway route and the run token resolved no classifier: %s", source.Note)
	}
	if want := "https://factory.example.com/api/gateway/" + GatewayRouteSlug; source.Config.APIBase != want {
		t.Errorf("the classifier base is %q, want the gateway route %q", source.Config.APIBase, want)
	}
	if source.Config.APIKey != "tkr_run-scoped" || source.Config.AccountID != "" {
		t.Errorf("the classifier credential is %+v, want the run token and no account", source.Config)
	}
	if !strings.Contains(source.Note, "gateway route") || !strings.Contains(source.Note, "Workers AI") {
		t.Errorf("the note does not say the classifier rides the run's gateway route to Workers AI: %q", source.Note)
	}
}

// The gateway source wins over the operator's stored credential: those names
// mean "this process lives inside a run's model path", and a process inside
// that path never reaches for the operator's Cloudflare token.
func TestTheGatewayRouteOutranksTheOperatorsCredential(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"AI_GATEWAY_BASE_URL":  "https://factory.example.com/api/gateway",
		"AI_GATEWAY_TOKEN":     "tkr_run-scoped",
		"TICFAC_JEV_API_TOKEN": "cf-token-override",
	}), storedCredential)
	if source.Config.APIKey != "tkr_run-scoped" || source.Config.AccountID != "" {
		t.Errorf("a sandboxed run was handed the operator's credential: %+v", source.Config)
	}
}

// A half-set gateway is a broken sandbox boot, not a classifier.
func TestAHalfSetGatewayIsNoCredentialAndSaysWhichVariables(t *testing.T) {
	t.Parallel()
	for name, values := range map[string]map[string]string{
		"base without token": {"AI_GATEWAY_BASE_URL": "https://factory.example.com/api/gateway"},
		"token without base": {"AI_GATEWAY_TOKEN": "tkr_run-scoped"},
	} {
		source := ResolveCredential(lookupOf(values), storedCredential)
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

// A local run classifies on the Cloudflare credential ~/.ticfacrc already
// holds — no key of its own for the operator to obtain — against the default
// REST root [New] applies. The note names where each half came from and
// never prints either value: run logs are committed to a public repository.
func TestTheStoredCloudflareCredentialIsTheLocalCredential(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{}), storedCredential)
	if !source.Configured {
		t.Fatalf("the stored Cloudflare credential resolved no classifier: %s", source.Note)
	}
	want := Config{AccountID: "account-placeholder", APIKey: "cf-token-placeholder"}
	if source.Config != want {
		t.Errorf("the local classifier config is %+v, want %+v", source.Config, want)
	}
	for _, named := range []string{"factory_cloudflare_api_token", "factory_gateway_url", Model} {
		if !strings.Contains(source.Note, named) {
			t.Errorf("the note does not name %s: %q", named, source.Note)
		}
	}
	for _, secret := range []string{"cf-token-placeholder", "account-placeholder"} {
		if strings.Contains(source.Note, secret) {
			t.Errorf("the note prints a credential value %q: %q", secret, source.Note)
		}
	}
}

// Each half has an environment override — for tests and one-offs — and the
// REST root moves with $TICFAC_JEV_API_BASE.
func TestTheEnvironmentOverridesEachHalfAndTheRoot(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{
		"TICFAC_JEV_API_TOKEN":  " token-override ",
		"TICFAC_JEV_ACCOUNT_ID": "account-override",
		"TICFAC_JEV_API_BASE":   "https://cloudflare.example.com/client/v4/ ",
	}), storedCredential)
	want := Config{APIBase: "https://cloudflare.example.com/client/v4", AccountID: "account-override", APIKey: "token-override"}
	if !source.Configured || source.Config != want {
		t.Fatalf("the overrides resolved to %+v, want %+v", source.Config, want)
	}
	for _, named := range []string{"TICFAC_JEV_API_TOKEN", "TICFAC_JEV_ACCOUNT_ID", "TICFAC_JEV_API_BASE"} {
		if !strings.Contains(source.Note, named) {
			t.Errorf("the note does not name the override %s: %q", named, source.Note)
		}
	}
}

// Half a local credential is no credential, and the note says which half.
func TestAHalfLocalCredentialSaysWhichHalfIsMissing(t *testing.T) {
	t.Parallel()
	tokenOnly := ResolveCredential(lookupOf(nil), Stored{APIToken: "cf-token-placeholder"})
	if tokenOnly.Configured || !strings.Contains(tokenOnly.Note, "no account") ||
		!strings.Contains(tokenOnly.Note, "[tier_policy.start]") {
		t.Errorf("a token with no account resolved %+v: %q", tokenOnly.Config, tokenOnly.Note)
	}
	accountOnly := ResolveCredential(lookupOf(nil), Stored{AccountID: "account-placeholder"})
	if accountOnly.Configured || !strings.Contains(accountOnly.Note, "no API token") ||
		!strings.Contains(accountOnly.Note, "[tier_policy.start]") {
		t.Errorf("an account with no token resolved %+v: %q", accountOnly.Config, accountOnly.Note)
	}
}

// A TypeSafe key is not a credential any more (tick tum): the operator has
// none, and Jev bills through Cloudflare. A lingering $TICFAC_JEV_API_KEY
// resolves nothing.
func TestATypeSafeKeyIsNotACredential(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{"TICFAC_JEV_API_KEY": "ts-key"}), Stored{})
	if source.Configured {
		t.Fatalf("a TypeSafe key resolved a classifier: %+v", source.Config)
	}
}

// No source at all is the DOCUMENTED DEGRADATION, and the note carries it.
func TestNoCredentialDegradesAndSaysSo(t *testing.T) {
	t.Parallel()
	source := ResolveCredential(lookupOf(map[string]string{}), Stored{})
	if source.Configured {
		t.Fatalf("an empty environment resolved a classifier: %+v", source.Config)
	}
	for _, want := range []string{
		"no classifier credential", "factory_cloudflare_api_token", "AI_GATEWAY_BASE_URL", "[tier_policy.start]",
	} {
		if !strings.Contains(source.Note, want) {
			t.Errorf("the degradation note does not name %s: %q", want, source.Note)
		}
	}
}

// The cloud route is a contract written on both sides in two languages, and
// neither imports the other: the Go client posts to <AI_GATEWAY_BASE_URL>/jev
// /ai/run naming typesafe/jev, and the TypeScript gateway must serve that slug
// and path by running that model on Workers AI. A slug, path or model that
// drifts silently is a cloud run whose every classification degrades to a
// 404 or 400 from the route, which reads as an outage of Jev rather than a
// typo — the shape a parity test reading both sides exists to catch.
//
// short: reads the pinned-in-repo TypeScript source, builds nothing
func TestTheGatewayRouteAndTheFactoryAgreeAboutTheSlugThePathAndTheModel(t *testing.T) {
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
		`export const JEV_ROUTE_SLUG = "` + GatewayRouteSlug + `";`,
		`export const JEV_ROUTE_PATH = "` + strings.TrimPrefix(runPath, "/") + `";`,
		`export const JEV_MODEL = "` + Model + `";`,
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("cloudflare/src/gateway.ts no longer contains %q — the classifier route the Go client targets has drifted", want)
		}
	}
	if strings.Contains(string(source), "TYPESAFE_API_KEY") {
		t.Errorf("cloudflare/src/gateway.ts still reads a TYPESAFE_API_KEY: Jev runs on Workers AI with the deployment's Cloudflare credential")
	}
}
