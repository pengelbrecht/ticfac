/**
 * The single-slot lease — RunRoom's lease semantics, ONE implementation.
 *
 * Tick ef7 (SPEC §12 Phase 4 item 3): the repository Durable Object's one
 * slot must "preserve RunRoom's lease semantics rather than reinventing
 * them", because the lease already does the one thing a slot has to do —
 * stop two parties writing one name — and it does it in ways this project
 * has already paid to learn:
 *
 *  - **acquire never blocks**: a conflict comes back naming the holding run
 *    (`lease_held_by:<run>`), which turns a second writer into an actionable
 *    refusal instead of a queue;
 *  - **the token is the release credential** and is withheld from everyone
 *    but the acquirer, so a refused caller can never release a lease it does
 *    not hold;
 *  - **release is compare-and-delete** on run id AND token, so a superseded
 *    holder cannot free its successor's lease on the way out;
 *  - **renewal tells the two failure classes apart** (`expired` vs `taken`),
 *    because the fixes for them point in opposite directions;
 *  - **a lapsed, unheld lease can be reclaimed by its own holder** under its
 *    own token, by compare-and-swap (tick oen) — offered, not imposed: a host
 *    exposes `reclaim` only where continuing after a lapse is sound;
 *  - **an abandoned lease expires on a DO alarm**, so a dead writer cannot
 *    wedge the name forever — the safe outcome when a lock-holder dies is
 *    that nobody holds the lock until somebody re-derives who should.
 *
 * Two hosts share this core: RunRoom's per-project **dispatch lease** (one
 * `.tick/` writer per project, wherever the orchestrator sits — D4/D19) and
 * RepoRoom's per-repository **publish slot** (one writer to a repository at a
 * time, SPEC §9.1's "RepoSemaphore" with N=1). The semantics are shared by
 * construction, and RunRoom's existing lease tests — which predate the core —
 * are what proves the extraction changed nothing.
 *
 * The core is storage SQL plus logic, and nothing else. It never arms the
 * alarm itself (a host may multiplex several deadlines onto one alarm, the
 * way RunRoom's queue does), and it holds no I/O: every method either
 * computes an answer from synchronous SQL or writes synchronously, so no
 * interleave can observe a half-made decision — the same rule RunRoom has
 * always stated at each of its read-modify-write blocks.
 */

/**
 * Lease lifetime, mirroring `DEFAULT_CONTROLLER_LEASE_MS` in the Pi extension's
 * file lease so an enrolled and an un-enrolled project behave the same. A run
 * outlives it many times over and renews on a heartbeat; the ttl bounds how
 * long an *abandoned* holder wedges the name, not how long a holder may take.
 */
export const DEFAULT_LEASE_TTL_MS = 60_000;
/** Same floor as the file lease: below this, clock skew alone expires a live lease. */
export const MIN_LEASE_TTL_MS = 100;
/** A ceiling so a bad caller cannot wedge a name for a day on one typo. */
export const MAX_LEASE_TTL_MS = 3_600_000;

/** Where the holder runs (D19). */
export type LeaseOrigin = "local" | "cloud";

/** The lease as its holder sees it — `token` is the release credential. */
export type DispatchLease = {
  run_id: string;
  /** Opaque fencing token. Only the acquirer ever receives it. */
  token: string;
  epic: string;
  origin: LeaseOrigin;
  requested_by?: string;
  acquired_at: string;
  expires_at: string;
};

/**
 * The lease as everyone else sees it. The token is withheld: handing a refused
 * caller the holder's token would let it release a lease it does not hold,
 * which is the one thing compare-and-delete exists to prevent.
 */
export type DispatchLeaseView = Omit<DispatchLease, "token">;

export type AcquireLeaseRequest = {
  run_id: string;
  epic: string;
  origin?: LeaseOrigin;
  requested_by?: string;
  ttl_ms?: number;
};

/**
 * A malformed call. Every lease method returns its failures rather than
 * throwing: a thrown RPC error reaches the caller as an opaque rejection and
 * is *also* logged as an uncaught DO exception, so a typed refusal keeps both
 * the contract and the logs honest.
 */
export type RequestInvalid = { ok: false; error: "invalid_request"; detail: string };

export type LeaseGranted = { ok: true; lease: DispatchLease; renewed: boolean };
export type LeaseRefused = {
  ok: false;
  error: "lease_held";
  /** The dispatch-log refusal reason (see docs/design/cloud-factory.md). */
  reason: string;
  holder: DispatchLeaseView;
  detail: string;
};
export type AcquireLeaseResult = LeaseGranted | LeaseRefused | RequestInvalid;

/** The credentials a holder presents to renew or release. */
export type HolderCredentials = { run_id: string; token: string };

/**
 * HOW a renewal failed, because the two are opposite problems and the fixes
 * for them point in opposite directions (tick 7n7).
 *
 * - `taken`: a live lease exists and it is not this run's. Somebody else is
 *   the arbiter now, and this run must stop rather than race it.
 * - `expired`: nobody holds the lease. It lapsed — either because nothing
 *   renewed it on the run's behalf, or because it was already released.
 *
 * The two are told apart at the only place that can see both the row and the
 * clock, never inferred from a message by every caller.
 */
export type LeaseLostReason = "expired" | "taken";

export type RenewLeaseResult =
  | { ok: true; lease: DispatchLease }
  | {
      ok: false;
      error: "lease_lost";
      /** Which of the two ways it was lost. Never inferred from the message. */
      lost: LeaseLostReason;
      /** Set only when `lost` is `taken`: an expired lease has no holder. */
      holder: DispatchLeaseView | null;
      detail: string;
    }
  | RequestInvalid;

/** What a holder presents to take back a lease that lapsed under it (tick oen). */
export type ReclaimLeaseRequest = HolderCredentials & {
  /** Used only when the row is already gone; a surviving row keeps its own. */
  epic: string;
  origin?: LeaseOrigin;
  requested_by?: string;
  ttl_ms?: number;
};

/**
 * A reclaim's verdict (tick oen).
 *
 * `reclaimed: false` is a lease that was live and this holder's after all — a
 * renewal that raced the one that reported the lapse — and is extended exactly
 * as `renew` would. `reclaimed: true` is the case the method exists for, and
 * `detail` says what state the lease was found in, because "it lapsed and the
 * alarm had already swept it" and "it lapsed and was still sitting there" are
 * different facts about how long the project went unheld.
 *
 * A refusal is always `taken`: a reclaim never answers `expired`, because an
 * expired lease nobody else holds is precisely what it takes back.
 */
export type ReclaimLeaseResult =
  | { ok: true; lease: DispatchLease; reclaimed: boolean; detail: string }
  | {
      ok: false;
      error: "lease_lost";
      lost: "taken";
      holder: DispatchLeaseView;
      detail: string;
    }
  | RequestInvalid;

/** The core's compare-and-delete release, without a host's own follow-ups. */
export type LeaseReleaseResult =
  | { ok: true; released: DispatchLeaseView }
  | { ok: false; error: "not_holder"; holder: DispatchLeaseView | null; detail: string }
  | RequestInvalid;

/** The storage row. Timestamps are epoch ms; views render them as ISO strings. */
export type LeaseRecord = {
  run_id: string;
  token: string;
  epic: string;
  origin: string;
  requested_by: string | null;
  acquired_at: number;
  expires_at: number;
};

export const stamp = (ms: number): string => new Date(ms).toISOString();

/** Returns a complaint when the field is not a non-empty string, else null. */
export function badText(value: unknown, field: string): string | null {
  return typeof value === "string" && value.trim() !== "" ? null : `${field} is required`;
}

/**
 * Returns a complaint when the ttl is outside the pinned bounds, else null.
 * The bounds are named constants with this guard so a limit change has to pass
 * a test rather than only a review.
 */
export function badTtl(ttl: unknown): string | null {
  if (ttl === undefined) return null;
  return Number.isSafeInteger(ttl) &&
    (ttl as number) >= MIN_LEASE_TTL_MS &&
    (ttl as number) <= MAX_LEASE_TTL_MS
    ? null
    : `lease ttl must be an integer between ${MIN_LEASE_TTL_MS} and ${MAX_LEASE_TTL_MS} ms, got ${String(ttl)}`;
}

/** What a host's typed refusal prefixes its complaints with. */
export function invalidRequest(scope: string, complaint: string): RequestInvalid {
  return { ok: false, error: "invalid_request", detail: `${scope}: ${complaint}` };
}

/** The slice of DO storage the lease needs — the real SQL namespace's type. */
export type LeaseSql = DurableObjectState["storage"]["sql"];

/**
 * One named slot in a host's storage: the row, its read-modify-write rules
 * and the acquire/renew/release vocabulary, in RunRoom's exact shapes.
 */
export class LeaseTable {
  readonly #sql: LeaseSql;
  readonly #table: string;
  readonly #row: string;
  /** What the lease is called in refusal details, e.g. "dispatch lease". */
  readonly #noun: string;
  /** Whose refusal details this is, e.g. "RunRoom". */
  readonly #scope: string;
  /** The name of what the lease guards, e.g. "project" or "repository". */
  readonly #domain: string;

  constructor(
    sql: LeaseSql,
    options: {
      table: string;
      /** The single row's id: the slot is one per host instance. */
      row: string;
      noun: string;
      scope: string;
      domain: string;
    },
  ) {
    this.#sql = sql;
    this.#table = options.table;
    this.#row = options.row;
    this.#noun = options.noun;
    this.#scope = options.scope;
    this.#domain = options.domain;
  }

  /** The row as stored, or null when the table holds none. */
  read(): LeaseRecord | null {
    const rows = [
      ...this.#sql.exec<LeaseRecord>(
        `SELECT run_id, token, epic, origin, requested_by, acquired_at, expires_at
         FROM ${this.#table} WHERE id = ?`,
        this.#row,
      ),
    ];
    return rows[0] ?? null;
  }

  write(record: LeaseRecord): void {
    this.#sql.exec(
      `INSERT INTO ${this.#table}
         (id, run_id, token, epic, origin, requested_by, acquired_at, expires_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET
         run_id = excluded.run_id,
         token = excluded.token,
         epic = excluded.epic,
         origin = excluded.origin,
         requested_by = excluded.requested_by,
         acquired_at = excluded.acquired_at,
         expires_at = excluded.expires_at`,
      this.#row,
      record.run_id,
      record.token,
      record.epic,
      record.origin,
      record.requested_by,
      record.acquired_at,
      record.expires_at,
    );
  }

  /**
   * Compare-and-delete: the row is removed only when BOTH the run id and the
   * token match, so a holder whose lease already expired and was taken over
   * cannot free its successor's on the way out.
   */
  deleteHeld(record: LeaseRecord): void {
    this.#sql.exec(
      `DELETE FROM ${this.#table} WHERE id = ? AND run_id = ? AND token = ?`,
      this.#row,
      record.run_id,
      record.token,
    );
  }

  grant(record: LeaseRecord): DispatchLease {
    return { ...this.view(record), token: record.token };
  }

  view(record: LeaseRecord): DispatchLeaseView {
    return {
      run_id: record.run_id,
      epic: record.epic,
      origin: record.origin as LeaseOrigin,
      ...(record.requested_by === null ? {} : { requested_by: record.requested_by }),
      acquired_at: stamp(record.acquired_at),
      expires_at: stamp(record.expires_at),
    };
  }

  #invalid(complaint: string): RequestInvalid {
    return invalidRequest(this.#scope, complaint);
  }

  /**
   * Takes the slot, or reports who holds it. Never blocks and never queues: a
   * caller that cannot have the lease is told the holding run's id so the
   * refusal is actionable. A second acquire by the *same* run is a renewal,
   * so a retried ignition is idempotent rather than a self-conflict.
   */
  acquire(request: AcquireLeaseRequest): AcquireLeaseResult {
    const complaint =
      badText(request?.run_id, "run_id") ??
      badText(request?.epic, "epic") ??
      badTtl(request?.ttl_ms);
    if (complaint !== null) return this.#invalid(complaint);
    const runID = request.run_id;
    const ttl = request.ttl_ms ?? DEFAULT_LEASE_TTL_MS;
    const now = Date.now();

    // Read and write with no `await` in between: DO storage SQL is
    // synchronous, so nothing can interleave and observe the same free lease.
    // This is what the file lease needed flock for.
    const current = this.read();
    const live = current !== null && current.expires_at > now;

    if (live && current.run_id !== runID) {
      const holder = this.view(current);
      return {
        ok: false,
        error: "lease_held",
        reason: `lease_held_by:${current.run_id}`,
        holder,
        detail: `the ${this.#noun} is held by run ${current.run_id} (epic ${holder.epic}, ${holder.origin}) until ${holder.expires_at}`,
      };
    }

    const renewed = live && current.run_id === runID;
    const record: LeaseRecord = {
      run_id: runID,
      // A renewal keeps the holder's token: it is the release credential, and
      // rotating it under a live holder would lock it out of its own lease.
      token: renewed ? current.token : crypto.randomUUID(),
      epic: request.epic,
      origin: request.origin ?? "cloud",
      requested_by: request.requested_by ?? null,
      acquired_at: renewed ? current.acquired_at : now,
      expires_at: now + ttl,
    };
    this.write(record);

    return { ok: true, lease: this.grant(record), renewed };
  }

  /**
   * Extends the lease for its holder. A lost or taken-over lease cannot be
   * renewed: the two are told apart at the only place that can see both the
   * row and the clock, rather than guessed at by every caller.
   */
  renew(request: HolderCredentials & { ttl_ms?: number }): RenewLeaseResult {
    const complaint =
      badText(request?.run_id, "run_id") ??
      badText(request?.token, "token") ??
      badTtl(request?.ttl_ms);
    if (complaint !== null) return this.#invalid(complaint);
    const runID = request.run_id;
    const ttl = request.ttl_ms ?? DEFAULT_LEASE_TTL_MS;
    const now = Date.now();

    const current = this.read();
    const live = current !== null && current.expires_at > now;
    if (!live || current.run_id !== runID || current.token !== request.token) {
      // A LIVE lease that is not this run's was taken; anything else lapsed.
      if (live) {
        return {
          ok: false,
          error: "lease_lost",
          lost: "taken",
          holder: this.view(current),
          detail:
            current.run_id === runID
              ? `run ${runID}'s ${this.#noun} was re-acquired under a different token`
              : `the ${this.#noun} is held by run ${current.run_id}, not ${runID}`,
        };
      }
      return {
        ok: false,
        error: "lease_lost",
        lost: "expired",
        holder: null,
        detail:
          current === null
            ? `run ${runID}'s ${this.#noun} has expired or been released — no ${this.#noun} is held ` +
              `for this ${this.#domain}, and no other run has taken it`
            : `run ${runID}'s ${this.#noun} expired at ${stamp(current.expires_at)} ` +
              "and no other run has taken it",
      };
    }

    const record: LeaseRecord = { ...current, expires_at: now + ttl };
    this.write(record);
    return { ok: true, lease: this.grant(record) };
  }

  /**
   * Takes back a lease that lapsed under its holder while NOBODY ELSE holds
   * it, under the holder's own credentials (tick oen).
   *
   * `renew` answers a lapsed lease `lost: expired`, and until tick oen the run
   * read that as a stop. But a lapsed, unheld lease is a project nobody is
   * running: no other run holds it, and none is waiting on it — a queued
   * submission is ignited by the very alarm that sweeps an expired lease, so a
   * run queued behind this one shows up as a LIVE lease under another holder,
   * which is `taken`. Stopping in that state hands the project to nobody and
   * throws the run's work away for it. Measured on CI: a wave run ignited with
   * a 200ms lease whose boot outlived it answered `expired` at its first
   * renewal and hard-stopped before its container ever worked — the same
   * arithmetic as a production boot, stall or step longer than the
   * ten-minute acquire, which nothing else covers.
   *
   * It is a compare-and-swap, not an acquire. The row and the clock are
   * re-read here with no `await` between the read and the write (DO storage
   * SQL is synchronous), so a lease another party took between the renewal
   * that reported the lapse and this call is seen, refused as `taken`, and
   * left exactly as it is. And it is narrower than an acquire in two ways:
   *
   *  - the TOKEN is the caller's own, never a fresh one. A holder's token is
   *    its release credential and a cloud run carries it immutably in its
   *    Workflow params, so a reclaim that rotated it would lock the run out of
   *    every later renewal and out of its own release.
   *  - an EXPIRED row that is somebody else's is refused, not taken. Somebody
   *    acquired the lease after this holder's lapsed; theirs has lapsed in
   *    turn, but it is the newer claim and theirs to reclaim. An acquire would
   *    take it (an expired row is free to `acquire`), which is right for a
   *    newcomer and wrong for a holder that was already superseded.
   *
   * What it cannot see: a run that acquired AND released inside the lapse
   * leaves the same empty row the alarm does. The exclusivity that gap broke
   * was broken while it was open; stopping now would not restore it.
   */
  reclaim(request: ReclaimLeaseRequest): ReclaimLeaseResult {
    const complaint =
      badText(request?.run_id, "run_id") ??
      badText(request?.token, "token") ??
      badText(request?.epic, "epic") ??
      badTtl(request?.ttl_ms);
    if (complaint !== null) return this.#invalid(complaint);
    const runID = request.run_id;
    const ttl = request.ttl_ms ?? DEFAULT_LEASE_TTL_MS;
    const now = Date.now();

    const current = this.read();
    const live = current !== null && current.expires_at > now;
    const mine = current !== null && current.run_id === runID && current.token === request.token;

    if (current !== null && !mine) {
      const holder = this.view(current);
      return {
        ok: false,
        error: "lease_lost",
        lost: "taken",
        holder,
        detail: live
          ? `the ${this.#noun} is held by run ${current.run_id}, not ${runID}`
          : `run ${current.run_id} acquired the ${this.#noun} after run ${runID}'s lapsed; ` +
            `its own lapsed at ${holder.expires_at}, but it is the newer claim`,
      };
    }

    const record: LeaseRecord =
      current === null
        ? {
            run_id: runID,
            token: request.token,
            epic: request.epic,
            origin: request.origin ?? "cloud",
            requested_by: request.requested_by ?? null,
            acquired_at: now,
            expires_at: now + ttl,
          }
        : // Still this holder's row, so nobody has touched it since: the same
          // lease resumes, `acquired_at` and all.
          { ...current, expires_at: now + ttl };
    this.write(record);

    return {
      ok: true,
      lease: this.grant(record),
      reclaimed: !live,
      detail: live
        ? `run ${runID}'s ${this.#noun} was live after all and was renewed`
        : current === null
          ? `run ${runID}'s ${this.#noun} had lapsed and been swept, and no other run had taken ` +
            `it; reclaimed under the same credentials`
          : `run ${runID}'s ${this.#noun} lapsed at ${stamp(current.expires_at)} and no other ` +
            "run had taken it; reclaimed under the same credentials",
    };
  }

  /** Compare-and-delete release, reporting the released view on success. */
  release(request: HolderCredentials): LeaseReleaseResult {
    const complaint = badText(request?.run_id, "run_id") ?? badText(request?.token, "token");
    if (complaint !== null) return this.#invalid(complaint);
    const runID = request.run_id;

    const current = this.read();
    if (current === null || current.run_id !== runID || current.token !== request.token) {
      return {
        ok: false,
        error: "not_holder",
        holder: current === null ? null : this.view(current),
        detail:
          current === null
            ? `no ${this.#noun} is held`
            : `the ${this.#noun} is held by run ${current.run_id}, not ${runID}`,
      };
    }

    this.deleteHeld(current);
    return { ok: true, released: this.view(current) };
  }

  /**
   * The live lease, or null. An expired row reads as null even before the
   * alarm deletes it, so a missed alarm cannot wedge the name.
   */
  status(): DispatchLeaseView | null {
    const current = this.read();
    if (current === null || current.expires_at <= Date.now()) return null;
    return this.view(current);
  }

  /**
   * Expires an abandoned lease. A holder that dies without releasing leaves
   * the name wedged until this fires; the caller gets the deleted record so
   * it can do its own follow-up work (RunRoom ignites the next parked
   * submission). A lease renewed since the alarm was set is followed, not
   * dropped — `expireDue` re-reads the row and the clock.
   */
  expireDue(now: number = Date.now()): LeaseRecord | null {
    const current = this.read();
    if (current === null || current.expires_at > now) return null;
    this.deleteHeld(current);
    return current;
  }

  /** The next expiry this slot can schedule an alarm for, or null. */
  deadline(): number | null {
    const current = this.read();
    return current === null ? null : current.expires_at;
  }
}
