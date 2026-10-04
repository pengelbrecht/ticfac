/**
 * The replay-safe tracked bash (epic 43y, tick kgk).
 *
 * pi-durable's own `createBashTool` is `replay: "unsafe"` by default: on
 * recovery after a crash mid-bash it reports "was interrupted and may have
 * partially run", because a shell is not idempotent. This tool makes it safe
 * the way the n0b round-2 prototype did (experiment 3): the call's nonce is
 * memoised durably — first-writer-wins, committed before anything starts —
 * and handed to the execution environment through
 * `execution.env[TICFAC_BASH_NONCE]`. `FactorySandboxEnv.exec` embeds that
 * nonce in the container command, and a replay finds the process by nonce
 * through `listProcesses` and reattaches instead of running it again.
 *
 * The tool itself only mints and memoises: the reattach lives in the env,
 * because the env is what knows how processes are named and found. Any env
 * that ignores the nonce gets the tool's normal, untracked behaviour.
 */

import { createBashTool } from "@earendil-works/pi-durable/tools";
import { BASH_NONCE_VAR } from "../env/factory-sandbox.js";

/** The memo the nonce is committed under, durably, per tool call. */
const NONCE_MEMO = "bash.nonce";

export function createTrackedBashTool() {
  const bash = createBashTool({
    async prepare(execution, api, context) {
      // First-writer-wins: the first invocation's nonce is the one every
      // replay reads, committed before the command it marks has started —
      // a replay that finds no process knows the command never started, and
      // a replay that finds one knows it must not start it again.
      const nonce = await api.memo(NONCE_MEMO, `bash-${crypto.randomUUID()}`, context);
      execution.env = { ...execution.env, [BASH_NONCE_VAR]: nonce };
    },
  });
  // `replay: "safe"`: pi-durable's recovery reruns `execute`, which reruns
  // this prepare, which reads the memoised nonce — the same one — so the
  // rerun reattaches to the same process instead of starting a second.
  return { ...bash, replay: "safe" as const };
}
