/**
 * vitest globalSetup: starts the workerd watchdog (workerd-watchdog.mjs) for
 * this vitest process. It runs in vitest's own main process, so
 * `process.pid` is the pid whose death the watchdog waits for. Detached and
 * unref'd: the watchdog must outlive a SIGKILL of this process, and must not
 * hold the test run open.
 */
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

export default function setup() {
  const watchdog = spawn(
    process.execPath,
    [fileURLToPath(new URL("./workerd-watchdog.mjs", import.meta.url)), String(process.pid)],
    { detached: true, stdio: "ignore" },
  );
  watchdog.unref();
}
