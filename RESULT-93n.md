# 93n — Live activity excerpt per running worker, local and cloud

## What changed

This attempt continued from a cancelled predecessor. Its commit (`70bf95dc1`) already
carried the cloud half of the tick: a cloud worker's activity read from the factory's
watch socket (`cloudWorkerActivity`, bounded to the first events frame), the cloud census
taken from the checkpoint's own word about which ticks it calls dispatched
(`cloudStandingAttempts`), the `RemoteActivity` source seam in the status model, the
terminal dashboard's cloud-invisibility line replaced, and `subprocess.RedactCredentials`/
`BoundLine` exported so every reader of a worker's conversation redacts through one
pattern set. Its tests were green.

On top of that, this attempt completed the local half and finished the seams:

- **Local pi-durable workers get the line.** A local pi-durable worker keeps no session
  transcript its worktree could name (its conversation is its own `worker.sqlite`), so the
  transcript reader answers nil for it and the activity line was permanently null. Now
  `decorateWorkers` falls through to `RemoteActivity` when the transcript says nothing,
  and `localWorkerActivity` answers through the worker's own watch door — the same Unix
  socket `ticfac watch <run> <tick>` opens (found via the same state roots the handle and
  runner readers walk; `reportStateRoots` is now the exported `statusmodel.ExecStateRoots`).
  A claude worker's transcript answers first and the door is never opened.
- **One dispatcher for both hosts.** `workerActivity(ctx, client, runID, tickID, attempt)`:
  nil client reads this machine's doors, a factory client the cloud sockets. Wired into the
  local `ticfac status --json` path (which the predecessor had left unwired), the cloud
  status path, the overview's full rows, `watchGatherModel`, and the watch's live loop —
  where the 5 s per-(tick, attempt) activity cache caps one socket dial per worker per TTL,
  now serving local and cloud alike.
- **The excerpt is the worker's newest act** (the tick description's "latest tool call …
  or latest assistant sentence"): whichever of the last tool call and the last assistant
  sentence the stream stamps later wins, ties to the tool call (a concrete act is the
  better excerpt of a same-stamp turn). Implemented in the transcript reader
  (`subprocess.TranscriptEvents.LastText/LastTextAt`, assistant lines only — a tool
  result's output and a person's typed message are neither the worker's words nor its act)
  and in the watch-fold reader (`lastSentenceIn` over `workerview` items).
- **Redaction everywhere the line goes.** Both new paths cut and redact through the one
  exported pattern set before the line is built; tests pin a token exported into a command,
  a URL carrying its credential in userinfo and query (stripped, the benign URL kept —
  tick ghh's interpretation of "no token or URL can appear"), and a token the model echoed
  in its own sentence.
- **The status pusher's snapshot gathers with the same reader**, so the model it pushes is
  the model `ticfac status --json` gathers (its own documented contract) — a local run's
  phone page now carries the pi-durable workers' lines.
- **The phone page's cloud line is gone.** `cloudflare/src/phone.ts` renders
  "workers: no census could be taken" for a null census — the same words the terminal
  dashboard has used since the predecessor's commit — instead of "workers run in the
  cloud — not visible from here". Its test's fixture comment updated to match the new
  truth (a cloud run's census is the checkpoint's word).
- **File rename for honesty:** `internal/cli/worker_cloud_activity.go` →
  `worker_activity.go` (+ its test), since the file now holds both halves.

Files: `internal/cli/{worker_activity,worker_activity_test,status_model,status_model_test,watch,overview,status,statuspush}.go`,
`internal/statusmodel/{activity,activity_test,build,report,handle,runner}.go`,
`internal/exec/subprocess/{activity,activity_test}.go`, `cloudflare/src/phone.ts`,
`cloudflare/test/phone-page.test.ts`. Committed as `026a6288b` on
`tick/ymf/attempt-2/93n`.

## What I ran

- `make gate` — exit 0 (the whole-repo short suite: gofmt, vet, 48 packages green,
  including `internal/cli` 49 s fresh and `internal/exec/subprocess` fresh in an earlier
  pass; `internal/statusmodel`, `internal/workerview` green).
- The `.tick/runners.toml` `ts` cell (`cd cloudflare && pnpm install … && pnpm lint &&
  pnpm contracts:check && pnpm exec tsc --noEmit`) — exit 0 (Biome clean, contracts ok at
  bundle 2.3.0, tsc clean).
- `npx vitest run` in `cloudflare/` — 80 files, 1880 tests, all pass.
- Targeted: `go test ./internal/cli/ ./internal/statusmodel/ ./internal/exec/subprocess/
  ./internal/workerview/ -count=1` — all ok.

## Acceptance, item by item

- **"each running tick carries an activity line with its age, local and cloud"** — the
  model carries `Activity.LastAction`/`LastActionAt` per standing worker; the workers
  panel renders the age (`dashWorkerLast`). Local: claude via its transcript, pi-durable
  via its watch door. Cloud: the checkpoint census names the standing workers and
  `RemoteActivity` reads each one's factory socket. Proven at three layers: the fold
  (`TestActivityFromSnapshot*`), the door (`TestLocalWorkerActivity*` with the real-socket
  `workerStandIn`), and the model wiring (`TestActivity*` in statusmodel,
  `TestStatusModelLocalWiringPassesTheDashboardReaders` extended for the fallback reader).
- **"tested with recorded streams"** — `internal/workerview/testdata/local-watch.jsonl`
  (the harness node suite's capture of a live local worker) drives the fold-level test and,
  through the real Unix-socket stand-in, the local-door test; the transcript tests use
  real claude-line shapes.
- **"no token or URL can appear"** — one redaction pattern set for every producer of the
  line; tokens and credential-bearing URLs are stripped before the 80-rune bound, tested
  on the cloud, door and transcript paths.
- **"make gate"** — exit 0.

## What the next tick has to know

- **The cloud pusher is the one remaining census gap** (filed as a finding): it builds the
  pushed cloud model through `localStatusModelHosted`, whose worktree census is empty for a
  cloud run (the container creates no worktrees — `internal/exec/cloudflaresandbox/
  executor.go` says so), so the phone page for a cloud run lists no worker rows at all
  even though the status CLI and the watch now render the checkpoint census with activity.
  The fix is `statusSnapshotTrimmed` building through `cloudStatusModel` for host cloud —
  the pusher already holds the factory client.
- **The pinned contract's census prose is now stale** (filed as a contract-change finding):
  `contracts/status-model.json` still says a cloud run's workers read null because its
  worktrees are not on this machine. The schema is fine (nullable array); only the two
  prose cells need the new truth.
- The activity snapshot read is bounded at 3 s per worker and nil-safe everywhere: a worker
  between processes, a door that refuses, a socket that answers nothing — all render as
  null activity, never as a stall or a guessed line.
- Herdr-substrate workers: claude kinds are covered by the transcript reader as before;
  codex/opencode transcript layouts were deliberately never read (their rollouts are not
  addressed per-worktree), and the herdr pi kind is deleted — nothing here changes that.
- The excerpt ties at equal stamps to the tool call; the recorded fixture's final assistant
  sentence shares its model call's stamp with the write before it, so the fold-level
  recorded-stream test still expects `write: RESULT-hpk.md`. The sentence preference is
  pinned separately with distinct stamps.

```findings v2
[
  {
    "kind": "contract-change",
    "title": "status-model contract still says a cloud run's census cannot be taken",
    "severity": "medium",
    "body": "Tick 93n takes a cloud run's workers census from the checkpoint's own word about which ticks it calls dispatched, so 'workers null means the census could not be taken (a cloud run)' is no longer the truth: null now means only that the records the census reads could not be read. The schema is unaffected (still an array-or-null); the two prose cells need the new statement so a close-out reading the contract does not hold the behavior against it. Exact edits, with the bundle version and changelog bump the ticfac-owned bundle's convention asks for: (1) in the checked_beyond_schema census clause, replace 'workers null means the census could not be taken (a cloud run), workers [] means no attempt stands' with 'workers null means the census could not be taken (the run's records could not be read), workers [] means the census read and no attempt stands - a cloud run's census is its checkpoint's own word about which ticks it calls dispatched'; (2) replace records.workers.description with 'One entry per live worker. Null when the census could not be taken (the run's records could not be read); empty when it read and nothing stands. A cloud run's census is its checkpoint's own word about which ticks it calls dispatched.'",
    "evidence": "contracts/status-model.json line 63 (checked_beyond_schema census clause) and the records.workers.description cell (lines 365-369)"
  },
  {
    "kind": "proposal",
    "title": "Cloud run's pushed status model carries no workers census at all",
    "severity": "medium",
    "body": "statusSnapshotTrimmed builds the pushed model through localStatusModelHosted for both hosts. For a cloud run the orchestrator container creates no worktrees, so its worktree census is empty and the pushed model says workers [] - the phone page lists no worker rows, while 'ticfac watch run_<id>' now renders the checkpoint census with per-worker activity. The pusher's own contract says it pushes the same model the watch builds; the fix is to build through cloudStatusModel for host cloud (the pusher already holds the factory client), or at minimum to feed the checkpoint census into the pushed gathering.",
    "evidence": "internal/cli/statuspush.go:333 (localStatusModelHosted with a worktree census); internal/exec/cloudflaresandbox/executor.go:32 ('this executor creates no worktree')"
  }
]
```

## Status

STATUS: DONE
