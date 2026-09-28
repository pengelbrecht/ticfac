import { describe, expect, it } from "vitest";

import contract from "../../contracts/status-model.json";
import { STATUS_SCHEMA_VERSION } from "../src/status";
import { type Defs, type Json, parseDefs, parseSchema, validate } from "./json-schema";

/**
 * contracts/status-model.json — the TypeScript half of `ticfac.status.v1`.
 *
 * 6dh cut this fixture package-local with one reader and a condition attached:
 * the day a second reader exists it moves into the bundle. This factory's
 * status page (i1r, src/status.ts) became that second reader, and the fixture
 * moved when ticfac took the bundle over (tick 4i8). This test is what makes
 * the move mean something: the phone page's reader now runs the SAME goldens
 * and the SAME refusals the Go readers run (internal/contracts/parity/
 * status_model_test.go), through a validator whose error strings are the Go
 * ones, and it pins that every field the factory reads is a field the schema
 * names — a schema that dropped one would silently degrade the page while
 * both suites stayed green, which is the exact drift the bundle exists for.
 */

type RecordEntry = { schema_id: string; schema: unknown };
const records = contract.records as unknown as Record<string, RecordEntry>;
const defs: Defs = parseDefs((contract as { $defs: unknown }).$defs);
const statusModel = parseSchema(records.status_model.schema, "records.status_model");

describe("the factory reads the status model the bundle pins", () => {
  it("is the schema the factory's STATUS_SCHEMA_VERSION answers to", () => {
    expect(records.status_model.schema_id).toBe("ticfac.status.v1");
    expect(contract.schema_version).toBe(STATUS_SCHEMA_VERSION);
  });

  it("admits every golden document, with no violation at all", () => {
    const goldens = contract.golden as Record<string, Json>;
    expect(Object.keys(goldens).length).toBeGreaterThan(0);
    for (const [name, document] of Object.entries(goldens)) {
      expect(
        validate(statusModel, defs, document),
        `golden ${name} is refused by its own schema`,
      ).toEqual([]);
    }
  });

  it("refuses every negative with the pinned refusal", () => {
    const negatives = contract.invalid as unknown as {
      why: string;
      expect_error_contains: string;
      document: Json;
    }[];
    expect(negatives.length).toBeGreaterThan(0);
    for (const negative of negatives) {
      const problems = validate(statusModel, defs, negative.document);
      expect(problems, `negative ${JSON.stringify(negative.why)} was ADMITTED`).not.toEqual([]);
      expect(problems.join("; ")).toContain(negative.expect_error_contains);
    }
  });

  it("names every top-level field the factory's reader types", () => {
    const properties = (statusModel.properties ?? {}) as Record<string, unknown>;
    // The subset of the model src/status.ts reads: a field the page needs
    // that the schema stops naming is a field the page loses silently.
    for (const field of [
      "schema_version",
      "run_id",
      "epic_id",
      "host",
      "generated_at",
      "liveness",
      "lifecycle",
      "attention",
      "waits_on",
      "waves",
    ]) {
      expect(Object.keys(properties), `the schema no longer names ${field}`).toContain(field);
    }
  });

  it("pins the attention row the alert evaluator reads", () => {
    // attention rows are $defs/wait; the page reads kind, what, since,
    // needs_person and unblock_command out of them.
    const wait = defs.wait;
    expect(wait).toBeDefined();
    for (const field of ["kind", "what", "since", "needs_person", "unblock_command"]) {
      expect(
        Object.keys(wait.properties ?? {}),
        `an attention row no longer carries ${field}`,
      ).toContain(field);
    }
  });

  it("pins liveness the stale-data rule reads", () => {
    const liveness = defs.liveness;
    expect(Object.keys(liveness.properties ?? {})).toContain("alive");
    expect(Object.keys(liveness.properties ?? {})).toContain("state");
    expect(Object.keys(liveness.properties ?? {})).toContain("reason");
  });
});
