import { expect, it } from "vitest";

/**
 * Tick p0n's FIRST question, answered and PINNED: does Hegel's WebAssembly
 * build (the browser entry, the build v0.4.7 adds) load under workerd, the
 * runtime `@cloudflare/vitest-pool-workers` runs this suite in?
 *
 * **No.** Under workerd the package's resolution conditions are
 * ["development", "workerd", "worker", "module", "browser", "import"] — "node"
 * is absent and "browser" is present, so the ROOT `@hegeldev/hegel` import
 * resolves through the exports map to `dist/browser/index.js`, the WASM
 * build. That build's top level awaits
 * `fetch(new URL("./libhegel-wasm32-unknown-unknown.wasm", import.meta.url))`,
 * and inside this pool `import.meta.url` is the module's real `file://`
 * path — workerd's `fetch` only speaks http(s), so module loading dies with
 * "Fetch API cannot load: file:///….wasm" wrapped in
 * `EngineError: Failed to load Hegel Wasm engine`. Nothing in this repo can
 * fix that from the outside: the URL is built inside the package, and the
 * pool serves no HTTP host the package could fetch the engine from.
 *
 * So the Hegel property tests run under PLAIN-Node vitest, over pure
 * modules — `vitest.config.hegel.ts`, the leg `pnpm test` runs before this
 * one. There the "node" condition picks the native koffi engine, which loads
 * and shrinks.
 *
 * This test asserts the failure itself, not merely a comment: the day Hegel
 * (or the pool) changes so the WASM build loads under workerd, this flips
 * red and the fallback above has to be re-decided rather than silently
 * staying on. The answer is a fact about two third parties, so it is
 * checked, not assumed — the same reason `bindings.test.ts` exists.
 */
it("does NOT load Hegel's WebAssembly build under workerd (tick p0n's answer)", async () => {
  let failure: unknown;
  try {
    await import("@hegeldev/hegel");
  } catch (error) {
    failure = error;
  }
  // The import has to fail LOADING THE ENGINE, not for some unrelated
  // reason (a typo in the specifier, a broken install): pin the mechanism.
  expect(failure, "the WASM build must not load under workerd").toBeInstanceOf(Error);
  const message = String((failure as { message?: unknown })?.message ?? failure);
  expect(message).toContain("Failed to load Hegel Wasm engine");
  const cause = String(
    ((failure as { cause?: unknown })?.cause as { message?: unknown })?.message ?? "",
  );
  expect(cause).toContain("Fetch API cannot load");
  expect(cause).toContain(".wasm");
});
