import { env } from "cloudflare:workers";

// This file is vitest `setupFiles`, so it runs once per test file — 80 times a
// run. Tick uhe went looking for the ~98 s of `setup` the suite reported on
// the theory that it was the migration set being replayed per file. It was
// not: the replay cost ~15 ms, and the `cloudflare:test` import this file
// made cost ~630 ms, once per file, for one helper function. Dropping that
// import took the suite's reported setup from 97.81 s to ~1 s, where it has
// stayed (re-measured 2026-10-07 on pool-workers 0.22.0 / vitest 4.1.11:
// 208.84 s wall, setup 1.08 s, 80 files, 28 migrations).
//
// Applying the set per file is now the only shape the runtime offers, and
// the reason is its isolation model rather than the pool being wasteful:
// vitest 4 boots a FRESH workerd per test file — one Miniflare per file, an
// in-memory D1 per instance; NODE_DEBUG=vitest-pool-workers prints
// "Starting runtime" once per file — so there is no shared database to seed
// once per run, and the only vitest-level switch that shares a runner across
// files (`isolate: false`) is exactly the cross-file bleed tick 5qj
// recorded. That guarantee is now guarded by the suite itself:
// test/d1-file-isolation-a.test.ts and -b write marker rows and fail the
// moment one file can see another's.
//
// What the per-file apply costs, measured 2026-10-07 one file at a time at
// host load ~22 (median of five runs, vitest's own `setup`):
//
//   setup file that is empty                                    ~16 ms
//   setup file that only imports "cloudflare:workers"           ~59 ms
//   this file, import + one batch of all 28 migrations           ~68 ms
//
// The import is the floor — `env` is reachable only through a module import
// — and all 28 migrations cost ~9 ms on top of it, in ONE db.batch(). The
// suite's remaining per-file cost is not here: it is `import` (135.71 s of
// the 208.84 s run), which 58 of the 80 test files pay to pull
// `cloudflare:test` into their own module graphs (the other 22 never name
// it). Making that stop is a question about how the pool serves its own
// module graph, not about D1.
const MIGRATIONS_TABLE = '"d1_migrations"';

// Reproduces `applyD1Migrations`' bookkeeping exactly, so the helper is still a
// no-op afterwards — db.test.ts asserts that by re-applying the set twice and
// checking the schema is unchanged.
await env.DB.prepare(
  `CREATE TABLE IF NOT EXISTS ${MIGRATIONS_TABLE} (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT UNIQUE,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
  );`,
).run();

const recorded = await env.DB.prepare(`SELECT name FROM ${MIGRATIONS_TABLE};`).all<{
  name: string;
}>();
const applied = new Set(recorded.results.map(({ name }) => name));

// Setup files may run more than once, so an already-applied migration is
// skipped rather than replayed — the same guard the helper applies, and what
// makes re-running this file safe rather than a UNIQUE violation on `name`.
const insert = env.DB.prepare(`INSERT INTO ${MIGRATIONS_TABLE} (name) VALUES (?);`);
const pending: { name: string; queries: string[] }[] = env.TEST_MIGRATIONS.filter(
  ({ name }) => !applied.has(name),
);
const statements = pending.flatMap((migration) => [
  ...migration.queries.map((query) => env.DB.prepare(query)),
  insert.bind(migration.name),
]);

if (statements.length > 0) await env.DB.batch(statements);
