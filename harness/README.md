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

Both suites run in CI (the `typescript` job of `.github/workflows/ci.yml`),
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

## What comes next (the epic's steps)

2. A gateway provider over `/api/gateway/workers-ai` with the run token.
3. Execution environments (`FactorySandboxEnv`, the replay-safe tracked
   `bash`, `NodeExecutionEnv`).
4. Workspace checkpoints (`afterTools` wip commits).
5. The worker contract as hooks and phases.
6. The `WorkerAgent` DO host — this package's `HarnessStorage` DO is its
   seed.
7. The local Node host.
