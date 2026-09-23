/**
 * The completion door (tick 7eq): how a finished orchestrator tells its Run
 * Workflow it is done, without the Workflow polling for it.
 *
 * ## Why this exists
 *
 * The Run Workflow learns an orchestrator finished by LOOKING — draining its
 * output, renewing the lease, reading the budgets, checking the process — and
 * the looks happen on a cadence. That cadence is load-bearing (it is where
 * budgets are enforced, D14) and it is also pure latency: a container that
 * exits the moment a look has passed sits unobserved until the next one,
 * which at the default backoff is minutes.
 *
 * Containers can make outbound requests, so the missing half is a push:
 *
 *   orchestrator finishes -> POSTs this door -> the Worker calls
 *   instance.sendEvent() -> the Workflow's waitForEvent returns.
 *
 * A container invoking a Workflow binding DIRECTLY is unverified platform
 * ground, so the route through the Worker is the design, not a workaround:
 * the Worker is the one party that provably holds the binding.
 *
 * ## What the event is — and, decisively, what it is NOT
 *
 * Events are BUFFERED by the platform ("you can send an event to a Workflow
 * instance before it reaches the corresponding waitForEvent call"), so a
 * container that finishes before the Workflow resumes to its wait does not
 * lose its completion. The event is therefore an OPTIMISATION that collapses
 * the wait, and nothing more:
 *
 *   - The Workflow still LOOKS after the event wakes it. The signal never
 *     decides anything: completion is proved against the durable layer
 *     (the process state, then `src/progress.ts`'s ref comparison), never
 *     against a claim.
 *   - Buffering closes the timing race but NOT the case where the container
 *     dies after finishing and before its callback lands. So the pushed
 *     branch is the source of truth and this door is best effort: delivery
 *     failures are reported to the caller, logged, and change nothing.
 *
 * A supervisor that trusted the event alone would report a finished run as
 * failed exactly when its callback was lost; the branch cannot lie about what
 * landed. That is the same rule the local reconciler already lives by — never
 * take a claim's word for its own progress — inherited rather than reinvented.
 *
 * ## The caller
 *
 * `ticfac run-epic` inside the orchestrator container, through
 * `TICKS_FACTORY_URL` and the run's own gateway token (the same credential
 * every other in-run door takes — D17: revoking it stops the run's dispatch,
 * its spending AND its signalling). No run id in the path or the body: the
 * credential decides which run is speaking.
 *
 * The payload is deliberately tiny: the branch and the head sha it landed at,
 * never the work product. The platform caps an event payload at 1 MiB
 * ({@link DONE_EVENT_PAYLOAD_CAP_BYTES}) and a run's verdict never rides on
 * it anyway — sending anything larger is spending a platform limit on
 * evidence nobody reads.
 */

import { authorizeGatewayRequest, type GatewayDenial } from "./gateway";
import type { Env } from "./index";
import { BASE_SHA_PATTERN, runWorkflowBinding } from "./runs";

/**
 * The event type the Run Workflow waits on.
 *
 * Cloudflare Workflows requires an event type matching
 * `^[a-zA-Z0-9_][a-zA-Z0-9-_]*$` — no dots — and an out-of-vocabulary type
 * makes `waitForEvent` never fire, which silently degrades the signal back
 * into polling. The pattern is exported so the suite can pin the constant to
 * it ({@link readDoneSignal} is the workflow-side shape check of the same
 * contract).
 */
export const DONE_EVENT_TYPE = "done";

/** The platform's event-type vocabulary, as documented. */
export const EVENT_TYPE_PATTERN = /^[a-zA-Z0-9_][a-zA-Z0-9-_]*$/;

/** The platform's per-event payload cap. The route refuses more, early. */
export const DONE_EVENT_PAYLOAD_CAP_BYTES = 1 << 20;

/**
 * A branch name the dispatch log can carry without embarrassment.
 *
 * This is NOT a security boundary — the credential decides who is speaking,
 * and nothing here is ever trusted — it is input hygiene for a payload that
 * ends up in the Workflow's step outputs: no whitespace, no control
 * characters, none of the bytes git forbids in a ref name, and a length a
 * human can read.
 */
export const MAX_BRANCH_CHARS = 255;

/** What a finished orchestrator reports: where it landed, not what it did. */
export type DoneSignal = {
  /** The run's integration branch, as the orchestrator pushed it. */
  branch: string;
  /** The branch head the durable layer should be holding, when it is known. */
  head?: string;
};

/**
 * The workflow-side read of an event payload: the same shape the door
 * validated, checked again because a `waitForEvent` payload is untyped at the
 * seam and a Workflow step must never throw on what it was handed.
 *
 * Returns null on anything unexpected — an unreadable signal is a lost
 * optimisation, never a lost run.
 */
export function readDoneSignal(payload: unknown): DoneSignal | null {
  if (payload === null || typeof payload !== "object") return null;
  const raw = payload as Record<string, unknown>;
  if (typeof raw.branch !== "string") return null;
  const signal: DoneSignal = { branch: raw.branch };
  if (typeof raw.head === "string" && raw.head !== "") signal.head = raw.head;
  return signal;
}

export type DoneResult =
  | {
      ok: true;
      /** Whether the Workflow instance took the event at all. */
      delivered: boolean;
      /** What happened, for the container's log. */
      detail: string;
    }
  | { ok: false; status: number; error: string; detail: string };

function refuse(status: number, error: string, detail: string): DoneResult {
  return { ok: false, status, error, detail };
}

function fromDenial(denial: GatewayDenial): DoneResult {
  return refuse(denial.status, denial.error, denial.detail);
}

// The bytes git forbids in a ref name, minus the two this needs to allow
// ("/" for epic/ branches, "-" and "." inside them). Enough to keep the
// payload printable without reimplementing check-ref-format.
const BAD_BRANCH_BYTES = /[\s~^:?*[\]\\]/;

/**
 * Turns an authenticated POST into the event the run's Workflow is waiting
 * for — best effort, exactly as the design demands.
 *
 * Refusals (auth, shape, size) are 4xx with a reason, because they are facts
 * about the request. Delivery failures are NOT refusals: the branch is the
 * source of truth, a container can do nothing with a "try again", and a
 * late or undeliverable callback is precisely the case the cadence looks
 * already cover — so the route answers 202 with `delivered: false` and the
 * detail, and the run concludes correctly either way.
 */
export async function signalRunDone(env: Env, request: Request): Promise<DoneResult> {
  // The credential is the run's own gateway token, verified exactly as every
  // other in-run door verifies it: unknown, revoked, orphaned and finished are
  // four distinct verdicts. A callback landing after finalize authenticates
  // against a finished run and is refused here — correctly, because there is
  // no supervisor left to wake.
  const authorized = await authorizeGatewayRequest(env, request);
  if (!authorized.ok) return fromDenial(authorized.denial);
  const run = authorized.run;

  // The platform refuses event payloads over 1 MiB; refusing the oversize
  // body HERE is what turns that platform error into a 4xx the container can
  // read, rather than a sendEvent failure nobody asked for. The payload this
  // door exists for is two short strings, so nothing honest is refused.
  const declared = Number(request.headers.get("content-length") ?? "0");
  if (declared > DONE_EVENT_PAYLOAD_CAP_BYTES) {
    return refuse(
      413,
      "payload_too_large",
      `the completion signal is the branch and the head sha — ${declared} bytes is not a signal; ` +
        `the platform caps an event payload at ${DONE_EVENT_PAYLOAD_CAP_BYTES} bytes`,
    );
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return refuse(400, "invalid_request", "the request body must be JSON");
  }
  if (body === null || typeof body !== "object" || Array.isArray(body)) {
    return refuse(400, "invalid_request", "the request body must be a JSON object");
  }
  const raw = body as Record<string, unknown>;

  if (typeof raw.branch !== "string" || raw.branch === "" || raw.branch.length > MAX_BRANCH_CHARS) {
    return refuse(
      400,
      "invalid_request",
      `branch must be the integration branch this run pushed (at most ${MAX_BRANCH_CHARS} characters)`,
    );
  }
  if (BAD_BRANCH_BYTES.test(raw.branch) || raw.branch.includes("..")) {
    return refuse(
      400,
      "invalid_request",
      `branch ${JSON.stringify(raw.branch)} is not a branch name`,
    );
  }

  // Optional on purpose: an orchestrator that cannot resolve its head (a run
  // that failed before pushing anything) still gets its wake-up. The branch
  // is the fact; the head is a convenience.
  const head = raw.head;
  if (head !== undefined && (typeof head !== "string" || !BASE_SHA_PATTERN.test(head))) {
    return refuse(
      400,
      "invalid_request",
      "head must be the full 40-character commit the branch stands at",
    );
  }

  // The event type is checked at this door's own boundary because a type the
  // platform rejects makes sendEvent fail with a request-shape error nobody
  // connected to this signal — pinned by the suite rather than trusted.
  if (!EVENT_TYPE_PATTERN.test(DONE_EVENT_TYPE)) {
    return refuse(
      500,
      "event_type_invalid",
      "the completion event type does not match the platform's vocabulary",
    );
  }

  const binding = runWorkflowBinding(env);
  if (binding === null) {
    return refuse(
      503,
      "no_run_workflow",
      "this deployment has no Run Workflow binding, so there is no supervisor to wake; " +
        "the run concludes from its branch regardless",
    );
  }

  const payload: DoneSignal =
    head === undefined ? { branch: raw.branch } : { branch: raw.branch, head };
  try {
    const instance = await binding.get(run.run_id);
    if (typeof instance.sendEvent !== "function") {
      return {
        ok: true,
        delivered: false,
        detail: `the Workflow instance for ${run.run_id} cannot take events`,
      };
    }
    await instance.sendEvent({ type: DONE_EVENT_TYPE, payload });
    return {
      ok: true,
      delivered: true,
      detail:
        `run ${run.run_id}'s Workflow was signalled: ${payload.branch}` +
        (payload.head === undefined ? "" : `@${payload.head}`),
    };
  } catch (error) {
    // Expected whenever the instance is not waiting on an event and the
    // platform does not buffer for its state (already terminal, say). The
    // signal is the optimisation, not the truth: the branch decides.
    console.error(
      `factory run-done: could not deliver run ${run.run_id}'s completion signal ` +
        `(the pushed branch remains the source of truth): ${String(error)}`,
    );
    return {
      ok: true,
      delivered: false,
      detail: `the Workflow instance for ${run.run_id} did not take the event: ${String(error)}`,
    };
  }
}
