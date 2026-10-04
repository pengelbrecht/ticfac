/**
 * Workspace checkpoints (epic 43y, step 4 of
 * docs/spikes/n0b-round2-pi-durable.md, tick dwn): the wip commit after every
 * tool round, pushed to the ATTEMPT branch, and the restore that rebuilds a
 * lost container's workspace from it.
 *
 * The spike's experiment 4, productised. The `afterTools` hook
 * ({@link workspaceCheckpointExtension}) commits the workspace as one `wip:
 * tool round` and pushes it to the attempt branch — the run's write ref —
 * which is the SAME ref `ticfac settle --carry-work` reads
 * (internal/reconcile/settle.go): the carried-work mechanism, now at
 * tool-round granularity. When a container is lost mid-turn
 * (`FactorySandboxEnv` sees its process gone, or ended with no exit code),
 * the restore ({@link restoreWorkspace}) clears the fresh container's empty
 * workspace, clones the branch, checks out its tip — the last wip commit —
 * runs the setup command and hands the model a "restored to …; re-check and
 * re-run" result, so the turn continues minus edits since the last round.
 *
 * Runtime-neutral on purpose: everything git runs through the narrow
 * {@link HostShell} — one short command per line, values as env vars, never
 * as shell text (the env's own quoting discipline) — so the same code drives
 * a FactorySandbox container (a `FactorySandboxEnv`) and a local worktree.
 *
 * Known boundary, deliberate for this step: the loss is detected where the
 * env can SEE it — the tracked bash's poll, and a replay's fresh start
 * (`FactorySandboxEnv.ensureWorkspaceRestored`). A container that dies
 * BETWEEN rounds, under a file operation, boots empty and the operation
 * fails plainly (ENOENT) rather than restoring: the short-command door has no
 * loss signal to hook, and the WorkerAgent host (epic step 6) owns the
 * container's lifetime and can call `restoreLostWorkspace` itself.
 */

import { defineExtension, type Extension, GenerationTask, hook } from "@earendil-works/pi-durable";

/**
 * The subject every wip commit carries. The prototype's own checkpoint
 * subject (docs/spikes/n0b-round2-pi-durable.md, experiment 4): short,
 * greppable, and never mistaken for a worker's finished work.
 */
export const WIP_COMMIT_SUBJECT = "wip: tool round";

/**
 * The git credential helper, in the two variants image/common.sh
 * `install_git_credential_helper` installs — keep them in step with it. The
 * restore installs one because a fresh container booted by the sandbox never
 * ran worker.sh's boot phase; without it, no fetch of a private remote.
 */
const CREDENTIAL_HELPER_STATIC =
  // biome-ignore lint/suspicious/noTemplateCurlyInString: the shell's own expansion, not a template
  '!f() { echo username=x-access-token; echo "password=${GITHUB_TOKEN}"; }; f';
const CREDENTIAL_HELPER_TOKEN_DOOR =
  // biome-ignore lint/suspicious/noTemplateCurlyInString: the shell's own expansion, not a template
  '!f() { test "$1" = get || exit 0; t=; n=0; while [ -z "$t" ] && [ "$n" -lt 3 ]; do [ "$n" -eq 0 ] || sleep 1; n=$((n+1)); t=$(curl -fsS --max-time 20 -X POST -H "Authorization: Bearer ${TICKS_FACTORY_TOKEN}" "${TICKS_GITHUB_TOKEN_URL}" 2>/dev/null | sed -n \'s/.*"token":"\\([^"]*\\)".*/\\1/p\'); done; [ -n "$t" ] || echo "ticks credential helper: the factory token door gave no GitHub token in $n tries; git is sending the token this container booted with, which GitHub expires an hour after it was minted" >&2; echo username=x-access-token; echo "password=${t:-$GITHUB_TOKEN}"; }; f';

/**
 * Which helper the restore installs, decided exactly as the image's own
 * installer decides it — by the env the container booted with (the token
 * door's URL and this run's factory token are in the container's env).
 */
const CREDENTIAL_HELPER_CHOICE =
  // biome-ignore lint/suspicious/noTemplateCurlyInString: a shell test, not a template
  'if [ -n "${TICKS_GITHUB_TOKEN_URL:-}" ] && [ -n "${TICKS_FACTORY_TOKEN:-}" ]; then ' +
  'git config --global credential.helper "$TOKEN_DOOR" || true; else ' +
  'git config --global credential.helper "$STATIC" || true; fi';

/** The attempt branch and its remote, as the checkpoint and restore drive them. */
export type WorkspaceGit = {
  /** The origin the wip commits push to and the restore fetches from. */
  readonly remote: string;
  /**
   * The attempt branch — the run's write ref: what the worker's boot adopts,
   * what `settle --carry-work` cuts the next attempt from, and what a lost
   * container is restored to.
   */
  readonly branch: string;
  /**
   * The git identity commits are made under. Required, not optional: a
   * restored container has none of the boot's global config, and `git
   * commit` refuses without one. Applied per repository (`git config
   * user.name`), never globally.
   */
  readonly identity: { readonly name: string; readonly email: string };
  /**
   * The setup command the restore runs after the checkout — the tick's
   * "then setup" (the repository's `[sandbox]` setup, what the boot's
   * `repo_setup` runs). Runs at the workspace root.
   */
  readonly setup?: string;
  /**
   * The base commit the attempt was cut from: a container lost before the
   * FIRST wip (nothing pushed yet) restores to it, the same state the boot
   * cloned.
   */
  readonly base?: string;
  /** Extra env vars on every git line (e.g. GITHUB_TOKEN, GIT_CONFIG_GLOBAL). */
  readonly env?: Record<string, string>;
};

/**
 * The narrow shell the checkpoint code runs its git through: one SHORT
 * command per line — the door's `run` shape, what every env here can already
 * do — at the workspace root, exit code and merged output. Host commands,
 * never the model's tracked bash.
 */
export type HostShell = {
  readonly execLine: (
    line: string,
    vars: Record<string, string>,
  ) => Promise<{ readonly exitCode: number; readonly output: string }>;
};

/** One round's wip checkpoint. */
export type WipOutcome =
  | { readonly kind: "pushed"; readonly sha: string }
  /** The round changed no file: nothing committed, nothing pushed. */
  | { readonly kind: "empty" }
  | { readonly kind: "failed"; readonly error: string };

/** One restore of a lost container's workspace. */
export type RestoreOutcome =
  | { readonly kind: "restored"; readonly sha: string; readonly subject: string }
  | { readonly kind: "failed"; readonly error: string };

/** The commit line's sentinel: nothing staged, nothing to commit. */
const EMPTY = 3;

/** Output as one flat line, bounded — how every failure quote reads. */
function brief(output: string): string {
  return output.trim().split("\n").join(" ").slice(0, 200);
}

/** A failed command, named and quoted, as the outcome's error text. */
function said(what: string, out: { exitCode: number; output: string }): string {
  return `${what}: exit ${out.exitCode}: ${brief(out.output)}`;
}

/** Every line's env: the workspace's own vars under the call's. */
function varsOf(git: WorkspaceGit, vars: Record<string, string>): Record<string, string> {
  return { ...git.env, ...vars };
}

/**
 * The round's wip checkpoint: identity, stage and commit in ONE line (a
 * round's checkpoint is one RPC when it commits, and a container whose boot
 * never ran still commits), then the push. `git diff --cached --quiet` after
 * the add decides the empty case — an empty wip would be noise on the
 * attempt branch. The push IS the durability: the branch on origin is what
 * survives the container.
 *
 * Never throws: a failure (the door unavailable, a refused push) is the
 * outcome, for the caller to report.
 */
export async function pushWipCheckpoint(shell: HostShell, git: WorkspaceGit): Promise<WipOutcome> {
  try {
    const commit = await shell.execLine(
      'git config user.name "$NAME" && git config user.email "$EMAIL" && ' +
        "git add -A && if git diff --cached --quiet; then exit 3; fi && " +
        'git commit -q -m "$MSG" && git rev-parse HEAD',
      varsOf(git, {
        MSG: WIP_COMMIT_SUBJECT,
        NAME: git.identity.name,
        EMAIL: git.identity.email,
      }),
    );
    if (commit.exitCode === EMPTY) return { kind: "empty" };
    if (commit.exitCode !== 0) return { kind: "failed", error: said("git add/commit", commit) };
    const sha = commit.output.trim();
    if (sha === "" || sha.includes("\n")) {
      return {
        kind: "failed",
        error: `git rev-parse HEAD answered oddly: ${brief(commit.output)}`,
      };
    }
    const push = await shell.execLine(
      'git push -q "$REMOTE" HEAD:"refs/heads/$BRANCH"',
      varsOf(git, { REMOTE: git.remote, BRANCH: git.branch }),
    );
    if (push.exitCode !== 0) return { kind: "failed", error: said("git push", push) };
    return { kind: "pushed", sha };
  } catch (error) {
    return { kind: "failed", error: error instanceof Error ? error.message : String(error) };
  }
}

/**
 * Rebuilds a lost container's workspace from the attempt branch: clear what
 * the box holds (a half-died one may hold anything), clone the branch — init
 * at the workspace root, origin, identity, the credential helper the boot
 * installs — check out its tip (the last wip commit, or the base when the
 * first round never landed), then run the setup command. The caller hands
 * the outcome to the model; the reads after it prove the restore.
 *
 * Never throws: a failure is the outcome, for the caller to report.
 */
export async function restoreWorkspace(
  shell: HostShell,
  git: WorkspaceGit,
): Promise<RestoreOutcome> {
  const failed = (error: string): RestoreOutcome => ({ kind: "failed", error });
  try {
    // Clear and clone in one line: the fresh container boots with an EMPTY
    // workspace, and `git init` in a directory that still holds a broken
    // checkout would answer from the old one.
    const prepare = await shell.execLine(
      "find . -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && " +
        'git init -q . && git remote add origin "$REMOTE" && ' +
        'git config user.name "$NAME" && git config user.email "$EMAIL" && ' +
        CREDENTIAL_HELPER_CHOICE,
      varsOf(git, {
        REMOTE: git.remote,
        NAME: git.identity.name,
        EMAIL: git.identity.email,
        TOKEN_DOOR: CREDENTIAL_HELPER_TOKEN_DOOR,
        STATIC: CREDENTIAL_HELPER_STATIC,
      }),
    );
    if (prepare.exitCode !== 0) return failed(said("the restore's clear and clone", prepare));

    // The attempt branch's tip IS the last wip commit. Before the first one
    // it may not exist on origin yet — a container lost that early restores
    // to the base the attempt was cut from, the state the boot cloned.
    const fetched = await shell.execLine(
      'git fetch -q origin "$BRANCH"',
      varsOf(git, { BRANCH: git.branch }),
    );
    let from = `the attempt branch ${git.branch}`;
    let out = fetched;
    if (fetched.exitCode !== 0 && git.base !== undefined) {
      out = await shell.execLine('git fetch -q origin "$BASE"', varsOf(git, { BASE: git.base }));
      from = `the base ${git.base}`;
    }
    if (out.exitCode !== 0) return failed(said(`git fetch (${from})`, out));

    const checkout = await shell.execLine(
      "git checkout -q --detach FETCH_HEAD && git rev-parse HEAD && git log -1 --format=%s",
      varsOf(git, {}),
    );
    if (checkout.exitCode !== 0) return failed(said("git checkout", checkout));
    const lines = checkout.output.trim().split("\n");
    const sha = (lines[0] ?? "").trim();
    const subject = lines.slice(1).join(" ").trim();
    if (sha === "") return failed(`git rev-parse HEAD answered nothing: ${brief(checkout.output)}`);

    if (git.setup !== undefined) {
      const setup = await shell.execLine(git.setup, varsOf(git, {}));
      if (setup.exitCode !== 0) return failed(said("the setup command", setup));
    }
    return { kind: "restored", sha, subject };
  } catch (error) {
    return failed(error instanceof Error ? error.message : String(error));
  }
}

/** What {@link workspaceCheckpointExtension} needs. */
export type WorkspaceCheckpointOptions = {
  /** Host-side git shell: one short command per line, at the workspace root. */
  readonly shell: HostShell;
  readonly workspace: WorkspaceGit;
  /** Every round's outcome, for the host's log and the tests. */
  readonly onCheckpoint?: (outcome: WipOutcome) => void;
};

/**
 * The extension that checkpoints the workspace after every tool round: an
 * `afterTools` hook on the built-in generation task, installed in the
 * registry beside the tools. A replay (the harness recovering mid-hook)
 * re-runs the push idempotently: the same tree commits nothing and the same
 * push answers up-to-date.
 *
 * A failed push THROWS, on purpose: pi-durable routes extension failures to
 * `HarnessOptions.onReport` rather than into the turn, so the host SEES the
 * lost checkpoint — a silent one would leave a run believing it has
 * durability it does not.
 */
export function workspaceCheckpointExtension(options: WorkspaceCheckpointOptions): Extension {
  return defineExtension({
    name: "ticfac-workspace-checkpoints",
    hooks: [
      hook(GenerationTask, {
        async afterTools(_assistant, _results, _api, _context) {
          const outcome = await pushWipCheckpoint(options.shell, options.workspace);
          options.onCheckpoint?.(outcome);
          if (outcome.kind === "failed") {
            throw new Error(`the wip checkpoint failed: ${outcome.error}`);
          }
        },
      }),
    ],
  });
}
