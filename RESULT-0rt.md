<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-7/0rt`, base `a5e191bbdb288e15bf2fa67f4c21f58be1045d19`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# Final review of the pi-durable cloud hardening diff — epic ex6, tick 0rt

## What I reviewed

The epic's thirteen tracker records (ten closed children, `eno` the close-out
that waits on this review, `sck` closed by restructuring, and this tick); the
integration branch `epic/ex6` against the commit it was cut from
(`db0c5d645`, "tick: ex6 runs on config:glm", 2026-10-07 15:43 +0200); the
ten tick merges and the three main folds the branch carries; and the run's
own records — this run (`run_af0e77b99c1540b7b8005cf04fd2795d`) took over
`run_69f8f57832d34601b040367c7207b81f`, whose checkpoint on the integration
branch reads `failed` with 2p3, 2pn, 96s and q6z held on
`attempt_needs_human` (the harness-fault-to-a-person ladder PR #260 fixed;
the fixed collect is folded into this branch, and those four ticks then
closed).

At the epic head (identical to this checkout except control-plane files
under `.tick/` and `.ticfac/`) I ran, on this container, everything the
epic's [A4] names plus the one suite no gate ran during this epic:

- the whole-repo gate (`gofmt -l`, `go vet ./...`, `go test -short ./...`) —
  green, every package;
- the ts gate (biome, contracts:check, tsc) — green;
- **the harness package's whole suite** — `pnpm lint && pnpm typecheck &&
  pnpm test` in `harness/` (the exact `harness-gate` recipe tick 2pn added,
  and the exact cell it proposes): green, both vitest halves;
- **the cloudflare vitest suite** the ts gate deliberately leaves to CI: 80
  files, 1874 tests — green, including every test 8gd added.

[A4]'s tree half holds at the head. That is the good news; the per-tick gate
never ran the harness half, which is exactly the silence 2pn exists to end.

## The ticks as integrated

- **3sd** — delivered: `internal/reconcile/classify_pin_test.go` gains the
  short-side digest comparison, so a profile edit fails the gate at the tick
  that makes it rather than CI hours later. ✓
- **8wa** — delivered: condensation level 3 omits each amendment's value and
  bounds its headline at a rune boundary, so a many-long-notes run can no
  longer fall to the forge's truncating fit; the look-first-here line no
  longer promises text the condensed body dropped. Tested. ✓
- **gzw** — delivered: the timeout-discipline guard recognises the
  `promisify(<entry point>)` shape under any local name, with the three
  real-bash tests of `standin-worker-entry-env` now named and enforced, a
  positive test for both alias spellings, and a counterweight that keeps a
  promisified timer out of the bound. ✓
- **8gd** — delivered: the cloud PR-review boot is hosted like every worker.
  `reviewHarness` resolves the hosted kind on a deployment with
  `WORKER_AGENTS`; the review container runs only the review's
  `--boot`/`--finish` halves (a bare boot on the hosted kind is refused
  early, and `--boot`/`--finish` refuse any other phase); the harness's
  `WorkerAttemptSpec` gains `kind` — a review gets no workspace restore, no
  wip checkpoints and no report contract, and its boot's `branch=` (the
  reviewed ref) is never given to the restore; `wrangler.toml` unsets
  `RUN_HARNESS` so the review reaches the hosted kind; the watch observes
  the agent (log, state, budgets) and settles it on every ending (trip,
  linger, unanswerable, out of looks, dead boot). The review boot/finish
  markers are pinned in the contract and asserted against both TS and
  `entrypoint.sh`. Well tested on both sides. ✓
- **qzg** — delivered: `containerLost` fires the same `onRestore` ear the
  nonce path uses, naming the loss that caused it, and the host's line is
  spelled out in full per cause — pinned by the runbook evidence test the
  runbook's [A2] criteria cite. Tested. ✓
- **2p3 + q6z** — the code half delivered: `ticfac init` writes no `args`
  into any routing it generates, on the base writer and the cloud overlay,
  with three layers of tests. The `.tick/` edits these ticks name — this
  repository's own `runners.toml` implement cell and `runners.cloud.toml` —
  are carried as **protected changes**, pending the run's labelled commits
  at the close-out's close. See finding 2 for what happens to the
  neighbouring pending cell when they land.
- **96s** — its entire deliverable (the `runners.local.toml` header note) is
  a pending protected change; the finding carries the composed, verified
  text, and the tick's own record says the behaviour guard already exists —
  documentation only. Pending.
- **2pn** — the Makefile half delivered (`harness-gate`, with its STATE note
  naming the window); the parity guard holds from whichever side lands
  first (`TestTheGateTargetMatchesTheDeclaredGate` tolerates the undeclared
  `harness` entry, `TestTheDeclaredHarnessGatePairsWithItsMakefileTwin`
  dry-runs the exact proposed cell against the real gate file and the real
  Makefile, with a negative control that refuses a `pnpm test` drop). The
  `[testing.commands.harness]` cell itself is the tick's **protected
  change**, pending. I ran the proposed command by hand: 1m30s, green, and
  byte-identical to the Makefile target.
- **m1w** — **half delivered**. See finding 1.

## The acceptance items, as integrated

- **[A1] a real cloud run, every worker on pi-durable, no stall
  undetected.** Every dispatch of this run — implement ticks, the
  resolve-conflict job, and this review itself — is a hosted pi-durable
  conversation on GLM in a cloud container (my own environment carries the
  harness's `TICFAC_BASH_NONCE`; I am the pi-durable worker this epic is
  about). 8gd removes the last CLI floor from the diff, so the PR-review job
  joins the hosted kind too, and the runbook's "designed exception"
  paragraph is updated to match. The stall detection this item leans on (the
  commit-stream watch, quiet-resume counting, the guarded tk resolution)
  landed in the epic's base as #236/#238; this run's own failures were
  harness faults, which the folded PR #260 turned into redispatches instead
  of questions for a person. The run has not completed yet — this review and
  `eno` are its remaining ticks — so [A1]'s "completes" is the close-out's
  to observe, not mine to assert.

- **[A2] the two faults, in a real cloud run, resume without redoing work.**
  **A concern, not a diff defect, and I say which it is.** The resume
  machinery is in the tree and pinned (the three restore lines, the nonce
  reattach, the wip ladder), but neither fault has been fired into this
  run, and nothing in the diff can fire them: fault 1 was attempted once —
  a factory deploy, 2026-10-07 17:01–17:11, which "landed between tool calls
  of ex6's gzw and proved nothing" (tick `0mz`'s own record) — and the door
  that would make it reliable (`0mz`) is an open, **unimplemented** operator
  tick (commit `d710b2f1e` created the tick; nothing implements it), on
  which `9if` — the injection, explicitly "the operator's to start" — is
  blocked. Fault 2's door exists (`wrangler containers delete` by exact
  name, runbook §2) and has not been used. So [A2]'s real-run half is
  unobserved at review time, and the runbook's fault-1 step still instructs
  the deploy the operator's own tick says proves nothing. No reader of this
  diff can act on that inside the epic; it belongs on the epic's PR as the
  operator's pending actions (`0mz`, `9if`, `lox`).

- **[A3] pi-durable workers metered locally and in the cloud.** The cloud
  half holds (every hosted conversation presents the run token to the
  factory's gateway, which stamps the attribution). **The local half is
  broken by finding 1**: m1w delivered the Go side of the join only, so a
  metered local dispatch writes facts into `worker.json` that no harness
  code reads, and the local durable worker's calls still go out through
  `localWorkersAIProvider()` on ambient credentials, with no gateway route
  and no attribution. m1w's own description sets the finish line — "local
  subprocess workers' spend is unmetered **until the harness reads it from
  worker.json**" — and the harness reads nothing.

- **[A4] make gate + cloudflare tests + harness suites pass in CI.** The
  tree half holds — I ran all four suites at the head, green. CI on the PR
  head is the close-out's wait. One integrated-state caveat rides finding 2:
  the per-tick gate still declares no harness command, and the pending set
  that is supposed to add it will instead remove it.

## Tests, judged

The failure this review exists to catch — a green suite over a change
nothing exercises — is present twice, and both are findings below: m1w's Go
tests exercise only the writer of a join no reader exists for (finding 1),
and the gate's parity guard exercises the *proposed* harness cell on a copy
while nothing exercises the close-out's composition of the five pending
protected changes, whose application order silently drops one of them
(finding 2). Every other tick's change is exercised by tests that came with
it. A third finding repairs the epic's own record so its marks parse: the
criteria are written on one line, so the machinery — the close-out's
done-evidence, worker-asserted-high absorption, and this report's own
`breaks` claim — can only ever see item A1, which is why finding 1 cannot
name the item it breaks typed.

```findings v2
[
  {
    "kind": "defect",
    "title": "m1w's metering join reaches worker.json and no harness code reads it",
    "severity": "high",
    "body": "Tick m1w's finish line — \"local subprocess workers' spend is unmetered until the harness reads it from worker.json\" — is delivered on the Go side only: internal/exec/subprocess/workerconfig.go:137 writes the join (gateway URL, run id, composed cf-aig-metadata value, credential pipeline) into worker.json, but no harness code reads it, so the local durable worker still calls Workers AI through localWorkersAIProvider() on ambient credentials with no gateway route and no attribution, and the epic's criterion \"pi-durable workers are metered locally and in the cloud\" fails its local half. workerconfig.go:112 even cites \"harness/src/local/gateway-metering.ts, tick m1w\" — a file that exists on no branch — and the gate stayed green because the only tests (workerconfig_test.go, executor_metering_test.go) exercise the writer. Ready when the harness half exists and is exercised: read the join from worker.json, compose the same provider override the pi CLI extension composes on the models the join applies to, with a harness test that a metered local launch tags its calls.",
    "evidence": "internal/exec/subprocess/workerconfig.go:100-160 (writes Metering, cites harness/src/local/gateway-metering.ts); harness/src/local/worker-host.ts:105-124 (LocalWorkerConfig declares no metering field) and :167-193 (localWorkersAIProvider, ambient credentials); `grep -rni metering harness/` finds nothing"
  },
  {
    "kind": "defect",
    "title": "Protected changes applied in key order wipe 2pn's harness gate cell",
    "severity": "high",
    "body": "The close-out applies each still-proposed protected change as one labelled commit, iterating the run's findings in key order (internal/runstate/findings_store.go:78), and a content proposal replaces its file whole (internal/exec/subprocess/protected_change.go Applied): 2pn's harness-cell append (finding 2911b0ce…) sorts first and applies, then q6z's and 2p3's whole-file .tick/runners.toml proposals (4fe8b808…, b916a378…, composed from bases before the cell existed) sort after it and each replace the file, so the cell the first commit added is gone from the branch the PR merges. Nothing catches it — the integrated gate ran before the protected changes, the append's finding is triaged FIXED by its own commit, and the parity guard documents an undeclared harness entry as the accepted window — so the epic's \"harness suites in the per-tick gate\" deliverable is silently lost at the moment of integration. Ready when those whole-file proposals carry the cell, or the apply path orders content replacements before appends per path, or it re-validates the declared gate commands after applying.",
    "evidence": "internal/reconcile/protected_changes.go:153-198 (applyProtectedChanges, one commit per finding in Findings() key order); internal/exec/subprocess/protected_change.go:68-76 (content replaces the file whole); the payloads: .ticfac/runs/run_af0e77b99c1540b7b8005cf04fd2795d/findings/2911b0ce…json (append, [testing.commands.harness]), 4fe8b808…json and b916a378…json (whole-file .tick/runners.toml without the cell)"
  },
  {
    "kind": "defect",
    "title": "ex6's acceptance criteria are one line, so only [A1] parses as an item",
    "severity": "medium",
    "body": "The epic's acceptance_criteria string puts all four [A<n>] marks on one line, and acceptance.Parse only counts a mark that leads its line, so the epic's done enumerates one item and A2–A4 are unaddressable: the close-out's done-evidence lists one mega-item, worker-asserted-high absorption can never match a finding naming A2–A4, and a review's typed breaks claim naming one is refused (this report hit exactly that). This is the same shape hn6's yjq repaired — the re-flow below keeps every item and its text, changing only the line breaks; the run applies it through its own tracker writer rather than filing a tick.",
    "tracker_edit": {"tick": "ex6", "field": "acceptance_criteria", "value": "[A1] A real cloud epic run completes with every worker on pi-durable, no stall left undetected.\n[A2] Killing the harness host mid-tool and destroying the container mid-turn in a real cloud run both resume without redoing work.\n[A3] pi-durable workers are metered locally and in the cloud.\n[A4] make gate + cloudflare tests + harness suites pass in CI."},
    "evidence": ".tick/issues/ex6.json acceptance_criteria (all four marks on one line); internal/acceptance/acceptance.go:15-17 (only a line-leading mark introduces an item); the lint's own answer: `ticfac-exec-subprocess lint-report … \"A3\" is not an acceptance item of the epic; the epic's items are A1`"
  }
]
```

## Verdict

The epic's harnessing, hosting and guard work (8gd, qzg, gzw, 3sd, 8wa, and
2p3/q6z's writer half) is integrated, tested and green at the head — I ran
every suite [A4] names myself. Two things keep it from ready: the local
metering deliverable is half a change, closed on a salvaged tree whose
report said "review the branch" while the half that does the work does not
exist (finding 1, the local half of "metered locally and in the cloud"); and
the pending protected changes — the designed channel through which four of
the ten ticks' actual deliverables reach the tree — are composed so that
applying them silently removes one of their own number (finding 2, 2pn).
Both are fixable inside the epic. [A2]'s real-run observation remains the
operator's — `0mz`, `9if`, `lox` — and is named above as a concern the PR
should carry, not a defect this branch can act on.

REVIEW-VERDICT: NOT READY — deliver m1w's harness half (read the metering join from worker.json, compose the provider override, with a harness test) and make the close-out's protected changes leave 2pn's harness gate cell in .tick/runners.toml (the whole-file proposals wipe it)

STATUS: DONE — the review ran to completion over the integrated epic; its judgement is the REVIEW-VERDICT line above, its blocking findings are in the findings block
