#!/usr/bin/env node
/**
 * Reaps the workerd processes a killed vitest leaves behind.
 *
 * miniflare starts workerd with plain pipes and stops it only from its own
 * exit hook. A vitest that exits normally runs that hook; one that is
 * SIGKILLed (a tool's timeout, an agent killing its own run) does not, and an
 * idle workerd never notices its parent is gone: it is reparented to init and
 * stays up for good. On this repository's shared host two dozen of them had
 * accumulated across agent worktrees, some for a week.
 *
 * `vitest.config.ts` starts this as a DETACHED process (its own session, so a
 * process-group kill of the test run does not take the watchdog with it) with
 * the vitest process's pid. While vitest lives, the watchdog records the
 * workerd processes descended from it. Once vitest is gone, it SIGKILLs every
 * recorded pid that still runs the same command (never a recycled pid), then
 * exits. A normal exit leaves nothing to reap and the watchdog just exits.
 *
 * Kill by exact pid only, never by pattern: the host is shared, and another
 * worktree's live workerd is not this run's to touch.
 */
import { execFileSync } from "node:child_process";

const parent = Number(process.argv[2]);
const POLL_MS = 1_000;

if (!Number.isInteger(parent) || parent <= 1) {
  process.exit(2);
}

/** Every process as pid -> { ppid, command }. */
function table() {
  const out = execFileSync("ps", ["-axo", "pid=,ppid=,command="], { encoding: "utf8" });
  const rows = new Map();
  for (const line of out.split("\n")) {
    const match = /^\s*(\d+)\s+(\d+)\s+(.*)$/.exec(line);
    if (match) rows.set(Number(match[1]), { ppid: Number(match[2]), command: match[3] });
  }
  return rows;
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error.code === "EPERM";
  }
}

/** The workerd processes under `root`, by pid, with the command each runs. */
function workerdUnder(rows, root) {
  const found = new Map();
  const children = new Map();
  for (const [pid, row] of rows) {
    if (!children.has(row.ppid)) children.set(row.ppid, []);
    children.get(row.ppid).push(pid);
  }
  const stack = [root];
  while (stack.length > 0) {
    const pid = stack.pop();
    for (const child of children.get(pid) ?? []) {
      const command = rows.get(child).command;
      if (/\/workerd(\s|$)/.test(command)) found.set(child, command);
      stack.push(child);
    }
  }
  return found;
}

const recorded = new Map();
for (;;) {
  if (alive(parent)) {
    try {
      for (const [pid, command] of workerdUnder(table(), parent)) recorded.set(pid, command);
    } catch {
      // A failed `ps` is retried on the next poll.
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    continue;
  }
  // vitest is gone. Whatever it left running is reaped, by exact pid, and
  // only while the pid still runs the command recorded for it.
  let rows = new Map();
  try {
    rows = table();
  } catch {
    process.exit(1);
  }
  for (const [pid, command] of recorded) {
    if (rows.get(pid)?.command !== command) continue;
    try {
      process.kill(pid, "SIGKILL");
    } catch {
      // Already gone.
    }
  }
  process.exit(0);
}
