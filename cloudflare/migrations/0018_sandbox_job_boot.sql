-- The model one JOB's worker container was booted on — keyed by the job, not
-- the attempt (the hn6 cloud-run stall).
--
-- sandbox_attempt_boot (0014) keyed a boot by run, tick and attempt: the
-- identity that named the container. But the reconciler runs several jobs
-- under ONE attempt number — the implement attempt, then a gate repair of it
-- and the repair's retries, a conflict resolution — and each is its own job
-- with its own container. Keyed by the attempt alone, a repair's boot would
-- overwrite the attempt's record and an adoption of either would read the
-- other's model. So the key grows the job's slot (sandbox-executor.ts
-- `attemptJobSlot`): '' for the attempt's own job — exactly the rows 0014
-- held, copied across so a container an earlier deployment booted is still
-- adoptable — and the slot for every other job.
--
-- sandbox_attempt_boot is left in place (not dropped) so a Worker still
-- running the previous release during a deploy reads and writes a table that
-- exists; nothing reads it once this release is live.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS sandbox_job_boot (
  run_id   TEXT NOT NULL,
  tick_id  TEXT NOT NULL,
  attempt  INTEGER NOT NULL,
  job      TEXT NOT NULL DEFAULT '',
  model    TEXT NOT NULL,
  at       TEXT NOT NULL,
  PRIMARY KEY (run_id, tick_id, attempt, job)
);

INSERT OR IGNORE INTO sandbox_job_boot (run_id, tick_id, attempt, job, model, "at")
  SELECT run_id, tick_id, attempt, '', model, "at" FROM sandbox_attempt_boot;
