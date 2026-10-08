/**
 * The local gateway metering join, composed (tick lrd — the harness half of
 * tick m1w): the join the Go executor resolves per dispatch and writes into
 * worker.json (internal/exec/subprocess/workerconfig.go — this file is the
 * one that comment names), composed into the provider override the pi CLI's
 * generated extension composes for a herdr pane
 * (internal/exec/subprocess/pimeter.go's WriteExtension): same route, same
 * headers, same credential, so a Workers AI call a local durable worker makes
 * joins the operator's AI Gateway logs exactly as a pi-CLI worker's does, and
 * the local half of "pi-durable workers are metered locally and in the cloud"
 * (epic ex6) carries rows a per-run read can join.
 *
 * A local run has no factory hop (tick dm2's finding, and this tick's): the
 * cloud run's calls are stamped by the factory's own proxy, but a local
 * durable worker without this override calls Workers AI straight through the
 * provider's own address on ambient credentials — nothing tags the request,
 * the gateway's logs carry no row a per-run read could join, and the local
 * cost line could only ever say "not metered".
 *
 * THE JOIN, as worker.json carries it — four facts, nothing else, spelled by
 * the Go writer's `workerMetering` (the field spellings are pinned from the
 * Go side by TestTheMeteringJoinsSpellingsMatchTheHarnessReader):
 *
 * - **gatewayUrl** — the operator's AI Gateway base URL
 *   (`https://gateway.ai.cloudflare.com/v1/<account>/<gateway>`); the
 *   OpenAI-compatible route `<gatewayUrl>/workers-ai/v1` is composed once,
 *   at boot.
 * - **runId** — the run the spend is attributed to: the metadata's key and
 *   the affinity header's value.
 * - **metadata** — the COMPOSED cf-aig-metadata header value, written by
 *   the Go side's one writer (GatewayMetering.MetadataValue). The harness
 *   reads one string and stamps it, never re-composing it, so its rows can
 *   never disagree with the rows the same join tags through the pi CLI.
 * - **credentialCommand** — the POSIX shell pipeline that prints the
 *   operator's Cloudflare API token as its Bearer value. The harness
 *   executes it ITSELF, at request time (the pi CLI resolves the same
 *   pipeline through its `!command` config-value syntax): the token is
 *   never written anywhere by this half, and a rotated ~/.ticfacrc is what
 *   the next request presents.
 *
 * The override mirrors WriteExtension's composition field for field: the
 * provider is still pi-ai's own `cloudflare-workers-ai`, every catalog entry
 * kept — only the address and the credential change, which are pi's
 * extension surface's own "registerProvider with only baseUrl and headers"
 * semantics — and every request carries the four headers the gateway joins
 * its logs by:
 *
 * - `Authorization` — the plain bearer the gateway FORWARDS to Workers AI,
 *   which authenticates that header and not the gateway's own (live, tick
 *   648 probe e: a valid cf-aig-authorization beside a bogus Authorization
 *   fails with upstream code 10000). It displaces whatever credential the
 *   host resolved for the provider, the same displacement the generated
 *   extension performs for the pi CLI.
 * - `cf-aig-authorization` — the AI Gateway's own credential header, the one
 *   that opens the route (live, tick dm2: the stored Workers AI wallet key
 *   is refused here with 401 code 2009).
 * - `cf-aig-metadata` — the attribution header the gateway turns into the
 *   metadata its logs filter by, the same key the status model's reader
 *   (internal/gatewaytrace) filters on.
 * - `x-session-affinity` — the run id, the header the factory stamps for a
 *   cloud run too (D24): one run, one model instance, so an agentic loop's
 *   unchanged prompt prefix stays cached instead of being re-billed.
 *
 * This module is node-only (it executes a shell pipeline), so it lives under
 * src/local with the rest of the local worker host and is never pulled into
 * the workerd bundle.
 */

import { exec } from "node:child_process";
import type { Provider } from "@earendil-works/pi-ai";

/** The metering join as worker.json carries it — the Go writer's workerMetering. */
export type LocalMetering = {
  /** The operator's AI Gateway base URL; the route is composed under it. */
  readonly gatewayUrl: string;
  /** The run the spend is attributed to — the metadata's key, affinity's value. */
  readonly runId: string;
  /** The composed cf-aig-metadata header value, written by the Go side's one writer. */
  readonly metadata: string;
  /** The POSIX shell pipeline that prints the operator's Cloudflare API token as its Bearer value. */
  readonly credentialCommand: string;
};

/** The AI Gateway's own route segment for Workers AI (pimeter.go's workersAIRoute). */
export const METERING_WORKERS_AI_ROUTE = "workers-ai/v1";

/** The plain bearer the gateway forwards to Workers AI, which authenticates it. */
export const METERING_UPSTREAM_AUTH_HEADER = "Authorization";

/** The AI Gateway's own credential header — the one that opens the route. */
export const METERING_GATEWAY_AUTH_HEADER = "cf-aig-authorization";

/** The attribution header the gateway turns into the metadata its logs filter by. */
export const METERING_METADATA_HEADER = "cf-aig-metadata";

/** Workers AI's prefix-cache routing header (D24): the value is the run id. */
export const METERING_AFFINITY_HEADER = "x-session-affinity";

/**
 * The gateway base URL, trimmed and validated — the same bar the writer
 * validates it against before this file is ever written (workerconfig.go's
 * gatewayBase: "a join that cannot be composed is a launch refusal, never a
 * silently unmetered run"). The harness composes the route at boot, so a
 * gateway URL it cannot compose an http(s) address from refuses the launch
 * by name rather than pointing a worker at a relative or unfetchable address.
 */
export function meteringGatewayBase(raw: string): string {
  const base = raw.trim().replace(/\/+$/, "");
  let parsed: URL;
  try {
    parsed = new URL(base);
  } catch {
    throw new Error(
      `the metering join's gateway URL is not a base URL: "${raw}" — the provider ` +
        "override would point the worker at a relative address",
    );
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    throw new Error(
      `the metering join's gateway URL is not an http(s) base URL: "${raw}" — the provider ` +
        "override would point the worker at an address no request can reach",
    );
  }
  return base;
}

/**
 * The route every metered request goes to: `<gateway>/workers-ai/v1`, the
 * OpenAI-compatible route the AI Gateway serves Workers AI on and the one
 * the factory proxies a cloud run's calls through — the pi CLI's provider
 * override writes exactly this baseUrl (image/common.sh's
 * configure_pi_provider), and pi-ai's openai-completions wire appends
 * `chat/completions` to it as given.
 */
export function meteringWorkersAIRoute(gatewayUrl: string): string {
  return `${meteringGatewayBase(gatewayUrl)}/${METERING_WORKERS_AI_ROUTE}`;
}

/**
 * Whether a routed model's spend runs through the join: only a Workers AI
 * model does, the same rule the writer and the herdr executor's extension
 * launch apply (GatewayMetering.Applies) — the override is the
 * cloudflare-workers-ai provider's own, so tagging any other provider's call
 * would route a request that never reaches the gateway and leave the run
 * looking metered while its spend joins nothing. The tests' faux rung never
 * applies: its credential river is the scripted transcript, and the writer
 * puts no join on it.
 */
export function meteringApplies(routed: string): boolean {
  for (const namespace of ["cloudflare-workers-ai/", "workers-ai/", "@cf/"]) {
    if (routed.startsWith(namespace) && routed.slice(namespace.length) !== "") return true;
  }
  return false;
}

/**
 * The credential the join's command prints, executed at request time in a
 * POSIX shell — the same pipeline the pi CLI resolves through its
 * `!command` config-value syntax, resolved per request so a rotated
 * ~/.ticfacrc is what the NEXT request presents. The output is the header
 * VALUE (`Bearer <token>`), never a token this half re-parses: both headers
 * the join stamps carry what the command printed, byte for byte, and the
 * token is never written to disk.
 */
export function resolveMeteringCredential(command: string, signal?: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    exec(command, { signal }, (error, stdout) => {
      if (error !== null && error !== undefined) {
        reject(
          new Error(
            `the metering join's credential command did not answer: ${String(error)} — ` +
              "a metered dispatch's requests cannot be sent without the credential it names",
          ),
        );
        return;
      }
      resolve(stdout.trim());
    });
  });
}

/**
 * The provider override a metered local worker runs on: the local rung's own
 * Workers AI provider with the join's address and headers, the harness's
 * counterpart of the extension WriteExtension generates for the pi CLI.
 *
 * `base` is the local rung's provider as `localWorkersAIProvider()` builds it
 * (pi-ai's catalog with the GLM corrections) and is taken as a parameter so
 * this module imports nothing from the host: every catalog entry is kept —
 * only the address and the credential change. The route is composed EAGERLY,
 * so a gateway URL that cannot compose one refuses the boot rather than the
 * first request.
 *
 * The credential is resolved at request time through pi-ai's own auth seam:
 * `resolve` is asked at every request, it executes the join's command, and
 * an empty answer is "not configured" — which pi-ai reports as a stream error
 * naming the provider rather than sending a request with no credential, the
 * same answer the cloud rung's gateway provider gives an empty token. A
 * dispatch whose ~/.ticfacrc lost its token fails loudly; it never makes an
 * anonymous call the operator cannot attribute.
 */
export function meteredWorkersAIProvider(
  base: Provider<"openai-completions">,
  join: LocalMetering,
  route: string = meteringWorkersAIRoute(join.gatewayUrl),
): Provider<"openai-completions"> {
  const { credentialCommand, runId, metadata } = join;
  return {
    ...base,
    auth: {
      apiKey: {
        name: "Operator gateway credential command",
        resolve: async ({ signal }) => {
          const bearer = await resolveMeteringCredential(credentialCommand, signal);
          if (bearer === "") {
            return undefined;
          }
          return {
            auth: {
              headers: {
                [METERING_UPSTREAM_AUTH_HEADER]: bearer,
                [METERING_GATEWAY_AUTH_HEADER]: bearer,
                [METERING_METADATA_HEADER]: metadata,
                [METERING_AFFINITY_HEADER]: runId,
              },
              baseUrl: route,
            },
            source: "operator gateway credential command",
          };
        },
      },
    },
  };
}
