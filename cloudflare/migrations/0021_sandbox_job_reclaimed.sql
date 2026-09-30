-- Each worker container the factory RECLAIMED because its run was over — at
-- the run's end (finalize) or by the hourly sweep — and whether it was asked
-- to stop and push first (hn6's cloud run, 2026-09-30).
--
-- The run run_8511bc66… failed with two worker containers still running:
-- nothing destroyed a run's workers when the run ended, so they held two of
-- the account's three container slots after it, and the door's capacity
-- count (sandbox-dispatch.ts) must know which booted containers are gone. A
-- job is holding a slot while it has a boot (0018) and neither a settlement
-- (0019) nor a row here.
--
-- reason: why it was reclaimed — `run_ended:<state>` from finalize, or
--   `run_not_live` from the sweep.
-- salvaged: 1 when a live work process was asked to stop and push first
--   (ticks-worker --cancel), 0 when nothing was running to ask.
-- detail: what the reclaim saw, for the person reading it.
--
-- A fresh boot under the identity clears its row (recordSandboxAttemptBoot):
-- the new container is holding a slot again.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS sandbox_job_reclaimed (
  run_id    TEXT NOT NULL,
  tick_id   TEXT NOT NULL,
  attempt   INTEGER NOT NULL,
  job       TEXT NOT NULL DEFAULT '',
  reason    TEXT NOT NULL,
  salvaged  INTEGER NOT NULL,
  detail    TEXT NOT NULL,
  at        TEXT NOT NULL,
  PRIMARY KEY (run_id, tick_id, attempt, job)
);
