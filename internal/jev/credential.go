package jev

import "strings"

// THE CREDENTIAL SOURCES (tick x0k, epic wne): what decides whether a run
// classifies anything at all. The CALL itself has been wired since tick 0ju
// and the exchange since w9b, but a client needs a credential, and where the
// credential comes from is not this package's to invent — the operator chose,
// and the choice is recorded in the tick: the gateway route with the run token
// in the cloud, the operator's own key locally.
//
// Resolution is BY PRECEDENCE, not by flag, because the two sources belong to
// two environments a run lives in and only one can be present at a time:
//
//   - AI_GATEWAY_BASE_URL + AI_GATEWAY_TOKEN in the environment IS the
//     sandbox contract — those names mean "this process lives inside a run's
//     model path" — and inside that path the classifier rides the factory's
//     gateway route like every other model call: the run token is presented,
//     the factory Worker exchanges it for the deployment's TYPESAFE_API_KEY
//     secret, and a revoked token stops classification with the rest of the
//     run's traffic. The route's slug is [GatewayRouteSlug]; the factory serves
//     it vendor-direct to [DefaultAPIBase], because the operator's AI Gateway
//     has no TypeSafe provider to proxy — the exchange, the kill switch and
//     the refusal story stay the Worker's either way.
//   - $TICFAC_JEV_API_KEY is the operator's own key, on the machine the run
//     is started from. $TICFAC_JEV_API_BASE beside it overrides the API root
//     (empty means [DefaultAPIBase], which [New] applies).
//
// NO CREDENTIAL IS THE DOCUMENTED FALLBACK, and the note says so rather than
// leaving the degradation to be inferred from a dispatch's tier: a run that
// classifies nothing routes every dispatch at [tier_policy.start] exactly as
// it did before the classifier existed, and the operator is told which
// credential would have changed that. A half-set gateway — a base with no
// token or the reverse — is treated the same way and named, because guessing
// a source the operator did not choose is a credential decision this package
// was explicitly not given.

// GatewayRouteSlug is the path segment the factory's gateway route serves the
// classifier under: <AI_GATEWAY_BASE_URL>/jev/v1/answers. cloudflare/src/
// gateway.ts serves the same slug with the TYPESAFE_API_KEY secret, and the
// parity test in credential_test.go fails the build if either side drifts.
const GatewayRouteSlug = "jev"

// The environment names, declared once so the sandbox contract, the operator's
// documentation and the tests quote the same spellings.
const (
	// GatewayBaseEnv and GatewayTokenEnv are the sandbox's model path, exactly
	// as image/common.sh exports it to every container a run boots — the
	// orchestrator's included, which is the process that classifies.
	GatewayBaseEnv  = "AI_GATEWAY_BASE_URL"
	GatewayTokenEnv = "AI_GATEWAY_TOKEN"
	// OperatorKeyEnv is the operator's own classifier key, and
	// OperatorBaseEnv optionally moves the API root it is presented to.
	OperatorKeyEnv  = "TICFAC_JEV_API_KEY"
	OperatorBaseEnv = "TICFAC_JEV_API_BASE"
)

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

// ResolveCredential resolves the classifier's credential from the
// environment: the gateway route a sandbox holds, else the operator's own
// key, else nothing. `lookup` is passed in rather than reached for so the
// resolution is the same pure function under test as in production.
func ResolveCredential(lookup func(string) string) CredentialSource {
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
			Note: "classifier: Jev, through this run's gateway route (" + GatewayBaseEnv + "), " +
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

	// The operator's own key, locally.
	if key := value(OperatorKeyEnv); key != "" {
		base := strings.Trim(value(OperatorBaseEnv), " /")
		source := CredentialSource{
			Config:     Config{APIBase: base, APIKey: key},
			Configured: true,
			Note:       "classifier: Jev, on the operator's own credential ($" + OperatorKeyEnv + ")",
		}
		if source.Config.APIBase != "" {
			source.Note += ", at $" + OperatorBaseEnv
		}
		return source
	}

	// Neither source: the documented degradation, said out loud.
	return CredentialSource{
		Note: "no classifier credential: this process has no run gateway (" + GatewayBaseEnv + "/" + GatewayTokenEnv +
			") and no operator key ($" + OperatorKeyEnv + "), so no tick is classified and every dispatch starts " +
			"at [tier_policy.start]",
	}
}
