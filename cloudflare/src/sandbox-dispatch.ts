/**
 * The per-tick sandbox dispatch door (tick 8ty): how the orchestrator
 * CONTAINER asks the factory, over HTTP, to start one attempt's worker
 * container — and to read one back by identity.
 *
 * ## Why this exists
 *
 * The Go orchestrator runs in a container. A container cannot create a
 * sibling Sandbox: the binding is a Worker binding, and the container holds
 * no Cloudflare credentials at all (D17 — the only credential it holds is its
 * run's own gateway token). So the container asks, and the Worker — which
 * holds the `SANDBOXES` binding — starts the container through the machinery
 * `sandbox-executor.ts` already is: `namedSandbox` to resolve a Sandbox by
 * name, `findWorkProcess` to inspect a running one, adoption before dispatch,
 * and the handle-not-a-result discipline the whole module is built around.
 *
 * The other end of this call is the Go executor `internal/exec` grows
 * (`cloudflare-sandbox`, tick keh) — the same four-operation seam
 * `subprocess` and `herdr` implement, with a different substrate. That makes
 * this module the ONE place the HTTP contract lives: the Go client and this
 * route are the two consumers of one mechanism, and a contract written down
 * on both sides is drift waiting to happen (the same lesson the wave door's
 * `tick-membership.ts` and the git door's challenge pin).
 *
 * ## The contract
 *
 * **Authorization — a route that starts compute must not be open, and the
 * scheme is not a new one.** Both routes are exempt from the FACTORY bearer
 * token (`auth.ts`'s path registry, `SANDBOX_DISPATCH_PREFIX`) for the same
 * reason `/api/wave` is: the caller is a container, and a container must
 * never hold the operator's credential — that token commands the whole
 * control plane. The credential a container holds is its run's own gateway
 * token (`TICKS_FACTORY_TOKEN`, D17), and it is verified by the SAME function
 * the model path, the git door, the wave door and the branch door authorize
 * through (`authorizeGatewayRequest`), whose four verdicts stay distinct:
 *
 *   - `401 run_token_required` — no credential at all. This is the tick's
 *     "an unauthenticated call is refused".
 *   - `401 run_token_unknown` — a credential this deployment never minted.
 *     The operator's factory token lands here too: a container holding it
 *     would be the leak D17 exists to prevent, and this door does not accept
 *     it in trade.
 *   - `403 run_token_revoked` — the run's kill switch reached this door too:
 *     a stopped run cannot start tick containers any more than it can spend.
 *   - `403 run_not_active` — the run is over; its sandbox question is moot.
 *
 * There is no run id in any path or body field the caller can choose: the
 * credential says which run is speaking, so a container cannot dispatch on
 * behalf of a run it is not.
 *
 * ### `POST /api/sandbox/attempts` — start one attempt's worker
 *
 * The request body — UTF-8 JSON, every field required. The IDENTIFIER fields
 * (`role`, `write_ref`, `base_ref`, `model`, `harness`) are printable ASCII
 * with no whitespace, at most 512 characters; the PROSE fields (`title`,
 * `prompt`) are any valid UTF-8 without control characters, bounded in UTF-8
 * bytes (the rules and the reasons: `PLAIN_FIELD_PATTERN` below):
 *
 * | field | what it is |
 * |---|---|
 * | `epic` | the epic the dispatch belongs to. Checked against the run's own row — a container that has somehow drifted onto another epic is refused rather than silently dispatching this run's tick under another one's name (the wave door's rule, verbatim). |
 * | `tick_id` | the tick this attempt implements. It names the container (`<run>-<tick>-<attempt>`), so it is constrained to the conservative shape a name needs: alphanumerics, `.`, `_`, `-`, first character alphanumeric, at most 64 characters. |
 * | `attempt` | 1-based attempt number, a positive integer. The attempt is in the container's name ON PURPOSE: a redispatch after a spent attempt must land in a FRESH container — reusing the name is how you inherit whatever broke it. |
 * | `job_id` | OPTIONAL: the caller's FULL job id, when the job is not the attempt itself — a gate repair (`run-<run>/tick-<tick>/repair-1`, its retries `…/repair-1-r2`), a conflict resolution, a base fold. The job id IS the identity: the container's name, its landing branch, its boot record and the job id every answer carries derive from it (`attemptJobSlot`), because the reconciler runs several jobs under one attempt number and a repair of attempt 1 is not attempt 1 — keyed by (run, tick, attempt) alone, the settled attempt answered for every repair of it and each was refused as "already settled" (the hn6 cloud-run stall). Absent, or exactly `run-<run>/tick-<tick>/attempt-<n>`, is the attempt's own job under the names it always had. Printable ASCII, no whitespace, at most 512 characters, and it must begin `run-<run>/` for the CREDENTIAL's run — a job id naming another run is refused, never addressed. |
 * | `role` | the role this attempt runs (`implement-tick`, …), for a later cancel boot to re-derive. |
 * | `write_ref` | the attempt's own ref (`refs/heads/…`), the one the marker names and collect reads. |
 * | `base_ref` | the epic's base branch, for the same re-derivation. |
 * | `title` | the tick's title, carried for the same re-derivation. PROSE: any UTF-8 text on one line with no control character, at most 512 UTF-8 bytes (em-dashes welcome). |
 * | `base_sha` | the FULL 40-hex commit the worker clones at — the run branch head this pass pushed, not necessarily the run's submitted base (the wave door's rule, verbatim: a wave-2 worker must implement against the tree its dependencies landed in). |
 * | `model` | the model the caller's profile RESOLVED for this attempt (tick a08) — a FRESH container is booted on exactly this (`TICKS_MODEL`), outranking the deployment's `RUN_WORKER_MODEL`. Required: a start with no model would boot on the factory's own default, and the caller's record would name a model that never ran. The handle's `model` names the model the container it ANSWERS FOR is on: for a fresh boot, this field; for an adoption, the recorded model of the boot that started the running work process (tick dyo) — never an echo of what this request carried. |
 * | `harness` | the harness the caller's profile RESOLVED for this attempt (tick 9iz) — the worker container binds exactly this (`TICKS_HARNESS`), outranking the deployment's `RUN_WORKER_HARNESS`, and the handle's `harness` names it back. On a run whose workers are WorkerAgents the handle names the AGENT's harness instead (`pi-durable`, tick 4uj): the agent drives the attempt whatever harness this field names for the container its tools run in, and the handle names what ran. Required, for the model's reason verbatim: a start with no harness would boot on the factory's own default, and the caller's record would name a harness that never ran. |
 * | `prompt` | the RENDERED role prompt the caller's profile resolved (tick 9iz) — the profile's own prompt text, not a filename and not a reference. The worker container's entrypoint renders its worker prompt from the checkout's tracker and never sees the factory's prompt otherwise; the door delivers it into the container's boot environment (`TICKS_ROLE_PROMPT`, beside the harness and the model the same boot carries), so the worker runs on the prompt the run's records digest into `prompt_digest`. Required: PROSE — any UTF-8 text with no control character but tab, LF and CR, at most 64 KiB (65536 UTF-8 bytes) — a start with no prompt would boot a worker on a prompt nobody chose. |
 * | `work_base_sha` | OPTIONAL: for a CARRIED attempt, the FULL 40-hex commit the carried work was cut from (epic hn6, run_3f034e68). `base_sha` is then the released attempt's head; the container (`TICKS_WORK_BASE_SHA`) measures the carried work from this, so a worker that finds it complete and adds nothing settles succeeded rather than no-work. Absent for every attempt that carries nothing. |
 * | `wall_seconds` | OPTIONAL: the dispatch's wall clock in whole seconds (tick 86y). The worker's harness is bounded just under it (`TICKS_WORKER_TIMEOUT`, the wall less the push margin — worker-boot.ts `workerHarnessBudgetMs`), so the container stops its harness, commits, reports and pushes before the reconciler's wall fires. Absent boots an unbounded harness. |
 * | `stuck_seconds` | OPTIONAL: the stuck watch's window in whole seconds (tick xba) — how long the hosted worker may show no activity before the watch nudges it with a steer, and again before it stops it. Carried to the attempt's WorkerAgent beside the wall; zero turns the watch off (the run's negative `StuckAfter`); absent is the default window the host states, so every hosted attempt is watched. |
 *
 * The response NEVER blocks until the attempt finishes — nothing waits. What
 * returns is a HANDLE, once the dispatch is confirmed (the green-start probe
 * and the confirmed-dispatch wait `spawnWorker` already runs), while the
 * tick's work goes on running inside the container:
 *
 *   - `201` — a fresh container was launched. Body:
 *     `{ "handle": <job_handle>, "adopted": false }`
 *   - `200` — the named container already held a live work process and was
 *     ADOPTED, not dispatched over. Body: `{ "handle": <job_handle>, "adopted": true }`.
 *     The handle is the SAME attempt's — same job id, same process — so a
 *     caller that retries an ambiguous request reaches the same answer as one
 *     whose first call landed. `adopted` is a first-class field, not text to
 *     be parsed back out of the handle's detail. The handle's `model` is the
 *     RUNNING container's, read from the door's own recorded boot (tick dyo)
 *     — a live work process whose boot was never recorded (a container an
 *     older deployment booted) is refused `409 adoption_model_unknown`
 *     rather than adopted under a model nobody observed.
 *
 *   - `503 no_capacity` — a FRESH start the account has no container slot
 *     for: every one of FACTORY_MAX_INSTANCES is held by a live run's
 *     orchestrator or a worker neither settled nor reclaimed
 *     (container-capacity.ts). Answered at once, before any container is
 *     addressed — addressing one queues it for a slot, which is the wait that
 *     outlasted the caller's client in hn6's cloud run. Retry later; an
 *     adoption is never refused for capacity.
 *
 *   - `422 invalid_sandbox_name` — the Sandbox SDK refuses the container
 *     name the job's identity derives (longer than 63 characters, a hyphen
 *     at either end, a reserved name). Permanent: the same identity derives
 *     the same name, so asking again is refused the same way. Answered on
 *     the state route too.
 *
 * `handle` is the pinned job-protocol `job_handle` record (`contracts/`
 * `$defs.job_handle`): the closed top level of identity and executor name
 * (`cloudflare-sandbox`), the issue time, and the one open `handle` object
 * carrying this substrate's private addressing — the container's name, the
 * work process id, the base, the per-attempt landing branch, the
 * write_ref, the `model` and the `harness` the container was booted
 * with. A caller re-derives NOTHING from it that the state route below
 * cannot also answer from identity alone; the handle is for the record, not
 * for addressing (a client on the far side of HTTP cannot carry a live
 * Sandbox object any more than a Workflow step can).
 *
 * ### `GET /api/sandbox/attempts/:tick_id/:attempt[?job_id=<job id>]` — the state of a named sandbox
 *
 * The identity is the same one start derived the container's name from (the
 * run, as ever, comes from the credential) — INCLUDING the job: a caller that
 * started a role job with a `job_id` asks about it with the same `job_id`
 * (URL-encoded, the same rules as the start field), and the answer's
 * `job_id` is that one. Without it the route answers for the attempt's own
 * job. A settled record for one job never answers for another. The answer is the pinned
 * job-protocol `job_status` record — one vocabulary across executors, so a
 * Go client maps it onto the `JobStatus` it already parses:
 *
 *   - `running` — the named container holds a live work process.
 *   - `succeeded` / `failed`, terminal — FINISHED WITH A RESULT: the exit
 *     code rides an `exited` observation, which is the whole result that
 *     exists at this layer; the tick's report lives in git, where every
 *     executor's collect reads it from.
 *   - `lost`, deliberately NOT terminal — ABSENT: no container under this
 *     identity holds a work process (never started, or its record is gone).
 *     The contract's own words are the point: `lost` is a statement about the
 *     observer, not a verdict on the job, and recovery may re-adopt. A caller
 *     must never read `lost` as "finished" and write the attempt off — and a
 *     caller that cannot REACH this route must never read the failure as
 *     `lost` either (tick avx's rule: unreachable is not absent, and the
 *     distinction lives in the client's error handling, not in this body).
 *
 * A read NEVER boots a container (epic hn6's second cloud run): through the
 * SDK any call on a container that is not running starts one, so the route
 * answers from D1 first — a job with no recorded boot is `lost` without its
 * container being addressed, and a settled job answers from its recorded
 * settlement (migration 0019). The first terminal observation is recorded
 * and the worker's container destroyed: its work is on its branch, and a
 * settled container left up held one of max_instances until it idled out.
 *
 * ### Where each of the other operations lives (DECIDED, tick xev)
 *
 * Which of the four operations cross this door and which the Go side does
 * from git was the open question the door shipped with (the finding triaged
 * against tick 8ty); it is decided, and this is the decision recorded where
 * the executor's doc says the contract lives — this file, the one place the
 * HTTP contract lives:
 *
 *   - **dispatch and inspect cross the door**, as built. A container cannot
 *     create a sibling sandbox — the binding is a Worker binding — so the
 *     door is the only route to boot one attempt's container and to
 *     re-address it by identity. Nothing about that is new.
 *   - **collect is the Go side's, from git, and NEVER a door route.** The
 *     orchestrator holds the clone; the worker's container pushes its
 *     per-attempt landing branch with the report its own entrypoint commits
 *     at `RESULT-<tick>.md` (image/worker.sh — in this substrate the report
 *     has to be committed, because the container is destroyed and collect
 *     reads the file off the pushed branch); the Go executor reads that
 *     durable layer (internal/exec/cloudflaresandbox/collect.go) exactly the
 *     way `worker-collect.ts` reads it through GitHub's API — commits, the
 *     changed-file list, the report — only through git, because the Go side
 *     has the clone. A collect route here would be a second mechanism
 *     beside the one the durable layer already is.
 *   - **cancel stays with the factory, and the Go executor refuses it
 *     typed.** The credential a sandbox attempt holds is the run's OWN
 *     gateway token (D17) — one credential shared by every attempt of the
 *     run, revoked only as the run-level kill switch this door already
 *     honours (`403 run_token_revoked`) — so the local executor's
 *     revoke-then-signal contract has nothing to revoke on this side of the
 *     boundary. Stopping one container is the salvage door and teardown
 *     `worker-boot.ts`/`worker-dispatch.ts` already own, and the
 *     queue-expiry sweep behind them; a per-tick cancel route would only
 *     ever half-exist beside machinery that already does the whole job.
 *   - **dispose has nothing to act on across this boundary, and is refused
 *     typed.** The Go executor owns no worktree, no local branch and no
 *     credential; the container belongs to the factory that booted it; the
 *     branch the work landed on is retired by the close.
 *
 * The door therefore stays exactly two routes BY DECISION, not by omission:
 * `POST /api/sandbox/attempts` and
 * `GET /api/sandbox/attempts/:tick_id/:attempt` are the whole of it, and a
 * third route is a change to this contract that starts here.
 *
 * ### The WorkerAgent routes (DECIDED, epic 43y tick xd3)
 *
 * That change: every worker is a WorkerAgent (worker-agent.ts) — the
 * attempt's pi-durable conversation in a Durable Object of its own, its
 * tools in its container, on whichever substrate the run was submitted on
 * (`do_v1` or the default `sdk0`, tick hxd). The two routes
 * above answer from the agent for such a run: the start records the attempt
 * on its agent (an agent already holding it is the adoption), and the state
 * route answers the agent's phase — `running` until it settles, then the
 * attempt's exit code, the finish phase's — copies its log by cursor, and
 * releases its container once settled. The reclaim stops the agent before
 * its container is destroyed (container-capacity.ts). Nothing about either
 * route's contract changes for the caller.
 *
 * A hosted attempt has a live conversation, and two routes reach it, on the
 * same run credential and the same identity (`?job_id=` as on the state
 * route):
 *
 *   - `GET /api/sandbox/attempts/:tick_id/:attempt/watch` — a WebSocket: the
 *     attempt's state, then the conversation's agent events (pi-durable
 *     `watchEvents`, one batch per commit, a snapshot first) and its log as
 *     they land; a `{"type":"steer","text":…}` message steers.
 *   - `POST /api/sandbox/attempts/:tick_id/:attempt/steer` — `{"text": …,
 *     "request_id"?: …}`, placed after the running tool round: `202
 *     {submission}`, `409 not_conversing` when nothing is running to steer.
 *
 * Both answer `409 not_hosted` for a run whose workers are not WorkerAgents.
 *
 * ### The lease (D4)
 *
 * `POST` verifies — never acquires — the project's dispatch lease, exactly as
 * the wave door does: the in-run orchestrator is the holder, and a holder
 * does not take a second lease. Anything that is not the holder is refused
 * here exactly as a competing dispatch is refused at submission
 * (`409 lease_lost` when the project has no live lease, `409 lease_held_by`
 * naming the holder when another run has it). A door that boots real
 * containers must not be the one place a lapsed arbiter can still spend.
 */

import type { AttemptSpec } from "./attempt-protocol";
import { heldSlots, mayBeAdoptable } from "./container-capacity";
import { getRun } from "./db";
import { authorizeGatewayRequest, type GatewayDenial } from "./gateway";
import type { Env } from "./index";
import { BASE_SHA_PATTERN, roomFor } from "./runs";
import { sandboxBinding } from "./sandbox";
import {
  AdoptionModelUnknownError,
  attemptJobSlot,
  attemptSandboxName,
  d1JobLogs,
  d1JobRecords,
  InvalidSandboxNameError,
  namedAttemptStatus,
  type SandboxJobHandle,
  sandboxExecutorDepsFromEnv,
  sandboxNameRejection,
  specJobID,
  startNamedAttempt,
} from "./sandbox-executor";
import { type WorkerAgentStub, workerAgentsFromEnv } from "./worker-agent";

// ------------------------------------------------------------ the results ---

/**
 * What a door handler answers: a response to send, or a refusal with the
 * status, the error class and the detail — the same shape `requestWave`
 * answers in, so `index.ts` turns both into status codes the same way.
 */
export type SandboxDispatchResult =
  | { ok: true; status: number; body: Record<string, unknown> }
  /** A response the door hands back whole: the watch route's WebSocket upgrade. */
  | { ok: true; status: 101; response: Response }
  | { ok: false; status: number; error: string; detail: string };

function refuse(status: number, error: string, detail: string): SandboxDispatchResult {
  return { ok: false, status, error, detail };
}

function fromDenial(denial: GatewayDenial): SandboxDispatchResult {
  return refuse(denial.status, denial.error, denial.detail);
}

/**
 * The door's answer to a fresh start the account has no container slot for
 * (hn6's cloud run): `503 no_capacity`, at once. The Go client types it
 * (cloudflaresandbox's NoCapacity) and the orchestrator waits and asks again —
 * a full account is a fact about the world, never a failure of the run.
 */
export const NO_CAPACITY = "no_capacity";

// ------------------------------------------------------------ the shapes ---

/**
 * A tick id as this door accepts it: it names a container, so it gets a
 * container name's conservatism rather than a free-text field's tolerance.
 * Every id the tracker mints already fits; a caller that cannot state one
 * that fits has no container to name.
 */
/**
 * How long a lease the door takes back for its run lasts (hn6): long enough
 * that the supervisor's own next renewal arrives inside it, the same ten
 * minutes a run's first acquire gets (BOOT_LEASE_TTL_MS).
 */
export const DOOR_RECLAIM_LEASE_TTL_MS = 600_000;

const TICK_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/**
 * The door's fields come in two kinds, and the kind decides the rule.
 *
 * IDENTIFIER fields (`role`, `write_ref`, `base_ref`, `model`, `harness`; and
 * `tick_id`, stricter still, above): printable ASCII, NO whitespace, at most
 * 512 characters (= bytes, being ASCII) — these name things (a role, a git
 * ref, a model, a harness) and ride environment variables into the
 * container, and a name that contains a space or a rune outside ASCII is a
 * name nothing downstream can be trusted to spell the same way.
 *
 * PROSE fields (`title`, `prompt`): any valid Unicode text EXCEPT a control
 * character (general category Cc — C0, DEL and C1), with the one exception
 * that the prompt keeps tab, line feed and carriage return, the line breaks
 * markdown needs. Prose is what people and profiles write, and they write
 * em-dashes and ellipses: every profile in profiles-cloudflare-sandbox/
 * carries them, and so do tick titles. A control character is still refused
 * because both fields ride environment variables into the container, and an
 * environment value is not a place to discover what the platform does with
 * a NUL or a terminal escape. Text that is not UTF-8 is refused too: a body
 * whose bytes are not UTF-8 at all (`jsonBody`), and a lone surrogate a JSON
 * `\u` escape can smuggle in (general category Cs), which no UTF-8 encoding
 * of the environment can carry.
 *
 * The prose bounds count UTF-8 BYTES, not characters or UTF-16 code units:
 * the bound exists for the environment variable the field becomes, and that
 * is measured in bytes. The Go client (`cloudflaresandbox/record.go`)
 * counts the same bytes (`len` of a Go string).
 */
const PLAIN_FIELD_PATTERN = /^[\x21-\x7e]{1,512}$/;

/**
 * A rendered prompt (`prompt`, tick 9iz): prose plus tab / LF / CR, at most
 * {@link PROMPT_FIELD_MAX_BYTES} UTF-8 bytes (checked beside the pattern).
 */
const PROMPT_FIELD_PATTERN = /^(?:[\t\n\r]|[^\p{Cc}\p{Cs}])+$/u;

/** The prompt bound, in UTF-8 bytes, spelled once for the check and the refusal. */
const PROMPT_FIELD_MAX_BYTES = 65536;

/**
 * A free-text field (`title`): prose on one line — no control character at
 * all, not even a tab or a line break — at most
 * {@link TITLE_FIELD_MAX_BYTES} UTF-8 bytes (checked beside the pattern).
 */
const TITLE_FIELD_PATTERN = /^[^\p{Cc}\p{Cs}]+$/u;

/** The title bound, in UTF-8 bytes. */
const TITLE_FIELD_MAX_BYTES = 512;

const utf8 = new TextEncoder();

/** A prose field: a string, in its class, at most `maxBytes` of UTF-8. */
function isProse(field: unknown, pattern: RegExp, maxBytes: number): field is string {
  // The pattern first: it refuses a lone surrogate, which the encoder would
  // otherwise quietly rewrite to U+FFFD before it was counted.
  return (
    typeof field === "string" &&
    pattern.test(field) &&
    // A UTF-16 code unit is at most 3 UTF-8 bytes, so a short string needs
    // no encoding to know it fits.
    (field.length * 3 <= maxBytes || utf8.encode(field).byteLength <= maxBytes)
  );
}

const strictUTF8 = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false });

/** Reads the request body as a JSON object, or says why it cannot be. */
async function jsonBody(
  request: Request,
): Promise<
  { ok: true; raw: Record<string, unknown> } | { ok: false; refusal: SandboxDispatchResult }
> {
  // Decoded strictly, not through `request.json()`: that decoder rewrites a
  // byte that is not UTF-8 into U+FFFD, and the door would then boot a worker
  // on text its caller never sent.
  let text: string;
  try {
    text = strictUTF8.decode(await request.arrayBuffer());
  } catch {
    return {
      ok: false,
      refusal: refuse(400, "invalid_request", "the request body must be UTF-8 JSON"),
    };
  }
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    return { ok: false, refusal: refuse(400, "invalid_request", "the request body must be JSON") };
  }
  if (body === null || typeof body !== "object" || Array.isArray(body)) {
    return {
      ok: false,
      refusal: refuse(400, "invalid_request", "the request body must be a JSON object"),
    };
  }
  return { ok: true, raw: body as Record<string, unknown> };
}

/**
 * Reads an optional `job_id`: undefined when absent, the id when it is a
 * name this door keys a container by, or the refusal. It must belong to the
 * CREDENTIAL's run (`run-<run>/…`): the run is never the caller's to state,
 * and a job id naming another run is refused rather than addressed.
 */
function jobIDOf(runID: string, field: unknown): string | undefined | SandboxDispatchResult {
  if (field === undefined || field === null) return undefined;
  if (
    typeof field !== "string" ||
    !PLAIN_FIELD_PATTERN.test(field) ||
    !field.startsWith(`run-${runID}/`)
  ) {
    return refuse(
      400,
      "invalid_request",
      `job_id must be the full job id of a job of run ${runID} (printable ASCII with no spaces, ` +
        `at most 512 characters, beginning run-${runID}/) — it is the identity the job's container is named by`,
    );
  }
  return field;
}

// --------------------------------------------------------------- the door ---

/**
 * `POST /api/sandbox/attempts` — start one attempt's worker container, named
 * by the attempt's identity, and return its handle without waiting for it.
 */
async function startAttemptRoute(env: Env, request: Request): Promise<SandboxDispatchResult> {
  // The credential is the run's own gateway token, verified exactly as model
  // traffic and wave requests are: unknown, revoked, orphaned and finished are
  // four distinct verdicts and stay distinct.
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) return fromDenial(authorized.denial);
  const run = authorized.run;

  const parsed = await jsonBody(request);
  if (!parsed.ok) return parsed.refusal;
  const raw = parsed.raw;

  // The epic is stated and checked rather than taken from the run, so a
  // container that has somehow drifted onto another epic is refused instead
  // of silently dispatching this run's tick under another one's name — the
  // wave door's check, applied to the same kind of caller.
  if (typeof raw.epic !== "string" || raw.epic !== run.epic) {
    return refuse(
      400,
      "invalid_request",
      `epic must be ${JSON.stringify(run.epic)}, the epic run ${run.run_id} is working on`,
    );
  }

  if (typeof raw.tick_id !== "string" || !TICK_ID_PATTERN.test(raw.tick_id)) {
    return refuse(
      400,
      "invalid_request",
      "tick_id must name the tick this attempt implements (alphanumerics, `.`, `_`, `-`; " +
        "at most 64 characters) — it is the name the attempt's container is addressed by",
    );
  }
  const tickID = raw.tick_id;

  // 1-based, per tick: the number the attempt's branch and container name
  // carry, and the wave machinery's own vocabulary.
  const attempt = raw.attempt;
  if (typeof attempt !== "number" || !Number.isInteger(attempt) || attempt < 1) {
    return refuse(
      400,
      "invalid_request",
      "attempt must be the positive integer that identifies this try of the tick",
    );
  }

  // The job, when it is not the attempt itself (a repair, a resolve, a base
  // fold): the FULL job id is the identity the container is named by.
  const jobID = jobIDOf(run.run_id, raw.job_id);
  if (typeof jobID !== "string" && jobID !== undefined) return jobID;

  // The dispatch's wall (tick 86y), optional: the worker's harness is bounded
  // just under it, and an older client that sends none boots unbounded.
  const wallSeconds = raw.wall_seconds;
  if (
    wallSeconds !== undefined &&
    (typeof wallSeconds !== "number" || !Number.isInteger(wallSeconds) || wallSeconds < 1)
  ) {
    return refuse(
      400,
      "invalid_request",
      "wall_seconds, when present, must be the dispatch's wall clock as a positive whole number of seconds",
    );
  }

  // The stuck watch's window (tick xba), optional: how long the hosted
  // worker may show no activity before the watch nudges it with a steer, and
  // again before it stops it. Zero turns the watch off — the run's negative
  // StuckAfter, spelled as zero because a negative window is refused here
  // like any other malformed bound — and absent is the default window the
  // attempt's host states, so an older client that sends none still gets the
  // watch.
  const stuckSeconds = raw.stuck_seconds;
  if (
    stuckSeconds !== undefined &&
    (typeof stuckSeconds !== "number" || !Number.isInteger(stuckSeconds) || stuckSeconds < 0)
  ) {
    return refuse(
      400,
      "invalid_request",
      "stuck_seconds, when present, must be the stuck watch's window as a whole number of seconds (0 turns the watch off)",
    );
  }

  const text = (name: string, field: unknown): string | SandboxDispatchResult => {
    if (typeof field !== "string" || !PLAIN_FIELD_PATTERN.test(field)) {
      return refuse(
        400,
        "invalid_request",
        `${name} must be a non-empty printable ASCII string with no spaces (at most 512 characters)`,
      );
    }
    return field;
  };
  const role = text("role", raw.role);
  if (typeof role !== "string") return role;
  const writeRef = text("write_ref", raw.write_ref);
  if (typeof writeRef !== "string") return writeRef;
  const baseRef = text("base_ref", raw.base_ref);
  if (typeof baseRef !== "string") return baseRef;
  if (!isProse(raw.title, TITLE_FIELD_PATTERN, TITLE_FIELD_MAX_BYTES)) {
    return refuse(
      400,
      "invalid_request",
      "title must be a non-empty line of UTF-8 text with no control characters " +
        `(at most ${TITLE_FIELD_MAX_BYTES} bytes)`,
    );
  }
  const title = raw.title;
  // The model the caller resolved (tick a08). Required, and booted as given:
  // a door that fell back to the deployment's own model here would hand the
  // caller a handle for a worker running something its records do not name.
  const model = text("model", raw.model);
  if (typeof model !== "string") return model;
  // The harness the caller resolved (tick 9iz). Required, and bound as given,
  // for the model's reason verbatim.
  const harness = text("harness", raw.harness);
  if (typeof harness !== "string") return harness;
  // The rendered role prompt the caller resolved (tick 9iz). Required: the
  // container's own entrypoint builds its worker prompt from the checkout's
  // tracker, so the profile's prompt reaches the worker through this field or
  // not at all — and a door that booted without it would be a door whose
  // caller's `prompt_digest` named a prompt that never ran.
  if (!isProse(raw.prompt, PROMPT_FIELD_PATTERN, PROMPT_FIELD_MAX_BYTES)) {
    return refuse(
      400,
      "invalid_request",
      "prompt must be the rendered role prompt the dispatch resolved (UTF-8 text with line " +
        `breaks and no other control characters, at most ${PROMPT_FIELD_MAX_BYTES} bytes) — the worker's ` +
        "container runs on it, and a start with none would boot a worker on a prompt nobody chose",
    );
  }
  const prompt = raw.prompt;

  // Not the run's submitted base: the caller names the commit this attempt's
  // worker must clone at, which for a later wave is the run branch head that
  // pass pushed. Full 40-hex, refused rather than parsed (the same rule the
  // submission boundary applies).
  if (typeof raw.base_sha !== "string" || !BASE_SHA_PATTERN.test(raw.base_sha)) {
    return refuse(
      400,
      "invalid_request",
      "base_sha must be the full 40-character commit the attempt's worker clones at — " +
        "the run branch head this pass pushed, not the run's original base",
    );
  }

  // A carried attempt's work base (epic hn6, run_3f034e68), optional: the
  // commit the carried work was cut from, which the worker's container
  // measures the carried work against. Full 40-hex, like base_sha.
  const workBaseSHA = raw.work_base_sha;
  if (
    workBaseSHA !== undefined &&
    (typeof workBaseSHA !== "string" || !BASE_SHA_PATTERN.test(workBaseSHA))
  ) {
    return refuse(
      400,
      "invalid_request",
      "work_base_sha, when present, must be the full 40-character commit a carried attempt's " +
        "work was cut from",
    );
  }

  // The arbiter (D4). Not acquired — verified, exactly as the wave door
  // verifies it: the in-run orchestrator is the holder, and a holder does not
  // take a second lease. Anything that is not the holder is refused here
  // exactly as a competing dispatch is refused at submission.
  //
  // A lease that LAPSED with nobody else holding it is not a stop (hn6,
  // run_3ca22fbd): the Workflow that renews it took no step for three minutes,
  // the 60s lease lapsed under a healthy orchestrator, and its first dispatch
  // was refused. The caller has just proven it is the run (an unrevoked
  // credential of an active run), so the door takes the lease back for it —
  // the room's compare-and-swap under the run's own token, refused if any
  // other run has since taken the project. Only THAT is a stop.
  const room = roomFor(env, run.project);
  const lease = await room.reclaimLapsedLeaseFor({
    run_id: run.run_id,
    ttl_ms: DOOR_RECLAIM_LEASE_TTL_MS,
  });
  if (lease.outcome === "taken") {
    return refuse(
      409,
      "lease_held_by",
      `the dispatch lease for ${run.project} is held by ${lease.holder.run_id}, not ${run.run_id}; ` +
        "one arbiter per project (D4): another run is driving this project now, so this run stops " +
        "rather than write beside it — let that run finish (or stop it) and resume this one",
    );
  }
  if (lease.outcome === "unknown") {
    return refuse(
      409,
      "lease_lost",
      `run ${run.run_id} holds no dispatch lease for ${run.project} and none of its own lapsed here ` +
        `to take back (${lease.detail}); nobody else holds it either, so its supervisor's next ` +
        "renewal can reclaim it — ask again",
    );
  }
  if (lease.outcome === "reclaimed") {
    console.warn(
      `factory sandbox door: ${run.run_id} reclaimed its lapsed dispatch lease: ${lease.detail}`,
    );
  }

  // The wiring the deployment can actually dispatch through — the same
  // diagnosis `sandboxExecutorFromEnv` gives the Workflow, stated in the
  // response rather than only on the Worker's log, because the caller is a
  // container reading one answer. The run id is the credential's own: the
  // worker this boot streams is this run's, and its logs land under the
  // run's own artifacts where the log read routes serve them (tick 9iz).
  const wired = sandboxExecutorDepsFromEnv(env, {
    project: run.project,
    base_sha: raw.base_sha,
    run_id: run.run_id,
  });
  if ("refusal" in wired) {
    return refuse(
      503,
      "sandbox_dispatch_not_wired",
      `this deployment cannot dispatch a worker sandbox: ${wired.refusal}`,
    );
  }

  // The spec the executor starts from. run_id and project come from the
  // credential, never from the body: the credential decides whose dispatch
  // this is, which is the whole difference between a door and an open route.
  const spec: AttemptSpec = {
    run_id: run.run_id,
    epic_id: run.epic,
    tick_id: tickID,
    attempt,
    ...(jobID === undefined ? {} : { job_id: jobID }),
    role,
    project: run.project,
    write_ref: writeRef,
    base_ref: baseRef,
    title,
    model,
    harness,
    prompt,
    ...(wallSeconds === undefined ? {} : { wall_seconds: wallSeconds }),
    ...(stuckSeconds === undefined ? {} : { stuck_seconds: stuckSeconds }),
    ...(workBaseSHA === undefined ? {} : { work_base_sha: workBaseSHA }),
  };

  // The account's capacity (hn6's cloud run, container-capacity.ts). A FRESH
  // boot addresses a container that is not running, and the platform queues
  // that for a free instance — a wait that outlasted the caller's client
  // twelve times over, each read as a transient remote. So the slots are
  // counted first and a full account is answered at once, typed: 503
  // no_capacity, which the orchestrator treats as "wait and ask again" and
  // never as a failure. A start under an identity whose container may still
  // be live is an ADOPTION and needs no slot, so it is never refused for one.
  const adoptable = await mayBeAdoptable(env.DB, {
    run_id: run.run_id,
    tick_id: tickID,
    attempt,
    job: attemptJobSlot(run.run_id, tickID, attempt, jobID) ?? "",
  });
  if (!adoptable) {
    const slots = await heldSlots(env.DB, env);
    if (slots.held >= slots.max) {
      return refuse(
        503,
        NO_CAPACITY,
        `every container slot this factory's account may run is held (${slots.held} of ${slots.max}: ` +
          `${slots.orchestrators} run orchestrator(s), ${slots.workers} worker(s)) — nothing was started. ` +
          "Ask again later: a slot frees when a worker settles, when a run ends, or when the hourly sweep " +
          "reclaims a container whose run is over",
      );
    }
  }

  // The machinery, not a copy of it: `startNamedAttempt` resolves the container
  // BY NAME, adopts a live work process instead of booting a rival beside it,
  // and returns once the dispatch is confirmed — a handle, never a result.
  // The one refusal the machinery itself can make is the adoption whose
  // running container has no recorded boot (tick dyo): the door cannot state
  // the model the handle must name, and a refusal the caller holds is honest
  // where a guess in a handle would not be.
  let started: Awaited<ReturnType<typeof startNamedAttempt>>;
  try {
    started = await startNamedAttempt(wired.deps, spec);
  } catch (error) {
    if (error instanceof AdoptionModelUnknownError) {
      return refuse(409, "adoption_model_unknown", error.message);
    }
    if (error instanceof InvalidSandboxNameError) {
      return refuse(422, INVALID_SANDBOX_NAME, error.message);
    }
    throw error;
  }
  const body: Record<string, unknown> = { handle: started.handle, adopted: started.adopted };
  return {
    ok: true,
    // 200 for the adoption, 201 for the fresh dispatch — the branch-record
    // door's own convention for an idempotent-by-identity write: the attempt
    // is running either way, and the caller is told which happened.
    status: started.adopted ? 200 : 201,
    body,
  };
}

/**
 * `GET /api/sandbox/attempts/:tick_id/:attempt` — the state of the named
 * sandbox: running, finished with a result, or absent.
 */
async function attemptStatusRoute(
  env: Env,
  request: Request,
  tickID: string,
  attemptText: string,
): Promise<SandboxDispatchResult> {
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) return fromDenial(authorized.denial);
  const run = authorized.run;

  if (!TICK_ID_PATTERN.test(tickID)) {
    return refuse(400, "invalid_request", "the tick id in the path is not a name this door reads");
  }
  if (/^[1-9][0-9]*$/.test(attemptText) === false) {
    return refuse(400, "invalid_request", "the attempt in the path must be a positive integer");
  }
  const attempt = Number(attemptText);
  const jobID = jobIDOf(run.run_id, new URL(request.url).searchParams.get("job_id") ?? undefined);
  if (typeof jobID !== "string" && jobID !== undefined) return jobID;

  const binding = sandboxBinding(env);
  if (binding === null) {
    return refuse(
      503,
      "sandbox_dispatch_not_wired",
      "the SANDBOXES container binding (a deployment that cannot boot a container cannot " +
        "inspect one either)",
    );
  }

  // The same job id the handle carries (`specJobID`), so a client keying
  // on job_id reads one value whether it asked to start the job or to read
  // it back — and the container asked about is THIS job's, never the attempt
  // it repairs. Re-addressed BY NAME on every look — never a live object.
  const identity = {
    run_id: run.run_id,
    tick_id: tickID,
    attempt,
    ...(jobID === undefined ? {} : { job_id: jobID }),
  };
  // Answered from the job records before any container is addressed: a
  // status read must never boot one (namedAttemptStatus says why).
  const status = await namedAttemptStatus(
    binding,
    identity,
    specJobID(identity),
    undefined,
    d1JobRecords(env.DB),
    // The worker's output past its confirm window, copied on every look and
    // drained before a settled container is reclaimed (tick 86y).
    env.ARTIFACTS === undefined ? undefined : d1JobLogs(env.DB, env.ARTIFACTS, run.project),
    // A run whose workers are WorkerAgents answers from the agent (tick xd3).
    workerAgentsFromEnv(env),
  );
  return { ok: true, status: 200, body: status as unknown as Record<string, unknown> };
}

/** The class for a live-conversation route on a run whose workers are not WorkerAgents. */
export const NOT_HOSTED = "not_hosted";

/** The class for a steer the attempt cannot take: it is not conversing. */
export const NOT_CONVERSING = "not_conversing";

/**
 * The attempt's WorkerAgent, for the routes that only a hosted attempt has —
 * or the refusal: the run's workers are not WorkerAgents (`409 not_hosted`),
 * or the deployment binds none.
 */
async function hostedAgent(
  env: Env,
  request: Request,
  tickID: string,
  attemptText: string,
): Promise<
  | { ok: true; agent: WorkerAgentStub; run_id: string }
  | { ok: false; refusal: SandboxDispatchResult }
> {
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) return { ok: false, refusal: fromDenial(authorized.denial) };
  const found = await agentFor(env, request, authorized.run.run_id, tickID, attemptText);
  if (!found.ok) return found;
  return { ok: true, agent: found.agent, run_id: authorized.run.run_id };
}

/**
 * One attempt's WorkerAgent, by its identity — run, tick, attempt and the
 * job id `?job_id=` names (the attempt's own job when absent) — or the
 * refusal: a path that names no attempt, a run whose workers are not
 * WorkerAgents (`409 not_hosted`), or a deployment that binds none. The
 * caller has already decided who may ask: the run credential on the door's
 * routes, the operator's token on /api/runs.
 */
async function agentFor(
  env: Env,
  request: Request,
  runID: string,
  tickID: string,
  attemptText: string,
): Promise<
  | { ok: true; agent: WorkerAgentStub; name: string; attempt: number }
  | { ok: false; refusal: SandboxDispatchResult }
> {
  if (!TICK_ID_PATTERN.test(tickID)) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "the tick id in the path is not a name this door reads",
      ),
    };
  }
  if (/^[1-9][0-9]*$/.test(attemptText) === false) {
    return {
      ok: false,
      refusal: refuse(400, "invalid_request", "the attempt in the path must be a positive integer"),
    };
  }
  const jobID = jobIDOf(runID, new URL(request.url).searchParams.get("job_id") ?? undefined);
  if (typeof jobID !== "string" && jobID !== undefined) return { ok: false, refusal: jobID };
  const agents = workerAgentsFromEnv(env);
  const hosting = agents === undefined ? null : await agents(runID);
  if (hosting === null) {
    return {
      ok: false,
      refusal: refuse(
        409,
        NOT_HOSTED,
        `run ${runID}'s workers are not WorkerAgents (no WORKER_AGENTS binding on this ` +
          "deployment, or no D1 to read the run's substrate with — a hosted attempt has no live " +
          "conversation to watch or steer)",
      ),
    };
  }
  const attempt = Number(attemptText);
  const name = attemptSandboxName(runID, tickID, attempt, jobID);
  return { ok: true, agent: hosting.agent(name), name, attempt };
}

/**
 * `GET /api/sandbox/attempts/:tick_id/:attempt/watch` — a WebSocket onto the
 * attempt's WorkerAgent: its state, the live conversation's agent events and
 * its log as they are committed; a `{"type":"steer","text":…}` message steers.
 */
async function attemptWatchRoute(
  env: Env,
  request: Request,
  tickID: string,
  attemptText: string,
): Promise<SandboxDispatchResult> {
  if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
    return refuse(
      426,
      "upgrade_required",
      "the watch route is a WebSocket: send Upgrade: websocket",
    );
  }
  const found = await hostedAgent(env, request, tickID, attemptText);
  if (!found.ok) return found.refusal;
  return { ok: true, status: 101, response: await found.agent.fetch(request) };
}

/**
 * `POST /api/sandbox/attempts/:tick_id/:attempt/steer` — `{"text": …,
 * "request_id"?: …}`: operator input placed after the attempt's running tool
 * round (pi-durable `whenBusy: "steer"`); the stuck nudge is one of these.
 * `202 {submission}`, or `409 not_conversing` when there is no running
 * conversation to place it in.
 */
async function attemptSteerRoute(
  env: Env,
  request: Request,
  tickID: string,
  attemptText: string,
): Promise<SandboxDispatchResult> {
  const found = await hostedAgent(env, request, tickID, attemptText);
  if (!found.ok) return found.refusal;
  return steerAgent(found.agent, request);
}

/** A steer's body read, validated and placed on the agent: 202, or the refusal. */
async function steerAgent(
  agent: WorkerAgentStub,
  request: Request,
): Promise<SandboxDispatchResult> {
  const parsed = await jsonBody(request);
  if (!parsed.ok) return parsed.refusal;
  const text = parsed.raw.text;
  if (!isProse(text, PROMPT_FIELD_PATTERN, PROMPT_FIELD_MAX_BYTES)) {
    return refuse(
      400,
      "invalid_request",
      `text must be the steer's UTF-8 text (line breaks allowed, at most ${PROMPT_FIELD_MAX_BYTES} bytes)`,
    );
  }
  const requestID = parsed.raw.request_id;
  if (
    requestID !== undefined &&
    (typeof requestID !== "string" || !PLAIN_FIELD_PATTERN.test(requestID))
  ) {
    return refuse(
      400,
      "invalid_request",
      "request_id, when present, must be printable ASCII with no spaces (at most 512 characters)",
    );
  }
  const steered = await agent.steer(text, requestID);
  if (!steered.ok) return refuse(409, NOT_CONVERSING, steered.error);
  return { ok: true, status: 202, body: { submission: steered.submission } };
}

// ------------------------------------------------- the operator's window ---

/** The class for an operator route naming a tick no hosted attempt was booted for. */
export const NO_ATTEMPT = "no_attempt";

/**
 * The operator's window into one hosted worker (tick y03): the same agent
 * the door's watch and steer routes reach, addressed by the run's id in the
 * path and authorized by the OPERATOR's factory token — index.ts has already
 * checked it before routing, as for every /api/runs path. `ticfac watch
 * <run> <tick>` and `ticfac steer` are its client.
 *
 *   - `GET  /api/runs/:run_id/workers/:tick_id/:attempt` — the attempt's
 *     state (WorkerAgentState) with the attempt it resolved;
 *   - `GET  /api/runs/:run_id/workers/:tick_id/:attempt/watch` — the
 *     agent's watch WebSocket: its state, the live conversation's agent
 *     events and its log as they land; a `{"type":"steer","text":…}`
 *     message steers;
 *   - `POST /api/runs/:run_id/workers/:tick_id/:attempt/steer` — `{"text":
 *     …, "request_id"?: …}` placed after the running tool round: `202
 *     {submission}`, `409 not_conversing` when nothing is running.
 *
 * `:attempt` is a positive integer, or `latest`: the newest attempt of the
 * tick this factory booted a worker for (the boot record every hosted start
 * writes before its agent is started), which is what an operator watching
 * "the worker on y03" means. `?job_id=` addresses a job other than the
 * attempt's own, as on the door. An unknown run is `404 not_found`, a tick
 * with no booted attempt `404 no_attempt`, a run whose workers are not
 * WorkerAgents `409 not_hosted`.
 *
 * A steer is the one write here, and it is the operator's own: the status,
 * logs and events routes stay read-only (D21) — this route steers ONE
 * worker's conversation, never the run.
 */
export async function operatorWorkerRoute(
  request: Request,
  env: Env,
  runID: string,
  segments: string[],
): Promise<SandboxDispatchResult> {
  try {
    return await routeOperatorWorker(request, env, runID, segments);
  } catch (error) {
    return doorFault(request, error);
  }
}

async function routeOperatorWorker(
  request: Request,
  env: Env,
  runID: string,
  segments: string[],
): Promise<SandboxDispatchResult> {
  const [tickID = "", attemptText = "", action] = segments;
  if (
    segments.length < 2 ||
    segments.length > 3 ||
    (action !== undefined && action !== "watch" && action !== "steer")
  ) {
    return refuse(
      404,
      "not_found",
      "the worker routes are GET /api/runs/:run_id/workers/:tick_id/:attempt, GET …/watch and POST …/steer",
    );
  }
  const run = await getRun(env.DB, runID);
  if (run === null) return refuse(404, "not_found", `no run ${runID} in this factory`);
  let attempt = attemptText;
  if (attempt === "latest") {
    if (!TICK_ID_PATTERN.test(tickID)) {
      return refuse(
        400,
        "invalid_request",
        "the tick id in the path is not a name this door reads",
      );
    }
    const newest = await env.DB.prepare(
      "SELECT MAX(attempt) AS attempt FROM sandbox_job_boot WHERE run_id = ? AND tick_id = ? AND job = ''",
    )
      .bind(runID, tickID)
      .first<{ attempt: number | null }>();
    if (newest?.attempt === null || newest?.attempt === undefined) {
      return refuse(404, NO_ATTEMPT, `no worker of tick ${tickID} was booted for run ${runID}`);
    }
    attempt = String(newest.attempt);
  }
  const found = await agentFor(env, request, runID, tickID, attempt);
  if (!found.ok) return found.refusal;
  if (action === "watch") {
    if (request.method !== "GET") {
      return refuse(405, "method_not_allowed", "the watch route is a GET WebSocket upgrade");
    }
    if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
      return refuse(
        426,
        "upgrade_required",
        "the watch route is a WebSocket: send Upgrade: websocket",
      );
    }
    return { ok: true, status: 101, response: await found.agent.fetch(request) };
  }
  if (action === "steer") {
    if (request.method !== "POST")
      return refuse(405, "method_not_allowed", "the steer route is POST");
    return steerAgent(found.agent, request);
  }
  if (request.method !== "GET") {
    return refuse(405, "method_not_allowed", "the worker's state route is GET");
  }
  return {
    ok: true,
    status: 200,
    body: {
      run_id: runID,
      tick_id: tickID,
      attempt: found.attempt,
      name: found.name,
      state: await found.agent.state(),
    },
  };
}

/**
 * The door's router: `sandboxAttemptRoute(request, env, segments)`, where
 * `segments` is everything after `/api/sandbox/attempts` — `[]` for the
 * start route, `[tick_id, attempt]` for the state route. Method and shape
 * are decided here so `index.ts` stays what it is everywhere else: routing
 * only — status codes, methods, body shapes.
 */
export async function sandboxAttemptRoute(
  request: Request,
  env: Env,
  segments: string[],
): Promise<SandboxDispatchResult> {
  // Every answer is the door's documented {error, detail} shape — including
  // the one for a throw nothing below anticipated (the container platform
  // failing a start mid-rollout, a binding that rejects). Uncaught, a throw
  // became the runtime's own 500 page, which the orchestrator could only
  // read as `unreadable_refusal` with no reason in it (epic hn6's cloud run:
  // two resolve-conflict starts lost that way at 13:29, while the container
  // application was rolling out an image it could not pull).
  try {
    return await routeSandboxAttempt(request, env, segments);
  } catch (error) {
    return doorFault(request, error);
  }
}

/** The class a throw inside the door answers with. */
export const DOOR_FAULT = "door_fault";

/**
 * The class for a container name the Sandbox SDK refuses: 422, permanent —
 * the name is derived from the job's identity, so every ask under it is
 * refused the same way, and a caller that retried it would spend its
 * allowance on an answer it already has.
 */
export const INVALID_SANDBOX_NAME = "invalid_sandbox_name";

/** How much of a thrown error's message the door hands back. */
const DOOR_FAULT_DETAIL_MAX = 400;

/**
 * A throw inside the door, answered typed: `500 door_fault`, with the throw's
 * own first line (bounded) so the run's feed can say why. The caller is the
 * run's own container, authenticated by its run token, so the reason is its
 * to read; the full error goes to the Worker's log.
 */
export function doorFault(request: Request, error: unknown): SandboxDispatchResult {
  const rejected = sandboxNameRejection(error);
  if (rejected !== null) {
    return refuse(
      422,
      INVALID_SANDBOX_NAME,
      `the Sandbox SDK refuses the container name this job's identity derives (${rejected}); ` +
        "asking again under the same identity is refused the same way",
    );
  }
  const text =
    error instanceof Error ? `${error.name}: ${error.message}` : String(error ?? "unknown error");
  const line = (text.split("\n")[0] ?? "").slice(0, DOOR_FAULT_DETAIL_MAX);
  console.error(
    `factory sandbox door: ${request.method} ${new URL(request.url).pathname} threw: ${text}`,
  );
  return refuse(
    500,
    DOOR_FAULT,
    `the sandbox dispatch door failed while answering this request (${line}); whatever it had ` +
      "started is under this attempt's identity, so asking again adopts it rather than booting a rival",
  );
}

async function routeSandboxAttempt(
  request: Request,
  env: Env,
  segments: string[],
): Promise<SandboxDispatchResult> {
  if (segments.length === 0) {
    if (request.method !== "POST") {
      return refuse(405, "method_not_allowed", "the start route is POST /api/sandbox/attempts");
    }
    return startAttemptRoute(env, request);
  }
  if (segments.length === 2) {
    if (request.method !== "GET") {
      return refuse(
        405,
        "method_not_allowed",
        `the state route is GET /api/sandbox/attempts/${segments[0]}/<attempt>`,
      );
    }
    return attemptStatusRoute(env, request, segments[0]!, segments[1]!);
  }
  if (segments.length === 3 && segments[2] === "watch") {
    if (request.method !== "GET") {
      return refuse(405, "method_not_allowed", "the watch route is a GET WebSocket upgrade");
    }
    return attemptWatchRoute(env, request, segments[0]!, segments[1]!);
  }
  if (segments.length === 3 && segments[2] === "steer") {
    if (request.method !== "POST") {
      return refuse(405, "method_not_allowed", "the steer route is POST");
    }
    return attemptSteerRoute(env, request, segments[0]!, segments[1]!);
  }
  return refuse(
    404,
    "not_found",
    "the sandbox dispatch door serves POST /api/sandbox/attempts, " +
      "GET /api/sandbox/attempts/:tick_id/:attempt, and for a hosted attempt " +
      "GET …/:tick_id/:attempt/watch and POST …/:tick_id/:attempt/steer",
  );
}

/**
 * The handle type re-exported beside the contract it belongs to: a consumer
 * of this door reads its response shape from the module that documents the
 * door, without reaching into the executor's addressing for it.
 */
export type { SandboxJobHandle };
