# How the factory gets `contracts/`

The factory's vitest suite asserts against the cross-language contracts in the
repository root `contracts/` directory — the same files the Go parity tests
read. `contracts/README.md` says what they are and why a one-sided edit is the
thing they exist to catch. This file says how the **TypeScript** side gets hold
of them.

Implementation: `contracts.pin.json` and `scripts/contracts.mjs`.

## Who owns the bundle

Since tick 4i8 **ticfac authors the bundle**, and the factory is inside ticfac:
`contracts/bundle.json` carries ticfac's version, `contracts/CHANGELOG.md` is
ticfac's, and the suite reads them **in place** — `mode: "workspace"`. The
original problem this file documented (the factory leaving the ticks
repository, needing a vendored, digest-verified copy of a ticks-published
module) was solved by that mechanism and the mechanism stays: the day the
factory is extracted into its own repository, the pin flips to `pinned` and the
same script vendors the bundle from a resolved ticfac module version — nothing
else changes.

Two files inside the bundle are not ticfac's to edit: `tk-json-manifest.json`
and `tracker-layout.json` describe **ticks'** own formats and are vendored from
a pinned ticks release by the Go repository's root `contracts.pin.json` (see
CONTRACTS.md there). The TypeScript side just reads them; it never vendors them
independently.

## What was chosen

**Read in place, verified on every test run by an offline digest check.**

Two commands, and the split between them is the entire safety argument:

| | `pnpm contracts:check` | `pnpm contracts:sync` |
|---|---|---|
| when | every `pnpm test`, every `pnpm typecheck`, its own CI step | only when a human bumps the pin |
| network | **never** | required |
| on failure | exit 1 | exit 1, writes nothing |

`check` is the gate and it makes no network call, so **no network failure can
turn a test run green by skipping.** `sync` is the only thing that needs the
proxy, and it is not on the test path, so a proxy outage can only make a
deliberate pin bump fail — never make a test run lie.

### Why the copy lands at the repository root

When the extraction comes, the vendored copy goes to the **consuming
repository's root** `contracts/`, which is exactly where the authored copy sits
relative to this package (`ticfac/cloudflare` → two levels up). That is
deliberate: the imports in `test/` resolve to the right file in both worlds, so
the extraction does not touch a single test file.

### The bundle version, and what it is pinned by

`contracts/` is a **versioned bundle**, not a directory of files. Its manifest
`contracts/bundle.json` carries a `version`, the file list, a sha256 per file,
and `version_digests` — the append-only ledger recording the digest each
version was first cut with, which is what makes "do not re-cut without bumping"
a check rather than a habit. `contracts/CHANGELOG.md` says what each version
changed and who has to follow. This package pins that version **by exact
value** in `contracts.pin.json`:

```json
"bundleVersion": "1.0.0"
```

`ref` would say which *ticfac module version* a vendored copy came from — a
mechanical fact about a download. `bundleVersion` says which *contract version
this package's code was written against* — a claim about behaviour, and the one
the ticfac SPEC (§3.2) requires.

### What `check` actually asserts

1. `contracts.pin.json` exists, parses, and names a known mode.
2. The set of contracts pinned is **exactly** the set the test suite imports.
   Both directions are fatal: a contract imported but not pinned is one
   nothing vendors or verifies; a contract pinned but not imported is a fixture
   with a single reader, which `contracts/README.md` is explicit detects
   nothing.
3. Every pinned file is present in `contracts/` and parses as JSON.
4. `contracts/bundle.json` exists, its `version` equals `bundleVersion`, its
   file list equals the pinned list, every listed file hashes to the digest it
   records, no unlisted `*.json` is sitting in `contracts/`, and
   `contracts/CHANGELOG.md` has an entry for the version.
5. The re-cut ledger: `version_digests` has an entry for the version on disk,
   and the digest of this cut matches it.

### Proving the check can fail

`scripts/contracts.test.mjs` (run by `pnpm contracts:test`, chained into
`pnpm test`, and its own CI step) breaks the bundle in a throwaway copy and
asserts `verifyBundle` refuses each break — an edited fixture, an unlisted
fixture, a missing fixture, a stale version pin, a version with no changelog
entry, a disagreeing file list, an absent `bundleVersion`, a manifest re-cut at
an unchanged version, and a version whose `version_digests` entry has been
deleted. `internal/contracts/bundle_test.go` does the same on the Go side, and
also asserts that Go's canonical digest form agrees with the JavaScript
generator's — two independent conventions that happen to agree today would not
be a cross-language check.

They run under plain `node --test` rather than vitest because the factory
suite executes inside workerd, which has no filesystem to build a broken bundle
in.

## Failure behaviour, stated plainly

This is the part that matters, so it is enumerated rather than implied. **No
path through `scripts/contracts.mjs` warns and continues.**

| situation | what happens |
|---|---|
| offline, `pnpm test` | **passes**, and correctly so — `check` never touches the network, and the files are read in place |
| offline, `pnpm contracts:sync` (when pinned) | exit 1, names the proxy, writes nothing |
| a contract changed without the bundle being re-cut | exit 1 on digest mismatch, on the next `pnpm test` |
| `contracts/` absent or empty | exit 1 |
| `contracts.pin.json` deleted | exit 1 |
| a new contract import added without pinning it | exit 1 |
| a pinned contract no longer imported | exit 1 |
| `contracts/bundle.json` absent or unparseable | exit 1 |
| the bundle version is not `bundleVersion` | exit 1, naming both |
| a fixture on disk that the bundle does not list | exit 1 |
| a bundle version with no `contracts/CHANGELOG.md` entry | exit 1 |
| the manifest re-cut at a version `version_digests` already records | exit 1, naming both digests |
| `version_digests` has no entry for the version on disk | exit 1 |
| `bundleVersion` absent from the pin | exit 1 |

## Adding a contract to the factory

1. Land the JSON in `contracts/` with its Go reader (see `contracts/README.md`
   and the readers map in `internal/contracts/parity`).
2. Add the TypeScript reader importing `../../../contracts/<name>.json`.
3. Add the file name to `files` in `contracts.pin.json`. `pnpm contracts:check`
   fails until you do, and names the file.
4. Bump the bundle version, add the changelog entry, and move `bundleVersion`
   in `cloudflare/contracts.pin.json` in the same commit.
