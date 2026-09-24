/**
 * The driver of the Go end-to-end door harness (tick 6gr): one Node process
 * that builds the REAL factory Worker (src/index.ts through the wrapper in
 * worker.ts) with esbuild, serves it in real workerd through miniflare on a
 * real local HTTP port, seeds its D1 index and dispatch lease through the
 * worker's own modules, and then holds the door open until the Go test says
 * to close it.
 *
 * The protocol with the Go test (internal/exec/cloudflaresandbox) is one JSON
 * line on stdout:
 *
 *   {"ready":true,"url":"http://127.0.0.1:NNNN", ...}
 *
 * — the only line the Go side parses, printed once the door is seeded and
 * serving. Everything else the runtime prints goes to stderr, so the channel
 * stays one line. The door closes when stdin ends or carries the word
 * `shutdown`: the Go test writes that to make the door unreachable for the
 * leg that proves an unreachable door is not read as "no sandbox".
 *
 * Run from the cloudflare/ directory (the Go test does exactly that):
 *
 *   node test/go-door-harness/run.mjs
 *
 * Skips are not the harness's business: it is started only after the Go test
 * has checked that node and this directory's node_modules exist.
 */
import { existsSync, readFileSync } from "node:fs";
import { mkdir, readdir, rm } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cloudflareRoot = resolve(here, "../../"); // cloudflare/
const migrationsDir = join(cloudflareRoot, "migrations");

// ------------------------------------------------------------- the bundles ---

/** Builds the wrapper worker and the seeding entry into a throwaway directory. */
async function buildBundles() {
  const esbuild = await import("esbuild");
  const { tmpdir } = await import("node:os");
  // A per-process directory under the system temp dir, removed when the door
  // closes (and harmless litter when it is killed, which is why it lives in
  // the temp dir rather than beside the sources). TICFAC_DOOR_HARNESS_BUILD
  // repoints it for a session that wants the bundles kept for inspection —
  // a repointed directory is never removed, because it is not ours.
  const outDir =
    process.env.TICFAC_DOOR_HARNESS_BUILD ?? join(tmpdir(), `ticfac-door-harness-${process.pid}`);
  await mkdir(outDir, { recursive: true });

  // The worker: real src/index.ts code through the harness wrapper, kept as
  // ES modules with workerd's own builtins external — the same shape wrangler
  // hands miniflare for `wrangler dev`.
  await esbuild.build({
    entryPoints: [join(here, "worker.ts")],
    outfile: join(outDir, "worker.mjs"),
    bundle: true,
    format: "esm",
    platform: "neutral",
    external: ["cloudflare:*", "node:*"],
    sourcemap: false,
    logLevel: "silent",
  });

  // The seeder: src/db.ts and src/gateway.ts bundled for Node, so the run
  // index and the gateway credential are seeded through the worker's own
  // inserts rather than a second spelling of them.
  await esbuild.build({
    entryPoints: [join(here, "seed.ts")],
    outfile: join(outDir, "seed.mjs"),
    bundle: true,
    format: "esm",
    platform: "node",
    sourcemap: false,
    logLevel: "silent",
  });

  return outDir;
}

// --------------------------------------------------------------- the seeds ---

/** The run a door dispatch belongs to: one row in the worker's own index. */
function runRow({ runID, project, epic, baseSHA }) {
  return {
    run_id: runID,
    project,
    epic,
    base_sha: baseSHA,
    requested_by: "go-door-harness",
    state: "running",
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  };
}

/** Applies the deployment's own D1 migrations, in file order, from Node. */
async function applyMigrations(db) {
  // The same splitter `wrangler d1 migrations apply` and the worker suite's
  // readD1Migrations use — never a homemade `;` split: this directory's
  // comments carry semicolons of their own.
  const { unstable_splitSqlQuery } = await import("wrangler");
  const names = (await readdir(migrationsDir)).filter((name) => name.endsWith(".sql")).sort();
  for (const name of names) {
    const raw = readFileSync(join(migrationsDir, name), "utf8");
    const queries = unstable_splitSqlQuery(raw);
    if (queries.length === 0) continue;
    await db.batch(queries.map((query) => db.prepare(query)));
  }
}

/** Finds one free local port, bound and released, for the door to listen on. */
async function freePort() {
  const net = await import("node:net");
  return await new Promise((resolvePromise, rejectPromise) => {
    const server = net.createServer();
    server.on("error", rejectPromise);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolvePromise(port));
    });
  });
}

// ----------------------------------------------------------------- the run ---

async function main() {
  if (!existsSync(join(cloudflareRoot, "node_modules", "miniflare"))) {
    console.error("cloudflare/node_modules/miniflare is missing: run pnpm install in cloudflare/");
    process.exit(2);
  }

  const outDir = await buildBundles();
  const { Miniflare, convertV4MiniflareOptions } = await import("miniflare");

  // The port is picked before the worker boots so the factory's own base URL
  // (FACTORY_BASE_URL, what the container boot's git and gateway URLs are
  // derived from) can name the door's real address in the same options — one
  // boot, no re-configuration once the runtime is live.
  const port = await freePort();
  const url = `http://127.0.0.1:${port}/`;

  // The v4 options shape is the one wrangler dev itself hands miniflare, so
  // the harness states its bindings the way the deployed config states them.
  // Only the bindings the door needs are declared: DB (the run index and the
  // credential rows), RUN_ROOMS (the real dispatch lease), and the plain vars
  // sandboxExecutorDepsFromEnv reads. SANDBOXES is deliberately absent — the
  // wrapper substitutes the fake binding per request, the same substitution
  // the worker's own vitest suite makes.
  const options = () =>
    convertV4MiniflareOptions({
      host: "127.0.0.1",
      port,
      verbose: false,
      logRequests: false,
      scriptPath: join(outDir, "worker.mjs"),
      modules: true,
      modulesRoot: outDir,
      compatibilityDate: "2026-08-11",
      compatibilityFlags: ["nodejs_compat"],
      bindings: {
        FACTORY_BASE_URL: url,
        GITHUB_TOKEN: "gh-door-harness",
      },
      d1Databases: { DB: join(outDir, "db.sqlite") },
      durableObjects: { RUN_ROOMS: { className: "RunRoom", useSQLite: true } },
    });

  const mf = new Miniflare(options());
  await mf.ready;

  const db = await mf.getD1Database("DB");
  await applyMigrations(db);

  // Seed through the worker's own modules: the run row (insertRun) and the
  // run's gateway credential (issueWorkerRunToken — minted, never rotated,
  // the same credential the orchestrator container would hold).
  const seed = await import(pathToFileURL(join(outDir, "seed.mjs")).href);
  const runID = `run_door_${process.pid}`;
  const project = "example-org/example-repo";
  const epic = "yoh";
  const baseSHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2";
  await seed.insertRun(db, runRow({ runID, project, epic, baseSHA }));
  const issued = await seed.issueWorkerRunToken(
    { DB: db },
    {
      run_id: runID,
      tick_id: epic,
      attempt: 1,
    },
  );

  // The dispatch lease: the real RunRoom, addressed the way roomFor addresses
  // it (one room per project), acquired through the same RPC the run
  // machinery acquires through — never a lease row faked beside the door.
  const rooms = await mf.getDurableObjectNamespace("RUN_ROOMS");
  const room = rooms.get(rooms.idFromName(project));
  const lease = await room.acquireDispatchLease({ run_id: runID, epic, origin: "cloud" });
  if (!lease.ok) {
    console.error(`the dispatch lease was refused: ${JSON.stringify(lease)}`);
    await mf.dispose();
    process.exit(2);
  }

  const say = (line) => process.stdout.write(`${JSON.stringify(line)}\n`);
  say({
    ready: true,
    url,
    observe: `${url}__door_harness/sandboxes`,
    run_id: runID,
    project,
    epic,
    base_sha: baseSHA,
    token: issued.token,
  });

  // Hold the door open until the Go test closes stdin or asks for a shutdown;
  // either way the door stops listening first (dispose), the build and
  // persistence litter is removed second, and the process ends last, so the
  // caller can wait on this process's exit and know both the port and the
  // files are gone. Both stdin events (the shutdown word and stdin ending)
  // can fire for one close, so the first wins and the second does nothing.
  let closing = false;
  const close = async () => {
    if (closing) return;
    closing = true;
    try {
      await mf.dispose();
      // Only the throwaway directory this process created is removed; a
      // repointed build directory is not ours to delete.
      if (process.env.TICFAC_DOOR_HARNESS_BUILD === undefined) {
        await rm(outDir, { recursive: true, force: true });
      }
    } catch (error) {
      console.error(String(error?.stack ?? error));
    } finally {
      process.exit(0);
    }
  };
  process.stdin.on("data", (chunk) => {
    if (String(chunk).trim() === "shutdown") {
      void close();
    }
  });
  process.stdin.on("end", () => void close());
  process.on("SIGTERM", () => void close());
  process.stdin.resume();
}

main().catch((error) => {
  console.error(String(error?.stack ?? error));
  process.exit(2);
});
