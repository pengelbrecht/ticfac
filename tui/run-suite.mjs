#!/usr/bin/env node
// run-suite.mjs — the terminal property suite's runner (tick z7w).
//
//   node run-suite.mjs            the honest half: the real ticfac binary,
//                                  every property must PASS
//   node run-suite.mjs --seeded    the seeded half: each property must
//                                  FAIL against its deliberately broken
//                                  program — a property that cannot fail
//                                  pins nothing
//   node run-suite.mjs --all       both, in order
//
// What a run does: build the ticfac binary (go build; GOBIN is not touched,
// the binary lands in the suite's own work directory), build the fixture
// world (build-world.mjs), write the SUT wrappers that bake the world's
// environment (HOME with no factory, TICFAC_REGISTRY_DIR at the fixture
// registry, PATH with the stub tk first), copy the world's facts beside the
// specs as world.json, and drive each specification through Bombadil's
// terminal driver.
//
// The suite is HERMETIC: the binary reads only the fixture world, the
// registry and the stub tk inside it. No real factory, no real tracker, no
// operator state, and no test may reach the operator's ~/.ticfac —
// HOME and TICFAC_REGISTRY_DIR are always pointed at the world.

import { spawnSync } from "node:child_process";
import { mkdirSync, rmSync, cpSync, chmodSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "..");
const work = join(here, ".tui-work");

const mode = process.argv.includes("--seeded")
  ? "seeded"
  : process.argv.includes("--all")
    ? "all"
    : "honest";

const say = (m) => console.log(m);
const die = (m) => {
  console.error(m);
  process.exit(1);
};

// 1. The binary. TICFAC_BIN overrides (CI builds its own); otherwise the
// runner builds it — go build is cached, and the suite must not depend on
// anything outside the checkout.
let ticfac = process.env.TICFAC_BIN ?? "";
if (!ticfac) {
  ticfac = join(work, "ticfac");
  mkdirSync(work, { recursive: true });
  const build = spawnSync("go", ["build", "-o", ticfac, "./cmd/ticfac"], {
    cwd: repo,
    stdio: "inherit",
  });
  if (build.status !== 0) die("the suite could not build the ticfac binary");
}

// 2. The fixture world.
rmSync(join(work, "world"), { recursive: true, force: true });
const world = join(work, "world");
const built = spawnSync(process.execPath, [join(here, "build-world.mjs"), world], {
  stdio: "inherit",
});
if (built.status !== 0) die("the suite could not build the fixture world");

// 3. The world's facts, beside the specs (world.json is generated: never
// committed, always re-written from the world the run just built).
cpSync(join(world, "world.json"), join(here, "specs", "world.json"));

// 4. The SUT wrappers: the honest programs, with the world's environment
// baked in. Bombadil spawns `sh <wrapper>`, and the wrapper execs the
// binary — no PATH games on the host, nothing outside the world.
const envPreamble = (path) =>
  [
    "#!/bin/sh",
    `HOME="${join(world, "home")}" \\`,
    `TICFAC_REGISTRY_DIR="${join(world, "registry")}" \\`,
    `PATH="${join(world, "bin")}:${path}" \\`,
    "export HOME TICFAC_REGISTRY_DIR PATH",
    // The driver types and clicks at the program while it runs; the
    // program never reads stdin, so without this the terminal's own ECHO
    // writes those bytes over the listing the properties assert. A quiet
    // keyboard is the driver's hygiene, the same as NO_COLOR: echo is the
    // terminal's artifact, never the program's answer.
    "stty -echo",
  ].join("\n");
const wrapper = (name, body) => {
  const path = join(work, name);
  writeFileSync(path, body);
  chmodSync(path, 0o755);
  return path;
};
const systemPath = process.env.PATH ?? "/usr/bin:/bin";
const overviewSUT = wrapper("run-overview.sh", [
  envPreamble(systemPath),
  `exec "${ticfac}" --repo "${join(world, "repo")}"`,
  "",
].join("\n"));
const watchSUT = wrapper("run-watch.sh", [
  envPreamble(systemPath),
  `exec "${ticfac}" watch --repo "${join(world, "repo")}" --interval 300ms epic-hld`,
  "",
].join("\n"));

// 5. The runs. Each entry is one specification against one program: honest
// entries must pass, seeded entries must violate.
const runs = [
  {
    name: "overview: the honest binary answers every property",
    spec: "overview.spec.ts",
    sut: overviewSUT,
    seconds: "25s",
    want: 0,
  },
  {
    name: "watch: the honest dashboard answers every property",
    spec: "watch.spec.ts",
    sut: watchSUT,
    seconds: "15s",
    want: 0,
  },
];

if (mode === "seeded" || mode === "all") {
  // The seeded half: each broken program against the specification whose
  // property it violates. The want is the VIOLATION (bombadil exits
  // non-zero), and the suite refuses a seeded program that passes — that is
  // the non-vacuity proof for the property its name says.
  runs.push(
    {
      name: "overview: a phantom running row violates no-stale-live",
      spec: "overview.spec.ts",
      sut: join(here, "seeded", "phantom-live.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "overview: a hold without its command violates clearing-command",
      spec: "overview.spec.ts",
      sut: join(here, "seeded", "commandless-hold.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "overview: history rendered as rows violates the collapse",
      spec: "overview.spec.ts",
      sut: join(here, "seeded", "history-expanded.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "watch: needs-you-nothing over a hold violates the hold alert",
      spec: "watch.spec.ts",
      sut: join(here, "seeded", "needs-you-nothing.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "watch: reordered rows violate the grouped-order property",
      spec: "watch.spec.ts",
      sut: join(here, "seeded", "rows-reordered.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "watch: groups standing in an order the design never drew violate the grouped-order property",
      spec: "watch.spec.ts",
      sut: join(here, "seeded", "groups-reordered.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "watch: a fabricated $0.00 violates never-a-fabricated-zero",
      spec: "watch.spec.ts",
      sut: join(here, "seeded", "fabricated-zero.sh"),
      seconds: "8s",
      want: "violation",
    },
    {
      name: "watch: a never-drawn frame violates the first-frame bound",
      spec: "watch.spec.ts",
      sut: join(here, "seeded", "never-frames.sh"),
      seconds: "8s",
      want: "violation",
    },
  );
}

if (mode === "seeded") {
  runs.splice(0, 2); // the honest half is --all's and the plain run's
}

// 6. Drive. Bombadil's exit code is the verdict: 0 no violation, 1 a
// property was violated, and anything else is the driver's own failure.
const bombadil = join(here, "node_modules", ".bin", "bombadil");
let failed = 0;
for (const run of runs) {
  const trace = join(work, "traces", run.spec.replace(".spec.ts", "") + "-" + run.name.replace(/[^a-z0-9]+/gi, "-"));
  rmSync(trace, { recursive: true, force: true });
  say(`> ${run.name}`);
  const result = spawnSync(
    bombadil,
    [
      "terminal", "test",
      "--specification", join(here, "specs", run.spec),
      "--time-limit", run.seconds,
      // 45 rows: the pane the overview's listing must seat whole — the
      // redesign (epic ymf) grew each run's block to the dashboard's own
      // headline (health line, phase track, you-are-here), taking the
      // listing from ~25 to ~40 lines. The properties assert the VISIBLE
      // answer, so the pane the honest run is driven on seats the whole
      // listing and a held run's row never scrolls off its own screen.
      "--columns", "100", "--rows", "45",
      "--output-path", trace,
      "--output-path-overwrite",
      "--exit-on-violation",
      "--", "sh", run.sut,
    ],
    { stdio: "inherit" },
  );
  const exit = result.status ?? -1;
  if (run.want === 0 && exit !== 0) {
    say(`  FAIL: the honest run exited ${exit} (a property was violated, or the driver failed)`);
    failed++;
  } else if (run.want === "violation" && exit === 0) {
    say(`  FAIL: the seeded program passed every property — the check is vacuous`);
    failed++;
  } else if (run.want === "violation" && exit !== 0) {
    say(`  ok: violated, as it must be (exit ${exit})`);
  } else {
    say(`  ok (exit ${exit})`);
  }
}
if (failed > 0) die(`${failed} of ${runs.length} runs failed`);
say(`all ${runs.length} runs answered as they must`);
