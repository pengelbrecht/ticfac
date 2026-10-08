# 0ek — harness as a machine prerequisite

Shipped the local pi-durable harness inside the ticfac binary: one esbuild
bundle, go:embedded, run with plain `node` — no checkout of `harness/`, no
`pnpm install`, on a machine that only has the ticfac binaries and Node.

## What changed

- **`harness/scripts/build-local-bundle.mjs`** (new): bundles
  `harness/src/local/main.ts` with esbuild — pi-durable, pi-ai and chord
  inlined — into `harness/embed/local-main.bundle.mjs`, and writes a sha256
  over `harness/src` (+ `package.json`, `pnpm-lock.yaml`) to
  `harness/embed/local-main.bundle.sources.sha256` beside it. Both files are
  committed (not gitignored — `harness/.gitignore`'s `dist/` entry is why the
  output lives under `embed/`, not `dist/`).
- **`harness/src/local/main.ts`**: `ensureDependencies()` now no-ops when
  `globalThis.__TICFAC_HARNESS_BUNDLED__` is set (an esbuild `--define`,
  true only in the bundle) — the bundle inlines `@earendil-works/*`, so
  there is no `node_modules` to check for, and the unbundled dev path
  (`$TICFAC_HARNESS_DIR`, type-stripped from source) is unchanged.
- **`embedded.go`** (module root): `//go:embed harness/embed/local-main.bundle.mjs`
  and its sources-hash sidecar, exposed as `LocalHarnessBundleJS()` /
  `LocalHarnessBundleSourcesHash()`, following the existing `HarnessFS`/
  `SandboxFS` pattern.
- **`internal/exec/subprocess/harnessbundle.go`** (new): `cachedLocalHarnessBundle()`
  writes the embedded bytes to `$TICFAC_CACHE_DIR/harness/<sha256[:16]>/local-main.bundle.mjs`
  (or the OS user cache dir, or `os.TempDir()` as a last resort) once per
  distinct build, atomically (temp file + rename), and returns the path.
- **`internal/exec/subprocess/runner.go`**: the `"pi"` table entry's default
  argv is now `node {{harness_bundle}} --config … --message …` — the
  embedded bundle. `piSourceArgv` (the old shape: `node
  --experimental-strip-types --import …/register.mjs …/main.ts …`) is
  selected by `resolveRunner` only when `launch.HarnessDir` is set — i.e.
  only when `$TICFAC_HARNESS_DIR` names a checkout (harness development).
  Added the `{{harness_bundle}}` placeholder and its own "resolved none"
  refusal, alongside the existing `{{harness_dir}}` one.
- **`internal/exec/subprocess/executor.go`**: `harnessDir()` →
  `harnessSourceDir()` (just `$TICFAC_HARNESS_DIR`, empty when unset — no
  more `<repo>/harness` fallback). `Start` resolves
  `cachedLocalHarnessBundle()` into `launch.HarnessBundle` whenever the
  runner is `"pi"`, no `$TICFAC_HARNESS_DIR` and no `TICFAC_RUNNER_ARGV`
  override.
- **`internal/cli/doctor.go`**: a `node` check (`node --version`, floor
  Node 22 — `node:sqlite`, the harness's own storage, needs it), checked
  unconditionally (not cloud-gated: the pi-durable host is a local process
  even inside a cloud run's container). Fix line: install Node 22+.
- **`Makefile`**: `make harness-bundle` (node + harness's pnpm packages,
  regenerates the two embed/ files); `build` depends on it for local dev
  convenience. The declared gate recipe (`gate:`) is untouched — the
  staleness check is a plain Go test, so `make gate` needs neither Node nor
  `make build`.
- **`harness/README.md`**: the `src/local/main.ts` paragraph now describes
  both the production (bundle) and dev (`$TICFAC_HARNESS_DIR`) shapes.
- Tests updated for the new default/launch shape:
  `executor_metering_test.go`, `unit_test.go` (the "pi with no harness" unit
  refusal now asserts the `harness_bundle` placeholder, since production
  defaults to the bundle rather than a directory); `doctor_test.go` (the
  `node` seam, table case, real-probe test, `nodeMajorVersion` unit test).

## New tests

- **`internal/exec/subprocess/harnessbundle_test.go`**:
  `TestLocalHarnessBundleMatchesItsSources` — recomputes the same sources
  hash the build script writes, in pure Go (no node, no esbuild — a file
  walk), and fails when the committed bundle has drifted from
  `harness/src`. Runs under `-short`, so it is part of `make gate`.
  Verified it actually catches drift (temporarily appended a byte to
  `harness/src/local/pi-auth-store.ts`, confirmed the test fails with a
  hash mismatch, reverted, confirmed green again).
- **`internal/exec/subprocess/local_host_e2e_test.go`**:
  `TestTheDurableRunnerRunsOnTheEmbeddedBundleWithNoHarnessCheckout` — the
  tick's acceptance criterion as a Go test: no `$TICFAC_HARNESS_DIR`, an
  isolated `$TICFAC_CACHE_DIR`, the fixture's repo is an unrelated temp
  checkout with no `harness/` of its own (exactly "a repository that is not
  ticfac") — a real supervisor, a real embedded-bundle Node process, a
  scripted model (pi-ai's faux provider), real git and real SQLite storage.
  Asserts the worker completes (`StateSucceeded`, ready-to-merge), its tool
  round's file landed in the worktree, and the resolved `RunnerArgv` names
  the cached bundle path under `$TICFAC_CACHE_DIR` — never
  `--experimental-strip-types` or `register.mjs`.
- `internal/cli/doctor_test.go`: `TestDoctorProbesTheRealNode` (the real
  `node --version` probe, on this dev/CI host), `TestNodeMajorVersionParsesTheRealCLIsOutput`.

## Decisions taken (no `tk` access from a worker; recording here instead)

1. **goreleaser / the sandbox image build are untouched.** The tick's
   description names `make build / goreleaser / the image build` as where
   the esbuild step could land. I wired `make build` (dev convenience) but
   left `.goreleaser.yaml` and `image/Dockerfile` alone: `release.yml` has
   no Node setup at all today, and CI's own `go build ./...` step
   (`ci.yml`) never runs `make` — so the committed `harness/embed/*` is
   already what every build path reads, the same way `profiles/` and
   `skills/` are committed source with no regeneration step of their own.
   Wiring Node into the release pipeline for this would be an untestable,
   higher-risk change the acceptance criteria does not actually ask for
   (it names `make gate passes`, not "goreleaser regenerates the bundle").
2. **Staleness check is a hash comparison, not a rebuild.** Reproducing
   esbuild's own dependency graph in Go, or shelling out to esbuild from a
   test, would either be imprecise or require Node in `make gate` (which
   currently needs none, and per `.tick/runners.toml`/the Makefile's
   `gate:` target, every other tick's local gate still shouldn't). A
   sha256 over `harness/src` + `package.json` + `pnpm-lock.yaml`,
   recomputed in pure Go, is conservative (a harness edit outside
   `src/local`'s own import graph can still mark it stale) but costs
   nothing beyond a file walk, and is what makes "fails the gate" true
   without adding a new machine prerequisite to every tick in this repo.
3. **Cache key is the bundle's own content hash** (first 16 hex chars of
   its sha256), under `$TICFAC_CACHE_DIR/harness/<hash>/…` (falling back to
   `os.UserCacheDir()`, then `os.TempDir()`). Two ticfac binaries built
   from different commits cache side by side rather than racing to
   overwrite one file; a cache write failure is a hard error for the
   attempt (the cache is not optional for correctness — it is the only way
   the bytes reach disk for `node` to run), not for the executor as a
   whole.
4. **`doctor`'s `node` check runs unconditionally**, not only under
   `--cloud`: the pi-durable host is a local process even inside a cloud
   run's container, so a run that never touches the cloud substrate still
   needs it.

## What I ran

- `harness/`: `pnpm install --frozen-lockfile --prefer-offline`, `pnpm lint`,
  `pnpm typecheck`, `pnpm test` (`vitest run` × 2 configs) — all green, no
  regressions from the `ensureDependencies()` guard or the README edit.
- `go build ./...`, `go vet ./...`, `gofmt -l .` (minus `contracts/`) — clean.
- `go test ./internal/exec/subprocess/... ./internal/cli/...` (whole
  packages, no `-short`) — green, including both local pi-durable e2e tests
  (the pre-existing `$TICFAC_HARNESS_DIR` one and the new embedded-bundle
  one) and the full `doctor` suite.
- `make gate` (whole repo, `GOTEST_PARALLEL=4 GOFLAGS=-p=2` per this repo's
  shared-host guidance) — green.
- Negative control: hand-edited a harness source file, confirmed
  `TestLocalHarnessBundleMatchesItsSources` fails with a hash mismatch and
  names the fix, reverted, confirmed green.

## What the next tick needs to know

- Editing `harness/src/local/main.ts` or anything it imports (or bumping
  `@earendil-works/*` in `harness/package.json`/`pnpm-lock.yaml`) now needs
  `make harness-bundle` run and its output (`harness/embed/*`) committed —
  the gate catches a forgotten rebuild, but the fix is always the same
  command.
- `$TICFAC_CACHE_DIR` is a new operator-preference env var, same layer as
  `$TICFAC_HARNESS_DIR`; unset, it resolves to the OS user cache dir.
- No findings outside this tick's scope.

```findings v2
[]
```

STATUS: DONE
