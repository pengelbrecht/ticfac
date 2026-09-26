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
// per-tick gate. It still runs — CI runs `make test` (the full suite),
// and epic close-out runs the full suite — but it no longer stands between a
// tick and its merge.
//
// # The two answers, and which one a test gets
//
// "End-to-end" is not the same claim as "not worth a tick's time", and the
// default must not quietly decide the second by observing the first. A run
// that is kept alive by one loop wants that loop proved on every tick even at
// a few seconds, while the two-hundredth variation on a dispatch is coverage
// CI can hold. So there are two answers and a test gets one of them
// deliberately:
//
//   - EndToEnd — the default. Skipped under -short, run by CI's `make test`
//     and by epic close-out. Reached through the harness constructors, so a
//     test gets it by construction and nobody has to remember.
//
//   - LoadBearing — the exception, taken by name. The test runs in the
//     per-tick gate DESPITE building the harness, because it is the only
//     proof a critical path works and a regression there would be expensive
//     and silent. It costs the gate real seconds, so it is spent against
//     Budget below rather than taken for free.
//
// Both are enforced rather than remembered, by the guard in this package's
// discipline_test.go. A load-bearing test must say so twice — the call AND a
// `gate:` doc-comment line carrying its measured cost — and the declared costs
// must fit in Budget. That is what stops the exception from being the thing
// that walks the gate back to 23 minutes one reasonable-looking test at a
// time: admitting a new one means either measuring it under the remaining
// budget or arguing to raise a number that this comment explains.
package shorttest

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// Budget is the wall-clock allowance for end-to-end tests that run IN the
// per-tick gate — the sum of the `gate:` costs the guard reads out of their
// doc comments.
//
// It is 30s against a Go gate that measures ~32s cold with the cache refused,
// and against tick miu's acceptance bound of 90s. The sum is deliberately
// taken SERIALLY even though these tests run in parallel: an allowance that
// counts on parallelism is an allowance that grows quietly every time the host
// is busy, which is the exact failure this tick exists to end.
//
// Raising this number is allowed. Raising it without saying what was bought is
// not: the guard prints the current spend, so a change here shows up in review
// as a decision rather than as a diff.
const Budget = 30 * time.Second

// loadBearing holds the names of the tests that have claimed a place in the
// per-tick gate. It is a map rather than a flag on the test because the claim
// is made by the TEST and read by the harness CONSTRUCTOR, which has no other
// way to know which test it is building for.
var loadBearing sync.Map

// LoadBearing keeps this test in the per-tick gate even though it builds the
// end-to-end harness.
//
// Call it as the test's first statement, before the constructor: the
// constructor is what would otherwise skip, so a claim made after it is a
// claim made too late. The test must also carry a `gate:` doc-comment line
// with its measured cost, and the costs of all such tests must fit in Budget —
// both enforced by this package's guard.
//
// Reach for it only when the coverage is load-bearing: the test is the only
// proof that a path the run depends on works, and a regression in it would be
// expensive and silent. "This test is important" is not the bar; nearly every
// test is important, which is how a fast gate becomes a slow one.
func LoadBearing(t testing.TB) {
	t.Helper()
	loadBearing.Store(t.Name(), struct{}{})
	t.Cleanup(func() { loadBearing.Delete(t.Name()) })
}

// EndToEnd marks the calling test as one that builds real repositories or
// spawns real processes, and skips it under `-short` unless the test has
// claimed a place in the gate with LoadBearing.
//
// Call it from the harness constructor rather than from the test where you
// can: one call in newFixture covers every test that will ever use the
// fixture, including the ones nobody has written yet.
func EndToEnd(t testing.TB) {
	t.Helper()
	if !testing.Short() {
		return
	}
	if claimed(t.Name()) {
		return
	}
	t.Skip("end-to-end: builds real repositories and spawns real processes, so it runs on CI and at close-out, not in the per-tick gate")
}

// claimed reports whether this test, or a test it is a subtest of, called
// LoadBearing. A subtest is checked against its parents because the claim is
// the TEST's — a test that keeps its place in the gate keeps it for the
// subtests that do its work, and asking each closure to re-claim would be a
// second thing to forget.
func claimed(name string) bool {
	for {
		if _, ok := loadBearing.Load(name); ok {
			return true
		}
		cut := strings.LastIndex(name, "/")
		if cut < 0 {
			return false
		}
		name = name[:cut]
	}
}
