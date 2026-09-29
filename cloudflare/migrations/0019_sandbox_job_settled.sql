-- The terminal state one JOB's worker container settled in — so the door can
-- answer for a settled job without addressing its container, and reclaim the
-- container the moment it settles (epic hn6's second cloud run).
--
-- The door's state route read every job by listing its container's
-- processes. Through the SDK, ANY call on a container that is not running
-- starts it; so a status read of a job whose container was never booted
-- cold-booted one, and a settled worker's container stayed up — it idles out
-- only after SANDBOX_SLEEP_AFTER, and every status poll renewed that. With
-- the orchestrator and two settled workers holding all of max_instances,
-- attempt 3's status read waited for a slot until the orchestrator's client
-- gave up. The first terminal observation is now recorded here and the
-- container destroyed; later reads answer from this row.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS sandbox_job_settled (
  run_id    TEXT NOT NULL,
  tick_id   TEXT NOT NULL,
  attempt   INTEGER NOT NULL,
  job       TEXT NOT NULL DEFAULT '',
  state     TEXT NOT NULL,
  exit_code INTEGER,
  at        TEXT NOT NULL,
  PRIMARY KEY (run_id, tick_id, attempt, job)
);
