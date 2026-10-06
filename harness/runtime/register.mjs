/**
 * The runtime shim that lets the harness package's TypeScript sources run
 * under plain `node` with no build step (epic 43y step 7, tick hpk).
 *
 * The local worker host (`src/local/main.ts`) is spawned by the Go executor
 * as:
 *
 *     node --experimental-strip-types \
 *          --import <harness>/runtime/register.mjs \
 *          <harness>/src/local/main.ts --config <stateDir>/worker.json
 *
 * Node's type stripping (flagged on Node 22, default from 23.6) compiles a
 * `.ts` file at load time — but it does NOT rewrite import specifiers, and
 * this package's sources import each other the way `tsc` wants them written:
 * with `.js` extensions (the convention the workerd and node vitest suites
 * compile through esbuild). The hook in `js-to-ts-hooks.mjs` closes that gap
 * the same way Earendil's own published packages do at build time: a relative
 * `./x.js` whose `./x.ts` exists resolves to the `.ts` file, and everything
 * else resolves as normal. It never touches `node:` builtins or package
 * imports (`@earendil-works/…` ships compiled `dist/*.js`), so the rewrite
 * can only ever apply to this package's own sources.
 *
 * `register.mjs` exists only to hold the `--import` entry: `module.register`
 * takes a MODULE, not a hooks object, so the hooks live beside it in
 * `js-to-ts-hooks.mjs` and this file points at them by URL so it works from
 * any working directory.
 */
import { register } from "node:module";

register(new URL("./js-to-ts-hooks.mjs", import.meta.url));
