#!/usr/bin/env node
// trace-frame.mjs — print the last screen state a Bombadil trace recorded:
// the dev window into what the driver actually saw (tui/README.md).
import { readFileSync } from "node:fs";

const path = process.argv[2] ?? "";
if (!path) {
  console.error("usage: trace-frame.mjs <trace.jsonl> [which: last|all]");
  process.exit(2);
}
const which = process.argv[3] ?? "last";

const states = [];
for (const line of readFileSync(path, "utf8").split("\n")) {
  if (!line.trim()) continue;
  let record;
  try {
    record = JSON.parse(line);
  } catch {
    continue;
  }
  if (record && record.state && record.state.grid && record.state.grid.cells) {
    states.push(record);
  }
}

const decode = (record) => {
  const { cells } = record.state.grid;
  const size = record.state.grid.size;
  // Bombadil 0.7.x records a cell as Empty or Occupied (contents + style
  // separate — the text is plain, the colours are not in it).
  const text = cells.map((cell) => {
    const key = Object.keys(cell)[0];
    if (key === "Occupied" || key === "Wide") return cell[key].contents ?? "?";
    return " ";
  });
  const rows = [];
  for (let r = 0; r < size.rows; r++) {
    rows.push(text.slice(r * size.columns, (r + 1) * size.columns).join("").trimEnd());
  }
  return rows;
};

const chosen = which === "all" ? states : [states[states.length - 1] ?? null].filter(Boolean);
for (const record of chosen) {
  console.log(`--- state at +${((record.timestamp - states[0].timestamp) / 1e6).toFixed(2)}s (action ${JSON.stringify(record.action)}) ---`);
  for (const row of decode(record)) console.log(row);
}
if (states.length === 0) console.error("no grid states in the trace");
