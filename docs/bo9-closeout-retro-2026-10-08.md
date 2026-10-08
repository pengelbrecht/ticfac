# Epic bo9 closed out: operator surface follow-ups

The retro of epic `bo9` from its close-out (tick `52h`, run `run_03aa9c8a71574ab3891b47df3dbd5ec9`).
It is written from the integration branch `epic/bo9`, the run's records under
`.ticfac/runs/run_03aa9c8a71574ab3891b47df3dbd5ec9/`, and the ticks' own `RESULT-*.md` reports on
their attempt branches. Plans and memory are not sources here. Every acceptance item below was
re-checked against the tree at this close-out rather than taken from a tick's own account.

## What the epic set out to do

Four follow-ups found 2026-10-07 while setting ticfac up on a second machine (ralph) and
refreshing the ticfac skill (#259): let a cloud run take `--config` (ba4); tell an operator when
an installed ticfac skill copy is older than the binary (g5f); print a skill's `references/`
files out of the binary (u2g); and ship the local pi harness inside the ticfac binary — esbuild
bundle, `go:embed`, run with `node`, no npm — so local pi runs work in any repository and on any
machine (0ek). A prerequisite for the first release (y9v).

## How the run went

| tick | role | model, tries | outcome |
|---|---|---|---|
| 0ek | implement | sonnet (economy), 1 | merged `c8e6310b0939`: `harness/scripts/build-local-bundle.mjs` bundles `harness/src/local/main.ts` with pi-durable, pi-ai and chord inlined into `harness/embed/local-main.bundle.mjs` + a committed sources-hash sidecar; `go:embed` in `embedded.go`; the runner's `"pi"` argv becomes `node {{harness_bundle}}`, cached per build under `$TICFAC_CACHE_DIR`; `doctor` checks Node 22 |
| ba4 | implement | sonnet (economy), 1 | merged `3a69386864b3`: `--cloud` + `--config` no longer refused; a `config` field on the factory submission, through `parseSubmission` → `RunWorkflowParams` → `orchestratorEnv`'s `TICKS_CONFIG` → `image/common.sh`'s `run_config` → `--config` on the `ticfac run-epic` argv |
| g5f | implement | sonnet (economy), 1 | merged `868d88b524c0`: `internal/skills/diff.go` and `ticfac skills diff <name>`, reporting added/removed/changed files and a stamp older than the bundle, each target naming `ticfac skills install <name>` as the fix |
| rkk | implement | sonnet (economy), 1 | merged `e28a6094b699` through an opus resolve of four files against g5f and u2g (`skills.go`, `skills_test.go`, `SKILL.md`, `references/commands.md`): `skills install`/`skills get` take no name and default to the ticfac skill |
| u2g | implement | sonnet (economy), 1 | merged `06d41537071b` through an opus resolve of `skills.go` against rkk: `skills get --full` prints SKILL.md then every `references/` file under a `--- <path> ---` header |
| g2s | review | opus | **READY** at `ed3f1d2fc915`, with six non-blocking findings |
| 52h | close-out | opus | **rejected**, `no-commits`: the attempt reported its retro in prose and proposed the learnings as a protected change, and committed nothing. See "The close-out that was rejected" below |
| two | review, round 2 | opus | **READY** at `e90a6ac091bb`. Round 2 ran only because the tree changed after g2s's verdict — a fold of main brought in `cloudflare/test/run-routes.test.ts`, which the epic's own diff touches — with four more non-blocking findings |
| 52h | close-out, attempt 9 | opus | this record |

Five implement attempts ran, all on sonnet at the economy tier, and every one delivered on its
first try. Both escalations to opus were merge resolves, not ability: rkk and u2g were
same-wave ticks editing the same four files, which is the collision the repository's learnings
already name ("two additions to one file are a union in INTENT, not in text"). The epic planned
four implement ticks and ran five — rkk was added on 2026-10-07 from an operator remark that
`ticfac skills install ticfac` repeats itself.

## Delivered against the definition of done

- **[A1] Built and tested hop by hop; never demonstrated by the run it names.** Every hop of the
  chain is in the tree: `internal/cli/run.go` drops the refusal, `run_cloud.go:214` carries
  `*fl.config` into the submission, `cloudflare/src/runs.ts` validates and threads it,
  `sandbox.ts` exports `TICKS_CONFIG`, `image/common.sh:162` reads it into `run_config`, and
  `internal/factory/ticfacentrypoint.go:343` appends `--config "$run_config"`.
  `TestRunCloudForwardsTheNamedConfig`, `TestOverrideForwardsTheNamedRunConfig` and
  `TestOverrideLeavesOffTheConfigFlagWhenUnset` pass. What has not happened is the command:
  no tick ran `ticfac run <epic> --cloud --config claude`. g2s verified the chain by READING all
  eight hops, saying so plainly because no factory was reachable from its container. This run is
  not that proof either — it is a cloud run on the claude config, but selected by bo9's own
  `config:claude` label, under the orchestrator deployed from main, which does not yet carry
  ba4. Filed as **ihr**, naming item A1. One hop carries no test at all:
  `image/common.sh:162`, the single link between the Worker's `orchestratorEnv` output and the
  entrypoint's argv (**w75**).
- **[A2] Met.** Verified at this close-out, not read: built the binary, `ticfac skills install`
  into a scratch repository, rewrote the stamp's version to `0.0.1`, and ran `ticfac skills diff
  ticfac` — "drift against the embedded bundle: installed=0.0.1 bundle=dev", "upgrade with
  `ticfac skills install ticfac`", exit 1.
- **[A3] Met.** Verified the same way: `ticfac skills get ticfac --full` printed SKILL.md and
  then all three `references/` files — `cloud.md`, `commands.md`, `holds.md` — each under a
  `--- <path> ---` header naming its path.
- **[A4] Met, and verified where it is weakest.** The container this close-out ran in has no
  `harness/node_modules` at all, and
  `TestTheDurableRunnerRunsOnTheEmbeddedBundleWithNoHarnessCheckout` still passed: a real
  supervisor and a real Node harness, from the embedded bundle cached under an isolated
  `$TICFAC_CACHE_DIR`, in the fixture's own unrelated temp checkout, with the argv asserted to be
  `node <cache>/harness/...` and never the `register.mjs` source shape. That is "no npm install
  anywhere" demonstrated rather than claimed. The staleness half was checked by breaking it:
  appending a line to `harness/src/local/main.ts` made
  `TestLocalHarnessBundleMatchesItsSources` fail, naming both hashes and `make harness-bundle` as
  the remedy. Note where that evidence lives: the end-to-end half is
  `shorttest.EndToEnd` and skipped under `-short`, so `make gate` does **not** run it — CI's
  `go-test` job does, because it installs Node 22 (`ci.yml:209-213`, `:241-244`). The staleness
  test is pure Go and does run in the gate.
- **[A5] Met.** `make gate` exits 0 on this tree (gofmt, `go vet ./...`, the whole-repo short
  suite, `internal/reconcile` included). CI is green on the last **code-bearing** commit of the
  branch, `7287ad5` — push run `37771454557`, every job `success`: go, go lint, go race, go test
  (packages), go test (reconcile 1/3, 2/3, 3/3), typescript, contracts, plan, release config. The
  `pull_request` run on the same sha is `skipped` and superseded by it; a skip is not a pass, and
  the pass is the push run. Everything on the branch after `7287ad5` is the run's own
  bookkeeping: `git diff --stat 7287ad5..epic/bo9 -- . ':(exclude).tick' ':(exclude).ticfac'` is
  empty.

No exception to any acceptance item was claimed on bo9's own record: the epic carries no notes,
and the run has no `amendments/` directory, so there is nothing unconfirmed to report.

## What the run absorbed

Fifteen findings, and **none entered the epic**. Fourteen were decided `backlog-default` — the
reporter rated them `low` or `medium`, and of those only ihr named a done item — and one,
**ifw**, by the run's live-run rule (`basis: rule`, placement `next-run`): its remedy is a real
run, which no worker inside the epic can perform. Every one is a parentless backlog tick, open:

- from g2s (round 1): **w75**, **50j**, **avo**, **jtj**, **rgx**, **359**
- from 52h attempt 7: **ihr**, **5p5**, **hdb**, **dd4**, **eus**
- from two (round 2): **656**, **ifw**, **k2n**, **8qr**

Nothing was deferred to a person and nothing was discarded. Neither reviewer named a blocking
finding, so the epic's shape never changed from a finding — which is why the acceptance above is
scored against the epic's original five items and not an amended set.

## The close-out that was rejected

Attempt 7 did the reading and wrote the retro, but committed nothing: it put the retro in its
report and proposed the compacted learnings as a finding's `protected_change`, which is the
channel the pre-commit hook's own refusal message names for a `.tick/` file. The run rejected it
`no-commits` — for `closeout-epic`, `subprocess.NoCommitsIsFailure` is true, because "its
deliverable is the record it leaves in the repository — the retro, and the learnings the boundary
permits it to write". The waiver that would have covered it,
`Reconciler.acceptProtectedDelivery`, returns early for any role that is not `implement-tick`
(`internal/reconcile/protected_changes.go:414`).

The committable deliverable the role actually has is this file: `docs/<epic>-closeout-retro-<date>.md`
is the convention the v5t, umq and ilz close-outs already follow, and it is outside `.tick/`. The
role's prompt never names it, and says instead "Your report is the only channel that is read" —
so a close-out that follows its prompt literally is rejected by a rule it was never told. Filed
below.

## The learnings

The bo9 compaction of `.tick/learnings.md` is **already filed and still pending**: finding
`18505c55` from attempt 7, carrying the whole new 150-line file as a `protected_change`, which
this close-out's close applies onto the epic branch as its own labelled commit for the merger to
review. `applyProtectedChanges` reads every finding left `proposed` regardless of which attempt
proposed it, so attempt 7's rejection did not orphan it.

It is not re-proposed here, and could not be: `PutFinding` dedupes on
(kind, title, target) — not the body — and on a repeat "the ORIGINAL stands and nothing new is
proposed". A proposal under a different title would be a second whole-file `content` replacement
of the same path, and `applyProtectedChanges` walks findings in sha256-key order, so which one
won would be hash luck. Filed below.

Two lessons therefore post-date that compaction and are recorded here for the next one to pick
up:

- **Problem:** g2s judged bo9 READY at `ed3f1d2f`, then a fold of main changed the tree under the
  verdict and round 2 (`two`) had to re-run the whole review. **Cause:** a review's verdict is
  about a tree, and the run folded its base after taking one. **Rule:** fold the base BEFORE the
  final review, or expect to pay for a second one.
- **Problem:** bo9's close-out was rejected `no-commits` for following its own prompt.
  **Cause:** the role's recorded commit rule and the role's prompt name different deliverables,
  and the protected-change waiver is gated to `implement-tick`. **Rule:** a close-out commits its
  retro document (`docs/<epic>-closeout-retro-<date>.md`) as well as reporting it.

## What is left open

- **The live proof of [A1]** — **ihr**, the eighth epic to close without the run its acceptance
  names, and the first where a hop-by-hop read stood in for it. It needs the merge first, since
  the factory deploys main only. **w75** (a test for `common.sh:162`) and **50j** (a queued cloud
  submission drops the named config at ignition) belong with it, and **ifw** is already routed to
  the next run.
- **The new embed root and the deploy decision** — **656**: `deploy-factory.yml`'s `paths` list
  enumerates `harness/` subpaths individually and does not include `harness/embed`, so a commit
  whose only shipped change is a regenerated bundle is decided "nothing that ships changed" and
  skipped, leaving orchestrator containers on the old bundle. The staleness gate cannot catch it
  either: it hashes `harness/src`, not the bundle. One path entry fixes it. This is the highest
  consequence item of the fifteen.
- **The skills family's remaining seams** — **5p5** (`skills diff` still demands the name
  `install` and `get` just dropped; reproduced at this close-out: `ticfac skills diff` with no
  argument answers "Exactly one skill name is required"), **avo**, **k2n**, **8qr**, **jtj**,
  **hdb**.
- **`make build` now needs node and a pnpm install and rewrites a tracked file** — **rgx**.
  Nothing automated depends on it; the cost falls on a human.
- **Two findings about the factory's own machinery** — **dd4** (the tk boundary guard reports
  refused READS as violations: four of bo9's six ticks carried a boundary-violation banner and
  only 0ek's `tk decide` was a write) and **eus** (the envelope's `decisions` field has no report
  block that fills it, so 0ek's four design decisions reached the record only as prose).
