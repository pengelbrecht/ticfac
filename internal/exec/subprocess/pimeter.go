package subprocess

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LOCAL GATEWAY METERING (tick dm2, epic hn6 — rule 7's metered half): how
// a local run's pi workers join their Workers AI spend to the operator's AI
// Gateway logs, so the dashboard can state a number a measurement made.
//
// The cloud run's calls are stamped by the factory's own proxy: every model
// call a container makes crosses the factory Worker, which exchanges the
// run's token and stamps cf-aig-metadata with the run id on the upstream
// request (cloudflare/src/gateway.ts, D17). A LOCAL run has no such hop: its
// pi workers call Workers AI straight through the provider's own address,
// api.cloudflare.com, so nothing tags the request and the gateway's logs
// carry no row a per-run read could join — which is why the local cost line
// has only ever said "not metered".
//
// The join is made where the run launches the worker. pi's extension surface
// can override a built-in provider's address and headers without touching its
// models or its stored credential (pi's custom-provider docs: "When only
// baseUrl and/or headers are provided (no models), all existing models for
// that provider are preserved with the new endpoint"), and herdr passes the
// agent's argv through verbatim — so the executor writes one small extension
// file into the ATTEMPT's state directory, with the run id and the operator's
// gateway URL baked in as literals, and appends `--extension <path>` to the
// agent's launch:
//
//   - the provider's baseUrl becomes the operator's own gateway route
//     (<gateway>/workers-ai — the same OpenAI-compatible route the factory
//     proxies its runs' calls through, so the wire shape is the one the
//     sandbox already proved: image/common.sh's configure_pi_provider writes
//     exactly this override, baseUrl and all, for cloud workers);
//   - every request carries cf-aig-metadata {"run_id": …} — the header the
//     gateway turns into the metadata its logs filter by, the same key and
//     value gatewaytrace (the status model's reader) filters on — so a run's
//     spend is attributed to the run and to nothing else;
//   - x-session-affinity names the run, the header the factory stamps too
//     (D24): one run, one model instance, so an agentic loop's unchanged
//     prompt prefix stays cached instead of being re-billed at full price.
//
// The operator's Cloudflare API token is NOT written anywhere by this: the
// provider's own stored credential (pi's auth for cloudflare-workers-ai)
// keeps resolving exactly as it does without the override, and the gateway
// accepts it for the Workers AI route the same way it accepts the factory's.
//
// NOTHING here is repository content: the extension file lives in the
// executor's state directory, outside every checkout, because it names the
// operator's account (the gateway URL) — the same boundary gatewaytrace and
// the factory's own credentials keep.

// workersAIProvider is pi's own name for the built-in provider that serves
// the @cf/… models the cloud rule routes: overriding it by name keeps every
// catalog entry (context window, thinking) and changes only the address and
// the headers.
const workersAIProvider = "cloudflare-workers-ai"

// workersAIRoute is the AI Gateway's own route segment for Workers AI —
// <gateway>/workers-ai/v1, the same route the factory proxies a cloud run's
// calls through (image/common.sh sets the pi provider override's baseUrl to
// "$gateway/workers-ai/v1", because pi's openai-completions wire appends
// chat/completions to the baseUrl as given), and NOT pi's provider name.
// Live, tick dm2: without the /v1 the gateway answers 7003 "could not route
// to /accounts/<account>/ai/chat/completions".
const workersAIRoute = "workers-ai/v1"

// extensionFile is the generated override's name in the attempt's state
// directory: one attempt, one file, named for what it is.
const extensionFile = "gateway-metering.mjs"

// metadataHeader is the header the AI Gateway turns into the metadata its
// logs can be filtered by — the same name the factory's proxy stamps
// (cloudflare/src/gateway.ts) and the same key gatewaytrace filters on.
const metadataHeader = "cf-aig-metadata"

// gatewayAuthHeader is the AI Gateway's own credential header — the one the
// factory's proxy stamps from its own CLOUDFLARE_API_TOKEN and the one the
// gateway honours over any plain Authorization the caller's harness sends
// (live, tick dm2: a request carrying an unpermitted wallet key in
// Authorization and the account token here answers 200).
const gatewayAuthHeader = "cf-aig-authorization"

// gatewayCredentialCommand reads the operator's Cloudflare API token — the
// factory_cloudflare_api_token key of ~/.ticfacrc, the SAME credential the
// factory itself authenticates its own gateway traffic with — at request
// time, through pi's `!command` config-value syntax (the whole value after
// `!` runs in the shell, resolved once and cached), and stamps it as the
// cf-aig-authorization header's Bearer value.
//
// WHY THE OVERRIDE MUST SUPPLY A CREDENTIAL AT ALL: pi's own stored auth for
// the cloudflare-workers-ai provider is the Workers AI wallet key (the cfu_…
// token of the unified billing rung), which api.cloudflare.com accepts and
// the GATEWAY refuses (live, tick dm2: 401 code 2009) — the gateway's
// Workers AI route authenticates the account's Cloudflare token, exactly as
// the factory's proxy stamps it. A provider-config apiKey override cannot
// displace a stored credential (live: pi sent its own key anyway), so the
// account token rides a HEADER the gateway reads with priority — and pi's
// own credential is left exactly as it resolved, used or ignored by the
// gateway as its docs say.
//
// The command is resolved at REQUEST time and nothing is copied to disk: the
// generated file names the KEY, never the token, and only on a host whose
// ~/.ticfacrc holds both halves — the caller refuses to build the join when
// either is missing, so the command never runs where it would find nothing.
const gatewayCredentialCommand = "!grep '^factory_cloudflare_api_token=' \"$HOME/.ticfacrc\" | cut -d= -f2- | sed 's/^/Bearer /'"

// affinityHeader is Workers AI's prefix-cache routing header (D24): the value
// is the run id, exactly as the factory stamps it for a cloud run.
const affinityHeader = "x-session-affinity"

// GatewayMetering is the local join, resolved once per dispatch by the caller
// that knows both halves: the run id the spend is attributed to, and the
// operator's AI Gateway base URL (https://gateway.ai.cloudflare.com/v1/
// <account>/<gateway>, the factory_gateway_url key of ~/.ticfacrc). Nil
// means the host states no gateway, and a worker launched with nil Metering
// runs exactly as it did before this tick — its calls unattributed, the cost
// line honestly unmetered.
type GatewayMetering struct {
	RunID      string
	GatewayURL string
}

// Applies reports whether a model's spend runs through this join: only a
// Workers AI model does — the same provider namespaces the cloud rule names
// (profile.CloudRule.ModelNamespaces) — because the override is the
// cloudflare-workers-ai provider's own. A claude or openrouter id keeps the
// credential river it already has; this join would tag a request that never
// reaches the gateway and route a provider that is not its own.
func (m *GatewayMetering) Applies(model string) bool {
	if m == nil {
		return false
	}
	for _, ns := range []string{"cloudflare-workers-ai/", "workers-ai/", "@cf/"} {
		if rest, ok := strings.CutPrefix(model, ns); ok && rest != "" {
			return true
		}
	}
	return false
}

// gatewayMetadata is the metadata every metered request carries. It is the
// one key the reader joins on, spelled as the logs API's filter expects: a
// JSON object of string values, exactly the shape the factory's proxy stamps
// (gatewayMetadata in cloudflare/src/gateway.ts) and gatewaytrace filters by.
type gatewayMetadata struct {
	RunID string `json:"run_id"`
}

// WriteExtension renders the override into dir and returns its path. The
// values are baked in as literals — the file is per-attempt state, generated
// at dispatch, and an env interpolation would only move the two facts the
// executor already holds into a name a pane may not export.
func (m *GatewayMetering) WriteExtension(dir string) (string, error) {
	if m == nil || m.RunID == "" || strings.TrimSpace(m.GatewayURL) == "" {
		return "", fmt.Errorf("gateway metering is not configured: a run id and a gateway URL are both required")
	}
	metadata, err := json.Marshal(gatewayMetadata{RunID: m.RunID})
	if err != nil {
		return "", fmt.Errorf("encode the gateway metadata: %w", err)
	}
	base := strings.TrimRight(strings.TrimSpace(m.GatewayURL), "/") + "/" + workersAIRoute
	if !strings.Contains(base, "://") {
		return "", fmt.Errorf("the gateway URL %q is not an absolute URL: the provider override would point pi at a relative address", m.GatewayURL)
	}
	var body strings.Builder
	body.WriteString("// Generated by ticfac — this run's Workers AI calls join the operator's AI\n")
	body.WriteString("// Gateway logs, tagged with the run id so the spend is attributed to the\n")
	body.WriteString("// run (tick dm2). Per-attempt state, not repository content.\n")
	body.WriteString("export default function (pi) {\n")
	body.WriteString("  pi.registerProvider(" + jsonWord(workersAIProvider) + ", {\n")
	body.WriteString("    baseUrl: " + jsonWord(base) + ",\n")
	body.WriteString("    api: \"openai-completions\",\n")
	body.WriteString("    headers: {\n")
	body.WriteString("      " + jsonWord(gatewayAuthHeader) + ": " + jsonWord(gatewayCredentialCommand) + ",\n")
	body.WriteString("      " + jsonWord(metadataHeader) + ": " + jsonWord(string(metadata)) + ",\n")
	body.WriteString("      " + jsonWord(affinityHeader) + ": " + jsonWord(m.RunID) + ",\n")
	body.WriteString("    },\n")
	body.WriteString("  });\n")
	body.WriteString("}\n")
	path := filepath.Join(dir, extensionFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create the attempt state directory for the gateway metering extension: %w", err)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		return "", fmt.Errorf("write the gateway metering extension at %s: %w", path, err)
	}
	return path, nil
}

// jsonWord quotes one literal for the generated module: JSON quoting is a
// subset of JavaScript's, and the values are ids and URLs the executor
// resolved — encoding them rather than interpolating them keeps a path or an
// id with a quote in it from breaking the module that carries it.
func jsonWord(v string) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "\"\""
	}
	return string(encoded)
}

// ExtensionArgs is the argv that loads the override into a pi launch:
// ["--extension", <path>]. A nil metering names no args, so the caller that
// has nothing to join launches the agent exactly as it did before.
func (m *GatewayMetering) ExtensionArgs(path string) []string {
	if m == nil || path == "" {
		return nil
	}
	return []string{"--extension", path}
}
