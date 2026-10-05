import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

/**
 * The timeout discipline of this suite's full-Harness tests (tick kjs) and
 * full-worker tests (tick 7wg).
 *
 * The node config's 30_000 default is a quiet-host number. The tests that
 * open the full pi-durable Harness over the local door drive real git and
 * real processes — a clone, a boot, a wip push per tool round, a restore that
 * fetches, checks out and re-runs setup — and their wall clock grows with
 * host load: while `make gate` ran beside this suite, one of them took 33.5 s
 * against the 30 s default and the run recorded a failure that was the
 * host's, not the tree's. A test whose runtime is load-dependent may not
 * borrow a quiet-host bound, so each one states its own: at least
 * FULL_HARNESS_TIMEOUT_MS, 4x the default — room for a loaded host, still a
 * bound a genuinely hung test fails inside.
 *
 * Tick 7wg extended the same discipline to the full-WORKER tests of
 * local-host.test.ts: those spawn a whole worker child process — the real
 * `src/local/main.ts` entry, the way the Go supervisor launches it — and
 * wait on wall-clock conditions of that child's own progress (a steer
 * socket that appears, a file a running tool writes), which a loaded shared
 * host starves. Observed twice in one night at load average 23-47: one of
 * them failed `timed out waiting for the tool to be running` after 1040 s.
 * A full-worker test does strictly more than a full-Harness one — a process
 * spawn and boot on top of everything the in-process Harness tests drive —
 * so its bound is FULL_WORKER_TIMEOUT_MS, 10x the default. Both are enforced
 * rather than remembered, the same way internal/shorttest keeps the Go
 * gate's short-suite discipline: a full-Harness or full-worker test added
 * next month without its own bound fails here, not in a run.
 */

/** Where this suite lives, whatever checkout it runs in. */
const HERE = dirname(fileURLToPath(import.meta.url));

/** The wall clock a full-Harness test must declare for itself, in ms. */
const FULL_HARNESS_TIMEOUT_MS = 120_000;

/** The wall clock a full-worker test must declare for itself, in ms. */
const FULL_WORKER_TIMEOUT_MS = 300_000;

/** The per-test timeout one `it(...)` declared, or undefined for none. */
type DeclaredTimeout = number | undefined;

/** Why a test is bound: it opens a whole Harness, or launches a whole worker. */
type GuardedKind = "harness" | "worker";

type GuardedTest = { file: string; title: string; kind: GuardedKind; timeout: DeclaredTimeout };

/** Whether the node's subtree calls `Harness.open` at any depth. */
function opensHarness(root: ts.Node): boolean {
  let opens = false;
  const walk = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression) &&
      ts.isIdentifier(node.expression.expression) &&
      node.expression.expression.text === "Harness" &&
      node.expression.name.text === "open"
    ) {
      opens = true;
    }
    node.forEachChild(walk);
  };
  walk(root);
  return opens;
}

/**
 * Whether the node's subtree launches the worker child: this suite's
 * fixture `run` — a `spawn` of the real local-worker entry as a separate
 * process, the way the Go supervisor launches it. A whole worker carries a
 * whole Harness inside it, so the full-Harness bound is not enough for one
 * of these; the receiver is whatever identifier holds the fixture (today
 * `f.run(...)` in local-host.test.ts).
 */
function launchesWorker(root: ts.Node): boolean {
  let launches = false;
  const walk = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression) &&
      ts.isIdentifier(node.expression.expression) &&
      node.expression.name.text === "run"
    ) {
      launches = true;
    }
    node.forEachChild(walk);
  };
  walk(root);
  return launches;
}

/**
 * The timeout an `it(...)` call declares, wherever vitest accepts it: as the
 * third argument — a bare number, or `{ timeout: <ms> }` — or as an options
 * object placed between the title and the test function.
 */
function declaredTimeout(
  title: ts.Expression,
  arguments_: readonly ts.Expression[],
): DeclaredTimeout {
  const timeoutOf = (argument: ts.Expression | undefined): DeclaredTimeout => {
    if (argument && ts.isNumericLiteral(argument)) return Number(argument.text);
    if (argument && ts.isObjectLiteralExpression(argument)) {
      for (const property of argument.properties) {
        if (
          ts.isPropertyAssignment(property) &&
          ts.isIdentifier(property.name) &&
          property.name.text === "timeout" &&
          ts.isNumericLiteral(property.initializer)
        ) {
          return Number(property.initializer.text);
        }
      }
    }
    return undefined;
  };

  // The test function is the one arrow argument after the title; the options
  // may sit before it (it(name, options, fn)) or after (it(name, fn, N)).
  const fnIndex = arguments_.findIndex(
    (argument, index) => index > arguments_.indexOf(title) && ts.isArrowFunction(argument),
  );
  if (fnIndex < 0) return undefined;
  return (
    timeoutOf(arguments_[fnIndex - 1] !== title ? arguments_[fnIndex - 1] : undefined) ??
    timeoutOf(arguments_[fnIndex + 1])
  );
}

/**
 * Every `it(...)` in the source this guard binds, with why: a worker launch
 * binds the stricter worker bound even when the body also opens a Harness.
 */
function guardedTests(source: string): {
  title: string;
  kind: GuardedKind;
  timeout: DeclaredTimeout;
}[] {
  const file = ts.createSourceFile("node.test.ts", source, ts.ScriptTarget.Latest, true);
  const tests: { title: string; kind: GuardedKind; timeout: DeclaredTimeout }[] = [];
  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "it" &&
      node.arguments.length >= 2 &&
      ts.isStringLiteralLike(node.arguments[0])
    ) {
      const title = node.arguments[0];
      const testFn = node.arguments.find(
        (argument) => ts.isArrowFunction(argument) && argument.body,
      );
      if (testFn && ts.isArrowFunction(testFn)) {
        if (launchesWorker(testFn)) {
          tests.push({
            title: title.text,
            kind: "worker",
            timeout: declaredTimeout(title, node.arguments),
          });
        } else if (opensHarness(testFn)) {
          tests.push({
            title: title.text,
            kind: "harness",
            timeout: declaredTimeout(title, node.arguments),
          });
        }
      }
    }
    node.forEachChild(visit);
  };
  visit(file);
  return tests;
}

describe("the full-Harness and full-worker tests of this suite state their own wall clock", () => {
  const files = readdirSync(HERE)
    .filter((name) => name.endsWith(".test.ts"))
    .sort();
  const tests: GuardedTest[] = files.flatMap((name) =>
    guardedTests(readFileSync(join(HERE, name), "utf8")).map((test) => ({
      file: name,
      ...test,
    })),
  );
  const harnessTests = tests.filter((test) => test.kind === "harness");
  const workerTests = tests.filter((test) => test.kind === "worker");

  it("finds the tests it guards, so it cannot pass by reading nothing", () => {
    expect(files.length).toBeGreaterThan(0);
    // At least the two kjs names: the destroy tests over real git, whose
    // mid-turn sibling took 33.5 s under a concurrent `make gate`. Both gone
    // means this half of the guard binds nothing and needs a look, not a
    // green tick.
    expect(harnessTests.length).toBeGreaterThanOrEqual(2);
    // At least the six full-worker tests of local-host.test.ts, whose
    // spawn-and-wait shape starved past every quiet-host bound twice in one
    // night (tick 7wg) — including the two that night's failure log names.
    expect(workerTests.length).toBeGreaterThanOrEqual(6);
    const titles = new Set(workerTests.map((test) => test.title));
    expect(
      titles.has(
        "resumes from the storage after the harness is killed mid-tool, without re-running the tool",
      ),
    ).toBe(true);
    expect(
      titles.has("continues the SAME conversation when relaunched with a follow-up message"),
    ).toBe(true);
  });

  it("declares a timeout of at least 120_000 ms on every full-Harness test", () => {
    const underbound = harnessTests.filter(
      (test) => test.timeout === undefined || test.timeout < FULL_HARNESS_TIMEOUT_MS,
    );
    expect(underbound).toEqual([]);
  });

  it("declares a timeout of at least 300_000 ms on every full-worker test", () => {
    const underbound = workerTests.filter(
      (test) => test.timeout === undefined || test.timeout < FULL_WORKER_TIMEOUT_MS,
    );
    expect(underbound).toEqual([]);
  });
});
