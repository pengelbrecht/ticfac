/**
 * The staging proof of the Workers AI gateway provider (tick oq4), against the
 * staging gateway Worker (cloudflare/src/staging-gateway.ts). Run from a host
 * with Node 24 (type stripping):
 *
 *   PROOF_URL=https://<staging gateway> PROOF_TOKEN=<its token> \
 *     node proof/gateway-staging.ts [evidence.json]
 *
 * It mints a run and a worker token on staging, runs a pi-durable
 * conversation with a tool round on `@cf/zai-org/glm-5.3` through the
 * provider, revokes the run's tokens, submits again, and then reads back what
 * reached the AI Gateway and the gateway's own log of each request. It exits 1
 * on the first claim that does not hold, and prints (or writes) the evidence
 * with the staging host and the token redacted.
 */

import { writeFileSync } from "node:fs";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { createModels, Type } from "@earendil-works/pi-ai";
import {
  createRegistry,
  defineExtension,
  defineTool,
  Harness,
  MemoryStorage,
} from "@earendil-works/pi-durable";
import { gatewayModelRef, workersAIGatewayProvider } from "../src/gateway/workers-ai.ts";

const base = (process.env.PROOF_URL ?? "").replace(/\/+$/, "");
const proofToken = process.env.PROOF_TOKEN ?? "";
if (base === "" || proofToken === "") {
  console.error("PROOF_URL and PROOF_TOKEN must be set");
  process.exit(2);
}
const MODEL = "cloudflare-workers-ai/@cf/zai-org/glm-5.3";

async function control(method: string, path: string): Promise<Record<string, unknown>> {
  const response = await fetch(`${base}${path}`, {
    method,
    headers: { authorization: `Bearer ${proofToken}` },
  });
  if (!response.ok) throw new Error(`${method} ${path}: HTTP ${response.status}`);
  return (await response.json()) as Record<string, unknown>;
}

const failures: string[] = [];
function claim(ok: boolean, what: string): void {
  console.error(`${ok ? "HOLDS" : "FAILS"}  ${what}`);
  if (!ok) failures.push(what);
}

// Every request the provider makes, as the provider sent it and as the
// gateway answered: the client half of the evidence.
type Sent = {
  at: string;
  status: number;
  path: string;
  bearer_is_run_token: boolean;
  cf_headers: string[];
  model: unknown;
  max_completion_tokens: unknown;
  thinking: unknown;
  reasoning_effort: unknown;
  refusal?: string;
};
const sent: Sent[] = [];

const run = await control("POST", "/proof/run?tick_id=oq4");
const runId = String(run.run_id);
const runToken = String(run.token);
const before = ((await control("GET", "/proof/seen")).seen as { seq: number }[]).at(-1)?.seq ?? 0;

const recordingFetch: typeof fetch = async (input, init) => {
  const request = new Request(input, init);
  const body = JSON.parse(await request.clone().text()) as Record<string, unknown>;
  const response = await fetch(request);
  const entry: Sent = {
    at: new Date().toISOString(),
    status: response.status,
    path: new URL(request.url).pathname,
    bearer_is_run_token: request.headers.get("authorization") === `Bearer ${runToken}`,
    cf_headers: [...request.headers.keys()].filter((name) => name.startsWith("cf-")),
    model: body.model,
    max_completion_tokens: body.max_completion_tokens,
    thinking: body.thinking,
    reasoning_effort: body.reasoning_effort,
  };
  if (!response.ok) entry.refusal = await response.clone().text();
  sent.push(entry);
  return response;
};

const echoed: string[] = [];
const echo = defineTool({
  name: "echo",
  description: "Echo the given text back as the tool result.",
  parameters: Type.Object({ text: Type.String() }),
  replay: "safe",
  async execute(args) {
    echoed.push(args.text);
    return { content: [{ type: "text", text: args.text }] };
  },
});
const models = createModels();
models.setProvider(
  workersAIGatewayProvider({
    gateway: `${base}/api/gateway`,
    token: runToken,
    fetch: recordingFetch,
  }),
);
const registry = createRegistry();
registry.install(defineExtension({ name: "echo", tools: [echo] }));
const context = BACKGROUND_CONTEXT;
const harness = await Harness.open(
  new MemoryStorage(),
  { models, registry, settings: { retry: { enabled: true, maxRetries: 2, baseDelayMs: 500 } } },
  context,
);
const root = await harness.root(context, {
  agent: { model: gatewayModelRef(MODEL), thinkingLevel: "high" },
});

// Turn 1: a tool round and an answer, on the live token.
const first = await (
  await root.submit(
    {
      type: "input",
      content:
        'Call the echo tool exactly once with the text "staging-proof", then reply with only the text the tool returned.',
    },
    context,
  )
).wait(context);
const live = sent.length;
claim(first.status === "done", `turn 1 settles done on the live token (got ${first.status})`);
claim(echoed.includes("staging-proof"), "turn 1 ran the echo tool through a tool round");
claim(live >= 2, `turn 1 made a request on each side of the tool round (${live} requests)`);
claim(
  sent.every((entry) => entry.status === 200),
  "every turn-1 request was answered 200 through the gateway",
);

// The kill switch, between turns.
const revoked = await control("POST", `/proof/revoke?run_id=${runId}&reason=staging-proof`);
claim(Number(revoked.revoked) >= 1, `revocation killed the run's token (${revoked.revoked} rows)`);

// Turn 2: the next request is refused, and nothing retries it.
const second = await (await root.submit({ type: "input", content: "Say hi." }, context)).wait(
  context,
);
await harness.close(context);
const after = sent.slice(live);
claim(second.status === "unanswered", `turn 2 settles unanswered (got ${second.status})`);
claim(
  second.status === "unanswered" && second.reason === "model_error",
  `turn 2 fails as a model error (${second.status === "unanswered" ? second.reason : "-"})`,
);
claim(
  after.length === 1,
  `exactly one request after the revocation, not retried (${after.length})`,
);
claim(after[0]?.status === 403, `that request was refused 403 (got ${after[0]?.status})`);
claim(
  (after[0]?.refusal ?? "").includes("run_token_revoked"),
  "the refusal names run_token_revoked",
);

// The client half: route, credential, nothing claimed, the GLM overrides.
claim(
  sent.every((entry) => entry.path.endsWith("/api/gateway/workers-ai/v1/chat/completions")),
  "every request went to the gateway's workers-ai route",
);
claim(
  sent.every((entry) => entry.bearer_is_run_token),
  "every request presented the run token as its bearer",
);
claim(
  sent.every((entry) => entry.cf_headers.length === 0),
  "no request carried a cf-* header (attribution is the gateway's to stamp)",
);
claim(
  sent.every((entry) => entry.max_completion_tokens === 65536),
  "every request asked for max_completion_tokens 65536 (the GLM override)",
);
claim(
  sent.every((entry) => JSON.stringify(entry.thinking) === '{"type":"enabled"}'),
  'every request sent thinking {"type":"enabled"} (thinkingFormat deepseek)',
);

// The gateway half: what reached the AI Gateway, and the gateway's own log.
type Seen = {
  seq: number;
  status: number;
  sent_metadata: string | null;
  log_id: string | null;
  gateway_log: { metadata?: unknown; model?: unknown; error?: string } | null;
};
const seen = (await control("GET", `/proof/seen?after=${before}`)).seen as Seen[];
claim(
  seen.length === live,
  `the AI Gateway saw exactly the ${live} live requests and not the refused one (${seen.length})`,
);
for (const row of seen) {
  const stamped = JSON.parse(row.sent_metadata ?? "{}") as Record<string, unknown>;
  claim(
    stamped.run_id === runId && stamped.tick_id === "oq4",
    `request ${row.seq}: the route stamped run ${String(stamped.run_id)} tick ${String(stamped.tick_id)}`,
  );
  const logged = row.gateway_log?.metadata;
  const loggedFields =
    typeof logged === "string"
      ? (JSON.parse(logged) as Record<string, unknown>)
      : ((logged ?? {}) as Record<string, unknown>);
  claim(
    loggedFields.run_id === runId && loggedFields.tick_id === "oq4",
    `request ${row.seq}: the AI Gateway's own log (${row.log_id ?? "no log id"}) attributes it to the run (${JSON.stringify(row.gateway_log)})`,
  );
}

const redact = (text: string) =>
  text.split(base).join("<staging-gateway>").split(runToken).join("<run-token>");
const evidence = redact(
  JSON.stringify(
    {
      at: new Date().toISOString(),
      model: MODEL,
      run_id: runId,
      turn1: first,
      turn2: second,
      sent,
      seen,
      failures,
    },
    null,
    2,
  ),
);
const out = process.argv[2];
if (out === undefined) console.log(evidence);
else writeFileSync(out, `${evidence}\n`);
console.error(failures.length === 0 ? "PROOF HOLDS" : `PROOF FAILS: ${failures.length} claim(s)`);
process.exit(failures.length === 0 ? 0 : 1);
