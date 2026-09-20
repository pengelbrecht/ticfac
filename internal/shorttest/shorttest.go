// Package shorttest is the one place that says what `-short` means in this
// repository.
//
// The gate runs `go test -short`, and for most of this repository's life that
// flag did nothing: 6 of 215 test files consulted testing.Short(), so every
// per-tick gate ran the entire end-to-end suite. That suite is not a unit
// suite — internal/reconcile, internal/exec/herdr and internal/runstate build
// real git repositories and spawn real worker processes per test — and it cost
// 20+ minutes a tick, competing with the four concurrent gates that are the
// reason the host is busy in the first place. Two of those runs failed on
// tests that pass on a quiet host (ticks e4u and 7c0) and stopped a phase for
// nothing.
//
// So `-short` now means what it says: an end-to-end test does not run in the
// per-tick gate. It still runs — CI runs `make test-short` AND `make test`,
// and epic close-out runs the full suite — but it no longer stands between a
// tick and its merge.
//
// The discipline is enforced, not remembered. See TestEveryEndToEndTestIsShortSkipped
// in this package: every top-level test in a declared end-to-end package must
// either go through that package's end-to-end harness (which calls EndToEnd
// for it), call EndToEnd itself, or carry a `short:` doc-comment line saying
// why it is cheap enough to pay for on every tick. A test added next month
// without one of the three fails that guard, which is the difference between a
// fix and a fashion.
package shorttest

import "testing"

// EndToEnd marks the calling test as one that builds real repositories or
// spawns real processes, and skips it under `-short`.
//
// Call it from the harness constructor rather than from the test where you
// can: one call in newFixture covers every test that will ever use the
// fixture, including the ones nobody has written yet.
func EndToEnd(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end: builds real repositories and spawns real processes, so it runs on CI and at close-out, not in the per-tick gate")
	}
}
