# RESULT — wgi: Split the runners config

**STATUS: DONE** (test evidence at the bottom; final `make test-short` run on the finished tree, green).

## The decision this tick existed to make

**Who parses `.tick/runners.toml`: two readers over one file, each pinned to its own tables — today's shape, made honest — with ONE writer staying: ticks' migrator (`tk config migrate`).**

Logged with `tk decide av8 --class architecture` (activity 2026-09-10 17:07). The reasoning, weighed against the standing `tk --json` + pinned-bundle rule:

- **A single parser in ticks reached through `tk --json`** was rejected: `tk --json` is the *tracker* contract. Making it also carry execution config would keep the execution tables' semantics in ticks forever — extraction in name only — and would couple every config read to whatever tk binary happens to be installed, rather than to the pinned, byte-verified bundle. The bundle is precisely the mechanism that makes a direct file read honest: the format is published (schema + contract fixtures), so reading it is the same kind of coupling as vendoring it, and the cloud factory's `repo-config.ts` is already a legitimate second reader of the shared format by exactly that argument.
- **Two files** were rejected: they double the versioned documents, force a migration on every repository, split one human-authored document in two, and the migrator would have to write both. Maximal "other side's migrator" corruption surface.
- **Two readers over one file** is what shipped. The version stays a property of the ONE document, enforced by both readers' gates before shape. ticks' migrator remains the only writer — so "a file written by the other side's migrator" cannot exist: ticfac never writes the file, and what the one writer writes is pinned as a committed fixture both sides must load (below).

A second decision, `tk decide wgi --class library-choice`: the reader decodes with `github.com/BurntSushi/toml v1.6.0` — the same decoder and version ticks' reader uses (first dependency in ticfac's go.mod; recorded because the module was dependency-free until now).

## What was built

**`internal/herd/config` — ticfac's execution-half reader** (~3,400 source lines + tests), lifted from ticks' `internal/herd/config` at the av8 base and cut along the domain line:

- `types.go`, `load.go`: the execution tables (version, orchestrator, orchestration, roles + tiers, testing, evidence, environment, sandbox) with the full shape validation, version gate first, `ValidationErrors`, `UnsupportedVersionError`. The tracker half (Signals/Sweep and their validators, vocabularies, patterns — ~9,500 bytes) was cut out and **stays in ticks**.
- `resolve.go`, `substrate.go`: byte-faithful lifts — role/tier resolution (`Worker`, `Resolve`, `Label`, `ErrNoConfig`) and the substrate decision procedure (`Decide`, `DecideOverride`, `Prober`, `Override`, `ParseOverride`, `Decision`), importing the herdr client lifted by tick 579.
- `doc.go` is new: the split, the decision, one writer / two readers, and the foreign-table rule.

Deliberate divergences from ticks' copy, each called out in a comment: the version gate's upgrade line names *ticfac* where ticks' names tk (each reader names the reader being refused); `RequiredVersion` computes from the tables visible to THIS reader (ticfac never writes, so the divergence is inert); no migrator, no kinds/compile machinery, no schema-doc tests (they need ticks' skill references).

**Foreign-table tolerance, the "made honest" core** (`load.go`, `presentForeignTables`): `[signals]` and `[sweeps]` are skipped as ticks' half — neither validated nor refused — *when present as tables*. The tolerance is keyed on shape, not name: `signals = true` (a scalar squatting on the name) is still refused as an unknown key, and a typo'd *execution* key is always refused. Pinned by `split_test.go`.

**`internal/profile/roles.go` became an adapter, not a parser.** The hand-rolled line reader that knew two keys and silently passed over the rest is gone; `ReadRoles`/`ParseRoles` keep their signatures and delegate to the validated reader, exposing only kind/model (the two fields a Phase 1 profile routes on). This is where the split gets teeth: a runners.toml that fails validation anywhere in the execution half now fails profile resolution — a file tk refuses is a file a run refuses. The reconciler's gate reader (`internal/reconcile/toml.go`) is deliberately NOT replaced: it reads exactly one table and refuses the rest because it is a permission boundary around `sh -c`, not a parser gap (doc.go names this).

Consequence, applied: the format requires `[roles]`, so the five run-fixture gate documents that carried only `[testing.commands]` (`passingGate`, `failingGate`, no_host_path, restart, teardown) now declare `[roles.implement]` mirroring the shipped profile — and `TestTheTargetRepositoriesRolesTableRoutesTheRun`'s fixture became self-contained (a TOML table may be declared once). Resolved models and digests are unchanged; fixtures now honor the format's own rules.

## The migrator obligation, as bytes

`testdata/old-migrator.runners.toml` was produced by the **real** `tk config migrate --write` (the tk build at the av8 base) in a scratch repo that had legacy structured sections in config.md AND routing AND the tracker tables under `version = 1`. The migrator merged the command surface in, preserved the tracker tables, bumped to version 2 — a whole document written by the one writer, both halves in it. `split_test.go` proves: **the execution half loads through ticfac's reader** (every table's values asserted), and the tracker half is intact and decodable (the migrator did not corrupt it) — that is the "still loads correctly in both projects" obligation, executable from this repository; ticks' own `TestMigrate*` suite proves its reader loads its own output, unchanged. ticks itself is **untouched** by this tick (`git status` clean in `~/Development/ticks` at main e123f461).

## internal/sandbox: where the 21 identifiers live

ticks' `internal/sandbox` reads ~21 exported identifiers from ticks' herd/config across six files: `LoadRepo`, `ErrNoConfig`, `FileName`, `Tier`/`TierFrontier`, `Command`, `Worker`, `Prober`, `Decision`, `DecideOverride`, `Override`/`ParseOverride`, `SubstrateEnvVar`, `SubstrateHarness`, `Config`, and the types behind them (`Substrate`, `Detect`, `ProbeResult`, ...). **They all live in ticfac's new `internal/herd/config`, same names, same semantics, byte-faithful to ticks' copies.** The resolution: **sandbox moves with the execution half** — it is execution machinery (it builds the container a worker runs in), its eventual home is ticfac, and the package it needs is now here so that move strands nothing. Sandbox is NOT part of this tick's move, so until that later tick, ticks' sandbox keeps importing ticks' config, which continues to exist until 4l2 retires the execution machinery there.

## Parity, upgraded

`internal/contracts/parity` now asserts the fixtures against the **real reader**, not just against data-driven re-implementations:

- `runners_config_test.go`: the image and max_parallel accept/refuse lists, the exact-length boundary, the refusal messages, and the typed-TOML refusals — all through `config.Parse` on whole documents (`TestTheRealReaderAgreesWithTheContract`).
- `toml_cases_test.go`: the sandbox-image case table is now fully executable (accepted cases load and parse out the pinned image; refused cases are refused naming `sandbox.image`), and the signal/sweep case documents — *including the ones ticks' reader refuses* — must load here as foreign tables, which is the split itself as an executable assertion.

**Follow-up filed as tick 9t0** (blocked by wgi, parent av8): the bundle does not yet pin the split's table enumeration or the substrate enum; those need ticks-side bundle changes plus a deliberate re-pin — a person's job, never a hand-edited fixture.

## Obligations handed to ticks (for 4l2's plan)

When ticks retires its execution machinery, its loader must come to tolerate the execution tables the way this reader tolerates `[signals]`/`[sweeps]` — mirror of the rule above — or a repository's legal execution config starts failing `tk` at author time.

## Incidents during the run (both self-inflicted, both recovered, no content change)

1. An empty shell variable sent my scratch-fixture commands into the worktree instead of the scratch repo, committing in-progress files as a junk "base" commit (author `worker@example.com`). Never pushed; `git reset a085b31` restored the branch, tree intact.
2. The same accident set the worktree's *local* git identity, which the follow-up tick briefly inherited as owner. Local identity unset (back to the repository default) and 9t0's owner corrected; the append-only activity log keeps the honest trace.

## Test evidence

- `go test ./internal/herd/config/ ./internal/profile/ ./internal/contracts/...` green (local, repeatedly).
- ticks untouched at main e123f461; its config suite still passes there (run for evidence, not modified).
- `make test-short` (GOTEST_TIMEOUT=45m, as required — never a bare `go test`), on the final tree, after all edits: **green** (reconcile ~888s at the wave-1 gate; the host was under 40–80 load from sibling wave workers, so wall clocks ran long).

Sandboxed decision ladder: 2 decisions logged (`tk decide av8`, `tk decide wgi`); 1 follow-up tick created (9t0); no money, credentials, live systems, or scope changes involved.
