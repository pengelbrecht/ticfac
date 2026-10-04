import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { SqliteStorage } from "@earendil-works/pi-durable/storage/sqlite";
import type { StorageConformanceProvider } from "@earendil-works/pi-durable/testing";
import { createStorageConformance } from "@earendil-works/pi-durable/testing";
import { storageConformanceAssertions } from "../testing/assertions.js";
import {
  DurableObjectSqliteDatabase,
  type DurableObjectSqliteTarget,
} from "./durable-object-sqlite.js";

/**
 * pi-durable's storage conformance suite against the `DurableObjectSqlite`
 * adapter.
 *
 * Each case runs INSIDE its own Durable Object instance, against that
 * instance's own `ctx.storage.sql` — a fresh instance is a fresh SQLite
 * database, which is the per-case isolation the suite's provider contract
 * expects. The adapter is exercised exactly where the epic will use it
 * (docs/spikes/n0b-round2-pi-durable.md): a DO hosting pi-durable's
 * `SqliteStorage` unchanged over a ~25-line `SqliteDatabase` facade.
 *
 * The cases themselves are pi-durable's own, runner-independent
 * (`createStorageConformance`); the assertions are ours (no `expect` inside a
 * DO), and the result travels back to the Vitest test that drove the case.
 */

/** One case's verdict, as returned to the driving test. */
export interface ConformanceCaseResult {
  readonly name: string;
  readonly ok: boolean;
  readonly error?: string;
}

const never = async (): Promise<void> => {
  throw new Error("the names-only provider is never invoked");
};

/** The suite's case names, so a driver can register one test per case. */
export function storageConformanceCaseNames(): readonly string[] {
  return createStorageConformance({
    assertions: storageConformanceAssertions,
    withStorage: never satisfies StorageConformanceProvider,
  }).map((c) => c.name);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** Run one conformance case by name against a Durable Object's own SQLite. */
export async function runStorageConformanceCase(
  target: DurableObjectSqliteTarget,
  name: string,
): Promise<ConformanceCaseResult> {
  const withStorage: StorageConformanceProvider = async (use) => {
    const storage = await SqliteStorage.open(new DurableObjectSqliteDatabase(target));
    try {
      await use(storage);
    } finally {
      await storage.close(BACKGROUND_CONTEXT);
    }
  };
  const cases = createStorageConformance({ assertions: storageConformanceAssertions, withStorage });
  const match = cases.find((c) => c.name === name);
  if (match === undefined) {
    return { name, ok: false, error: `unknown conformance case: ${name}` };
  }
  try {
    await match.run();
    return { name, ok: true };
  } catch (error) {
    return { name, ok: false, error: errorMessage(error) };
  }
}
