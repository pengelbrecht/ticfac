-- The model one attempt's worker container was booted on (tick dyo).
--
-- The dispatch door boots a worker container on the model the dispatch's
-- profile resolved, and the handle it answers with names that model back —
-- so the record a caller keeps states the model that actually ran (tick a08).
-- The one start that could not name it truthfully was the ADOPTION: a second
-- start under the same identity that finds a live work process booted by an
-- earlier dispatch, which may have resolved a DIFFERENT model. Until this
-- table existed the door could only name the model the NEW request carried,
-- and the caller's check — the one that refuses a container running anything
-- but the model its dispatch resolved, and with it the Workers-AI-only rule
-- (tick nwn) — could never fire for an adopted attempt: the door was echoing
-- the question back as the answer.
--
-- So the boot is RECORDED, before the container is addressed, keyed by the
-- same identity that names the container — run, tick, attempt — and an
-- adoption reads the record of the boot that started the work process it is
-- about to adopt. That is the honest source on a substrate whose process
-- list cannot read a running process's environment: the door is the only
-- party that boots under an identity, so its own durable record of what it
-- commanded IS what the container is on. A live work process with no record
-- is a container an older deployment booted, and the door refuses to adopt
-- it rather than guess — a hold for a person, never a lie in a handle.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS sandbox_attempt_boot (
  run_id   TEXT NOT NULL,
  tick_id  TEXT NOT NULL,
  attempt  INTEGER NOT NULL,
  model    TEXT NOT NULL,
  at       TEXT NOT NULL,
  PRIMARY KEY (run_id, tick_id, attempt)
);
