/**
 * ncr: the refusal half of `pnpm lint`.
 *
 * Biome is this side's gofmt AND its vet, and `pnpm lint` — `biome ci
 * --error-on-warnings` — is what the gate's ts command and CI's typescript
 * job both run. The acceptance that added it names a proof, not just a
 * spellcheck: the format check and the lint check must both FAIL on a
 * deliberately bad file. Until this suite that proof was a probe a worker ran
 * by hand — the commit that added biome.jsonc says of noFloatingPromises "it
 * was also checked that it CAN fire" — and evidence that leaves with the
 * terminal it ran in is evidence nobody can re-run. This is that probe,
 * committed, so the gate re-runs it on every tick and CI on every push.
 *
 * Every case breaks a THROWAWAY tree and asserts the REAL binary, under the
 * REAL biome.jsonc, refuses it. The config is copied from this repository
 * rather than restated here: a test config would prove that some config can
 * refuse, not that the one the gate runs does. The tree gets a `git init`
 * because the real config asks for one — `vcs.enabled` — and without a
 * repository Biome exits on a configuration error and checks nothing
 * (measured at 2.5.13: `internalError/fs`, exit 1, zero files checked). A
 * config the test could not even load would pass every refusal case vacuously
 * as a non-zero exit, which is why each case also matches the diagnostic it
 * expects: a refusal for the wrong reason is a failure, not a pass.
 *
 * Runs under plain `node --test` like contracts.test.mjs, on purpose: the
 * factory's vitest suite executes inside workerd, which has no filesystem to
 * build a throwaway tree in. `pnpm lint` needs this repository's own
 * node_modules (the biome binary), so the suite runs where install has
 * already happened: the gate's ts command, after `pnpm install` and beside
 * the `pnpm lint` it is evidence for, and CI's typescript job through
 * `pnpm test`.
 */

import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

const FACTORY_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");
// The launcher is a Node script that finds the platform binary, so the same
// path resolves on every platform pnpm installs @biomejs/biome for.
const BIOME = join(FACTORY_DIR, "node_modules", "@biomejs", "biome", "bin", "biome");
const CONFIG = join(FACTORY_DIR, "biome.jsonc");

const trees = [];
after(() => {
  for (const tree of trees) {
    rmSync(tree, { recursive: true, force: true });
  }
});

// biome runs the real binary in a tree and settles with its exit code and
// merged output. A spawn failure settles with code null and the error in the
// output, so a missing binary fails every case on its own assertion rather
// than looking like a pass. ANSI and hyperlink escapes are stripped before
// the output is matched: Biome colorizes counts mid-word ("Found \u001b[0m1\u001b[0m
// warning."), and a case that pattern-matched the color would pass on one
// terminal and fail on another for no reason either of them caused.
function biome(cwd, args) {
  return new Promise((done) => {
    execFile(process.execPath, [BIOME, ...args], { cwd }, (error, stdout, stderr) => {
      const code = error === null ? 0 : typeof error.code === "number" ? error.code : null;
      const raw = `${stdout}${stderr}${error === null ? "" : String(error)}`;
      done({
        code,
        output: raw
          .replace(/\u001B\][^\u001B]*\u001B\\/g, "")
          .replace(/\u001B\[[0-9;]*[A-Za-z]/g, ""),
      });
    });
  });
}

// makeTree builds what `pnpm lint` runs against: this repository's
// biome.jsonc and whatever deliberately bad files a case names. Nothing is
// written into the factory itself — a gate must never leave a bad file in
// the tree it is passing, and two gates may share a checkout.
function makeTree(files) {
  const tree = mkdtempSync(join(tmpdir(), "biome-refusals-"));
  trees.push(tree);
  cpSync(CONFIG, join(tree, "biome.jsonc"));
  for (const [name, content] of Object.entries(files)) {
    const path = join(tree, name);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, content);
  }
  // The config's vcs integration needs a repository; --quiet keeps the
  // default-branch hint out of a gate's log.
  execFileSync("git", ["init", "--quiet", "."], { cwd: tree, stdio: "ignore" });
  return tree;
}

// A file that is formatted and lint-clean under the real config. The other
// four probes each break exactly one half, so their refusals attribute.
const FINE = `export function fine(name: string): string {
  return \`hello \${name}\`;
}
`;

// Badly formatted and nothing else: one tab where the config says two spaces,
// no quotes, no lint findings — so only the formatter half can refuse it.
// Tabs are also what a DROPPED config would not catch Biome's own defaults
// disagreeing with (default indentStyle is space too, measured), which is
// why the config-is-in-force claim is carried by the nursery rule below and
// not by this case.
const MISFORMATTED = `export function indented(): string {
\treturn "one tab, where this config says two spaces";
}
`;

// Lint-bad and nothing else: formatted clean, one floating promise. A
// dropped promise in a Workers runtime is a request that returns before its
// own writes land — the bug class the tick that added Biome named, and the
// reason noFloatingPromises is in the config by hand: it is NURSERY, so
// `recommended` does not carry it and neither do Biome's defaults. This case
// is therefore also the proof the config, not a fallback, is in force.
const FLOATING = `function returnsPromise(): Promise<void> {
  return Promise.resolve();
}

returnsPromise();
`;

// Lint-bad at WARNING severity: an unused variable, one of the findings
// `tsc --noEmit` cannot make at all. Warning severity is the whole point of
// --error-on-warnings — plain \`biome ci\` exits 0 with this still on the
// floor — so this case runs both spellings and refuses the one the gate
// uses being any softer than the claim in .tick/runners.toml.
const UNUSED = `export function used(): number {
  const unused = 41;
  return 1;
}
`;

test("a formatted, lint-clean file passes", async () => {
  const run = await biome(makeTree({ "src/fine.ts": FINE }), ["ci", "--error-on-warnings"]);
  assert.equal(
    run.code,
    0,
    `the clean control must pass: if the real config refuses this, every refusal case\nbelow is a check that fails everything, which proves nothing.\n${run.output}`,
  );
  assert.doesNotMatch(
    run.output,
    /Found [1-9][\d,]* (error|warning)/,
    `the "clean" probe was not clean — a check whose control case fails cannot be\ntrusted to attribute any refusal below:\n${run.output}`,
  );
});

test("the format check refuses a deliberately mis-formatted file", async () => {
  const run = await biome(makeTree({ "src/format-probe.ts": MISFORMATTED }), [
    "ci",
    "--error-on-warnings",
  ]);
  assert.notEqual(
    run.code,
    0,
    `a tab-indented module must fail the gate. The formatter is this side's gofmt —\n` +
      `the half tsc never had — and a formatter that cannot refuse is not one.\n${run.output}`,
  );
  assert.match(
    run.output,
    /format-probe\.ts/,
    `the refusal does not name the probe file:\n${run.output}`,
  );
  assert.match(
    run.output,
    /File content differs from formatting output/,
    `the refusal is not the formatter's own message — some other check failed, and\n` +
      `the exit code alone would have hidden that:\n${run.output}`,
  );
});

test("the lint check refuses a floating promise", async () => {
  const run = await biome(makeTree({ "src/floating-probe.ts": FLOATING }), [
    "ci",
    "--error-on-warnings",
  ]);
  assert.notEqual(
    run.code,
    0,
    `a dropped promise must fail the gate: in a Workers runtime it is a request that\n` +
      `returns before its own writes land, the bug class this tool was added to catch.\n${run.output}`,
  );
  assert.match(
    run.output,
    /lint\/nursery\/noFloatingPromises/,
    `the refusal is not noFloatingPromises. That rule is nursery, so neither the\n` +
      `\`recommended\` preset nor Biome's built-in defaults carry it: if it did not fire,\n` +
      `the config in force here is not biome.jsonc — the exact hazard its own header\n` +
      `warns about.\n${run.output}`,
  );
});

test("--error-on-warnings is what refuses warning-severity findings", async () => {
  const tree = makeTree({ "src/warning-probe.ts": UNUSED });
  const plain = await biome(tree, ["ci"]);
  assert.equal(
    plain.code,
    0,
    `plain \`biome ci\` is expected to exit 0 here — warnings on the floor, which is\n` +
      `the measured reason the gate's copy carries the flag. If plain ci starts\n` +
      `refusing warnings, this case says so before the flag's claim rots.\n${plain.output}`,
  );
  assert.match(
    plain.output,
    /Found 1 warning\./,
    `expected exactly the one warning probe:\n${plain.output}`,
  );

  const flagged = await biome(tree, ["ci", "--error-on-warnings"]);
  assert.notEqual(
    flagged.code,
    0,
    `the gate's spelling must refuse an unused variable. Most of what Biome found in\n` +
      `this tree on arrival — the unused values, the implicit anys — is warning\n` +
      `severity; without this refusal the check cannot catch what it was added to.\n${flagged.output}`,
  );
  assert.match(
    flagged.output,
    /lint\/correctness\/noUnusedVariables/,
    `the refusal is not noUnusedVariables, so it is not the probe that was refused:\n${flagged.output}`,
  );
});

test("a file the config's includes never named is not checked", async () => {
  // MISFORMATTED again, at the tree root: biome.jsonc scopes the checks to
  // src/**, test/**, scripts/** and vitest.config.ts, and that scope is a
  // protection, not a detail — the pinned JSON in this directory
  // (contracts.pin.json) is out of it on purpose, because a formatter that
  // rewrites a pin has changed the evidence rather than tidied it.
  const run = await biome(makeTree({ "outside.ts": MISFORMATTED }), ["ci", "--error-on-warnings"]);
  assert.equal(
    run.code,
    0,
    `a badly formatted file outside biome.jsonc's \`files.includes\` must not fail the\n` +
      `gate. If it does, the scope has widened to files the config never named — and\n` +
      `the pins stop being pins.\n${run.output}`,
  );
  assert.doesNotMatch(
    run.output,
    /File content differs from formatting output/,
    `the out-of-scope file was formatted-checked anyway:\n${run.output}`,
  );
});
