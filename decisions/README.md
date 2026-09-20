# decisions

The reconciler's decision record. `internal/reconcile` (Go, the local host)
and `cloudflare/src/epic-reconciler.ts` (TypeScript, the Workflow host) are
two implementations of one thing, and this directory is the written statement
of where they are allowed to differ: every intentional semantic difference
between them is a numbered decision in `reconciler-parity.json`, with the
code region that implements it pinned on each side.

The numbers continue the cloud-factory series, whose D1–D25 live in the ticks
repository's `docs/design/cloud-factory.md` (the same place the code's
existing `D16`/`D17`/`D22` citations resolve). This record starts at D26
because that is the next number that repository had not spent. A decision
cited anywhere in this repository resolves in one of the two places: here,
or there.

## Why a record and not a comment

The Workflow port changed reconciler semantics in places without recording
those changes anywhere, so the two implementations could diverge with nothing
stating which is intended (tick `sz0`, from the `pxc` review's finding
f8ef8154). This is the same drift the vendored contract bundle
(`contracts/`, verified by `internal/contracts`) and the vendored image
context (`image/`, verified by `internal/sandboxpin`) are already answered
with: **a decision recorded in prose with no check is how the copies got
here**. So each decision here is pinned, not just written.

## The pin, and the check that holds it

Each decision anchors the code regions that implement its behaviour, on the
Go side (`internal/reconcile`, `internal/runstate`), the Workflow side
(`cloudflare/src`), or both, with marker comments bracketing the region:

```
reconciler-decision:<id>:begin:<region>
... the recorded behaviour ...
reconciler-decision:<id>:end:<region>
```

`internal/reconcile/parity_decision_test.go` (in the per-tick gate's short
suite) verifies every anchor: the region must exist exactly once, and its
content must digest to the `sha256` the record holds. The check fails when
either side changes a recorded behaviour without a matching decision — a
changed line, a removed marker, a marker whose decision was deleted from the
record, or a decision with nothing pinned — and its failure message names the
decision, the side and the region.

## Changing a recorded behaviour

If you changed the behaviour a decision records, you are changing the
decision, and the record must move in the same change:

1. Update the decision's `go`/`workflow`/`why` prose to state the behaviour
   as it now is — a digest update with stale prose is a record that lies.
2. Move the markers with the code if the region moved.
3. Re-derive the digests:

   ```
   RECONCILE_PARITY_UPDATE=1 go test ./internal/reconcile -run TestReconcilerParityDecisionsMatchTheImplementions -count=1
   ```

   The update writes digests only; it never writes prose. That is deliberate:
   the prose is the decision, and the command must not be able to make one.

If the change is a NEW divergence between the two reconcilers, add a decision
(the next number) with anchors on each side where code exists, and say why it
is intended. A divergence with no decision is what this record exists to
prevent; the check holds what is recorded, and the review holds what is not.
