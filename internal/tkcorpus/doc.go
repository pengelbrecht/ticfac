// Package tkcorpus holds one thing: the pinned corpus that certifies the
// TypeScript tracker client's WRITES against a real tk binary.
//
// cloudflare/src/tracker-client.ts is a second implementation of the tick
// record format, written for the one host that cannot run tk (SPEC §3.1).
// A second implementation of a shared format drifts silently unless something
// compares the two, and tick b9w found what happens without that: the two
// halves were wrong about each other for two major contract versions. The
// review that closed Phase 4 (pxc, finding 7707a087, tick v3q) found the same
// shape again in the writes: the client emitted bytes tk would not —
// encoding/json escapes <, > and & as \u003c, \u003e and \u0026 and JSON.stringify
// does not — and the test that should have caught it compared against
// fixtures written from the same misunderstanding as the code.
//
// This package is the generated half of the repair. It drives a real `tk`
// binary command by command through a corpus of writes in a throwaway
// repository, captures the exact record bytes tk commits, canonicalizes the
// two things that cannot be byte-stable across runs (the wall clock and the
// randomly minted tick ids), and pins the result at
// cloudflare/test/fixtures/tk-write-corpus.json.
//
// Two tests read the pin, each refusing one direction of drift:
//
//   - TestTkWriteCorpusMatchesCurrentTk regenerates the corpus with the tk on
//     PATH and byte-compares against the pin. When tk changes what it writes,
//     this fails on every host that has tk — the per-tick gate's host always
//     does — and the pin is regenerated deliberately with
//     `go test ./internal/tkcorpus -update`, never hand-edited.
//   - cloudflare/test/tk-write-corpus.test.ts replays every corpus case
//     through the real TypeScript client and byte-compares its committed
//     records against the pin. When the client drifts, CI's vitest job fails.
//
// The canonicalization is the contract's own clock and identity hygiene, not
// a place to hide drift: every rewrite asserts the value it replaces had the
// shape tk writes (RFC3339 clock fields, `YYYY-MM-DD HH:MM - ` note lines), so
// a tk that changes the SHAPE still fails the regeneration, and the pinned
// bytes remain tk's own serialization — field order, omitempty, indent,
// escaping — with only the substituted values differing.
package tkcorpus
