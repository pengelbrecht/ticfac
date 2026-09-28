# Cross-language contracts

The JSON files in this directory are **case tables and pinned surfaces that
more than one language reads**, each owned by whichever product defines the
format. Since tick 4i8 that owner is **ticfac** for everything here except two
files: `tk-json-manifest.json` and `tracker-layout.json` describe ticks' own
formats — the published `tk --json` command surface and the tracker's on-disk
layout — and are vendored from a pinned ticks release through
`contracts.pin.json` at the repository root. They are still read, verified and
re-cut like every other file in this bundle; they are just not *edited* here.
Change them in ticks, cut a bundle version there, and move the pin.

Go reads these files from its parity tests (`internal/contracts/parity`). The
factory Worker's vitest suite (`cloudflare/test`) reads the same files. That is
the whole point: **every implementation of a rule is pinned to one file, so a
rule changed in one of them and not the others fails a test.** Most are
two-sided (Go and TypeScript); `collect-vocabulary.json` is three-sided.

Note that "cross-language" is the common case, not the requirement — two Go
packages that re-implement the same rule have the same drift problem and belong
here too.

## The bundle: versioned, pinned, executable

These files are not just a directory. They are a **bundle with a version**
because they have consumers that pin it by exact value — `cloudflare/` here,
and any repository that vendors this one. Three files make that real:

| file | what it is |
|---|---|
| `bundle.json` | the manifest — `version`, the file list, a sha256 per file, and `version_digests` |
| `CHANGELOG.md` | what each version changed, and the rule that governs bumps |
| `../contracts.pin.json` | the ticks pin: how the two ticks-owned files got here, and which ticks commit they were fetched at |

The version is only worth anything if it always names the same bytes, so both
languages re-hash the fixtures against the manifest on every build:

- **Go** — `internal/contracts` (`Verify`, `VerifyChangelog`, `VerifyPin`),
  run by `go test` and the `contracts` CI job.
- **TypeScript** — `cloudflare/scripts/contracts.mjs` (`verifyBundle`), run
  by `pnpm contracts:check` on every `pnpm test` and `pnpm typecheck`.

And both sides carry **negative controls** — `internal/contracts/bundle_test.go`
and `cloudflare/scripts/contracts.test.mjs` — that break a fixture in a
throwaway copy and assert the check refuses it. That is the point the SPEC
discharges in the only form that means anything: a deliberate fixture break
**is shown** to fail a build, rather than asserted to.

## Who owns what

| file | what it pins | owner |
|---|---|---|
| `job-protocol.json` | the executor/worker job protocol (SPEC §3.3) | ticfac |
| `ticfac-run-state.json` | the `.ticfac/` run-state layout | ticfac |
| `lifecycle-invariants.json` | SPEC Appendix A's lifecycle invariants | ticfac |
| `credential-ownership.json` | the `factory_*` credential vocabulary of `~/.ticfacrc` | ticfac |
| `collect-vocabulary.json` | the collect / intake vocabulary | ticfac |
| `message-context.json` | the message context the operator composes | ticfac |
| `run-event-feed.json` | the `ticfac events` feed's event envelope | ticfac |
| `runners-config-contract.json` | `.tick/runners.toml`: the rules two readers share, the table enumeration, the substrate enum, the `[findings]` table | ticfac |
| `status-model.json` | the `ticfac.status.v1` status model every surface renders | ticfac |
| `worker-boot-contract.json` | the sandbox worker boot handshake | ticfac |
| `sweep-selection-contract.json` | the reconciler's wave selection | ticfac |
| `sandbox-image-cases.json` | `[sandbox].image` behaviour over whole documents | ticfac |
| `signal-source-cases.json` | `[signals.sources.<name>]` cases | ticfac |
| `sweep-policy-cases.json` | `[sweeps.<name>]` cases | ticfac |
| `tk-json-manifest.json` | the published `tk --json` command surface | **ticks**, vendored |
| `tracker-layout.json` | the tracker's on-disk layout (`.tick/issues/<id>.json`) | **ticks**, vendored |

## Adding a contract

1. Put the JSON here, kebab-case, named for the thing it pins. Use
   `*-cases.json` for input → expected tables and `*-contract.json` (or a bare
   noun) for a pinned surface.
2. Add the Go reader in `internal/contracts/parity` (the readers map there is
   checked against the bundle's file list in both directions), and the
   TypeScript reader in `cloudflare/test` — a fixture with only one reader
   detects nothing.
3. Bump `version` in `bundle.json` (a new contract is a MINOR bump), refresh
   `files`/`digests`, add the `CHANGELOG.md` entry, and set `bundleVersion` in
   `cloudflare/contracts.pin.json`. The checks on both sides name whichever of
   these you skipped.

A fixture that leaves this bundle (moves into a package, or is deleted) is a
MAJOR bump, and its readers move or go in the same commit —
`TestEveryBundleFileHasAReader` fails both directions of a list that lies.
