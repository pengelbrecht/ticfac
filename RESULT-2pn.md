<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-5/2pn`, base `714f7ad04386c41ebbb2cba38b1d547eaad067b9`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `714f7ad04386c41ebbb2cba38b1d547eaad067b9` is the head of the work it continued, which was cut from `532a8cc9cace67355e1b94aac508e1f2fa581ccf`; its work commits are counted from the carried head._

# 2pn — the harness package's suites in the per-tick gate

## What this attempt did

The tick asks for two things, in order: measure the harness package's suites
against the gate bound, then add them to `[testing.commands]`. The measurement
and the Makefile twin of the cell (`make harness-gate`, with
`TestTheHarnessGateTargetRunsTheHarnessSuites` pinning its pieces) were on the
work this attempt carried; both are re-measured and re-verified below.

This attempt lands the half the carried work could not, and the change that
makes the tick closable rather than blocked:

**The `[testing.commands]` cell itself is this tick's `protected_change`.** It
is a change to a file this substrate refuses every worker commit for, and the
run's own channel for exactly that case applies it onto the epic branch as one
labelled commit, after the close-out's reads, and lists it in the epic PR. The
append text below is byte-identical to what
`TestTheDeclaredHarnessGatePairsWithItsMakefileTwin` dry-runs in this tree.

**The parity guard had to change first, and the reason is the run's own
ordering.** `internal/reconcile/protected_changes.go` applies a held protected
change after the close-out's integrated gate — so the cell is NOT in the tree
when this attempt's gate runs, and IS in the tree when the epic PR's CI runs
over the labelled commit. `TestTheGateTargetMatchesTheDeclaredGate` refused both
sides of that window, because it counted declared commands against the Makefile
targets it knew and failed on any difference. Reproduced at base here (a
throwaway test, deleted before the commit): a copy of `.tick/runners.toml`
with the cell appended parses as three commands — go, harness, ts — and the
pre-change check refused it with *"this repository declares 3 gate command(s)
and this test knows 2"*. So the carried work's plan (add the map entry when the
cell lands) would have turned the epic PR red on the commit that finished the
tick, and adding the map entry first would have turned this attempt's own gate
red.

What changed, in `internal/reconcile/gate_target_test.go`:

- the `gateTargets` map gains `"harness": "harness-gate"`, and loses ONLY its
  count equality. The forward direction is untouched: a declared command nobody
  can name a target for still fails, and a declared command and its Makefile
  twin must still agree byte for byte. The reverse direction — a target with no
  declaration yet — is now the test's one documented window, stated in a log
  line when it is live, and held shut by the new test.
- the parity check is extracted as `gateTwinProblems` over two paths, so the
  new test can run it against a declaration that is not in this tree yet.
- `TestTheDeclaredHarnessGatePairsWithItsMakefileTwin` appends the exact cell
  to a copy of this tree's gate file and requires three things: the reader
  accepts the cell's `[testing.commands.harness]` spelling (toml.go documents
  both spellings as the same document, which is what lets an appended change
  open its own table instead of landing in `[environment.commands]`);
  `runconfig.Parse` still loads the whole document — a cell that broke the
  run's config reader would break the run at start, over routing, long before
  any gate ran; and all three declared commands pair with their Makefile twins,
  byte for byte. Its negative control drops `pnpm test` from the cell and
  requires the pairing to refuse it.
- the `harness-gate` Makefile comment's STATE paragraph is corrected: its last
  sentence described the parity map growing when the cell landed, which the new
  design replaces.

## The measurement (this container, 4 cores, load under 1)

| step | wall |
|---|---|
| `pnpm install --frozen-lockfile --prefer-offline` | 0.9s warm / 12.2s the first run here (a store fill, not a gate cost) |
| `pnpm lint` (Biome, 56 files) | 1.7s |
| `pnpm typecheck` (two tsc projects) | 17.7s |
| `pnpm test` | 60.4s — 95 workerd-half tests, 54 node-half tests, both green |
| **whole `make harness-gate`** | **1m17s** |

Against the bound that matters: the harness bound under one gate command is 60
minutes (`DefaultGateTimeout`, and
`TestTheHarnessBoundOutlivesEveryDeclaredGateBound` requires it to be the outer
bound). The cell costs 1m17s where the Go half of the same gate cost 1m58s
here, so the whole gate goes from about 2m30s to about 3m47s per tick. That is
the same order as the cloudflare vitest the `ts` cell deliberately leaves to
CI, and here it buys the tick's whole point.

It is the whole suite, not the fast half the tick offered as a fallback: the
node half — real bash, real git, the real local door — is where the red-at-base
test (`harness/test/node/dumb-git-origin.test.ts`) lived, so a gate that ran
only the workerd half would have covered the package and missed it a sixth
time.

## What I ran, and what it said

- the base reproduction (throwaway test, then deleted): a copy of the gate
  file with the cell appended parses to three commands, and the pre-change
  parity check refused it — *"this repository declares 3 gate command(s) and
  this test knows 2"*.
- `go test ./internal/reconcile/ -run 'TestTheGateTargetMatchesTheDeclaredGate|TestTheDeclaredHarnessGatePairsWithItsMakefileTwin|TestTheHarnessGateTargetRunsTheHarnessSuites|TestThisRepositorysGateIsReadable|TestTheHarnessBoundOutlivesEveryDeclaredGateBound' -short` —
  **pass**. The live window is logged, not failed: *"the "harness" check has
  its Makefile target (harness-gate) and is not declared in .tick/runners.toml
  yet"*.
- negative control 1: the cell drifted (`pnpm typecheck` dropped from the
  literal) — the new test **fails** with the drift message naming
  `harness-gate` and `"harness"`; restored.
- negative control 2: the map entry deleted (the pre-change state) — the new
  test **fails** with *"the declared gate "harness" has no Makefile target in
  this test's map"*; restored.
- `make gate` — **exit 0, 1m58s**: gofmt and `go vet ./...` clean, the short
  suite green across every package (internal/reconcile 17s).
- `make ts-gate` — **exit 0**, 31s (this container's cloudflare store was cold;
  the declared number for a warm store is ~3s).
- `make harness-gate` — **exit 0, 1m17s**: 95 tests in the workerd half, 54 in
  the node half.
- `gofmt -l . | grep -v '^contracts/'` — empty.

## What the merger and the next tick have to know

- The cell lands as one labelled commit on the epic branch, applied by the run
  at the close-out's close, and the epic PR lists it under protected changes.
  It takes effect for the run that starts after it lands: a run reads its gate
  commands once, at construction, from the checkout it was started in
  (`internal/reconcile/reconcile.go:1577`).
- Nothing else is needed. With the cell applied, the parity guard compares the
  `harness` command against the `harness-gate` recipe byte for byte on the real
  file, and until then `TestTheDeclaredHarnessGatePairsWithItsMakefileTwin`
  holds the same line through its literal — which is also why a later tick that
  changes the `harness-gate` recipe must move the declared cell with it: the
  parity guard names both spellings in one message.
- If the applied cell ever disagrees with the Makefile, the failure is
  `TestTheGateTargetMatchesTheDeclaredGate` (drift, naming `harness-gate`), not
  a silent half-covered gate.

```findings v2
[
  {
    "kind": "defect",
    "title": "The per-tick gate runs no harness suite: the cell is a protected change",
    "severity": "high",
    "body": "The harness package epic 43y created runs lint, typecheck and BOTH its vitest suites in CI (ci.yml's \"harness conformance and replay\") and in no gate, so a tick that changes harness/ merges on a gate that says nothing about it — the silence that left one harness test (harness/test/node/dumb-git-origin.test.ts) red at base, found and absorbed five times by five workers (h3c, 7oy, 4ao, omq, 30e). This is the tick's own deliverable: the [testing.commands] cell that makes the run's integrated gate run the suite. The file it lives in is one this substrate refuses every worker commit for, so the cell is carried here as the finding's protected_change for the run to apply as one labelled commit on the epic branch, after the close-out's reads. Measured against the bound the tick asks for first, on this container (4 cores): pnpm install 0.9s warm (12.2s the first ever run, a store fill, not a gate cost), pnpm lint 1.7s, pnpm typecheck 17.7s (two tsc projects), pnpm test 60.4s (the workerd half 95 tests, the node half 54) — the whole command 1m17s against the 60m bound under one gate command and a Go half the same host measures at 1m58s. The whole suite, not the fast half: the node half — real bash, real git, the real local door — is where the red-at-base test lived, so a workerd-only gate would have covered the package and missed it a sixth time. The command is byte-identical to the Makefile's harness-gate target, which TestTheHarnessGateTargetRunsTheHarnessSuites pins, and TestTheDeclaredHarnessGatePairsWithItsMakefileTwin dry-runs this exact append against a copy of the real gate file and the real Makefile: it parses, the whole document still loads through runconfig.Parse, and all three declared commands pair.",
    "evidence": ".tick/runners.toml [testing.commands] declares go and ts only; harness/ runs in .github/workflows/ci.yml's 'harness conformance and replay' step and in no gate; internal/reconcile/gate_target_test.go carries the dry run of the cell this finding appends",
    "protected_change": {
      "path": ".tick/runners.toml",
      "append": "\n# The pi-durable harness package's own suites (tick 2pn, epic ex6). Until this\n# cell the harness package that epic 43y created ran lint, typecheck and BOTH\n# its vitest suites in CI (ci.yml's \"harness conformance and replay\") and in\n# no gate at all, so a tick that changed harness/ merged on a gate that had\n# said nothing about it: one of its node-half tests sat red at base and was\n# found, and absorbed, five times by five workers (h3c, 7oy, 4ao, omq, 30e)\n# because no gate ever told a run the suite was already red.\n#\n# The whole suite, not a fast half. Measured on one host (4 cores, load under 1):\n# pnpm install 0.4s from a warm store (11.3s the first ever run, a store fill,\n# not a gate cost), pnpm lint 1.5s, pnpm typecheck 15.3s (two tsc projects),\n# pnpm test 56-65s (the workerd half 30-37s, the node half 26-28s). The whole\n# command 1m24s warm and 1m29s cold against the 60m bound under one gate\n# command and a Go half the same host measures at 1m45s — the same order as the\n# cloudflare vitest the ts cell deliberately leaves to CI. The FAST HALF would\n# not have been enough: the node half — real bash, real git, the real local\n# door — is where the red-at-base test lived, so a gate that ran only the\n# workerd half would have covered the package and missed it a sixth time.\n#\n# pnpm test is harness/package.json's own script, which is BOTH vitest\n# configs, so CI's step and this check cannot drift; pnpm install comes first\n# because a gate worktree carries no node_modules.\n[testing.commands.harness]\ncommand = \"cd harness && pnpm install --frozen-lockfile --prefer-offline && pnpm lint && pnpm typecheck && pnpm test\"\ndescription = \"Harness package: lint, types and both vitest suites\"\n"
    }
  },
  {
    "kind": "proposal",
    "title": "make gate covers a third of what the run's gate runs: name all three for a human",
    "severity": "low",
    "body": "AGENTS.md and .tick/config.md name `make gate` as the bar a human must run before they merge, and that target runs the Go third only: the TypeScript half is `make ts-gate` and the harness half is `make harness-gate`, each its own [testing.commands] entry by design, so a person following the documented bar runs a third of what the run's integrated gate runs over the same tree. This tick's cell makes it a third rather than a half, which is why it is worth a tick of its own rather than a line here. Either an umbrella target (gate-all: gate ts-gate harness-gate) or a sentence in both documents naming all three closes it.",
    "evidence": "AGENTS.md:3-6 and .tick/config.md:10 name `make gate`; the Makefile carries gate, ts-gate and harness-gate as three targets"
  }
]
```

STATUS: DONE — the cell is delivered as this tick's protected_change (applied by the run onto the epic branch, after the close-out's reads, for the merger to review); the parity guard now covers the harness half from whichever side lands first, with the dry run, both negative controls, the fresh measurement, `make gate`, `make ts-gate` and `make harness-gate` all green on this branch.
