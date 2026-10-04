<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-1-base-fold-1-47160c2a/hn6`, base `eb9ec1f509fc4cf7cd3c037a2d8ee6eef79c9b06`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-1-base-fold-1-966a7332/hn6`, base `a7c5bf39fe5fb6155531c0bb721b0295910ea4dc`, harness `pi` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# resolve-conflict: the fold of main into epic/hn6

The branch is `tick/hn6/attempt-1-base-fold-1-966a7332/hn6`: epic/hn6 at
`220aea004231` with main at `3e88ca73ab5c` merged in and left unresolved. Two
files carried conflict markers:

- `contracts/CHANGELOG.md` — one `## 1.1.0` entry from each side
- `contracts/bundle.json` — two different `version_digests["1.1.0"]` bindings

## What the conflict was

Not two ticks: a base fold, and the collision was over the bundle's VERSION
IDENTITY. Both sides had independently bumped the contract bundle from 1.0.1
to 1.1.0 with different bytes:

- **main's** cut (commit 6066a47e, #158, hn6 run_3f034e68): `worker-boot-contract.json`
  gains `env.work_base` / `TICKS_WORK_BASE_SHA` — for a carried attempt, the
  base the carried work was cut from, so a worker that adds nothing to
  complete carried work settles succeeded instead of no-work (10).
- **The epic's** cut (hn6 wave 1, tick r5i): `status-model.json` grows the
  dashboard vocabulary — per-tick `pipeline`, `parent_tick_id`,
  `duration_seconds`, `findings`, `report`, per-try `tier`/`reason`/`next_step`,
  per-worker `handle`/`activity`, `health.verdict`, per-source `cost.lines`,
  top-level `recent` and `epic_title`, plus the `dashboard` golden and three
  new negatives.

The two fixture edits are in different files, so the FIXTURES merged cleanly
and the merged tree already holds the full union: `status-model.json` carries
the dashboard vocabulary, `worker-boot-contract.json` carries `work_base`, and
the bundle's `digests` map came through the merge as the union of the two
sides' cuts (verified against the files on disk — all 16 digests match). Only
the version identity failed to merge, because both sides cut the SAME version.

## The resolution

A version string must never name two different sets of bytes, and the union
matches neither side's recorded 1.1.0 — the ledger binds 1.1.0 once per side,
and `contracts.Verify` (Go) and `verifyBundle` (TypeScript) both refuse a
re-cut that contradicts a recorded binding. The fold therefore re-cuts the
union as **1.2.0** (MINOR: both halves additive and unrelated; an unchanged
consumer is still correct but no longer complete, twice over):

- `contracts/bundle.json`: `version` → 1.2.0; `files`/`digests` unchanged
  (already the union, matching disk); `version_digests["1.2.0"]` = `dbe3ab82…`,
  computed over the canonical `version + digests` form both verifiers share.
- `contracts/CHANGELOG.md`: a new `## 1.2.0` entry tells the fold story; the
  `## 1.1.0` section keeps BOTH texts verbatim, labeled by which line cut
  them (both cuts really happened, and both are the "who has to follow"
  documentation for a consumer pinned to either 1.1.0).
- `cloudflare/contracts.pin.json`: `bundleVersion` → 1.2.0, per the bundle's
  own rule ("How to cut a bump", step 5) — without this move the TypeScript
  verifier's exact-value pin refuses the re-cut.

## The one genuinely irreconcilable point — chosen, and why

`version_digests` is a JSON map: ONE binding per version, write-once. It
cannot hold both sides' 1.1.0 cuts, so one had to be chosen and the other
recorded elsewhere:

- The ledger keeps **the epic's** binding (`db0bbc96…`) — the entry the fold's
  surviving line (HEAD, first parent) had already written; a ledger entry is
  never rewritten, and the fold lands on this line.
- **main's** cut of the same version (`b3c50e1c…`) is recorded in the 1.2.0
  changelog entry, so the binding survives in prose rather than being lost.

Everything else is a pure union: both fixture changes, both changelog texts,
both implementations (main's `worker-boot.ts`/`image/worker.sh` half merged
cleanly alongside its contract).

## Finding: the collision is structural, not a text defect

Both sides behaved correctly in isolation — each bumped the bundle under the
rule "every change to contracts/ bumps the version in the same commit". The
defect is the timing of the base fold: whenever main bumps the bundle between
an epic's fork and the fold, and the epic's own ticks bump it too, the two
lines cut the same version with different bytes and the fold cannot keep both
ledger bindings. The bundle's rule (one meaning per version string) catches it
loudly, but the resolution is manual and lossy-in-form — one binding survives
in the ledger, the other only in changelog prose. Worth knowing for future
folds; no change to either side's text was needed to reconcile INTENT, which
was never in conflict — only the version numbering was.

## Verification

All run on this worktree, all foreground, all green:

- `go run ./cmd/contracts check` — "ticfac's bundle verifies, and the
  ticks-owned files match contracts.pin.json (offline check)"
- `node cloudflare/scripts/contracts.mjs check` — "contracts: ok — 16
  contract(s) at bundle 1.2.0 read in place from /work/repo/contracts (mode:
  workspace)"
- `go test -short -timeout 20m -parallel 4 ./internal/contracts/ ./internal/statusmodel/` — ok
- `make gate` (gofmt, `go vet ./...`, short suite across the repository) — exit 0
- No conflict markers remain: `git grep '^<<<<<<<\|^=======\|^>>>>>>>' -- ':!.ticfac'`
  finds nothing in tracked source.

## Commits

- `384779c` — resolve the fold of main into epic/hn6: re-cut the contract
  bundle at 1.2.0 (`contracts/bundle.json`, `contracts/CHANGELOG.md`,
  `cloudflare/contracts.pin.json` — source only; no build output, no runs/
  artifacts)

STATUS: DONE
