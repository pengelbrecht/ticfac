/**
 * The seeding half of the Go end-to-end door harness (tick 6gr): re-exports
 * the worker's OWN D1 accessors and credential minter, so `run.mjs` seeds the
 * factory's D1 index through the same functions the deployed Worker reads it
 * through — never through a second spelling of the inserts that could drift
 * from `src/db.ts` unnoticed.
 *
 * `run.mjs` bundles this file for Node with esbuild and imports the result;
 * the modules are pure over `D1Database` (`src/db.ts` takes the binding, not
 * the environment), so they run unmodified outside workerd.
 */
export { insertRun } from "../../src/db";
export { issueWorkerRunToken } from "../../src/gateway";
