# decisions

The reconciler's decision record. `internal/reconcile` (Go) is the ONE
implementation of the reconciler this repository ships: the local host runs
it directly, and a cloud epic runs it inside the orchestrator container
through ticfac. The record was born (tick `sz0`) as the parity record between
TWO implementations — the Go one and the Workflow-hosted TypeScript one
(`cloudflare/src/epic-reconciler.ts`) — and every entry was a numbered
decision pinning a region on each side that implemented it.

Tick `mn7` deleted the Workflow implementation: two implementations of one
mechanism is what put the factory through `99f` (two contents-API writers,
one of which never sent the branch), and once a cloud epic ran through ticfac
in the container, the isolate's reimplementation had no callers. The record
is REWRITTEN rather than deleted, because it still does the two things it
was built for:

1. **It pins the surviving implementation's recorded behaviours.** Every
   surviving entry carries a Go anchor, and `internal/reconcile/
   parity_decision_test.go` (in the per-tick gate's short suite) verifies
   each anchor's region still exists exactly once and still digests to the
   `sha256` the record holds. The failure message names the decision, the
   side and the region.
2. **It is the history of WHY the surviving code is shaped as it is.**
   Several decisions record behaviour the deleted side's departure
   motivated that still stands: D29's per-repository publish slot and D30's
   lapsed-lease re-acquire describe rooms this repository still ships
   (`repo-room.ts`), and D31 names the integration boundary the container's
   reconciler now crosses where the Workflow's used to stop.

The `workflow` prose of each entry is retained, prefixed **Historical**: it
describes the deleted side, which was half of what the decision recorded.
Entries whose only anchored side was the deleted implementation's (D28,
D34) were deleted with it — a decision with nothing left to pin cannot catch
drift, and the check refuses exactly that shape.

The numbers continue the cloud-factory series, whose D1–D25 live in the ticks
repository's `docs/design/cloud-factory.md`. A decision cited anywhere in
this repository resolves in one of the two places: here, or there.

## Why a record and not a comment

The Workflow port changed reconciler semantics in places without recording
those changes anywhere, so the two implementations could diverge with nothing
stating which is intended (tick `sz0`, from the `pxc` review's finding
f8ef8154). This is the same drift the vendored contract bundle (`contracts/`,
verified by `internal/contracts`) and the vendored image context (`image/`,
verified by `internal/sandboxpin`) are already answered with: **a decision
recorded in prose with no check is how the copies got here**. So each
decision here is pinned, not just written.

## The pin, and the check that holds it

Each decision anchors the code regions that implement its behaviour on the
surviving side (`internal/reconcile`, `internal/runstate`), with marker
comments bracketing the region:

```
reconciler-decision:<id>:begin:<region>
... the recorded behaviour ...
reconciler-decision:<id>:end:<region>
```

`internal/reconcile/parity_decision_test.go` (in the per-tick gate's short
suite) verifies every anchor: the region must exist exactly once, and its
content must digest to the `sha256` the record holds. The check fails when
the recorded behaviour changes without a matching decision — a changed line,
a removed marker, a marker whose decision was deleted from the record, or a
decision with nothing pinned — and its failure message names the decision,
the side and the region.

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

A NEW divergence between two reconcilers, if one ever returns, is a new
decision (the next number) with anchors on each side where code exists, and
a note saying why it is intended. A divergence with no decision is what this
record exists to prevent; the check holds what is recorded, and the review
holds what is not.
