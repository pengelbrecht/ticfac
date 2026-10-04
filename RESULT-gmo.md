# gmo — the watch dashboard shows the epic's state, not only the latest run's

Epic hn6, tick gmo. The operator screenshot (2026-10-01) caught the defect's
shape: after many runs had closed 13 of hn6's ticks, `ticfac watch hn6`
answered for the newest cloud run — which failed at orchestrator boot before
touching anything — and showed 0/11 ticks, every row with attempts 0 and
empty tier/pipeline/time. The dashboard was reading ONE run's records, and
for a cloud run it was reading them under the wrong run id besides. This
attempt makes the dashboard answer for the epic: tick rows and the progress
bar derive from the epic's tracker state on the integration branch plus
every run's records for it; the run section (alive, workers, cost, feed)
stays the newest run's alone.

## What changed

**1. The model merges every run's records (`internal/statusmodel/epic.go`,
new).** `Sources` gains `PriorRecords` — every earlier run's records for the
same epic, oldest first. The merge's rules, stated once and enforced in one
place:

- A tick's STATE is the newest non-"ready" row any run checkpointed for it.
  A fresh run seeds its plan "ready" before it settles the tracker's answer
  (the exact shape of the screenshot: the failed run's checkpoint carried
  "ready" rows for ticks earlier runs had closed), so a "ready" row is not
  evidence of openness — the tracker's own closed status backs it.
- A tick's ROW — dispatch markers, gate evidence, provenance, tries, the
  tier the row shows — is the LAST run that has records for it, never a
  cross-run union: attempt numbers are per run, so the same (tick, attempt)
  key names different dispatches in different runs (verified in hn6's real
  records: lkq carries attempt 1 in run_aaa and attempt 2 in run_bbb).
- Absorptions and findings accumulate across runs, deduplicated by content
  key with the earliest record kept (a finding is one record; a later run's
  re-promotion is the duplicate, not a newer truth).
- Everything else — workers, cost, waits, gates, feed, liveness — stays the
  newest run's alone. The cost line counts the newest run's dispatches, not
  the epic's whole history.

**2. The tracker is read from the integration branch
(`internal/cli/status_tracker.go`, new).** The epic's tracker state lives on
`epic/<id>` on origin — where the run's own durable tracker writes land —
not in the checkout's working tree, which an operator watching a cloud run
has on main. From main, every tick the integration branch closed still read
open. The branch's `.tick/` is materialized through `git archive` into a
private temp dir (no worktree registered, no existing ref moved; the fetch
targets a per-process `refs/ticfac/peek/status/<pid>-<seq>/<epic>` ref, the
store's own never-FETCH_HEAD rule) and tk reads there; the checkout's own
tree is the fallback when origin has no branch or the read fails anywhere.
The graph is read with closed tasks included (`tk graph --all`, a new
`Client.GraphAll` in internal/tk that appends the CLI's own documented flag
after the manifest argv and validates the response against the same pinned
schema), and the record fields the graph endpoint omits — notes,
closed_at, closed_reason, which the duplicate and duration derivations read
— are enriched from the same branch's `tk list --all --json`.

**3. The run's records are read under the id the surface names
(`statusRecords`).** The container execs `ticfac run-epic --run-id
$TICKS_RUN_ID` (the factory's `run_<hex>`, since hn0), so a cloud run's
records land under the factory run id — but the reader tick tem cut reads
under `epic-<epic-id>`, so a real cloud run's records came back EMPTY, which
is the second half of the screenshot's empty rows. `statusRecords` now tries
the addressed run id first and the epic spelling second (tem's era), and —
in the same single fetch — gathers every sibling run's records for the
epic, oldest first, from the store's fetched view (a new
`runstate.Store.List` and `statusmodel.RecordsFromStoreRun`, sharing the
same record-source walk `RecordsFromDir` uses so the two readers cannot
drift).

**4. Duplicates are counted out and shown dimmed.** `Tick` gains
`duplicate_of` — the dedup writer's own record, parsed from the tracker's
note ("closed as a duplicate of <id>") or a hand-closed closed_reason. A
duplicate is excluded from the progress counts (`total`/`closed`/`open`),
because a duplicate is not work the epic owes; its row still stands (rows
never move), rendered dim with "duplicate of <id>" in the WHAT column, and
the drill-in says the work is the named tick's. The phone page renders the
same words (A5's one-model-two-renderers rule).

**5. Contract bundle re-cut to 1.4.0 (MINOR).** `contracts/status-model.json`
gains `duplicate_of` (required-and-null, every golden updated at null), a
`checked_beyond_schema` rule for the real-ticks counting, a CHANGELOG entry,
and the cloudflare pin bump — all in this one commit.

Two smaller derivations follow the merge: the elapsed countdown only
measures a live attempt (an earlier run's last dispatch is history — a
frozen state must not count seconds to now), and a refusal the newest run
did not leave names the resume command as its next step ("nothing of that
run is working — ticfac run <epic> [--cloud] takes it up"), because nothing
of a dead run will retry anything. The header's identity line now says
`failed`/`cancelled` instead of `not alive` when the lifecycle carries the
terminal word — the run header says what the run said it ended as.

## What was run

- Model suite with the new across-runs fixture
  (`internal/statusmodel/build_epic_test.go`): two earlier runs closed
  ticks, one re-closed a tick at a different tier (the last run's
  provenance wins), a held tick parked by an earlier run, a duplicate
  promotion, and a newest run failed at boot with fresh "ready" rows —
  progress 3-of-5 real closed, the closed rows carrying the last run's
  tier/time/attempts, the held tick held, the run header failed, duplicates
  excluded, ETA from all measured closes. `go test ./internal/statusmodel/
  ./internal/cli/ ./internal/tk/ ./internal/gitbin/ ./internal/runstate/
  ./internal/contracts/...` all green.
- Renderer: `watch_dashboard_epic_state.txt` golden pinned at width 120 over
  the same fixture's model, plus the dimmed-duplicate and not-dimmed-
  neighbour assertions; the existing dashboard goldens unchanged (no
  duplicate in the contract fixture).
- Gathering: prior-records ordering and the other-epic exclusion
  (`TestStatusRecordsCarriesEveryRunsRecords`), the wiring pin
  (`TestStatusWiringPassesPriorRecords`), the integration-branch tracker
  reader including closed tasks and enrichment with the checkout fallback
  (`TestTheIntegrationBranchTrackerAnswers`,
  `TestEpicGraphPrefersTheIntegrationBranch`), tk's GraphAll argv pin.
- `make gate` green (gofmt, vet, short suite, whole-repo guards — its
  transport-bound guard caught the new git reads and they carry
  `gitbin.TransportEnv` now).
- `pnpm test` in cloudflare/ green (1681 tests), including the new
  duplicate-row rendering test.
- Real-data verification against this repository's own origin (read-only):
  the hn6 integration branch's tracker reads 26 tick rows across 3 waves,
  3 duplicates detected (log→oro, qrl→yjq, dvv→mk5), progress 19/23 real
  closed, 14 prior runs' records gathered, exactly the tracker's own
  arithmetic.

## What the next tick has to know

- The merge rules live in `internal/statusmodel/epic.go` and are the single
  authority for "which run's record a row reads"; `buildWaves`,
  `decorateTicks` and `buildRemaining` consume the merged view. A new
  per-tick derivation must ask the merged view, not `Records`, or it will
  silently read only the newest run's half of the epic.
- `statusRecords` returns `(records, prior, error)` now. The addressed run
  id is tried first, the `epic-<id>` spelling second — tick tem's test
  still passes through the fallback, and a future reader should keep both
  spellings until the older container layout is provably gone.
- The bundle is 1.4.0; `cloudflare/contracts.pin.json` is bumped in the
  same commit, so a fold must take both halves plus
  `contracts/CHANGELOG.md` together or the verifiers refuse.
- Duplicate detection reads free text (the dedup writer's note or a
  hand-closed closed_reason) with the tracker's pinned id alphabet (3-4
  lowercase alphanumerics) as the pointer check. A tracker-side
  duplicate_of field would be more robust — filed below.

```findings v2
[
  {
    "kind": "proposal",
    "title": "An earlier run's hold never reaches needs-you across runs",
    "severity": "medium",
    "body": "The epic-across-runs merge reads prior runs' RECORDS but not their FEEDS, so a hold an earlier run left — an attempt struck out for a person, untriaged findings nobody moved — shows in the tick row's state (rejected, the work stage crossed) but never as the model's held-for-person wait or attention entry: the newest run failed at boot, its own feed holds no run_held line, and 'needs you' answers nothing while a person's decision is actually standing. Reading each prior run's feed (local .ticfac/logs, or the factory's events route per run id for cloud runs) would let buildWaits see those holds the same way it sees the newest run's.",
    "breaks": {"item": "A1", "check": "go test -short -timeout 20m -run TestBuildDerivesTheEpicAcrossRuns ./internal/statusmodel/"},
    "evidence": "internal/statusmodel/build.go buildWaits reads src.Feed only (the newest run's); internal/statusmodel/build_epic_test.go's t3/at3 is rejected with a person's release still owed and model.Attention answers empty"
  },
  {
    "kind": "contract-change",
    "title": "tk graph's --all flag is outside the pinned manifest's argv",
    "severity": "low",
    "body": "The pinned tk-json-manifest pins graph's argv as [graph, <epic-id>, --json], but the epic dashboard needs the CLOSED tasks and reads them through `tk graph <epic> --json --all`, appended after the manifest argv in internal/tk Client.GraphAll (the response validates against the same pinned schema — status is a free string). The manifest should name the flag and state that closed tasks ride in the waves (and a tracker-side duplicate_of field would make the duplicate detection structural rather than free-text parsing of the dedup note). Both are ticks-repository contract changes; this repo cannot cut them.",
    "target": "pengelbrecht/ticks",
    "evidence": "contracts/tk-json-manifest.json commands[graph].argv; internal/tk/tk.go invokeJSONArgs and TestGraphAllAppendsAllAfterTheManifestArgv; internal/statusmodel/epic.go duplicateOf parses notes free text"
  }
]
```

STATUS: DONE
