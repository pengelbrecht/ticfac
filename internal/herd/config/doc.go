// Package config is ticfac's reader for the EXECUTION half of
// `.tick/runners.toml` — the per-repo run configuration whose format is
// published by ticks (skills/ticks/references/runners-config.md and its JSON
// Schema) and whose rules are pinned in this repository through the vendored
// contract bundle (contracts/runners-config-contract.json).
//
// # The split this package is
//
// The file is ONE document with TWO domains, and each domain has its own
// reader:
//
//   - the execution tables — `[orchestrator]`, `[orchestration]`, `[roles.*]`
//     with their `[roles.*.tiers.*]` overlays, `[testing]`, `[evidence]`,
//     `[environment]` and `[sandbox]` — are THIS package's, in ticfac, because
//     ticfac is the orchestrator: it routes roles, decides the substrate, runs
//     the gate and reads the run's environment.
//   - the tracker tables — `[signals]` and `[sweeps]` — are ticks': they are
//     acted on by the cloud factory's control plane, and tk is their
//     author-time validator. This reader does not validate them and does not
//     refuse them (see "Foreign tables" below).
//
// That shape is a decision recorded on epic av8 (2026-09-10): of the three
// ways to answer "who parses `.tick/runners.toml`", this is the second — two
// readers over one file, each pinned to its own tables, today's shape made
// honest. The alternatives were weighed and rejected there: a single parser in
// ticks reached through `tk --json` would keep the execution tables' semantics
// in ticks forever and would couple ticfac's config reads to whatever tk
// binary is installed rather than to the pinned bundle; two files would double
// the versioned documents, force a migration on every repository and split one
// human-authored document in two. The direct file read this package performs
// is the same kind of coupling the pinned bundle is: a read of a published,
// fixture-pinned format, not an import of ticks' code.
//
// # One writer, two readers
//
// ticks' migrator (`tk config migrate`) remains the ONLY thing that writes the
// file. It is an author-time tool run by an operator beside the repository; it
// versions the document as one unit, and it is on ticks' side of the split by
// construction, because writing is authoring and the format is ticks' to
// publish. ticfac NEVER writes `.tick/runners.toml` — this package is a
// reader — so there is no "file written by the other side's migrator" to
// reconcile: every file is written by the one migrator, and both readers have
// to load what it writes. That obligation is pinned by a committed fixture
// (testdata/old-migrator.runners.toml, produced by the migrator itself) which
// this package's tests load.
//
// The `version` key stays a property of the ONE document and is enforced by
// BOTH readers' gates before shape (see [Parse] and ticks' own loader): a file
// from a future format version is refused with one upgrade line on either
// side, never with a list of unknown keys. When ticks changes the format —
// a new table on either side, a new key — the contract bundle changes with it,
// and this repository's pin (`contracts check`, on every test run) is what
// makes a one-sided change visible rather than silent.
//
// # Foreign tables
//
// A reader pinned to its own tables must still meet a file carrying the other
// reader's. [Parse] tolerates the tracker tables — `[signals]` and `[sweeps]` —
// as out of its domain: it neither validates nor refuses them, because a
// legal file on the other side of the split is not a typo'd key on this side.
// Everything else it does not understand IS refused (additionalProperties:
// false, as the schema says): a typo'd execution key is an error, never
// silently ignored — the rule this repository's wave-1 work (tick 8ug) already
// enforced for the two hand-rolled readers this package replaces.
//
// The mirror obligation is ticks': its loader must come to tolerate the
// execution tables the same way when ticks retires its execution machinery
// (ticfac tick 4l2). Until then ticks' loader still validates the whole
// document, which is harmless here — both readers agree on the shared half.
//
// # The package answers three questions for an orchestrator
//
//  1. What does this repo's config say? — [Load] / [LoadRepo] parse the file
//     and enforce the format's shape rules in Go.
//  2. Which worker serves this role at this tier? — [Config.Resolve].
//  3. Which substrate orchestrates the run? — [Decide], or [DecideOverride]
//     when whatever booted the run states the substrate explicitly (a cloud
//     sandbox has no herdr server to probe for; see [SubstrateEnvVar]).
//
// It also carries the run's command surface — [Testing], [Evidence]
// (close-out only, with its acceptance authorization table) and
// [Environment] — which it validates but does not execute. The table a command
// sits in is its authorization.
//
// # A failing config is a stop
//
// The format's authoring rule is explicit: a config that fails validation is a
// stop, not a guess — report the error and let the user fix the file rather
// than fall back to defaults silently. [Load] therefore never returns a
// partially-usable [Config]: on any validation failure it returns a
// [ValidationErrors] and a nil config.
//
// A *missing* file is not a failure. [LoadRepo] returns (nil, nil) when
// `.tick/runners.toml` does not exist, and every function here accepts a nil
// [Config] as "no configuration" — substrate `auto`, adapter defaults.
//
// # Where this came from, and what it left behind
//
// The execution half was lifted from ticks' `internal/herd/config` at the
// av8 base (the tracker half of that package stays in ticks, along with the
// migrator, the spawner's kind/compile machinery, and the schema-doc tests
// that need ticks' skill references). Adaptations are deliberate and called
// out in comments: the tracker tables are foreign here (see above), and the
// version gate names ticfac where ticks' named tk. Everything else is
// byte-faithful, so the two readers of the shared half stay comparable line
// by line.
//
// The identifiers ticks' `internal/sandbox` needs from a config package —
// [LoadRepo], [Resolve] and [Worker], [DecideOverride], [Prober], [Tier],
// [SubstrateEnvVar], [FileName] and the rest of its twenty-one — all live in
// this package now, so that when sandbox moves with the rest of the execution
// machinery (it is not part of THIS move) it has a home to import and nothing
// is stranded. Until it moves it keeps importing ticks' copy, which continues
// to exist until ticfac tick 4l2 retires it.
//
// # Readers this package does not absorb
//
// The reconciler's gate reader (internal/reconcile/toml.go) is NOT replaced by
// this package. It parses exactly one table, `[testing.commands]`, and
// refuses everything else ON PURPOSE: the reconciler executes shell, so its
// reader is a permission boundary — a general parser would be a general
// permission, every other table in the file being somebody else's process
// configuration. This package is for the orchestration side; the profiles
// (internal/profile) read roles through it, and the executor ticks of Phase 2
// build on it. It runs nothing.
package config
