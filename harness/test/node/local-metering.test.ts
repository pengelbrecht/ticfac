import type { ChildProcessByStdio } from "node:child_process";
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import type { Readable } from "node:stream";
import { fileURLToPath } from "node:url";
import { createModels, type Message } from "@earendil-works/pi-ai";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  type LocalMetering,
  meteredWorkersAIProvider,
  meteringApplies,
  meteringWorkersAIRoute,
} from "../../src/local/gateway-metering.js";
import { piAuthStore } from "../../src/local/pi-auth-store.js";
import { type LocalWorkerConfig, localWorkersAIProvider } from "../../src/local/worker-host.js";

/**
 * The local gateway metering join's acceptance tests (tick lrd, the harness
 * half of tick m1w): "a metered local launch tags its calls" — the join the
 * Go executor writes into worker.json reaches the wire of a real local
 * worker, exactly as the pi CLI's generated extension tags a herdr pane's.
 *
 * Everything here drives the REAL doors. The gateway is a real local HTTP
 * server standing in for the operator's AI Gateway (an SSE OpenAI-compatible
 * workers-ai route that RECORDS what crosses it, demands the identity the
 * real one demands — cf-aig-authorization or a 401 — and holds the stream's
 * tail back long enough to see whether the client drains it, the read the
 * gateway needs before it writes the row the metering IS, tick 648). The
 * credential is the REAL pipeline worker.json carries, executed against a
 * SEALED ~/.ticfacrc, so the operator's own token never reaches a wire this
 * suite asserts on. The launch tests drive the real entry
 * (`src/local/main.ts`) as a real child process over a real git worktree —
 * the same fixture shape as local-host.test.ts, with the model served by the
 * fake gateway instead of the faux provider, because the claim under test is
 * the MODEL WIRE.
 */

/** The harness package root, from this test's own place in it. */
const harnessRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

/**
 * The entry's invocation, exactly as the Go runner table spells it (see
 * local-host.test.ts for the flags' reasons).
 */
const NODE_FLAGS = [
  "--experimental-strip-types",
  "--import",
  join(harnessRoot, "runtime", "register.mjs"),
];

/** The account token the sealed ~/.ticfacrc names — never the operator's own. */
const ACCOUNT_TOKEN = "cf_lrd_metering_test_token";

/** The run the tests attribute: the metadata's key and the affinity's value. */
const RUN_ID = "run_lrd_metered";

/** The composed metadata value, the bytes the Go writer's one writer prints. */
const METADATA = '{"run_id":"run_lrd_metered","tick_id":"lrd","attempt":"10"}';

/** The routed model these tests speak: the local rung's own GLM. */
const GLM = "@cf/zai-org/glm-5.3";

/**
 * The credential pipeline EXACTLY as the Go side composes it —
 * internal/factory/credentials.ShellGetCommand(KeyCloudflareAPIToken) plus
 * the Bearer prefix the join's writer appends (pimeter.go's
 * gatewayCredentialPipeline, tick frr's one reader). The harness must execute
 * THIS text as worker.json carries it; the tests' ~/.ticfacrc answers it.
 */
const CREDENTIAL_PIPELINE =
  'cat "$HOME/.ticfacrc" 2>/dev/null | ' +
  "sed -n '/^[[:space:]]*factory_cloudflare_api_token[[:space:]]*=/" +
  "{s/^[[:space:]]*factory_cloudflare_api_token[[:space:]]*=//;" +
  "s/^[[:space:]]*//;s/[[:space:]]*$//;p;q;}'" +
  " | sed 's/^/Bearer /'";

/** The join as a worker.json carries it, against one gateway address. */
function meteringJoin(gatewayUrl: string): LocalMetering {
  return { gatewayUrl, runId: RUN_ID, metadata: METADATA, credentialCommand: CREDENTIAL_PIPELINE };
}

/**
 * A sealed HOME whose ~/.ticfacrc names exactly the test's account token —
 * in the hand-edited spelling with spaces the one reader reads fine (tick
 * frr), so the pipeline's own lenience is what the test exercises.
 */
function sealedHome(token: string): string {
  const home = mkdtempSync(join(tmpdir(), "ticfac-metering-home-"));
  writeFileSync(join(home, ".ticfacrc"), `factory_cloudflare_api_token = ${token}\n`);
  return home;
}

/** One request the fake gateway received, as the real one would read it. */
type SeenRequest = {
  url: string;
  headers: Record<string, string>;
  body: Record<string, unknown>;
};

/** One scripted answer: an assistant text, or a tool call. */
type Answer = { text: string } | { tool: string; args: Record<string, unknown> };

/**
 * How long the fake gateway holds the stream's tail back after
 * `finish_reason` — the read the AI Gateway needs before it writes the row a
 * metered run joins on (tick 648: a stream abandoned after the finish is a
 * call the logs never show). Long enough for a client that stopped at the
 * finish to have closed, short enough to cost the suite nothing.
 */
const TAIL_GAP_MS = 150;

/**
 * A recording stand-in for the operator's AI Gateway: it serves the
 * workers-ai route's OpenAI-compatible SSE stream in the shape the gateway
 * sends it, accepts only `cf-aig-authorization: Bearer <account token>` for
 * a live credential (refusing anything else with the gateway's own 401 —
 * the route a stored Workers AI wallet key is refused at, live dm2), records
 * every request it serves, and tracks whether the client drained each
 * stream to its tail. A fake more forgiving than the real thing certifies
 * the defect it hides.
 */
async function fakeAIGateway(answers: Answer[], token: string) {
  const seen: SeenRequest[] = [];
  const streams = { abandoned: 0, drained: 0 };
  const script = answers.slice();
  const chunk = (delta: Record<string, unknown>, finish: string | null, usage = false) =>
    `data: ${JSON.stringify({
      id: "chatcmpl-lrd",
      object: "chat.completion.chunk",
      created: 1,
      model: GLM,
      choices: usage ? [] : [{ index: 0, delta, finish_reason: finish }],
      ...(usage ? { usage: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 } } : {}),
    })}\n\n`;
  const server = createServer(async (request, response) => {
    const chunks: Buffer[] = [];
    for await (const chunk of request) chunks.push(chunk as Buffer);
    const headers: Record<string, string> = {};
    for (const [name, value] of Object.entries(request.headers)) headers[name] = String(value);
    const raw = Buffer.concat(chunks).toString("utf8");
    seen.push({
      url: request.url ?? "",
      headers,
      body: raw === "" ? {} : (JSON.parse(raw) as Record<string, unknown>),
    });
    if (headers["cf-aig-authorization"] !== `Bearer ${token}`) {
      response.writeHead(401, { "content-type": "application/json" });
      response.end(JSON.stringify({ error: { code: 2009, message: "Unauthorized" } }));
      return;
    }
    const answer = script.shift();
    if (answer === undefined) {
      response.writeHead(418, { "content-type": "application/json" });
      response.end(JSON.stringify({ error: "script_exhausted" }));
      return;
    }
    response.writeHead(200, { "content-type": "text/event-stream" });
    if ("text" in answer) {
      response.write(chunk({ role: "assistant", content: answer.text }, null));
      response.write(chunk({}, "stop"));
    } else {
      response.write(
        chunk(
          {
            role: "assistant",
            tool_calls: [
              {
                index: 0,
                id: "call-lrd",
                type: "function",
                function: { name: answer.tool, arguments: JSON.stringify(answer.args) },
              },
            ],
          },
          null,
        ),
      );
      response.write(chunk({}, "tool_calls"));
    }
    // The tail is held back: the gateway writes the call's log row only once
    // the client has read the streamed response to its end, so the gap is
    // what makes an abandoned stream observable.
    await new Promise((resolve) => setTimeout(resolve, TAIL_GAP_MS));
    if (response.destroyed) {
      streams.abandoned += 1;
      return;
    }
    response.write(chunk({}, null, true));
    response.write("data: [DONE]\n\n");
    response.end();
    if (response.destroyed) streams.abandoned += 1;
    else streams.drained += 1;
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return {
    seen,
    streams,
    url: `http://127.0.0.1:${port}`,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

type RunChild = { readonly output: () => string } & ChildProcessByStdio<null, Readable, Readable>;

type Fixture = {
  root: string;
  origin: string;
  worktree: string;
  /** The sealed HOME whose ~/.ticfacrc the join's credential pipeline reads. */
  home: string;
  configPath: string;
  config: (over: Partial<LocalWorkerConfig>) => LocalWorkerConfig;
  transcript: (turns: unknown[]) => string;
  run: (
    over: Partial<LocalWorkerConfig>,
    env?: Record<string, string>,
    message?: string,
  ) => RunChild;
  /** Every child `run` spawned, so afterEach can reap one still alive. */
  readonly children: RunChild[];
};

/** A real repository, a real origin, a real worktree — the attempt's shape. */
function makeFixture(): Fixture {
  const root = mkdtempSync(join(tmpdir(), "ticfac-local-metering-"));
  const origin = join(root, "origin.git");
  execFileSync("git", ["init", "--quiet", "--bare", "-b", "main", origin]);
  const seed = join(root, "seed");
  execFileSync("git", ["init", "--quiet", "-b", "main", seed]);
  writeFileSync(join(seed, "README.md"), "# the base\n");
  execFileSync("git", ["-C", seed, "add", "-A"]);
  execFileSync("git", [
    "-C",
    seed,
    "-c",
    "user.name=seed",
    "-c",
    "user.email=seed@example.com",
    "commit",
    "--quiet",
    "-m",
    "the base commit",
  ]);
  execFileSync("git", ["-C", seed, "remote", "add", "origin", origin]);
  execFileSync("git", ["-C", seed, "push", "--quiet", "origin", "HEAD:refs/heads/main"]);
  const worktree = join(root, "worktree");
  execFileSync("git", ["clone", "--quiet", origin, worktree]);
  execFileSync("git", ["-C", worktree, "config", "user.name", "ticfac worker"]);
  execFileSync("git", ["-C", worktree, "config", "user.email", "worker@example.com"]);

  const home = sealedHome(ACCOUNT_TOKEN);
  const base: LocalWorkerConfig = {
    storage: join(root, "worker.sqlite"),
    steerSock: join(root, "steer.sock"),
    worktree,
    remote: origin,
    branch: "ticfac/run-ex6/tick-lrd/attempt-10",
    base: execFileSync("git", ["--git-dir", origin, "rev-parse", "main"], {
      encoding: "utf8",
    }).trim(),
    report: join(worktree, "RESULT-lrd.md"),
    checker: join(root, "checker.sh"),
    tick: "lrd",
    role: "implement",
    model: `cloudflare-workers-ai/${GLM}`,
  };
  writeFileSync(base.checker, "#!/bin/sh\nexit 0\n");
  execFileSync("chmod", ["+x", base.checker]);
  const configPath = join(root, "worker.json");
  const config = (over: Partial<LocalWorkerConfig>): LocalWorkerConfig => {
    const merged = { ...base, ...over };
    writeFileSync(configPath, JSON.stringify(merged));
    return merged;
  };
  let transcriptN = 0;
  const children: RunChild[] = [];
  const transcript = (turns: unknown[]): string => {
    transcriptN += 1;
    const file = join(root, `transcript-${transcriptN}.json`);
    writeFileSync(file, JSON.stringify(turns));
    return file;
  };
  const run = (
    over: Partial<LocalWorkerConfig>,
    env?: Record<string, string>,
    message = "do the job",
  ): RunChild => {
    config(over);
    const child = spawn(
      process.execPath,
      [
        ...NODE_FLAGS,
        join(harnessRoot, "src", "local", "main.ts"),
        "--config",
        configPath,
        "--message",
        message,
      ],
      {
        cwd: root,
        // The sealed credentials: the join's pipeline reads ~/.ticfacrc from
        // the HOME this fixture writes — never the operator's own — and pi's
        // auth store is pointed at a file this fixture never creates, so
        // nothing of the operator's is read by a child these tests launch.
        // The ambient key and the empty store are the credentials a WRONG
        // composition would fall back to, so the wire assertions below catch
        // the displacement rather than a pass by forgiveness.
        env: {
          ...process.env,
          ...(env ?? {}),
          HOME: home,
          TICFAC_PI_AUTH_FILE: join(root, "auth.json"),
          CLOUDFLARE_API_KEY: "ambient-workers-ai-key",
          CLOUDFLARE_ACCOUNT_ID: "ambient-account",
        },
        detached: true,
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    let said = "";
    child.stdout.on("data", (c: Buffer) => (said += c.toString("utf8")));
    child.stderr.on("data", (c: Buffer) => (said += c.toString("utf8")));
    const run = Object.assign(child, { output: () => said }) as unknown as RunChild;
    children.push(run);
    return run;
  };
  return { root, origin, worktree, home, configPath, config, transcript, run, children };
}

/** The settled child's exit code, waited on. */
async function exitOf(child: RunChild): Promise<number> {
  return new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("exit", (code: number | null) => resolve(code ?? -1));
  });
}

/** What the provider stamped on every request it served, in one place. */
function expectTagged(request: SeenRequest): void {
  // The gateway's workers-ai route, OpenAI-compatible under /v1 — never the
  // Cloudflare API, never the model's own address. The request's PATH is
  // what the server sees; the host it dialed is the gateway URL the join
  // composed this route from, which the launch itself reaches for.
  expect(request.url).toBe("/workers-ai/v1/chat/completions");
  // Both bearers are the credential the join's command printed: the plain
  // Authorization the gateway forwards to Workers AI (which authenticates
  // that, live 648), and the gateway's own credential header that opens the
  // route (live dm2). Anything else here is an unmetered or anonymous call:
  // the ambient key, a stored key, or pi-ai's "unused" placeholder.
  expect(request.headers.authorization).toBe(`Bearer ${ACCOUNT_TOKEN}`);
  expect(request.headers["cf-aig-authorization"]).toBe(`Bearer ${ACCOUNT_TOKEN}`);
  // The attribution value is the composed string worker.json carried, read
  // verbatim — the one writer's rows, never a second composition of them.
  expect(request.headers["cf-aig-metadata"]).toBe(METADATA);
  // The run id, the header the gateway and the factory both name a run by.
  expect(request.headers["x-session-affinity"]).toBe(RUN_ID);
  expect(request.body.model).toBe(GLM);
}

describe("the join the harness composes", () => {
  it("applies to exactly the Workers AI spellings the writer's rule names", () => {
    for (const model of [
      "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
      "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash",
      "workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast",
      "@cf/zai-org/glm-5.3",
    ]) {
      expect(meteringApplies(model)).toBe(true);
    }
    for (const model of [
      "opus",
      "claude-opus-5",
      "openrouter/anthropic/claude-opus-5",
      "faux/faux-1",
      "",
      "cloudflare-workers-ai/",
    ]) {
      expect(meteringApplies(model)).toBe(false);
    }
  });

  it("composes the gateway's workers-ai route, trailing slashes and all", () => {
    expect(meteringWorkersAIRoute("https://gateway.ai.cloudflare.com/v1/acct/gw/")).toBe(
      "https://gateway.ai.cloudflare.com/v1/acct/gw/workers-ai/v1",
    );
    expect(meteringWorkersAIRoute("https://gateway.ai.cloudflare.com/v1/acct/gw")).toBe(
      "https://gateway.ai.cloudflare.com/v1/acct/gw/workers-ai/v1",
    );
  });

  it("refuses a gateway URL it cannot compose a fetchable route from, by name", () => {
    expect(() => meteringWorkersAIRoute("acct/gw")).toThrow(/not a base URL/);
    expect(() => meteringWorkersAIRoute("ftp://gateway.example.com/acct/gw")).toThrow(
      /not an http\(s\) base URL/,
    );
  });

  it("keeps the local rung's catalog, GLM corrections included — only the address changes", () => {
    const join = meteringJoin("https://gateway.example.com/acct/gw");
    const provider = meteredWorkersAIProvider(localWorkersAIProvider(), join);
    const models = provider.getModels();
    expect(models.map((model) => model.id)).toEqual(
      localWorkersAIProvider()
        .getModels()
        .map((model) => model.id),
    );
    const glm = models.find((model) => model.id === GLM);
    // The GLM corrections on the wire the local rung already makes: a
    // near-1M max-output drew a bodyless 400 from Workers AI (learnings), and
    // the deepseek thinking format keeps `</think>` tags out of the text.
    expect(glm?.maxTokens).toBe(65536);
    expect(glm?.compat?.thinkingFormat).toBe("deepseek");
  });
});

describe("the metered provider's wire", () => {
  // The join's credential pipeline executes IN THIS PROCESS's shell, so the
  // suite's own HOME is sealed to the fixture's ~/.ticfacrc for these tests
  // and restored after — the operator's real token must never reach a wire
  // these assertions print on failure.
  let realHome: string | undefined;
  let home: string;

  beforeEach(() => {
    realHome = process.env.HOME;
    home = sealedHome(ACCOUNT_TOKEN);
    process.env.HOME = home;
  });
  afterEach(() => {
    if (realHome === undefined) delete process.env.HOME;
    else process.env.HOME = realHome;
    rmSync(home, { recursive: true, force: true });
  });

  it("sends every request to the join's route, tagged, on the credential the command prints", {
    timeout: 300_000,
  }, async () => {
    const gateway = await fakeAIGateway([{ text: "ok" }], ACCOUNT_TOKEN);
    try {
      // A stored Workers AI key the WRONG composition would send: the metered
      // provider's resolve ignores it, and the wire below says so.
      const store = join(home, "auth.json");
      writeFileSync(
        store,
        JSON.stringify({
          "cloudflare-workers-ai": {
            type: "api_key",
            key: "stored-workers-ai-key",
            env: { CLOUDFLARE_ACCOUNT_ID: "stored-account" },
          },
        }),
      );
      const models = createModels({ credentials: piAuthStore(store) });
      models.setProvider(
        meteredWorkersAIProvider(localWorkersAIProvider(), meteringJoin(gateway.url)),
      );
      const model = models.getModel("cloudflare-workers-ai", GLM);
      if (model === undefined) throw new Error("the catalog carries no GLM to ask");
      const message: Message = await models.completeSimple(model, {
        messages: [{ role: "user", content: "say ok", timestamp: Date.now() }],
      });
      expect(message.stopReason).toBe("stop");
      expect(gateway.seen.length).toBe(1);
      expectTagged(gateway.seen[0] as SeenRequest);
      // The catalog the override kept, on the wire: GLM's corrected
      // max-output, not the catalog's near-1M that drew a bodyless 400.
      expect((gateway.seen[0] as SeenRequest).body.max_completion_tokens).toBe(65536);
      expect(gateway.streams.abandoned).toBe(0);
      expect(gateway.streams.drained).toBe(1);
    } finally {
      await gateway.close();
    }
  });

  it("refuses a request whose command prints nothing — the provider named, nothing sent", {
    timeout: 300_000,
  }, async () => {
    const gateway = await fakeAIGateway([{ text: "never" }], ACCOUNT_TOKEN);
    try {
      // The half-set host (tick dm2's own rule, from the resolver side): a
      // gateway is named, no account token is. The command finds nothing.
      writeFileSync(join(home, ".ticfacrc"), "factory_gateway_url=https://gateway.example.com\n");
      const models = createModels();
      models.setProvider(
        meteredWorkersAIProvider(localWorkersAIProvider(), meteringJoin(gateway.url)),
      );
      const model = models.getModel("cloudflare-workers-ai", GLM);
      if (model === undefined) throw new Error("the catalog carries no GLM to ask");
      const message: Message = await models.completeSimple(model, {
        messages: [{ role: "user", content: "say ok", timestamp: Date.now() }],
      });
      expect(message.stopReason).toBe("error");
      expect(message.errorMessage ?? "").toContain(
        "Provider is not configured: cloudflare-workers-ai",
      );
      // The refusal is loud and it is the ONLY thing that happens: no request
      // goes out with an empty credential, because a call like that is
      // spend the operator can never attribute.
      expect(gateway.seen.length).toBe(0);
    } finally {
      await gateway.close();
    }
  });
});

describe("a metered local launch", () => {
  let f: Fixture;
  beforeEach(() => {
    f = makeFixture();
  }, 120_000);
  afterEach(() => {
    for (const child of f.children) {
      if (child.exitCode === null && child.signalCode === null && child.pid !== undefined) {
        try {
          process.kill(-child.pid, "SIGKILL");
        } catch {
          /* already gone */
        }
      }
    }
    rmSync(f.root, { recursive: true, force: true });
  }, 120_000);

  it("tags its calls: the join worker.json carries reaches a real worker's wire", {
    timeout: 300_000,
  }, async () => {
    // THE TICK'S ACCEPTANCE. A real metered local launch — the real entry,
    // the real worktree, the real credential pipeline — whose every model
    // call crosses the fake AI Gateway tagged with the join's facts, so its
    // spend joins the gateway's logs exactly as a herdr/pi-CLI worker's does.
    const gateway = await fakeAIGateway(
      [
        {
          tool: "write",
          args: {
            path: "RESULT-lrd.md",
            content: "# lrd\n\nthe metered local run\n\nSTATUS: DONE\n",
          },
        },
        { text: "the metered local run answered" },
      ],
      ACCOUNT_TOKEN,
    );
    try {
      const config = f.config({ metering: meteringJoin(gateway.url) });
      const child = f.run({ metering: meteringJoin(gateway.url) });
      expect(await exitOf(child)).toBe(0);

      // The conversation really ran: the model's tool round wrote the report,
      // and the runner log ends with the answer's own words.
      expect(readFileSync(config.report, "utf8")).toContain("STATUS: DONE");
      expect(child.output()).toContain("the metered local run answered");

      // Two requests — the tool round and the final answer — and EVERY one
      // of them tagged, or the run's spend joins nothing.
      expect(gateway.seen.length).toBe(2);
      for (const request of gateway.seen) expectTagged(request);
      // The catalog the override kept, on the wire.
      expect(gateway.seen[0]?.body.max_completion_tokens).toBe(65536);
      // Every stream was drained to its tail: the gateway writes a call's
      // log row only once the client read the whole response (tick 648), so
      // a launch that abandoned the stream would meter nothing while passing
      // every header assertion above.
      expect(gateway.streams.abandoned).toBe(0);
      expect(gateway.streams.drained).toBe(2);
    } finally {
      await gateway.close();
    }
  });

  it("runs the tests' faux rung unmetered: a join it does not apply to reaches no wire", {
    timeout: 300_000,
  }, async () => {
    // The writer puts no join on the tests' rung, and the harness composes
    // one only for the models it applies to: a config carrying a join on the
    // faux rung (which no production dispatch would write) must run its
    // scripted transcript exactly as before, with no request routed at the
    // gateway — never a refusal, and never a tagged-less call.
    const gateway = await fakeAIGateway([{ text: "never" }], ACCOUNT_TOKEN);
    try {
      const transcript = f.transcript([
        {
          toolCalls: [
            {
              name: "write",
              args: { path: "RESULT-lrd.md", content: "# lrd\n\nfaux\n\nSTATUS: DONE\n" },
            },
          ],
        },
        { text: "the faux rung ran as before" },
      ]);
      const config = f.config({
        model: "faux/faux-1",
        fauxTranscript: transcript,
        metering: meteringJoin(gateway.url),
      });
      const child = f.run({
        model: "faux/faux-1",
        fauxTranscript: transcript,
        metering: meteringJoin(gateway.url),
      });
      expect(await exitOf(child)).toBe(0);
      expect(readFileSync(config.report, "utf8")).toContain("STATUS: DONE");
      expect(gateway.seen.length).toBe(0);
    } finally {
      await gateway.close();
    }
  });

  it("refuses at boot a join whose gateway URL composes no route, rather than running unmetered", {
    timeout: 300_000,
  }, async () => {
    // The writer's own rule, at the launch side: a join that cannot be
    // composed is a launch refusal (exit 2, the harness's cannot-run code),
    // never a silently unmetered run on some guessed address.
    const child = f.run({ metering: meteringJoin("not a url") });
    const code = await exitOf(child);
    expect(code).toBe(2);
    expect(child.output()).toContain("could not run");
    expect(child.output()).toContain("not a base URL");
  });

  it("settles unanswered, the provider named, when the join's command finds nothing", {
    timeout: 300_000,
  }, async () => {
    const gateway = await fakeAIGateway([{ text: "never" }], ACCOUNT_TOKEN);
    try {
      // The credential is gone (a rotated or hand-edited ~/.ticfacrc): the
      // honest answer is a refusal naming the provider, and no request at
      // all — never a call the operator cannot attribute.
      writeFileSync(join(f.home, ".ticfacrc"), "factory_gateway_url=https://gateway.example.com\n");
      const child = f.run({ metering: meteringJoin(gateway.url) });
      expect(await exitOf(child)).toBe(1);
      expect(child.output()).toContain("Provider is not configured: cloudflare-workers-ai");
      expect(gateway.seen.length).toBe(0);
    } finally {
      await gateway.close();
    }
  });
});
