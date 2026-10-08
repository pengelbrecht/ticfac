# two — re-review of epic bo9, as integrated (round 2)

Read-only review of `epic/bo9` at `e90a6ac0` (the frontier my checkout holds;
`0547fcdd` plus the three bookkeeping commits the run wrote after it) against
the base the epic now sits on, `origin/main` at `79405e74` — which is the
merge-base, so main is wholly contained and `git diff origin/main HEAD` is the
epic's own diff.

## What round 2 exists for

Round 1 (g2s, decision 8) judged the epic READY at `ed3f1d2f`. The only tree
change since is the base fold at `7287ad5f`:

    git diff ed3f1d2fc915 HEAD -- . ':(exclude).ticfac' ':(exclude).tick'
      cloudflare/src/run-room.ts         | 39 ++++-
      cloudflare/test/run-routes.test.ts | 65 ++++++++

Both halves are PR #266 (`fix/xvk-promotion`, "a repeat `--queue` widens the
standing entry"), arriving from main — not the epic's own work. The fold is
clean in both directions: the epic's own 21 added lines in
`cloudflare/test/run-routes.test.ts` (the two `config`-into-Workflow-params
cases, at :346 and :355) and main's 65 (`#widenQueued`'s cases) are both present,
and `cloudflare/src/runs.ts` — the file the epic rewrites hardest — is
untouched by the fold.

Does #266's change interact with ba4's new `config` field? It cannot: the
widening path (`run-room.ts:917-931` → `#widenQueued` at :1458-1478) merges
`waits_for_release`, `window_ms` and `expires_at` on a `QueuedRecord`, and
`config` is not a field of `QueuedSubmission` at all. The known gap there (a
queued submission drops the named config at ignition) is unchanged by the fold
and is already filed as backlog from round 1. So nothing in the fold moves the
verdict, and the rest of this review is my own pass over the epic's diff.

## The acceptance criteria, item by item

**[A1] `ticfac run <epic> --cloud --config claude` runs the epic on the claude
config (ba4).** The chain is complete and each hop is spelled in the diff:

| hop | where | test |
| --- | --- | --- |
| flag reaches the submission | `internal/cli/run_cloud.go:214`, `:326` (`Config: runConfig`) | `TestRunCloudForwardsTheNamedConfig` (asserts `post.Body["config"] == "claude"`) |
| the old refusal is gone | `internal/cli/run.go:411-418` | same test (exit 0 where it was exit 2) |
| the route parses it | `cloudflare/src/runs.ts:377-385`, `:571` | `run-artifacts.test.ts` "a submission's own named config" (3 cases, including `123`/`""`/`"   "`/201 chars refused) |
| submission → Workflow params | `runs.ts:998` (`submitRun`'s `startRun` call), `:782` | `run-routes.test.ts:346`, `:355` (present and absent) |
| params → container env | `run-workflow.ts:1853-1857`, `sandbox.ts:808` | `run-artifacts.test.ts` "carries a submission's named run config as `TICKS_CONFIG`" + the omit case |
| env → shell variable | `image/common.sh:162` | **none** |
| shell variable → argv | `internal/factory/ticfacentrypoint.go:341-345` | `TestOverrideForwardsTheNamedRunConfig`, `TestOverrideLeavesOffTheConfigFlagWhenUnset` |
| argv → run-epic | `internal/cli/cli.go:114` (`--config` exists and feeds `RunConfig`) | pre-existing |

I checked the one untested hop by name rather than by test: `sandbox.ts:808`
writes `env.TICKS_CONFIG`, `common.sh:162` reads `${TICKS_CONFIG:-}`,
`sandboximage.go:61` declares the same string. The spellings match, and
`ticfacentrypoint_test.go:225` sets `run_config=""` by hand in its own
harness, so the Go test proves the argv but not the read. Round 1 filed that
as medium (backlog); I reach the same severity and do not re-report it.

A1's literal wording is an observed run, and no tick performed one — the
close-out (52h) filed that as a medium proposed-tick. I do not escalate it:
every hop but one shell assignment is tested on both sides, and that
assignment's two ends I verified by name above.

**[A2] `ticfac skills diff` reports an installed copy older than the binary
with the upgrade command (g5f).** Exercised, not just read. Built the binary
and ran it against an install whose stamp I rewrote to `v0.1.0`:

    drift against the embedded bundle: installed=v0.1.0 bundle=dev
    upgrade with `ticfac skills install ticfac`
    ticfac skills diff: drift detected — fix with `ticfac skills install ticfac`
    exit:1

`internal/skills/diff.go` + `diff_test.go` cover missing / fresh /
version-mismatch / unstamped / added-removed-changed; `skills_test.go` covers
the command's report, its `--json` document and its exit codes. Met.

**[A3] `ticfac skills get ticfac --full` prints SKILL.md and every references
file (u2g).** Exercised:

    $ ticfac skills get ticfac --full | grep -n '^--- '
    187:--- references/cloud.md ---
    276:--- references/commands.md ---
    389:--- references/holds.md ---

SKILL.md leads (lines 1-186, its own front matter), then all three references
files under headers naming their paths. The bare `ticfac skills get --full`
form (rkk's optional name) prints the same. Met.

**[A4] A local pi-durable run in a repository that is not ticfac, with only
the ticfac binaries and node, dispatches and completes a worker; a stale
embedded harness bundle fails the gate (0ek).** Both halves hold, and I ran
both rather than reading them.

The first half is `TestTheDurableRunnerRunsOnTheEmbeddedBundleWithNoHarness
Checkout` (`internal/exec/subprocess/local_host_e2e_test.go:159-263`), and it
is the right test for the claim: it sets no `$TICFAC_HARNESS_DIR`, never
stats `harness/node_modules`, runs against the fixture's unrelated temp
checkout, and then asserts on the *resolved* argv in the attempt record —
that it contains neither `--experimental-strip-types` nor `register.mjs`, and
that the file node was handed sits under the isolated `$TICFAC_CACHE_DIR`.
Locally, on node v22.23.2: `--- PASS (0.86s)`. It genuinely runs the harness —
the assertion on `harness-ran.txt`'s contents in the worktree can only pass
if the bundled host completed a real tool round.

The second half is `TestLocalHarnessBundleMatchesItsSources`
(`harnessbundle_test.go:17-49`), which recomputes in pure Go the digest
`build-local-bundle.mjs`'s `hashSources` writes, so it costs a file walk and
runs inside `make gate`. It passes, i.e. the committed bundle is in sync with
`harness/src` on this tree.

The wiring that makes the production path the default is sound where I looked
for holes: `executor.go:458-466` gates staging on
`at.HarnessDir == "" && Runner == "pi" && len(RunnerArgv) == 0`, and
`opts.RunnerArgv` is folded in from `$TICFAC_RUNNER_ARGV` at construction
(`executor.go:208-212`), so the override test is not fooled by the
environment. `resolveRunner`'s `def.Argv = piSourceArgv` mutates a struct
copy, and both `insertBeforePrompt` (`nudge.go:207`) and `withModel`
(`runner.go:361`) allocate, so neither the `runners` table nor `piSourceArgv`
can be corrupted across calls.

**[A5] `make gate` passes and CI is green on the epic PR.** The local half,
run in this container at `e90a6ac0`:

| command | result |
| --- | --- |
| `make gate` (gofmt, `go vet ./...`, short suite, whole repo) | exit 0 |
| `make ts-gate` (biome `--error-on-warnings`, `contracts:check`) + `pnpm exec tsc --noEmit` | exit 0 |
| `cd cloudflare && pnpm test` (vitest) | 80 files, **1880 tests passed**, exit 0 |

The CI half I cannot verify: `gh` is not installed in this container, and the
epic's PR is the close-out's own work (52h, rejected, re-queued behind this
review). The three commands above are the gate and both halves CI runs on an
epic branch for the TypeScript side, so I have no reason to expect CI to
disagree.

## Judging the tests, not just the code

The pattern across the epic is that each behaviour is asserted at the level it
lives, not one level up where a green suite would say nothing:

- ba4 is tested at six of its seven hops, on both the present and absent
  branch at each. The one with no test is named above.
- 0ek's acceptance is tested as acceptance — no `$TICFAC_HARNESS_DIR`, no
  `node_modules` stat, and an assertion on the resolved argv so a regression to
  the source shape fails loudly rather than passing on a dev box that happens
  to have `harness/node_modules`.
- g5f/u2g/rkk: `internal/skills/diff_test.go` holds the five drift shapes as
  unit tests; `internal/cli/skills_test.go` holds the command's report,
  exit codes and `--json` for all three ticks, including the
  `install`/`get`-with-no-name and too-many-args cases rkk introduced.
- The doctor node check keeps a real probe (`TestDoctorProbesTheRealNode`
  restores the live seam) rather than only the canned one, and
  `nodeMajorVersion` is table-tested on the real CLI's `v22.23.2` spelling.

I found no green-but-vacuous test in this diff. The staleness gate is
deliberately narrower than "the bundle is correct" — it compares the sources
hash, not the bundle — and its own doc comment says so; that trade is already
filed as backlog from round 1.

## Findings

Four, all new (none duplicates the eleven already filed as backlog by g2s and
52h), none a reason the epic is not ready.

The first is the one I would want fixed soonest, because it is the kind that
is silent: 0ek added a new `go:embed` root, `harness/embed/`, and
`deploy-factory.yml`'s shipped-paths list — the one place that decides whether
the factory ships a commit at all — enumerates `harness/` subpaths one by one
and does not include it. I verified it with the workflow's own pathspec:

    git diff --name-only origin/main HEAD -- <the workflow's paths> | grep harness
    harness/src/local/main.ts
    internal/cli/cloud_harness_preflight.go
    internal/exec/subprocess/harnessbundle.go

`harness/embed/local-main.bundle.mjs` is absent from that output while being
embedded into the binary the deploy cross-compiles into the image. This epic's
own merge deploys regardless (`harness/src` and `internal/` both changed); the
gap bites the next commit that changes only the bundle.

CI's own affected-package plan is fine, for the record — `ci-plan.sh:52`
builds its `embedded` set from `go list -f '{{range .EmbedFiles}}'`, so a
bundle-only diff maps to the root package correctly. It is only the deploy
decision that enumerates by hand.

```findings v2
[
  {
    "kind": "defect",
    "title": "deploy-factory's shipped paths miss harness/embed, the new embed root",
    "severity": "medium",
    "body": "Tick 0ek added harness/embed/local-main.bundle.mjs as a go:embed root of the root package, so it ships inside the ticfac binaries the deploy cross-compiles into the image. deploy-factory.yml's `paths` array enumerates harness/ subpaths individually (harness/src, harness/package.json, harness/pnpm-lock.yaml, harness/pnpm-workspace.yaml, harness/tsconfig.json) and does not include harness/embed, so a commit on main whose only shipped change is a regenerated bundle — a rebuild under a newer esbuild, a change to harness/scripts/build-local-bundle.mjs's define/target, a hand-fix — is decided 'nothing that ships changed' and skipped, leaving the factory's orchestrator containers running the previous bundle. The staleness gate cannot catch it either: it hashes harness/src, not the bundle, so a bundle-only change passes make gate. The workflow's own comment calls the list 'deliberately broad: a needless deploy costs ten minutes, a missed one is a factory running old code'. One path entry fixes it.",
    "evidence": ".github/workflows/deploy-factory.yml:281-290 (the paths array; its harness entries are :284-285); `git diff --name-only origin/main HEAD -- <those paths> | grep harness` prints harness/src/local/main.ts but not harness/embed/local-main.bundle.mjs"
  },
  {
    "kind": "defect",
    "title": "run --cloud --config is silently dropped when it attaches to a live run",
    "severity": "low",
    "body": "runCloudCommand returns from the `liveness.Alive` branch before it ever reaches submitCloudRun, so `ticfac run <epic> --cloud --config claude` against an epic that already has a live cloud run attaches to that run — started on whatever config it was submitted with — and the flag has no effect and no mention. The prose on that branch says only 'cloud run <id> is alive (<reason>) — attaching'. Before this epic the same command was refused with exit 2 and a sentence explaining what a submitted run selects by, so the flag went from loud-and-useless to quiet-and-useless on this one path. The start path does say '(run config: claude)'; the attach path could say the flag does not apply to a run already running.",
    "evidence": "internal/cli/run_cloud.go:192-196 (the `case liveness.Alive` branch and its return) against :214 (the submit that carries *fl.config) and :201-211 (the start path's own prose)"
  },
  {
    "kind": "defect",
    "title": "skills get --json serves two document shapes under one schema id",
    "severity": "low",
    "body": "`skills get --json` emits {skill, file, content} and `skills get --full --json` emits {skill, files:[{path,content}]}, both stamped ticfac.skills-get.v1. A reader that keys off the schema id — which is the whole point of stamping one — has to branch on which flag the caller passed to know whether `content` or `files` exists. Everything else in this tree that changed an agent document's shape took a new id or added a field rather than replacing two. Either give --full its own id, or keep `files` as the one shape and have the bare form emit a single-element array.",
    "evidence": "internal/cli/skills.go:146-156 (the bare shape) and :177-186 (the --full shape), both agentSchemaID(\"skills-get\") at :152 and :181; internal/cli/skills_test.go:344 asserts the id on the --full shape, and no test covers the bare form's --json document at all"
  },
  {
    "kind": "defect",
    "title": "skills is in parseOnlyCommands but only install honours parseOnly",
    "severity": "low",
    "body": "g5f added \"skills\" to parseOnlyCommands and a `if parseOnly { return exitSuccess }` guard to skillsInstallCommand, which is what lets TestEveryPrintedRemedyIsACommandTheCLIAccepts run `ticfac skills install <instantiated>` without a real install. skillsDiffCommand and the get body have no such guard, so the moment a backticked `ticfac skills diff …` or `ticfac skills get …` remedy appears in internal/cli or internal/reconcile source, that test will execute the real command against the live checkout and fail on its exit 1 (drift) or exit 3 (no repo) rather than on a usage error. Latent today — the only backticked skills remedies in scanned source are the three `ticfac skills install %s` lines skillsDiffReport prints — but the trap is set by this diff, and the guard belongs beside the one install already has.",
    "evidence": "internal/cli/parseonly.go:16 (\"skills\": true) and internal/cli/skills.go:308-310 (the only guard) against :477 (skillsDiffCommand, none) and :131-196 (the get body, none); the test is internal/cli/remedy_test.go:68-84"
  }
]
```

## What I did not find

I went looking specifically for things the fold could have broken and for
holes round 1 would not have had reason to check, and came back empty on all
of these: the `runners`-table mutation hazard in `resolveRunner`; a stale
committed bundle (the hash gate passes on this tree); `$TICFAC_RUNNER_ARGV`
being read somewhere the staging condition cannot see; `run-epic` lacking the
`--config` the entrypoint now passes it (`cli.go:114` has it); the
`TICKS_CONFIG` spelling differing between `sandbox.ts`, `common.sh` and
`sandboximage.go` (all three agree); and `make build`'s new `harness-bundle`
dependency reaching an automated consumer (nothing in `.github/`,
`image/`, `.goreleaser.yaml`, `install.sh` or `.tick/runners.toml` runs
`make build`, so round 1's `low` on that stands).

The eleven findings g2s and 52h filed are all absorbed as backlog with
`gating: false`, none at `high`, and my tick's notes list no DEFERRED
FINDINGS for me to rule on. I re-read the four mediums among them and agree
with the severity on each; none is a reason to hold the epic.

REVIEW-VERDICT: READY

STATUS: DONE
