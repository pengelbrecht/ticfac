import { describe, expect, it } from "vitest";
import { resumedFromStorage, wipCheckpointsAfterResume } from "../proof/fault-claims.ts";

/**
 * The agent-faults proof's order-checked claim (tick umx): "wip checkpoints
 * kept landing after the resume" must read the log IN ORDER — the proof's
 * log is an append-only stream, so a line's position in it is its order in
 * time. A checkpoint the host pushed before the deploy killed it satisfies
 * the old anywhere-in-the-log match; these fixtures pin the extracted reader
 * that replaced it (proof/fault-claims.ts) — the same read the staging
 * script makes, so the README's hand-read "four … after the resume" is now a
 * number the evidence records (`checkpoints_after_resume`).
 */

/** A log shaped like the kill scenario's: `before` checkpoints, the resume, `after`. */
const killLog = (before: number, after: number): string => {
  const lines = [
    "worker attempt kill-host-mid-tool: recorded; booting the container",
    "boot phase started (try 1): ticks-worker --sandbox …",
    'tool bash: {"command":"sleep 90 && printf \'tool-ran-once\\n\' >> tool-runs.txt"}',
  ];
  for (let i = 0; i < before; i += 1) lines.push(`wip checkpoint 44659c0dd${i}0${i} pushed`);
  lines.push(
    "a new host life resumed the conversation from its storage (submission 8)",
    "the tracked bash reattached to the process it left running",
  );
  for (let i = 0; i < after; i += 1) lines.push(`wip checkpoint 9785c02bb${i}0${i} pushed`);
  return `${lines.join("\n")}\n`;
};

describe("the kill scenario's 'wip checkpoints kept landing after the resume' claim", () => {
  it("counts only the checkpoints after the resume line", () => {
    expect(wipCheckpointsAfterResume(killLog(3, 2))).toBe(2);
  });

  it("a checkpoint from before the kill does not satisfy it", () => {
    // The defect this tick exists for: the old claim matched
    // /wip checkpoint … pushed/ anywhere in the log, so this log — one
    // checkpoint, pushed before the deploy killed the host, and none after —
    // passed it. The order-checked read refuses it.
    expect(wipCheckpointsAfterResume(killLog(1, 0))).toBe(0);
  });

  it("a log with no resume in it answers 0", () => {
    expect(wipCheckpointsAfterResume("wip checkpoint 44659c0dd00 pushed\n")).toBe(0);
  });

  it("failed checkpoints do not count", () => {
    const log = [
      "a new host life resumed the conversation from its storage (submission 8)",
      "wip checkpoint failed: not in a git directory",
      "wip checkpoint 9785c02bb00 pushed",
    ].join("\n");
    expect(wipCheckpointsAfterResume(log)).toBe(1);
  });
});

describe("the resume marker", () => {
  it("matches the host's resume line", () => {
    expect(
      resumedFromStorage.test(
        "a new host life resumed the conversation from its storage (submission 8)",
      ),
    ).toBe(true);
  });

  it("does not match a log where no host life resumed", () => {
    expect(resumedFromStorage.test(killLog(0, 0).replace(/a new host life resumed.*/, ""))).toBe(
      false,
    );
  });
});
