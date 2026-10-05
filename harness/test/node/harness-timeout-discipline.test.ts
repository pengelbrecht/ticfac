import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

/**
 * The timeout discipline of this package's load-dependent tests (tick kjs,
 * tick 7wg, tick fim) — BOTH suites this package runs.
 *
 * A test whose wall clock grows with host load may not borrow a quiet-host
 * bound: on this repository's shared host (observed steady load 23-47) every
 * default is a quiet-host number until proven otherwise. The tests this
 * guard binds are the ones that drive the things a loaded host starves:
 *
 * - a whole Harness (kjs), however the file spells the open — directly in
 *   the test body, inside a helper the body calls (the gateway tests'
 *   `openHarness`), or inside the whole attempt host the body drives
 *   (`new WorkerAttemptHost`, whose Harness.open lives in src, beyond a
 *   test-file scan's reach). Bound: FULL_HARNESS_TIMEOUT_MS, 4x the suite's
 *   original 30s default. The workerd pool runs its files beside each other;
 *   one of these measured 15.6s at synthetic load 200 against ~1s quiet
 *   (tick hv3), so even the in-process half of the criterion is load-bound.
 * - a whole worker child (7wg): the fixture's `run(...)` spawns the real
 *   local-worker entry as a separate process and the test waits on
 *   wall-clock conditions of that child's own progress. Bound:
 *   FULL_WORKER_TIMEOUT_MS, 10x the original default.
 * - a real process or the real door (fim): the node suite's shell-behaviour
 *   tests — the boundary guard, the tracked bash, FactorySandboxEnv's file
 *   operations, the workspace checkpoints' git rounds — drive real bash and
 *   real git through the local door. Under synthetic 12x oversubscription
 *   (spawn storms; harsher than the observed load) 24 node-suite tests
 *   failed at the 30s default and a 120s-bounded git-heavy test crossed its
 *   bound at 138s, so a process-driving test takes the same 10x room a
 *   full-worker one does.
 *
 * All three are enforced rather than remembered, the same way
 * internal/shorttest keeps the Go gate's discipline: a load-dependent test
 * added next month without its own bound fails here, not in a run. The
 * vitest defaults themselves are load-sized (vitest.node.config.ts); the
 * bounds this guard demands are the ones a test must STATE, so the suite
 * cannot grow a quiet-host number back by forgetting one file.
 */

/** Where the node suite lives, whatever checkout it runs in. */
const HERE = dirname(fileURLToPath(import.meta.url));

/** The workerd-pool suite this package runs beside it (vitest.config.ts). */
const WORKERD_SUITE = dirname(HERE);

/** The wall clock a full-Harness test must declare for itself, in ms. */
const FULL_HARNESS_TIMEOUT_MS = 120_000;

/** The wall clock a full-worker test must declare for itself, in ms. */
const FULL_WORKER_TIMEOUT_MS = 300_000;

/** The wall clock a process-driving test must declare for itself, in ms. */
const FULL_PROCESS_TIMEOUT_MS = 300_000;

/** The per-test timeout one `it(...)` declared, or undefined for none. */
type DeclaredTimeout = number | undefined;

/** Why a test is bound: a whole Harness, a whole worker child, real processes. */
type GuardedKind = "harness" | "worker" | "process";

/** Which of the package's two vitest configs runs the file. */
type Suite = "node" | "workerd";

type GuardedTest = {
  file: string;
  suite: Suite;
  title: string;
  kinds: GuardedKind[];
  timeout: DeclaredTimeout;
};

/**
 * The calls that start or drive a real operating-system process: node's
 * child_process entry points, plus this suite's own real-process pieces —
 * the local door (its sandbox runs every command as a real child bash) and
 * the guarded node env (its exec runs real bash behind the boundary-guard
 * shim). `FactorySandboxEnv` itself is runtime-neutral (the workerd suite
 * drives it over a scripted door), so it is not on this list; a test binds
 * through the door it is handed, which the fixture-variable analysis below
 * resolves.
 */
const PROCESS_ENTRY_POINTS = new Set([
  "exec",
  "execSync",
  "execFileSync",
  "spawn",
  "spawnSync",
  "fork",
  "localSandboxDoor",
  "createGuardedNodeExecutionEnv",
]);

/**
 * Constructors whose object opens a whole Harness INSIDE itself, so the
 * open never appears in the test file: the attempt host (src/host/
 * worker-attempt.ts) drives boot, conversation and finish on a Harness of
 * its own. A test that drives one is a full-Harness test.
 */
const WHOLE_HOST_CONSTRUCTORS = new Set(["WorkerAttemptHost"]);

/** Whether the subtree contains a `Harness.open(...)` call at any depth. */
function isHarnessOpen(node: ts.Node): boolean {
  return (
    ts.isCallExpression(node) &&
    ts.isPropertyAccessExpression(node.expression) &&
    ts.isIdentifier(node.expression.expression) &&
    node.expression.expression.text === "Harness" &&
    node.expression.name.text === "open"
  );
}

/** Whether the subtree constructs a whole-host object (see WHOLE_HOST_CONSTRUCTORS). */
function isWholeHostConstruction(node: ts.Node): boolean {
  return (
    ts.isNewExpression(node) &&
    ts.isIdentifier(node.expression) &&
    WHOLE_HOST_CONSTRUCTORS.has(node.expression.text)
  );
}

/** Whether the subtree calls a real-process entry point (see PROCESS_ENTRY_POINTS). */
function isProcessCall(node: ts.Node): boolean {
  return (
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    PROCESS_ENTRY_POINTS.has(node.expression.text)
  );
}

/**
 * Whether the subtree launches the worker child: this suite's fixture
 * `run` — a `spawn` of the real local-worker entry as a separate process,
 * the way the Go supervisor launches it. The receiver is whatever
 * identifier holds the fixture (today `f.run(...)` in local-host.test.ts).
 */
function isWorkerLaunch(node: ts.Node): boolean {
  return (
    ts.isCallExpression(node) &&
    ts.isPropertyAccessExpression(node.expression) &&
    ts.isIdentifier(node.expression.expression) &&
    node.expression.name.text === "run"
  );
}

/** Whether any node of the subtree at any depth answers `matches`. */
function walk(root: ts.Node, matches: (node: ts.Node) => boolean): boolean {
  let found = false;
  const visit = (node: ts.Node): void => {
    if (matches(node)) found = true;
    node.forEachChild(visit);
  };
  visit(root);
  return found;
}

/**
 * Every function the file declares (and every arrow assigned to a name),
 * plus every identifier the file assigns — the raw material for resolving
 * what a test body drives through the helpers and fixtures it names.
 *
 * Destructuring declarations (`const { harness } = openHarness(...)`) are
 * skipped: a test that destructures a helper's answer still calls the
 * helper in its own body, which is the evidence that binds it.
 */
function collectDeclarations(root: ts.Node): {
  helpers: Map<string, ts.Node>;
  assignments: Map<string, ts.Node[]>;
} {
  const helpers = new Map<string, ts.Node>();
  const assignments = new Map<string, ts.Node[]>();
  const assign = (name: string, initializer: ts.Node): void => {
    const list = assignments.get(name);
    if (list) list.push(initializer);
    else assignments.set(name, [initializer]);
  };
  const visit = (node: ts.Node): void => {
    if (ts.isFunctionDeclaration(node) && node.name !== undefined && node.body !== undefined) {
      helpers.set(node.name.text, node.body);
    }
    if (
      ts.isVariableDeclaration(node) &&
      node.initializer !== undefined &&
      ts.isIdentifier(node.name)
    ) {
      assign(node.name.text, node.initializer);
      if (ts.isArrowFunction(node.initializer) && node.initializer.body !== undefined) {
        helpers.set(node.name.text, node.initializer.body);
      }
    }
    if (
      ts.isBinaryExpression(node) &&
      node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
      ts.isIdentifier(node.left)
    ) {
      assign(node.left.text, node.right);
    }
    node.forEachChild(visit);
  };
  visit(root);
  return { helpers, assignments };
}

/**
 * The helpers and fixture variables one drive marks, by fixpoint: a helper
 * is marked when its body holds the drive's direct evidence, calls another
 * marked helper, or references a marked variable; a variable is marked
 * when any of its assignments does. That is how a test binds through the
 * fixtures it names — `env` is only process-driving because the file
 * assigns it from the real door (`env = new FactorySandboxEnv({ sandbox:
 * door.sandbox, ... })`, `door = localSandboxDoor(...)`) — and how it binds
 * through helpers whose bodies hold the evidence instead (`openHarness`,
 * `workerHarness`, `messagesOf`).
 *
 * The approximation, accepted: a helper parameter that shares a name with a
 * marked variable marks the helper too (scope is not resolved). Every such
 * helper today has only callers its own body already binds, and the guard
 * over-binding a test costs it a bound it can state, not a bound it misses.
 */
function drivenNames(
  helpers: Map<string, ts.Node>,
  assignments: Map<string, ts.Node[]>,
  directly: (node: ts.Node) => boolean,
): { helperNames: Set<string>; variableNames: Set<string> } {
  const helperNames = new Set<string>();
  const variableNames = new Set<string>();
  const callsMarkedHelper = (root: ts.Node): boolean =>
    walk(root, (node) => {
      return (
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        helperNames.has(node.expression.text)
      );
    });
  const referencesMarkedVariable = (root: ts.Node): boolean =>
    walk(root, (node) => ts.isIdentifier(node) && variableNames.has(node.text));
  const evidence = (node: ts.Node): boolean =>
    directly(node) || callsMarkedHelper(node) || referencesMarkedVariable(node);
  for (let changed = true; changed; ) {
    changed = false;
    for (const [name, body] of helpers) {
      if (!helperNames.has(name) && evidence(body)) {
        helperNames.add(name);
        changed = true;
      }
    }
    for (const [name, initializers] of assignments) {
      if (!variableNames.has(name) && initializers.some(evidence)) {
        variableNames.add(name);
        changed = true;
      }
    }
  }
  return { helperNames, variableNames };
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
 * Every `it(...)` in the source this guard binds, with why: the drives the
 * body holds directly, the helpers it calls that hold them, and the fixture
 * variables it names that were assigned from them. A test can hold several
 * drives at once (a full-worker test drives real processes too); the bound
 * it owes is the largest of the ones it holds.
 */
function guardedTests(source: string): {
  title: string;
  kinds: GuardedKind[];
  timeout: DeclaredTimeout;
}[] {
  const file = ts.createSourceFile("suite.test.ts", source, ts.ScriptTarget.Latest, true);
  const { helpers, assignments } = collectDeclarations(file);
  const harnessDriven = drivenNames(helpers, assignments, (node) => {
    return walk(node, isHarnessOpen) || walk(node, isWholeHostConstruction);
  });
  const processDriven = drivenNames(helpers, assignments, (node) => walk(node, isProcessCall));
  const workerDriven = drivenNames(helpers, assignments, (node) => walk(node, isWorkerLaunch));
  const calls = (body: ts.Node, names: Set<string>): boolean =>
    walk(body, (node) => {
      return (
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        names.has(node.expression.text)
      );
    });
  const names = (body: ts.Node, identifiers: Set<string>): boolean =>
    walk(body, (node) => ts.isIdentifier(node) && identifiers.has(node.text));

  const tests: { title: string; kinds: GuardedKind[]; timeout: DeclaredTimeout }[] = [];
  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "it" &&
      node.arguments.length >= 2 &&
      ts.isStringLiteralLike(node.arguments[0])
    ) {
      const title = node.arguments[0];
      const testFn = node.arguments.find((argument) => ts.isArrowFunction(argument));
      if (testFn && ts.isArrowFunction(testFn)) {
        const kinds: GuardedKind[] = [];
        if (walk(testFn, isWorkerLaunch) || calls(testFn, workerDriven.helperNames)) {
          kinds.push("worker");
        }
        if (
          walk(testFn, isHarnessOpen) ||
          walk(testFn, isWholeHostConstruction) ||
          calls(testFn, harnessDriven.helperNames)
        ) {
          kinds.push("harness");
        }
        if (
          walk(testFn, isProcessCall) ||
          calls(testFn, processDriven.helperNames) ||
          names(testFn, processDriven.variableNames)
        ) {
          kinds.push("process");
        }
        if (kinds.length > 0) {
          tests.push({
            title: title.text,
            kinds,
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

/** The guarded tests of one suite, read from the directory its config includes. */
function suiteTests(directory: string, names: string[], suite: Suite): GuardedTest[] {
  return names.flatMap((name) =>
    guardedTests(readFileSync(join(directory, name), "utf8")).map((test) => ({
      file: name,
      suite,
      ...test,
    })),
  );
}

describe("the load-dependent tests of both suites state their own wall clock", () => {
  const nodeFiles = readdirSync(HERE)
    .filter((name) => name.endsWith(".test.ts"))
    .sort();
  const workerdFiles = readdirSync(WORKERD_SUITE)
    .filter((name) => name.endsWith(".test.ts"))
    .sort();
  const tests = [
    ...suiteTests(HERE, nodeFiles, "node"),
    ...suiteTests(WORKERD_SUITE, workerdFiles, "workerd"),
  ];
  const harnessTests = tests.filter((test) => test.kinds.includes("harness"));
  const workerTests = tests.filter((test) => test.kinds.includes("worker"));
  const processTests = tests.filter((test) => test.kinds.includes("process"));

  /** The underbound tests as file: title lines, so a failure names its work. */
  const named = (tests: GuardedTest[]): string[] =>
    tests.map((test) => `${test.suite}/${test.file}: ${test.title}`);

  it("finds the tests it guards, so it cannot pass by reading nothing", () => {
    expect(nodeFiles.length).toBeGreaterThan(0);
    expect(workerdFiles.length).toBeGreaterThan(0);
    // At least the two kjs names: the destroy tests over real git, whose
    // mid-turn sibling took 33.5 s under a concurrent `make gate`. Both gone
    // means the in-body half of the harness criterion binds nothing and
    // needs a look, not a green tick.
    expect(harnessTests.filter((test) => test.suite === "node").length).toBeGreaterThanOrEqual(2);
    // At least the workerd pool's full-Harness tests (fim): the gateway
    // tests whose open sits inside the openHarness helper, the five
    // worker-followups tests behind workerHarness, the attempt-host tests
    // whose open sits inside src, and the in-body openers
    // (transcript-replay, tracked-bash-resume, workspace-checkpoints). A
    // count below this means the helper and whole-host halves of the
    // criterion stopped binding, not that the suite shrank.
    expect(harnessTests.filter((test) => test.suite === "workerd").length).toBeGreaterThanOrEqual(
      20,
    );
    // At least the seven full-worker tests of local-host.test.ts, whose
    // spawn-and-wait shape starved past every quiet-host bound twice in one
    // night (tick 7wg) — including the two that night's failure log names.
    expect(workerTests.length).toBeGreaterThanOrEqual(7);
    const workerTitles = new Set(workerTests.map((test) => test.title));
    expect(
      workerTitles.has(
        "resumes from the storage after the harness is killed mid-tool, without re-running the tool",
      ),
    ).toBe(true);
    expect(
      workerTitles.has("continues the SAME conversation when relaunched with a follow-up message"),
    ).toBe(true);
    // At least the process-driving set of the node suite (fim): the 24 that
    // failed at the quiet-host 30s default under synthetic 12x
    // oversubscription — the boundary guard, the tracked bash,
    // FactorySandboxEnv's file operations, the workspace checkpoints' git
    // rounds, and the full-worker tests (which drive real processes too).
    expect(processTests.length).toBeGreaterThanOrEqual(24);
    const processTitles = new Set(processTests.map((test) => test.title));
    expect(
      processTitles.has(
        "refuses a tracker write, says the pinned refusal, and never reaches the real tk",
      ),
    ).toBe(true);
    expect(
      processTitles.has(
        "cancels an invocation without killing the process, then reattaches to the same one",
      ),
    ).toBe(true);
    expect(
      processTitles.has("writes and reads text back, with a path that is a value, not shell text"),
    ).toBe(true);
    expect(
      processTitles.has(
        "pushes nothing for a round that changed no file, and the changed round's wip lands on origin",
      ),
    ).toBe(true);
    // The two spellings fim added the harness criterion for: the helper that
    // opens (the gateway tests) and the whole host whose open is in src.
    const workerdHarnessTitles = new Set(
      harnessTests.filter((test) => test.suite === "workerd").map((test) => test.title),
    );
    expect(
      workerdHarnessTitles.has(
        "carries the GLM maxTokens and thinkingFormat overrides on the wire",
      ),
    ).toBe(true);
    expect(
      workerdHarnessTitles.has(
        "boots, converses on the boot's prompt, finishes and settles with the finish phase's exit code",
      ),
    ).toBe(true);
  });

  it("declares a timeout of at least 120_000 ms on every full-Harness test", () => {
    const underbound = harnessTests.filter(
      (test) => test.timeout === undefined || test.timeout < FULL_HARNESS_TIMEOUT_MS,
    );
    expect(named(underbound)).toEqual([]);
  });

  it("declares a timeout of at least 300_000 ms on every full-worker test", () => {
    const underbound = workerTests.filter(
      (test) => test.timeout === undefined || test.timeout < FULL_WORKER_TIMEOUT_MS,
    );
    expect(named(underbound)).toEqual([]);
  });

  it("declares a timeout of at least 300_000 ms on every process-driving test", () => {
    const underbound = processTests.filter(
      (test) => test.timeout === undefined || test.timeout < FULL_PROCESS_TIMEOUT_MS,
    );
    expect(named(underbound)).toEqual([]);
  });
});
