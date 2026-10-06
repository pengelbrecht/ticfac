/**
 * Bindings declared in wrangler.toml.
 *
 * The same hand-written shape as cloudflare/src/env.d.ts, so the test harness
 * sees exactly the worker's bindings without a generated file.
 */
declare namespace Cloudflare {
  interface Env {
    /**
     * One HarnessStorage DO instance per conformance case in this package's
     * CI suite (a fresh instance is a fresh DO SQLite database, the isolation
     * pi-durable's conformance cases assume), and later in the epic one per
     * worker attempt.
     */
    HARNESS_STORAGE: DurableObjectNamespace<import("./worker").HarnessStorage>;
  }
}
