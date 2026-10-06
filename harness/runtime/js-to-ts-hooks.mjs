/**
 * The resolve hook `register.mjs` installs — see that file for why the local
 * worker host needs it. Kept in its own module because `module.register`
 * takes a module URL, not a hooks object.
 *
 * One rule, applied only to RELATIVE specifiers: `./x.js` (or `../x.js`)
 * becomes `./x.ts` when that file exists. A relative `.js` import inside
 * this package is always one of its own sources under esbuild's convention;
 * a specifier that resolves elsewhere — a real `.js` file, a package, a
 * builtin — is left exactly as it was.
 */
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

export function resolve(specifier, context, nextResolve) {
  if ((specifier.startsWith("./") || specifier.startsWith("../")) && specifier.endsWith(".js")) {
    try {
      const ts = new URL(`${specifier.slice(0, -".js".length)}.ts`, context.parentURL);
      if (existsSync(fileURLToPath(ts))) {
        return { url: ts.href, shortCircuit: true };
      }
    } catch {
      // A specifier that is not a valid URL against its parent resolves the
      // ordinary way and fails with node's own error.
    }
  }
  return nextResolve(specifier, context);
}
