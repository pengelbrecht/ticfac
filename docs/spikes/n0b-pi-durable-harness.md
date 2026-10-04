# Spike n0b: pi's durable harness in a Durable Object, the container as its hands

Tick `n0b` (pi durable harness spike), 2026-10-04. Related: epic `umq` (SDK 1.0 /
DO scheduling), whose `FactorySandbox` the prototype used as the tool backend.

**Recommendation: not now. Revisit when the trigger below fires.** It works,
and the prototype proves it. A turn interrupted mid-tool by a Durable Object
eviction, a crash or a redeploy resumed on its own and finished. But the move
does not fix the failure the tick was opened for. When a container dies, the
workspace dies with it: the clone, the uncommitted edits, the build cache. A
DO-hosted loop keeps only the transcript. With umq landed, the main way a
container died mid-turn (the application-wide image rollout) is gone. In
exchange, the move would:

- swap the pi coding agent the local executor runs for a different harness
  (pi-durable plus tools we write ourselves);
- rebuild the worker contract in TypeScript;
- add a new interruption source (every Worker deploy restarts the agent's DO);
- rest on packages that call themselves experimental and are 1-3 days old.

The cheaper fix for "a container death loses the turn" is to persist the pi
CLI's own session and resume it in a fresh container ("Alternative" below).

## Question

Should a cloud worker run pi's agent loop durably in a Durable Object
(Agents SDK `PiHarness` + `@earendil-works/pi-durable` + `@earendil-works/pi-ai`,
announced 2026-10-02)? Under that design, the tools (shell, git, toolchains,
tests) would execute in our container (`FactorySandbox`, `durable_object`
scheduling policy). Today pi runs inside the container (`image/worker.sh`), and
a container death loses the in-flight turn. Only pushed branches survive.

## What PiHarness / pi-durable actually provide

Versions read: `agents` 0.26.0 (2026-10-02), `@earendil-works/pi-durable` and
`@earendil-works/pi-ai` 1.0.2 (2026-10-04), `@cloudflare/computer` 0.4.0
(2026-10-02). Sources: the Cloudflare docs (agents/harnesses/pi,
.../pi/extensions, agents/models/pi-ai), the package sources, the official
example (cloudflare/agents `examples/next/harnesses/pi`, including its
NOTES.md), and Earendil's posts (pi-durable, pi 1.0).

**Durability granularity.** pi-durable is a chain of tasks: one generation
task, then one task per tool call, then a tool-result entry, then the next
generation. Every state change is an atomic SQLite commit in the DO, in
tables prefixed `pi_`.

- **Partial model output and tool output:** committed at most every 100 ms
  (100 KiB/s throttle).
- **Interrupted model stream:** the stored partial becomes an aborted
  assistant entry, and the request is reissued.
- **Interrupted tool call:** its intent (arguments plus replay flag) was
  committed before `execute()` ran. On recovery it is rerun only if the tool
  says `replay: "safe"`. Otherwise the model gets an `interrupted` error
  result ("may have partially run") carrying the output so far.
- **The default is unsafe.** pi-durable's built-in `CodingTools` (read,
  write, edit, bash) declare no replay flag, so even `read` comes back as
  interrupted. This was filed as pi#10320 and closed "not planned".

**Resume after eviction.** `PiHarness` is a Lifecycle capability. While a
session has live tasks, a wake job keeps a heartbeat alarm every 30 s. Each
alarm waits up to 10 minutes inside its invocation (the alarm wall limit is
15 minutes), then hands off to a fresh alarm. A crashed or evicted object
restarts on the next heartbeat, and Pi resumes from its last commit. A deploy
is treated as a crash: in-flight work gets 30 s, then the alarm restarts the
object. Measured in the prototype (below), including with no client request.

**Tools.** Tools are extensions: plain objects `{ name, tools, sections }`
installed on a registry before `Harness.open()`.

- A tool is a `ToolRegistration` with typebox `parameters`, `replay`,
  `executionMode` and `execute(args, api, ctx)`.
- `api.output()` streams output, `api.details()` sends UI data, and
  `api.memo(name, candidate)` gives a durable first-writer-wins value per
  tool task. That makes it an idempotency key, the primitive a replay-safe
  container tool is built on.
- The cleaner seam is pi-durable's `ExecutionEnv` (a `FileSystem` plus a
  `Shell.exec`), which the built-in tools run against. Its README names "a
  container per conversation" as an intended backend.
- Hooks: `beforeTool` returns `{block}` or rewritten args before intent is
  recorded. There are also `afterTool`, `beforeRequest`, `afterResponse`,
  `afterTools` and `beforeCompact`.

**Streaming.** `session.events()` gives a snapshot followed by
batch-per-commit events. It is in memory only, with no cursor: after a
restart you open a new stream and get a fresh snapshot.

**Limits and gaps.**

- No max turns, budget or approval primitive; you build them from hooks and
  `pi.usage`.
- `abort()` waits for tools to honour their abort signal.
- No session deletion.
- A single model stream longer than 15 minutes can be cut off.
- Background tasks are noticed only on the 30 s poll.
- A running session never lets the object drain.
- The DO has 128 MB of memory, shared with the transcript and tool output.
- A tool's `execute` is an ordinary in-memory promise. There is no
  suspend-until-callback, so a long tool has two options:
  - be a replay-safe poller that reattaches to its job (prototyped below), or
  - return "started" and have the result submitted back into the session
    later.
- Open issues: agents#2456 (an RPC call before startup can deadlock startup),
  pi#10325, pi#10411 and pi#10357.

**Models.** `createAI` from `agents/models/pi-ai` needs the `AI` binding.

- `@cf/...` ids go through `env.AI.run`.
- Third-party models go through the AI Gateway universal endpoint.
- It has no HTTP transport and no token, so it cannot address our
  `/api/gateway` route.
- The clean way to keep D17 (run-scoped token, attribution stamped by the
  factory, kill switch, cost read from gateway logs) is pi-ai's own
  `createProvider` with an `openai-completions` model whose `baseUrl` is
  `<factory>/api/gateway/workers-ai/...` and whose apiKey resolves to the run
  token. This is the same wire the container's pi uses today
  (`harness_model_api="openai-completions"` in `image/common.sh`). If the DO
  lives in the factory Worker, the transport can be a service-binding
  `fetch`, avoiding the public hop. Using `env.AI` directly would bypass our
  budget, attribution and revocation, so it is not an option for runs.

**Maturity.** pi-durable's README says "Experimental. The API changes
without notice". `PiHarness` is `@beta` and "will likely change as Pi Durable
matures". Both pi packages and `agents` 0.26.0 are newer than this
repository's 3-day pnpm release-age gate. `cloudflare/pnpm-workspace.yaml`
exempts only `@cloudflare/*` and wrangler/workerd/miniflare, so adoption
would also mean exempting `agents` and `@earendil-works/*`.

## `@cloudflare/computer`

Its README says "PREVIEW ONLY … NOT suitable for production". It is a
SQLite-backed virtual filesystem owned by a DO (`workspace.fs`: read, write,
mkdir, readdir, grep, find, stat), plus `workspace.runtime.exec` on one of
several backends:

- a container running `computerd`, with the container-side VFS synced to the
  DO over a capnweb WebSocket and FUSE;
- a legacy container backend;
- `just-bash` in a Dynamic Worker;
- JavaScript in a Worker.

Git is opt-in isomorphic-git on the VFS. The README pitches it at
"agent-scale workspaces, not full monorepos", says heavy I/O is slow, and
gives about 10 GB as the limit.

The Pi example uses the Worker-JavaScript backend, with no container and no
shell. It wraps `createPiTools({ workspace })` into `ToolRegistration`s,
marking read/ls/find/grep/write/delete replay-safe and leaving edit and exec
unsafe.

It is a different thing from Sandbox SDK 1.0 / `ctx.container`: an
abstraction that can sit on top of a container, not a replacement for one.

**Our tool backend should not be `computer`.** A Go toolchain, `go test`,
real git, `tk`, and the boundary guard's PATH shim and pre-commit hook all
need a real Linux filesystem and processes. That is what `FactorySandbox`
already is. A custom computer backend means implementing its capnweb
`WorkspaceRPC`, which is heavy. pi-durable's `ExecutionEnv`, or our own small
tools over `FactorySandbox`'s seven methods, is the seam to use.

## Prototype (throwaway, staging only)

Setup:

- A throwaway Worker, `ticfac-n0b-spike`, with a `PiWorker` DO (plain
  `DurableObject` + `Lifecycle.install(this).use(new PiHarness(...))`).
- Model: `@cf/moonshotai/kimi-k2.7-code` through `env.AI`. That is acceptable
  for a spike only, per the Models section.
- Two tools, `sh` and `sh_once`, implemented over the **real
  `FactorySandbox`** of the staging Worker `ticks-factory-staging`, through a
  cross-script DO binding. The tool calls `startProcess`, polls `getProcess`
  and `readOutput` every second, and returns output plus exit code.
- `sh` is `replay: "safe"` *by construction*: it memoises the container
  process id with `api.memo`, so a replay reattaches to the same process
  instead of running the command twice.
- `sh_once` keeps the default (unsafe).

No production resource was touched. Nothing was committed. The Worker was
deleted after the spike.

Results (each run was a single tool call: `echo run >> runs.log; 30 × (echo
tick; sleep 1); echo finished`, disrupted about 10 s in):

| Disruption mid-tool | Tool | Outcome |
|---|---|---|
| none (baseline) | `sh` | done in 3.7 s end to end (one model round, one tool call, one answer) |
| DO evicted (`ctx.abort()`) | `sh` (safe + memo) | **resumed**: tool replayed, reattached to the same container process, full output tick 1..30, `exit=0 replayed=true`; turn finished 26 s later |
| DO evicted, **no client request afterwards** | `sh` | **resumed by the heartbeat alarm alone**: the tool result committed 22 s after the eviction, before anyone looked |
| Worker **redeployed** mid-tool | `sh` | **resumed**: `exit=0 replayed=true`, turn finished |
| DO evicted | `sh_once` (unsafe) | model got `Tool sh_once was interrupted and may have partially run` with ticks 1..9; turn ended INTERRUPTED. **The container process kept running, orphaned** |
| **container destroyed** mid-tool | `sh` | the loop survived and the tool returned `lost (container gone)` with ticks 1..7, which the model reported. **The workspace was gone**: the next call booted a fresh, empty container |
| 17-minute tool call, untouched | `sh` | completed in one execution (`exit=0 replayed=false`), across the 10-minute alarm hand-off |

Latency, DO to container, with a warm container on the staging image:

- **From inside the agent's DO:** about 15-30 ms per `getProcess` or
  `readOutput` RPC, and 70-320 ms for `startProcess`.
- **Whole trivial tool call** (start, one poll, read), measured from the
  Worker: 118-188 ms, median about 140 ms.
- **First call on a cold container:** about 8.4 s (the start is recorded
  pending and the FactorySandbox alarm starts it).
- **Per-call overhead:** negligible next to a model round or any real
  command.
- **CPU:** the agent DO used 1-300 ms of CPU per 30 s alarm invocation.

Long tool call: a `sleep 1020` (17 minutes) with no client request during it
**completed in one execution**: `exit=0 replayed=false`. The answer was
committed at 17m04s. The poller's short RPCs and the harness's alarm hand-off
kept the object alive past the 15-minute alarm cap, with no replay needed.

## What our worker contract needs, and where each piece would live

What `image/worker.sh` + `common.sh` + `worker-dispatch.ts` +
`sandbox-dispatch.ts` guarantee today, and what a DO-hosted loop does to
each:

| Contract | Today (in the container) | DO-hosted loop |
|---|---|---|
| Boot faults: exit codes 2-8, 13-15 (#171/#178: clone, tk version, preflight, setup, model, harness, origin/gateway unavailable) | `worker.sh` boot, before the harness | Unchanged in shape: a `ticks-worker --boot` phase **in the container**, whose exit code the DO maps to the same classes. The harness probe becomes a model probe from the DO. |
| Report: `RESULT-<tick>.md` with STATUS, findings, tracker-edits; container facts prepended; committed and pushed; exit 9/10/11 | `worker.sh` after the harness | A `ticks-worker --finish` phase in the container, run by the DO when the session settles. Exit 9/10/11 still come from git facts. |
| Report linter pushback (#183) and the early-exit nudge (060) | re-prompt `pi --session-id` up to N times | **Better fit**: the DO runs `ticfac lint-report --pushback` in the container and `submit`s the pushback into the same session with an idempotent `operationId`. No process relaunch. |
| Boundary guard (no tracker writes): tk shim on PATH, pre-commit hook, sweep | PATH set around the harness | The shim and hook stay **in the container** (every `exec` gets the guard PATH); `beforeTool` can block the obvious `tk` writes as a belt. The command-string match is bypassable, so the container layers remain the enforcement. |
| Wall / budget / stuck watch | `TICKS_WORKER_TIMEOUT` bounds the harness; budget = gateway token revocation; stuck = the reconciler reading output progress through the dispatch door | Wall: DO deadline, then `abort()`, then finish. Budget: unchanged **only if** model calls go through `/api/gateway` on the run token (see Models). Stuck: the door's output cursor must be synthesised from pi's committed events. |
| Model per tier from the dispatch; harness from the dispatch | `TICKS_MODEL`/`TICKS_HARNESS` env, pi `models.json` override | The dispatch's model becomes the session model. "Harness" stops being a choice: the DO path is pi-durable only, so omp/claude workers would keep the container path. |
| Gateway-only credentials | the container holds only the run token | Kept, if the DO's provider uses the run token against `/api/gateway`. |
| Carried work (hn6), reclaim/adoption, `lost` is not terminal | git branch plus container identity; the state route reads the container's process | Carried work is unchanged (git). Reclaim is **re-designed**: attempt identity becomes the agent DO (pending ops, terminal result) plus its container, and the state route answers from the DO. |
| Same harness locally and in the cloud | `pi` CLI in both | **Lost.** The cloud agent would be pi-durable plus our tools, not the pi coding agent: different system prompt, tool set, AGENTS.md/skills loading and compaction. |

## What it fixes and what it does not

What it fixes:

- **DO-side interruptions** (eviction, crash, Worker deploy) no longer lose
  a turn. Proven above.
- **Linter pushback and nudges** become follow-up inputs into a live
  session.
- **The transcript** is queryable state rather than container stdout.

What it does not fix:

- **Container death mid-turn**, the tick's premise. The agent keeps its
  memory, but its hands come back empty. The workspace (clone, uncommitted
  edits, caches, running test) dies with the container either way.
- Continuing after one needs:
  - a re-provisioning step (clone the attempt branch at its last pushed
    head, re-run setup), and
  - a work checkpoint cadence (for example an `afterTools` hook that pushes
    a WIP commit to the attempt branch after each tool round). Even then,
    edits since the last checkpoint are lost.
- That checkpointing is the same work the cheaper alternative needs.

How often a container dies mid-turn, once umq lands: the rollout cause is
gone, because a deploy no longer touches a running container. What remains is
host failure, OOM and lifetime bugs, and we have no measured rate for those
yet.

## Costs and risks

- **Maturity:** the packages call themselves experimental and beta, with
  APIs that "change without notice". They were 1-3 days old at the time of
  the spike, and there are open startup/RPC deadlock and wait-cycle issues.
  The factory runs unattended, so an API break lands as a stalled run.
- **Two interruption sources instead of one:** the container can still die,
  and the agent's DO now restarts on every factory deploy (30 s grace). It
  recovers, but every unsafe tool in flight comes back as `interrupted`, so
  tools must be designed replay-safe (memo-keyed jobs).
  - `FactorySandbox.startProcess` would need to accept a caller-chosen
    process id (the runner already refuses a taken id with exit 4) to make
    that airtight. With the prototype's memo-after-start there is a small
    window where an eviction leaves an orphan.
  - Interrupted unsafe tools leave orphan processes in the container.
- **Harness drift:** the cloud agent stops being the agent we run and
  measure locally. Every prompt, profile and quality finding about pi would
  need re-validating on pi-durable plus our tools.
- **Scope:** `worker.sh` (about 1,565 lines of the contract) splits into
  boot and finish phases around a TypeScript supervisor. The dispatch door's
  state, reclaim and stuck surfaces move to a DO. The gateway path needs a
  pi-ai provider. That is epic-sized, comparable to umq.
- **Runtime cost:** small. The agent DO is pinned while a session runs: a
  heartbeat every 30 s and 128 MB of duration, measured at 1-300 ms CPU per
  30 s. The container runs throughout either way, so container cost is
  unchanged.
- **Long tools:** pattern-dependent. A tool that holds a single fetch open
  beyond about 15 minutes is at the platform's mercy. The replay-safe poller
  avoids this. A 17-minute poller call completed without replay.

## Alternative: make the in-container pi resumable

What survives a container death would then be the same as a DO-hosted loop
plus checkpoints, without changing harness. The steps:

1. `worker.sh` already names the pi session (`--session-id`).
2. After every harness exit, and periodically during the run, it copies
   pi's session JSONL out of the container to durable storage keyed by job
   id: an R2 object through a factory route on the run token, or a commit on
   a side ref.
3. It pushes a WIP commit of the work tree on the same cadence.
4. A re-dispatched attempt (the carried-work path) restores the session file
   and resumes `pi --session-id` on the restored branch, with a "the
   container was replaced; re-verify your last step" preamble.

Granularity is per appended message, which is coarser than pi-durable's
commits. It needs no new runtime, keeps local/cloud harness parity, and
reuses carried work. This is the recommended next step if container deaths
show up after umq. It is a single tick, not an epic.

## Recommendation

**Later.** Revisit pi-durable/PiHarness when both of these hold:

1. Container deaths mid-turn still happen often enough to matter after umq.
   Count settled attempts whose container stopped while the harness ran, and
   the cheaper resumable-session fix above has proved insufficient.
2. pi-durable drops "experimental" and `PiHarness` leaves beta, or at least
   holds its API steady for a few releases with the startup-deadlock issue
   closed.

If both fire, adopt it in this shape.

### Epic sketch (if adopted)

1. **FactorySandbox: caller-chosen process ids.** `startProcess(id, ...)`
   is idempotent, so a memo-keyed tool can never double-start. Small, and
   useful on its own.
2. **Container `ExecutionEnv` over FactorySandbox.** A TypeScript class
   implementing pi-durable's `FileSystem` + `Shell`: exec through the runner
   as a replay-safe poller, file ops through short execs. Tested against
   pi-durable's `MemoryStorage` and a fake container.
3. **Factory gateway provider for pi-ai.** An `openai-completions` provider
   over `/api/gateway/workers-ai` with the run token and a service-binding
   fetch, plus the GLM `compat`/`maxTokens` overrides `common.sh` carries.
   Prove that attribution and revocation hold.
4. **Split `worker.sh` into `--boot` and `--finish` phases.** Keep the
   current all-in-one mode as the default. The exit-code contract is
   unchanged and pinned by `contracts/worker-boot-contract.json`.
5. **`AgentWorker` DO (PiHarness) behind a substrate flag.** It runs boot,
   then the session, then lint/nudge follow-ups, then finish. It enforces
   the wall with `abort()` and settles a job record. The dispatch door's
   state, output and reclaim routes answer from it for `do_pi` runs only.
6. **Stuck-watch feed.** Synthesise the door's output cursor from pi's
   committed events, so the reconciler's stuck watch works unchanged.
7. **Container-death continuation.** Re-provision (clone the attempt
   branch, setup) and add a WIP-checkpoint `afterTools` hook. Inject a
   "workspace replaced" system section.
8. **Staging proof, then a real run.** Evict the DO and kill the container
   mid-tick on staging, then run an hn6-style cloud run on `do_pi` to close.

## Pre-existing problem found on the way

The staging Worker `ticks-factory-staging` was still running FactorySandbox
code from before #200. Every `startProcess` failed at once with "the
container stopped while starting: The container has not been started". That
is the inline cold-start wait #200 removed. The spike redeployed staging from
current code (`wrangler deploy -c staging/wrangler.toml`) to use it. Staging
is not deployed by CI, so it drifts behind main.
