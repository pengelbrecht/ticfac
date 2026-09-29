-- How far one JOB's worker output has been copied into the run's log stream
-- (tick 86y) — so the door's state route can copy what the worker printed
-- since the last look, on every look, and drain it to the end before a
-- settled container is reclaimed.
--
-- The door drained a worker's output only inside its confirm window, which
-- closes at the first byte of output: epic hn6's r5i try 2 left five lines
-- ending at "checked out <sha>", died five minutes later, and said nothing
-- about why. With the settled container reclaimed at its first terminal
-- observation (0019), the unread tail was destroyed rather than just unread.
--
-- process_id: the work process the cursor belongs to (a fresh boot under the
--   same identity is a new process, and starts a new cursor).
-- output_offset: how much of that process's output is already in R2.
-- epoch/seq: where the next segment goes in the tick's stream.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS sandbox_job_log (
  run_id        TEXT NOT NULL,
  tick_id       TEXT NOT NULL,
  attempt       INTEGER NOT NULL,
  job           TEXT NOT NULL DEFAULT '',
  process_id    TEXT NOT NULL,
  output_offset INTEGER NOT NULL,
  epoch         INTEGER NOT NULL,
  seq           INTEGER NOT NULL,
  PRIMARY KEY (run_id, tick_id, attempt, job)
);
