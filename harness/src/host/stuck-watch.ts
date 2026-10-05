/**
 * The stuck watch, the hosted worker's half (epic 43y, tick xba): the cloud
 * counterpart of the local supervisor's activity watch
 * (internal/exec/subprocess/activity.go, tick wv2), for a worker whose
 * conversation this host drives and whose tools run in a container nobody
 * else can see into.
 *
 * WHY HERE, and not in the Go executor that dispatched the attempt: the
 * local watch reads the machine it shares with its worker — the process
 * table, the worktree, the harness's own session files — and a cloud
 * orchestrator shares none of that with a container in a factory. What the
 * conversation's own host CAN see is exactly the local watch's three
 * signals, translated:
 *
 *   - its harness's transcript → this host's own log, which says every
 *     model round, every tool call, every checkpoint, every steer — the
 *     `ticfac-harness:` lines the WorkerAgent's watch socket already
 *     serves (`WorkerAttemptHost.say`);
 *   - the CPU of the tool processes → the container's own `/proc`, the
 *     whole container being this attempt's tool tree, sampled through the
 *     door with one bounded command;
 *   - its worktree and branch → the wip checkpoint lines in the same log
 *     (every tool round pushes one), which is the branch's heartbeat.
 *
 * And the nudge is the one behaviour the local watch already prefers on a
 * durable runner (tick hpk, internal/exec/subprocess/steer.go): a STEER,
 * placed after the running tool round in the same conversation
 * (`whenBusy: "steer"`) — never an interrupt-and-reprompt, never a second
 * process. A slow-but-working tool is not interrupted by its nudge at all;
 * only a tool that is truly hung meets the stop, and the stop is the wall's
 * own mechanism (the conversation aborted, the finish phase run with the
 * unanswered status), so the attempt settles failed and the run's retry
 * ladder — not the wall clock — takes it from there.
 *
 * The ladder, like the local one (activity.go `DecideStuck`):
 *
 *   quiet for the window on every signal → one nudge, a steer carrying the
 *   evidence; activity after a nudge clears it, so a later silence earns a
 *   nudge of its own; still quiet for the window past the nudge → the stop.
 *
 * The window is the run's own stuck window (reconcile's `StuckAfter`, tick
 * wv2), carried by the dispatch door as `stuck_seconds` and the spec's
 * `stuckMs`. Absent is the local watch's default (15 minutes); zero turns
 * the watch off (the run's negative `StuckAfter`, spelled as zero because
 * the door refuses negatives).
 */

/**
 * The default window, mirroring `subprocess.DefaultStuckAfter`: how long a
 * hosted worker may show no activity before it is nudged, and again before
 * it is stopped.
 */
export const DEFAULT_STUCK_MS = 15 * 60_000;

/**
 * The CPU floor (activity.go `cpuFloor`): the container-CPU growth within a
 * window that counts as progress — a 180th of the window, never under 50ms,
 * never over 5s. Idle helpers tick a little; only real tool work clears it.
 */
export function cpuFloorMs(windowMs: number): number {
  const floor = windowMs / 180;
  if (floor < 50) return 50;
  if (floor > 5_000) return 5_000;
  return floor;
}

/**
 * How often the watch looks (activity.go `CheckEvery`): a tenth of the
 * window, between 10ms and 30s. The local floor is 100ms for a look that
 * walks a whole worktree; this look is one bounded door command, so the
 * floor here only keeps a test's tiny window from busy-looping.
 */
export function checkEveryMs(windowMs: number): number {
  const every = windowMs / 10;
  if (every < 10) return 10;
  if (every > 30_000) return 30_000;
  return every;
}

/**
 * The container's process table, sampled through the door's `run`: the sum
 * of every process's utime+stime from `/proc`, in clock ticks. One bounded
 * line, POSIX enough for the image's bash, robust against a command name
 * with spaces (the fields are counted past the LAST `)`, exactly as the Go
 * side's `ParseProcStat` does). The container IS the attempt's tool tree,
 * so the whole table is the tool CPU the local watch walks by pid.
 */
export const CONTAINER_CPU_COMMAND =
  't=0; for f in /proc/[0-9]*/stat; do read -r l < "$f" || continue; ' +
  // biome-ignore lint/suspicious/noTemplateCurlyInString: the ${…} here is the container's bash parameter expansion, never this file's
  'r=${l##*)}; set -- $r; if [ $# -ge 13 ]; then t=$((t+$12+$13)); fi; done; echo "$t"';

/** `/proc/<pid>/stat` counts CPU in USER_HZ, which every Linux userspace ABI fixes at 100. */
const USER_HZ = 100;

/**
 * Reads the sampler's answer: the CPU total of the container's processes,
 * in milliseconds. Null when the answer is not one integer — a container
 * that could not be asked is a signal that could not be read, never zero.
 */
export function parseContainerCpuMs(output: string): number | null {
  const match = /(\d+)\s*$/.exec(output.trim());
  if (match === null) return null;
  const ticks = Number(match[1]);
  if (!Number.isFinite(ticks) || ticks < 0) return null;
  return (ticks * 1000) / USER_HZ;
}

/** What the watch keeps between looks — this life's, like the local supervisor's. */
export type StuckWatchState = {
  /** The baseline: the attempt's start, so a host that restarts mid-quiet does not reset the window. */
  firstSeenAt: number;
  /** The container CPU total when it last cleared the floor, and when. */
  cpuMarkMs: number;
  cpuMarkAt: number | null;
  /** When the nudge was delivered, if it was. Activity after it clears it. */
  nudgedAt: number | null;
  /** How many nudges this watch has delivered (request ids are one each). */
  steers: number;
};

/** One look's signals. */
export type StuckSignals = {
  /** When the host's log last grew — the transcript signal. Null before this life said anything. */
  lastLogAt: number | null;
  /** The container's CPU total in ms, when it could be read; the state's mark holds when it moved. */
  cpuRead: boolean;
};

/** What one look decides (activity.go `StuckStep`). */
export type StuckStep = "none" | "nudge" | "stop";

/**
 * Folds one CPU sample into the state (activity.go `ObserveCPU`): the mark
 * moves when the container has used at least the floor since it last moved.
 * The first sample is a baseline, dated at the watch's own baseline — it
 * says nothing about when the CPU was used.
 */
export function observeCpu(
  state: StuckWatchState,
  cpuMs: number,
  now: number,
  windowMs: number,
): void {
  if (state.cpuMarkAt === null) {
    state.cpuMarkMs = cpuMs;
    state.cpuMarkAt = state.firstSeenAt || now;
    return;
  }
  if (cpuMs - state.cpuMarkMs >= cpuFloorMs(windowMs)) {
    state.cpuMarkMs = cpuMs;
    state.cpuMarkAt = now;
    return;
  }
  if (cpuMs < state.cpuMarkMs) {
    // Processes exited and took their CPU with them: the new total is the
    // baseline, and nothing is claimed about when.
    state.cpuMarkMs = cpuMs;
  }
}

/**
 * The newest sign of activity, of any kind (activity.go `Activity.Last`):
 * the log's last line, the CPU mark, and — as the floor the quiet is
 * measured from — the watch's own baseline.
 */
export function lastActivity(state: StuckWatchState, signals: StuckSignals): number {
  let last = state.firstSeenAt;
  if (signals.lastLogAt !== null && signals.lastLogAt > last) last = signals.lastLogAt;
  if (state.cpuMarkAt !== null && state.cpuMarkAt > last) last = state.cpuMarkAt;
  return last;
}

/**
 * The rule (activity.go `DecideStuck`): quiet for the window → nudge once;
 * activity after a nudge clears it, so a later silence earns a nudge of its
 * own; still quiet for the window past the nudge → stop.
 */
export function decideStuck(
  state: StuckWatchState,
  signals: StuckSignals,
  now: number,
  windowMs: number,
): StuckStep {
  const last = lastActivity(state, signals);
  if (state.nudgedAt !== null && last > state.nudgedAt) {
    state.nudgedAt = null;
  }
  if (now - last < windowMs) return "none";
  if (state.nudgedAt === null) return "nudge";
  if (now - state.nudgedAt >= windowMs) return "stop";
  return "none";
}

/**
 * The evidence sentence the nudge and the stop carry: every signal and how
 * long ago it last moved (activity.go `Activity.Evidence`, this watch's
 * signals).
 */
export function stuckEvidence(state: StuckWatchState, signals: StuckSignals, now: number): string {
  const ago = (at: number | null): string =>
    at === null ? "never in this life" : `${Math.max(0, now - at)}ms ago`;
  const parts = [
    signals.lastLogAt === null
      ? "its harness log has said nothing in this life"
      : `its harness log last grew ${ago(signals.lastLogAt)}`,
  ];
  parts.push(
    signals.cpuRead
      ? `its container's processes last used CPU ${ago(state.cpuMarkAt)} (${Math.round(state.cpuMarkMs)}ms in total)`
      : "its container's process CPU could not be read",
  );
  return parts.join("; ");
}

/**
 * The nudge text (activity.go `StuckPrompt`, mirrored so a worker reads the
 * same push from the cloud watch as from the local one).
 */
export function stuckPrompt(evidence: string, windowMs: number): string {
  const window = `${Math.round(windowMs / 1000)}s`;
  return (
    `You appear stuck: the run has seen no activity from you for ${window} — ${evidence}. ` +
    "If a command is hung, stop it and do not start it again the same way. Commit your work in progress " +
    `now, then carry on, or write your report if you cannot. If nothing moves in the next ${window} you are ` +
    "stopped and the tick is retried."
  );
}
