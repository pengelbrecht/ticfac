import type {
  SqliteDatabase,
  SqliteExecutor,
  SqliteValue,
} from "@earendil-works/pi-durable/storage/sqlite";

/**
 * The subset of a Durable Object's `ctx.storage` the adapter touches: the DO
 * SQLite accessor plus the storage transaction. Declared structurally, so the
 * adapter is testable against a stub and stays honest about what it needs.
 */
export interface DurableObjectSqliteTarget {
  /** DO SQLite (`ctx.storage.sql`). */
  readonly sql: SqlStorage;
  /** `ctx.storage.transaction`: every `sql` operation inside the closure is one atomic transaction. */
  transaction<T>(closure: () => Promise<T>): Promise<T>;
}

/** A value as DO SQLite accepts and returns it. */
type DoSqliteValue = ArrayBuffer | string | number | null;

/** Convert a binding the portable core may hand us into one DO SQLite accepts. */
function toDoBinding(value: SqliteValue): DoSqliteValue {
  if (typeof value === "bigint") {
    // The portable core stores ids as TEXT and sequence numbers as small
    // INTEGERs, so a bigint binding never occurs in practice; refusing it
    // would put a runtime limit pi-durable does not promise, so it converts.
    return Number(value);
  }
  if (value instanceof Uint8Array) {
    // DO SQLite binds ArrayBuffer, not views; copy so byteOffset never leaks
    // bytes of a shared buffer into the row (and the copy's buffer is a plain
    // ArrayBuffer, never a SharedArrayBuffer).
    return new Uint8Array(value).buffer;
  }
  return value;
}

/** Convert a row DO SQLite returned into the portable core's value types. */
function fromDoRow<T extends object>(
  row: Record<string, DoSqliteValue> | undefined,
): T | undefined {
  if (row === undefined) {
    return undefined;
  }
  const out: Record<string, SqliteValue> = {};
  for (const [column, value] of Object.entries(row)) {
    out[column] = value instanceof ArrayBuffer ? new Uint8Array(value) : value;
  }
  return out as T;
}

/** One `SqliteExecutor` over DO SQLite. Inside a transaction this handle IS the transaction: DO SQLite has no separate transaction connection — every `sql` operation that runs inside `ctx.storage.transaction`'s closure is part of it. */
class DurableObjectSqliteExecutor implements SqliteExecutor {
  constructor(private readonly target: DurableObjectSqliteTarget) {}

  async exec(sql: string): Promise<void> {
    this.target.sql.exec(sql);
  }

  async run(sql: string, ...params: SqliteValue[]): Promise<void> {
    this.target.sql.exec(sql, ...params.map(toDoBinding));
  }

  async get<T extends object>(sql: string, ...params: SqliteValue[]): Promise<T | undefined> {
    const cursor = this.target.sql.exec(sql, ...params.map(toDoBinding));
    const next = cursor.next();
    return next.done ? undefined : fromDoRow<T>(next.value);
  }

  async all<T extends object>(sql: string, ...params: SqliteValue[]): Promise<T[]> {
    const rows = this.target.sql.exec(sql, ...params.map(toDoBinding)).toArray();
    return rows.map((row) => fromDoRow<T>(row) as T);
  }
}

/**
 * pi-durable's `SqliteDatabase` facade over a Durable Object's SQLite.
 *
 * This is the 25-line adapter the n0b round-2 prototype proved in staging
 * (docs/spikes/n0b-round2-pi-durable.md): pi-durable's own `SqliteStorage`
 * runs unchanged on top of it, so every commit is a `ctx.storage.transaction`
 * and is acknowledged only once the platform has durably stored and
 * replicated it (the output gate).
 *
 * `close()` is a no-op: DO SQLite's lifecycle is the platform's, and the
 * facade's contract only requires that later operations fail or are moot —
 * a closed DO instance simply no longer accepts requests.
 */
export class DurableObjectSqliteDatabase implements SqliteDatabase {
  private readonly executor: DurableObjectSqliteExecutor;

  constructor(private readonly target: DurableObjectSqliteTarget) {
    this.executor = new DurableObjectSqliteExecutor(target);
  }

  async exec(sql: string): Promise<void> {
    await this.executor.exec(sql);
  }

  async run(sql: string, ...params: SqliteValue[]): Promise<void> {
    await this.executor.run(sql, ...params);
  }

  async get<T extends object>(sql: string, ...params: SqliteValue[]): Promise<T | undefined> {
    return this.executor.get<T>(sql, ...params);
  }

  async all<T extends object>(sql: string, ...params: SqliteValue[]): Promise<T[]> {
    return this.executor.all<T>(sql, ...params);
  }

  async transaction<T>(callback: (transaction: SqliteExecutor) => Promise<T>): Promise<T> {
    // A Durable Object is single-instance and serves one request at a time
    // on this path, so the facade's "queue other operations until the
    // transaction finishes" holds by construction: the storage's own await
    // chain serialises everything on top of it.
    // The callback's handle is the same `sql` accessor: DO SQLite has no
    // separate transaction connection, so every operation the callback issues
    // inside `ctx.storage.transaction`'s closure is part of that transaction.
    return this.target.transaction(() => callback(this.executor));
  }

  async close(): Promise<void> {
    // No-op: see the class comment.
  }
}
