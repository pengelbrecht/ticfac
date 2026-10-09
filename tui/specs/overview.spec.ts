// overview.spec.ts — the properties of the bare `ticfac` overview, asserted
// from outside the process through a real terminal (tick z7w): the one
// screen an unattended factory is glanced at with, driven by a fixture
// world of registries and feeds (tui/build-world.mjs).
//
// The same specification runs against the seeded broken programs
// (tui/seeded/) in the suite's seeded half, where each property is shown to
// fail against a deliberately broken screen.
import { always, eventually } from "@antithesishq/bombadil";
import { extract, type State, actions, type ActionTemplate } from "@antithesishq/bombadil/terminal";
import world from "./world.json" with { type: "json" };

// The screen as one text block; the listing's marker is its closing prose
// (the cloud note the honest world always prints when no factory is
// configured) — a state that shows it is a state the listing has answered
// in, and the seeded programs print it too, so the properties are never
// vacuous against either.
const screenText = (s: State): string => {
  const rows: string[] = [];
  for (let i = 0; i < s.grid.size.rows; i++) rows.push(s.grid.rowText(i));
  return rows.join("\n");
};
const screen = extract((s: State): string => screenText(s));
const listed = extract((s: State): boolean =>
  screenText(s).includes("cloud runs are not listed"),
);
const exited = extract((s: State): number | null =>
  s.exitStatus === null ? null : s.exitStatus.code,
);

// A run's row is the line that leads with its id: "epic-stl: held for a
// person — …". The state word is the row's first word after the id — the
// overview's own vocabulary (held, failed, running, done, cancelled), never
// prose that merely contains the word ("not_running" is not "running").
const rowsFor = (text: string, runID: string): string[] =>
  text.split("\n").filter((line) => line.trimStart().startsWith(runID + ":"));
const stateWordOf = (row: string): string => {
  const at = row.indexOf(":");
  return at < 0 ? "" : row.slice(at + 1).trim().split(/\s+/)[0] ?? "";
};

// P1: a stale or orphaned run never shows as live — none of the world's
// stale, gone-checkout (#86) and ten-days-silent runs renders a row whose
// state word is running. The world holds no live process, so "running" on
// any of those rows would be a phantom of exactly the class the operator
// saw in the factory's cloud runs.
export const noStaleRunShowsAsLive = always(() => {
  if (!listed.current) return true;
  const text = screen.current;
  for (const runID of world.neverLive) {
    for (const row of rowsFor(text, runID)) {
      if (stateWordOf(row) === "running") return false;
    }
  }
  return true;
});

// P2: every held or failed run shows the ONE command that clears it — the
// row names the reason and carries the command a person can copy whole.
export const everyStopShowsItsClearingCommand = always(() => {
  if (!listed.current) return true;
  const text = screen.current;
  for (const need of world.needingAPerson) {
    const rows = rowsFor(text, need.runID);
    if (rows.length === 0) return false;
    if (!rows.some((row) => row.includes(need.command))) return false;
  }
  return true;
});

// P3 (#87): history collapses into one line — the finished runs the human
// view folds away are absent as rows, and the one summary line that says so
// is present: a screen of attention, not a screen of history.
export const historyCollapsesIntoOneLine = always(() => {
  if (!listed.current) return true;
  const text = screen.current;
  for (const runID of world.history) {
    if (rowsFor(text, runID).length > 0) return false;
  }
  return text.includes(world.collapseMention);
});

// P4: the overview is one answer — the program ends, and it ends with the
// table's own exit code (0: attention is data the screen orders by, not a
// failure of the command that reports it).
export const theListingAnswersAndEnds = eventually(() => exited.current === 0).within(20, "seconds");

// The actions: CLICKS, and only clicks. The overview is a one-shot answer
// that prints once and exits — a resize would reflow the printed text (the
// terminal's doing, not a second ticfac answer) and typed text would ECHO
// over the listing (the program never reads stdin) — both molest the screen
// the properties assert. A click the program ignores is an action the screen
// never shows, and the engine needs at least one generator to run.
export const clicks = actions<ActionTemplate>(() => [
  { Click: { row: 3, column: 5 } },
]);
