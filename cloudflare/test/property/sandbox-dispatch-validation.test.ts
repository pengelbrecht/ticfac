import * as hegel from "@hegeldev/hegel";
import * as gs from "@hegeldev/hegel/generators";
import { expect, it } from "vitest";

import {
  BASE_SHA_PATTERN,
  isProse,
  jobIDOf,
  jsonBody,
  PLAIN_FIELD_PATTERN,
  PROMPT_FIELD_MAX_BYTES,
  PROMPT_FIELD_PATTERN,
  parseReadRequest,
  parseStartRequest,
  type StartAttemptRequest,
  TICK_ID_PATTERN,
  TITLE_FIELD_MAX_BYTES,
  TITLE_FIELD_PATTERN,
} from "../../src/sandbox-dispatch-validation";

/**
 * The sandbox dispatch door's request validation (tick p0n): properties over
 * `src/sandbox-dispatch-validation.ts` — the grammar the Go orchestrator's
 * client speaks against (`internal/exec/cloudflaresandbox/record.go`), so a
 * rule here that disagrees with what Go sends refuses every dispatch, and a
 * rule that forgives what it should not boots a container on text nobody
 * chose.
 *
 * Two directions, per the door's own field kinds:
 *
 * - **Accept**: a request built from the documented domains parses, and the
 *   parsed values are EXACTLY what was sent — no coercion, no trimming, no
 *   re-spelling. The door boots what it was told.
 * - **Reject**: a request mutated in one field is refused with the door's
 *   one refusal class for request shape (400 invalid_request), and the
 *   refusal names the field that failed — so a caller debugging from the
 *   orchestrator's log reads which of its fields was wrong, never a generic
 *   shrug. The first failing field wins, in the parser's documented order.
 *
 * The body reader is covered directly too: bodies that are not UTF-8 at all,
 * bodies that are not JSON, and bodies that are JSON but not an object are
 * each refused before any field is read.
 */

const RUN = { run_id: "run_a1b2c3d4", epic: "e5f6" } as const;

// --------------------------------------------------------- the generators ---

/** Printable ASCII, no whitespace — the identifier alphabet itself. */
const IDENTIFIER_ALPHABET =
  "!\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~";

const identifierGen = gs.text({
  alphabet: IDENTIFIER_ALPHABET,
  minSize: 1,
  maxSize: 512,
});

const tickIDGen = gs.oneOf(
  // The shape the tracker mints.
  gs.text({ alphabet: "abcdefghijklmnopqrstuvwxyz0123456789", minSize: 3, maxSize: 4 }),
  // The door accepts the wider container-name grammar — whose FIRST
  // character is alphanumeric (the pattern says so), so the identifier is
  // drawn first and the rest after it.
  gs.composite((tc) => {
    const head = tc.draw(gs.text({ alphabet: "ABCXYZabcxyz0123456789", minSize: 1, maxSize: 1 }));
    const rest = tc.draw(
      gs.text({ alphabet: "ABCXYZabcxyz0123456789._-", minSize: 0, maxSize: 63 }),
    );
    return `${head}${rest}`;
  }),
);

const shaGen = gs.composite((tc) =>
  Array.from(
    { length: 40 },
    () => "0123456789abcdef"[tc.draw(gs.integers({ minValue: 0, maxValue: 15 }))],
  ).join(""),
);

/** Prose: Unicode minus the categories the door refuses (Cc, Cs). */
const proseGen = gs.text({ minSize: 1, maxSize: 80, excludeCategories: ["Cc", "Cs"] });

/** A prompt: prose plus the line breaks markdown needs. */
const promptGen = gs.composite((tc) => {
  const base = tc.draw(proseGen);
  const breaks = tc.draw(gs.sampledFrom(["", "\n", "\r\n", "\t", " \n "]));
  return `${base}${breaks}${base}`;
});

const positiveIntGen = gs.integers({ minValue: 1, maxValue: 2 ** 31 });

/** A well-formed start request body, straight from the documented domains. */
const validBodyGen = gs.composite<Record<string, unknown>>((tc) => ({
  epic: RUN.epic,
  tick_id: tc.draw(tickIDGen),
  attempt: tc.draw(positiveIntGen),
  role: tc.draw(identifierGen),
  write_ref: tc.draw(identifierGen),
  base_ref: tc.draw(identifierGen),
  title: tc.draw(proseGen),
  model: tc.draw(identifierGen),
  harness: tc.draw(identifierGen),
  prompt: tc.draw(promptGen),
  base_sha: tc.draw(shaGen),
}));

/** Every field of the request, in the parser's own order. */
const FIELD_ORDER = [
  "epic",
  "tick_id",
  "attempt",
  "job_id",
  "wall_seconds",
  "stuck_seconds",
  "role",
  "write_ref",
  "base_ref",
  "title",
  "model",
  "harness",
  "prompt",
  "base_sha",
  "work_base_sha",
] as const;

/** One hostile value per field, drawn from the shape class each refuses. */
const HOSTILE_FIELD_VALUES: Record<(typeof FIELD_ORDER)[number], gs.Generator<unknown>> = {
  epic: gs.sampledFrom([null, 42, "another-epic", ""]),
  tick_id: gs.sampledFrom([
    "",
    "with space",
    "a".repeat(65),
    "tick\nwith-newline",
    "üñí",
    42,
    null,
    ["ab", "cd"],
  ]),
  attempt: gs.sampledFrom([0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, "1", null, []]),
  job_id: gs.sampledFrom(["", "run-other/…", `run-${RUN.run_id}/ with space`, "not-a-job-id", 42]),
  wall_seconds: gs.sampledFrom([0, -5, 3.5, "30", null]),
  stuck_seconds: gs.sampledFrom([-1, 0.5, "0", null]),
  role: gs.sampledFrom(["", "has space", "üñí", "a".repeat(513), 42, null]),
  write_ref: gs.sampledFrom(["", "two words", "ü", "a".repeat(513), null]),
  base_ref: gs.sampledFrom(["", "b b", "ü", null]),
  title: gs.sampledFrom(["", "line one\nline two", "tab\there", "null-byte\u0000here", 42, null]),
  model: gs.sampledFrom(["", "m m", "modèl", null]),
  harness: gs.sampledFrom(["", "h h", null]),
  prompt: gs.sampledFrom([
    "",
    "bell\u0007here",
    "escape\u001bhere",
    "surrogate\ud800 here",
    "nul\u0000here",
    42,
    null,
  ]),
  base_sha: gs.sampledFrom(["", "ABC", "z".repeat(40), "0".repeat(39), "0".repeat(41), 42, null]),
  work_base_sha: gs.sampledFrom(["", "short", "g".repeat(40), null]),
};

// ------------------------------------------------------------ the properties ---

it("a request built from the documented domains parses, byte for byte", () => {
  hegel.test(
    (tc) => {
      const body = tc.draw(validBodyGen);
      const parsed = parseStartRequest(body, RUN);
      if (!parsed.ok) {
        throw new Error(`a valid request was refused: ${JSON.stringify(parsed.refusal)}`);
      }
      // The door boots what it was told: every field round-trips exactly,
      // with no coercion or trimming anywhere.
      const request: StartAttemptRequest = parsed.request;
      expect(request.tickID).toBe(body.tick_id);
      expect(request.attempt).toBe(body.attempt);
      expect(request.role).toBe(body.role);
      expect(request.writeRef).toBe(body.write_ref);
      expect(request.baseRef).toBe(body.base_ref);
      expect(request.title).toBe(body.title);
      expect(request.model).toBe(body.model);
      expect(request.harness).toBe(body.harness);
      expect(request.prompt).toBe(body.prompt);
      expect(request.baseSHA).toBe(body.base_sha);
      // Optional fields are undefined, never invented.
      expect(request.jobID).toBeUndefined();
      expect(request.wallSeconds).toBeUndefined();
      expect(request.stuckSeconds).toBeUndefined();
      expect(request.workBaseSHA).toBeUndefined();
    },
    { testCases: 1000 },
  );
});

it("optional fields, when sent well-formed, are carried through exactly as sent", () => {
  hegel.test(
    (tc) => {
      const body = tc.draw(validBodyGen);
      body.job_id = `run-${RUN.run_id}/tick-ab/attempt-1-r${tc.draw(gs.integers({ minValue: 1, maxValue: 9 }))}`;
      body.wall_seconds = tc.draw(gs.integers({ minValue: 1, maxValue: 86_400 }));
      body.stuck_seconds = tc.draw(gs.integers({ minValue: 0, maxValue: 3_600 }));
      body.work_base_sha = tc.draw(shaGen);
      const parsed = parseStartRequest(body, RUN);
      if (!parsed.ok) {
        throw new Error(`a valid request was refused: ${JSON.stringify(parsed.refusal)}`);
      }
      expect(parsed.request.jobID).toBe(body.job_id);
      expect(parsed.request.wallSeconds).toBe(body.wall_seconds);
      expect(parsed.request.stuckSeconds).toBe(body.stuck_seconds);
      expect(parsed.request.workBaseSHA).toBe(body.work_base_sha);
      // Zero stuck_seconds is the documented "watch off" spelling, not a
      // malformed bound — it must survive as zero, not be dropped.
      body.stuck_seconds = 0;
      const again = parseStartRequest(body, RUN);
      if (!again.ok) throw new Error(`stuck_seconds 0 was refused: ${again.refusal.detail}`);
      expect(again.request.stuckSeconds).toBe(0);
    },
    { testCases: 1000 },
  );
});

it("a hostile mutation of one field is refused, by that field, with the one request-shape class", () => {
  hegel.test(
    (tc) => {
      const body = tc.draw(validBodyGen);
      const field = tc.draw(gs.sampledFrom(FIELD_ORDER));
      const hostile = tc.draw(HOSTILE_FIELD_VALUES[field]);
      const mutated: Record<string, unknown> = { ...body, [field]: hostile };
      const parsed = parseStartRequest(mutated, RUN);
      if (parsed.ok) {
        throw new Error(
          `${field}=${JSON.stringify(hostile)} was accepted: the door booted a request it should refuse`,
        );
      }
      expect(parsed.refusal.status).toBe(400);
      expect(parsed.refusal.error).toBe("invalid_request");
      // The refusal names the field that failed — a caller debugging from a
      // container's log reads WHICH field, never a shrug.
      if (field === "job_id") expect(parsed.refusal.detail).toContain("job_id");
      else expect(parsed.refusal.detail.toLowerCase()).toContain(field);
    },
    { testCases: 3000 },
  );
});

it("the first failing field wins: the parser checks in one order, and a caller reads the first fault", () => {
  hegel.test(
    (tc) => {
      const body = tc.draw(validBodyGen);
      const first = tc.draw(
        gs.sampledFrom([
          "epic",
          "tick_id",
          "attempt",
          "role",
          "title",
          "prompt",
          "base_sha",
        ] as const),
      );
      const second = tc.draw(gs.sampledFrom(FIELD_ORDER.filter((f) => f !== first)));
      const hostileFor: Record<string, unknown> = {
        epic: "another-epic",
        tick_id: "",
        attempt: 0,
        job_id: "",
        wall_seconds: 0,
        stuck_seconds: -1,
        role: "",
        write_ref: "",
        base_ref: "",
        title: "",
        model: "",
        harness: "",
        prompt: "",
        base_sha: "",
        work_base_sha: "",
      };
      const mutated: Record<string, unknown> = {
        ...body,
        [first]: hostileFor[first],
        [second]: hostileFor[second],
      };
      const parsed = parseStartRequest(mutated, RUN);
      if (parsed.ok) {
        throw new Error(`two hostile mutations were accepted (${first}, ${second})`);
      }
      // Whichever of the two the parser checks first is the one it names.
      const winner = FIELD_ORDER.indexOf(first) < FIELD_ORDER.indexOf(second) ? first : second;
      expect(parsed.refusal.status).toBe(400);
      expect(parsed.refusal.error).toBe("invalid_request");
      if (winner === "job_id") expect(parsed.refusal.detail).toContain("job_id");
      else expect(parsed.refusal.detail.toLowerCase()).toContain(winner);
    },
    { testCases: 2000 },
  );
});

it("a body that is not a UTF-8 JSON object is refused before any field is read", async () => {
  await hegel.testAsync(
    async (tc) => {
      const kind = tc.draw(
        gs.sampledFrom(["not-utf8", "not-json", "array", "string", "number", "null"] as const),
      );
      let request: Request;
      switch (kind) {
        case "not-utf8": {
          // Bytes that no strict UTF-8 decoder accepts — the kind of body a
          // mis-declared client or a corrupted pipe delivers.
          const bytes = new Uint8Array([0x7b, 0x22, 0x61, 0xff, 0x7d]);
          request = new Request("https://factory.example.com/api/sandbox/attempts", {
            method: "POST",
            body: bytes,
          });
          break;
        }
        case "not-json":
          request = new Request("https://factory.example.com/api/sandbox/attempts", {
            method: "POST",
            body: "this is not json",
          });
          break;
        case "array":
          request = new Request("https://factory.example.com/api/sandbox/attempts", {
            method: "POST",
            body: JSON.stringify([1, 2, 3]),
          });
          break;
        case "string":
          request = new Request("https://factory.example.com/api/sandbox/attempts", {
            method: "POST",
            body: JSON.stringify("a string"),
          });
          break;
        case "number":
          request = new Request("https://factory.example.com/api/sandbox/attempts", {
            method: "POST",
            body: JSON.stringify(42),
          });
          break;
        default:
          request = new Request("https://factory.example.com/api/sandbox/attempts", {
            method: "POST",
            body: "null",
          });
          break;
      }
      const parsed = await jsonBody(request);
      if (parsed.ok) {
        throw new Error(`${kind} was read as a JSON object body`);
      }
      expect(parsed.refusal.status).toBe(400);
      expect(parsed.refusal.error).toBe("invalid_request");
    },
    { testCases: 1000 },
  );
});

it("a well-formed body the reader accepts is the exact object that was sent", async () => {
  await hegel.testAsync(
    async (tc) => {
      const body = tc.draw(validBodyGen);
      const request = new Request("https://factory.example.com/api/sandbox/attempts", {
        method: "POST",
        body: JSON.stringify(body),
      });
      const parsed = await jsonBody(request);
      if (!parsed.ok) throw new Error(`a JSON object body was refused: ${parsed.refusal.detail}`);
      expect(parsed.raw).toEqual(body);
    },
    { testCases: 500 },
  );
});

it("job_id belongs to the credential's run: another run's id is refused, never addressed", () => {
  hegel.test(
    (tc) => {
      const foreign = tc.draw(
        gs.oneOf(
          gs.just("run-other/"),
          gs.just(""),
          gs.just(`run-${RUN.run_id}x/`),
          gs.just("run-/"),
        ),
      );
      const refused = jobIDOf(RUN.run_id, `${foreign}tick-ab/attempt-1`);
      if (typeof refused !== "object" || refused === null || !("ok" in refused) || refused.ok) {
        throw new Error(`a foreign job id was accepted: ${JSON.stringify(foreign)}`);
      }
      expect(refused.status).toBe(400);
      // And the run's own ids — with the full name grammar — are accepted.
      const own = tc.draw(identifierGen);
      const accepted = jobIDOf(RUN.run_id, `run-${RUN.run_id}/${own}`);
      if (typeof accepted !== "string") {
        throw new Error(`the run's own job id was refused: ${JSON.stringify(accepted)}`);
      }
      expect(accepted).toBe(`run-${RUN.run_id}/${own}`);
      // Absent and null are both "no job id stated".
      expect(jobIDOf(RUN.run_id, undefined)).toBeUndefined();
      expect(jobIDOf(RUN.run_id, null)).toBeUndefined();
    },
    { testCases: 1000 },
  );
});

it("the read path validates the same name grammar: tick, attempt and job_id", () => {
  hegel.test(
    (tc) => {
      const tick = tc.draw(tickIDGen);
      const attempt = tc.draw(positiveIntGen);
      const job = tc.draw(
        gs.oneOf(gs.just(undefined), gs.just(`run-${RUN.run_id}/tick-ab/repair-1`)),
      );
      const parsed = parseReadRequest(RUN.run_id, tick, String(attempt), job);
      if (!parsed.ok) throw new Error(`a well-formed read was refused: ${parsed.refusal.detail}`);
      expect(parsed.identity.tickID).toBe(tick);
      expect(parsed.identity.attempt).toBe(attempt);
      expect(parsed.identity.jobID).toBe(job);

      // The path grammar refuses what the body grammar refuses: the same
      // tick-id pattern, a positive-integer attempt, and the same job rule.
      const bad = tc.draw(
        gs.oneOf(
          gs.just(parseReadRequest(RUN.run_id, "with space", "1", undefined)),
          gs.just(parseReadRequest(RUN.run_id, "ab", "0", undefined)),
          gs.just(parseReadRequest(RUN.run_id, "ab", "1.5", undefined)),
          gs.just(parseReadRequest(RUN.run_id, "ab", "-1", undefined)),
          gs.just(parseReadRequest(RUN.run_id, "ab", "1", "run-other/x")),
        ),
      );
      if (bad.ok)
        throw new Error(`a hostile read path was accepted: ${JSON.stringify(bad.identity)}`);
      expect(bad.refusal.status).toBe(400);
      expect(bad.refusal.error).toBe("invalid_request");
    },
    { testCases: 1000 },
  );
});

it("the prose bounds count UTF-8 bytes, and the patterns refuse what no environment can carry", () => {
  hegel.test(
    (tc) => {
      const prose = tc.draw(proseGen);
      // A prose string from the clean domain is accepted by both field kinds
      // (the title needs at least one character; the prompt accepts empty).
      expect(isProse(prose, TITLE_FIELD_PATTERN, TITLE_FIELD_MAX_BYTES)).toBe(true);
      expect(isProse(prose, PROMPT_FIELD_PATTERN, PROMPT_FIELD_MAX_BYTES)).toBe(true);

      // The byte bound: astral characters are 4 UTF-8 bytes for 2 UTF-16
      // code units, so a string that fits by code units can still refuse —
      // and the boundary sits exactly at TITLE_FIELD_MAX_BYTES / 4.
      const count = tc.draw(gs.integers({ minValue: 1, maxValue: 260 }));
      const astral = "𝔸".repeat(count);
      const fits = count * 4 <= TITLE_FIELD_MAX_BYTES;
      expect(isProse(astral, TITLE_FIELD_PATTERN, TITLE_FIELD_MAX_BYTES)).toBe(fits);

      // A control character is refused in prose — the pattern, not the size.
      expect(isProse("before\u0000after", TITLE_FIELD_PATTERN, TITLE_FIELD_MAX_BYTES)).toBe(false);
      expect(isProse("before\u0000after", PROMPT_FIELD_PATTERN, PROMPT_FIELD_MAX_BYTES)).toBe(
        false,
      );
      // ...but the prompt keeps the line breaks markdown needs.
      expect(isProse("a\nb\rc\td", PROMPT_FIELD_PATTERN, PROMPT_FIELD_MAX_BYTES)).toBe(true);
      expect(isProse("a\nb", TITLE_FIELD_PATTERN, TITLE_FIELD_MAX_BYTES)).toBe(false);

      // The identifier pattern: printable ASCII, no whitespace, bounded.
      const id = tc.draw(identifierGen);
      expect(PLAIN_FIELD_PATTERN.test(id)).toBe(true);
      expect(PLAIN_FIELD_PATTERN.test(`${id} `)).toBe(false);
      expect(PLAIN_FIELD_PATTERN.test("with space")).toBe(false);

      // The tick id grammar is the container-name conservatism.
      expect(TICK_ID_PATTERN.test("ab")).toBe(true);
      expect(TICK_ID_PATTERN.test("a b")).toBe(false);
      expect(TICK_ID_PATTERN.test("a".repeat(65))).toBe(false);

      // The SHA grammar is full 40-hex, nothing else — and uppercase hex is
      // not hex, whenever the drawn sha carries a letter to fold.
      const sha = tc.draw(shaGen);
      expect(BASE_SHA_PATTERN.test(sha)).toBe(true);
      expect(BASE_SHA_PATTERN.test(sha.slice(0, 39))).toBe(false);
      if (/[a-f]/.test(sha)) {
        expect(BASE_SHA_PATTERN.test(sha.toUpperCase())).toBe(false);
      }
    },
    { testCases: 1000 },
  );
});
