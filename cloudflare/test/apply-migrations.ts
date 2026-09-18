import { env } from "cloudflare:workers";

// This file is vitest `setupFiles`, so it runs once per test file — 53 times a
// run. Tick uhe went looking for the ~98 s of `setup` that costs, on the theory
// that it was the sixteen migrations being replayed per file. It is not.
//
// Measured one test file at a time on a quiet machine (vitest's own `setup`):
//
//   setup file that is empty                                    ~6 ms
//   setup file that ONLY imports "cloudflare:test"            ~650 ms
//   setup file that applies all 16 migrations, no import       ~15 ms
//   setup file as it was (import + applyD1Migrations)         ~630 ms
//
// So the migrations are ~15 ms and the IMPORT is ~630 ms: pulling
// `cloudflare:test` into a file's module graph costs most of a second, and this
// file was doing that to every test file in the suite for one helper function.
// Sending the migrations as one `db.batch()` rather than the helper's sixteen
// is worth ~15 ms and was not why this got faster; dropping the import was.
//
// It is only a PARTIAL win, and the reason is worth knowing before anyone
// chases the rest: 36 of the 53 test files import `cloudflare:test` themselves,
// and for those the cost does not disappear, it MOVES from `setup` to `import`
// (health.test.ts: setup 602 ms / import 8 ms became setup 14 ms / import
// 573 ms, total Duration unchanged). Only the 17 files that never name
// `cloudflare:test` actually stop paying. Making the other 36 stop is a
// question about how the pool serves its own module graph, not about D1.
//
// What did NOT change is that the schema is still built FRESH for every test
// file. @cloudflare/vitest-pool-workers 0.21 hands each file a D1 with nothing
// in it — measured, by writing a row in one file and failing to read it in the
// next — and suites here ask questions of the WHOLE database ("was there
// anything to say", loop-digest.test.ts) whose answer is wrong the moment
// another file's rows survive. Seeding once per RUN was the bigger prize and
// was the thing uhe asked for; at ~15 ms a file there is nothing left to buy
// with it.
const MIGRATIONS_TABLE = '"d1_migrations"';

// Reproduces `applyD1Migrations`' bookkeeping exactly, so the helper is still a
// no-op afterwards — db.test.ts asserts that by re-applying the set twice and
// checking the schema is unchanged.
await env.DB.prepare(
  `CREATE TABLE IF NOT EXISTS ${MIGRATIONS_TABLE} (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT UNIQUE,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
  );`
).run();

const applied = new Set(
  (await env.DB.prepare(`SELECT name FROM ${MIGRATIONS_TABLE};`).all<{ name: string }>()).results.map(
    ({ name }) => name
  )
);

// Setup files may run more than once, so an already-applied migration is
// skipped rather than replayed — the same guard the helper applies, and what
// makes re-running this file safe rather than a UNIQUE violation on `name`.
const insert = env.DB.prepare(`INSERT INTO ${MIGRATIONS_TABLE} (name) VALUES (?);`);
const pending: { name: string; queries: string[] }[] = env.TEST_MIGRATIONS.filter(
  ({ name }) => !applied.has(name)
);
const statements = pending.flatMap((migration) => [
  ...migration.queries.map((query) => env.DB.prepare(query)),
  insert.bind(migration.name),
]);

if (statements.length > 0) await env.DB.batch(statements);
