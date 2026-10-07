import { configDefaults, defineConfig } from "vitest/config";

/**
 * The plain-Node vitest leg (tick p0n).
 *
 * The main config (`vitest.config.ts`) runs the suite inside real workerd
 * through @cloudflare/vitest-pool-workers; that is where every test that needs
 * a binding, D1, R2, a Durable Object or a Workflow belongs, and it stays the
 * default `vitest run`. This config is the OTHER leg the Hegel trial needed:
 *
 * - Hegel's WebAssembly build does NOT load under workerd —
 *   `test/hegel-probe.test.ts` pins the mechanism — because the browser entry
 *   it resolves to fetches its own `.wasm` from a `file://` URL, which
 *   workerd's `fetch` refuses. Under plain Node the package's "node" export
 *   condition picks the native koffi engine, which loads and shrinks.
 * - Property tests over PURE modules therefore run here: no cloudflare plugin,
 *   no bindings, no setup files, no watchdog. Anything a property test in this
 *   leg imports must be importable without `cloudflare:workers` in its chain —
 *   that constraint is itself a design pressure this tick pays attention to
 *   (the supervision loop's decisions moved to src/run-watch.ts for exactly
 *   this reason), and any module that fails it is a module whose decisions are
 *   entangled with the platform.
 *
 * The include is the property directory alone, and vitest.config.ts excludes
 * the same directory from its workerd include, so a file here can never run
 * under the wrong runtime: importing hegel there would fail the file at load,
 * and a test needing bindings would fail here at first use.
 */
export default defineConfig({
  test: {
    include: ["test/property/**/*.test.ts"],
    exclude: [...configDefaults.exclude],
    environment: "node",
    // The workerd leg's 30 s budget is a laptop number for real container
    // churn; this leg is pure functions, but shrinking a counterexample can
    // still take a moment under CI load, so it gets the same headroom.
    testTimeout: 30_000,
  },
});
