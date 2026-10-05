/**
 * Workspace checkpoints (epic 43y, step 4 of
 * docs/spikes/n0b-round2-pi-durable.md, tick dwn): the wip commit after every
 * tool round, pushed to the ATTEMPT branch, and the restore that rebuilds a
 * lost container's workspace from it.
 *
 * The spike's experiment 4, productised. The `afterTools` hook
 * ({@link workspaceCheckpointExtension}) snapshots the workspace as one `wip:
 * tool round` commit — built in a throwaway index on top of the agent's own
 * HEAD, never committed on the agent's branch (tick xd3, see
 * {@link pushWipCheckpoint}) — and pushes it to the attempt branch — the run's write ref —
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
 * The pre-round ready check (tick 4fs): a container destroyed BETWEEN tool
 * rounds — under no harness call, so no loss signal ever reaches the env,
 * and the run-door RPC has none to hook — boots empty, and the next round's
 * first file operation would fail plainly (ENOENT) instead of restoring.
 * The `beforeRequest` hook of {@link workspaceCheckpointExtension} is the
 * host-side seam: it verifies the ready marker before every round's request
 * goes out and restores the workspace when the marker is gone (wired to
 * `FactorySandboxEnv.ensureWorkspaceReady`, which reuses
 * `restoreLostWorkspace`), so the round runs on the rebuilt tree and nothing
 * is lost — the last round's wip already landed. A loss DURING a round, under
 * a file operation, still fails plainly: the remaining boundary belongs to
 * the WorkerAgent host (epic step 6), which owns the container's lifetime.
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
  /**
   * One LONG host command — the restore's setup (tick cni): a real
   * repository's `[sandbox]` setup is a dependency install that runs
   * MINUTES and prints megabytes, and the run door's one-RPC shape is for
   * short commands — its bounding `head -c` SIGPIPEs a command that prints
   * past its bound, so the install dies mid-way (the `exit 141` the node
   * suite reproduces). A shell that can carries the setup through the
   * process doors instead (started once, polled to its end, the output a
   * file a cursor reads); one that cannot falls back to {@link execLine}.
   * Same shape as {@link execLine}: at the workspace root, exit code and
   * merged output (the tail, bounded).
   */
  readonly execLong?: (
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

/** One end-of-run salvage of the uncommitted tree, after {@link retireWipSnapshot}. */
export type SalvageOutcome =
  | { readonly kind: "salvaged"; readonly sha: string }
  /** The tree was clean (or only the excluded paths were dirty): no commit. */
  | { readonly kind: "empty" }
  | { readonly kind: "failed"; readonly error: string };

/**
 * The pre-round ready check's answer (tick 4fs): the workspace's ready marker
 * was there (`ready`), or it was gone and the workspace was rebuilt from the
 * attempt branch (`restored`), or the rebuild failed (`failed`).
 */
export type WorkspaceReadyOutcome = { readonly kind: "ready" } | RestoreOutcome;

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
 * The round's wip checkpoint: a SNAPSHOT of the workspace, pushed to the
 * attempt branch — and nothing in the checkout touched. The snapshot is
 * built with plumbing in a throwaway index (the way worker.sh's boot-stopped
 * marker is): the working tree's every file, a commit whose parent is the
 * agent's own HEAD, pushed with force over the previous round's snapshot. The
 * agent's branch, index and history stay exactly what the agent made them.
 *
 * Why not a commit on the agent's branch (the first cut, tick dwn): the xd3
 * staging run's model ran `git add && git commit` after a round whose wip had
 * already committed its edits, was told "nothing to commit", decided the
 * harness had swallowed its work and `git reset --soft HEAD^` to rewrite it —
 * and every push after that, the finish phase's fast-forward-only one
 * included, was refused as non-fast-forward (exit 9). A checkpoint must be
 * invisible to the work it checkpoints.
 *
 * A clean round (the working tree is HEAD's tree) pushes the agent's own
 * HEAD when its commits moved since the last push, so the branch carries
 * them; the first clean round only records what it saw (the boot's HEAD is
 * on origin already: the base, or the branch it adopted). A round that
 * changed nothing since the last push pushes nothing — the record (the
 * pushed HEAD and tree) is kept in the checkout's git dir. The push IS the
 * durability: the branch on origin is what survives the container.
 *
 * Never throws: a failure (the door unavailable, a refused push) is the
 * outcome, for the caller to report.
 */
export async function pushWipCheckpoint(shell: HostShell, git: WorkspaceGit): Promise<WipOutcome> {
  try {
    const snapshot = await shell.execLine(
      'git config user.name "$NAME" && git config user.email "$EMAIL" && ' +
        'idx="$(git rev-parse --git-path ticfac-wip-index)" && rec="$(git rev-parse --git-path ticfac-wip-pushed)" && ' +
        'rm -f "$idx" && GIT_INDEX_FILE="$idx" git read-tree HEAD && GIT_INDEX_FILE="$idx" git add -A && ' +
        'tree="$(GIT_INDEX_FILE="$idx" git write-tree)" && rm -f "$idx" && head="$(git rev-parse HEAD)" && ' +
        'key="$head $tree" && ' +
        'if [ -f "$rec" ] && [ "$(cat "$rec")" = "$key" ]; then exit 3; fi && ' +
        'if [ "$tree" = "$(git rev-parse "HEAD^{tree}")" ]; then ' +
        'if [ ! -f "$rec" ]; then printf %s "$key" > "$rec"; exit 3; fi; ' +
        'printf "%s\n%s\n" "$head" "$key"; exit 0; fi && ' +
        'c="$(git commit-tree "$tree" -p "$head" -m "$MSG")" && printf "%s\n%s\n" "$c" "$key"',
      varsOf(git, {
        MSG: WIP_COMMIT_SUBJECT,
        NAME: git.identity.name,
        EMAIL: git.identity.email,
      }),
    );
    if (snapshot.exitCode === EMPTY) return { kind: "empty" };
    if (snapshot.exitCode !== 0) {
      return { kind: "failed", error: said("the wip snapshot", snapshot) };
    }
    const [sha = "", key = ""] = snapshot.output.trim().split("\n");
    if (!/^[0-9a-f]{7,64}$/.test(sha.trim()) || key.trim() === "") {
      return {
        kind: "failed",
        error: `the wip snapshot answered oddly: ${brief(snapshot.output)}`,
      };
    }
    const push = await shell.execLine(
      'git push -q -f "$REMOTE" "$SHA:refs/heads/$BRANCH" && ' +
        'printf %s "$KEY" > "$(git rev-parse --git-path ticfac-wip-pushed)"',
      varsOf(git, { REMOTE: git.remote, BRANCH: git.branch, SHA: sha.trim(), KEY: key.trim() }),
    );
    if (push.exitCode !== 0) return { kind: "failed", error: said("git push", push) };
    return { kind: "pushed", sha: sha.trim() };
  } catch (error) {
    return { kind: "failed", error: error instanceof Error ? error.message : String(error) };
  }
}

/**
 * Before the finish phase (epic 43y step 6, tick xd3): the attempt branch
 * goes back to the agent's own HEAD. The last round's snapshot sits on it,
 * and the finish phase's push is fast-forward only (image/worker.sh
 * `push_branch`) — from a HEAD the snapshot is a child of, never an
 * ancestor. The working tree is untouched: what was uncommitted is still
 * there for the finish phase's salvage to commit.
 *
 * Never throws.
 */
export async function retireWipSnapshot(
  shell: HostShell,
  git: WorkspaceGit,
): Promise<{ readonly kind: "retired" } | { readonly kind: "failed"; readonly error: string }> {
  try {
    const push = await shell.execLine(
      'git push -q -f "$REMOTE" "HEAD:refs/heads/$BRANCH" && rm -f "$(git rev-parse --git-path ticfac-wip-pushed)"',
      varsOf(git, { REMOTE: git.remote, BRANCH: git.branch }),
    );
    return push.exitCode === 0
      ? { kind: "retired" }
      : { kind: "failed", error: said("git push of HEAD", push) };
  } catch (error) {
    return { kind: "failed", error: error instanceof Error ? error.message : String(error) };
  }
}

/**
 * The end-of-run salvage (tick nou): everything the worker wrote and did not
 * commit, committed as one commit ON the agent's own branch — the cloud
 * finish phase's own move (image/worker.sh `salvage_uncommitted`, tick 5fg:
 * run run_2e66e765's containers paid for real work and kept none of it),
 * written runtime-neutral for the LOCAL host to run after it retires the
 * last wip snapshot.
 *
 * Why the local host needs it: since tick xd3 a wip snapshot is a commit ON
 * TOP of the agent's HEAD, force-pushed and invisible to the branch's own
 * history — so a worker that settles without committing leaves the attempt
 * branch holding nothing its history can count, and the supervisor's
 * fast-forward push of HEAD is refused over the snapshot (the reflog rescue
 * cannot apply: the snapshot was never a state the branch held). Retire,
 * then salvage, and the branch ends at a commit collect can count and the
 * push can follow.
 *
 * What may ride the salvage is the WORK. Never the report (the caller names
 * its path; the cloud commits it separately with the agent's STATUS line
 * intact, and a salvage commit carrying it would make the work
 * indistinguishable from the account of it), and never tracker state
 * (`.tick/`, `.ticfac/` — the boundary the guards enforce, unstaged here the
 * way worker.sh's salvage unstages it): a salvage the host authored must not
 * launder a violation into a commit the boundary would then refuse the whole
 * attempt over.
 *
 * Best effort, never throws: a salvage that cannot be made is the outcome,
 * for the host to report — it must not stop the exit path that follows it.
 */
export async function salvageUncommittedWork(
  shell: HostShell,
  git: WorkspaceGit,
  options: { readonly subject: string; readonly reportPath?: string },
): Promise<SalvageOutcome> {
  try {
    const salvage = await shell.execLine(
      'git config user.name "$NAME" && git config user.email "$EMAIL" && git add -A && ' +
        // The never-salvaged paths, each a failure-tolerant segment: a report
        // that is not there yet, a worktree with no .tick, stage nothing.
        (options.reportPath === undefined
          ? ""
          : '{ git reset -q -- "$REPORT" 2>/dev/null || true; } && ') +
        "{ git reset -q -- .tick .ticfac 2>/dev/null || true; } && " +
        "if git diff --cached --quiet; then git reset -q; exit 3; fi && " +
        'git commit -q --no-verify -m "$MSG" >/dev/null && git rev-parse HEAD',
      varsOf(git, {
        MSG: options.subject,
        NAME: git.identity.name,
        EMAIL: git.identity.email,
        ...(options.reportPath === undefined ? {} : { REPORT: options.reportPath }),
      }),
    );
    if (salvage.exitCode === EMPTY) return { kind: "empty" };
    if (salvage.exitCode !== 0) {
      return { kind: "failed", error: said("the salvage of the uncommitted tree", salvage) };
    }
    const sha = salvage.output.trim();
    if (!/^[0-9a-f]{7,64}$/.test(sha)) {
      return {
        kind: "failed",
        error: `the salvage commit answered oddly: ${brief(salvage.output)}`,
      };
    }
    return { kind: "salvaged", sha };
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

    // On the attempt branch, as the boot left the agent: the tip is the last
    // round's snapshot (or the agent's own HEAD, when a clean round pushed
    // it). A snapshot is unwrapped — HEAD back to the agent's own commit, the
    // snapshot's tree left in the index and the working tree — so the agent
    // finds its history as it made it and its uncommitted edits uncommitted.
    // The sha and subject reported are the tip's: what was restored FROM.
    const checkout = await shell.execLine(
      'git checkout -q -B "$BRANCH" FETCH_HEAD && git rev-parse HEAD && git log -1 --format=%s && ' +
        'if [ "$(git log -1 --format=%s)" = "$MSG" ] && git rev-parse -q --verify "HEAD~1" >/dev/null; ' +
        "then git reset -q --soft HEAD~1; fi",
      varsOf(git, { BRANCH: git.branch, MSG: WIP_COMMIT_SUBJECT }),
    );
    if (checkout.exitCode !== 0) return failed(said("git checkout", checkout));
    const lines = checkout.output.trim().split("\n");
    const sha = (lines[0] ?? "").trim();
    const subject = lines.slice(1).join(" ").trim();
    if (sha === "") return failed(`git rev-parse HEAD answered nothing: ${brief(checkout.output)}`);

    if (git.setup !== undefined) {
      // The one LONG line of the restore (see HostShell.execLong): through
      // the process doors when the shell has them, never through the run
      // door's bounding `head -c` — the tick cni rule is to carry the
      // minutes-long install on the doors that already hold minutes-long
      // commands, not to widen any bound or timeout.
      const setup = await (shell.execLong ?? shell.execLine)(git.setup, varsOf(git, {}));
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
  /**
   * The host-side ready check run before EVERY round's request (tick 4fs): a
   * container destroyed between rounds boots empty and no other path sees it.
   * Wire it to `FactorySandboxEnv.ensureWorkspaceReady()`; without it the
   * extension checks nothing and the between-rounds loss stays a boundary.
   */
  readonly ensureReady?: () => Promise<WorkspaceReadyOutcome>;
  /** Every restore the ready check performed, for the host's log and the tests. */
  readonly onRestore?: (outcome: RestoreOutcome) => void;
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
        /**
         * The pre-round ready check (tick 4fs): a container destroyed
         * BETWEEN rounds boots empty, and only the host can see it — no
         * harness call is in flight to fail. Restore BEFORE the round's
         * request goes out; nothing is lost, because the last round's wip
         * already landed, so the round continues without a word to the
         * model. A failed restore THROWS, like a failed push below: the
         * host must see a workspace it could not rebuild, not a round
         * running on an empty box.
         */
        async beforeRequest(_request, _api, _context) {
          if (options.ensureReady === undefined) return undefined;
          const ready = await options.ensureReady();
          if (ready.kind !== "ready") options.onRestore?.(ready);
          if (ready.kind === "failed") {
            throw new Error(`the pre-round ready check failed: ${ready.error}`);
          }
          return undefined;
        },
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
