/**
 * The boundary guard of a worker's execution environment (epic 43y, tick kgk).
 *
 * `.tick/` is the ORCHESTRATOR's serialized state, and a worker's agent may
 * not write it. The container's image already enforces this in three layers
 * (image/worker.sh, tick dxk): a `tk` shim on the harness's PATH, a
 * pre-commit hook, and a sweep before the salvage. The pi-durable harness
 * runs its own tools through an `ExecutionEnv`, so the same boundary has to
 * hold there: every command the env runs resolves `tk` to a shim that
 * refuses, whether that env is a FactorySandbox container or a local
 * worktree.
 *
 * The refusal text is the SAME pinned string the container's shim prints —
 * "tk is not available to a worker agent", pinned in
 * internal/sandboximage/sandboximage.go and contracts/worker-boot-contract.json
 * (`boundary.tk_denied`) — and the node test suite asserts this file against
 * that contract, so the two shims cannot drift apart silently.
 *
 * What the shim refuses is a WRITE to THIS checkout's tracker, and only
 * that: a read (version, show, list, …) passes through to the real tk, and so
 * does any call against a tracker that is not this checkout's (a test
 * fixture's temporary repository) — the same scoping the container's shim
 * learned in epic hn6 (run_3f034e68), so ticfac's own startup probes and the
 * repository's tests keep answering.
 */

import type { Context } from "@earendil-works/chord";

/**
 * The refusal the agent is handed, first line of the shim's answer. Pinned in
 * three places: the container's worker.sh, this harness shim, and the shared
 * contract (contracts/worker-boot-contract.json `boundary.tk_denied`, asserted
 * by internal/sandboximage and by this package's node test suite).
 */
export const WORKER_TK_DENIED = "tk is not available to a worker agent";

/** The `tk` shim itself: the layer-1 script the guard directory holds. */
export function boundaryGuardShim(): string {
  return `#!/usr/bin/env bash
# Installed by the ticfac harness's execution environment (epic 43y, tick kgk).
# The container's own guard (image/worker.sh install_boundary_guard, tick dxk)
# installs the same refusal for the pi-CLI worker path; this is the pi-durable
# env's copy. The orchestrator owns all tick state; a worker's agent has no
# business in it, so this is what \`tk\` resolves to for the harness and
# everything it spawns.
set -u
_guard="$(cd "$(dirname "\${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"
_real="$(cat "\${_guard}/real-tk" 2>/dev/null)"
_checkout="$(cat "\${_guard}/checkout" 2>/dev/null)"
_pass=""
case "\${1:-}" in
version | --version | help | --help | -h | show | list | ls | ready | next | deps | graph | status | \\
	blocked | notes | labels | stats | whoami)
	_pass=1
	;;
esac
for _a in "$@"; do
	case "$_a" in --help | -h) _pass=1 ;; esac
done
if [ -z "$_pass" ] && [ -n "$_checkout" ]; then
	_here="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
	[ "$_here" = "$_checkout" ] || _pass=1
fi
if [ -n "$_pass" ]; then
	if [ -n "$_real" ] && [ -x "$_real" ]; then
		exec "$_real" "$@"
	fi
	printf 'ticks-worker: tk is not installed in this container.\\n' >&2
	exit 127
fi
printf 'ran \`tk %s\`\\n' "$*" >>"\${_guard}/attempts" 2>/dev/null || true
{
	printf 'ticks-worker: ${WORKER_TK_DENIED}.\\n'
	printf 'The orchestrator owns all tick state (the .tick/ directory): it opens,\\n'
	printf 'closes and annotates ticks. A worker that writes it produces conflicting\\n'
	printf 'writes across the wave, so this container refuses rather than asks.\\n'
	printf 'This attempt was recorded and will be reported to a human.\\n'
	printf 'Put whatever you were going to record in your RESULT file instead, and\\n'
	printf 'carry on with the tick.\\n'
} >&2
exit 1
`;
}

/**
 * The prefix that puts the guard's `tk` ahead of every other one on PATH.
 *
 * A command prefix, not a PATH value: the env does not know what PATH the
 * image (or the operator's machine) would have given the command, so the
 * shell expands its own \`$PATH\` behind the guard directory. The guard
 * directory must be installed first ({@link installBoundaryGuard}).
 */
export function guardPathPrefix(guardDir: string): string {
  return `export PATH="${guardDir}:$PATH"; `;
}

/**
 * What the guard needs of the environment it installs into: the same three
 * moves every `ExecutionEnv` can make, without the Result ceremony — the
 * installer is internal, and its failures are the env's own to classify.
 */
export type GuardInstallBase = {
  readonly cwd: string;
  /** Runs one short command and returns its merged output. */
  short(
    command: string,
    vars: Record<string, string>,
  ): Promise<{ exitCode: number; output: string }>;
  /** Writes a text file whole. */
  writeTextFile(path: string, content: string): Promise<void>;
};

/**
 * Installs the guard directory: the shim, its `real-tk` and `checkout`
 * companions, and the ledger the shim appends every refusal to.
 *
 * The companions are files the shim READS (exactly as the container's shim
 * does) rather than values baked into the script, so a path with a quote or a
 * space in it cannot break the shim. NOT idempotent on its own — it truncates
 * the ledger, as the container's installer does — so the caller memoizes the
 * promise and installs once per env.
 */
export async function installBoundaryGuard(
  base: GuardInstallBase,
  options: { dir: string; context: Context },
): Promise<void> {
  const { dir } = options;
  const mkdir = await base.short('mkdir -p "$DIR"', { DIR: dir });
  if (mkdir.exitCode !== 0) {
    throw new Error(`boundary guard: could not create ${dir}: ${mkdir.output.trim()}`);
  }
  // Resolved at install time, as the container's installer resolves them:
  // which tk is being shadowed, and which checkout this guard protects.
  const realTk = await base.short("command -v tk || true", {});
  const checkout = await base.short(
    "git rev-parse --path-format=absolute --git-common-dir || true",
    {},
  );
  await base.writeTextFile(`${dir}/real-tk`, realTk.output.trim());
  await base.writeTextFile(`${dir}/checkout`, checkout.output.trim());
  await base.writeTextFile(`${dir}/tk`, boundaryGuardShim());
  const chmod = await base.short('chmod +x "$SHIM"', { SHIM: `${dir}/tk` });
  if (chmod.exitCode !== 0) {
    throw new Error(`boundary guard: could not make the shim executable: ${chmod.output.trim()}`);
  }
  await base.short(': >"$LEDGER"', { LEDGER: `${dir}/attempts` });
}

/**
 * The default guard directory for a working directory: a SIBLING of the
 * checkout, never inside it — a ledger under the worktree would be an
 * untracked file a salvage could commit (the container's installer learned
 * this the same way; see image/worker.sh).
 */
export function defaultGuardDir(cwd: string): string {
  return `${cwd.replace(/\/+$/, "")}.guard`;
}
