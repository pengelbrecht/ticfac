import { describe, expect, it } from "vitest";

import contract from "../../contracts/run-event-feed.json";

import { type Defs, parseDefs, parseSchema, type Schema, validate } from "./json-schema";

/**
 * The TypeScript reader for `contracts/run-event-feed.json`.
 *
 * The contract arrived in bundle 5.2.0 and nothing on this side read it, which
 * is how `cloud/factory` came to be pinned at 3.0.0 against a 5.2.0 bundle —
 * two majors behind, wrong about three record shapes, and silent, because
 * nothing runs this suite (tick odc). The pin's own rule is what catches it:
 * a contract listed in `files` that no test imports is refused, because
 * "a contract with a single reader detects no drift at all".
 *
 * So this file is the second reader. It reads the feed as the host that has no
 * filesystem and no git: it cannot open `.ticfac/logs/<run-id>/events.jsonl`
 * and follow the appends, so it asserts what it CAN — that the line schema is
 * what the Go writer will emit, character for character on the refusals, using
 * this repository's one strict-subset validator.
 *
 * What it deliberately does NOT assert: that lines are appended and never
 * rewritten, that a reader resumes where it left off, or that the file is
 * uncommitted. All three need a filesystem; `internal/runfeed` owns them.
 *
 * The one rule worth restating because a cloud consumer will be tempted to
 * forget it: a feed line means WORTH LOOKING NOW, never "the work is finished".
 * The durable truth of a tick is the evidence on the integration branch, and a
 * lost, late or untruthful line changes no verdict. A dashboard that treated a
 * `closed` line as the close would be reading exhaust as authority.
 */

const feedEvent = contract.records.feed_event as { schema_id: string; schema: unknown };

// The feed's line schema stands alone — it references no $defs — but validate
// takes the definitions map regardless, so it gets an empty one rather than a
// borrowed one from another contract.
const defs: Defs = parseDefs({});
const schema: Schema = parseSchema(feedEvent.schema, "records.feed_event");

describe("the run event feed, shared with the Go writer", () => {
  it("validates every golden line against the contract's own schema", () => {
    for (const [name, document] of Object.entries(contract.golden)) {
      const errors = validate(schema, defs, document as never);
      expect(errors, `golden ${name} must satisfy ${feedEvent.schema_id}`).toEqual([]);
    }
  });

  it("refuses every invalid line for the reason the contract pins", () => {
    for (const invalid of contract.invalid) {
      const errors = validate(schema, defs, invalid.document as never);
      // The pinned text, not merely "some error": a case that started failing
      // for an unrelated reason would otherwise stay green forever, which is
      // the failure `expect_error_contains` was added across the bundle to
      // stop (3.0.0).
      expect(errors.join("\n"), invalid.why).toContain(invalid.expect_error_contains);
    }
  });

  it("carries run identity on every line, with null meaning genuinely absent", () => {
    // required-and-nullable is the shape the whole bundle uses for provenance:
    // "there is no tick" and "the tick was not recorded" are different claims,
    // and only the first is a null a reader may trust.
    const schema = feedEvent.schema as { required?: string[] };
    for (const field of ["schema_version", "at", "run_id", "tick_id", "attempt", "stage"]) {
      expect(schema.required, `${field} is required on every feed line`).toContain(field);
    }
  });

  it("says where the feed lives and that it is not durable", () => {
    expect(contract.layout.path).toBe(".ticfac/logs/<run-id>/events.jsonl");
    expect(contract.layout.committed).toBe(false);
    expect(contract.layout.cardinality).toBe("exactly one per run");
  });
});
