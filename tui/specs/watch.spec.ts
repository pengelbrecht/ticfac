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
// The world's facts come from world.json (the runner writes it beside this
// file before each run): the plan order the tracker answers, the run the
// watch holds, and the command that clears it.
import { always, eventually, type Formula } from "@antithesishq/bombadil";
import { extract, type State, actions, type ActionTemplate } from "@antithesishq/bombadil/terminal";
import world from "./world.json" with { type: "json" };

// The screen as one text block, and the frame's own marker: the tick
// table's header. A state that shows the marker is a state a person is
// reading the dashboard in; the properties are about exactly those states.
const screen = extract((s: State): string => {
  const rows: string[] = [];
  for (let i = 0; i < s.grid.size.rows; i++) rows.push(s.grid.rowText(i));
  return rows.join("\n");
});
const columns = extract((s: State): number => s.grid.size.columns);
const frameShown = extract((s: State): boolean => {
  for (let i = 0; i < s.grid.size.rows; i++) {
    if (s.grid.rowText(i).includes("TICK")) return true;
  }
  return false;
});

// The tick ids the frame's rows carry, in the order the rows appear — the
// dashboard's own row order, read back off the screen. The feed lines name
// ticks as "t1#1"; the plan's ids are "t01"-shaped, so only table rows
// match, and drill-in views (one tick's own rows) keep the plan's order by
// construction.
const rowIDs = extract((s: State): string[] => {
  const found: string[] = [];
  for (let i = 0; i < s.grid.size.rows; i++) {
    const text = s.grid.rowText(i);
    for (const id of world.planOrder) {
      if (text.includes(id) && !found.includes(id)) found.push(id);
    }
  }
  return found;
});

const isSubsequence = (plan: string[], rows: string[]): boolean => {
  let i = 0;
  for (const id of rows) {
    if (i < plan.length && id === plan[i]) i++;
  }
  return i === plan.length;
};

// The watch never outlives its run here, so a plain boolean in the thunk is
// the whole formula; the implies shape keeps every property quiet until the
// first frame and loud for every frame after it.
const overTheFrame = (holds: () => boolean): Formula =>
  always(() => !frameShown.current || holds());

// P1 (outside): the rows never reorder — the ids the screen's rows carry,
// in their screen order, are a subsequence of the plan's order, at every
// pane shape the generator resizes to.
export const rowsNeverReorder = overTheFrame(() => isSubsequence(world.planOrder, rowIDs.current));

// P2 (outside): a run that ended holding something for a person never says
// "needs you: nothing" — and the hold's clearing command is on screen,
// every word of it. A wrap is by design (u4l's P2, tick 9um: the pane that
// cannot seat the whole line wraps the command under the announcement), so
// the assertion is the command's WORDS, not its one-line spelling.
export const needsYouCarriesTheHold = overTheFrame(() => {
  const text = screen.current;
  if (text.includes("needs you: nothing")) return false;
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
// dashboard's own header, on the screen, within five seconds of the watch
// starting. The Go side pins the tight bound (status_firstframe_test.go);
// this is the outside half, over a real spawn.
export const firstFrameWithinItsBound = eventually(() => frameShown.current).within(5, "seconds");

// The actions: generated resizes across the pane shapes a herdr pane and a
// laptop terminal take. The dashboard answers each one with a frame that
// still obeys the properties above.
export const resizes = actions<ActionTemplate>(() => [
  { Resize: { columns: [60, 140], rows: [18, 40] } },
]);
