import { describe, expect, it } from "vitest";
import layout from "../../contracts/tracker-layout.json";
import {
  EPIC_TYPE,
  parseTickRecord,
  TICK_RECORD_DIR,
  tickRecordPath,
} from "../src/tick-membership";

describe("the tracker layout both languages read", () => {
  it("reads records where Go's Store writes them", () => {
    expect(TICK_RECORD_DIR).toBe(layout.record_dir);
    expect(tickRecordPath(layout.record_path_example.tick)).toBe(layout.record_path_example.path);
    expect(EPIC_TYPE).toBe(layout.epic_type);
  });

  it("reads the fields Go spells, and treats an omitted parent as none", () => {
    const record = {
      [layout.fields.id]: "abc",
      [layout.fields.type]: "task",
      [layout.fields.parent]: "1vn",
      [layout.fields.external_ref]: "telegram:8412",
    };
    expect(parseTickRecord(JSON.stringify(record))).toEqual({
      id: "abc",
      type: "task",
      parent: "1vn",
      external_ref: "telegram:8412",
    });

    // Go omits `parent` on a parentless tick (`json:"parent,omitempty"`), so
    // the reader must not distinguish absent from empty. The external ref is
    // omitted the same way on a tick no signal produced, and read the same way
    // — the funnel's reconciler compares it to decide whether a record at a
    // candidate path is the one ITS interrupted commit wrote.
    expect(layout.parent_omitted_when_empty).toBe(true);
    expect(layout.written_by_the_control_plane.external_ref_omitted_when_empty).toBe(true);
    const rootless = { [layout.fields.id]: "1vn", [layout.fields.type]: layout.epic_type };
    expect(parseTickRecord(JSON.stringify(rootless))).toEqual({
      id: "1vn",
      type: layout.epic_type,
      parent: "",
      external_ref: "",
    });
  });

  it("refuses to read anything that is not a tick record", () => {
    expect(parseTickRecord("not json")).toBeNull();
    expect(parseTickRecord("[]")).toBeNull();
    expect(parseTickRecord("null")).toBeNull();
    expect(parseTickRecord(JSON.stringify({ title: "no id" }))).toBeNull();
  });
});
