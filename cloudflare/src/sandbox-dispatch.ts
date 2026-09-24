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
 * The request body, every field required:
 *
 * | field | what it is |
 * |---|---|
 * | `epic` | the epic the dispatch belongs to. Checked against the run's own row — a container that has somehow drifted onto another epic is refused rather than silently dispatching this run's tick under another one's name (the wave door's rule, verbatim). |
 * | `tick_id` | the tick this attempt implements. It names the container (`<run>-<tick>-<attempt>`), so it is constrained to the conservative shape a name needs: alphanumerics, `.`, `_`, `-`, first character alphanumeric, at most 64 characters. |
 * | `attempt` | 1-based attempt number, a positive integer. The attempt is in the container's name ON PURPOSE: a redispatch after a spent attempt must land in a FRESH container — reusing the name is how you inherit whatever broke it. |
 * | `role` | the role this attempt runs (`implement-tick`, …), for a later cancel boot to re-derive. |
 * | `write_ref` | the attempt's own ref (`refs/heads/…`), the one the marker names and collect reads. |
 * | `base_ref` | the epic's base branch, for the same re-derivation. |
 * | `title` | the tick's title, carried for the same re-derivation. |
 * | `base_sha` | the FULL 40-hex commit the worker clones at — the run branch head this pass pushed, not necessarily the run's submitted base (the wave door's rule, verbatim: a wave-2 worker must implement against the tree its dependencies landed in). |
 * | `model` | the model the caller's profile RESOLVED for this attempt (tick a08) — a FRESH container is booted on exactly this (`TICKS_MODEL`), outranking the deployment's `RUN_WORKER_MODEL`. Required: a start with no model would boot on the factory's own default, and the caller's record would name a model that never ran. The handle's `model` names the model the container it ANSWERS FOR is on: for a fresh boot, this field; for an adoption, the recorded model of the boot that started the running work process (tick dyo) — never an echo of what this request carried. |
 * | `harness` | the harness the caller's profile RESOLVED for this attempt (tick 9iz) — the worker container binds exactly this (`TICKS_HARNESS`), outranking the deployment's `RUN_WORKER_HARNESS`, and the handle's `harness` names it back. Required, for the model's reason verbatim: a start with no harness would boot on the factory's own default, and the caller's record would name a harness that never ran. |
 * | `prompt` | the RENDERED role prompt the caller's profile resolved (tick 9iz) — the profile's own prompt text, not a filename and not a reference. The worker container's entrypoint renders its worker prompt from the checkout's tracker and never sees the factory's prompt otherwise; the door delivers it into the container's boot environment (`TICKS_ROLE_PROMPT`, beside the harness and the model the same boot carries), so the worker runs on the prompt the run's records digest into `prompt_digest`. Required: printable prose with line breaks, at most 64 KiB — a start with no prompt would boot a worker on a prompt nobody chose. |
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
 * ### `GET /api/sandbox/attempts/:tick_id/:attempt` — the state of a named sandbox
 *
 * The identity is the same one start derived the container's name from (the
 * run, as ever, comes from the credential). The answer is the pinned
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
import { authorizeGatewayRequest, type GatewayDenial } from "./gateway";
import type { Env } from "./index";
import { BASE_SHA_PATTERN, roomFor } from "./runs";
import { sandboxBinding } from "./sandbox";
import {
  AdoptionModelUnknownError,
  attemptJobID,
  namedAttemptStatus,
  type SandboxJobHandle,
  sandboxExecutorDepsFromEnv,
  startNamedAttempt,
} from "./sandbox-executor";

// ------------------------------------------------------------ the results ---

/**
 * What a door handler answers: a response to send, or a refusal with the
 * status, the error class and the detail — the same shape `requestWave`
 * answers in, so `index.ts` turns both into status codes the same way.
 */
export type SandboxDispatchResult =
  | { ok: true; status: number; body: Record<string, unknown> }
  | { ok: false; status: number; error: string; detail: string };

function refuse(status: number, error: string, detail: string): SandboxDispatchResult {
  return { ok: false, status, error, detail };
}

function fromDenial(denial: GatewayDenial): SandboxDispatchResult {
  return refuse(denial.status, denial.error, denial.detail);
}

// ------------------------------------------------------------ the shapes ---

/**
 * A tick id as this door accepts it: it names a container, so it gets a
 * container name's conservatism rather than a free-text field's tolerance.
 * Every id the tracker mints already fits; a caller that cannot state one
 * that fits has no container to name.
 */
const TICK_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/**
 * An identifier or a reference (`role`, `write_ref`, `base_ref`, `model`,
 * `harness`): printable ASCII, NO whitespace, bounded — these name things (a
 * role, a git ref, a model, a harness) and ride environment variables into
 * the container, and a name that contains a space is a name nothing
 * downstream can use.
 */
const PLAIN_FIELD_PATTERN = /^[\x21-\x7e]{1,512}$/;

/**
 * A rendered prompt (`prompt`, tick 9iz): printable prose plus the line breaks
 * markdown needs, never other control characters, bounded at 64 KiB — it
 * rides one environment variable into the container, where the worker's
 * harness reads it, and an environment value is not a place to discover what
 * the platform does with a terminal escape.
 */
// biome-ignore lint/suspicious/noControlCharactersInRegex: the tab, line feed and carriage return in this class are the line breaks a rendered markdown prompt needs; every other control character is excluded on purpose, and naming them inside the class is how that stays true.
const PROMPT_FIELD_PATTERN = /^[\x09\x0a\x0d\x20-\x7e]{1,65536}$/;

/** The prompt bound, in characters, spelled once for the pattern and the refusal. */
const PROMPT_FIELD_MAX = 65536;

/**
 * A free-text field (`title`): printable ASCII plus the spaces prose needs,
 * never control characters — it rides an environment variable into the
 * container for a later boot's re-derivation, and an environment value is
 * not a place to discover what the platform does with a newline.
 */
const TITLE_FIELD_PATTERN = /^[\x20-\x7e]{1,512}$/;

/** Reads the request body as a JSON object, or says why it cannot be. */
async function jsonBody(
  request: Request,
): Promise<
  { ok: true; raw: Record<string, unknown> } | { ok: false; refusal: SandboxDispatchResult }
> {
  let body: unknown;
  try {
    body = await request.json();
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

  const text = (name: string, field: unknown, pattern: RegExp): string | SandboxDispatchResult => {
    if (typeof field !== "string" || !pattern.test(field)) {
      return refuse(
        400,
        "invalid_request",
        `${name} must be a non-empty printable ASCII string (at most 512 characters${
          pattern === TITLE_FIELD_PATTERN ? "; spaces allowed" : "; no spaces"
        })`,
      );
    }
    return field;
  };
  const role = text("role", raw.role, PLAIN_FIELD_PATTERN);
  if (typeof role !== "string") return role;
  const writeRef = text("write_ref", raw.write_ref, PLAIN_FIELD_PATTERN);
  if (typeof writeRef !== "string") return writeRef;
  const baseRef = text("base_ref", raw.base_ref, PLAIN_FIELD_PATTERN);
  if (typeof baseRef !== "string") return baseRef;
  const title = text("title", raw.title, TITLE_FIELD_PATTERN);
  if (typeof title !== "string") return title;
  // The model the caller resolved (tick a08). Required, and booted as given:
  // a door that fell back to the deployment's own model here would hand the
  // caller a handle for a worker running something its records do not name.
  const model = text("model", raw.model, PLAIN_FIELD_PATTERN);
  if (typeof model !== "string") return model;
  // The harness the caller resolved (tick 9iz). Required, and bound as given,
  // for the model's reason verbatim.
  const harness = text("harness", raw.harness, PLAIN_FIELD_PATTERN);
  if (typeof harness !== "string") return harness;
  // The rendered role prompt the caller resolved (tick 9iz). Required: the
  // container's own entrypoint builds its worker prompt from the checkout's
  // tracker, so the profile's prompt reaches the worker through this field or
  // not at all — and a door that booted without it would be a door whose
  // caller's `prompt_digest` named a prompt that never ran.
  if (typeof raw.prompt !== "string" || !PROMPT_FIELD_PATTERN.test(raw.prompt)) {
    return refuse(
      400,
      "invalid_request",
      "prompt must be the rendered role prompt the dispatch resolved (printable text with line " +
        `breaks, at most ${PROMPT_FIELD_MAX} characters) — the worker's container runs on it, and a start with ` +
        "none would boot a worker on a prompt nobody chose",
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

  // The arbiter (D4). Not acquired — verified, exactly as the wave door
  // verifies it: the in-run orchestrator is the holder, and a holder does not
  // take a second lease. Anything that is not the holder is refused here
  // exactly as a competing dispatch is refused at submission.
  const lease = await roomFor(env, run.project).leaseStatus();
  if (lease === null) {
    return refuse(
      409,
      "lease_lost",
      `run ${run.run_id} no longer holds the dispatch lease for ${run.project} — it expired, ` +
        "which means this run is no longer the project's arbiter and must not boot containers",
    );
  }
  if (lease.run_id !== run.run_id) {
    return refuse(
      409,
      "lease_held_by",
      `the dispatch lease for ${run.project} is held by ${lease.run_id}, not ${run.run_id}; ` +
        "one arbiter per project (D4), and this run is not it",
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
    role,
    project: run.project,
    write_ref: writeRef,
    base_ref: baseRef,
    title,
    model,
    harness,
    prompt,
  };

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

  const binding = sandboxBinding(env);
  if (binding === null) {
    return refuse(
      503,
      "sandbox_dispatch_not_wired",
      "the SANDBOXES container binding (a deployment that cannot boot a container cannot " +
        "inspect one either)",
    );
  }

  // The same job id the handle carries (`attemptJobID`), so a client keying
  // on job_id reads one value whether it asked to start the attempt or to
  // read it back. Re-addressed BY NAME on every look — never a live object.
  const status = await namedAttemptStatus(
    binding,
    { run_id: run.run_id, tick_id: tickID, attempt },
    attemptJobID(run.run_id, tickID, attempt),
  );
  return { ok: true, status: 200, body: status as unknown as Record<string, unknown> };
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
  return refuse(
    404,
    "not_found",
    "the sandbox dispatch door serves POST /api/sandbox/attempts and " +
      "GET /api/sandbox/attempts/:tick_id/:attempt",
  );
}

/**
 * The handle type re-exported beside the contract it belongs to: a consumer
 * of this door reads its response shape from the module that documents the
 * door, without reaching into the executor's addressing for it.
 */
export type { SandboxJobHandle };
