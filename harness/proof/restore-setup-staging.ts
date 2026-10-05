/**
 * Staging proof: the restore's setup survives a REAL dependency install
 * (epic 43y, tick cni). Drives the staging agent Worker
 * (cloudflare/src/staging-agent.ts, cloudflare/staging/agent.wrangler.toml) —
 * the same two stand-ins `agent-staging.ts` names — and answers the two
 * questions the tick carries, on the real platform:
 *
 *  1. THE RESTORE'S SETUP. An attempt loses its workspace between tool
 *     rounds; the pre-round ready check rebuilds it from the attempt branch
 *     — clear, clone, checkout of the wip tip, then the repository's
 *     `[sandbox]` setup (`ticks-worker --setup`: a real apt toolchain
 *     install, minutes of it) — and the turn completes on the restored
 *     workspace with its dependencies live. The workspace is EMPTIED rather
 *     than the container destroyed because the staging stand-in keeps the
 *     bare origin INSIDE the container, so a destroyed box could fetch
 *     nothing back; the setup line is the same either way, and the
 *     mid-turn destroy is jhp's proof.
 *
 *  2. THE RUN DOOR'S HOLD — the tick's open question, now an observation
 *     rather than a dependency: does ONE `run` RPC, holding one container
 *     exec await for minutes, answer with the command's own exit code? The
 *     restore's setup no longer rides this door (its bounding `head -c`
 *     SIGPIPEs a chatty install — the `exit 141` the node suite reproduces —
 *     so the setup rides the process doors instead); the answer is recorded
 *     so the door's callers know its shape.
 *
 * Usage (Node 24 runs the TypeScript directly):
 *
 *   PROOF_URL=https://ticfac-staging-agent.<subdomain>.workers.dev PROOF_TOKEN=<token> \
 *     node proof/restore-setup-staging.ts evidence.json
 *
 * Exits 0 when every claim holds, 1 naming the ones that do not.
 */

import { writeFileSync } from "node:fs";

const base = (process.env.PROOF_URL ?? "").replace(/\/+$/, "");
const token = process.env.PROOF_TOKEN ?? "";
const out = process.argv[2] ?? "restore-setup-staging-evidence.json";
if (base === "" || token === "") {
  console.error("PROOF_URL and PROOF_TOKEN are required");
  process.exit(2);
}
const name = `proof-cni-${Date.now().toString(36)}`;
const tick = "cni";
const branch = `tick/proof/${tick}`;
const auth = { authorization: `Bearer ${token}` };

const PROMPT = [
  "You are working in /work/repo, a git checkout on its own branch.",
  "",
  "1. greet.sh prints a misspelled greeting. Fix it so `./greet.sh` prints exactly `Hello, world`.",
  "2. Run `./greet.sh` to check it.",
  "3. Commit your change on the current branch with git.",
  "4. Write RESULT-cni.md in /work/repo: one line saying what you changed, then a last line `STATUS: DONE`.",
  "",
  "Use the tools; do not ask questions.",
].join("\n");

async function call(verb: string, init: RequestInit = {}, query = ""): Promise<unknown> {
  const response = await fetch(`${base}/proof/${verb}?name=${name}${query}`, {
    ...init,
    headers: { ...auth, "content-type": "application/json", ...(init.headers ?? {}) },
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${verb}: ${response.status} ${text}`);
  return JSON.parse(text);
}

async function exec(
  command: string,
): Promise<{ ready: boolean; exitCode?: number; output?: string }> {
  return (await call("exec", { method: "POST", body: JSON.stringify({ command }) })) as {
    ready: boolean;
    exitCode?: number;
    output?: string;
  };
}

type State = {
  phase: string;
  exit_code: number | null;
  detail: string | null;
  branch: string | null;
};

const started = Date.now();
const timeline: { at_s: number; what: string }[] = [];
const mark = (what: string) => {
  timeline.push({ at_s: Math.round((Date.now() - started) / 100) / 10, what });
  console.log(`[${timeline.at(-1)?.at_s}s] ${what}`);
};

mark(`starting attempt ${name}`);
const start = (await call("start", {
  method: "POST",
  body: JSON.stringify({ prompt: PROMPT, tick }),
})) as { run_id: string; state: State };
mark(`started: ${JSON.stringify(start.state)}`);

/** The log so far, polled — the restore announces itself here. */
let cursor = 0;
let log = "";
const readLog = async () => {
  const chunk = (await call("log", {}, `&offset=${cursor}`)) as {
    text: string;
    offset: number;
  };
  if (chunk.text !== "") {
    log += chunk.text;
    for (const line of chunk.text.split("\n").filter((l) => l.trim() !== "")) {
      mark(`log: ${line.slice(0, 160)}`);
    }
    cursor = chunk.offset;
  }
};

/**
 * The between-rounds loss (tick 4fs's shape): the workspace is emptied while
 * no tool is in flight, so the next round's ready check is what sees it.
 * One emptying per completed round, until the restore announces itself.
 */
const emptyWorkspace = async () => {
  const answer = await exec("find /work/repo -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +");
  if (!answer.ready || answer.exitCode !== 0) {
    mark(`emptying the workspace refused: ${JSON.stringify(answer)}`);
    return;
  }
  mark("the workspace was emptied between rounds — the next round must restore it");
};

let emptied = false;
let lastEmptyAt = 0;
let restoredAt = 0;
let restoredLine: RegExpMatchArray | null = null;
let state = start.state;
const deadline = Date.now() + 30 * 60 * 1000;
while (state.phase !== "settled" && Date.now() < deadline) {
  await new Promise((resolve) => setTimeout(resolve, 2000));
  state = (await call("state")) as State;
  if (state.phase === "conversing") await readLog();

  const pushedLines = (log.match(/wip checkpoint [0-9a-f]+ pushed/g) ?? []).length;
  const seen = log.match(
    /the container was lost between rounds; workspace restored to ([0-9a-f]+)/,
  );
  if (seen !== null && restoredLine === null) {
    restoredLine = seen;
    restoredAt = Date.now();
    mark("the restore announced itself (the ready check's path)");
  }
  if (!emptied && pushedLines >= 1) {
    // A round's work is on the attempt branch: the loss has something to
    // restore from. ONE emptying, never retried: the round that meets the
    // empty workspace restores through whichever path sees the loss first
    // — the ready check (which announces itself) or the tracked bash's
    // nonce check (which restores silently) — and a second emptying only
    // buys another install and a loss the model cannot explain (this
    // proof's first run did exactly that; the model spent its turns
    // investigating the harness and still finished, exit 0).
    emptied = true;
    await emptyWorkspace();
    lastEmptyAt = Date.now();
  }
}
mark(`settled: ${JSON.stringify(state)}`);
await readLog();
const seen = log.match(/the container was lost between rounds; workspace restored to ([0-9a-f]+)/);
if (seen !== null && restoredLine === null) {
  restoredLine = seen;
  restoredAt = Date.now();
}

// The evidence, from the container's own origin: what the finish pushed.
const fromOrigin = async (what: string) => {
  const answer = await exec(`git -C /srv/origin.git show "${branch}:${what}"`);
  return answer.ready ? (answer.output ?? "") : "";
};
const setupRan = await fromOrigin(".setup-ran");
const report = await fromOrigin(`RESULT-${tick}.md`);
const history = await exec(`git -C /srv/origin.git log --format=%s main..${branch}`);
const greet = await fromOrigin("greet.sh");
const liveToolchain = await exec("cd /work/repo && gcc --version | sed -n 1p");

// 2. THE RUN DOOR'S HOLD: one RPC, one container exec, minutes. Quiet on
// purpose — the door's `head -c` bound would SIGPIPE a chatty command, and
// the hold is the question here, not the bound (the node suite covers that).
mark("run door hold: one RPC, one container exec, 150s — asking");
const holdStart = Date.now();
let hold: unknown = null;
try {
  hold = await exec('{ sleep 150; printf "held"; }');
} catch (error) {
  hold = { error: error instanceof Error ? error.message : String(error) };
}
const heldSeconds = Math.round((Date.now() - holdStart) / 100) / 10;
mark(`run door answered after ${heldSeconds}s: ${JSON.stringify(hold)}`);

const holdAnswer = hold as { ready?: boolean; exitCode?: number; output?: string; error?: string };
const setupSeconds = Number(setupRan.match(/^seconds: (\d+)$/m)?.[1] ?? 0);
const restoreGap =
  restoredLine === null || lastEmptyAt === 0
    ? 0
    : Math.round((restoredAt - lastEmptyAt) / 100) / 10;

const historyText = history.ready ? (history.output ?? "") : "";

const claims: Record<string, boolean> = {
  "the attempt settled with exit 0 (the finish pushed a report with a STATUS line)":
    state.phase === "settled" && state.exit_code === 0,
  // The setup entry the restore runs is the ONLY writer of .setup-ran, and
  // it lands in a tree the loss had EMPTIED: it on the pushed branch is
  // the restore itself, whichever path fired it.
  "the workspace was lost between rounds and rebuilt by the restore":
    /gcc: \S+ \(.+\)/.test(setupRan) && setupSeconds > 0,
  "the restore's setup was a REAL dependency install, minutes of it":
    /go version go/.test(setupRan) && setupSeconds >= 60,
  "the install the restore's setup made is live in the container the turn continued in":
    /gcc \(.+\)/.test(liveToolchain.ready ? (liveToolchain.output ?? "") : ""),
  "the round's edit survived the loss: the restored tree carried it to the pushed branch":
    /Hello, world/.test(greet) && historyText.trim().split("\n").length >= 2,
  "the report is on the pushed branch with a STATUS line": /STATUS: /.test(report),
  "one run-door RPC held one 150s container exec and answered with its own exit code":
    holdAnswer?.ready === true &&
    holdAnswer?.exitCode === 0 &&
    (holdAnswer?.output ?? "").includes("held") &&
    heldSeconds >= 140,
};

const evidence = {
  attempt: name,
  run_id: start.run_id,
  state,
  timeline,
  restore: {
    // Whichever path fired it — the ready check (announced) or the tracked
    // bash's nonce check (silent) — it is the same restoreLostWorkspace; the
    // node and workerd suites pin both paths' wiring.
    announced_line: restoredLine?.[0] ?? null,
    setup_ran: setupRan,
    gap_seconds: restoreGap,
  },
  branch: {
    history: history.ready ? (history.output ?? "") : "",
    greet,
    report,
  },
  run_door_hold: { answer: hold, held_seconds: heldSeconds },
  live_toolchain: liveToolchain,
  log,
};
writeFileSync(out, `${JSON.stringify(evidence, null, 2)}\n`);
await call("reclaim", { method: "POST" }).catch(() => undefined);

const failed = Object.entries(claims).filter(([, ok]) => !ok);
for (const [claim, ok] of Object.entries(claims))
  console.log(`${ok ? "HOLDS " : "FAILS "} ${claim}`);
if (failed.length > 0) {
  console.log(`PROOF FAILS: ${failed.length} claim(s) — evidence in ${out}`);
  process.exit(1);
}
console.log(`PROOF HOLDS — evidence in ${out}`);
