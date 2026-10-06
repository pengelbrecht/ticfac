import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  createModels,
  type FauxResponseFactory,
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
  type Message,
} from "@earendil-works/pi-ai";
import {
  createRegistry,
  defineExtension,
  Harness,
  MemoryStorage,
} from "@earendil-works/pi-durable";
import { describe, expect, it } from "vitest";
import { FactorySandboxEnv } from "../src/env/factory-sandbox.js";
import { createTrackedBashTool } from "../src/tools/tracked-bash.js";
import { fakeSandboxDoor, waitFor } from "./sandbox-door-helper.js";

/**
 * Kill mid-bash, then resume: the tick's acceptance test (kgk).
 *
 * The prototype's experiment 3 (docs/spikes/n0b-round2-pi-durable.md),
 * reproduced on the REAL machinery: a full pi-durable Harness whose bash
 * tool is the tracked one, driving a sandbox door. The harness is killed
 * mid-bash — modelled as exactly what a killed process looks like: a door
 * call that never returns, and no cleanup — and a SECOND harness opens the
 * same storage and recovers. The recovery replays the tool (it is `replay:
 * "safe"`), the prepare reads the SAME nonce from the durable memo, and the
 * env REATTACHES to the process the dead harness left running: the command
 * runs exactly once, and the turn completes with its output.
 *
 * What is real and what stands in: the Harness, its recovery, the memo, the
 * tool and the env are production code. The door is a stand-in FactorySandbox
 * that answers with the same shapes (see ./sandbox-door-helper.ts); the
 * door's own behaviour is pinned by cloudflare/test/factory-sandbox.test.ts,
 * and the env's commands are proven against real bash by the node suite.
 */

/** The text blocks of a message, whatever shape its content takes. */
function textOf(message: Message): string[] {
  if (typeof message.content === "string") return [message.content];
  return message.content.flatMap((block) => (block.type === "text" ? [block.text] : []));
}

describe("the tracked bash survives a harness killed mid-bash", () => {
  // A full-Harness test (tick fim): the opens sit in this body, but the old
  // guard never saw the file — it scanned test/node only. The workerd pool
  // runs its files beside each other, so the wall clock grows with pool
  // contention: the kjs 120s bound, not the 30s quiet-host default.
  it("resumes in a new process, reattaches, and runs the command exactly once", {
    timeout: 120_000,
  }, async () => {
    const context = BACKGROUND_CONTEXT;

    // The container command, as the stand-in door times it: it prints its
    // marker and finishes — slower than the crash that follows, so the
    // replay reattaches to a process that is STILL running.
    const door = fakeSandboxDoor({ commandMs: 700, commandOutput: "finished\n" });
    const env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      guardDir: null,
      pollMs: 10,
    });

    const faux = fauxProvider();
    const responses: FauxResponseFactory[] = [
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "sleep 0.7; echo finished" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("the turn completed after recovery"),
    ];
    faux.setResponses(responses);
    const models = createModels();
    models.setProvider(faux.provider);

    const registry = createRegistry();
    registry.install(defineExtension({ name: "tracked", tools: [createTrackedBashTool()] }));

    const storage = new MemoryStorage();

    // Harness 1: the process that dies. Never awaited to settlement — it has
    // no settlement to reach.
    const h1 = await Harness.open(storage, { models, registry, env: () => env }, context);
    const root1 = await h1.root(context, {
      agent: { model: { provider: "faux", modelId: "faux-1" } },
    });
    void root1.submit({ type: "input", content: "run the command" }, context);

    // The command has started, the harness is mid-tool, and the process is
    // killed: its door call never answers, and no cleanup runs.
    await waitFor("the tracked command to start", () => door.starts.length === 1);
    door.die();
    // Wait for the dead harness to be truly parked: one of its poll calls
    // is issued after the kill and never answers, so it cannot race the
    // resumed harness once the door thaws.
    await waitFor("the dead harness to be parked on a dead call", () => door.deadCalls >= 1);
    // A deploy restarts the Durable Object between the death and the
    // replay (tick 2oa): the container and its process live on, the
    // object's in-memory ready flag is lost. This env runs guardDir null,
    // so no `run` comes before the replay's list — the reattach must not
    // depend on one having marked the object ready.
    door.restartObject();

    // Harness 2: a new process, the same storage. Opening it recovers the
    // unfinished tool task; the replay runs prepare, which reads the
    // memoised nonce, and the env reattaches instead of starting again.
    const h2 = await Harness.open(
      storage,
      {
        models,
        registry,
        env: (_target, _buildContext) => {
          door.thaw();
          return env;
        },
      },
      context,
    );
    const root2 = await h2.conversation(root1.id, context);
    if (root2 === undefined) throw new Error("the resumed conversation was lost");
    await root2.waitForIdle(context);

    // The command ran exactly once: one start across BOTH harnesses, and
    // zero kills — the crash left the process alone, and the replay found it.
    expect(door.starts.length).toBe(1);
    expect(door.kills.length).toBe(0);

    // The turn completed: the tool result carries the reattached process's
    // output, and the final assistant message is the second faux response.
    const view = await root2.context(context);
    const last = view.messages.at(-1) as Message;
    expect(last.role).toBe("assistant");
    expect(textOf(last).join(" ")).toContain("the turn completed after recovery");
    const toolResults = view.messages.filter((m) => m.role === "toolResult");
    expect(toolResults.length).toBe(1);
    expect(textOf(toolResults[0] as Message).join(" ")).toContain("finished");

    // The dead harness's pending call stays dead: no timer, no wake, no
    // race with the closed one.
    await h2.close(context);
  });
});
