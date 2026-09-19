/**
 * The repository Durable Object — ONE slot and ONE serialized publisher
 * (tick ef7, SPEC §12 Phase 4 item 3; §9.1's "RepoSemaphore" with N=1).
 *
 * THE PROBLEM THIS ROOM ANSWERS. This project keeps meeting "two parties
 * writing one name" on the local host — `FETCH_HEAD`, `refs/remotes`, the
 * shared peek ref (`.tick/learnings.md`, Phase 3 found it three times) — and
 * every fix so far has been the SAME answer: a narrower private namespace, so
 * the two parties stop sharing the name. A Durable Object is the OTHER answer:
 * not disjoint namespaces but a single serialized writer. A Durable Object is
 * a coordination lock, not a store (SPEC §9.1): every fact it acts on is read
 * from `.ticfac/` and the tracker through the contents seam, and if the room
 * is lost, the safe outcome is that nobody holds the slot until somebody
 * re-derives who should — the lapsed-lease failure mode, which fails exactly
 * this safe way.
 *
 * **The slot** is RunRoom's dispatch lease, not a new design: one
 * implementation (`src/lease.ts`) serves both rooms, because the lease already
 * stops two runs of one epic and its semantics are what this project paid to
 * learn (non-blocking acquire naming the holder, token as release credential,
 * compare-and-delete release, `expired` vs `taken` renewals, alarm expiry).
 * RunRoom's lease stops two runs IGNITING; the slot stops two runs WRITING —
 * the repository-level answer, which holds even when a second run exists
 * anyway (a lease bypassed, a local and a cloud run of one project, an
 * operator's second submission). The lease is per project; this room is per
 * repository (`idFromName("owner/name")`) — the name being written is what
 * has to be serialised, and two projects never share one.
 *
 * **The publisher** is one method, `publish`, and every publish to a
 * repository goes through it: the room's single thread, plus a promise chain
 * for the awaits inside a write, means one write is in flight at a time, in
 * arrival order. The write itself is performed through `contentsStore` — the
 * same contents-API mechanism (and the same compare-and-swap answers) the
 * Workflow host has always used; this room serializes WHO writes, and the
 * repository's own CAS rules what a write may overwrite. A publish must name
 * the slot holder's credentials and is refused (`not_holder`, naming the
 * holder, token withheld) for anyone else — the slot is the belt to RunRoom's
 * braces.
 *
 * WHICH OF THE TWO ANSWERS EACH WRITE ON THIS HOST RELIES ON — the rule this
 * tick asks to state, so no future write gets its answer by accident:
 *
 *  - the run's publishes on the run branch — checkpoint, attempt markers,
 *    decisions, `.tick/` tracker records (run-state-store, tracker-client
 *    through the DO-backed `contentsStore`) — rely on the SERIALIZED WRITER:
 *    one slot per repository, one publisher. (`repoContentsStore` in
 *    git-contents.ts is how a run's store reaches this room.)
 *  - the SignalInbox's commits to `.tick/` (tracker-write.ts) rely on the
 *    inbox's OWN serialized queue — a serialized writer too, but one that
 *    does not hold this room's slot; widening it to route through the
 *    publisher is later phase work, and until then its writes and a run's
 *    writes share one repository without sharing one name (the inbox's
 *    `.tick/signals/` paths are disjoint from the run branch's records).
 *  - every read (collect, PR review, `status`) relies on NEITHER: reading one
 *    name needs no writer, which is why this room never serves reads.
 *  - when a NEW write lands on this host, its author must add a row here
 *    naming its answer — disjoint namespace or this room — never neither.
 */
import { DurableObject } from "cloudflare:workers";

import { contentsStore, type StoreWrite } from "./git-contents";
import type { Env } from "./index";
import {
  type AcquireLeaseRequest,
  type AcquireLeaseResult,
  badText,
  DEFAULT_LEASE_TTL_MS,
  type DispatchLeaseView,
  type HolderCredentials,
  invalidRequest,
  LeaseTable,
  MAX_LEASE_TTL_MS,
  MIN_LEASE_TTL_MS,
  type RenewLeaseResult,
  type RequestInvalid,
} from "./lease";

// The slot's bounds are the shared lease's bounds (src/lease.ts) — re-exported
// so a caller imports the slot's vocabulary from the room that owns it, the
// same way RunRoom's tests import the lease's from run-room.ts.
export { DEFAULT_LEASE_TTL_MS, MAX_LEASE_TTL_MS, MIN_LEASE_TTL_MS };

/** Single-row table: the slot is one per repository, and the room *is* the repository. */
const SLOT_TABLE = "publish_slot";
const SLOT_ROW = "slot";

/** One write as a publisher performs it: the contents-API vocabulary, one op. */
export type PublishWrite =
  | { op: "create"; path: string; content: string; message: string }
  | { op: "update"; path: string; sha: string; content: string; message: string };

export type PublishRequest = {
  /** The slot holder's credentials: only the holder may publish. */
  holder: HolderCredentials;
  /** The ref being written — the room is a lock over the repository, not a store bound to one branch. */
  ref: string;
  write: PublishWrite;
};

export type PublishResult =
  | { ok: true; write: StoreWrite }
  | { ok: false; error: "not_holder"; holder: DispatchLeaseView | null; detail: string }
  | RequestInvalid;

/** The slot's release, without RunRoom's queue follow-ups — this room has no queue. */
export type ReleaseSlotResult =
  | { ok: true; released: DispatchLeaseView }
  | { ok: false; error: "not_holder"; holder: DispatchLeaseView | null; detail: string }
  | RequestInvalid;

function invalid(complaint: string): RequestInvalid {
  return invalidRequest("RepoRoom", complaint);
}

export class RepoRoom extends DurableObject<Env> {
  /** The one slot: RunRoom's lease semantics, one implementation (src/lease.ts). */
  readonly #slot: LeaseTable;
  /**
   * The publisher's serialization. The DO's input gate orders requests, but a
   * publish awaits the contents API mid-write, and anything may interleave at
   * an await inside a DO — so each publish chains onto the last: one write in
   * flight at a time, in arrival order, failures included (a rejected promise
   * still unblocks the next publish).
   */
  #publishes: Promise<unknown> = Promise.resolve();

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    // Synchronous, so it is complete before any request or alarm is delivered
    // — DO storage SQL does not yield. Same columns as RunRoom's dispatch
    // lease, because it is the same row under another name.
    ctx.storage.sql.exec(`
      CREATE TABLE IF NOT EXISTS publish_slot (
        id TEXT PRIMARY KEY,
        run_id TEXT NOT NULL,
        token TEXT NOT NULL,
        epic TEXT NOT NULL,
        origin TEXT NOT NULL,
        requested_by TEXT,
        acquired_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL
      );
    `);
    this.#slot = new LeaseTable(ctx.storage.sql, {
      table: SLOT_TABLE,
      row: SLOT_ROW,
      noun: "publish slot",
      scope: "RepoRoom",
      domain: "repository",
    });
  }

  // ---------------------------------------------------------------- slot ---

  /**
   * Takes the repository's one slot, or reports who holds it — RunRoom's
   * `acquireDispatchLease` exactly: never blocks, never queues, names the
   * holder in the refusal, treats a re-acquire by the same run as a renewal.
   */
  async acquireSlot(request: AcquireLeaseRequest): Promise<AcquireLeaseResult> {
    const result = this.#slot.acquire(request);
    if (result.ok) await this.#arm();
    return result;
  }

  /** Extends the slot for its holder; a lost or taken-over slot cannot be renewed. */
  async renewSlot(request: HolderCredentials & { ttl_ms?: number }): Promise<RenewLeaseResult> {
    const result = this.#slot.renew(request);
    if (result.ok) await this.#arm();
    return result;
  }

  /**
   * Compare-and-delete release: the row is removed only when BOTH the run id
   * and the token match, so a superseded holder cannot free its successor's
   * slot on the way out.
   */
  async releaseSlot(request: HolderCredentials): Promise<ReleaseSlotResult> {
    const result = this.#slot.release(request);
    await this.#arm();
    return result;
  }

  /**
   * The live slot, or null. An expired row reads as null even before the
   * alarm deletes it, so a missed alarm cannot wedge the repository.
   */
  async slotStatus(): Promise<DispatchLeaseView | null> {
    return this.#slot.status();
  }

  /**
   * Expires an abandoned slot. A run that dies without releasing leaves the
   * repository wedged until this fires — and the safe outcome when a
   * lock-holder dies is that nobody holds the lock until somebody re-derives
   * who should (SPEC §9.1).
   */
  override async alarm(): Promise<void> {
    this.#slot.expireDue();
    await this.#arm();
  }

  // ------------------------------------------------------------ publisher ---

  /**
   * THE one serialized publisher: every publish to a repository goes through
   * this method, one write in flight at a time, in arrival order.
   *
   * The slot verdict is read synchronously before the write and the write is
   * chained behind any in-flight one, so no interleave can hand the publisher
   * two writers. A refusal is typed, never thrown — an opaque RPC rejection
   * would say nothing and log as an uncaught DO exception besides.
   *
   * A store failure (the repository unreachable) is the one thing that throws,
   * as it does through the direct contents store: the caller already knows how
   * to retry a write the contents API did not commit, and this room adds no
   * second answer to that one.
   */
  async publish(request: PublishRequest): Promise<PublishResult> {
    const holder = request?.holder;
    const write = request?.write;
    const complaint =
      badText(holder?.run_id, "holder.run_id") ??
      badText(holder?.token, "holder.token") ??
      badText(request?.ref, "ref") ??
      badText(write?.path, "write.path") ??
      badText(write?.content, "write.content") ??
      badText(write?.message, "write.message");
    if (complaint !== null) return invalid(complaint);
    if (write === undefined || write === null) {
      return invalid("write is required");
    }
    const op: unknown = write?.op;
    if (write !== undefined && write !== null && write.op !== "create" && write.op !== "update") {
      return invalid(`write.op must be "create" or "update", not ${JSON.stringify(op)}`);
    }
    if (write.op === "update" && badText(write.sha, "write.sha") !== null) {
      return invalid("write.sha is required for an update");
    }

    const project = this.ctx.id.name;
    if (project === undefined || project === null) {
      return invalid("this room was not addressed by repository name");
    }

    // The slot verdict happens inside the serialized section — a publisher
    // that checked the slot before an in-flight write settled could hand the
    // repository to a run whose slot had since been taken.
    const mine = this.#publishes.then(() => this.#publishNow(project, request));
    this.#publishes = mine.then(
      () => undefined,
      () => undefined,
    );
    return mine;
  }

  async #publishNow(project: string, request: PublishRequest): Promise<PublishResult> {
    const holder = request.holder;
    // Read and write with no `await` between the slot read and the verdict:
    // DO storage SQL is synchronous, so nothing can interleave here.
    const current = this.#slot.read();
    const live = current !== null && current.expires_at > Date.now();
    if (
      current === null ||
      !live ||
      current.run_id !== holder.run_id ||
      current.token !== holder.token
    ) {
      return {
        ok: false,
        error: "not_holder",
        holder: current === null ? null : this.#slot.view(current),
        detail:
          current === null
            ? `no publish slot is held for ${project}, so nothing may publish to it`
            : `the publish slot for ${project} is held by run ${current.run_id}, not ${holder.run_id}`,
      };
    }

    const store = contentsStore(this.env, project, request.ref);
    const write = request.write;
    let result: StoreWrite;
    if (write.op === "create") {
      result = await store.create(write.path, {
        content: write.content,
        message: write.message,
      });
    } else {
      result = await store.update(write.path, write.sha, {
        content: write.content,
        message: write.message,
      });
    }
    return { ok: true, write: result };
  }

  // --------------------------------------------------------------- status ---

  override async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/status") {
      return Response.json({ object: "RepoRoom", slot: await this.slotStatus() });
    }
    // The Worker reaches the room over RPC; HTTP exists for a human-readable
    // probe, so an unknown path is simply absent.
    return Response.json({ error: "not_found" }, { status: 404 });
  }

  // ------------------------------------------------------------- internals ---

  /** Arms the room's one alarm at the slot's deadline; no slot clears it. */
  async #arm(): Promise<void> {
    const deadline = this.#slot.deadline();
    if (deadline === null) {
      await this.ctx.storage.deleteAlarm();
      return;
    }
    await this.ctx.storage.setAlarm(deadline);
  }
}
