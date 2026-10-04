import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  createModels,
  type FauxResponseFactory,
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
  type Message,
  Type,
} from "@earendil-works/pi-ai";
import {
  createRegistry,
  defineExtension,
  defineTool,
  GenerationTask,
  Harness,
  hook,
  MemoryStorage,
  type ToolRegistration,
} from "@earendil-works/pi-durable";
import { describe, expect, it } from "vitest";
import {
  armWallDeadline,
  shellQuote,
  WORKER_HEADLESS_LINE,
  WORKER_MAX_NUDGES,
  WORKER_MAX_PUSHBACKS,
  WORKER_REPORT_CHECKER,
  workerNudgeMessage,
  workerOnYield,
  workerReportCheckCommand,
} from "../src/worker-contract.js";
import { FakeWorkerEnv } from "./fake-worker-env.js";

/**
 * The worker contract as hooks on pi-durable (epic 43y step 5, tick pom):
 * the early-exit nudge (060) and the report linter pushback (#183) as
 * `onYield` follow-ups in the SAME conversation — worker.sh's nudge/pushback
 * loop ported to the durable path — and the wall deadline as `abort()`.
 *
 * The whole stack runs for real (tick 92k's replay test is the pattern): the
 * pinned pi-durable Harness, the faux provider, the built-in generation and
 * tool tasks, and the hook installed the way the worker host will install
 * it — as an extension's hook on GenerationTask. When the pinned API moves,
 * this fails in CI instead of in a run.
 */

const context = BACKGROUND_CONTEXT;
const repoDir = "/work/tick-pom";
const reportPath = "/work/tick-pom/RESULT-pom.md";
const branch = "tick/43y/pom";
const tick = "pom";

/** The text blocks of a message, whatever shape its content takes. */
function textOf(message: Message): string[] {
  if (typeof message.content === "string") {
    return [message.content];
  }
  return message.content.flatMap((block) => (block.type === "text" ? [block.text] : []));
}

function contractOptions() {
  return { reportPath, repoDir, branch, tick };
}

/**
 * A harness whose generation task carries the worker's onYield, installed
 * the way the worker host installs it: one extension, selected by the
 * conversation, whose hooks patch the built-in generation. `tools` rides in
 * the same extension — the stand-in agent's tools.
 */
async function workerHarness(env: FakeWorkerEnv, tools: readonly ToolRegistration[] = []) {
  const models = createModels();
  const faux = fauxProvider();
  models.setProvider(faux.provider);
  const extension = defineExtension({
    name: "worker-contract",
    tools,
    hooks: [hook(GenerationTask, { onYield: workerOnYield(env, contractOptions()) })],
  });
  const registry = createRegistry();
  registry.install(extension);
  const harness = await Harness.open(
    new MemoryStorage(),
    { models, registry, env: () => env },
    context,
  );
  const root = await harness.root(context, {
    agent: {
      model: { provider: "faux", modelId: "faux-1" },
      extensions: [extension],
    },
  });
  return { harness, root, faux };
}

/** Script the provider with responses that each record the transcript they saw. */
function recordResponses(
  faux: ReturnType<typeof fauxProvider>,
  responses: ReturnType<typeof fauxAssistantMessage>[],
): Message[][] {
  const seen: Message[][] = [];
  const factories: FauxResponseFactory[] = responses.map((message) => (transcript) => {
    seen.push([...transcript.messages]);
    return message;
  });
  faux.setResponses(factories);
  return seen;
}

/** Whether a request carries a user message containing `needle`. */
function requestCarriesUser(request: Message[] | undefined, needle: string): boolean {
  return (
    request?.some((m) => m.role === "user" && textOf(m).some((t) => t.includes(needle))) ?? false
  );
}

describe("the worker's onYield follow-ups", () => {
  it("nudges a yield that wrote no report, and the report the next turn writes settles the run", async () => {
    // The one check the third yield runs: the tool-written report passes.
    const env = new FakeWorkerEnv({
      cwd: repoDir,
      execScript: [{ exitCode: 0, output: "ok: the report passes the check\n" }],
    });

    // The stand-in agent: a tool the nudged turn calls to write its report.
    const writeReport = defineTool({
      name: "write_report",
      description: "Write the tick's report.",
      parameters: Type.Object({}),
      async execute(_args, api) {
        await api.env?.writeFile(
          reportPath,
          "# pom\n\nI did the thing.\n\nSTATUS: DONE\n",
          context,
        );
        return { content: [{ type: "text", text: "report written" }] };
      },
    });

    const { harness, root, faux } = await workerHarness(env, [writeReport]);
    const seen = recordResponses(faux, [
      fauxAssistantMessage("I have thought about it and I am done."),
      fauxAssistantMessage([fauxToolCall("write_report", {})], { stopReason: "toolUse" }),
      fauxAssistantMessage("The work is finished."),
    ]);

    const settled = await (
      await root.submit({ type: "input", content: "implement the tick" }, context)
    ).wait(context);

    // Three model turns: the empty-handed yield, the nudged turn that writes
    // the report, and the final yield whose report passes the check.
    expect(settled.status).toBe("done");
    expect(faux.state.callCount).toBe(3);

    // The nudge reached the model as the SAME conversation's next input —
    // the headless line and the report path, exactly what worker.sh's
    // nudge_prompt_text asks for.
    expect(requestCarriesUser(seen[1], WORKER_HEADLESS_LINE)).toBe(true);
    expect(requestCarriesUser(seen[1], reportPath)).toBe(true);
    expect(requestCarriesUser(seen[1], branch)).toBe(true);

    // The report the tool wrote is the one the checker was pointed at, and
    // the checker was asked the way the container asks it.
    expect(env.files.get(reportPath)).toContain("STATUS: DONE");
    expect(env.execCalls.length).toBe(1);
    const check = env.execCalls[0];
    expect(check?.command).toContain(`${shellQuote(WORKER_REPORT_CHECKER)} lint-report`);
    expect(check?.command).toContain(shellQuote(reportPath));
    expect(check?.command).toContain("--pushback");
    expect(check?.command).toContain(`--tick ${shellQuote(tick)}`);
    expect(check?.command).toContain(`--repo ${shellQuote(repoDir)}`);

    await harness.close(context);
  });

  it("pushes a failing report back with the checker's own message, at most twice", async () => {
    // The agent wrote its report before yielding — badly — and keeps being
    // unable to fix it: every check refuses.
    const refusal =
      "Your report does not pass the report check.\n\nerror: STATUS: the report has no STATUS line\n";
    const env = new FakeWorkerEnv({
      cwd: repoDir,
      execScript: [
        { exitCode: 1, output: refusal },
        { exitCode: 1, output: refusal },
        { exitCode: 1, output: refusal },
      ],
    });
    env.writeFileDirect(reportPath, "# pom\n\nno status line here\n");

    const { harness, root, faux } = await workerHarness(env);
    const seen = recordResponses(faux, [
      fauxAssistantMessage("Done."),
      fauxAssistantMessage("I fixed it, I think."),
      fauxAssistantMessage("Still done."),
    ]);

    const settled = await (
      await root.submit({ type: "input", content: "implement the tick" }, context)
    ).wait(context);

    // The bound is the shell's own (REPORT_PUSHBACK_MAX): two pushbacks,
    // then the third failing yield stands and collect decides.
    expect(settled.status).toBe("done");
    expect(faux.state.callCount).toBe(3);
    expect(env.execCalls.length).toBe(3);
    expect(requestCarriesUser(seen[1], "error: STATUS: the report has no STATUS line")).toBe(true);
    expect(requestCarriesUser(seen[2], "error: STATUS: the report has no STATUS line")).toBe(true);

    await harness.close(context);
  });

  it("bounds the nudge itself: a report that never appears settles after the last one", async () => {
    // No tool writes the report; every check would be skipped because the
    // hook only checks a report that EXISTS.
    const env = new FakeWorkerEnv({ cwd: repoDir, execScript: [] });

    const { harness, root, faux } = await workerHarness(env);
    const seen = recordResponses(faux, [
      fauxAssistantMessage("Done thinking."),
      fauxAssistantMessage("Done again."),
      fauxAssistantMessage("Done for good."),
    ]);

    const settled = await (
      await root.submit({ type: "input", content: "implement the tick" }, context)
    ).wait(context);

    // NUDGE_MAX is the shell's own 2: yield → nudge, yield → nudge, yield →
    // the report is still missing and the yield stands.
    expect(settled.status).toBe("done");
    expect(faux.state.callCount).toBe(3);
    expect(env.execCalls.length).toBe(0);
    expect(requestCarriesUser(seen[1], WORKER_HEADLESS_LINE)).toBe(true);
    expect(requestCarriesUser(seen[2], WORKER_HEADLESS_LINE)).toBe(true);

    await harness.close(context);
  });
});

// The wall: a host deadline, then abort(), then the finish phase.
describe("the wall deadline as abort()", () => {
  // A tool that takes long enough for a deadline to catch it.
  const slowTool = (ms: number) =>
    defineTool({
      name: "slow",
      description: "Take a while.",
      parameters: Type.Object({}),
      async execute(_args, api) {
        await new Promise((resolve) => setTimeout(resolve, ms));
        void api;
        return { content: [{ type: "text", text: "finally" }] };
      },
    });

  it("aborts the conversation at the deadline, and no model call follows", async () => {
    const env = new FakeWorkerEnv({ cwd: repoDir, execScript: [] });
    const { harness, root, faux } = await workerHarness(env, [slowTool(400)]);
    recordResponses(faux, [
      fauxAssistantMessage([fauxToolCall("slow", {})], { stopReason: "toolUse" }),
      fauxAssistantMessage("never reached"),
    ]);

    const deadline = armWallDeadline(root, 50, context);
    const submitted = await root.submit({ type: "input", content: "implement the tick" }, context);
    const settled = await submitted.wait(context);

    // The wall fired mid-tool: the conversation aborted, the submission
    // settled unanswered, and the model was never asked anything after it.
    expect(settled.status).toBe("unanswered");
    expect(faux.state.callCount).toBe(1);
    deadline.cancel();
    await harness.close(context);
  });

  it("a deadline canceled after a settled run never aborts the next submission", async () => {
    const env = new FakeWorkerEnv({
      cwd: repoDir,
      execScript: [{ exitCode: 0, output: "ok: the report passes the check\n" }],
    });
    // The agent already wrote a passing report, so each submission settles
    // at its own first yield: what is under test is the deadline, not the
    // follow-ups.
    env.writeFileDirect(reportPath, "# pom\n\nI did the thing.\n\nSTATUS: DONE\n");
    const { harness, root, faux } = await workerHarness(env, [slowTool(50)]);
    recordResponses(faux, [
      fauxAssistantMessage([fauxToolCall("slow", {})], { stopReason: "toolUse" }),
      fauxAssistantMessage("first done"),
      fauxAssistantMessage("second done"),
    ]);

    const deadline = armWallDeadline(root, 60_000, context);
    const first = await (await root.submit({ type: "input", content: "first" }, context)).wait(
      context,
    );
    expect(first.status).toBe("done");
    // Disarmed: the timer would otherwise outlive the settled run and abort
    // whatever the host submits next.
    deadline.cancel();

    const second = await (await root.submit({ type: "input", content: "second" }, context)).wait(
      context,
    );
    expect(second.status).toBe("done");
    expect(faux.state.callCount).toBe(3);
    await harness.close(context);
  });
});

// The strings both halves of the contract share, spelled once and pinned.
describe("the follow-up texts and the checker's command", () => {
  it("nudges with the shell's own words: the headless line, the branch, the report path", () => {
    const message = workerNudgeMessage({ reportPath, repoDir, branch, tick });
    expect(message).toContain(WORKER_HEADLESS_LINE);
    expect(message).toContain(branch);
    expect(message).toContain(reportPath);
    // The bounds the shell holds the loop to are this module's defaults.
    expect(WORKER_MAX_NUDGES).toBe(2);
    expect(WORKER_MAX_PUSHBACKS).toBe(2);
  });

  it("composes the checker's command the way the container composes it", () => {
    const command = workerReportCheckCommand({
      reportPath,
      repoDir,
      branch,
      tick,
    });
    expect(command).toBe(
      `${shellQuote(WORKER_REPORT_CHECKER)} lint-report ${shellQuote(reportPath)} --pushback --tick ${shellQuote(tick)} --repo ${shellQuote(repoDir)}`,
    );
    const withRole = workerReportCheckCommand({
      reportPath,
      repoDir,
      branch,
      tick,
      role: "implement-tick",
    });
    expect(withRole).toBe(`${command} --role ${shellQuote("implement-tick")}`);
  });

  it("quotes a shell word the way a command line needs", () => {
    expect(shellQuote("plain")).toBe("'plain'");
    expect(shellQuote("it's")).toBe("'it'\\''s'");
  });
});
