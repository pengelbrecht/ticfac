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

## What comes next (the epic's steps)

4. Workspace checkpoints (`afterTools` wip commits, restore on a lost
   container).
5. The worker contract as hooks and phases.
6. The `WorkerAgent` DO host — this package's `HarnessStorage` DO is its
   seed, and it is what wires `FactorySandboxEnv` to the SANDBOXES_V1 stub
   the `run` door was built for.
7. The local Node host, on `createGuardedNodeExecutionEnv`.
