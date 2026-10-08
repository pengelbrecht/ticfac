#!/usr/bin/env node
// build-world.mjs — one fixture world for the terminal property suite
// (tick z7w): a checkout that knows several runs, a machine registry that
// knows two more, and a stub tk that answers the tracker reads with a fixed
// graph. Everything a run needs is stamped relative to NOW, so ages are the
// ages the properties assert (a ten-day-old silence is stale; a three-week
// old finish is history).
//
// The world is deliberately PUBLIC-shaped: run ids, host names and paths are
// example.com-grade placeholders, and no real registry, factory or tracker is
// consulted — the binary reads only this directory (TICFAC_REGISTRY_DIR,
// HOME with no factory in it, PATH with the stub tk first).
//
// Usage: node build-world.mjs <target-dir>

import { mkdirSync, writeFileSync, rmSync, chmodSync } from "node:fs";
import { join } from "node:path";

const target = process.argv[2];
if (!target) {
  console.error("usage: build-world.mjs <target-dir>");
  process.exit(2);
}
rmSync(target, { recursive: true, force: true });

const now = Date.now();
const ago = (minutes) => new Date(now - minutes * 60_000).toISOString();
const rfc = (date) => date.toISOString().replace(/\.\d{3}Z$/, "Z");

const repo = join(target, "repo");
const registry = join(target, "registry");
const home = join(target, "home");
const bin = join(target, "bin");
for (const dir of [
  repo, registry, home, bin,
  join(repo, ".ticfac", "runs"),
  join(repo, ".ticfac", "logs"),
]) {
  mkdirSync(dir, { recursive: true });
}

// oneRun writes a run's directory, its feed, and (optionally) a checkpoint.
// A feed line is the run event feed's own shape: schema_version, at, run_id,
// tick_id (null for a run-level line), attempt (null likewise), stage,
// detail.
function runDir(runID) {
  mkdirSync(join(repo, ".ticfac", "runs", runID), { recursive: true });
  mkdirSync(join(repo, ".ticfac", "logs", runID), { recursive: true });
  return {
    say: (minutesAgo, stage, detail, tick = null, attempt = null) => {
      const line = {
        schema_version: 1, at: rfc(new Date(now - minutesAgo * 60_000)),
        run_id: runID, tick_id: tick, attempt, stage, detail,
      };
      writeFileSync(
        join(repo, ".ticfac", "logs", runID, "events.jsonl"),
        JSON.stringify(line) + "\n",
        { flag: "a" },
      );
    },
  };
}

// The world's runs, one per state the properties need:
//
//   epic-hld  a run that ended holding a finding for a person, half an hour
//             ago — attention, with the one command that clears it;
//   epic-fld  a run that failed two hours ago — attention, resume command;
//   epic-stl  a run that went silent ten days ago mid-attempt, its process
//             long gone — the STALE run that must never show as live;
//   epic-old  a run that failed three weeks ago — history by age (#87).
//
// The world holds five runs and two registry entries, so the whole listing
// fits a 30-row pane: the properties assert the VISIBLE answer, and a
// listing taller than the pane scrolls its own head off.
{
  const held = runDir("epic-hld");
  held.say(50, "dispatched", "t1 try 1 dispatched as job-1", "t1", 1);
  held.say(31, "run_held", "finding_untriaged: a finding waits for a person", "t1", 1);
  held.say(30, "run_finished", "failed: the run holds a finding for a person");

  const failed = runDir("epic-fld");
  failed.say(130, "dispatched", "t1 try 1 dispatched as job-2", "t1", 1);
  failed.say(120, "run_finished", "failed: the integrated gate refused attempt 1 of t1");

  const stale = runDir("epic-stl");
  stale.say(60 * 24 * 10, "dispatched", "t1 try 1 dispatched as job-4", "t1", 1);

  const old = runDir("epic-old");
  old.say(60 * 24 * 22, "dispatched", "t1 try 1 dispatched as job-5", "t1", 1);
  old.say(60 * 24 * 21, "run_finished", "failed: t1 did not pass");
}

// The registry's two: a STALE local run whose registration is honest but
// whose process is ten days gone, and — #86 — a registration whose checkout
// was deleted after the run claimed it. The gone checkout existed long
// enough to be registered; nothing of it is left to read.
function registration(runID, repoPath, minutesAgo) {
  writeFileSync(join(registry, runID + ".json"), JSON.stringify({
    schema_version: 1,
    run_id: runID,
    repo: repoPath,
    host: "example-host",
    registered_at: rfc(new Date(now - minutesAgo * 60_000)),
  }, null, 2) + "\n");
}
registration("run-stale", repo, 60 * 24 * 10);
{
  const gone = join(target, "gone-repo");
  mkdirSync(gone, { recursive: true });
  registration("run-gone", gone, 60 * 24 * 12);
  rmSync(gone, { recursive: true, force: true });
}

// The stub tk: answers the two --json reads the status model makes, with a
// fixed graph whose plan order the properties assert. The REAL tk is never
// consulted — the stub sits first on PATH, and its answers are validated by
// the binary against the embedded contract manifest, so a shape change here
// fails loudly, not quietly.
const graph = {
  epic: { id: "hld", title: "a fixture epic for the terminal property suite" },
  needs_planning: false,
  missing_process_ticks: [],
  unjustified_gates: [],
  stats: {
    total_tasks: 4, wave_count: 2, max_parallel: 2,
    ready_for_agent: 1, awaiting_human: 0, deferred: 0,
  },
  dispatch: { max_parallel: 0, in_flight: 0, in_flight_ids: [], free: -1, now: [] },
  waves: [
    {
      wave: 1, parallel: 2, ready: false,
      tasks: [
        {
          id: "t01", title: "first wave tick one", status: "closed",
          priority: 2, type: "task", agent_ready: false, blocked_by: [], blocks: ["t03"],
        },
        {
          id: "t02", title: "first wave tick two", status: "closed",
          priority: 2, type: "task", agent_ready: false, blocked_by: [], blocks: ["t04"],
        },
      ],
    },
    {
      wave: 2, parallel: 2, ready: true,
      tasks: [
        {
          id: "t03", title: "second wave tick three", status: "in_progress",
          priority: 2, type: "task", agent_ready: false, blocked_by: ["t01"], blocks: [],
        },
        {
          id: "t04", title: "second wave tick four", status: "ready",
          priority: 2, type: "task", agent_ready: true, blocked_by: ["t02"], blocks: [],
        },
      ],
    },
  ],
  critical_path: 2,
};
mkdirSync(join(target, "stub"), { recursive: true });
writeFileSync(join(target, "stub", "graph.json"), JSON.stringify(graph) + "\n");
const version = {
  tk: "0.32.0", contract: 1, supported_contracts: [1],
  min_tk_version: "0.32.0", manifest: "contracts/tk-json-manifest.json",
};
writeFileSync(join(target, "stub", "version.json"), JSON.stringify(version) + "\n");
const stubTK = join(bin, "tk");
writeFileSync(stubTK, [
  "#!/bin/sh",
  `WORLD="${target}"`,
  'case "$1" in',
  "  version)",
  `    cat "$WORLD/stub/version.json"`,
  "    exit 0",
  "    ;;",
  "  graph)",
  `    cat "$WORLD/stub/graph.json"`,
  "    exit 0",
  "    ;;",
  "  *)",
  '    echo "the stub tk answers version and graph only: $*" >&2',
  "    exit 2",
  "    ;;",
  "esac",
  "",
].join("\n"));
chmodSync(stubTK, 0o755);

// The world's own description: what the properties may assert. The runner
// copies this beside the specs as world.json, so the spec's oracles read the
// same facts the fixture wrote.
writeFileSync(join(target, "world.json"), JSON.stringify({
  repo,
  registry,
  // The stub tracker's plan, in the order the dashboard owes (u4l's P1,
  // asserted here from outside).
  planOrder: ["t01", "t02", "t03", "t04"],
  // The command the hold the watch is of clears with — whole, copyable.
  clearingCommand: "ticfac triage hld",
  // A run nobody vouches for is never live: these are the stale, the
  // gone-checkout (#86) and the ten-days-silent runs, whose honest words
  // are anything but running.
  neverLive: ["run-stale", "run-gone", "epic-stl"],
  // The runs whose rows need a person, each with the command its row owes.
  needingAPerson: [
    { runID: "epic-hld", command: "ticfac triage hld" },
    { runID: "epic-fld", command: "ticfac run-epic fld" },
  ],
  // History: the finished run the human view collapses (#87), absent as a
  // row and named by the one summary line that says so.
  history: ["epic-old"],
  collapseMention: "older run",
}, null, 2) + "\n");

console.log(target);
