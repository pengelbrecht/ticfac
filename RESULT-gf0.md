<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-6-resolve-6-193cc47a/gf0`, base `2eb7a00224b6f977d905819ff9a5edde03ae428d`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict: gf0's per-hold-kind watch hold alert into epic/hn6

The branch is `tick/hn6/attempt-6-resolve-6-193cc47a/gf0`: the conflicted merge
of `ticfac/run-run_f44b6ad8909b495cbd3bae4ee7cf229c/tick-gf0/attempt-6` into
`epic/hn6`, left unresolved. One file carried conflict markers:

- `internal/cli/watch.go` — the `run_held` alert of `ticfac watch`'s stream
  path, one hunk inside the `print` callback.

Everything else (gf0's `statusmodel.HoldReason`/`HoldClearingCommand`, the
model and pipeline call sites, both sides' tests) merged clean, so the tree
already held the union everywhere except this one block.

## What the conflict was

Two intents over one alert:

- **gf0 (mine)** — "Watch's settle alert spells a dash attempt for a
  never-dispatched tick": the clearing command a hold alert names is decided
  per hold KIND, through the shared `statusmodel.HoldClearingCommand` /
  `HoldReason` the model's needs-you reads. A hold that fires before the
  tick's first dispatch (`claim_width`, `foreign_claim`,
  `absorption_depth_exceeded`) carries a NULL attempt, so the old inline
  fallback spelled `ticfac settle <epic> <tick> - --release "<who>"` — a
  command the settle CLI refuses, and the wrong verb anyway (a foreign claim
  clears when the holder's tick closes, not by a release). Each names the
  command that actually moves it on; a hold the closed set does not know
  names no command at all, never a wrong one a person copies.
- **The epic side** — the already-merged ticks the hunk's own comments name:
  **q8m** (the triage is addressed to the run whose store carries the drafts —
  the line's own run id, not the bare command's `epic-<epic-id>` default) and
  **ulw/qxj** (the settle release names `--run-id` whenever the run's id is
  NOT the epic spelling, via `SettleCommandForCurrentRun`), with the inline
  dash fallback still standing for a null attempt.

## The resolution — a union

I took gf0's switch, because it already CARRIES the epic side's intent rather
than replacing it: `HoldClearingCommand(epicID, host, storeRunID, runID, event)`
takes the line's own run id as the triage's store address (q8m) and routes
every attempt hold through `SettleCommandForCurrentRun` →
`reconcile.SettleReleaseCommand` (qxj's `--run-id` rule, read rather than
mirrored). The merged tests — which came through as the union's expectations —
pin exactly this: cloud triage `--run-id <run_<hex>>`, cloud settle
`--run-id <run_<hex>> --release`, `ticfac run-epic <epic>` (never settle) for
width and foreign-claim holds, triage plus the `--absorption-depth` escape
for the bound, and NO command for an unrecognised hold. The `finding_untriaged`
message text is byte-identical on both sides.

- **One adaptation**: gf0's hunk spelled the epic id as a variable `epicID`;
  the merged tree spells it as the lazy `epicOf()` closure (ticks gtk/fub —
  read when a command is spelled, not when the watch starts, because the
  checkpoint may not exist yet). The union calls `epicOf()`.
- **One deliberate drop**: the epic side's inline dash-settle fallback for a
  null attempt. That is the bug gf0 exists to remove — it subsumes into the
  per-kind commands, and the merged tests pin its absence. Not a loss of epic
  intent: the null-attempt case now names the command that actually clears it.

Nothing was irreconcilable: no side's intent was chosen against the other's.

## Verification

All run in this worktree, in the foreground, all green:

- `go vet ./internal/cli/ ./internal/statusmodel/`
- `go test -timeout 10m -run 'TestWatch(HoldAlert|StreamReadsTheCheckpoint|SurfacesARunThatEndedHolding)' ./internal/cli/`
- `go test -timeout 20m ./internal/cli/ ./internal/statusmodel/` (full packages)
- `make gate` (gofmt, `go vet ./...`, the short suite across the repository) —
  exit 0, run twice
- No conflict markers remain: a repo-wide grep for
  `^(<<<<<<<|=======|>>>>>>>)` finds nothing outside `.git`

Related work deliberately NOT folded in: gf0 try-1's finding was absorbed as
open tick **quz** (`land_review_not_ready` names a release that does not clear
it — the resume is arguably the right clearing command there too). quz has no
merged work in this conflict, and changing `HoldClearingCommand`'s contract
for land-review holds is its own tick's decision, so it stays untouched here.

## Findings

```findings v2
[
  {
    "kind": "defect",
    "title": "The resolve-conflict snapshot's ours-side hunk cannot compile on its own",
    "severity": "low",
    "body": "The conflicted worktree this job resolved was committed as one root snapshot with no reachable parents, and the ours-side (epic/hn6) hunk it presents for internal/cli/watch.go is internally inconsistent with the common text it is joined to: it reads an `attempt` variable that no line of the presented file defines (gf0's side removed the dash-attempt variable, and that removal sits in the snapshot's common region), it calls strconv.Atoi while the file's import block has no strconv, and it computes a `settle` command it never prints — the else branch's release Fprintf is absent entirely. The epic's real file must differ (its gate was green when the merge was cut). The consequence for this job class: a resolver that treats each side's literal text as complete, or that reaches for 'keep ours' as the safe default, produces a tree that cannot compile and has no test run to catch it before the gate. Resolution must be against intent read from the tick records, with the hunk text as evidence only. Worth knowing whenever the resolve-conflict snapshot is built by squashing the merge into a root commit.",
    "evidence": "worktree commit 2eb7a00 internal/cli/watch.go lines 357-434 at the conflict markers: ours hunk references `attempt` (undefined above), `strconv.Atoi` (unimported), assigns `settle` twice with no use, and the hunk ends at `settle = statusmodel.SettleCommandForCurrentRun(...)` directly followed by the shared three closing braces; compare internal/cli/watch.go:343-356 (common region, no attempt variable) and the import block lines 3-16"
  },
  {
    "kind": "defect",
    "title": "Wave collision: the watch hold-alert block had three owners inside one window",
    "severity": "low",
    "body": "gf0's own record says its fix is 'a per-hold-kind decision, not the --run-id spelling this branch already carries' — gf0 was cut knowing qxj's work, and its design routes through the shared helpers qxj/ulw/q8m built, so the intents never conflicted. What forced the textual conflict was ownership churn on one block after gf0's cut: the epic landed the gtk/fub epicOf() lazy-read refactor of the very lines gf0's hunk reads the epic id from (gf0's hunk still references the `epicID` variable that refactor removed), beside qxj's change to the same alert. The union was cheap here only because gf0's branch already carried the epic's addressing work; a worse pairing would have needed real reconciliation against unreadable bases. One owner per surface block per wave — or dispatching gf0 after the epicOf refactor closed — would have made this merge clean.",
    "evidence": ".tick/issues/gf0.json description ('not the --run-id spelling this branch already carries'); internal/cli/watch.go:296-303 epicOf closure attributed to ticks gtk/fub in the merged common region vs the theirs hunk's `HoldClearingCommand(epicID, ...)` at old line 401; .tick/issues/qxj.json, ulw.json, q8m.json (all closed, parent hn6)"
  }
]
```

## Commits

- `7011f2f` — resolve the watch hold-alert conflict: gf0's per-hold-kind
  decision, carrying the epic side's addressing (`internal/cli/watch.go`,
  source only; no build output, nothing under runs/)

STATUS: DONE
