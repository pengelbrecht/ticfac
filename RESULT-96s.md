<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-14/96s`, base `304cca6250414b27584d43678a2b38a4f914e7c2`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# RESULT — tick 96s (run attempt 14, the tick's first attempt) — runners.local.toml header note: review/closeout deliberately not overlaid

## Summary

STATUS: BLOCKED. The tick's whole deliverable is a one-paragraph comment note
in `.tick/runners.local.toml`'s header explaining why that file declares no
`[roles.review]` / `[roles.closeout]` overlay. `.tick/runners.local.toml` is a
`.tick/` path, and I obey the refusals that govern it: the container's
pre-commit hook (installed from image/worker.sh's `install_boundary_guard`,
tick dxk) refuses any commit that stages a `.tick/` path, and the cloud
collect (`cloudflare/src/worker-collect.ts:313`, `:530`) reads any `.tick/`
path in a branch diff as a boundary violation and refuses the branch. The
tick's own description records that a worker was already rejected for exactly
this edit once (attempt 32 of run-epic-43y, tick j6o): "runners.local.toml is
outside the files a worker may write." I staged nothing, committed nothing,
and recorded no boundary attempt — the guard's ledger is empty.

## What I did instead

1. Verified the tick's premise — that the behaviour is already enforced and
   the note is documentation only. Both guard tests pass on this tree:
   - `TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells`
     (`internal/reconcile/routing_check_test.go:350`)
   - `TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness`
     (`internal/reconcile/routing_check_test.go:288`)
   `.tick/runners.toml`'s header comment (lines 52–64) states the same thing
   in prose; this tick asks for the matching prose in the local overlay's own
   header.

2. Composed the note. **The exact change the tick needs** — insert after the
   last paragraph of the header comment in `.tick/runners.local.toml`
   (currently its line 9, the "attempt 1 -> ... capped there." sentence),
   before the line `version = 2`:

   ```
   # Deliberately no [roles.review] or [roles.closeout] cells here (tick 96s):
   # the blessed local claude exception lives in .tick/runners.toml — its
   # [roles.review], [roles.review.tiers.frontier] and [roles.closeout] cells
   # name the claude CLI on opus for local final reviews and close-outs, and
   # an overlay cell in this file would move those jobs off the blessed
   # decision. So this file overlays only the implement role: a local run's
   # review and close-out route through .tick/runners.toml exactly as written,
   # and the claude CLI appears locally only in those cells and on implement's
   # frontier rung. Guard:
   # TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells and
   # TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness
   # (internal/reconcile/routing_check_test.go:288, :350).
   ```

3. Ran the whole-repo gate. The first `make gate` run reported FAIL; two
   follow-up runs exit 0, including a full `-count=1` uncached pass over all
   47 packages. I cannot attribute the first failure to a named test (its
   output was lost to my own output truncation and nothing failed again), so
   per this repo's rule — the run dedupes findings on the test id — I file no
   finding for it and flag it here for a human eye.

## Why this needs a person or the run's own writer

- The routing behaviour is already enforced by the two guard tests above;
  nothing in this tick is code. It is one paragraph of prose in a file whose
  only writers are the orchestrator and the operator.
- The tracker-edit channel a worker can trigger (`internal/reconcile/
  tracker_edits.go`, `subprocess/tracker_edits.go`) carries tick-record prose
  fields only (`acceptance_criteria`, `description`, `notes`) — it has no verb
  for a runner-table header paragraph. Appending the text to this tick's
  `notes` would not be a substitute: it would satisfy the tick's reader
  without satisfying its deliverable, which the record names as the file
  header. And a BLOCKED answer carrying a tracker-edit gets applied and the
  tick closed (tick l89's rule), which would close 96s over a note instead of
  the file — so I deliberately propose no tracker edit.
- Attempting the edit anyway is a recorded boundary attempt, not a delivery:
  the hook refuses the commit and logs it, and a refused branch risks the
  real work on it.

## What the next attempt (or the operator) should do

- If the run's own writer can touch `.tick/runners.local.toml` outside a
  worker attempt, apply the paragraph above verbatim — it is the whole tick.
- Otherwise this waits for the operator: one paragraph, one paste, then close
  96s. The guards stay green either way; nothing else on this branch changes.

## What I ran

- `go test ./internal/reconcile/ -run
  'TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells|TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness'
  -count=1 -timeout 5m` → ok 0.882s
- `make gate` → FAIL once (unattributed, never reproduced), then exit 0 twice
  (cached suite + full `-count=1` pass, 47 packages ok)
- `git status` → clean tree except this report file; no commits made

## What the next tick has to know

- Nothing on this branch moved from its base. The two guard tests above are
  the evidence that the behaviour the note documents is already enforced — do
  not re-verify by editing the file inside an attempt; that is the boundary.
- Found while checking why a non-read `tk` call reached the real binary (the
  second finding): this container's tk boundary shim is disarmed because its
  `checkout` companion holds git's error text instead of a path.

```findings v2
[
  {
    "kind": "defect",
    "title": "Tick 96s's deliverable is writable by no actor a run can dispatch",
    "severity": "medium",
    "body": "Tick 96s's whole deliverable is a comment paragraph in .tick/runners.local.toml's header. Workers are refused it twice over (the container's pre-commit hook refuses any staged .tick/ path, image/worker.sh's install_boundary_guard, tick dxk; the cloud collect reads any .tick/ diff as a violation, cloudflare/src/worker-collect.ts:313,530) — the wall open ticks 9sy and 26g already carry — but this file adds a lock beyond both: it is not in internal/exec/subprocess/report.go's exemptFromBoundary (only config.md, runners.toml, learnings.md are), so even honoring the exemption list would not unblock it. The run's own writer has no verb for it either: the tracker-edit channel a worker can trigger carries tick-record prose fields only (subprocess/tracker_edits.go), so the one remaining writer is a person. Every dispatched attempt of this tick can only end BLOCKED, costing two more attempts up the ladder before the hold; the note is composed and ready to paste in this attempt's report, which is the 30-second resolution.",
    "evidence": "internal/exec/subprocess/report.go:190-194 (exemptFromBoundary), .git/hooks/pre-commit, cloudflare/src/worker-collect.ts:313 and :530, internal/reconcile/tracker_edits.go (TrackerEditFields)"
  },
  {
    "kind": "defect",
    "title": "Harness guard writes git's stderr into its checkout file, disarming the tk shim",
    "severity": "medium",
    "body": "harness/src/env/boundary-guard.ts resolves the guard's checkout companion with 'git rev-parse --path-format=absolute --git-common-dir || true' run through a helper that captures the merged output of the command, so when the resolution runs before the checkout exists (or from outside it) the failure's stderr line — 'fatal: not a git repository ...' — is written to the checkout file as if it were a path. The shim then can never match the live git dir, so its 'pass through anything against a different tracker' clause fires for EVERY call and the guard passes reads and WRITES alike to the real tk, silently: Layer 1 of the boundary is disarmed for the whole container life while looking installed. Observed in this run's container: /work/repo.guard/checkout holds exactly that fatal line (the image's own installer at image/worker.sh:468 truncates the file on failure and redirects stderr, so this one came from the harness path), the ledger is empty, and a non-read 'tk --json' printed the real tk's usage. The pre-commit hook and the collect's boundary check still hold, so this is defense-in-depth lost, not a write path — but the guard's refusal (and its ledger, which is what tells a person the agent tried) is off exactly where it exists to fire.",
    "evidence": "harness/src/env/boundary-guard.ts:171-176 (the checkout resolution and its writeTextFile), harness/src/env/factory-sandbox.ts:798-800 (onOutput accumulates the door's merged output stream); observed live: /work/repo.guard/checkout content and empty /work/repo.guard/attempts after a non-read invocation"
  }
]
```

STATUS: BLOCKED — the deliverable is one comment paragraph in .tick/runners.local.toml, a .tick/ path no worker may commit (refused by the pre-commit hook and collect alike) and no tracker-edit verb covers; the paragraph is composed verbatim in this report and needs the operator's .tick authority to land, after which the tick closes with no code change.
