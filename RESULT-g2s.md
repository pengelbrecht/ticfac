# g2s — final review of the operator surface follow-ups diff (epic bo9)

Read as the integrated tree: `epic/bo9` against `990bb6eb` (the merge base with
`main`, and the `source_sha` the run's provenance records). 40 non-bookkeeping
files, 55,286 insertions — 53,432 of which are the one generated file,
`harness/embed/local-main.bundle.mjs`.

## What I ran

| check | result |
| --- | --- |
| `gofmt -l .` (less `contracts/`) | clean |
| `go vet ./...` | exit 0 |
| `make gate` (`GOTEST_PARALLEL=4 GOFLAGS=-p=2`, `nice -n 19`) | **exit 0** |
| `make ts-gate` | **exit 0** (biome 1 info, pre-existing, in `dashboard` code outside the diff) |
| `pnpm exec vitest run test/run-artifacts.test.ts test/run-routes.test.ts` | 108 passed |
| `go test -run TestTheDurableRunnerRunsOnTheEmbeddedBundleWithNoHarnessCheckout ./internal/exec/subprocess/` (NOT `-short`) | PASS |

I could not read CI: this launch has no `gh` on PATH (`gh: No such file or
directory`), so A5's second half — CI green on the epic's pull request — is not
something I can report on. It is the close-out's own gate (AGENTS.md: "the
close-out waits for CI on the epic branch's head"), and the diff changes the
`Makefile`, which per `.github`'s plan job makes that PR run the whole suite
rather than the affected-only subset. A5's first half I ran myself, above.

## The acceptance criteria, as integrated

**[A1] `ticfac run <epic> --cloud --config claude` runs the epic on the claude
config (ba4).** Verified by reading every hop, because no factory is reachable
from here:

1. `internal/cli/run.go:411` — the old refusal is gone; `--cloud` no longer
   rejects `--config`.
2. `internal/cli/run_cloud.go:214` — `submitCloudRun(..., *fl.config, ...)`;
   the submission struct gained `Config string \`json:"config,omitempty"\``
   (`run_cloud.go:317`). `TestRunCloudForwardsTheNamedConfig` asserts the POST
   body carries `"config": "claude"`.
3. `cloudflare/src/runs.ts:382` — `parseSubmission` reads it through the same
   `text()` validator as every other free-text field (empty, whitespace-only,
   non-string and >200 chars all refused; tests in `run-artifacts.test.ts`).
4. `runs.ts:998` → `startRun` → `runs.ts:785` → Workflow params. Asserted by
   `run-routes.test.ts` ("carries a named run config into the Workflow params",
   and the negative control).
5. `cloudflare/src/run-workflow.ts:1853` — `params.config` into
   `orchestratorEnv`, only on the non-`review` branch (correct: the review job
   routes through the worker ladder, not `run-epic`).
6. `cloudflare/src/sandbox.ts:808` — `TICKS_CONFIG`. Asserted both ways in
   `run-artifacts.test.ts` ("the image contract").
7. `image/common.sh:162` — `run_config="${TICKS_CONFIG:-}"`, a top-level
   assignment in the file `image/entrypoint.sh:48` sources before
   `start_harness` is called (`entrypoint.sh:791`, `:802`). `set -u` is on in
   `entrypoint.sh`, so this definition is load-bearing, and it is present.
8. `internal/factory/ticfacentrypoint.go:338` — `cmd+=(--config "$run_config")`
   when non-empty, with `"$epic"` moved after it so the flag cannot be read as
   a positional. `TestOverrideForwardsTheNamedRunConfig` and
   `TestOverrideLeavesOffTheConfigFlagWhenUnset` cover both.

The chain is correct. Hop 7 is the one with no test (finding 1).
`--cloud-workers --config` still forwards the same value
(`run_cloud_workers.go:227`), and `preflightCloudHarness`'s subscription half
now asks its question of the config a `--cloud` run selects too
(`cloud_harness_preflight.go:182`), which it could not before.

**[A2] `ticfac skills diff` reports an installed copy older than the binary with
the upgrade command (g5f).** Verified against a binary I built from this tree
(`go build ./cmd/ticfac`), in a throwaway git repo with `.claude/skills/`:

    not installed (run `ticfac skills install ticfac` to install it)
    ticfac skills diff: drift detected — fix with `ticfac skills install ticfac`   exit 1

    no drift: matches the embedded bundle (version dev)                            exit 0

    drift against the embedded bundle: installed=v0.0.1 bundle=dev
    upgrade with `ticfac skills install ticfac`                                     exit 1

`internal/skills.Diff` reports a missing dir as `Installed: false` rather than
an error, and `Drift()` covers stamp mismatch, added, removed and changed
files; `diff.go`'s target detection mirrors `install`'s exactly (same
convention dirs, same `exitNoRepo`/`exitNotFound`/`exitGeneric` table). The
report always ends in the one command that fixes it, which is what the
criterion asked for.

**[A3] `ticfac skills get ticfac --full` prints SKILL.md and every references
file (u2g).** Verified on the same built binary:

    $ ticfac skills get --full | grep -n '^--- '
    187:--- references/cloud.md ---
    276:--- references/commands.md ---
    389:--- references/holds.md ---

SKILL.md leads, the three references files follow under headers naming their
paths, and `--full --json` carries the same bytes as a `files: [{path,
content}]` array. The ordering is incidentally rather than deliberately correct
— finding 3.

**[A4] A local pi-durable run in a repository that is not ticfac, with only the
ticfac binaries and node, dispatches and completes a worker; a stale embedded
harness bundle fails the gate (0ek).** Both halves hold, and I checked both
rather than trusting the tests:

- `TestTheDurableRunnerRunsOnTheEmbeddedBundleWithNoHarnessCheckout` passes.
  It sets no `$TICFAC_HARNESS_DIR`, points `$TICFAC_CACHE_DIR` at a temp dir,
  runs a real supervisor and a real `node` child against a temp repo with no
  `harness/` of its own, and then asserts the recorded argv: `node <cached
  bundle>`, never `--experimental-strip-types` or `register.mjs`. That is the
  criterion, at the only level a Go test can reach it.
- The staleness gate: I appended a line to `harness/src/local/main.ts` and ran
  `TestLocalHarnessBundleMatchesItsSources`. It failed, naming both hashes and
  `make harness-bundle` as the fix, and I restored the file (`git diff` on it
  is empty).
- The bundle is reproducible. I ran `pnpm install --frozen-lockfile` under
  `harness/` and `node scripts/build-local-bundle.mjs`: the regenerated
  `embed/local-main.bundle.mjs` is **byte-identical** to the committed one and
  the sources hash is unchanged
  (`18d94fbdda220a64eb63cf9ae5565b47aa299952c87396c9c19d114e8778b4d9`). So the
  committed artefact really is what the committed script builds from the
  committed sources. I removed `harness/node_modules` afterwards (it is
  ignored by `harness/.gitignore`); the working tree is clean.

Two design points I checked rather than assumed. `harnessRoot` in
`harness/src/local/main.ts` — which is garbage in the bundle, because
`import.meta.url` is the cache dir — is used *only* inside
`ensureDependencies`, which the `__TICFAC_HARNESS_BUNDLED__` define short-
circuits; nothing else reads it. And `resolveRunner`'s `def.Argv =
piSourceArgv` mutates a *copy* of the map value, and both `insertBeforePrompt`
and `withModel` allocate, so the package-level `piSourceArgv` is never
appended into. The doctor half is there too (`node` check, Node 22 floor for
`node:sqlite`, real probe exercised by `TestDoctorProbesTheRealNode`), and
`doctor` is not run by the image or by CI, so the new hard check cannot wedge a
container boot.

**[A5] `make gate` passes and CI is green on the epic PR.** `make gate` exit 0,
run here. CI: see "What I ran" — out of reach from this launch, and the
close-out's gate by design.

## Integration, specifically

Two conflict resolutions in this run both landed in `internal/cli/skills.go`
(decisions 6 and 7: rkk×g5f over `skills.go`, `skills_test.go`, `SKILL.md` and
`references/commands.md`; then u2g×rkk over `skills.go`). A resolution dropping
one tick's behaviour is the failure mode that matters here, so I exercised all
three on the built binary rather than reading the merge: `skills diff` (g5f),
`skills install`/`get` with no name (rkk), and `get --full` (u2g) all work, and
`SKILL.md` and `references/commands.md` carry both the short-form rewrite and
the `diff` paragraph. Nothing was lost.

`parseonly.go` gained `"skills": true` and `skillsInstallCommand` gained the
`parseOnly` early return — needed because `skills.go` and `diff.go` now print
`` `ticfac skills install %s` `` as a remedy, which `remedy_test.go` runs
through the real parser. Every printed `ticfac skills …` remedy in the tree is
an `install`, so the one guard is the right one.

No `internal/reconcile/` change in the diff, so AGENTS.md's wait-for-PR-CI
exception does not apply to this epic.

## Judgement

The epic does what it said it would. Everything I found is backlog: one real
test-coverage gap on a hop that is nonetheless correct today, one latent
API-surface gap on the queued-submission path that no shipped command can
reach, and three smaller things.

```findings v2
[
  {
    "kind": "defect",
    "title": "No test covers common.sh reading TICKS_CONFIG into run_config",
    "severity": "medium",
    "body": "TestOverrideForwardsTheNamedRunConfig declares run_config=\"claude\" by hand in its own harness script, so it proves the override's argv but never image/common.sh's run_config=\"${TICKS_CONFIG:-}\" — the single link between the Worker's orchestratorEnv output and the entrypoint's argv. Every other hop of ba4's chain has a test on both sides; this one has none, and internal/sandboximage's fixture (f.env[EnvModel], f.env[EnvHarness]) is exactly the level that could assert it. A rename or typo on either side would ship a green gate while every `run --cloud --config <name>` silently fell back to the epic's label and the declared default. sandboximage.EnvConfig is declared and referenced by no Go code at all, which is the same gap seen from the other end.",
    "evidence": "internal/factory/ticfacentrypoint_test.go:225 declares run_config=\"\"; image/common.sh:162 is the untested line; internal/sandboximage/sandboximage.go:61 declares EnvConfig, `grep -rn EnvConfig --include=*.go` finds no non-test use"
  },
  {
    "kind": "defect",
    "title": "A queued cloud submission drops the named config at ignition",
    "severity": "medium",
    "body": "submitRun parks a contended submission with room.queueSubmission, and that call does not carry submission.config — so an entry that ignites later from the RunRoom's alarm starts a run on the epic's label and the declared default instead of the config its submitter named. The same block's own comments explain why the budget and the credential grade ARE parked (\"a budget that survives the submission but not the queue is a run the operator believes is bounded and is not\"); config is that class of value. Latent today rather than live: `ticfac run --cloud` always sends queue:false, and `ticfac cloud run`, the only surface with --queue, has no --config to send. The fix is one field on QueueSubmissionRequest/QueuedSubmission and one line at each end, beside the ones already there.",
    "evidence": "cloudflare/src/runs.ts:1030-1058 (the queueSubmission call, no config), against cloudflare/src/runs.ts:998 (the started path, which does carry it); cloudflare/src/run-room.ts:146-189 (QueuedSubmission) and :190-209 (QueueSubmissionRequest)"
  },
  {
    "kind": "defect",
    "title": "skills get --full puts SKILL.md first only by lexicographic luck",
    "severity": "low",
    "body": "`skills get --full` prints skills.Paths(name) in order and writes the \"--- <path> ---\" separator only for i > 0, so the contract u2g states — SKILL.md, then every references file under a header naming its path — holds only because skills.Paths sorts lexicographically and 'S' (0x53) sorts before 'r' (0x72). A top-level file added to the bundle that sorts before SKILL.md (README.md, CHANGELOG.md, AGENTS.md) would be printed FIRST and, being index 0, with no header at all — silently inverting the documented order and leaving the reader no way to tell where one file ends and the next begins. Leading with SKILL.md explicitly, or giving every file its header, makes the contract true by construction.",
    "evidence": "internal/cli/skills.go:185-190 (the `if i > 0` separator) with internal/skills/skills.go:111 (sort.Strings); TestSkillsGetFullPrintsEveryFile asserts strings.HasPrefix(out, SKILL.md) and so encodes the same accident"
  },
  {
    "kind": "defect",
    "title": "The ticfac skill's command reference never mentions skills get --full",
    "severity": "medium",
    "body": "u2g exists because an agent cannot read the skill's references/ files out of the binary without installing it first, and --full is the fix. But references/commands.md — the file SKILL.md introduces as \"every command and the flags worth knowing\" — was edited in this epic and still says only \"`ticfac skills get` prints its SKILL.md\". An agent reading the skill is therefore told the opposite of what the epic shipped: that SKILL.md is all it can get. rkk's short form and g5f's diff both reached this file; u2g's flag did not.",
    "evidence": "skills/ticfac/references/commands.md:15-24 (the skills bullet, as rewritten by this epic) and skills/ticfac/SKILL.md:14-20, neither naming --full; internal/cli/skills.go:120-126 is where the flag IS documented"
  },
  {
    "kind": "defect",
    "title": "make build now needs node and a pnpm install, and rewrites a committed file",
    "severity": "low",
    "body": "`build: harness-bundle` makes a plain `make build` run `pnpm install --frozen-lockfile --prefer-offline` under harness/ and regenerate harness/embed/local-main.bundle.mjs before `go build ./...`. That target was pure Go before this epic, so on a machine without node or pnpm, or without network for a cold pnpm store, `make build` now fails where it used to work — and on a machine where it succeeds it rewrites a tracked file as a side effect of building. Nothing automated depends on it (CI, image/Dockerfile, .goreleaser.yaml and image/build.sh all read the committed bundle; `make gate` does not depend on harness-bundle), so the cost falls only on a human, and the Makefile's own comment documents it. Keeping `build` as `go build ./...` and leaving regeneration to the explicit `make harness-bundle` — which the gate test already names as the fix — matches how the rest of the embedded trees (profiles/, skills/) are built.",
    "evidence": "Makefile:44-48; `grep -rn 'make build' .github/ image/ .goreleaser.yaml` finds no consumer"
  },
  {
    "kind": "defect",
    "title": "main.ts's header still documents the pre-bundle argv as the argv",
    "severity": "low",
    "body": "harness/src/local/main.ts opens with \"THE ARGV, as internal/exec/subprocess's runner table spells it\" followed by the node --experimental-strip-types --import <harness>/runtime/register.mjs form. After 0ek the runner table spells `node <bundle> --config … --message …`; the documented shape is now only what $TICFAC_HARNESS_DIR selects. The file's ensureDependencies comment was updated for the bundle and this one was not, so the entry point's own contract statement contradicts the table it cites. harness/README.md gets this right, which makes the stale copy the more misleading of the two.",
    "evidence": "harness/src/local/main.ts:7-14 against internal/exec/subprocess/runner.go:156-164 (the \"pi\" table entry) and runner.go:178-185 (piSourceArgv)"
  }
]
```

REVIEW-VERDICT: READY

STATUS: DONE
