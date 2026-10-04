# Spike n0b, round 2: pi-durable as ticfac's single worker harness

Tick `n0b` (pi-durable worker harness), round 2, 2026-10-04. Round 1 is
[n0b-pi-durable-harness.md](n0b-pi-durable-harness.md) (PR #207). It looked
at Cloudflare's `PiHarness`-in-a-DO packaging and recommended "later",
because a dead container still lost the workspace. Round 2 looks at
Earendil's own Pi Durable (`@earendil-works/pi-durable`,
github.com/earendil-works/pi `packages/durable`, MIT, experimental) as the
**one** harness for local and cloud workers. The harness runs beside the
orchestrator, the tools run in the worker container or worktree, and the
storage outlives both.

**Recommendation: adopt, as an epic behind a harness flag, with pinned
versions and our own contract tests in front of the experimental API.**

- **Storage:** DO SQLite per attempt in the cloud, local SQLite locally.
- **Workspace:** wip checkpoints of the workspace after every tool round.

The prototype did what round 1 could not: it killed the container mid-turn
and the turn still finished on restored files. It also showed the other
three behaviours the tick asked for:

- **Harness killed mid-tool:** a new process resumed from Cloudflare storage
  and reattached to the *same* still-running container process, with no
  rerun.
- **Container destroyed mid-command:** the workspace was rebuilt from the
  last wip checkpoint in about 26 s, and the model re-ran the command.
- **Second client:** it watched the run live and steered it, and kept
  watching across a harness restart.

Two costs are the reason this stays an epic behind a flag, not a swap:

- The packages are three days old and say "the API changes without notice".
- The cloud and local agent stops being the pi CLI we run and measure today.

## Question

Can pi-durable be ticfac's single worker harness, local and cloud? Under
that design:

- the harness is hosted beside the orchestrator (a local Node process, the
  orchestrator container, or a DO);
- tools run through an execution-environment adapter in the worker container
  (`FactorySandbox`) or a local worktree;
- storage lives somewhere that survives both the harness and the container.

The design should give us crash resume across processes, the same harness
in both places, and live watch/steer of worker conversations. That means
`ticfac watch` showing thinking and tool calls, f6o's heartbeat, and stuck
nudges as real steering.

The operator also asked how the Cloudflare stores compare as the storage:
D1, JSONL in R2, DO SQLite, and K2 for the live fan-out, against local
SQLite/JSONL.

## What pi-durable is (1.0.2, read 2026-10-04)

- **A harness is storage plus the machinery that runs conversations on it.**
  - Every model turn, tool call, queue change and streamed partial is an
    atomic commit, made *before* anything is shown.
  - Partials are throttled to one commit per 100 ms.
  - A new process that opens the same storage resumes every unfinished
    task from its last checkpoint (`harness.resume()`).
- **Tools declare `replay: "safe"` or default to unsafe.**
  - On recovery, a safe tool reruns.
  - An unsafe one gives the model an `interrupted` result carrying the
    output so far.
  - `api.memo(name, value)` is a durable first-writer-wins value per tool
    call. It is the primitive for idempotent tools.
- **Execution environments.** `env` builds an `ExecutionEnv` (a `FileSystem`
  plus a `Shell`) per call. The built-in `read`/`write`/`edit`/`bash` touch
  files and processes only through it. The README names "a container per
  conversation" as an intended backend.
- **Storage is an interface** of about 15 methods: `commit(writes)` plus
  keyed reads and scans.
  - It ships memory, SQLite and JSONL implementations.
  - The SQLite and JSONL cores are runtime-neutral and take an async
    `SqliteDatabase` facade or a `FileSystem`.
  - "One process owns a storage at a time; there is no cross-process
    locking."
  - A conformance suite (`/testing`) checks custom backends.
- **Watch and steer.**
  - `viewState()`/`watch()`/`watchEvents()` are in-process views of
    committed state.
  - `submit({ whenBusy: "steer" })` places input after the current tool
    round.
  - `requestId` makes a submission idempotent across restarts.
  - Cross-process clients go through `pi-server`/`pi-client`/`pi-protocol`,
    which are experimental and route to the process that owns the session.
- **Hooks** for the contract: `beforeTool` (block or rewrite), `afterTool`,
  `afterTools` (end of a tool round), `onYield` (continue the run with
  another user message), `beforeRequest`, `beforeCompact`.
- **Maturity.**
  - Versions 0.0.1 (2026-09-19) to 1.0.2 (2026-10-04), with 12 releases in
    15 days.
  - The README says "Experimental. The API changes without notice."
  - **The pi CLI itself does not run on pi-durable.** `pi-coding-agent`
    1.0.2 is built on `pi-agent-core`. Its pi-durable mode
    (`src/experimental/durable`, which renders pi's own system prompt
    sections on pi-durable) is excluded from the published package.

## Prototype (throwaway; staging and spike-only resources, all deleted)

### Architecture

```
 Mac: node harness.mjs  ── pi-durable Harness, Workers-AI model via a gateway route
   │  Storage = RemoteStorage ──WebSocket──▶ spike Worker ▶ StoreDO: pi-durable SqliteStorage
   │                                                         over ctx.storage.sql (DO SQLite),
   │                                                         epoch fencing, watcher fan-out
   │  ExecutionEnv = ContainerEnv ──HTTPS──▶ spike Worker ──cross-script DO──▶ STAGING
   │                                         FactorySandbox (ticks-factory-staging) container
   └  afterTools hook: wip commit + git bundle ──▶ R2
 Mac: node watch.mjs ──WebSocket (role=watch)──▶ the same StoreDO (reads commits, relays steers)
```

The prototype is about 990 lines: a Worker (331), the remote storage (111),
the container env and tool (344), the harness (144) and the watcher (57).
Its parts:

- **Spike Worker** `ticfac-n0b2-spike`, with a DO, a throwaway D1 database
  and a throwaway R2 bucket. It held its own bearer token as the only
  credential.
- **Model:** `@cf/moonshotai/kimi-k2.7-code`, through an OpenAI-compatible
  route on the spike Worker (`/gw/workers-ai/v1/chat/completions` onto
  `env.AI.run`, Workers AI models only).
  - The harness used a pi-ai `createProvider` whose `baseUrl` is that route
    and whose API key is the route's token. This is exactly the shape that
    would address the factory's `/api/gateway/workers-ai` with a run token.
  - No AI Gateway sat behind it: the available credentials cannot create
    one (see Pre-existing problems).
- **Storage, DO SQLite:** pi-durable's own `SqliteStorage`, unchanged, over
  a 25-line `SqliteDatabase` adapter for `ctx.storage.sql`.
  - `transaction()` maps to `ctx.storage.transaction()`.
  - The harness reaches it over one WebSocket. Every Storage method is
    forwarded, and ids are minted locally from the DO's next id.
  - Opening as owner bumps an epoch. Earlier owners are closed and their
    commits refused. A spot test confirmed a superseded owner's commit is
    refused.
- **Tools:** pi-durable's `read`/`write`/`edit`, run over a `ContainerEnv`
  (the `FileSystem` subset they use, as short container commands). A
  custom `bash` tool, `replay: "safe"`:
  1. memoises a nonce durably before it starts;
  2. writes the nonce into the container command;
  3. on replay, finds the process by nonce (`listProcesses`) and
     reattaches instead of starting it again.

  The tool needed no change to `FactorySandbox`.
- **Workspace durability:**
  - **Checkpoint:** an `afterTools` hook commits the work tree as `wip`
    and stores a `git bundle seed..main` in R2.
  - **Restore:** when a process vanishes, or the ready marker is missing
    in a fresh box, the env re-provisions (staging's image has no git or
    python, so `apt-get`), re-creates the deterministic seed commit,
    applies the bundle and resets.
- **The task:** a small Python repo with an off-by-one bug. The model
  fixes it, adds a `restock()` method with a test, runs `./check.sh` (a
  deliberately slow 25 s check) and writes `RESULT.md` with a
  `STATUS:` line.

### Results

| # | Experiment | Outcome |
|---|---|---|
| 1 | Baseline: harness on the Mac, storage in the DO, tools in staging | **Done.** 12 tool calls, 118 s end to end, of which 23.6 s was the first-boot provision. 238 commits, 487 storage round trips. $0.008 of model spend. |
| 2 | Same task, local SQLite storage (comparison) | **Done.** 11 tool calls, 77 s, of which 22 s was provision. |
| 3 | **`kill -9` the harness 6 s into `./check.sh`**; start a new process | **Resumed.** The new process opened storage (epoch 2) and, 1.3 s after starting, logged `REATTACH to container process 3a63cc34`, the *same* process. The check finished, and the turn wrote `RESULT.md` and settled `done`. No command ran twice, and nothing was lost. |
| 4 | **Destroy the container 8 s into `./check.sh`** (mid-tool, mid-turn) | **Turn continued.** The poll found the process gone within 1 s. The env restored a fresh container to checkpoint `673d34a wip: tool round` (both edits) in 25.8 s: about 10 s cold boot, about 12 s apt provision, then seed and bundle. The model got "container lost mid-command; workspace restored to …; re-check and re-run", re-read the files, re-ran `./check.sh` (pass) and finished. Edits since the last tool round would have been lost; there were none. |
| 5 | **A second client watches and steers**; the harness is killed mid-tool while it watches | **Steered and kept watching.** The watcher saw every commit live: user, assistant tool calls, tool results. On the first `edit` call it sent "also give restock() a docstring…", and the steer travelled as follows: <ul><li>reached the owner 33 ms after it was sent;</li><li>was submitted durably 0.3 s later;</li><li>was placed after the running tool round 2.4 s later (seq 70).</li></ul> The model then edited the docstring in. The harness was then `kill -9`'d mid-`./check.sh`, and the *same watcher socket* saw `leave owner harness-38560`, then `join owner harness-9656 epoch=2`, then the reattached result and the final answer. The final `RESULT.md` mentions the docstring. |

The per-commit latency of each store is in the comparison below.

What the experiments did **not** cover:

- a harness hosted in a DO (round 1 covered that: eviction, redeploy and
  17-minute tools);
- a real ticfac worker contract;
- a long tick;
- a Go-sized repo.

Restore time is dominated by staging's bare image. The factory image has
the toolchains baked in, so there the restore would be a cold boot plus a
clone of the attempt branch.

## Storage comparison

The latency numbers replay the exact commit log of run 1 (238 pi-durable
commits, 101 KiB, p50 271 B, p90 975 B, max 3.3 KiB) against each store.
They are **per-commit latency as the committer sees it**.

- **Cloudflare stores** were timed from inside the spike Worker, the place a
  cloud harness or its storage front would sit, next to a no-op baseline
  for the same hop.
- **Local stores** were timed on this Mac, which is a shared, busy host.

| Store | Fits pi-durable as is? | Durability of an acknowledged checkpoint | Write latency per checkpoint (measured) | Cost (Workers Paid list prices) | Size limits | Multi-reader watch / steer |
|---|---|---|---|---|---|---|
| **Local SQLite** (`openNodeSqliteStorage`, WAL, `synchronous=NORMAL`) | **Yes**, shipped | Survives a process crash. The newest commit can be lost on power or host failure. One disk. | **p50 0.1 ms**, p99 1.4 ms | free | disk | Same process only (`watch()`), or other processes read the WAL database read-only and poll. Steering must go through the owning process. |
| **Local JSONL** (`openNodeJsonlStorage`) | **Yes**, shipped | Append-only. `fsync: true` flushes before each commit marker. | p50 1.1 ms (no fsync) / **10 ms (fsync)**; p99 109 / 141 ms on this busy host | free | disk, but **the whole log is loaded into memory on open** | Other processes can `tail -f` the files. Steering needs the owner. |
| **DO SQLite** (pi's `SqliteStorage` over `ctx.storage.sql`, 25-line adapter) | **Yes**, through the adapter; the pi code is unchanged | Acknowledged only after the write is durably stored and replicated by the platform (output gate). Survives harness and container death. | Worker→DO: **p50 29 ms** / p99 80 ms, against a no-op RPC of p50 20 ms, so the write itself is about 9 ms. Mac→DO over WebSocket in the run: p50 41 ms, p90 115 ms. A harness *inside* the DO pays no hop at all. | Rows written $1/M after 50M/month included. A commit writes about 4-6 rows (record, id index, revision, metadata), so roughly 1-1.5k rows for a 2-minute run and a few tens of thousands for a long tick. Duration only while the harness talks to it (hibernatable WebSockets). | 10 GB per DO, 2 MB per row. One DO per attempt, so per-attempt limits only. | **Native.** Hibernatable WebSockets fan each commit out to every watcher (prototype). Steers relay to the owner, which submits them durably. Epoch fencing gives the single-writer rule pi-durable assumes. |
| **D1** | **No.** pi's SQLite core reads *inside* the commit transaction (metadata, id uniqueness, document revisions); D1 has only an atomic `batch()` of pre-bound statements. It needs a new Storage implementation, or a single-writer front that keeps that state in memory. | Durable on acknowledgement, replicated. | Worker→D1 batch of the same writes: **p50 39 ms** / p99 110 ms, against `SELECT 1` p50 27 ms, so the write itself is about 12 ms. **Every read is another query** (about 27 ms), and the run made about 250 reads. | Rows written $1/M after 50M included; storage $0.75/GB-month. | 10 GB per database, 2 MB row, 100 KB statement | None. Watchers poll. |
| **R2 JSONL** | **No.** The JSONL core needs append, truncate and flush. R2 has no append, so the choice is one object per commit (segments) or rewriting the whole log per commit (quadratic bytes). Reopening means list plus read every segment into memory. | Durable on acknowledgement. | **Per-commit segment p50 186 ms**, p99 402 ms. Whole-log rewrite p50 199 ms and growing. | Class A $4.50/M, so 238 puts is about $0.001. Storage $0.015/GB-month. | 5 GB per put | None. Event notifications go through Queues, which take seconds. |
| **K2 streams** (beta) | **No.** It is a log, not keyed state, so it cannot serve pi-durable's reads. | R2-backed. | **Not measured:** the wrangler OAuth login lacks the `k2.read`/`k2.write` scopes and the API token is refused. Documented: produce p99 about 1 s. | $0.04/GB produced, $0.04/GB consumed, $0.02/GB-month; free in beta | 30 MB/s per stream, 7-day default retention, 10 GB per account in beta | **Built for fan-out** (subscriptions, many consumer groups), but too slow per checkpoint for live token streaming. |

Reading the table:

- **DO SQLite is the store.** It is the only Cloudflare store that runs
  pi-durable's own storage code unchanged and atomically, with no extra hop
  if the harness lives in the same DO. It is also the only one with native
  push to many readers, and a DO is single-instance by construction, which
  is exactly pi-durable's ownership model.
- **D1 is a good *mirror*, not the primary.** Copying settled entries into
  D1 gives a cross-attempt, SQL-queryable history (analytics, `ticfac
  status`). As the primary it would mean writing our own Storage and paying
  a query per read.
- **R2 is the archive.** Use it for the end-of-attempt transcript export and
  workspace bundles (the prototype used it for those). At 186 ms+ per
  commit and with no append, it is not the checkpoint store.
- **K2 belongs to the run feed (n0r), not the harness.** Publish settled
  events (tool start/end, assistant messages, STATUS) from the DO for
  remote, offline and many consumers. Keep token-level live watch on the
  DO's WebSockets.
- **Locally, local SQLite is the obvious choice.** It is 400x faster than
  any remote store, and `ticfac watch` can read it read-only.
  JSONL loads the whole log into memory on open.

**Round-trip count matters more than per-commit latency.** pi-durable made
487 storage calls in a 12-tool run: 238 commits and 249 reads. From the Mac
to the DO that is about 21 s of storage round trips over a 95 s run, much
of it overlapped with model streaming. The comparable local-SQLite run took
55 s, with model variance in both. So:

- **The harness should sit next to its storage.** In the cloud that means
  inside the attempt's DO (0 hops) or in a Cloudflare container (about
  20-30 ms per hop).
- **The local harness should use local storage**, not a Cloudflare store
  across the internet.

## Where the harness lives

| Host | Storage | Tools | Survives | Notes |
|---|---|---|---|---|
| **Cloud: a `WorkerAgent` DO per attempt** (recommended) | its own DO SQLite (0 hops) | `FactorySandbox` via DO RPC (15-30 ms per call, round 1) | DO eviction, crash, redeploy (round 1); container death (round 2); orchestrator container death | Single instance per attempt, so no fencing problem. Watchers attach to the same DO. 128 MB memory; every factory deploy restarts it, with resume proven. |
| Cloud: Node sidecar in the orchestrator container | an attempt DO over WebSocket (prototype shape, about 30 ms per call) | `FactorySandbox` via the factory Worker | harness death and container death (prototype) | Needs the epoch fencing the prototype built. Dies with the orchestrator container and resumes in the next one. |
| **Local: Node process spawned by the Go executor per worker** | local SQLite file in the run directory | pi-durable's `NodeExecutionEnv` in the worktree (shipped) | harness crash (resume on next spawn); there is no separate container to lose | `ticfac watch` reads the SQLite read-only, or attaches to a local socket for steering. |

It is **one harness**: one TypeScript package (extensions, tools, contract
hooks, gateway provider) written runtime-neutral. pi-durable itself has no
Node-only APIs, and the env and tools use only `fetch`. It gets two hosts
and two `ExecutionEnv`s. Only the host, the storage backend and the env
adapter differ between local and cloud.

## The worker contract on pi-durable

Today the contract is `image/worker.sh` plus `common.sh`, `worker-dispatch.ts`
and `sandbox-dispatch.ts`.

| Contract | On pi-durable |
|---|---|
| **Boot faults, exit codes 2-8, 13-15** (clone, tk version, preflight, setup, model, harness, origin/gateway unavailable) | Container-side checks (clone, tk, preflight, setup) stay a `worker.sh --boot` phase. It is the env's first command, and its exit code maps to the same classes. The model and harness probes become a tool-less generation from the host. The codes stay pinned by `contracts/worker-boot-contract.json`. |
| **Report** `RESULT-<tick>.md` with STATUS, findings and tracker-edits; container facts prepended; committed and pushed; exit 9/10/11 | The model writes the report with tools, as today. A `worker.sh --finish` phase, run by the host once the submission settles, prepends container facts, commits and pushes. Exit 9/10/11 still come from git facts. |
| **Report linter pushback (#183), early-exit nudge (060)** | **Better fit:** an `onYield` hook runs `ticfac lint-report --pushback` through the env and returns `{ continue: pushback }`. That is the same conversation, durable, with no relaunch. |
| **Boundary guard** (tk shim on PATH, pre-commit hook, sweep) | It stays in the container. Every tool command runs with the guard PATH, and a `beforeTool` hook blocks the obvious `tk` writes as a belt. The container layers remain the enforcement. |
| **Wall / budget / stuck watch** | <ul><li>**Wall:** a host deadline, then `abort()`, then the finish phase.</li><li>**Budget:** unchanged, because the provider calls `/api/gateway/workers-ai` with the run token (the prototype used exactly this shape), so revocation stops the next request.</li><li>**Stuck:** progress is the commit sequence and live tool output in storage, read by the door. A stuck nudge becomes a real steer, placed after the current tool round (experiment 5).</li></ul> |
| **Tier model from dispatch** | `configure({ model })` per conversation from the dispatch. GLM's `maxTokens`/`thinkingFormat` overrides move from `models.json` into the provider's model entries. |
| **Gateway-only credentials** | **Stronger:** the harness, outside the container, holds the run token. The container, where untrusted code runs, needs **no model credential at all**. |
| **Carried work (hn6)** | The wip checkpoint per tool round is pushed to the attempt branch instead of an R2 bundle. That is the same mechanism hn6 carries work with, now at tool-round granularity. The transcript can be carried too (`fork` or `reset(handoff)`). |
| **Reclaim / adoption; `lost` is not terminal** | An attempt's identity is its storage (a DO id or a SQLite file) plus its container name. Reclaim means opening the storage as the new owner (epoch fencing) and calling `resume()`. A replay-safe `bash` reattaches to processes that are still running, and a lost container is restored from the last checkpoint. |
| **f6o heartbeat; `ticfac watch`** | Commits are the heartbeat. Watchers see thinking, tool calls and tool output from the commit stream. |
| **Harness choice** (`pi` / `omp` / `claude` from the dispatch) | It collapses to pi-durable for Workers AI and pi-ai models. omp retires. The local `claude` CLI harness (the strongest local option for hard ticks) cannot be hosted by pi-durable. It stays a separate, local-only harness or is dropped: an operator decision. |

## What it fixes, and what it costs

What it fixes:

- **Container death mid-turn** (the tick's premise). The turn continues,
  minus edits since the last tool round. Proven by experiment 4.
- **Harness death mid-tool.** The tool is not rerun: the new harness
  reattaches to the running process. Proven by experiment 3.
- **Live watch and steer from any client**, independent of which process
  hosts the harness. Proven by experiment 5.
- **Linter pushback and nudges** become conversation inputs instead of
  process relaunches.
- **The container holds no model credential.**

What it costs:

- **API churn risk.** The package went from 0.0.1 to 1.0.2 in 15 days and
  says it changes without notice. Mitigations:
  - pin exact versions;
  - run the storage conformance suite and our own replay tests in CI;
  - wrap pi-durable in one ticfac harness package, so churn lands in one
    place;
  - since it is MIT and about 15k lines, it can be frozen or vendored if
    upstream moves away from us.
- **Harness drift.** The agent stops being the pi CLI we run and measure.
  Its prompt, tool set, AGENTS.md and skills loading, and compaction all
  differ. pi's unpublished durable mode shows pi's prompt sections can be
  rendered on pi-durable, so we can port them. Even so, every prompt and
  quality finding about pi needs re-validating on the new harness, and the
  epic must include an A/B on real ticks.
- **Scope.** `worker.sh` (about 1,565 lines) splits into boot and finish
  phases around a TypeScript host. The dispatch door's state, output and
  reclaim surfaces answer from the attempt's storage. That is epic-sized,
  comparable to umq.
- **Release-age gate.** `cloudflare/pnpm-workspace.yaml` would need to
  exempt `@earendil-works/*`.
- **Runtime cost:** small. DO rows and duration come to fractions of a
  cent per attempt, next to model spend. The container runs either way.

## Recommendation

**Adopt, as an epic behind a `harness=pi-durable` flag.** Keep the pi-CLI
worker path until the flag has carried real cloud and local runs. The
shape:

- **Cloud:** a `WorkerAgent` DO per attempt hosts the harness on its own DO
  SQLite. Tools run in the attempt's `FactorySandbox` through a replay-safe
  `ExecutionEnv`. Watchers attach to the DO.
- **Local:** a Node harness process per worker, spawned by the Go executor.
  It uses local SQLite in the run directory and `NodeExecutionEnv` in the
  worktree.
- **Workspace:** wip commits to the attempt branch after every tool round,
  with restore on container loss.
- **Event fan-out:** the DO's WebSockets for live watch, and K2 or the run
  feed (n0r) for settled events. D1 is an optional queryable mirror and R2
  the archive.

### Epic sketch (PR-sized steps)

1. **Harness package skeleton.**
   - A `harness/` TypeScript package (pnpm) that pins exact `pi-durable`,
     `pi-ai` and `chord` versions, with the release-age exemption.
   - CI runs pi-durable's storage conformance suite against a
     `DurableObjectSqlite` adapter (vitest pool workers).
   - CI also runs a faux-provider transcript replay test, so API churn
     fails CI rather than a run.
2. **Gateway provider.** A pi-ai provider over
   `/api/gateway/workers-ai` with the run token, carrying the GLM
   `maxTokens`/`thinkingFormat` overrides. Prove on staging that
   attribution holds and that a revoked token stops the next request.
3. **Execution environments.**
   - `FactorySandboxEnv`: the `FileSystem` subset over short commands, plus
     a replay-safe tracked `bash` (nonce memo, then reattach through
     `listProcesses`) with the guard PATH.
   - A `FactorySandbox.run` door (start, wait and read in one RPC).
   - Local: `NodeExecutionEnv` plus the same guard.
4. **Workspace checkpoints.** An `afterTools` wip commit pushed to the
   attempt branch, and a restore on a lost container (clone the branch,
   check out the wip commit, then setup). This reuses hn6's carried-work
   code.
5. **Contract as hooks and phases.**
   - Split `worker.sh` into `--boot` and `--finish` phases, keeping the
     all-in-one default.
   - `onYield` linter pushback and the early-exit nudge.
   - Wall deadline via `abort()`, with exit codes unchanged and pinned.
6. **Cloud host.** A `WorkerAgent` DO (harness plus DO SQLite plus
   hibernatable watch/steer WebSockets) behind the flag. The door's state,
   output and reclaim routes answer from it for flagged runs.
7. **Local host.** The Go executor spawns the Node harness per worker on
   local SQLite. `ticfac watch` reads it, and the stuck nudge becomes a
   steer.
8. **Watch surfaces.** `ticfac watch` renders thinking and tool calls from
   the commit stream, with an operator `steer` command and the f6o
   heartbeat from commit sequence. Optionally, publish settled events to the
   run feed (n0r, K2).
9. **Proof, then flip.**
   - On staging: kill the host mid-tool, destroy the container mid-turn,
     deploy mid-run.
   - One hn6-style cloud run and one local epic run on the flag.
   - An A/B of tick outcomes against the pi-CLI path.
   - Then flip the default and delete the pi-CLI worker path.

## Pre-existing problems found on the way

These are surfaced for a decision. None blocked the spike.

1. **The staging image cannot run a worker-like task.**
   `cloudflare/staging/Dockerfile` is debian-slim with no git, go or
   python, by design for umq's deploy rehearsal. Any staging proof of
   worker behaviour (this spike, epic step 9) must provision at boot (about
   12 s of `apt-get` here) or use the factory image. Possible fix: a
   `staging` image flavour built from `image/`.
2. **No credential this host holds can reach K2 or create an AI Gateway.**
   - The wrangler OAuth login lacks the `k2.read`/`k2.write` scopes
     (wrangler says so and asks for `wrangler login`).
   - The API token in `~/.config/cloudflare/env` has neither K2, AI nor AI
     Gateway permission.
   - So K2 could not be measured and the spike's model route could not
     sit behind an AI Gateway.
   - Fix: re-run `wrangler login`, an operator step.
3. **Staging is deployed by hand and drifts behind main.** Round 1 noted
   this. It was redeployed from main again for this spike.
4. **The spike left a little staging state behind.** Six `FactorySandbox`
   DO instances on staging (`n0b2-*`) keep a few KV keys after `destroy()`.
   There is no per-instance delete. The keys are harmless; noted for
   completeness.

Throwaway resources were created and deleted after the spike: the
`ticfac-n0b2-spike` Worker with its DOs, the `ticfac-n0b2-spike` D1
database and the `ticfac-n0b2-spike` R2 bucket (emptied, then deleted).
Their containers on staging were destroyed. Nothing touched the production
factory.
