<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-11/ynd`, base `e698666f5d985f26824ec0494cbd33a70355f025`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Round-2 review of the pi-durable cloud hardening diff — epic ex6, tick ynd

## What I reviewed

The epic's thirteen tracker records (`eno` the close-out that waits on this
review, `sck` closed by restructuring, the rest closed); the integration
branch `epic/ex6` against the commit it was cut from (`db0c5d645`, "tick: ex6
runs on config:glm", 2026-10-07 13:43 UTC) — a source diff of 83 files,
+7392/−565, excluding the run's own state under `.ticfac/` and the tracker's
records under `.tick/`; the run's decision, absorption and gate records for
both fix ticks; and the previous review (0rt, decision 8), whose NOT READY
named two blocking findings and one medium one.

The round-1 review base was `a5e191bbdb28`. Between it and the current head
the tree changed by **exactly the two fix commits** — 8 files, +1218/−17:

- `tick lrd` (merged as `73f92acbff69`, integrated gate go+ts green at that
  head): `harness/src/local/gateway-metering.ts` (new, 234 lines),
  `harness/src/local/worker-host.ts` (the wiring),
  `harness/test/node/local-metering.test.ts` (new, 645 lines),
  `internal/exec/subprocess/workerconfig_parity_test.go` (new),
  `harness/README.md`;
- `tick tgx` (merged as `587236914556`, integrated gate go+ts green):
  `internal/reconcile/protected_changes.go` (+26),
  `internal/reconcile/protected_changes_order_test.go` (new),
  `internal/reconcile/testdata/fake-runner.sh` (a new `protected_change_order`
  mode).

Everything else in the diff is unchanged from round 1, which verified each
tick's delivery in detail.

## Blocker 1 — m1w's harness half (lrd): delivered, wired, exercised

Round 1 found the metering join written into `worker.json` and read by no
harness code, so the local durable worker called Workers AI on ambient
credentials, unmetered — the local half of [A3]. That half now exists:

- **The reader.** `LocalWorkerConfig` gains `metering` (the four facts the Go
  writer marshals: `gatewayUrl`, `runId`, `metadata`, `credentialCommand`), and
  `runLocalWorker` composes `meteredWorkersAIProvider(localWorkersAIProvider(),
  join)` when the config carries one **and** the routed model is one the join
  applies to (`meteringApplies` — the same three-namespace, non-empty-rest rule
  the Go writer's `GatewayMetering.Applies` applies, so the writer and the
  reader can never disagree about who gets a join; the tests' faux rung never
  does, either side's rule).
- **The override** mirrors the pi CLI's generated extension (`WriteExtension`)
  field for field: the route `<gateway>/workers-ai/v1` (pi-ai appends
  `chat/completions`), `Authorization` and `cf-aig-authorization` both
  carrying what the join's pipeline prints, `cf-aig-metadata` the composed
  value **read verbatim, never re-composed** (one writer for both artifacts,
  `workerconfig_test.go` pins the config's metadata to the extension's own
  composition), `x-session-affinity` the run id — every catalog entry kept,
  only the address and the credential displaced. The pipeline executes at
  request time through pi-ai's own auth seam; an empty answer is "not
  configured", reported as a stream error naming the provider — no anonymous
  call. A gateway URL that composes no http(s) route refuses the boot (exit 2)
  rather than running unmetered, the same refusal bar the writer's
  `gatewayBase` enforces.
- **The wiring chain is complete in production**: `localMetering` (per
  dispatch, `internal/cli/executor.go`) → `DefaultExecutor`
  (`internal/reconcile/dispatch.go`) → `subprocess.Options.Metering` →
  `meteringFor`/`writeWorkerConfig` (only for a `durableResume` launch) →
  `worker.json` → the harness override.
- **The tests drive the real doors.** `local-metering.test.ts` (10 tests)
  stands a real HTTP server in for the operator's AI Gateway — it demands
  `cf-aig-authorization` (401 code 2009 otherwise), records every request, and
  holds the stream's tail back (the read the gateway needs before it writes
  the row the metering IS). The acceptance test drives the **real entry**
  (`src/local/main.ts`) as a child process over a real git worktree with the
  **real credential pipeline** against a sealed `~/.ticfacrc`, and asserts
  every model call tagged — the route, both bearers, the metadata, the
  affinity — with the GLM catalog corrections on the wire and both streams
  drained. The negatives bite too: the faux rung runs unmetered with no
  request routed at the gateway; a join whose URL composes no route refuses
  the boot; a rotated-away credential settles unanswered, provider named,
  with nothing sent. I ran the file: 10/10 green, and this test fails at base
  by construction (at `a5e191bb` no code read `config.metering` — the calls
  go out on ambient credentials and the wire assertions fail).
- **The drift guard.** `TestTheMeteringJoinsSpellingsMatchTheHarnessReader`
  (short, so it runs in the per-tick gate) pins every field the Go writer
  marshals to a `readonly <field>:` declaration in the harness reader — the
  direction only a Go test can see, and the exact shape of the drift round 1
  caught.

I ran the whole harness package at the head: `pnpm lint`, `pnpm typecheck`
(both tsc projects), `pnpm test` — workerd half 10 files / 100 tests, node
half 11 files / 66 tests, all green.

## Blocker 2 — the protected-change order (tgx): delivered, exercised, and the real payloads compose

Round 1 found the close-out applying each still-proposed protected change as
one labelled commit in findings-key order, with a content proposal replacing
its file whole — so 2pn's harness-cell append (`2911b0ce…`, key sorts first)
landed and then q6z's and 2p3's whole-file `.tick/runners.toml` proposals
(`4fe8b808…`, `b916a378…`, composed without the cell) each replaced the file,
silently erasing the gate cell from the branch the PR merges.

- **The fix** is the second of the three remediations round 1 offered:
  `orderForApplication` orders the findings per path — every whole-file
  content replacement before every append, key order kept within each group —
  and `applyProtectedChanges` (the only apply path, called at the close-out's
  close, after the close-out's reads, with CI below gating the head that
  carries them) iterates that order.
- **The incident is reproduced end to end.**
  `TestProtectedChangesApplyContentsBeforeAppendsOnTheSameFile` (fake-runner
  mode `protected_change_order`) runs a real fake run with three findings to
  the SAME file whose keys sort append-then-contents — the exact order that
  lost the cell — and asserts the branch's final `runners.toml` carries BOTH
  the appended cell and the last content's edit, each finding triaged FIXED by
  its own commit. I ran it green (6.86 s, non-short), with the short unit guard
  of `orderForApplication` green beside it.
- **The real payloads compose.** I composed this run's pending set by hand the
  way the close-out now will — `4fe8b808` (content) then `b916a378` (content,
  last) then `2911b0ce` (append): the result parses as TOML, declares `go`,
  `ts` and `harness`, the harness command byte-identical to the Makefile's
  `harness-gate` recipe (which `gateTargets` already pairs, so
  `TestTheGateTargetMatchesTheDeclaredGate` holds the moment the cell lands),
  and the implement cell carries no `args`. The `runners.cloud.toml` and
  `runners.local.toml` payloads differ from the branch's files by exactly
  their intended edits — no stale-base clobbering anywhere in the set.

So 2pn's harness gate cell now survives integration, and the per-tick gate's
harness half turns live for the runs after the merge — the "harness suites in
the per-tick gate" deliverable reaches the tree through the channel designed
for it, listed on the PR for the merger to review.

## The rest of the epic, as integrated

Unchanged between rounds, and verified green by the suites below: 3sd (the
classification-digest short guard), 8wa (PR-body condensation level 3), gzw
(the promisify gap in the timeout-discipline guard), 8gd (the cloud PR-review
boot hosted — `wrangler.toml` binds `WORKER_AGENTS` and leaves `RUN_HARNESS`
unset so the review falls through to the hosted pi-durable rung, the exact
lesson v5t's 6fv taught about doors tested with the binding unbound), qzg (the
mid-command restore announcing through `onRestore`, its cause named), the
2p3/q6z writer half (`ticfac init` writes no `args`), and 2pn's Makefile half
(`harness-gate`, pinned by its own guards). Round-1's medium finding was
applied by the run: ex6's `acceptance_criteria` now parses four items.

## The acceptance items, as integrated

- **[A1] a real cloud run, every worker on pi-durable, no stall undetected.**
  The run this review belongs to IS that run: every dispatch — implement
  ticks, the resolve-conflict job, and both reviews — is a hosted pi-durable
  conversation in a cloud container (I am one). 8gd removed the last CLI
  floor. "Completes" is the close-out's to observe once this review lands: the
  close-out (`eno`) is the run's own remaining tick, and a review cannot
  demand the completion it is part of.
- **[A2] the two faults resume without redoing work.** The machinery is in the
  tree and pinned: the kill-mid-tool resume is exercised by a node-half test I
  ran green ("resumes from the storage after the harness is killed mid-tool,
  without re-running the tool"), the three restore lines and the nonce
  reattach are pinned (`runbook_fault_evidence_test.go`), and the
  mid-command loss now announces itself. **The live fault injection remains
  the operator's** — `9if`, blocked on `0mz`, both open ticks outside this
  epic, explicitly the operator's to start because they touch the live
  factory — and the runbook honestly scores the real-run half *predicted*
  until then. That is a concern for the PR, not a defect a reader of this diff
  can act on: no file in it is wrong, and the epic's own constitution routes
  a deployed-factory clause to post-merge.
- **[A3] pi-durable workers metered locally and in the cloud.** The cloud half
  holds (every hosted conversation presents the run token to the factory's
  gateway, which stamps the attribution). The local half is the round-1
  blocker, now delivered end to end by m1w + lrd.
- **[A4] make gate + cloudflare tests + harness suites pass in CI.** I ran
  everything at the head, green: the whole-repo Go short gate (`gofmt -l`,
  `go vet ./...`, `go test -short ./...` — 47 packages ok), the ts gate (biome,
  `contracts:check`, `tsc --noEmit`), the harness package (lint, typecheck,
  100 workerd + 66 node tests), and the cloudflare vitest suite the ts gate
  leaves to CI (80 files, 1874 tests). CI on the PR head — which will carry
  the protected changes, and whose go job runs the full suite including the
  two end-to-end protected-change tests — is the close-out's wait, and the
  close-out holds on it by rule.

## Tests, judged

Round 1 found the failure this review exists to catch twice: a green suite
over a change nothing exercises. Both are repaired, and I verified the repairs
run and bite: the metering join now has a reader exercised by a wire-level
acceptance test that fails at base, with the writer pinned to the reader by a
parity guard that runs in the per-tick gate; and the protected-change
composition now has an end-to-end test reproducing the incident's exact key
order, plus a short unit guard. One test-fidelity gap remains, filed low below:
the harness suite's credential pipeline is a hand-copied duplicate of the Go
builder's, and nothing compares the two, so the suite could keep passing
against a pipeline production no longer writes.

No DEFERRED FINDINGS are listed on this tick's record, so there is nothing to
re-report on a worker's word.

## Concerns for the PR (not diff defects, and I say which they are)

- `0mz` / `9if` — the fault-1 door is still a factory deploy, which "lands
  between tool calls and proves nothing" (the runbook says so itself), and the
  operator door that would make the injection reliable is open and
  unimplemented. The [A2] real-run observation waits on both, and on the
  operator's hand.
- `lox` — the post-merge cloud run for the 43y [A4] cloud half, the
  operator's to start on the deployed factory.
- The harness gate cell, the implement-cell cleanups and the local header note
  are pending protected changes: they reach the tree as labelled commits at
  the close-out's close, and the PR lists every one for the person who merges.

## Verdict

Both of round 1's blocking findings are fixed inside the epic, and each fix is
delivered with tests that exercise the change itself rather than a stand-in
for it — I ran them. The integrated state the close-out will produce is sound:
the pending protected changes now compose to a gate file that declares all
three commands, pairs with its Makefile twin, and keeps the cell the incident
erased. The items the epic's marks name that no diff can deliver — the run's
completion, the live fault injections, CI on the PR head — are the close-out's
observations and the operator's post-merge ticks, named above as concerns the
PR carries. Nothing I can ground in this diff keeps the epic from doing what
it said it would.

```findings v2
[
  {
    "kind": "defect",
    "title": "Harness metering test's credential pipeline is unpinned from Go's",
    "severity": "low",
    "body": "local-metering.test.ts spells the credential pipeline by hand (CREDENTIAL_PIPELINE) as a copy of the Go builder's gatewayCredentialPipeline, and nothing compares the two: workerconfig_test.go pins that worker.json carries the real pipeline, and the field-spelling parity guard pins only the join's field names, so a change to credentials.ShellGetCommand or its key would leave the harness suite green against the old text while real launches execute the new one. The suite that proves \"a metered local launch tags its calls\" could then certify a credential path production no longer writes. Pin the fixture — the parity test already reads the harness sources, and one more assertion comparing the test's constant against gatewayCredentialPipeline closes it.",
    "evidence": "harness/test/node/local-metering.test.ts:74-78 (CREDENTIAL_PIPELINE, hand-copied) vs internal/exec/subprocess/pimeter.go:172 (gatewayCredentialPipeline = credentials.ShellGetCommand(KeyCloudflareAPIToken) + Bearer prefix); internal/exec/subprocess/workerconfig_parity_test.go pins field spellings only"
  }
]
```

REVIEW-VERDICT: READY

STATUS: DONE — the review ran to completion over the integrated epic; its judgement is the REVIEW-VERDICT line above, its findings are in the findings block
