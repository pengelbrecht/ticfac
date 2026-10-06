/**
 * Staging proof: epic 43y's [A2] fault claims on the real platform (tick jhp).
 * Drives the same staging agent Worker as proof/agent-staging.ts
 * (cloudflare/src/staging-agent.ts, deployed from cloudflare/staging/
 * agent.wrangler.toml) and injects the three faults the epic's acceptance
 * names, one attempt each:
 *
 *   1. HOST LOST MID-TOOL — a `wrangler deploy` fired at the first
 *      tool_execution_start of a ninety-second bash (a staging deploy takes
 *      about twenty, so the host dies well inside the tool): the deploy kills the
 *      WorkerAgent Durable Object the way a lost harness process dies, while
 *      the tracked bash keeps running in its container. The new isolate's
 *      heartbeat reopens the storage, pi-durable resumes the unfinished task,
 *      the tracked bash REATTACHES to the process it left running — and the
 *      tool must not run twice. Evidence: the attempt settles 0 and the
 *      tool's side-effect file carries exactly one line.
 *   2. CONTAINER DESTROYED MID-TURN — since tick a2l the staging origin
 *      OUTLIVES THE BOX (the attempt's repository is the staging Worker's
 *      own /proof/git origin, in Durable Object storage), so the proof
 *      destroys the whole container between tool rounds, the loss the
 *      checkpoint extension's ready check exists for (tick 4fs): the next
 *      round's request goes out only after ensureWorkspaceReady has booted
 *      the replacement, fetched from the origin that survived, and checked
 *      out the last wip commit — and the turn completes from the restored
 *      tree. Evidence: the attempt settles 0, the log shows "workspace
 *      restored to …", and both steps' work is on the pushed branch.
 *   3. DEPLOY MID-RUN — a `wrangler deploy` fired during the conversation,
 *      under a generation rather than at a tool: the attempt settles 0 with
 *      the work delivered, whatever the deploy interrupted. Evidence: the
 *      attempt settles 0 and the report is on the pushed branch.
 *
 * Usage (Node 24 runs the TypeScript directly):
 *
 *   PROOF_URL=https://ticfac-staging-agent.<subdomain>.workers.dev PROOF_TOKEN=<token> \
 *     node proof/agent-faults-staging.ts evidence.json
 *
 * Exits 0 when every claim holds, 1 naming the ones that do not.
 */

import { execFile } from "node:child_process";
import { writeFileSync } from "node:fs";
import { promisify } from "node:util";

import { resumedFromStorage, wipCheckpointsAfterResume } from "./fault-claims.ts";

const run = promisify(execFile);

const base = (process.env.PROOF_URL ?? "").replace(/\/+$/, "");
const token = process.env.PROOF_TOKEN ?? "";
const out = process.argv[2] ?? "agent-faults-evidence.json";
if (base === "" || token === "") {
  console.error("PROOF_URL and PROOF_TOKEN are required");
  process.exit(2);
}
// POSIX path of this script's directory (harness/proof); the cloudflare
// package that deploys the staging Worker sits two levels up beside it.
const proofDir = new URL(".", import.meta.url).pathname;
const cloudflareDir = `${proofDir}../../cloudflare`;

type State = {
  phase: string;
  exit_code: number | null;
  detail: string | null;
  branch: string | null;
};

/** The attempt the calls below address; one scenario at a time. */
let name = "";
const auth = { authorization: `Bearer ${token}` };

async function call(verb: string, init: RequestInit = {}, query = ""): Promise<unknown> {
  const response = await fetch(`${base}/proof/${verb}?name=${name}${query}`, {
    ...init,
    headers: { ...auth, "content-type": "application/json", ...(init.headers ?? {}) },
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${verb}: ${response.status} ${text}`);
  return JSON.parse(text);
}

async function exec(command: string): Promise<{ exitCode: number; output: string }> {
  const answer = (await call("exec", { method: "POST", body: JSON.stringify({ command }) })) as {
    ready: boolean;
    exitCode?: number;
    output?: string;
  };
  return answer.ready
    ? { exitCode: answer.exitCode ?? -1, output: answer.output ?? "" }
    : { exitCode: -1, output: "(the container was not ready)" };
}

/** A deploy of the staging Worker — the platform's own way to kill the host. */
async function deploy(): Promise<string> {
  const { stdout } = await run(
    "pnpm",
    ["exec", "wrangler", "deploy", "-c", "staging/agent.wrangler.toml"],
    { cwd: cloudflareDir, maxBuffer: 8 * 1024 * 1024 },
  );
  return stdout;
}

type WatchHandle = { seen: (type: string) => number; close: () => void };

/** Watches the attempt's conversation, firing `when` once its event count says so. */
function watch(when: (seen: (type: string) => number) => Promise<void>): WatchHandle {
  const counts = new Map<string, number>();
  const handle: WatchHandle = {
    seen: (type) => counts.get(type) ?? 0,
    close: () => socket.close(),
  };
  let fired = false;
  const url = `${base.replace(/^http/, "ws")}/proof/watch?name=${name}`;
  const socket = new WebSocket(url, { headers: auth } as unknown as string[]);
  socket.addEventListener("message", (event) => {
    const frame = JSON.parse(String(event.data)) as { type: string; events?: { type: string }[] };
    for (const e of frame.events ?? []) counts.set(e.type, (counts.get(e.type) ?? 0) + 1);
    if (frame.type === "events" && !fired && handle.seen("tool_execution_start") > 0) {
      fired = true;
      void when(handle.seen).catch((error: unknown) => {
        timeline.push({
          at_s: Math.round((Date.now() - started) / 100) / 10,
          what: `the trigger failed: ${error instanceof Error ? error.message : String(error)}`,
        });
      });
    }
  });
  socket.addEventListener("error", () => {
    /* deploys close sockets; the state poll is the durable signal */
  });
  return handle;
}

async function startAttempt(prompt: string): Promise<State> {
  const start = (await call("start", {
    method: "POST",
    body: JSON.stringify({ prompt, model: "workers-ai/@cf/zai-org/glm-5.3" }),
  })) as { state: State };
  return start.state;
}

/** Polls state until settled, tolerating the transient errors a deploy makes. */
async function waitSettled(deadlineMs: number): Promise<State> {
  const deadline = Date.now() + deadlineMs;
  let failures = 0;
  for (;;) {
    if (Date.now() > deadline) throw new Error("the attempt did not settle in time");
    try {
      const state = (await call("state")) as State;
      if (state.phase === "settled") return state;
      failures = 0;
    } catch (error) {
      failures += 1;
      if (failures > 30) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 3000));
  }
}

async function readLog(): Promise<string> {
  let log = "";
  for (let offset = 0; ; ) {
    const chunk = (await call("log", {}, `&offset=${offset}`)) as { text: string; offset: number };
    if (chunk.text === "") break;
    log += chunk.text;
    offset = chunk.offset;
  }
  return log;
}

const started = Date.now();
const timeline: { at_s: number; what: string }[] = [];
const mark = (what: string) => {
  timeline.push({ at_s: Math.round((Date.now() - started) / 100) / 10, what });
  console.log(`[${timeline.at(-1)?.at_s}s] ${what}`);
};

const evidence: {
  attempts: Record<string, { state: State; log: string }>;
  timeline: typeof timeline;
  claims: Record<string, boolean>;
} & Record<string, unknown> = { attempts: {}, timeline: [], claims: {} };

/**
 * Runs one attempt: starts it, watches the conversation, fires the fault at
 * the watch says, waits for settlement and records the evidence.
 */
async function attempt(
  id: string,
  prompt: string,
  fault: (seen: (type: string) => number) => Promise<void>,
): Promise<{ state: State; log: string }> {
  name = id;
  mark(`starting ${id}`);
  const start = await startAttempt(prompt);
  mark(`started: ${JSON.stringify(start)}`);

  const handle = watch(fault);
  let state: State;
  try {
    state = await waitSettled(20 * 60 * 1000);
  } finally {
    handle.close();
  }
  mark(`settled: ${JSON.stringify(state)}`);
  const log = await readLog();
  evidence.attempts[id] = { state, log };
  return { state, log };
}

/**
 * Releases the attempt's container. Called only AFTER its evidence is read:
 * a reclaim destroys the container; the origin it pushed to outlives the
 * box (tick a2l) but its workspace does not.
 */
async function reclaim(): Promise<void> {
  await call("reclaim", { method: "POST" }).catch(() => undefined);
}

/** The branch the finish phase pushed, as the settled state names it. */
const branchOf = (state: State) => state.branch ?? "tick/proof/xd3";

function claim(text: string, ok: boolean): void {
  evidence.claims[text] = ok;
}

// --- 1. HOST LOST MID-TOOL (deploy fired at the long tool's start) ---------

{
  const { state, log } = await attempt(
    "kill-host-mid-tool",
    [
      "You are working in /work/repo, a git checkout on its own branch.",
      "",
      "1. Run this EXACT bash command with the bash tool, and wait for it to finish (it takes about ninety seconds):",
      "   sleep 90 && printf 'tool-ran-once\\n' >> tool-runs.txt",
      "2. Then run: cat tool-runs.txt",
      "3. Commit everything on the current branch with git.",
      "4. Write RESULT-xd3.md in /work/repo: one line saying what you changed, then a last line `STATUS: DONE`.",
      "",
      "Use the tools; do not ask questions.",
    ].join("\n"),
    async () => {
      mark("a tool is running; deploying the staging Worker (the host dies with it)");
      const stdout = await deploy();
      mark(
        /Deployed|Updated/.test(stdout)
          ? `the deploy finished: ${/Deployed|Updated/.exec(stdout)?.[0] ?? "redeployed"}`
          : `the deploy finished: ${stdout.slice(0, 120)}`,
      );
    },
  );

  // The evidence, from the attempt's own workspace: the branch the finish
  // pushed, checked out in the container that holds it.
  const runs = await exec(`git -C /work/repo show ${branchOf(state)}:tool-runs.txt`);
  evidence.tool_runs_file = runs.output;
  await reclaim();
  claim("kill the host mid-tool: the attempt settles 0", state.exit_code === 0);
  claim(
    "kill the host mid-tool: the tool did not run twice (the resumed host reattached, it did not re-run)",
    runs.output.trim() === "tool-ran-once",
  );
  claim(
    "kill the host mid-tool: a new host life resumed the conversation from its storage",
    resumedFromStorage.test(log),
  );
  // Order-checked (tick umx): the checkpoint lines must FOLLOW the resume
  // line in the log — the proof's log is an append-only stream, so position
  // in it is order in time, and a checkpoint from before the kill no longer
  // passes this claim. The count is recorded so the evidence says how many
  // landed, the number the README's "four … after the resume" read by hand.
  const checkpointsAfterResume = wipCheckpointsAfterResume(log);
  evidence.checkpoints_after_resume = checkpointsAfterResume;
  claim(
    "kill the host mid-tool: wip checkpoints kept landing after the resume",
    checkpointsAfterResume > 0,
  );
}

// --- 2. CONTAINER DESTROYED MID-TURN (the replacement box; the restore) ------

{
  const { state, log } = await attempt(
    "container-destroyed-mid-turn",
    [
      "You are working in /work/repo, a git checkout on its own branch.",
      "",
      "1. Write the file first-step.txt with the content: step one",
      "2. WAIT for that to finish. Only then, write the file second-step.txt with the content: step two",
      "3. Verify with the read tool that both files exist, then commit everything on the current branch with git.",
      "4. Write RESULT-xd3.md in /work/repo: one line saying what you changed, then a last line `STATUS: DONE`.",
      "",
      "Do the two writes in SEPARATE steps, one at a time. Use the tools; do not ask questions.",
    ].join("\n"),
    async (seen) => {
      // Wait for a round boundary — every started tool has ended — then for
      // that round's wip checkpoint to land on the origin that outlives the
      // box, and destroy the whole CONTAINER while the model is between
      // rounds: the next round's ready check must boot the replacement and
      // restore it from that checkpoint before the request goes out.
      for (;;) {
        const ends = seen("tool_execution_end");
        if (ends >= 1 && ends === seen("tool_execution_start")) break;
        await new Promise((resolve) => setTimeout(resolve, 200));
      }
      let before = "";
      try {
        before = await readLog();
      } catch {
        /* the log is best effort; the checkpoint poll below re-reads it */
      }
      const hadCheckpoints = (before.match(/wip checkpoint [0-9a-f]+ pushed/g) ?? []).length;
      for (;;) {
        const log = await readLog();
        if ((log.match(/wip checkpoint [0-9a-f]+ pushed/g) ?? []).length > hadCheckpoints) break;
        await new Promise((resolve) => setTimeout(resolve, 500));
      }
      mark("a round's checkpoint landed on the origin; destroying the container between rounds");
      const destroyed = await call("destroy", { method: "POST" }, "&tick=xd3");
      mark(`the container is destroyed (${JSON.stringify(destroyed)}) — the origin outlived it`);
    },
  );

  // The evidence, from the container the restore left behind: its workspace
  // is the pushed branch, checked out at the tip the finish pushed.
  const first = await exec(`git -C /work/repo show ${branchOf(state)}:first-step.txt`);
  const second = await exec(`git -C /work/repo show ${branchOf(state)}:second-step.txt`);
  evidence.restored_files = { first: first.output, second: second.output };
  await reclaim();
  claim("container destroyed mid-turn: the attempt settles 0", state.exit_code === 0);
  claim(
    "container destroyed mid-turn: the log shows the restore from the last wip commit",
    /workspace restored to [0-9a-f]+/.test(log),
  );
  claim(
    "container destroyed mid-turn: the turn completed on the restored tree (both steps' work pushed)",
    first.output.trim() === "step one" && second.output.trim() === "step two",
  );
}

// --- 3. DEPLOY MID-RUN (fired during the conversation, not at a tool) -----

{
  name = "deploy-mid-run";
  mark("starting deploy-mid-run");
  const start = await startAttempt(
    [
      "You are working in /work/repo, a git checkout on its own branch.",
      "",
      "1. Write RESULT-xd3.md in /work/repo: one line saying what you changed, then a last line `STATUS: DONE`.",
      "2. Commit everything on the current branch with git.",
      "",
      "Use the tools; do not ask questions.",
    ].join("\n"),
  );
  mark(`started: ${JSON.stringify(start)}`);
  // Deploy the moment the boot has handed off and the conversation is on:
  // the first model request is in flight, not a tool — whatever the deploy
  // interrupts, the attempt must still deliver, from its storage.
  for (;;) {
    const now = (await call("state")) as State;
    if (now.phase !== "booting") break;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  mark("the conversation is on; deploying the staging Worker mid-conversation");
  const stdout = await deploy();
  mark(
    /Deployed|Updated/.test(stdout)
      ? `the deploy finished: ${/Deployed|Updated/.exec(stdout)?.[0] ?? "redeployed"}`
      : `the deploy finished: ${stdout.slice(0, 120)}`,
  );

  const state = await waitSettled(20 * 60 * 1000);
  mark(`settled: ${JSON.stringify(state)}`);
  const log = await readLog();
  evidence.attempts["deploy-mid-run"] = { state, log };

  const report = await exec(`git -C /work/repo show ${branchOf(state)}:RESULT-xd3.md`);
  evidence.deploy_mid_run_report = report.output;
  await reclaim();
  claim("deploy mid-run: the attempt settles 0", state.exit_code === 0);
  claim(
    "deploy mid-run: a new host life resumed the conversation from its storage",
    resumedFromStorage.test(log),
  );
  claim(
    "deploy mid-run: the work was delivered (the report is on the pushed branch)",
    /STATUS: /.test(report.output),
  );
}

evidence.timeline = timeline;
writeFileSync(out, `${JSON.stringify(evidence, null, 2)}\n`);

const failed = Object.entries(evidence.claims).filter(([, ok]) => !ok);
for (const [text, ok] of Object.entries(evidence.claims))
  console.log(`${ok ? "HOLDS " : "FAILS "} ${text}`);
if (failed.length > 0) {
  console.log(`PROOF FAILS: ${failed.length} claim(s) — evidence in ${out}`);
  process.exit(1);
}
console.log(`PROOF HOLDS — evidence in ${out}`);
