/**
 * The sandbox dispatch door's REQUEST VALIDATION — the field grammar the door
 * accepts, extracted as a pure module (tick p0n).
 *
 * Everything here once lived inline in `sandbox-dispatch.ts`, interleaved with
 * the routes it guards, so nothing could test the door's grammar without a
 * real Worker, a real credential and real D1 behind it. Now the property tests
 * (`test/property/sandbox-dispatch-validation.test.ts`) drive THIS grammar
 * over arbitrary request bodies — and the constraints that made that possible
 * are the module's design rules:
 *
 * 1. **No imports.** The plain-Node vitest leg that runs the Hegel tests
 *    loads this module, and anything that drags `cloudflare:workers` into its
 *    chain is not plain-Node importable. `Request` and `TextDecoder` come from
 *    the standard runtime, not from a binding.
 * 2. **Validation decides; the route acts.** Every function here answers with
 *    the parsed request or the typed refusal the door sends — never with a
 *    side effect. The routes in `sandbox-dispatch.ts` hold the auth, the
 *    lease, the capacity and the boot; a request that reaches them has already
 *    been judged here, in the order this module pins.
 *
 * The grammar's two field kinds and the reasons for them travel with the
 * checks: IDENTIFIER fields name things and ride environment variables, so
 * printable ASCII with no whitespace; PROSE fields are what people and
 * profiles write, so Unicode minus control characters — and the bounds count
 * UTF-8 bytes because that is what the environment variable is measured in,
 * the same bytes the Go client (`cloudflaresandbox/record.go`) counts. The
 * Go client sends these requests, so a rule here that disagrees with what Go
 * sends is a rule that refuses every dispatch: the property tests keep this
 * side honest, the Go side's own tests keep that one honest, and the
 * TS↔Go seam (`.tick/learnings.md`: a fake demands the real shape) is the
 * door's whole HTTP contract.
 */

/** What the door answers a request it refuses: the status, class and detail. */
export type DoorRefusal = { ok: false; status: number; error: string; detail: string };

/** The typed refusal, in the shape the door's routes answer in. */
export function refuse(status: number, error: string, detail: string): DoorRefusal {
  return { ok: false, status, error, detail };
}

/**
 * A tick id as this door accepts it: it names a container, so it gets a
 * container name's conservatism rather than a free-text field's tolerance.
 * Every id the tracker mints already fits; a caller that cannot state one
 * that fits has no container to name.
 */
export const TICK_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

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
export const PLAIN_FIELD_PATTERN = /^[\x21-\x7e]{1,512}$/;

/**
 * A rendered prompt (`prompt`, tick 9iz): prose plus tab / LF / CR, at most
 * {@link PROMPT_FIELD_MAX_BYTES} UTF-8 bytes (checked beside the pattern).
 */
export const PROMPT_FIELD_PATTERN = /^(?:[\t\n\r]|[^\p{Cc}\p{Cs}])+$/u;

/** The prompt bound, in UTF-8 bytes, spelled once for the check and the refusal. */
export const PROMPT_FIELD_MAX_BYTES = 65536;

/**
 * A free-text field (`title`): prose on one line — no control character at
 * all, not even a tab or a line break — at most
 * {@link TITLE_FIELD_MAX_BYTES} UTF-8 bytes (checked beside the pattern).
 */
export const TITLE_FIELD_PATTERN = /^[^\p{Cc}\p{Cs}]+$/u;

/** The title bound, in UTF-8 bytes. */
export const TITLE_FIELD_MAX_BYTES = 512;

const utf8 = new TextEncoder();

/** A prose field: a string, in its class, at most `maxBytes` of UTF-8. */
export function isProse(field: unknown, pattern: RegExp, maxBytes: number): field is string {
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

/**
 * Reads the request body as a JSON object, or says why it cannot be.
 *
 * Decoded strictly, not through `request.json()`: that decoder rewrites a
 * byte that is not UTF-8 into U+FFFD, and the door would then boot a worker
 * on text its caller never sent.
 */
export async function jsonBody(
  request: Request,
): Promise<{ ok: true; raw: Record<string, unknown> } | { ok: false; refusal: DoorRefusal }> {
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
export function jobIDOf(runID: string, field: unknown): string | undefined | DoorRefusal {
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

/**
 * The start request a dispatch sends, parsed: everything a fresh attempt's
 * boot needs, in the order the door checks it — epic first (the credential's
 * own fact, checked rather than taken from the body), then the identity the
 * container is named by, then the bounds, then the fields the container's
 * environment becomes.
 */
export type StartAttemptRequest = {
  tickID: string;
  attempt: number;
  jobID: string | undefined;
  wallSeconds: number | undefined;
  stuckSeconds: number | undefined;
  role: string;
  writeRef: string;
  baseRef: string;
  title: string;
  model: string;
  harness: string;
  prompt: string;
  baseSHA: string;
  workBaseSHA: string | undefined;
};

/** The `base_sha` grammar the door shares with the submission boundary. */
export const BASE_SHA_PATTERN = /^[0-9a-f]{40}$/;

/**
 * Validates a start request's body against the run whose credential asked.
 *
 * `run` is what the credential already proved (the authorized run's id and
 * epic), never anything the body says: a container that has drifted onto
 * another epic is refused instead of silently dispatching this run's tick
 * under another one's name — the wave door's check, applied to the same kind
 * of caller.
 */
export function parseStartRequest(
  raw: Record<string, unknown>,
  run: { run_id: string; epic: string },
): { ok: true; request: StartAttemptRequest } | { ok: false; refusal: DoorRefusal } {
  // The epic is stated and checked rather than taken from the run, so a
  // container that has somehow drifted onto another epic is refused instead
  // of silently dispatching this run's tick under another one's name — the
  // wave door's check, applied to the same kind of caller.
  if (typeof raw.epic !== "string" || raw.epic !== run.epic) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        `epic must be ${JSON.stringify(run.epic)}, the epic run ${run.run_id} is working on`,
      ),
    };
  }

  if (typeof raw.tick_id !== "string" || !TICK_ID_PATTERN.test(raw.tick_id)) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "tick_id must name the tick this attempt implements (alphanumerics, `.`, `_`, `-`; " +
          "at most 64 characters) — it is the name the attempt's container is addressed by",
      ),
    };
  }

  // 1-based, per tick: the number the attempt's branch and container name
  // carry, and the wave machinery's own vocabulary.
  const attempt = raw.attempt;
  if (typeof attempt !== "number" || !Number.isInteger(attempt) || attempt < 1) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "attempt must be the positive integer that identifies this try of the tick",
      ),
    };
  }

  // The job, when it is not the attempt itself (a repair, a resolve, a base
  // fold): the FULL job id is the identity the container is named by.
  const jobID = jobIDOf(run.run_id, raw.job_id);
  if (typeof jobID !== "string" && jobID !== undefined) return { ok: false, refusal: jobID };

  // The dispatch's wall (tick 86y), optional: the worker's harness is bounded
  // just under it, and an older client that sends none boots unbounded.
  const wallSeconds = raw.wall_seconds;
  if (
    wallSeconds !== undefined &&
    (typeof wallSeconds !== "number" || !Number.isInteger(wallSeconds) || wallSeconds < 1)
  ) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "wall_seconds, when present, must be the dispatch's wall clock as a positive whole number of seconds",
      ),
    };
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
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "stuck_seconds, when present, must be the stuck watch's window as a whole number of seconds (0 turns the watch off)",
      ),
    };
  }

  const text = (name: string, field: unknown): string | DoorRefusal => {
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
  if (typeof role !== "string") return { ok: false, refusal: role };
  const writeRef = text("write_ref", raw.write_ref);
  if (typeof writeRef !== "string") return { ok: false, refusal: writeRef };
  const baseRef = text("base_ref", raw.base_ref);
  if (typeof baseRef !== "string") return { ok: false, refusal: baseRef };
  if (!isProse(raw.title, TITLE_FIELD_PATTERN, TITLE_FIELD_MAX_BYTES)) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "title must be a non-empty line of UTF-8 text with no control characters " +
          `(at most ${TITLE_FIELD_MAX_BYTES} bytes)`,
      ),
    };
  }
  const title = raw.title;
  // The model the caller resolved (tick a08). Required, and booted as given:
  // a door that fell back to the deployment's own model here would hand the
  // caller a handle for a worker running something its records do not name.
  const model = text("model", raw.model);
  if (typeof model !== "string") return { ok: false, refusal: model };
  // The harness the caller resolved (tick 9iz). Required, and bound as given,
  // for the model's reason verbatim.
  const harness = text("harness", raw.harness);
  if (typeof harness !== "string") return { ok: false, refusal: harness };
  // The rendered role prompt the caller resolved (tick 9iz). Required: the
  // container's own entrypoint builds its worker prompt from the checkout's
  // tracker, so the profile's prompt reaches the worker through this field or
  // not at all — and a door that booted without it would be a door whose
  // caller's `prompt_digest` named a prompt that never ran.
  if (!isProse(raw.prompt, PROMPT_FIELD_PATTERN, PROMPT_FIELD_MAX_BYTES)) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "prompt must be the rendered role prompt the dispatch resolved (UTF-8 text with line " +
          `breaks and no other control characters, at most ${PROMPT_FIELD_MAX_BYTES} bytes) — the worker's ` +
          "container runs on it, and a start with none would boot a worker on a prompt nobody chose",
      ),
    };
  }
  const prompt = raw.prompt;

  // Not the run's submitted base: the caller names the commit this attempt's
  // worker must clone at, which for a later wave is the run branch head that
  // pass pushed. Full 40-hex, refused rather than parsed (the same rule the
  // submission boundary applies).
  if (typeof raw.base_sha !== "string" || !BASE_SHA_PATTERN.test(raw.base_sha)) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "base_sha must be the full 40-character commit the attempt's worker clones at — " +
          "the run branch head this pass pushed, not the run's original base",
      ),
    };
  }

  // A carried attempt's work base (epic hn6, run_3f034e68), optional: the
  // commit the carried work was cut from, which the worker's container
  // measures the carried work against. Full 40-hex, like base_sha.
  const workBaseSHA = raw.work_base_sha;
  if (
    workBaseSHA !== undefined &&
    (typeof workBaseSHA !== "string" || !BASE_SHA_PATTERN.test(workBaseSHA))
  ) {
    return {
      ok: false,
      refusal: refuse(
        400,
        "invalid_request",
        "work_base_sha, when present, must be the full 40-character commit a carried attempt's " +
          "work was cut from",
      ),
    };
  }

  return {
    ok: true,
    request: {
      tickID: raw.tick_id,
      attempt,
      jobID,
      wallSeconds,
      stuckSeconds,
      role,
      writeRef,
      baseRef,
      title,
      model,
      harness,
      prompt,
      baseSHA: raw.base_sha,
      workBaseSHA,
    },
  };
}

/**
 * The path and query a read-back validates: the tick id and attempt the URL
 * names, and the optional `job_id` query — the identity the read is scoped
 * by, never the attempt it repairs.
 */
export function parseReadRequest(
  runID: string,
  tickID: string,
  attemptText: string,
  jobIDQuery: string | undefined,
):
  | { ok: true; identity: { tickID: string; attempt: number; jobID: string | undefined } }
  | { ok: false; refusal: DoorRefusal } {
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
  const jobID = jobIDOf(runID, jobIDQuery);
  if (typeof jobID !== "string" && jobID !== undefined) return { ok: false, refusal: jobID };
  return { ok: true, identity: { tickID, attempt: Number(attemptText), jobID } };
}
