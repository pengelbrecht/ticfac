# ticfac-harness

The pi-durable worker harness package (epic 43y, step 1 of
[docs/spikes/n0b-round2-pi-durable.md](../docs/spikes/n0b-round2-pi-durable.md)):
the single place pi-durable lands in this repository, so its API churn
("the API changes without notice", 0.0.1 to 1.0.2 in 15 days) lands in one
place and fails CI here instead of in a run.

What is in it today:

- **Pinned versions.** `@earendil-works/{pi-durable,pi-ai,chord}` are pinned
  EXACT in `package.json`, behind the release-age exemption in
  `pnpm-workspace.yaml` (the packages are three days old at adoption; nothing
  moves until a tick moves it).
- **`DurableObjectSqliteDatabase`** (`src/storage/durable-object-sqlite.ts`):
  pi-durable's `SqliteDatabase` facade over a Durable Object's own
  `ctx.storage.sql` — the ~25-line adapter the n0b round-2 prototype proved in
  staging. pi-durable's own `SqliteStorage` runs unchanged on top of it, so
  every commit is a `ctx.storage.transaction`, acknowledged only after the
  platform has durably stored and replicated it.
- **The conformance suite** (`test/storage-conformance.test.ts`): pi-durable's
  own storage conformance cases, each run inside its own DO instance (a fresh
  instance is a fresh SQLite database, the per-case isolation the suite
  assumes) against the adapter, inside real workerd.
- **The transcript replay test** (`test/transcript-replay.test.ts`): the full
  harness stack — pi-ai `Models`, the built-in generation and tool tasks, a
  scripted two-turn conversation with a tool call — driven against pi-ai's
  faux provider, no network, no credentials. It asserts what the harness
  REPLAYED to the provider, not just what it returned.
- **The Workers AI gateway provider** (`src/gateway/workers-ai.ts`, tick
  oq4): `workersAIGatewayProvider` overrides pi's built-in
  `cloudflare-workers-ai` provider so every catalog model is addressed at the
  factory's `<gateway>/workers-ai/v1` on the run's gateway token (resolved at
  every request), with GLM 5.3's `maxTokens: 65536` and
  `thinkingFormat: "deepseek"` corrections; `gatewayModelRef` turns a routed
  model id into the harness's `ModelRef` and refuses anything that is not
  Workers AI (never claude in the cloud). Unit-tested against a recording
  stand-in for the gateway route (`test/gateway-provider.test.ts`), and proved
  on staging against the real route: attribution in the AI Gateway's own logs,
  and a revoked token stopping the next request (`proof/README.md`).

## The worker contract as hooks (epic 43y step 5, tick pom)

- **`workerOnYield`** (`src/worker-contract.ts`): the early-exit nudge
  (tick 060) and the report linter pushback (#183) as `onYield` follow-ups
  in the SAME conversation — the port of `image/worker.sh`'s
  nudge/pushback loop (which re-prompted a relaunched process) onto the
  durable path, where `{ continue }` simply appends the next user message
  and the run continues. The bounds are the shell's own (2 and 2, the Go
  supervisor's `MaxNudges`/`MaxLintPushbacks`), counted per conversation
  in the harness process — a `continue` hands the run to a successor
  generation, so a task memo would reset every follow-up; the process
  scope matches the shell's own per-boot loop, and the wall is the outer
  bound.
- **`armWallDeadline`**: the wall as `abort()` — the host arms it before
  the conversation runs and cancels it when it settles; an armed timer
  left behind would abort the NEXT submission. Note the semantic the host
  (step 6) has to live with: `Conversation.abort()` marks and waits for
  the conversation to go idle, so a tool already running finishes first —
  abort stops the NEXT model call, not the tool in flight.
- **The container's half** of the step is `image/worker.sh --boot` /
  `--finish`, and the args and markers both halves share are pinned in
  `contracts/worker-boot-contract.json` 1.2.0, asserted by
  `internal/sandboximage` and `cloudflare/src/worker-boot.ts`.

The suites run in CI (the `typescript` job of `.github/workflows/ci.yml`),
not in the per-tick gate: same split as the factory suite — the gate covers
contracts and types, CI covers behaviour.

## Running locally

```bash
pnpm install --frozen-lockfile
pnpm lint && pnpm typecheck && pnpm test
```

`wrangler.toml` is test-only: it exists so `@cloudflare/vitest-pool-workers`
runs the suites inside real workerd against real DO SQLite. This worker is
never deployed.

## The execution environments (epic 43y step 3, tick kgk)

- **`FactorySandboxEnv`** (`src/env/factory-sandbox.ts`): a pi-durable
  `ExecutionEnv` over the factory's FactorySandbox Durable Object. The
  `FileSystem` subset the built-in tools use runs as SHORT container
  commands through the `FactorySandbox.run` door (cloudflare/src/
  factory-sandbox.ts) — one RPC per operation, output bounded by the door —
  and the `Shell` is the replay-safe tracked bash: a call carrying
  `TICFAC_BASH_NONCE` (the bash tool's `prepare` mints it and memoises it
  durably) is embedded in the container command, and a replay finds the
  still-running process by nonce through `listProcesses` and REATTACHES
  instead of running the command again.
- **`createTrackedBashTool`** (`src/tools/tracked-bash.ts`): pi-durable's
  own `bash` tool made `replay: "safe"` through that nonce memo — the
  recovery rerun reads the same nonce and reattaches.
- **`createGuardedNodeExecutionEnv`** (`src/env/node.ts`, the package's one
  Node-only entry, exported as `./env/node`): pi-durable's
  `NodeExecutionEnv` with the same boundary guard, for the local host.
- **The boundary guard** (`src/env/boundary-guard.ts`): the same `tk` shim
  the container installs (image/worker.sh, tick dxk) — same refusal (pinned
  in contracts/worker-boot-contract.json, asserted by the node suite
  against the contract), same read pass-list, same ledger beside the shim —
  installed by both envs and on the PATH of every command they run.

## Workspace checkpoints (epic 43y step 4, tick dwn)

- **`workspaceCheckpointExtension`** (`src/workspace/checkpoints.ts`): an
  `afterTools` hook on pi-durable's generation task that snapshots the
  workspace after every tool round — `wip: tool round`, built in a throwaway
  index on top of the agent's own HEAD and force-pushed over the previous
  round's snapshot, never committed on the agent's branch (tick xd3: a wip on
  the branch made the agent's own commit answer "nothing to commit", the
  model rewrote the history it could not explain, and the finish phase's
  fast-forward push was refused) — to the ATTEMPT branch, the run's write ref.
  A clean round pushes the agent's own HEAD when its commits moved, and
  `retireWipSnapshot` puts the branch back on that HEAD before the finish
  phase; a restore unwraps the snapshot (HEAD the agent's commit, the
  snapshot's tree uncommitted). That is the same ref
  `ticfac settle --carry-work` reads (internal/reconcile): the carried-work
  mechanism at tool-round granularity, so a stopped attempt's carried work
  now carries its in-flight edits too. A round that changed nothing commits
  and pushes nothing; a failed push is reported to `HarnessOptions.onReport`
  — never silent, because the run's durability rests on it.
- **The lost-container restore** (same module, wired into
  `FactorySandboxEnv`): a container lost mid-command — its process gone
  (`missing`), or ended with no exit code (`lost`) — is restored from the
  attempt branch: clear the fresh (empty) box, clone the branch, check out
  its tip (the last wip commit, or the base before the first one), run the
  setup command, then hand the model a `restored to <sha> …; re-check and
  re-run` result, so the turn continues minus edits since the last round.
  A tracked-bash replay whose process is gone checks the workspace is there
  before re-starting on it, so a container that died while no harness
  watched is restored too. `restoreLostWorkspace()` is public for the host
  that owns the container's lifetime (epic step 6).
- **The pre-round ready check** (tick 4fs, same extension): a container
  destroyed BETWEEN tool rounds — no harness call in flight, so none of the
  restore triggers above can fire — boots empty, and the next round's first
  file operation would fail ENOENT instead of restoring. The extension's
  `beforeRequest` hook runs `ensureReady` before EVERY round's request:
  wire it to `FactorySandboxEnv.ensureWorkspaceReady()`, which verifies the
  ready marker (one short command) and restores from the attempt branch
  when it is gone — nothing is lost, the last round's wip already landed.
  `onRestore` reports every restore the check performed. A loss DURING a
  round, under a file operation, is the remaining boundary, for the host
  that owns the container's lifetime (epic step 6).
- The tests carry the acceptance criteria — destroy the container mid-turn;
  the next turn sees both edits; destroy it between rounds and the ready
  check restores before the next round — twice, in the two halves this
  package splits every claim into: **workerd** (`test/workspace-checkpoints.
  test.ts`) proves the door's RPC shapes, the hook wiring and the restore's
  command sequence on the scripted door; **node** (`test/node/
  workspace-checkpoints.test.ts`) proves the git itself on a real origin —
  the wips really land on the branch, and the model's next reads really see
  both edits from the restored tree — through the full Harness, real
  processes and real git.

The tests are split by what they can prove where:

- **workerd** (`vitest run`): the RPC shapes — one run per file operation,
  the nonce marker, reattach without a second start, timeouts that kill,
  aborts that do not — and, in `tracked-bash-resume.test.ts`, the tick's
  acceptance test: a full pi-durable harness KILLED mid-bash whose
  recovery, in a second harness over the same storage, reattaches and
  finishes the command exactly once.
- **node** (`vitest run -c vitest.node.config.ts`): the shell behaviour —
  the guard really refusing tracker writes against real bash, and the
  env's command lines really reading, writing, listing, classifying — over
  a local stand-in door (test/node/local-sandbox-door.ts) that spawns real
  processes. The node suite exists because a shell behaviour test that
  never runs a shell certifies nothing, and workerd cannot run one.

## The local worker host (epic 43y step 7, tick hpk)

- **`runLocalWorker`** (`src/local/worker-host.ts`): the process the Go
  executor's `pi` runner IS on a local run — one Node process per worker,
  spawned by the supervisor, running the whole conversation on pi-durable
  over LOCAL SQLite (`openNodeSqliteStorage`, one `worker.sqlite` per
  attempt in its state directory), tools in the worktree through
  `createGuardedNodeExecutionEnv`. It assembles the pieces the earlier steps
  built — `workerOnYield` and `armWallDeadline` (tick pom),
  `workspaceCheckpointExtension` (tick dwn) — plus the local rung's model
  access: pi-ai's own `cloudflare-workers-ai` provider with the GLM
  catalog corrections, credentials resolved exactly the way the pi CLI
  resolves them — the stored credential in pi's own
  `~/.pi/agent/auth.json` first, the ambient environment as pi-ai's own
  fallback (`src/local/pi-auth-store.ts`). There is no factory gateway
  locally: no run token, no exchange, and the harness holds no credential
  the host did not already have.
- **The steer socket** (`src/local/steer-socket.ts`): a Unix domain socket
  beside the storage, and the one door the Go supervisor has into a RUNNING
  conversation. One JSON line in (`{"requestId","text"}`), one JSON line out
  (`{"ok":true}` once the steer is DURABLY admitted — pi-durable's
  idempotent `submit({ whenBusy: "steer" })`). The supervisor's STUCK NUDGE
  goes through it: the tick's acceptance criterion, "the stuck nudge is a
  steer" — input placed after the current tool round, the same conversation,
  no relaunch. The protocol is pinned from all three sides: this server, the
  node suite's `steerOnce` client, and Go's client
  (internal/exec/subprocess/steer.go).
- **`src/local/main.ts`**: the entry the supervisor execs. No build step:
  `runtime/register.mjs` installs a resolve hook that maps the package's
  `./x.js` imports onto its `./x.ts` sources, so plain `node
  --experimental-strip-types` runs them (Node ≥ 22.6; default from 23.6).
  The config (`worker.json`, written by the executor beside the attempt
  record) is the whole interface; the argv carries only its path, the model
  and the message. Exit codes: 0 settled, 1 unanswered, 2 cannot-run.
- The node suite's `local-host.test.ts` drives the real entry as a real
  child process over real git and real SQLite: a whole worker to settlement,
  the steer placed after a live tool round, a `kill -9` mid-tool resumed
  from the storage without re-running the tool, and a follow-up relaunch
  continuing the SAME conversation.

## The attempt host (epic 43y step 6, tick xd3)

- **`WorkerAttemptHost`** (`src/host/worker-attempt.ts`): one worker attempt
  driven end to end from a durable record — the container's
  `ticks-worker --boot` (its process id recorded before it is polled, so a
  host that dies mid-boot reattaches), the conversation on the prompt the boot
  printed between its markers (submitted under a fixed `requestId`, resumed
  by `harness.resume()` in a later life), and `ticks-worker --finish
  <status>` (0 done, 124 the wall fired, 1 otherwise) once it settles. The
  finish phase's exit code is the attempt's — what the all-in-one
  entrypoint's always was. The wall is ABSOLUTE (fixed at start), steer
  places input after the running tool round, and reclaim stops the attempt
  where it stands without a finish. Before the finish the host restores a
  workspace lost after the last round and rewrites the boot's branch record
  for the finish phase's own process.
- **The cloud host** is the factory's `WorkerAgent` Durable Object
  (`cloudflare/src/worker-agent.ts`), which links this package as
  `ticfac-harness` and reaches pi-durable only through `src/host/cloud.ts`:
  `openDurableObjectStorage` (pi-durable's `SqliteStorage` over its own DO
  SQLite), `gatewayModelAccess` (the gateway provider, the only provider) and
  `watchAttemptEvents` (the conversation's agent events for its watch
  sockets). `ticfac-harness/testing` (`src/host/testing.ts`) is a faux model
  under the gateway provider's id, for the factory's own suites only.
- Tested here (`test/worker-attempt-host.test.ts`, workerd, the real Harness
  over the scripted door and a faux model): the whole attempt, a boot that
  stops, a boot with no handoff, a container lost mid-boot, a host killed
  mid-tool and resumed in a new life (the prompt once, the tool once), a
  steer, the wall, a reclaim. The factory's suites drive the real
  `WorkerAgent` through the dispatch door (`cloudflare/test/
  worker-agent.test.ts`) and the door's hosted routes against a fake agent
  (`cloudflare/test/worker-agent-door.test.ts`).

## What comes next (the epic's steps)

8. Watch surfaces (`ticfac watch` from the commit stream), and the proof
   runs (kill the host mid-tool, destroy the container mid-turn, deploy
   mid-run) — the wip checkpoints, the restore and the local host's resume
   here are the machinery those runs will exercise; the WorkerAgent's watch
   socket and steer route are what `ticfac watch` and the stuck nudge reach
   in the cloud, as the local host's steer socket is what they reach locally.
