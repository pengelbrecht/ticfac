#!/usr/bin/env node
// trace-frame.mjs — print the last screen state a Bombadil trace recorded:
// the dev window into what the driver actually saw (tui/README.md).
import { createReadStream } from "node:fs";
import { createInterface } from "node:readline";

const path = process.argv[2] ?? "";
if (!path) {
  console.error("usage: trace-frame.mjs <trace.jsonl> [which: last|all]");
  process.exit(2);
}
const which = process.argv[3] ?? "last";

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

// The trace is read as a STREAM, never as one string. A trace holds every
// state the driver sampled, and how many it samples is the host's business:
// a loaded host's honest overview run left a 1.2 GB trace — past Node's
// per-string limit — and the readFileSync this replaced failed on it with
// ERR_STRING_TOO_LONG, so the dev window answered nothing for exactly the
// run a person was debugging. One line at a time, the memory is the line;
// `all` prints as it reads, and `last` keeps only the record it will print.
let first = null;
let last = null;
let seen = 0;
const rl = createInterface({ input: createReadStream(path) });
const show = (record) => {
  console.log(
    `--- state at +${((record.timestamp - first) / 1e6).toFixed(2)}s (action ${JSON.stringify(record.action)}) ---`,
  );
  for (const row of decode(record)) console.log(row);
};
for await (const line of rl) {
  if (!line.trim()) continue;
  let record;
  try {
    record = JSON.parse(line);
  } catch {
    continue;
  }
  if (!record || !record.state || !record.state.grid || !record.state.grid.cells) continue;
  if (first === null) first = record.timestamp;
  seen++;
  if (which === "all") show(record);
  else last = record;
}
if (which !== "all" && last !== null) show(last);
if (seen === 0) console.error("no grid states in the trace");
