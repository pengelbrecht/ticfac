<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-11/3sd`, base `d8cedc95f4b979caa7ee5bc49c68fb3f9580c6fb`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `d8cedc95f4b979caa7ee5bc49c68fb3f9580c6fb` is the head of the work it continued, which was cut from `ee4ad6b817be5b934764ed528dd63686b040ceea`; its work commits are counted from the carried head._

# 3sd — pin the classification profile digest in a short test so `make gate` catches it

## The tick, as the tracker states it

`3sd` (open, backlog, parent epic `ex6`, promoted from finding
`c23a820d…` of the 43y review): the classification decision record is pinned byte for
byte by `TestTheClassificationRecordIsPinnedAsItIsWritten`, and that writer is
EndToEnd, so the per-tick gate skips it. When a profile edit moves `profileSetDigest`,
the pin's `provenance.profile_digest` goes stale and nothing says so until the epic
branch's CI goes red — 7sn's edit to `profiles/closeout-epic.md` did exactly that, and
4w7 regenerated the pin. The tick asks for a SHORT (non-EndToEnd) test comparing
`profileSetDigest` of the embedded profiles against the pinned record's
`profile_digest`, so the gate fails at the tick that edits a profile instead of hours
later on the epic branch.

## What changed

One commit on this branch (`tick 3sd: pin the classification profile digest in a short
test`), touching only `internal/reconcile/classify_pin_test.go`:

- `TestTheClassificationPinsProfileDigestIsTheEmbeddedProfileSet`, added to the pin
  writer's own file so the pin's two halves live in one place. It reads the pin through
  `runstate.Decision` — the same closed record type the store reads it with, so a pin
  that stops being a decision is refused rather than compared on one field — and then
  re-derives the profile set digest and holds it against `provenance.profile_digest`.
  Its doc comment carries the `short:` line the `internal/shorttest` guard requires,
  and it calls `t.Parallel()` for the package's parallel guard.

- The derivation is the one the pin writer's run makes, minus the repository:
  `profile.ResolveAll` over the embedded `profiles/` set, routed through the fixture's
  own `passingGate` (`[roles.implement]` claude/sonnet, which mirrors the shipped
  implement-tick profile and so changes no resolved value), with no tier and no named
  run config, substrate-blind. I derived through the fixture's gate cell rather than
  through the unrouted embedded set on purpose: the pin's digest is a fact about the
  run's *resolved* set, so the guard's failure message is always "regenerate the pin",
  never "edit the guard". The unrouted derivation would have asserted the fixture's
  mirroring of the shipped profile as if it were a property of the pin — a guard whose
  fix is the wrong file. Substrate-blind is safe here and documented in the test: the
  fixture's repository declares no `runners.local.toml`/`runners.cloud.toml`, so no
  substrate the decision procedure can answer changes a routed value (verified: the
  digest is identical for substrate-blind, `harness` and `herdr`); what a real
  repository's resolution adds — an overlay file, a selected config, a tier — stays the
  EndToEnd writer's half, in CI and at close-out.

No production code changed; no fixture changed; the pin itself was not regenerated.

## Test-first, and the reproduction

A drift guard is green at the base by definition, so the test's failure was reproduced
at the exact condition it guards — 7sn's own scenario, twice, then reverted:

    printf '\nscratch\n' >> profiles/closeout-epic.md
    go test -short -count=1 -run 'TestTheClassificationPinsProfileDigestIsTheEmbeddedProfileSet' ./internal/reconcile/

fails with

    --- FAIL: TestTheClassificationPinsProfileDigestIsTheEmbeddedProfileSet (0.00s)
        classify_pin_test.go:173: the pin's profile_digest is "sha256:83d224a1…", but the embedded profile set it was pinned under digests to "sha256:88514b3f…".
        Adopt the change DELIBERATELY: go test ./internal/reconcile -run TestTheClassificationRecordIsPinnedAsItIsWritten -update-pin, …

which is the failure 4w7 only saw on the epic branch's CI, now in the suite `make gate`
pays for. `profiles/closeout-epic.md` was restored byte for byte afterwards (`git
status` clean apart from the test file).

## What I ran

- `gofmt -l .` (clean) and `go vet ./internal/reconcile/`.
- The new test, short and alone: `go test -short -count=1 -run 'TestTheClassificationPinsProfileDigestIsTheEmbeddedProfileSet' ./internal/reconcile/` — PASS (0.00s).
- The reproduction above — FAIL on the profile edit, PASS after the revert.
- The guards that police a new test in this package: `TestEveryTestRunsInParallelOrSaysWhyItCannot` (PASS) and `internal/shorttest`'s `TestEveryEndToEndTestSkipsItselfOrSaysWhyItIsShort` (PASS — the `short:` line is accepted).
- The other two readers of the pin, unchanged and green: `TestTheClassificationRecordIsPinnedAsItIsWritten` without `-short` (PASS), and `internal/runstate`'s `TestThePinnedClassificationRecordDecodesAsThisStoreReadsIt` (PASS).
- The whole-repo gate, per this repo's rules and run low-priority for the shared host: `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — exit 0, gofmt, `go vet ./...` and the short suite across every package green (internal/reconcile 10.7s, internal/cli 41.9s).

I did not run the full `internal/reconcile` suite: it is 20–30 minutes, this repository
says not to run it locally as a matter of course, and this tick changes no production
code the suite exercises. CI's sharded `make test` will still run the EndToEnd pin
writer beside the new short guard.

## What the next tick has to know

- The pin has three readers now: the EndToEnd writer (`internal/reconcile`), the
  digest guard (new, short, `internal/reconcile`), and the record-shape reader
  (`internal/runstate`). Any edit to `profiles/*` or to `passingGate`'s role cell moves
  `profile_digest`: regenerate with `-update-pin`, read the one-line diff, and both
  guards go green again. The digest guard makes that a gate failure at the editing tick
  rather than an epic-branch CI failure.
- `make gate` now fails on a profile edit even before the pin is regenerated — that is
  the point of this tick, not a regression: `-update-pin` is the deliberate adoption,
  and the guard's failure message names it.
- A new top-level test in `internal/reconcile` needs `t.Parallel()` (or a `serial:`
  line) and, when it does not build the fixture, a `short:` doc-comment line; both
  guards caught nothing because both were satisfied.

## Findings

Nothing outside this tick: the pin is the only byte-for-byte record in the repository
whose only short-reachable moving part is a digest (checked: the other
`cloudflare/test/fixtures` entry is a recorded omp wire exchange with no provenance),
and the boundary the new guard leaves to CI — a resolution that starts reading a config
the fixture does not have — is stated in the test rather than left to be discovered.

```findings v2
[]
```

STATUS: DONE
