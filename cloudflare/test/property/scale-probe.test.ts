import { expect, it } from "vitest";
import { declaredMaxParallel, declaredSandboxImage } from "../../src/repo-config";
import { watchLook, watchOut, watchStart } from "../../src/run-watch";
import { parseToml, TomlParseError } from "../../src/toml";

/**
 * The scale probe (hegel-skill techniques/scale.md), kept as a test so it
 * runs with the rest: drive the machine and the parsers at sizes the
 * generators never reach — the REAL look budget, a config at the byte bound,
 * and a deeply hostile TOML document — and assert linear behaviour and the
 * typed refusals rather than timing budgets (timings are recorded in the
 * tick's report, not asserted here).
 */
it("the watch holds through a real-sized look budget without recursion or slowdown", () => {
  const config = { max_observations: 10_000, settle_ms: 30_000, settle_look_ms: 10_000 };
  let state = watchStart(0);
  const look = { process: "running" as const, exit_code: null, trip: null, at_ms: 0 };
  const t0 = performance.now();
  let steps = 0;
  while (!watchOut(state, config)) {
    const decision = watchLook(state, look, config);
    if (decision.kind !== "hold") throw new Error(`scale probe: ${decision.kind}`);
    state = decision.state;
    steps++;
  }
  const ms = performance.now() - t0;
  // 10,000 folds in well under a second is linear; the property draws budgets
  // of at most 6, so this is the only place the real number is reached.
  expect(steps).toBe(10_000);
  expect(ms).toBeLessThan(1_000);
});

it("a tracked config at the byte bound parses and reads its declarations", () => {
  // One declaration, padded past the reader's own byte bound with comment
  // lines — whole lines, so the padding is parseable at every size.
  const padding = `# ${"x".repeat(120)}\n`.repeat(2_000);
  const source = `[sandbox]\nimage = "ticks-orchestrator:0.32.0"\n${padding}`;
  expect(declaredSandboxImage(source)).toBe("ticks-orchestrator:0.32.0");
  expect(declaredMaxParallel(source)).toBeNull();
});

it("a deeply hostile TOML document is refused as a TomlParseError, never a crash", () => {
  expect(() => parseToml("[".repeat(50_000))).toThrow(TomlParseError);
  expect(() => parseToml("=".repeat(50_000))).toThrow(TomlParseError);
  const brackets = "[".repeat(25_000);
  expect(() => parseToml(`${brackets}]`)).toThrow(TomlParseError);
});
