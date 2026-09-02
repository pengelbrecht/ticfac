# How ticfac gets `contracts/`

ticfac depends on ticks through exactly two things (SPEC §3.1–3.2): `tk --json`,
and the **pinned contract bundle**. This file is about the second one — how the
bundle gets here, what verifies it, and what has to fail when it drifts.

The design is not new. `cloud/factory/CONTRACTS.md` in ticks worked it out for
the factory, which faced the same problem first: a consumer leaving the
repository that authors the fixtures. ticfac is the second consumer, and this
is that design applied, not a second mechanism.

Implementation: `contracts.pin.json`, `internal/contracts`, `cmd/contracts`.

## The rule

**A Go/TypeScript/ticfac divergence has to fail a build.** A copy of the
fixtures existing is not enough; the copy has to be pinned to a known ticks
version and verified, and every way that verification can fail has to be loud.

Three commands, and the split between them is the whole safety argument:

|                    | `contracts check`   | `contracts verify-upstream` | `contracts sync`        |
| ------------------ | ------------------- | --------------------------- | ----------------------- |
| when               | every `go test`, CI | the CI `contracts` job      | a person bumping the pin |
| network            | **never**           | required                    | required                |
| on failure         | exit 1              | exit 1                      | exit 1, writes nothing  |

`check` is the gate and it makes no network call, so **no network failure can
turn a test run green by skipping it.** `sync` is the only thing that writes,
and it is not on the test path, so a GitHub outage can only make a deliberate
pin bump fail — never make a test run lie.

## What is pinned, and by what

| field in `contracts.pin.json` | claim |
| --- | --- |
| `bundleVersion` | the contract version **this repository's code is written against**. SPEC §3.2 requires it by exact value. |
| `ref` | the immutable ticks **commit** the vendored bytes were fetched at. A mechanical fact about a download. |

They are different claims and move independently: ticks can publish twenty
commits that do not touch a contract, and `bundleVersion` stays where it is.
Both move together only when a person adopts a new bundle.

`ref` must be a full 40-character sha. A branch name is refused — a pin to
something that moves pins nothing.

## Why GitHub at a commit, and not the module proxy

ticks' factory syncs through the public Go module proxy, because it needs a
*resolved module version* anyway for the sandbox image. ticfac does not: it
needs bytes at a commit, and the module proxy would add a resolution step that
can only introduce ambiguity. `codeload.github.com/<repo>/tar.gz/<sha>` is
exact by construction and needs no toolchain.

## What `check` asserts

1. `contracts.pin.json` parses, and names mode `pinned` — the only mode this
   repository has, because there is no ticks checkout here for a workspace mode
   to point at.
2. The vendored bundle verifies against its own manifest: every listed file
   present, parsing, hashing to its recorded digest, no unlisted `*.json`
   alongside them, and `CHANGELOG.md` carrying an entry for the version.
3. The manifest's `version_digests` ledger still agrees with the version on
   disk — so a **re-cut at an unchanged version** is refused. This is the one
   drift a consumer pinned by exact value cannot otherwise see, and it is the
   only reason the version exists.
4. The bundle's version equals `bundleVersion`.
5. The pinned file set equals the bundle's file set, in **both** directions.
6. Every vendored file — the fourteen fixtures plus `bundle.json`,
   `CHANGELOG.md` and `README.md` — hashes to the digest recorded in the pin,
   and no unpinned file is sitting in `contracts/`.

## What CI adds

- **`verify-upstream`** fetches ticks at `ref` and requires the vendored
  `contracts/` to be byte-for-byte what ticks published there. That is what
  makes "vendored" mean "pinned" rather than "copied".
- **Every reader runs**, as its own named step, so a contract problem is
  legible in the log instead of surfacing as one failure among many.
- **A deliberately broken fixture fails.** CI edits a vendored fixture on
  purpose, requires `check` to refuse it, restores it, and requires `check` to
  pass again. `internal/contracts/bundle_test.go` does the same fifteen ways in
  a throwaway copy, on every `go test`. A check nothing has ever seen fail is
  not known to be a check.

## Adopting a new bundle version

A deliberate, online act by a person — never automatic, because an automatic
bump silently adopts a contract change, and the whole point is that adopting
one is visible and its readers then run.

1. Set `ref` to the new ticks commit and `bundleVersion` to the version
   `contracts/bundle.json` carries there.
2. `go run ./cmd/contracts sync` — it writes `contracts/` and refreshes the
   pin's digests, and writes nothing at all if any file fails to resolve.
3. `go test ./...` — the readers now run against the new fixtures, and
   whatever ticks changed that ticfac has not followed goes red.
4. Commit `contracts/` **and** `contracts.pin.json` together. The digests are
   meaningless apart from the files they describe.

Never edit a vendored fixture here. Change it in ticks, cut a bundle version
there, and adopt it in one commit — the checks on both sides name whichever of
these you skipped.

## The readers

`internal/contracts/parity` holds one reader per bundle file, and
`TestEveryBundleFileHasAReader` keeps that map honest in both directions. Each
reader says which kind it is:

- **executable** — the fixture's cases are run against ticfac code;
- **structural** — the fixture pins a rule over a format ticfac does not parse
  yet, so the shape, the vocabularies and the cross-file claims are asserted
  and the cases wait for the work the reader names.

A structural reader is deliberately not called a parity reader. Calling it one
would be the failure `contracts/README.md` warns about: a check that reads as
if it asserted something while asserting nothing.
