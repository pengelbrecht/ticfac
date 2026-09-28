package jev

import "strings"

// THE CREDENTIAL SOURCES (tick x0k, epic wne; rewired by tick tum): what
// decides whether a run classifies anything at all. Jev runs on CLOUDFLARE
// WORKERS AI (model typesafe/jev), billed to the operator's own Cloudflare
// account — there is no TypeSafe key anywhere in this design. The operator
// already holds everything the call needs: the Cloudflare API token and the
// account the factory setup ladder stored in ~/.ticfacrc.
//
// Resolution is BY PRECEDENCE, not by flag, because the sources belong to the
// two environments a run lives in and only one can be present at a time:
//
//   - AI_GATEWAY_BASE_URL + AI_GATEWAY_TOKEN in the environment IS the
//     sandbox contract — those names mean "this process lives inside a run's
//     model path" — and inside that path the classifier rides the factory's
//     gateway route like every other model call: the run token is presented at
//     <AI_GATEWAY_BASE_URL>/jev/ai/run, the factory Worker runs typesafe/jev on
//     Workers AI with the deployment's own Cloudflare credential, and a revoked
//     token stops classification with the rest of the run's traffic. The
//     sandbox never holds a Cloudflare token.
//   - Locally, the operator's Cloudflare API token and account id: the
//     factory_cloudflare_api_token key of ~/.ticfacrc and the account the
//     factory_gateway_url key carries (https://gateway.ai.cloudflare.com/v1/
//     <account>/<gateway>) — the caller reads the file and hands both in as a
//     [Stored]. $TICFAC_JEV_API_TOKEN and $TICFAC_JEV_ACCOUNT_ID override them
//     one by one, and $TICFAC_JEV_API_BASE moves the Cloudflare REST root
//     (empty means [DefaultAPIBase], which [New] applies) — all three exist
//     for tests and one-off overrides, not as a second setup path.
//
// NO CREDENTIAL IS THE DOCUMENTED FALLBACK, and the note says so rather than
// leaving the degradation to be inferred from a dispatch's tier: a run that
// classifies nothing routes every dispatch at [tier_policy.start] exactly as
// it did before the classifier existed, and the operator is told which
// credential would have changed that. A half-set gateway — a base with no
// token or the reverse — and a half-set local credential — a token with no
// account or the reverse — are treated the same way and named, because
// guessing a source the operator did not choose is a credential decision this
// package was explicitly not given.
//
// No note ever prints the account id or the token: a run's startup note lands
// in run logs, and this repository is public.

// GatewayRouteSlug is the path segment the factory's gateway route serves the
// classifier under: <AI_GATEWAY_BASE_URL>/jev/ai/run. cloudflare/src/gateway.ts
// serves the same slug and path, and the parity test in credential_test.go
// fails the build if either side drifts.
const GatewayRouteSlug = "jev"

// The environment names, declared once so the sandbox contract, the operator's
// documentation and the tests quote the same spellings.
const (
	// GatewayBaseEnv and GatewayTokenEnv are the sandbox's model path, exactly
	// as image/common.sh exports it to every container a run boots — the
	// orchestrator's included, which is the process that classifies.
	GatewayBaseEnv  = "AI_GATEWAY_BASE_URL"
	GatewayTokenEnv = "AI_GATEWAY_TOKEN"
	// OperatorTokenEnv overrides the stored Cloudflare API token,
	// OperatorAccountEnv the stored account id, and OperatorBaseEnv the
	// Cloudflare REST root the call is made against.
	OperatorTokenEnv   = "TICFAC_JEV_API_TOKEN"
	OperatorAccountEnv = "TICFAC_JEV_ACCOUNT_ID"
	OperatorBaseEnv    = "TICFAC_JEV_API_BASE"
)

// The ~/.ticfacrc keys the local credential is read from, spelled here for the
// notes; internal/factory/credentials owns the constants and the contract.
const (
	storedTokenKey   = "factory_cloudflare_api_token"
	storedAccountKey = "factory_gateway_url"
)

// Stored is the local credential as ~/.ticfacrc holds it: the Cloudflare API
// token (factory_cloudflare_api_token) and the account id parsed from the AI
// Gateway URL (factory_gateway_url). The caller reads the file; this package
// reads no files, so the resolution stays one pure function.
type Stored struct {
	APIToken  string
	AccountID string
}

// CredentialSource is the credential one process could reach, resolved: the
// Config to build a client with when [CredentialSource.Configured] is true,
// and the Note that says which source was chosen — or why none was, and what
// the run does about it. The note is what run-epic prints at startup, where
// the operator can still cancel cheaply.
type CredentialSource struct {
	Config     Config
	Configured bool
	Note       string
}

// ResolveCredential resolves the classifier's credential: the gateway route a
// sandbox holds, else the operator's Cloudflare credential (the environment's
// overrides over what ~/.ticfacrc stores), else nothing. `lookup` is passed
// in rather than reached for so the resolution is the same pure function under
// test as in production.
func ResolveCredential(lookup func(string) string, stored Stored) CredentialSource {
	value := func(name string) string {
		if lookup == nil {
			return ""
		}
		return strings.TrimSpace(lookup(name))
	}

	// The gateway route first: inside a sandbox it is the only source that
	// exists, and on any other host those names are not set at all.
	base, token := value(GatewayBaseEnv), value(GatewayTokenEnv)
	switch {
	case base != "" && token != "":
		return CredentialSource{
			Config: Config{
				APIBase: strings.TrimRight(base, "/") + "/" + GatewayRouteSlug,
				APIKey:  token,
			},
			Configured: true,
			Note: "classifier: Jev on Workers AI, through this run's gateway route (" + GatewayBaseEnv + "), " +
				"presenting the run's gateway token",
		}
	case base != "" || token != "":
		// Half-set is a broken sandbox boot: neither dialling (against which
		// base, with which token?) nor stopping is right; the documented
		// fallback is, with both variables named.
		return CredentialSource{
			Note: "the run's gateway is half-configured (" + GatewayBaseEnv + " without " + GatewayTokenEnv +
				", or the reverse), so no classifier is configured and every dispatch starts at [tier_policy.start]",
		}
	}

	// The operator's own Cloudflare credential, locally.
	apiToken, tokenFrom := value(OperatorTokenEnv), "$"+OperatorTokenEnv
	if apiToken == "" {
		apiToken, tokenFrom = strings.TrimSpace(stored.APIToken), storedTokenKey+" in ~/.ticfacrc"
	}
	account, accountFrom := value(OperatorAccountEnv), "$"+OperatorAccountEnv
	if account == "" {
		account, accountFrom = strings.TrimSpace(stored.AccountID), "the account in "+storedAccountKey
	}
	switch {
	case apiToken != "" && account != "":
		source := CredentialSource{
			Config: Config{
				APIBase:   strings.Trim(value(OperatorBaseEnv), " /"),
				AccountID: account,
				APIKey:    apiToken,
			},
			Configured: true,
			Note: "classifier: Jev on Workers AI (" + Model + "), on the operator's Cloudflare credential (" +
				tokenFrom + ", " + accountFrom + ")",
		}
		if source.Config.APIBase != "" {
			source.Note += ", at $" + OperatorBaseEnv
		}
		return source
	case apiToken != "":
		return CredentialSource{
			Note: "no classifier credential: the Cloudflare API token is set (" + tokenFrom + ") but no account " +
				"is (" + storedAccountKey + " in ~/.ticfacrc names none, and $" + OperatorAccountEnv + " is unset), " +
				"so no tick is classified and every dispatch starts at [tier_policy.start]",
		}
	case account != "":
		return CredentialSource{
			Note: "no classifier credential: the Cloudflare account is known (" + accountFrom + ") but no API " +
				"token is (" + storedTokenKey + " in ~/.ticfacrc, or $" + OperatorTokenEnv + "), so no tick is " +
				"classified and every dispatch starts at [tier_policy.start]",
		}
	}

	// Neither source: the documented degradation, said out loud.
	return CredentialSource{
		Note: "no classifier credential: this process has no run gateway (" + GatewayBaseEnv + "/" + GatewayTokenEnv +
			") and no Cloudflare credential for Workers AI (" + storedTokenKey + " and " + storedAccountKey +
			" in ~/.ticfacrc — 'ticfac factory setup' stores both — or $" + OperatorTokenEnv + " and $" +
			OperatorAccountEnv + "), so no tick is classified and every dispatch starts at [tier_policy.start]",
	}
}
