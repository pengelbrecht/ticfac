-- The notification memory for the phone page's ALERT half (ticfac tick i1r):
-- one row per (run, stop), the thing that makes a Telegram message
-- "deduplicated per stop, not per poll".
--
-- A local run pushes a snapshot every 30 seconds, and the same stop is present
-- in many consecutive snapshots. An evaluator that compared "did the picture
-- change" would page on every push; an evaluator that compared "which stops
-- are OPEN" pages once per stop episode and never again:
--
--  - A stop APPEARS -> the row is inserted with `notified_at` NULL. The send
--    marks `notified_at`; a row with `notified_at` set is never re-sent, no
--    matter how many more snapshots carry the same stop.
--  - A stop CLEARS -> `cleared_at` is set. A stop that re-appears after
--    clearing is a NEW episode: the row is reset (`notified_at` NULL,
--    `cleared_at` NULL) and the operator is told again — because a stop that
--    came back is news, not noise.
--  - A send that could not be delivered (Telegram unconfigured, the API
--    refused) leaves `notified_at` NULL beside a non-null `opened_at`, and the
--    NEXT evaluation retries — the same shape `loop_digest`'s `sent_at` NULL
--    uses for a message that was built and not delivered.
--
-- The rate limit rides beside the dedup rather than replacing it: sends for
-- one run are bounded by a minimum gap, so a run that trips three stops in
-- the same second still reads as one conversation, with each stop still
-- named once. A deferred send simply waits for the next evaluation.
--
-- Cloud runs write the same rows from the Run Workflow's finalize (epic done,
-- run failed), so a retried Workflow step cannot page twice for one ending.
CREATE TABLE IF NOT EXISTS status_alerts (
  run_id  TEXT NOT NULL,
  -- The stop's identity within the run: "person:<kind>:<digest of what>" for
  -- a needs-a-person stop, "terminal:done" / "terminal:failed:<digest>" for an
  -- ending. Stable across pushes of the same stop, different across stops.
  stop_key   TEXT NOT NULL,
  -- "person", "done" or "failed" — the message's own word for itself.
  kind    TEXT NOT NULL,
  what     TEXT NOT NULL,
  clear_with TEXT,
  opened_at  TEXT NOT NULL,
  notified_at TEXT,
  cleared_at  TEXT,
  PRIMARY KEY (run_id, stop_key)
);

-- The rate-limit read: the newest send for one run.
CREATE INDEX IF NOT EXISTS idx_status_alerts_notified
  ON status_alerts (run_id, notified_at);
