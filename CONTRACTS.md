# How ticfac gets `contracts/`

Since tick 4i8, **ticfac authors the contract bundle**. Every contract
describing a ticfac format — the executor protocol, the `.ticfac/` layout, the
status model, the run event feed, `.tick/runners.toml`'s rules, the sandbox
boot handshake — is edited here, re-cut here, and versioned here in
`contracts/bundle.json` with its own changelog. The design ticfac grew up on
(the consumer side of ticks' `cloud/factory/CONTRACTS.md`) stays in one place:
the **two contracts that describe ticks' own formats** — `tk-json-manifest.json`
(the published `tk --json` surface) and `tracker-layout.json` (the tracker's
on-disk layout) — are still vendored from a pinned ticks release, because
ticks is where their other readers live. ticks' bundle 7.0.0 carries exactly
those two files, and this repository pins it.

Implementation: `contracts.pin.json`, `internal/contracts`, `cmd/contracts`.

## The rule

**A Go/TypeScript/ticfac divergence has to fail a build.** A copy of the
fixtures existing is not enough; the copy has to be pinned to a known version
and verified, and every way that verification can fail has to be loud.

Three commands, and the split between them is the whole safety argument:

|                    | `contracts check`   | `contracts verify-upstream` | `contracts sync`        |
| ------------------ | ------------------- | --------------------------- | ----------------------- |
| when               | every `go test`, CI | the CI `contracts` job       | a person bumping the pin |
| network            | **never**           | required                     | required                |
| on failure         | exit 1              | exit 1                       | exit 1, writes nothing  |

`check` is the gate and it makes no network call, so **no network failure can
turn a test run green by skipping it.** `sync` is the only thing that writes,
and it is not on the test path, so a GitHub outage can only make a deliberate
pin bump fail — never make a test run lie.

## What is pinned, and by what

| field in `contracts.pin.json` | claim |
| --- | --- |
| `bundleVersion` | the **ticks** bundle version the vendored bytes were published at. Offline it is a recorded claim; `verify-upstream` binds it to bytes online by requiring the pin's digests to be exactly what ticks' own manifest publishes for that version. |
| `ref` | the immutable ticks **commit** the vendored bytes were fetched at. A mechanical fact about a download. |

They are different claims and move together only when a person adopts a new
ticks bundle. `ref` must be a full 40-character sha. A branch name is refused —
a pin to something that moves pins nothing.

`files` in the pin is the ticks-owned half of `contracts/` — today, the two
files of bundle 7.0.0. Every one of them also rides **inside ticfac's own
bundle**, so the offline check and the readers' map cover them here too. The
ticfac-owned contracts need no pin: they are verified by ticfac's own manifest
in the same directory.

## Why GitHub at a commit, and not the module proxy

ticks' factory syncs through the public Go module proxy, because it needs a
*resolved module version* anyway for the sandbox image. ticfac does not: it
needs bytes at a commit, and the module proxy would add a resolution step that
can only introduce ambiguity. `codeload.github.com/<repo>/tar.gz/<sha>` is
exact by construction and needs no toolchain.

## What `check` asserts

1. `contracts.pin.json` parses, names mode `pinned`, and every pinned file has
   a recorded digest.
2. ticfac's own bundle verifies against its manifest: every listed file
   present, parsing, hashing to its recorded digest, no unlisted `*.json`
   alongside them — and the changelog has an entry for ticfac's version.
3. The manifest's `version_digests` ledger still agrees with the version on
   disk — so a **re-cut at an unchanged version** is refused, whoever owns the
   file. This is the one drift a consumer pinned by exact value cannot
   otherwise see, and it is the only reason the version exists.
4. Every pinned ticks file is also listed by ticfac's bundle, and every file
   on disk is either bundle-listed or is the manifest, the changelog or the
   readme — no unverified file in a verified directory.
5. Every pinned file hashes to the digest the pin records — ticks' published
   digest — so a local edit of a ticks-owned contract is caught by **two**
   independent records at once.

## What `verify-upstream` adds (CI, online)

- fetches ticks at `ref`, requires ticks' own `bundle.json` there to carry
  `bundleVersion`, the same file list as the pin, and the same digests — the
  binding between "7.0.0" and bytes that the offline gate cannot make;
- requires the two vendored files to be byte-for-byte what ticks published.

## What CI adds

- **Every reader runs**, as its own named step, so a contract problem is
  legible in the log instead of surfacing as one failure among many.
- **A deliberately broken fixture fails.** CI edits a fixture on purpose,
  requires `check` to refuse it, restores it, and requires `check` to pass
  again. `internal/contracts/bundle_test.go` does the same many ways in a
  throwaway copy, on every `go test`. A check nothing has ever seen fail is
  not known to be a check.

## Cutting a bundle version (a ticfac-owned contract)

1. Edit the fixture, and every implementation of the rule, in one commit
   (`contracts/README.md` — a one-sided edit is what these files exist to
   catch).
2. Bump `version` in `contracts/bundle.json`, refresh `files`/`digests`, add
   the `version_digests` entry, and write the `CHANGELOG.md` entry.
3. `go test ./...` — the readers now run against the new bytes.
4. Move `bundleVersion` in `cloudflare/contracts.pin.json` in the same commit:
   the factory Worker's suite is a pinned consumer of this bundle too.

## Adopting a new ticks bundle (the two ticks-owned files)

A deliberate, online act by a person — never automatic, because an automatic
bump silently adopts a contract change.

1. Set `ref` to the new ticks commit and `bundleVersion` to the version
   `contracts/bundle.json` carries there.
2. `go run ./cmd/contracts sync` — it writes the pinned files and refreshes
   the pin's digests, touches nothing ticfac-authored, and then runs the
   offline gate. The gate **fails until ticfac's own bundle is re-cut** in the
   same commit — the vendored bytes are part of ticfac's bundle too, so
   adopting them is a bundle bump like any other.
3. Commit `contracts/` **and** `contracts.pin.json` together. The digests are
   meaningless apart from the files they describe.

Never edit a ticks-owned fixture here. Change it in ticks, cut a bundle
version there, and adopt it in one commit — the checks on both sides name
whichever of these you skipped.

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
