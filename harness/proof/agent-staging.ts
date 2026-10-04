/**
 * Staging proof: one cloud worker attempt on its WorkerAgent, end to end
 * (epic 43y, tick xd3). Drives the staging agent Worker
 * (cloudflare/src/staging-agent.ts, cloudflare/staging/agent.wrangler.toml):
 *
 *   1. starts an attempt on a real WorkerAgent Durable Object;
 *   2. watches it over the agent's WebSocket while it converses, and steers it
 *      once its first tool call lands;
 *   3. waits for it to settle and reads the evidence back: the agent's state
 *      and log, and — from the attempt's container — the branch the finish
 *      phase pushed, its wip checkpoints, the fix, the steer's edit and the
 *      report.
 *
 * Usage (Node 24 runs the TypeScript directly):
 *
 *   PROOF_URL=https://ticfac-staging-agent.<subdomain>.workers.dev PROOF_TOKEN=<token> \
 *     node proof/agent-staging.ts evidence.json
 *
 * Exits 0 when every claim holds, 1 naming the ones that do not.
 */

import { writeFileSync } from "node:fs";

const base = (process.env.PROOF_URL ?? "").replace(/\/+$/, "");
const token = process.env.PROOF_TOKEN ?? "";
const out = process.argv[2] ?? "agent-staging-evidence.json";
if (base === "" || token === "") {
  console.error("PROOF_URL and PROOF_TOKEN are required");
  process.exit(2);
}
const name = `proof-${Date.now().toString(36)}`;
const auth = { authorization: `Bearer ${token}` };

const PROMPT = [
  "You are working in /work/repo, a git checkout on its own branch.",
  "",
  "1. greet.sh prints a misspelled greeting. Fix it so `./greet.sh` prints exactly `Hello, world`.",
  "2. Run `./greet.sh` to check it.",
  "3. Commit your change on the current branch with git.",
  "4. Write RESULT-xd3.md in /work/repo: one line saying what you changed, then a last line `STATUS: DONE`.",
  "",
  "Use the tools; do not ask questions.",
].join("\n");

const STEER = "Also append the line `proofed by the WorkerAgent` to README.md, and commit it too.";

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
  body: JSON.stringify({ prompt: PROMPT, model: "workers-ai/@cf/zai-org/glm-5.3" }),
})) as { run_id: string; state: State };
mark(`started: ${JSON.stringify(start.state)}`);

// Watch, once the attempt is conversing, and steer on its first tool call.
let watched = 0;
const eventTypes = new Map<string, number>();
let steered: unknown = null;
let watcher: WebSocket | null = null;
const openWatch = () => {
  const url = `${base.replace(/^http/, "ws")}/proof/watch?name=${name}`;
  // Node's WebSocket (undici) takes headers in its options.
  const socket = new WebSocket(url, { headers: auth } as unknown as string[]);
  socket.addEventListener("message", (event) => {
    watched += 1;
    const frame = JSON.parse(String(event.data)) as {
      type: string;
      events?: { type: string }[];
      submission?: number;
      error?: string;
    };
    for (const e of frame.events ?? []) eventTypes.set(e.type, (eventTypes.get(e.type) ?? 0) + 1);
    if (frame.type === "steered" || frame.type === "error") {
      steered = frame;
      mark(`steer answered: ${JSON.stringify(frame)}`);
    }
    if (
      steered === null &&
      frame.type === "events" &&
      (frame.events ?? []).some((e) => e.type === "tool_execution_start")
    ) {
      steered = "sent";
      mark("first tool call seen on the socket; steering");
      socket.send(JSON.stringify({ type: "steer", text: STEER }));
    }
  });
  socket.addEventListener("open", () => mark("watch socket open"));
  socket.addEventListener("close", () => mark("watch socket closed"));
  return socket;
};

let state = start.state;
const deadline = Date.now() + 20 * 60 * 1000;
while (state.phase !== "settled" && Date.now() < deadline) {
  await new Promise((resolve) => setTimeout(resolve, 3000));
  const next = (await call("state")) as State;
  if (next.phase !== state.phase) mark(`phase ${state.phase} -> ${next.phase}`);
  state = next;
  if (state.phase === "conversing" && watcher === null) watcher = openWatch();
}
watcher?.close();
mark(`settled: ${JSON.stringify(state)}`);

let log = "";
for (let offset = 0; ; ) {
  const chunk = (await call("log", {}, `&offset=${offset}`)) as { text: string; offset: number };
  if (chunk.text === "") break;
  log += chunk.text;
  offset = chunk.offset;
}

const branch = state.branch ?? "tick/proof/xd3";
const greet = await exec(`git -C /srv/origin.git show ${branch}:greet.sh`);
const readme = await exec(`git -C /srv/origin.git show ${branch}:README.md`);
const report = await exec(`git -C /srv/origin.git show ${branch}:RESULT-xd3.md`);
const history = await exec(`git -C /srv/origin.git log --format=%s main..${branch}`);
const runGreet = await exec("cd /work/repo && ./greet.sh");

const claims: Record<string, boolean> = {
  "the attempt settled with exit 0 (the finish phase pushed a report with a STATUS line)":
    state.phase === "settled" && state.exit_code === 0,
  "the boot phase ran in the container and handed off": log.includes("booted: branch"),
  "the model ran tools in the container": log.includes("tool "),
  "the fix is on the pushed branch": /Hello, world/.test(greet.output),
  "the fixed script prints Hello, world": runGreet.output.trim() === "Hello, world",
  "the report is on the pushed branch with a STATUS line": /STATUS: /.test(report.output),
  "wip checkpoints landed on the branch after tool rounds": /wip: tool round/.test(history.output),
  "a watcher saw the conversation live": eventTypes.has("snapshot") && watched > 2,
  "a steer was placed while it worked": (steered as { type?: string } | null)?.type === "steered",
  "the steer's edit is on the pushed branch": /proofed by the WorkerAgent/.test(readme.output),
};

const evidence = {
  attempt: name,
  run_id: start.run_id,
  state,
  claims,
  timeline,
  watch: { frames: watched, event_types: Object.fromEntries(eventTypes), steered },
  branch: {
    history: history.output,
    greet: greet.output,
    readme: readme.output,
    report: report.output,
  },
  greet_runs: runGreet.output,
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
