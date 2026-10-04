import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  type AssistantMessage,
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
  Harness,
  MemoryStorage,
} from "@earendil-works/pi-durable";
import { describe, expect, it } from "vitest";

/** The text blocks of a message, whatever shape its content takes. */
function textOf(message: Message): string[] {
  if (typeof message.content === "string") {
    return [message.content];
  }
  return message.content.flatMap((block) => (block.type === "text" ? [block.text] : []));
}

/**
 * The faux-provider transcript replay test (tick 92k).
 *
 * The harness package's whole reason for existing is that pi-durable and
 * pi-ai churn ("the API changes without notice", three days old at adoption —
 * docs/spikes/n0b-round2-pi-durable.md). This test drives the FULL stack the
 * epic builds on — pi-ai `Models` and transcript, the built-in generation and
 * tool tasks, tool registration, a scripted two-turn conversation — against
 * pi-ai's faux provider, with no network and no credentials. When the pinned
 * API moves, this fails in CI instead of in a run.
 *
 * It runs inside workerd (the whole suite does), which also keeps the
 * runtime-neutral claim of the spike honest: the same harness code that will
 * run in a cloud DO runs under the worker runtime here.
 */
describe("faux-provider transcript replay", () => {
  it("answers through a tool call and replays the transcript to the provider", async () => {
    const context = BACKGROUND_CONTEXT;
    const echoed: string[] = [];

    const echo = defineTool({
      name: "echo",
      description: "Echo text back as the tool result.",
      parameters: Type.Object({ text: Type.String() }),
      replay: "safe",
      async execute(args) {
        echoed.push(args.text);
        return { content: [{ type: "text", text: args.text }] };
      },
    });

    // Every request the provider sees, captured so the test can assert
    // what the harness REPLAYED, not just what it returned: the second
    // request must carry the first exchange's user message and the tool
    // result, or the model would answer without its own conversation.
    const seen: Message[][] = [];
    const recordAnd =
      (message: AssistantMessage): FauxResponseFactory =>
      (transcript) => {
        seen.push([...transcript.messages]);
        return message;
      };

    const faux = fauxProvider();
    faux.setResponses([
      recordAnd(
        fauxAssistantMessage([fauxToolCall("echo", { text: "heard" })], { stopReason: "toolUse" }),
      ),
      recordAnd(fauxAssistantMessage("echoed: heard")),
    ]);

    const models = createModels();
    models.setProvider(faux.provider);

    const storage = new MemoryStorage();
    const registry = createRegistry();
    registry.install(defineExtension({ name: "echo", tools: [echo] }));

    const harness = await Harness.open(storage, { models, registry }, context);
    const root = await harness.root(context, {
      agent: { model: { provider: "faux", modelId: "faux-1" } },
    });

    const submission = await root.submit(
      { type: "input", content: "say heard through the echo tool" },
      context,
    );
    const settled = await submission.wait(context);
    expect(settled.status).toBe("done");
    expect(echoed).toEqual(["heard"]);
    expect(faux.state.callCount).toBe(2);

    // The provider saw two requests, and the second one carried the whole
    // first exchange: this is the transcript replay the harness owes the
    // provider on every turn.
    expect(seen.length).toBe(2);
    const second = seen[1] ?? [];
    expect(second.some((m) => m.role === "user")).toBe(true);
    expect(second.some((m) => m.role === "toolResult" && textOf(m).includes("heard"))).toBe(true);

    // The settled answer is the final assistant message of the conversation.
    const view = await root.context(context);
    const last = view.messages.at(-1);
    expect(last?.role).toBe("assistant");
    expect(textOf(last as Message).includes("echoed: heard")).toBe(true);

    // NOTE: this test does not prove crash RESUME — pi-durable's reopen path
    // needs storage that survives a close (DO SQLite in the cloud host, epic
    // step 6; local SQLite, step 7). What it proves is the transcript replay
    // on every turn: the harness owes the provider its own conversation, and
    // that is the API surface that must fail CI when it churns.
    await harness.close(context);
  });
});
