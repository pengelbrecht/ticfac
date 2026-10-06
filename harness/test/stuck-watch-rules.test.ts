import { describe, expect, it } from "vitest";
import {
  COMMIT_SILENCE_WINDOWS,
  decideStuck,
  judgeResume,
  RESUME_LOOP_LIMIT,
  type StuckWatchState,
} from "../src/host/stuck-watch.js";

/**
 * The stuck watch's two rules that read the conversation's commit stream
 * rather than the host's log (run_7445005f, epic umq, tick dax), alone.
 */

const W = 1_000;

function state(over: Partial<StuckWatchState> = {}): StuckWatchState {
  return { firstSeenAt: 0, cpuMarkMs: 0, cpuMarkAt: null, nudgedAt: null, steers: 0, ...over };
}

describe("the commit-silence bound", () => {
  it("stops a conversation silent for the bound however recently its CPU moved", () => {
    const now = COMMIT_SILENCE_WINDOWS * W + 10;
    const busy = state({ cpuMarkAt: now - 1 });
    expect(decideStuck(busy, { lastCommitAt: 5, cpuRead: true }, now, W)).toBe("stop");
  });

  it("leaves a busy conversation alone below the bound", () => {
    const now = (COMMIT_SILENCE_WINDOWS - 1) * W;
    const busy = state({ cpuMarkAt: now - 1 });
    expect(decideStuck(busy, { lastCommitAt: 0, cpuRead: true }, now, W)).toBe("none");
  });
});

describe("the resume-loop rule", () => {
  it("counts quiet resumes at the same entry and stops at the limit", () => {
    let watch = judgeResume(undefined, 27, 0, W, 0).watch;
    expect(watch.lastEntryId).toBe(27);
    let idle = 0;
    for (let life = 1; life <= RESUME_LOOP_LIMIT; life++) {
      const judged = judgeResume(watch, 27, life * 2 * W, W, 0);
      watch = judged.watch;
      idle = judged.idleResumes;
      expect(judged.stuck).toBe(life >= RESUME_LOOP_LIMIT);
    }
    expect(idle).toBe(RESUME_LOOP_LIMIT);
  });

  it("starts over when the conversation gained an entry between resumes", () => {
    const first = judgeResume({ lastEntryId: 27, lastEntryAt: 0 }, 27, 2 * W, W, 0);
    expect(first.idleResumes).toBe(1);
    const moved = judgeResume(first.watch, 31, 4 * W, W, 0);
    expect(moved.idleResumes).toBe(0);
    expect(moved.stuck).toBe(false);
  });

  it("does not count a resume within a window of the last entry", () => {
    const judged = judgeResume(
      { lastEntryId: 27, lastEntryAt: 0, idleResumes: 2 },
      27,
      W / 2,
      W,
      0,
    );
    expect(judged.idleResumes).toBe(0);
  });

  it("is off when the watch is off", () => {
    const judged = judgeResume(
      { lastEntryId: 27, lastEntryAt: 0, idleResumes: 9 },
      27,
      99 * W,
      0,
      0,
    );
    expect(judged.stuck).toBe(false);
  });
});
