/**
 * ogv: operator-facing strings name THIS binary's commands. The factory move
 * ported ticks' text verbatim and never repointed the pointer, so failure
 * details kept telling operators to run `tk factory setup` and
 * `tk factory deploy` — commands ticks' pwp deleted from tk and ticfac serves
 * as its own `ticfac factory` family. An operator who followed one ran a
 * binary this repository does not ship and cleared nothing.
 *
 * The guard scans every string-bearing literal in cloudflare/src — the
 * details a run's failures, digests and findings carry to the operator — and
 * refuses a pointer at those tk subcommand families, so the next verbatim
 * port fails here rather than in front of an operator. It reads literals
 * through the TypeScript parser, not bytes: the doc comments may name tk (the
 * historical narratives of what tk once served), and `tk herd spawn` is
 * deliberately not matched, because the branch-namespace registry names it as
 * an ACTOR on an operator's laptop (who else creates tick/* branches), not as
 * a command to run.
 *
 * Runs under plain `node --test` like contracts.test.mjs, on purpose: the
 * vitest suite executes inside workerd, which has no filesystem to read
 * cloudflare/src from.
 */

import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const FACTORY_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SRC_DIR = join(FACTORY_DIR, "src");

/** Matches, inside a literal's VALUE, a pointer at a tk command this repository serves as ticfac. */
const pointer = /tk (factory|cloud)\b/;

test("no operator-facing string in the factory points at the ticks CLI", () => {
  const files = readdirSync(SRC_DIR).filter((name) => name.endsWith(".ts"));
  assert.ok(
    files.length > 0,
    "found no TypeScript sources to guard — the scan cannot be silently empty",
  );

  const offenders = [];
  for (const name of files) {
    const text = readFileSync(join(SRC_DIR, name), "utf8");
    const source = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true);
    const literals = [];
    const collect = (node) => {
      if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
        literals.push(node.text);
      } else if (ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) {
        literals.push(node.text);
      }
      node.forEachChild(collect);
    };
    source.forEachChild(collect);
    for (const literal of literals) {
      if (pointer.test(literal)) {
        offenders.push(`${name}: ${JSON.stringify(literal)}`);
      }
    }
  }
  assert.deepEqual(
    offenders,
    [],
    "operator-facing strings still point at 'tk factory/cloud' commands this binary does not serve",
  );
});
