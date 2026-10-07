<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-23/qzg`, base `0f60b5a8547c5d7d2e0e4c62e9b3ab485e6a6c46`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `0f60b5a8547c5d7d2e0e4c62e9b3ab485e6a6c46` is the head of the work it continued, which was cut from `b05b072d262ad98d464c676027ed9c2349606408`; its work commits are counted from the carried head._

# qzg — the mid-command restore reaches the host's log

## The defect, as filed

`containerLost` — a tracked bash whose process is gone, or ended with no exit
code — restored the workspace and told only the MODEL which sha it rebuilt
from: the sha went into the `ExecutionError` message the model reads, and the
WorkerAgent host's own log stream never said a line for it. The operator
watching the say-stream read a mysteriously slow round and then a model that
knew something they did not. The `FactorySandboxEnvOptions.onRestore` ear dbi
wired for the nonce path did not fire from this path, so the restore existed
in the transcript and nowhere an operator watches.

## What changed

**`harness/src/env/factory-sandbox.ts`** — `containerLost` now fires the ear,
for a restore that succeeded and for one that failed alike. The callback grew a
second argument, `RestoreCause` (`"nonce-replay" | "mid-command"`, exported from
the package index), naming which of the env's own paths performed the restore:
the nonce path now says `"nonce-replay"` (behaviour unchanged otherwise), the
mid-command one says `"mid-command"`. Without the cause, the host's line for
the two paths could not say which loss the operator was hearing — and the
nonce path's wording ("a tracked bash found a fresh container") would have been
a lie about a container that was lost mid-command. The no-workspace-git case
still announces nothing: no restore happened there, matching the nonce path's
`{kind: "ready"}` case.

**`harness/src/host/worker-attempt.ts`** — the env's ear now goes through one
builder, `restoreSaid()`, so the restored and the failed case keep the same
shape per cause. Each line is spelled out in full, deliberately: the nonce
line's text is unchanged byte for byte, and the new mid-command line is
`the container was lost mid-command; the workspace was restored to <sha>
(<subject>)` (failed: `… and the workspace could not be restored: <error>`).
The between-rounds line (the checkpoint extension's own ear) and the
pre-finish one are untouched.

**`docs/pi-durable-cloud-run-runbook.md`** + **`internal/cli/
runbook_fault_evidence_test.go`** — the [A2] fault-2 observation criteria cited
two restore lines; the mid-turn destroy that fault injects most often lands
under a tracked bash, which is exactly the path that until now said nothing.
The criterion now names all three, the Go guard pins the new line against both
the host's source and the runbook, and the guard's own comment says three.
`make gate` caught this on my first draft (the guard failed on the reworded
nonce line), which is how the runbook gap was found — the guard's stated
purpose, working as designed.

**`harness/README.md`** — one sentence on the env's own restores announcing
through the ear, where the person wiring a host looks.

## Test-first evidence

Written failing, then made to pass. At base (before the change), in
`harness/test/workspace-checkpoints.test.ts`:

- "tells the host which sha the mid-command restore rebuilt from" failed:
  `expected [] to deeply equal [{outcome: …, cause: "mid-command"}]` — the ear
  never fired;
- "tells the host a mid-command restore that failed, and still fails the
  command" failed: `restores.length` was 0;
- the nonce-path test failed on the new second argument: `cause: undefined`.

In `harness/test/worker-attempt-host.test.ts`, "says which sha a mid-command
loss restored the workspace from" failed at base on exactly the tick's claim:
a whole attempt whose bash is lost mid-command settles 0, the restore runs,
the model is told the sha — and `lines` (the host's own log) carried no
mid-command line. After the change it carries exactly one, with the sha and
the wip subject, and neither the between-rounds nor the nonce path's line.

## What I ran

- `npx vitest run test/workspace-checkpoints.test.ts` — 9 passed (was 3 failed
  at base).
- `npx vitest run test/worker-attempt-host.test.ts` — 26 passed (1 failed at
  base).
- `pnpm test` in `harness/` — both halves: workerd 100 passed, node 56 passed.
- `make gate` — **green on the final tree** (exit 0, 47 packages ok; gofmt and
  vet clean). It took four runs: the first failed on the runbook-parity guard
  in `internal/cli` — the reworded nonce line — fixed above; the second
  failed `internal/factory`'s `TestWranglerDeployOutputIsStreamedAsItIsWritten`,
  a wall-clock streaming test that shares no code path with this change,
  passes alone (`-count=3` green, 0.05s) and is named by no `.tick/issues/`
  entry — a host-load flake, not this tree; the third and the fourth (on the
  committed tree, after the last runbook wording) were green.
- `make ts-gate` — green (biome, contracts:check, tsc --noEmit).
- `pnpm test` in `cloudflare/` — 80 files, 1870 tests passed (the suite that
  links this harness package).
- `go test -short -run TestTheCloudRunbooksA2FaultEvidenceMatchesTheHarnessHost
  ./internal/cli/` — passes.

## What the next tick has to know

- The three container-loss restore lines are now pinned on BOTH sides by
  `internal/cli/runbook_fault_evidence_test.go`: the host's TypeScript and the
  runbook. Reword one in `restoreSaid()` and the gate goes red naming the
  other side — that is the intended coupling, not a nuisance.
- A fourth restore path exists and already had its own line: the pre-finish
  ready check says `the workspace was lost before the finish phase; restored
  to <sha>`. It is outside this tick and the runbook's fault-2 criteria still
  name only the three (a destroy that lands after the conversation settled is
  not the mid-turn fault that section injects).
- The harness suites are NOT part of `make gate` (`.tick/runners.toml`'s `go`
  and `ts` commands); CI runs them. A change to `harness/src/` has to be
  proven with `pnpm test` in `harness/` by hand, as above.

```findings v2
[
  {
    "kind": "defect",
    "title": "Staging proof's announced-line regex matches only the between-rounds path",
    "severity": "low",
    "body": "harness/proof/restore-setup-staging.ts matches the log for one line only ('the container was lost between rounds; …'), while its own comment says the loss may be met by the ready check or the tracked bash's nonce check, and now by the mid-command path too. A proof run whose restore fires through any path but the ready check records announced_line: null in its evidence, which reads as 'nothing announced' when a line did land.",
    "evidence": "harness/proof/restore-setup-staging.ts:149,172 (the regex), :245-248 (announced_line)"
  },
  {
    "kind": "defect",
    "title": "Two restore lines can name one restore when the paths race",
    "severity": "low",
    "body": "restoreLostWorkspace() memoises concurrent callers into one restore, but each caller announces its own line: a mid-command loss racing the pre-round ready check on the same fresh box would say both 'the container was lost mid-command; …' and 'the container was lost between rounds; …' for one restore, against the 'one restore says one line' intent both wirings state. Pre-existing in kind since dbi; the ear this tick extended is the second announcer.",
    "evidence": "harness/src/env/factory-sandbox.ts:950 (containerLost announces) against the this.restoring memo at :913-918, and the extension's own onRestore in harness/src/host/worker-attempt.ts:945-949"
  }
]
```

STATUS: DONE — the mid-command restore now fires the env's `onRestore` ear with the loss that caused it, the host's say-stream names the sha and the loss, and the runbook and its Go parity guard pin the new line; harness suites (workerd 100, node 56), `make gate`, `make ts-gate` and cloudflare's 1870 tests are green.
