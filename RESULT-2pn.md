<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-10/2pn`, base `532a8cc9cace67355e1b94aac508e1f2fa581ccf`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `532a8cc9cace67355e1b94aac508e1f2fa581ccf` is the head of the work it continued, which was cut from `177cfc3965afce7842ce168d7af19ef9cfce61a7`; its work commits are counted from the carried head._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent staged .tick/runners.toml for commit

# 2pn — the harness package's suites in the per-tick gate

## What this attempt did

The tick asks for two things, in order: measure the harness package's suites
against the gate bound, then add them to `[testing.commands]`.

**The measurement is done, and it says the whole suite fits** (see the table
below). That answers the tick's "or at least its fast half": the two halves
cost about the same, and the node half — real bash, real git, the real local
door — is the half the red-at-base test (`dumb-git-origin`) lived in, so a
fast half would have gate-covered the package and missed the very thing that
made this tick necessary.

**What landed, in one commit (`d6294ad`):**

- `Makefile` gains `harness-gate`, the Makefile twin of the harness gate
  command: `cd harness && pnpm install --frozen-lockfile --prefer-offline &&
  pnpm lint && pnpm typecheck && pnpm test`. It is the target
  `TestTheGateTargetMatchesTheDeclaredGate` will pin the declaration to, and
  until the declaration lands it is the human/CI half alone — the comment says
  so, so a reader is not left to guess.
- `internal/reconcile/gate_target_test.go` gains
  `TestTheHarnessGateTargetRunsTheHarnessSuites`: the target must run the
  package's own install, lint, typecheck and `pnpm test`, and
  `harness/package.json`'s `test` script must run BOTH vitest configs — so
  the target cannot quietly cover half the package's suites. Verified to fail
  both halves when broken (I broke each, read both error messages, restored).

**What did not land is the `[testing.commands]` cell itself — the one line the
tick exists to write — and that is why this attempt answers BLOCKED.** The cell
lives in `.tick/runners.toml`, and this substrate refuses a worker commit
under `.tick/` wholesale:

- the container's pre-commit hook (image/worker.sh, installed at
  `.git/hooks/pre-commit`) refused my commit, verbatim:
  *"ticks-worker: refusing this commit — it stages tracker state. … Refused
  paths: .tick/runners.toml. Unstage them (git reset -- .tick) and commit your
  work without them."* The attempt is in the guard's ledger and will be
  prepended to this report by the container.
- the Go write boundary itself **exempts** the runner table
  (`internal/exec/subprocess/report.go:190`: "the run configuration, the runner
  table and the learnings"), so by the run's own authority this tick is
  deliverable by a worker — that is presumably why it was dispatched.
- but the two enforcers this substrate actually runs do not honour that list:
  the hook refuses any staged `.tick/` path, and the cloud collect
  (`cloudflare/src/worker-collect.ts:313`, `:530`) makes any `.tick/` path in
  a branch diff a `boundary-violation` verdict that refuses the whole branch.
  That is exactly tick **9sy**, still open, re-checked as "unmoved" by the ilz
  cloud close-out on 2026-10-07 (`docs/ilz-closeout-retro-2026-10-07.md:19`).

I did not force the cell past the hook with `--no-verify`. The hook exists to
stop the agent, the collect behind it refuses the branch wholesale, and two
real cloud runs have already documented that wall; betting that some other
collector might accept it is how an attempt's real work gets thrown away. The
finding below carries it.

## The measurement (4 cores, load under 1, this container)

| step | wall |
|---|---|
| `pnpm install --frozen-lockfile --prefer-offline` | 0.4s warm store / **11.3s the first ever run** (a store fill, not a gate cost) |
| `pnpm lint` (Biome) | 1.5s |
| `pnpm typecheck` (two tsc projects) | 15.3s |
| `pnpm test`, workerd half (`vitest.config.ts`) | 30–37s |
| `pnpm test`, node half (`vitest.node.config.ts`) | 26–28s |
| **whole command, warm** | **1m24s** (1m25s on a second warm run) |
| whole command, cold store | 1m29s |

Against the bounds that matter: the harness bound under one gate command is
**60 minutes** (`DefaultGateTimeout`, and `TestTheHarnessBoundOutlivesEveryDeclaredGateBound`
requires it to be the outer bound), and the Go half of this gate is measured
in minutes — 1m45s for `make gate` here. ~85s is the same order as the
cloudflare vitest the `ts` cell deliberately leaves to CI, and here it buys
the whole point of the tick: a suite the gate does not run is one a tick can
silently break, which is how one harness test came to be found red at base by
five workers and absorbed five times (h3c, 7oy, 4ao, omq, 30e).

## The exact remaining change — verified here, ready to apply

Two edits, nothing else. The Makefile target, the guard and the measurement are
already on this branch.

**1. `.tick/runners.toml`** — insert this block inside `[testing.commands]`
(after the `ts = { … }` line, before `[environment.commands]`):

```toml
# The pi-durable harness package's own suites (tick 2pn, epic ex6). Until it,
# the harness package that epic 43y created ran lint, typecheck and BOTH its
# vitest suites in CI (ci.yml's `harness conformance and replay`) and in no
# gate at all. A tick that changed harness/ merged on a gate that had said
# nothing about it: one of its node-half tests sat red at base and was found,
# and absorbed, five times by five workers (h3c, 7oy, 4ao, omq, 30e), because
# no gate ever told a run the suite was already red before its work began.
#
# What is here is the measurement the tick asked for first. Measured on this
# host (4 cores, load under 1): pnpm install 0.4s from a warm store (11.3s the
# first ever run, a store fill, not a gate cost), `pnpm lint` 1.5s, `pnpm
# typecheck` 15.3s (two tsc projects), `pnpm test` 56-65s (the workerd half
# 30-37s, the node half 26-28s). The whole command: 1m24s warm, 1m29s cold
# store, against the 60m harness bound under one gate command and a Go half
# this host measures at 1m45s. The FAST HALF would not have been enough: the
# node half — real bash, real git, the real local door — is where the
# red-at-base test lived, so a gate that ran only the workerd half would have
# covered the package and missed it a sixth time.
#
# `pnpm test` is harness/package.json's own script, so CI's step and this
# check cannot drift; `pnpm install` comes first because the gate runs in a
# fresh worktree of the integration branch, which carries no node_modules.
harness = { command = "cd harness && pnpm install --frozen-lockfile --prefer-offline && pnpm lint && pnpm typecheck && pnpm test", description = "Harness package: lint, types and both vitest suites" }
```

**2. `internal/reconcile/gate_target_test.go`** — in
`TestTheGateTargetMatchesTheDeclaredGate`, add the mapping and drop the comment
lines that say it is deliberately absent:

```go
	targets := map[string]string{
		"go":      "gate",
		"ts":      "ts-gate",
		"harness": "harness-gate",
	}
```

**Verified here:** a throwaway test (run, then deleted) read a patched copy of
`runners.toml` through `reconcile.ReadGateCommands` — it parses into three
commands, `go`, `harness`, `ts` — and compared the `harness` command with the
`harness-gate` recipe via the same `makeRecipe` the parity test uses: **byte
identical**, so edit 2 completes the parity and the whole tick. Nothing else
is needed: `make gate`, `make ts-gate` and `make harness-gate` all pass.

One thing to know about when it takes effect: the run reads its gate commands
once, at construction, from the orchestrator's own checkout
(`internal/reconcile/reconcile.go:1577`). The cell therefore gates the run
that starts after it lands — not the gates of the run already in flight.

## What I ran, and what it said

- `go test ./internal/reconcile/ -run 'TestTheHarnessGateTargetRunsTheHarnessSuites|TestTheGateTargetMatchesTheDeclaredGate|TestThisRepositorysGateIsReadable|TestTheHarnessBoundOutlivesEveryDeclaredGateBound'` — **pass** (the new guard, the parity guard, and both gate-file guards).
- the new guard's negative control: with `pnpm test` dropped from the target and `vitest.node.config.ts` dropped from the package's `test` script, it fails naming both, verbatim: *"the `harness-gate` target does not run "pnpm test"…"* and *"harness/package.json's `test` script does not run "vitest.node.config.ts" … so `pnpm test`, and the harness gate with it, covers only half the package's suites"*. Restored, and both files are back to their committed state.
- `make gate` (the whole-repo gate: gofmt, `go vet ./...`, `go test -short -timeout 45m -parallel 12 ./...`) — **exit 0, 1m45s**, 47 packages ok, gofmt and vet clean.
- `make harness-gate` — **exit 0, 1m25s**: 95 tests passed in the workerd half, 54 in the node half.
- `make ts-gate` — **exit 0**, 25.6s here (this container's cloudflare store was cold; the declared number for a warm store is ~3s).
- `gofmt -l . | grep -v '^contracts/'` — empty.

## What the next tick has to know

- The two edits above are the whole remaining work; the branch carries the
  target, the guard and the measurement, and `make gate` is green on it.
- If the next attempt is another container worker, it hits the same wall and
  must not force `.tick/` past the hook: the fix is either 9sy landing (honour
  `ExemptFromBoundary()` in the hook and in `worker-collect.ts`, with
  report.go's list as the single fixture) or a person applying the two edits.
- This tick cannot honestly be closed on this branch alone: closing it with
  the declaration missing would be a close behind a gate that still says
  nothing about the harness package.

```findings v2
[
  {
    "kind": "defect",
    "title": "A tick whose deliverable is .tick/runners.toml is undeliverable from a container",
    "severity": "high",
    "body": "The Go write boundary exempts the runner table (internal/exec/subprocess/report.go:190), so planning law says a tick whose deliverable is a .tick/runners.toml cell is a worker's to deliver — but this substrate refuses it twice over: the container's pre-commit hook refuses any staged .tick/ path (this attempt's commit was refused naming .tick/runners.toml), and the cloud collect makes any .tick/ path in a branch diff a boundary-violation that refuses the whole branch. So the one line tick 2pn exists to write cannot be written by any dispatched attempt here, and the same wall already cost the umq and ilz cloud close-outs their learnings compaction. This is tick 9sy's defect reached by a new class of tick: the fix it names — honour ExemptFromBoundary() in the hook and in worker-collect.ts, with report.go's list as the single fixture both are tested against — would also unblock this one.",
    "evidence": "image/worker.sh:534 (the pre-commit hook) and cloudflare/src/worker-collect.ts:313,530, against internal/exec/subprocess/report.go:190; the hook's refusal of this attempt's commit is recorded in /work/repo.guard/attempts and prepended to this report"
  },
  {
    "kind": "proposal",
    "title": "make gate runs only the Go third of the three commands the run gate runs",
    "severity": "low",
    "body": "AGENTS.md and .tick/config.md tell a human the bar to merge is `make gate`, and that target runs the Go half only: the TypeScript half is `make ts-gate` and the harness half is `make harness-gate`, each its own [testing.commands] entry by design, so a person following the documented bar runs a third of what the run's integrated gate runs. Either an umbrella target (gate-all: gate ts-gate harness-gate) or a sentence in both documents naming all three would close the gap; this tick makes it a third rather than a half, which is why it is worth its own tick rather than a line here.",
    "evidence": "AGENTS.md:3-6 and .tick/config.md:10 name `make gate`; Makefile carries gate, ts-gate and harness-gate as three separate targets"
  }
]
```

STATUS: BLOCKED — the tick's deliverable is one `[testing.commands]` cell in `.tick/runners.toml`, and this substrate refuses any worker commit under `.tick/` (the container's pre-commit hook refused it; the cloud collect refuses any `.tick/` path in a branch diff) even though the run's own boundary exempts the runner table — tick 9sy's wall. The measurement, the Makefile twin and its guard are committed; the two verified edits that finish the tick are in this report.
