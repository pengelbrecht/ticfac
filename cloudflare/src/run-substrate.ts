/**
 * Which container substrate a run is on, and the binding that routes each of
 * its containers there (epic umq, tick 1hq).
 *
 * A run is submitted on one substrate and stays on it (migration 0023): the
 * Sandbox SDK 0.x `Sandbox` class (SANDBOXES — every run before umq, and
 * every run that does not ask) or FactorySandbox on the `durable_object`
 * policy (SANDBOXES_V1, `substrate: "do_v1"`). The same container name
 * addresses a different Durable Object in each namespace, so the choice is
 * read per RUN, never per request: a run whose later boot looked in the other
 * namespace would find none of its live containers.
 *
 * The routing is done once, under the seam, by container NAME: every name the
 * factory boots begins with its run id (`run_<hex>`, which has no `-`), then
 * `-` — the orchestrator's `<run>-<boot>`, an attempt's `<run>-<tick>-<n>`, a
 * role job's `<run>-<tick>-<n>-<slot>`. So every call site that already asks
 * `sandboxBinding(env)` for a container — the dispatch door, the Workflow,
 * the reclaim, the sweep — routes correctly without being told the run.
 */

import type { InstanceSize } from "./factory-sandbox";
import type { OrchestratorSandbox, SandboxBinding } from "./sandbox";

/** The substrates a run can be on. `sdk0` is the default and has no row. */
export type RunSubstrate = "sdk0" | "do_v1";

/** FactorySandbox on the durable_object policy (SANDBOXES_V1). */
export const DO_V1: RunSubstrate = "do_v1";

export function isRunSubstrate(value: unknown): value is RunSubstrate {
  return value === "sdk0" || value === "do_v1";
}

/** What a run's record says about its containers. */
export type RunSubstrateRecord = { substrate: RunSubstrate; image: string | null };

const SDK0: RunSubstrateRecord = { substrate: "sdk0", image: null };

/** Records a run's substrate (and image pin) at submit. Idempotent. */
export async function recordRunSubstrate(
  db: D1Database,
  runID: string,
  substrate: RunSubstrate,
  image: string | null = null,
): Promise<void> {
  if (substrate === "sdk0") return;
  await db
    .prepare(
      `INSERT INTO run_substrate (run_id, substrate, image) VALUES (?, ?, ?)
       ON CONFLICT(run_id) DO NOTHING`,
    )
    .bind(runID, substrate, image)
    .run();
}

/** The run's substrate: `sdk0` when the run has no row. */
export async function readRunSubstrate(db: D1Database, runID: string): Promise<RunSubstrateRecord> {
  const row = await db
    .prepare("SELECT substrate, image FROM run_substrate WHERE run_id = ?")
    .bind(runID)
    .first<{ substrate: string; image: string | null }>();
  if (row === null || !isRunSubstrate(row.substrate)) return SDK0;
  return { substrate: row.substrate, image: row.image ?? null };
}

// ------------------------------------------------------------- the names ---

/** The run a container name belongs to: everything before its first `-`. */
export function runIDOfSandboxName(name: string): string {
  const dash = name.indexOf("-");
  return dash === -1 ? name : name.slice(0, dash);
}

/** What a container is for, read from its name. */
export type ContainerJobKind = "orchestrator" | "implement" | "resolve" | "repair";

/**
 * The job a container name is for. `<run>-<boot>` is the run's orchestrator
 * (sandboxName in src/sandbox.ts); `<run>-<tick>-<n>` is an attempt's
 * implement job; `<run>-<tick>-<n>-<slot>` is a role job, whose slot starts
 * with the job's own name (`resolve-…`, `repair-…`; attemptJobSlot).
 */
export function jobKindOfSandboxName(name: string): ContainerJobKind {
  const parts = name.split("-");
  if (parts.length === 2 && /^[0-9]+$/.test(parts[1] ?? "")) return "orchestrator";
  const slot = parts.slice(3).join("-");
  if (slot.startsWith("resolve")) return "resolve";
  if (slot.startsWith("repair")) return "repair";
  return "implement";
}

/**
 * The instance each kind of job starts on (`durable_object` policy only; the
 * 0.x application is one `instance_type` for all). One table, so a size is a
 * one-line decision.
 *
 *  - implement and repair do gate-like work — build, test, lint — so they get
 *    the most CPU there is (standard-4: 4 vCPU, 12 GiB, 20 GB).
 *  - resolve merges two branches and re-runs little: disk-heavy, CPU-light
 *    (2 vCPU, 8 GiB, the 20 GB disk maximum — the multi-GB image plus a
 *    checkout and its caches is what fills a container).
 *  - the orchestrator runs the reconciler and the per-tick gate in-container
 *    (v1d), so it is sized like gate work.
 */
export const INSTANCE_BY_JOB_KIND: Record<ContainerJobKind, InstanceSize> = {
  orchestrator: "standard-4",
  implement: "standard-4",
  repair: "standard-4",
  resolve: { vcpu: 2, memoryMib: 8192, diskMb: 20000 },
};

// ------------------------------------------------------------- the router ---

type GetOptions = Parameters<SandboxBinding["get"]>[1] & {
  instance?: InstanceSize;
  pinnedImage?: string;
};

/**
 * Which containers of a `do_v1` run go to SANDBOXES_V1. Workers since 1hq;
 * the orchestrator stays on SANDBOXES until v1d moves it.
 */
export type RouteScope = { orchestrator: boolean };

export const ROUTE_WORKERS_ONLY: RouteScope = { orchestrator: false };

/**
 * One binding over both substrates: each `get` reads the run's record
 * (once per run per binding) and addresses the container in the namespace
 * the run was submitted on, with the instance its job kind starts on and the
 * image the run pinned.
 */
export function routedSandboxBinding(
  legacy: SandboxBinding,
  v1: SandboxBinding,
  lookup: (runID: string) => Promise<RunSubstrateRecord>,
  scope: RouteScope = ROUTE_WORKERS_ONLY,
): SandboxBinding {
  const records = new Map<string, Promise<RunSubstrateRecord>>();
  const recordOf = (runID: string): Promise<RunSubstrateRecord> => {
    let found = records.get(runID);
    if (found === undefined) {
      found = lookup(runID);
      // A failed read is not cached: the next get asks again.
      found.catch(() => records.delete(runID));
      records.set(runID, found);
    }
    return found;
  };
  return {
    async get(name: string, options?: GetOptions): Promise<OrchestratorSandbox> {
      const record = await recordOf(runIDOfSandboxName(name));
      const kind = jobKindOfSandboxName(name);
      if (record.substrate !== DO_V1 || (kind === "orchestrator" && !scope.orchestrator)) {
        return legacy.get(name, options);
      }
      return v1.get(name, {
        ...options,
        instance: options?.instance ?? INSTANCE_BY_JOB_KIND[kind],
        ...(record.image === null || options?.pinnedImage !== undefined
          ? {}
          : { pinnedImage: record.image }),
      } as GetOptions);
    },
  };
}
