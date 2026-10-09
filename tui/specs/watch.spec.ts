// watch.spec.ts — the properties of `ticfac watch`'s dashboard, asserted
// FROM OUTSIDE the process through a real terminal (tick z7w): Bombadil
// spawns the ticfac binary on a pty, generates resizes, and this
// specification reads the SCREEN the way a person reads it.
//
// The same specification runs against the seeded broken programs
// (tui/seeded/) in the suite's seeded half: a property that cannot fail
// pins nothing, so each one is shown to fail against a deliberately broken
// screen there.
//
// The properties are pointed at the dashboard the 2026-10 redesign drew
// (docs/design/watch-redesign-2026-10.md; re-pointed by tick vii, whose
// subject was that this suite still pinned the layout the redesign
// removed): the frame's marker is the key-hints footer the design draws on
// every frame, the ticks stand grouped by state, and the needs-you answer
// is spelled "Needs you: …".
//
// The world's facts come from world.json (the runner writes it beside this
// file before each run): the plan order the tracker answers, the run the
// watch holds, and the command that clears it.
import { always, eventually, type Formula } from "@antithesishq/bombadil";
import { extract, type State, actions, type ActionTemplate } from "@antithesishq/bombadil/terminal";
import world from "./world.json" with { type: "json" };

// The screen as rows, and as one text block. The frame's marker is the
// dashboard's own signature — the key-hints footer, the last thing the
// design's layout draws ("[enter] details  [e] all events  [q] quit"): a
// state that shows it is a state a person is reading the dashboard in; the
// properties are about exactly those states. The old marker, the tick
// table's "TICK" header, was a column of the pre-redesign layout.
const frameMarker = "[enter] details  [e] all events  [q] quit";
const screenRows = (s: State): string[] => {
  const out: string[] = [];
  for (let i = 0; i < s.grid.size.rows; i++) out.push(s.grid.rowText(i));
  return out;
};
const rows = extract(screenRows);
const screen = extract((s: State): string => screenRows(s).join("\n"));
const frameShown = extract((s: State): boolean =>
  screenRows(s).some((line) => line.includes(frameMarker)),
);
const columns = extract((s: State): number => s.grid.size.columns);

// The state groups the redesign draws, and the lines that open them: a
// group's header stands at the line's first column — "NOW", "DONE (2)",
// "UP NEXT (4)   t01 …" (the collapsed group's ticks seated after it),
// "HELD (1)" — while every row beneath it is indented. The header is where
// the grouping is asserted; the lines beneath are where the order is.
const groupNames = ["NOW", "DONE", "UP NEXT", "HELD"];
const isGroupHeader = (line: string): boolean => {
  const text = line.replace(/^\s+/, "");
  if (text === "") return false;
  for (const name of groupNames) {
    if (text === name) return true;
    if (text.startsWith(name + " (")) return true;
  }
  return false;
};

// The screen's state groups, each the lines from one group header to the
// next, with the header's own group name. Lines above the first header
// (the identity, the needs-you answer, the phase track) belong to no group,
// and the tail beneath the latest rule is dropped with the rule: its
// sentences name ticks in the feed's own "t1#1" spelling, never the plan's.
const groupSections = extract((s: State): { name: string; lines: string[] }[] => {
  const sections: { name: string; lines: string[] }[] = [];
  let current: { name: string; lines: string[] } | null = null;
  for (const line of screenRows(s)) {
    if (line.startsWith("─ latest")) break;
    if (isGroupHeader(line)) {
      const text = line.replace(/^\s+/, "");
      const name = groupNames.find((n) => text === n || text.startsWith(n + " (")) ?? "";
      current = { name, lines: [line] };
      sections.push(current);
      continue;
    }
    if (current !== null && line.trim() !== "") current.lines.push(line);
  }
  return sections;
});

// The plan ids a group's lines carry, in the order they FIRST appear
// reading down the screen — first occurrence only, so a mention inside a
// row's other cells (a duplicate's "duplicate of …") cannot re-order what
// the row's own id already placed.
const idsInScreenOrder = (lines: string[]): string[] => {
  const text = lines.join("\n");
  const first = new Map<string, number>();
  for (const id of world.planOrder) {
    const at = text.indexOf(id);
    if (at >= 0) first.set(id, at);
  }
  return [...first.entries()].sort((a, b) => a[1] - b[1]).map(([id]) => id);
};

// inPlanOrder says whether the ids, in the order they appear, follow the
// plan's: each one at or after the one before, with plan entries between
// allowed — a fold or a narrow pane may hide some, and hiding keeps order.
const inPlanOrder = (plan: string[], ids: string[]): boolean => {
  let at = 0;
  for (const id of ids) {
    while (at < plan.length && plan[at] !== id) at++;
    if (at >= plan.length) return false;
    at++;
  }
  return true;
};

// The watch never outlives its run here, so a plain boolean in the thunk is
// the whole formula; the implies shape keeps every property quiet until the
// first frame and loud for every frame after it.
const overTheFrame = (holds: () => boolean): Formula =>
  always(() => !frameShown.current || holds());

// P1 (outside): the dashboard groups the ticks by state — the redesign's
// own rule, replacing the plan-order table — and within each group the rows
// keep the plan's order. The group headers are part of the property: a
// dashboard that dropped them and printed one flat table fails here, as one
// that reordered the groups themselves does (the design draws them NOW,
// DONE, UP NEXT, HELD). A group whose rows reshuffle between frames is a
// table nobody can read.
export const groupedTicksKeepPlanOrder = overTheFrame(() => {
  const sections = groupSections.current;
  if (sections.length === 0) return false;
  if (!inPlanOrder(groupNames, sections.map((s) => s.name))) return false;
  for (const section of sections) {
    if (!inPlanOrder(world.planOrder, idsInScreenOrder(section.lines))) return false;
  }
  return true;
});

// P2 (outside): a run that ended holding something for a person never says
// "Needs you: nothing" — and the hold's clearing command is on screen,
// every word of it. A wrap is by design (u4l's P2, tick 9um: the pane that
// cannot seat the whole line wraps the command under the announcement), so
// the assertion is the command's WORDS, not its one-line spelling.
export const needsYouCarriesTheHold = overTheFrame(() => {
  const text = screen.current;
  if (text.includes("Needs you: nothing")) return false;
  for (const word of world.clearingCommand.split(" ")) {
    if (word !== "" && !text.includes(word)) return false;
  }
  return true;
});

// P3 (outside): no fabricated cost — the world's spend is unmeasured, so
// no "$0.00" may appear anywhere on the dashboard: a number that looks like
// a measurement where none was made is the dishonesty hn6's A4 and tick 1tm
// refuse, asserted here from outside the process.
export const neverAFabricatedZero = overTheFrame(() => !screen.current.includes("$0.00"));

// P4 (outside): the frame fits the pane it is given — no line the frame
// drew is wider than the terminal, at any generated size.
export const frameFitsThePane = overTheFrame(() => {
  const text = screen.current;
  for (const line of text.split("\n")) {
    if (line.length > columns.current) return false;
  }
  return true;
});

// P5 (#112, outside): the first frame appears within its bound — the
// dashboard's own signature, on the screen, within five seconds of the
// watch starting. The Go side pins the tight bound (status_firstframe_test.go);
// this is the outside half, over a real spawn.
export const firstFrameWithinItsBound = eventually(() => frameShown.current).within(5, "seconds");

// The actions: generated resizes across the pane shapes a herdr pane and a
// laptop terminal take. The dashboard answers each one with a frame that
// still obeys the properties above.
export const resizes = actions<ActionTemplate>(() => [
  { Resize: { columns: [60, 140], rows: [18, 40] } },
]);
